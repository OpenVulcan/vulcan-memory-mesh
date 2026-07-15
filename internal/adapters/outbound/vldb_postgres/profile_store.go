// profile_store.go implements PostgreSQL-backed profile snapshots, manual instruction persistence, and lifecycle convergence for the combined runtime.
// profile_store.go 用于实现组合运行时基于 PostgreSQL 的画像快照、手工指令持久化和生命周期收敛逻辑。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// profileQueryer captures the subset of pgx query methods shared by the pool and transactions so profile helpers can stay reusable.
// profileQueryer 用于抽象连接池与事务共享的 pgx 查询子集，让画像辅助逻辑可以复用。
type profileQueryer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LoadProfileTargets loads the current durable user/project profile blobs so post-action can merge fresh profile evidence before persistence.
// LoadProfileTargets 用于加载当前长期 user/project 画像 Blob，让 post-action 在持久化前先合并新的画像证据。
func (r *profileRepository) LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProfileTargetsSnapshot{}, fmt.Errorf("postgres store is not initialized")
	}
	if session.UserID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	user, err := r.loadUserByID(ctx, session.UserID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	project, err := r.loadProjectByID(ctx, session.ProjectID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	return logicdomain.ProfileTargetsSnapshot{
		UserProfile:    strings.TrimSpace(user.Profile),
		ProjectProfile: strings.TrimSpace(project.Profile),
	}, nil
}

// LoadProfileReviewTargets loads the currently active and non-expired user/project profile nodes so post-action can review new candidates against factual atomic records.
// LoadProfileReviewTargets 用于加载当前活跃且未过期的 user/project 画像节点，让 post-action 可以基于原子事实记录评审新候选。
func (r *profileRepository) LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("postgres store is not initialized")
	}
	if session.UserID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	now := time.Now().UTC()
	userNodes, err := r.loadActiveProfileNodes(ctx, r.shared.pool, logicdomain.ProfileTypeUser, session.UserID, now, 0)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query postgres user profile review targets: %w", err)
	}
	projectNodes, err := r.loadActiveProfileNodes(ctx, r.shared.pool, logicdomain.ProfileTypeProject, session.ProjectID, now, 0)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query postgres project profile review targets: %w", err)
	}
	return logicdomain.ProfileReviewTargetsSnapshot{
		UserNodes:    userNodes,
		ProjectNodes: projectNodes,
	}, nil
}

// ListActiveProfileNodes returns only the current active nodes for one resolved target in a deterministic order, while respecting the caller's explicit limit when provided.
// ListActiveProfileNodes 用于按确定性顺序返回某个已解析目标当前 active 的画像节点，并在调用方显式提供 limit 时尊重该限制。
func (r *profileRepository) ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return nil, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return nil, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	now := time.Now().UTC()
	rows, err := r.queryProfileNodes(ctx, r.shared.pool, target.ProfileType, target.BindID, now, limit, true)
	if err != nil {
		return nil, fmt.Errorf("query postgres active profile nodes: %w", err)
	}
	records := make([]logicdomain.ProfileNodeRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, row.toRecord())
	}
	return records, nil
}

// LoadRenderedProfile returns the durable scope-level rendered profile body stored on the resolved target row.
// LoadRenderedProfile 用于返回已解析目标行上持久化的 scope 级渲染画像正文。
func (r *profileRepository) LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return "", fmt.Errorf("postgres store is not initialized")
	}
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return "", logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return "", logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	return r.loadRenderedProfileByTarget(ctx, r.shared.pool, target.ProfileType, target.BindID)
}

// CreateProfileInstruction inserts one pending manual profile instruction so later review results can reference a durable instruction id.
// CreateProfileInstruction 用于插入一条 pending 的手工画像指令，让后续评审结果能够引用稳定的 instruction id。
func (r *profileRepository) CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	if !logicdomain.ValidProfileType(record.ProfileType) {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
	if record.BindID == 0 {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if strings.TrimSpace(record.Instruction) == "" {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "instruction", Message: "is required"}
	}
	if !logicdomain.ValidProfileInstructionStatus(record.Status) {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "status", Message: "must be one supported profile instruction status"}
	}
	now := time.Now().UTC()
	sqlText := fmt.Sprintf(`
INSERT INTO %s (
	profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $7
)
RETURNING id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_at, updated_at
`, r.profileInstructionsTable())
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	var row profileInstructionScanRow
	if err := r.shared.pool.QueryRow(
		callCtx,
		strings.TrimSpace(sqlText),
		record.ProfileType,
		int64(record.BindID),
		strings.TrimSpace(record.Instruction),
		record.Status,
		strings.TrimSpace(record.ReviewResult),
		strings.TrimSpace(record.FailureReason),
		now,
	).Scan(
		&row.ID,
		&row.ProfileType,
		&row.BindID,
		&row.Instruction,
		&row.InstructionState,
		&row.ReviewResult,
		&row.FailureReason,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("insert postgres profile instruction: %w", err)
	}
	return row.toDomain(), nil
}

// FailProfileInstruction marks one pending manual profile instruction as failed and stores the failure reason for later debugging.
// FailProfileInstruction 用于把一条 pending 手工画像指令标记为失败，并保存失败原因，便于后续调试。
func (r *profileRepository) FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if instructionID == 0 {
		return logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}
	sqlText := fmt.Sprintf(`
UPDATE %s
SET instruction_status = $1,
    review_result_json = $2,
    failure_reason = $3,
    updated_at = $4
WHERE id = $5
  AND instruction_status = $6
`, r.profileInstructionsTable())
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	tag, err := r.shared.pool.Exec(callCtx, strings.TrimSpace(sqlText), logicdomain.ProfileInstructionStatusFailed, strings.TrimSpace(reviewResult), strings.TrimSpace(failureReason), time.Now().UTC(), int64(instructionID), logicdomain.ProfileInstructionStatusPending)
	if err != nil {
		return fmt.Errorf("mark postgres profile instruction failed: %w", err)
	}
	if err := requirePostgresProfileInstructionRowsAffected("mark postgres profile instruction failed", instructionID, tag.RowsAffected(), 1); err != nil {
		return err
	}
	return nil
}

// requirePostgresProfileInstructionRowsAffected rejects profile-instruction state drift when a known instruction transition did not touch its expected row.
// requirePostgresProfileInstructionRowsAffected 用于在已知画像指令状态迁移未命中预期行时拒绝状态漂移。
func requirePostgresProfileInstructionRowsAffected(action string, instructionID uint64, rowsAffected, expectedRows int64) error {
	if rowsAffected == expectedRows {
		return nil
	}
	return fmt.Errorf("%s for instruction %d affected %d rows, want %d", action, instructionID, rowsAffected, expectedRows)
}

// requirePostgresManualProfileRowsAffected rejects manual-profile transaction drift before the transaction commits.
// requirePostgresManualProfileRowsAffected 用于在手工画像事务提交前拒绝行数漂移。
func requirePostgresManualProfileRowsAffected(action string, rowsAffected, expectedRows int64) error {
	if rowsAffected == expectedRows {
		return nil
	}
	return fmt.Errorf("%s affected %d rows, want %d", action, rowsAffected, expectedRows)
}

// ApplyManualProfileInstruction persists the reviewed manual profile instruction inside one transaction so accepted nodes, retirements, instruction state, and rendered profile stay aligned.
// ApplyManualProfileInstruction 用于在单个事务里持久化手工画像评审结果，确保接纳节点、退役节点、指令状态和渲染画像保持一致。
func (r *profileRepository) ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("postgres store is not initialized")
	}
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if instruction.ID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}

	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("begin postgres manual profile tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	now := time.Now().UTC()
	accepted := make([]logicdomain.ProfileNodeRecord, 0, len(nodes))
	// Track nodes already retired by a replacement node so the standalone retire loop does not update the same row twice.
	// 记录已被替代节点退役的旧节点，避免独立 retire 循环再次更新同一行。
	supersededNodeIDs := make(map[uint64]struct{})
	for idx, node := range nodes {
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].content", idx), Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].status", idx), Message: "must be one supported profile status"}
		}
		if !logicdomain.ValidProfilePriority(node.Priority) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].priority", idx), Message: "must be one supported profile priority"}
		}
		if !logicdomain.ValidProfileLevel(node.ProfileLevel) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].profile_level", idx), Message: "must be one supported profile level"}
		}
		if !logicdomain.ValidProfileSourceKind(node.SourceKind) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].source_kind", idx), Message: "must be one supported profile source kind"}
		}
		if node.RefreshWeight < 0 {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: fmt.Sprintf("nodes[%d].refresh_weight", idx), Message: "must be >= 0"}
		}
		profileDate := strings.TrimSpace(node.ProfileDate)
		if profileDate == "" {
			profileDate = logicdomain.FormatDisplayDate(now)
		}

		insertSQL := fmt.Sprintf(`
INSERT INTO %s (
	turn_id, profile_type, bind_id, content, profile_status, priority, profile_level,
	level_reason, refresh_weight, source_kind, source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at
) VALUES (
	NULL, $1, $2, $3, $4, $5, $6,
	$7, $8, $9, $10, $11, $12, 0, $13, $14, $14
)
RETURNING id, turn_id, profile_type, bind_id, content, profile_status, priority, profile_level, level_reason, refresh_weight,
          source_kind, source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at
`, r.profileNodesTable())
		var row profileNodeScanRow
		if err := tx.QueryRow(
			callCtx,
			strings.TrimSpace(insertSQL),
			target.ProfileType,
			int64(target.BindID),
			strings.TrimSpace(node.Content),
			node.Status,
			node.Priority,
			node.ProfileLevel,
			strings.TrimSpace(node.LevelReason),
			node.RefreshWeight,
			node.SourceKind,
			int64(node.SourceID),
			strings.TrimSpace(node.StatusReason),
			nullableTime(node.ExpiresAt),
			profileDate,
			now,
		).Scan(
			&row.ID,
			&row.TurnID,
			&row.ProfileType,
			&row.BindID,
			&row.Content,
			&row.ProfileStatus,
			&row.Priority,
			&row.ProfileLevel,
			&row.LevelReason,
			&row.RefreshWeight,
			&row.SourceKind,
			&row.SourceID,
			&row.StatusReason,
			&row.ExpiresAt,
			&row.SupersededByID,
			&row.ProfileDate,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("insert postgres manual profile node %d: %w", idx, err)
		}
		inserted := row.toRecord()
		supersedeNodeIDs := storageutil.NormalizeUint64List(node.SupersedeNodeIDs)
		if len(supersedeNodeIDs) > 0 {
			updateSupersededSQL := fmt.Sprintf(`
UPDATE %s
SET profile_status = $1,
    superseded_by_id = $2,
    status_reason = $3,
    updated_at = $4
WHERE profile_status = $5
  AND id = ANY($6)
`, r.profileNodesTable())
			tag, err := tx.Exec(
				callCtx,
				strings.TrimSpace(updateSupersededSQL),
				logicdomain.ProfileStatusSuperseded,
				int64(inserted.ID),
				strings.TrimSpace(node.StatusReason),
				now,
				logicdomain.ProfileStatusActive,
				toInt64List(supersedeNodeIDs),
			)
			if err != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("supersede postgres manual profile nodes for %d: %w", inserted.ID, err)
			}
			if err := requirePostgresManualProfileRowsAffected(fmt.Sprintf("supersede postgres manual profile nodes for %d", inserted.ID), tag.RowsAffected(), int64(len(supersedeNodeIDs))); err != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, err
			}
			for _, nodeID := range supersedeNodeIDs {
				supersededNodeIDs[nodeID] = struct{}{}
			}
		}
		accepted = append(accepted, inserted)
	}

	for _, decision := range standalonePostgresProfileRetireDecisions(retired, supersededNodeIDs) {
		if decision.NodeID == 0 {
			continue
		}
		retireSQL := fmt.Sprintf(`
UPDATE %s
SET profile_status = $1,
    status_reason = $2,
    updated_at = $3
WHERE profile_status = $4
  AND id = $5
`, r.profileNodesTable())
		tag, err := tx.Exec(callCtx, strings.TrimSpace(retireSQL), logicdomain.ProfileStatusSuperseded, strings.TrimSpace(decision.Reason), now, logicdomain.ProfileStatusActive, int64(decision.NodeID))
		if err != nil {
			return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("retire postgres manual profile node %d: %w", decision.NodeID, err)
		}
		if err := requirePostgresManualProfileRowsAffected(fmt.Sprintf("retire postgres manual profile node %d", decision.NodeID), tag.RowsAffected(), 1); err != nil {
			return logicdomain.ManualProfileInstructionApplyResult{}, err
		}
	}

	updateInstructionSQL := fmt.Sprintf(`
UPDATE %s
SET instruction_status = $1,
    review_result_json = $2,
    failure_reason = '',
    updated_at = $3
WHERE id = $4
  AND instruction_status = $5
`, r.profileInstructionsTable())
	tag, err := tx.Exec(callCtx, strings.TrimSpace(updateInstructionSQL), logicdomain.ProfileInstructionStatusApplied, strings.TrimSpace(reviewResult), now, int64(instruction.ID), logicdomain.ProfileInstructionStatusPending)
	if err != nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("mark postgres manual profile instruction applied: %w", err)
	}
	if err := requirePostgresProfileInstructionRowsAffected("mark postgres manual profile instruction applied", instruction.ID, tag.RowsAffected(), 1); err != nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, err
	}
	if err := r.updateRenderedProfileTarget(callCtx, tx, target.ProfileType, target.BindID, renderedProfile, now); err != nil {
		return logicdomain.ManualProfileInstructionApplyResult{}, err
	}
	result := logicdomain.ManualProfileInstructionApplyResult{
		InstructionID: instruction.ID,
		AcceptedNodes: accepted,
		RetiredNodes:  retired,
	}
	if err := tx.Commit(callCtx); err != nil {
		return result, postgresManualProfileCommitOutcomeUncertainError(err)
	}
	return result, nil
}

// postgresManualProfileCommitOutcomeUncertainError marks manual-profile commits whose node, instruction, and rendered-profile writes may already be durable.
// postgresManualProfileCommitOutcomeUncertainError 用于标记手工画像提交失败，此时节点、指令状态和渲染画像写入可能已经持久化。
func postgresManualProfileCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("apply manual profile instruction", "commit postgres manual profile tx", err)
}

// standalonePostgresProfileRetireDecisions removes retire decisions already applied through a replacement-node supersede update.
// standalonePostgresProfileRetireDecisions 用于移除已经通过替代节点 supersede 更新落库的退役决策。
func standalonePostgresProfileRetireDecisions(retired []logicdomain.ProfileRetireDecision, supersededNodeIDs map[uint64]struct{}) []logicdomain.ProfileRetireDecision {
	if len(retired) == 0 || len(supersededNodeIDs) == 0 {
		return retired
	}
	standalone := make([]logicdomain.ProfileRetireDecision, 0, len(retired))
	for _, decision := range retired {
		if _, alreadySuperseded := supersededNodeIDs[decision.NodeID]; alreadySuperseded {
			continue
		}
		standalone = append(standalone, decision)
	}
	return standalone
}

// ConvergeExpiredProfileNodes marks due active profile nodes as expired and returns the affected targets together with their remaining renderable active nodes.
// ConvergeExpiredProfileNodes 用于把已到期的 active 画像节点收敛为 expired，并返回受影响目标及其剩余可渲染 active 节点。
func (r *profileRepository) ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if limit <= 0 {
		limit = 256
	}
	now := time.Now().UTC()
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres profile convergence tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	expiredSQL := fmt.Sprintf(`
SELECT p.id, p.turn_id, p.profile_type, p.bind_id, p.content, p.profile_status, p.priority, p.profile_level, p.level_reason, p.refresh_weight,
       p.source_kind, p.source_id, p.status_reason, p.expires_at, p.superseded_by_id, p.profile_date, COALESCE(t.created_at, p.created_at) AS profile_date_anchor_at, p.created_at, p.updated_at
FROM %s AS p
LEFT JOIN %s AS t ON t.id = p.turn_id
WHERE p.profile_status = $1
  AND p.expires_at IS NOT NULL
  AND p.expires_at <= $2
ORDER BY p.expires_at ASC, p.id ASC
LIMIT $3
`, r.profileNodesTable(), r.turnsTable())
	rows, err := tx.Query(callCtx, strings.TrimSpace(expiredSQL), logicdomain.ProfileStatusActive, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query postgres expired profile nodes: %w", err)
	}
	expiredRows, err := scanProfileNodeRows(rows)
	if err != nil {
		return nil, err
	}
	if len(expiredRows) == 0 {
		return nil, nil
	}

	expiredIDs := make([]uint64, 0, len(expiredRows))
	targetIndex := map[string]int{}
	targets := make([]logicdomain.ProfileRenderTargetSnapshot, 0)
	for _, row := range expiredRows {
		expiredIDs = append(expiredIDs, row.ID)
		key := fmt.Sprintf("%d:%d", row.ProfileType, row.BindID)
		if _, exists := targetIndex[key]; exists {
			continue
		}
		targetIndex[key] = len(targets)
		targets = append(targets, logicdomain.ProfileRenderTargetSnapshot{
			ProfileType: row.ProfileType,
			BindID:      row.BindID,
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ProfileType != targets[j].ProfileType {
			return targets[i].ProfileType < targets[j].ProfileType
		}
		return targets[i].BindID < targets[j].BindID
	})

	updateExpiredSQL := fmt.Sprintf(`
UPDATE %s
SET profile_status = $1,
    status_reason = $2,
    updated_at = $3
WHERE profile_status = $4
  AND id = ANY($5)
`, r.profileNodesTable())
	expireTag, err := tx.Exec(callCtx, strings.TrimSpace(updateExpiredSQL), logicdomain.ProfileStatusExpired, "expired by lifecycle convergence", now, logicdomain.ProfileStatusActive, toInt64List(expiredIDs))
	if err != nil {
		return nil, fmt.Errorf("mark postgres expired profile nodes: %w", err)
	}
	if err := requirePostgresManualProfileRowsAffected("expire postgres profile nodes by lifecycle convergence", expireTag.RowsAffected(), int64(len(expiredIDs))); err != nil {
		return nil, err
	}
	for idx := range targets {
		nodes, err := r.loadActiveProfileNodes(callCtx, tx, targets[idx].ProfileType, targets[idx].BindID, now, 0)
		if err != nil {
			return nil, fmt.Errorf("load postgres active profile nodes for render target: %w", err)
		}
		targets[idx].Nodes = nodes
	}
	if err := tx.Commit(callCtx); err != nil {
		return targets, postgresProfileConvergenceCommitOutcomeUncertainError(err)
	}
	return targets, nil
}

// postgresProfileConvergenceCommitOutcomeUncertainError marks lifecycle convergence commits whose expired-node status flips may already be durable.
// postgresProfileConvergenceCommitOutcomeUncertainError 用于标记画像生命周期收敛提交失败，此时过期节点状态切换可能已经持久化。
func postgresProfileConvergenceCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("converge expired profile nodes", "commit postgres profile convergence tx", err)
}

// ReplaceRenderedProfiles writes the already rendered scope-level profile blobs back into PostgreSQL after lifecycle convergence, manual instruction review, or batch review.
// ReplaceRenderedProfiles 用于在生命周期收敛、手工画像评审或批量评审之后，把已经渲染好的 scope 画像文本回写到 PostgreSQL。
func (r *profileRepository) ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if len(updates.UserProfiles) == 0 && len(updates.TeamProfiles) == 0 && len(updates.SpaceProfiles) == 0 && len(updates.ProjectProfiles) == 0 {
		return nil
	}
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres rendered-profile replace tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	now := time.Now().UTC()
	if err := r.replaceRenderedProfileBatch(callCtx, tx, logicdomain.ProfileTypeUser, updates.UserProfiles, now); err != nil {
		return err
	}
	if err := r.replaceRenderedProfileBatch(callCtx, tx, logicdomain.ProfileTypeTeam, updates.TeamProfiles, now); err != nil {
		return err
	}
	if err := r.replaceRenderedProfileBatch(callCtx, tx, logicdomain.ProfileTypeSpace, updates.SpaceProfiles, now); err != nil {
		return err
	}
	if err := r.replaceRenderedProfileBatch(callCtx, tx, logicdomain.ProfileTypeProject, updates.ProjectProfiles, now); err != nil {
		return err
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresRenderedProfileReplaceCommitOutcomeUncertainError(err)
	}
	return nil
}

// postgresRenderedProfileReplaceCommitOutcomeUncertainError marks rendered-profile replace commits whose scope blobs may already be durable.
// postgresRenderedProfileReplaceCommitOutcomeUncertainError 用于标记渲染画像替换提交失败，此时 scope 画像 Blob 可能已经持久化。
func postgresRenderedProfileReplaceCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("replace rendered profiles", "commit postgres rendered-profile replace tx", err)
}

// queryProfileNodes loads profile rows for one target and keeps the row order aligned with the calling workflow's needs.
// queryProfileNodes 用于按目标加载画像行，并保持返回顺序与调用场景一致。
func (r *profileRepository) queryProfileNodes(ctx context.Context, q profileQueryer, profileType int, bindID uint64, now time.Time, limit int, profileListOrder bool) ([]profileNodeScanRow, error) {
	orderBy := "COALESCE(t.created_at, p.created_at) ASC, p.id ASC"
	if profileListOrder {
		orderBy = "COALESCE(t.created_at, p.created_at) DESC, p.id ASC"
	}
	sqlText := fmt.Sprintf(`
SELECT p.id, p.turn_id, p.profile_type, p.bind_id, p.content, p.profile_status, p.priority, p.profile_level, p.level_reason, p.refresh_weight,
       p.source_kind, p.source_id, p.status_reason, p.expires_at, p.superseded_by_id, p.profile_date, COALESCE(t.created_at, p.created_at) AS profile_date_anchor_at, p.created_at, p.updated_at
FROM %s AS p
LEFT JOIN %s AS t ON t.id = p.turn_id
WHERE p.profile_type = $1
  AND p.bind_id = $2
  AND p.profile_status = $3
  AND (p.expires_at IS NULL OR p.expires_at > $4)
`, r.profileNodesTable(), r.turnsTable())
	args := []any{profileType, int64(bindID), logicdomain.ProfileStatusActive, now}
	if limit > 0 {
		sqlText += fmt.Sprintf("\nORDER BY %s\nLIMIT $5", orderBy)
		args = append(args, limit)
	} else {
		sqlText += fmt.Sprintf("\nORDER BY %s", orderBy)
	}
	rows, err := q.Query(ctx, strings.TrimSpace(sqlText), args...)
	if err != nil {
		return nil, err
	}
	return scanProfileNodeRows(rows)
}

// loadActiveProfileNodes converts active durable profile rows into the lightweight active-node model shared by reviewers and lifecycle convergence.
// loadActiveProfileNodes 用于把活跃画像行转换成评审器和生命周期收敛共享的轻量 active-node 模型。
func (r *profileRepository) loadActiveProfileNodes(ctx context.Context, q profileQueryer, profileType int, bindID uint64, now time.Time, limit int) ([]logicdomain.ProfileActiveNodeRecord, error) {
	rows, err := r.queryProfileNodes(ctx, q, profileType, bindID, now, limit, false)
	if err != nil {
		return nil, err
	}
	nodes := make([]logicdomain.ProfileActiveNodeRecord, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, row.toActiveNode())
	}
	return nodes, nil
}

// loadRenderedProfileByTarget reads the durable scope-level rendered profile blob after the caller resolves one profile target.
// loadRenderedProfileByTarget 用于在调用方解析完 profile target 后读取对应的长期 scope 级渲染画像 Blob。
func (r *profileRepository) loadRenderedProfileByTarget(ctx context.Context, q profileQueryer, profileType int, bindID uint64) (string, error) {
	table := ""
	switch profileType {
	case logicdomain.ProfileTypeUser:
		table = r.usersTable()
	case logicdomain.ProfileTypeTeam:
		table = r.teamsTable()
	case logicdomain.ProfileTypeSpace:
		table = r.spacesTable()
	case logicdomain.ProfileTypeProject:
		table = r.projectsTable()
	default:
		return "", logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
	sqlText := fmt.Sprintf(`SELECT profile FROM %s WHERE id = $1 LIMIT 1`, table)
	var profile string
	if err := q.QueryRow(ctx, sqlText, int64(bindID)).Scan(&profile); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("load postgres rendered profile: %w", err)
	}
	return strings.TrimSpace(profile), nil
}

// updateRenderedProfileTarget writes one rendered profile blob back to its durable scope row inside the current transaction.
// updateRenderedProfileTarget 用于在当前事务内把一份渲染画像文本写回到对应的长期 scope 行。
func (r *profileRepository) updateRenderedProfileTarget(ctx context.Context, q profileQueryer, profileType int, bindID uint64, renderedProfile string, updatedAt time.Time) error {
	table := ""
	switch profileType {
	case logicdomain.ProfileTypeUser:
		table = r.usersTable()
	case logicdomain.ProfileTypeTeam:
		table = r.teamsTable()
	case logicdomain.ProfileTypeSpace:
		table = r.spacesTable()
	case logicdomain.ProfileTypeProject:
		table = r.projectsTable()
	default:
		return logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
	sqlText := fmt.Sprintf(`UPDATE %s SET profile = $1, updated_at = $2 WHERE id = $3`, table)
	tag, err := q.Exec(ctx, sqlText, strings.TrimSpace(renderedProfile), updatedAt.UTC(), int64(bindID))
	if err != nil {
		return fmt.Errorf("update postgres rendered profile target: %w", err)
	}
	if err := requirePostgresManualProfileRowsAffected("update postgres rendered profile target", tag.RowsAffected(), 1); err != nil {
		return err
	}
	return nil
}

// replaceRenderedProfileBatch reuses the scope-specific updater for one binding-id map so rendered profile writes stay deterministic.
// replaceRenderedProfileBatch 用于对单个 scope 的 binding-id map 复用统一更新器，保证渲染画像写入保持确定性。
func (r *profileRepository) replaceRenderedProfileBatch(ctx context.Context, q profileQueryer, profileType int, profiles map[uint64]string, now time.Time) error {
	for _, bindID := range sortedProfileBindingIDs(profiles) {
		if err := r.updateRenderedProfileTarget(ctx, q, profileType, bindID, profiles[bindID], now); err != nil {
			return err
		}
	}
	return nil
}

// profileQueryContext derives one bounded query context so profile repository calls stay inside the configured runtime timeout.
// profileQueryContext 用于派生一个有界查询上下文，确保 profile repository 调用始终受运行时超时约束。
func (r *profileRepository) profileQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// profileQualifiedTable returns the fully-qualified table name for one profile table inside the configured schema.
// profileQualifiedTable 用于返回配置 schema 中某个画像表的完整限定表名。
func (r *profileRepository) profileQualifiedTable(name string) string {
	return fmt.Sprintf("%s.%s", quoteIdentifier(r.shared.cfg.Schema), quoteIdentifier(name))
}

// profileNodesTable returns the fully-qualified durable profile-node table name.
// profileNodesTable 用于返回长期画像节点表的完整限定名称。
func (r *profileRepository) profileNodesTable() string {
	return r.profileQualifiedTable("vmm_profile_nodes")
}

// profileInstructionsTable returns the fully-qualified manual profile-instruction table name.
// profileInstructionsTable 用于返回手工画像指令表的完整限定名称。
func (r *profileRepository) profileInstructionsTable() string {
	return r.profileQualifiedTable("vmm_profile_instructions")
}

// turnsTable returns the fully-qualified durable turn table name so profile queries can reuse source-turn timestamps as stable profile-date anchors.
// turnsTable 用于返回长期 turn 表的完整限定名称，让画像查询可以复用源 turn 时间作为稳定的 profile 日期锚点。
func (r *profileRepository) turnsTable() string {
	return r.profileQualifiedTable("vmm_turn_records")
}

// usersTable returns the fully-qualified durable user table name for profile review lookups.
// usersTable 用于返回画像评审查找所需的长期用户表完整限定名称。
func (r *profileRepository) usersTable() string {
	return r.profileQualifiedTable("vmm_users")
}

// teamsTable returns the fully-qualified durable team table name for profile review lookups.
// teamsTable 用于返回画像评审查找所需的长期 team 表完整限定名称。
func (r *profileRepository) teamsTable() string {
	return r.profileQualifiedTable("vmm_teams")
}

// spacesTable returns the fully-qualified durable space table name for profile review lookups.
// spacesTable 用于返回画像评审查找所需的长期 space 表完整限定名称。
func (r *profileRepository) spacesTable() string {
	return r.profileQualifiedTable("vmm_spaces")
}

// projectsTable returns the fully-qualified durable project table name for profile review lookups.
// projectsTable 用于返回画像评审查找所需的长期 project 表完整限定名称。
func (r *profileRepository) projectsTable() string {
	return r.profileQualifiedTable("vmm_projects")
}

// loadUserByID loads one user row by numeric id so profile targets can read the durable user profile blob.
// loadUserByID 用于按数字 id 加载用户行，让画像目标能够读取长期用户画像 Blob。
func (r *profileRepository) loadUserByID(ctx context.Context, userID uint64) (logicdomain.UserRecord, error) {
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	return loadUserByQueryer(callCtx, r.shared.pool, userID, r.usersTable(), "")
}

// loadProjectByID loads one project row by numeric id together with its Team/Space display names so profile targets can read the durable project profile blob.
// loadProjectByID 用于按数字 id 加载项目行及其 Team/Space 展示名称，让画像目标能够读取长期项目画像 Blob。
func (r *profileRepository) loadProjectByID(ctx context.Context, projectID uint64) (logicdomain.ProjectRecord, error) {
	callCtx, cancel := r.profileQueryContext(ctx)
	defer cancel()
	return loadProjectByQueryer(callCtx, r.shared.pool, projectID, r.projectsTable(), r.teamsTable(), r.spacesTable())
}

// scanProfileNodeRows decodes PostgreSQL profile rows into reusable scan structs and always closes the rows before returning.
// scanProfileNodeRows 用于把 PostgreSQL 画像结果行解码成可复用的扫描结构，并在返回前统一关闭 rows。
func scanProfileNodeRows(rows pgx.Rows) ([]profileNodeScanRow, error) {
	defer rows.Close()
	items := make([]profileNodeScanRow, 0)
	for rows.Next() {
		var row profileNodeScanRow
		if err := rows.Scan(
			&row.ID,
			&row.TurnID,
			&row.ProfileType,
			&row.BindID,
			&row.Content,
			&row.ProfileStatus,
			&row.Priority,
			&row.ProfileLevel,
			&row.LevelReason,
			&row.RefreshWeight,
			&row.SourceKind,
			&row.SourceID,
			&row.StatusReason,
			&row.ExpiresAt,
			&row.SupersededByID,
			&row.ProfileDate,
			&row.ProfileDateAnchorAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres profile node row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres profile node rows: %w", err)
	}
	return items, nil
}
