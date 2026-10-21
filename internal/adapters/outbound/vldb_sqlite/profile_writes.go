// SQLite profile instruction and lifecycle writes for profile workflows.
// SQLite 适配层画像工作流使用的指令和生命周期写入。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CreateProfileInstruction inserts one pending manual profile instruction so later review results can reference a durable instruction id.
// CreateProfileInstruction 用于插入一条 pending 的手工画像指令，让后续评审结果能够引用稳定的 instruction id。
func (s *Store) CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_profile_instructions")
	if err != nil {
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("allocate profile instruction id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	record.ID = nextID
	record.CreatedAt = now
	record.UpdatedAt = now
	if err := s.exec(ctx, `
INSERT INTO vmm_profile_instructions (
  id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
`, nextID, record.ProfileType, record.BindID, strings.TrimSpace(record.Instruction), record.Status, strings.TrimSpace(record.ReviewResult), strings.TrimSpace(record.FailureReason), nowMs, nowMs); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, ok, reconcileErr := s.reconcileProfileInstructionCreate(ctx, record)
			if reconcileErr == nil && ok {
				return recovered, nil
			}
			if reconcileErr != nil {
				return logicdomain.ProfileInstructionRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert profile instruction",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ProfileInstructionRecord{}, logicdomain.OutcomeUncertainError{
				Operation: "insert profile instruction",
				Message:   err.Error(),
			}
		}
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("insert profile instruction: %w", err)
	}
	return record, nil
}

// FailProfileInstruction marks one pending manual profile instruction as failed and stores the failure reason for later debugging.
// FailProfileInstruction 用于把一条 pending 手工画像指令标记为失败，并保存失败原因，便于后续调试。
func (s *Store) FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error {
	if instructionID == 0 {
		return logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	if err := s.execExpectRowsChanged(ctx, "mark profile instruction failed", 1, `
UPDATE vmm_profile_instructions
SET instruction_status = ?,
    review_result_json = ?,
    failure_reason = ?,
    updated_timestamp = ?
WHERE id = ?
  AND instruction_status = ?;
`, logicdomain.ProfileInstructionStatusFailed, strings.TrimSpace(reviewResult), strings.TrimSpace(failureReason), nowMs, instructionID, logicdomain.ProfileInstructionStatusPending); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionFailed(ctx, instructionID, failureReason, reviewResult, nowMs)
			if reconcileErr == nil && recovered {
				return nil
			}
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "mark profile instruction failed",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "mark profile instruction failed",
				Message:   err.Error(),
			}
		}
		return err
	}
	return nil
}

// ApplyManualProfileInstruction persists the reviewed manual profile instruction inside one serialized write section and
// reconciles "commit outcome uncertain" failures before giving up, so duplicate retries are less likely to create extra nodes.
// ApplyManualProfileInstruction 用于在单个串行写入区间内持久化手工画像评审结果，
// 并在遇到“提交结果不确定”错误时先做状态对账，从而降低重复重试制造额外节点的概率。
func (s *Store) ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if instruction.ID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	profileStartID := uint64(0)
	var err error
	if len(nodes) > 0 {
		profileStartID, err = s.nextNumericID(ctx, "vmm_profile_nodes")
		if err != nil {
			return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("allocate manual profile node id: %w", err)
		}
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	nowRFC3339 := now.Format(time.RFC3339Nano)

	accepted := make([]logicdomain.ProfileNodeRecord, 0, len(nodes))
	mutated := false
	// Track nodes already retired by supersede updates so returned retire decisions do not trigger duplicate SQLite writes.
	// 记录已通过 supersede 更新退役的节点，避免返回用退役决策再次触发重复 SQLite 写入。
	supersededNodeIDs := make(map[uint64]struct{})
	for idx, node := range nodes {
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].status", Message: "must be one supported profile status"}
		}
		if !logicdomain.ValidProfilePriority(node.Priority) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].priority", Message: "must be one supported profile priority"}
		}
		if !logicdomain.ValidProfileLevel(node.ProfileLevel) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].profile_level", Message: "must be one supported profile level"}
		}
		if !logicdomain.ValidProfileSourceKind(node.SourceKind) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].source_kind", Message: "must be one supported profile source kind"}
		}
		if node.RefreshWeight < 0 {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].refresh_weight", Message: "must be >= 0"}
		}
		if strings.TrimSpace(node.ProfileDate) == "" {
			node.ProfileDate = logicdomain.FormatDisplayDate(now)
		}
		insertedID := profileStartID + uint64(idx)
		acceptedRecord := logicdomain.ProfileNodeRecord{
			ID:            insertedID,
			TurnID:        0,
			ProfileType:   target.ProfileType,
			BindID:        target.BindID,
			Content:       strings.TrimSpace(node.Content),
			Status:        node.Status,
			Priority:      node.Priority,
			ProfileLevel:  node.ProfileLevel,
			LevelReason:   strings.TrimSpace(node.LevelReason),
			RefreshWeight: node.RefreshWeight,
			ProfileDate:   strings.TrimSpace(node.ProfileDate),
			SourceKind:    node.SourceKind,
			SourceID:      node.SourceID,
			StatusReason:  strings.TrimSpace(node.StatusReason),
			ExpiresAt:     node.ExpiresAt.UTC(),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		// Persist the new active node with typed params so reviewer text never becomes executable SQL.
		// 使用强类型参数持久化新的 active 节点，避免评审文本进入可执行 SQL。
		insertStatement := parameterizedProfileNodeInsertStatement(insertedID, nil, target.ProfileType, target.BindID, node, nowMs)
		if err := s.exec(ctx, insertStatement.SQL, insertStatement.Params...); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, ok, reconcileErr := s.reconcileManualProfileNodeInsert(ctx, target, node, insertedID)
				if reconcileErr == nil && ok {
					acceptedRecord = recovered
					mutated = true
				} else if reconcileErr != nil {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("insert manual profile node %d", insertedID),
						Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
					}
				} else {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("insert manual profile node %d", insertedID),
						Message:   err.Error(),
					}
				}
			} else {
				return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("insert manual profile node %d: %w", insertedID, err))
			}
		}
		mutated = true
		supersedeNodeIDs := storageutil.NormalizeUint64List(node.SupersedeNodeIDs)
		if supersedeStatement, ok := parameterizedProfileNodesSupersedeStatement(target.ProfileType, target.BindID, supersedeNodeIDs, insertedID, strings.TrimSpace(node.StatusReason), nowMs); ok {
			// Retire the superseded nodes in a separate execute call because SQLite's shared-connection batch path
			// can enter a resource-deadlock state when one script both inserts and updates the same hot table.
			// 单独执行 supersede 更新，因为 SQLite 的共享连接批量路径在同一脚本里同时插入并更新热点表时，
			// 可能进入 resource deadlock 状态。
			if err := s.execExpectRowsChanged(ctx, fmt.Sprintf("supersede manual profile nodes for %d", insertedID), int64(len(supersedeNodeIDs)), supersedeStatement.SQL, supersedeStatement.Params...); err != nil {
				if isSQLiteOutcomeUncertainError(err) {
					recovered, reconcileErr := s.reconcileProfileNodesSuperseded(ctx, supersedeNodeIDs, insertedID)
					if reconcileErr == nil && recovered {
						mutated = true
						for _, nodeID := range supersedeNodeIDs {
							supersededNodeIDs[nodeID] = struct{}{}
						}
						accepted = append(accepted, acceptedRecord)
						continue
					}
					if reconcileErr != nil {
						return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation: fmt.Sprintf("supersede manual profile nodes for %d", insertedID),
							Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
						}
					}
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("supersede manual profile nodes for %d", insertedID),
						Message:   err.Error(),
					}
				}
				return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("supersede manual profile nodes for %d: %w", insertedID, err))
			}
			mutated = true
			for _, nodeID := range supersedeNodeIDs {
				supersededNodeIDs[nodeID] = struct{}{}
			}
		}
		accepted = append(accepted, acceptedRecord)
	}
	for _, decision := range standaloneSQLiteProfileRetireDecisions(retired, supersededNodeIDs) {
		if decision.NodeID == 0 {
			continue
		}
		// Apply standalone retire decisions after inserts so reviewer-directed removals stay explicit and debuggable.
		// 在插入完成后再应用独立退役决策，让评审器给出的移除动作保持明确且可调试。
		retireStatement, ok := parameterizedSingleProfileNodeRetireStatement(decision.NodeID, decision.Reason, nowMs)
		if !ok {
			continue
		}
		if err := s.execExpectRowsChanged(ctx, fmt.Sprintf("retire manual profile node %d", decision.NodeID), 1, retireStatement.SQL, retireStatement.Params...); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, reconcileErr := s.reconcileProfileNodesRetired(ctx, []uint64{decision.NodeID})
				if reconcileErr == nil && recovered {
					mutated = true
					continue
				}
				if reconcileErr != nil {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("retire manual profile node %d", decision.NodeID),
						Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
					}
				}
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: fmt.Sprintf("retire manual profile node %d", decision.NodeID),
					Message:   err.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("retire manual profile node %d: %w", decision.NodeID, err))
		}
		mutated = true
	}

	updateProfileSQL := parameterizedProfileTargetUpdateSQL(target.ProfileType)
	if strings.TrimSpace(updateProfileSQL) == "" {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	// Refresh the rendered scope blob before marking the instruction as applied so a missing target row cannot leave a successful instruction status behind.
	// 先刷新 scope 画像 Blob，再把指令标记为 applied，避免目标行缺失时留下成功状态。
	if err := s.execExpectRowsChanged(ctx, "update rendered manual profile target", 1, updateProfileSQL, strings.TrimSpace(renderedProfile), nowRFC3339, target.BindID); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileRenderedProfileTarget(ctx, target.ProfileType, target.BindID, renderedProfile)
			if reconcileErr == nil && recovered {
				mutated = true
				goto markInstructionApplied
			}
			if reconcileErr != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "update rendered manual profile target",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
				Operation: "update rendered manual profile target",
				Message:   err.Error(),
			}
		}
		return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("update rendered manual profile target: %w", err))
	}
	mutated = true
markInstructionApplied:
	// Mark the instruction as applied only after every node mutation and the rendered profile update have succeeded.
	// 只有在全部节点变更与渲染画像回写都成功后，才把指令标记为 applied。
	if err := s.execExpectRowsChanged(ctx, "mark manual profile instruction applied", 1, `
UPDATE vmm_profile_instructions
SET instruction_status = ?,
    review_result_json = ?,
    failure_reason = '',
    updated_timestamp = ?
WHERE id = ?
  AND instruction_status = ?;
`, logicdomain.ProfileInstructionStatusApplied, strings.TrimSpace(reviewResult), nowMs, instruction.ID, logicdomain.ProfileInstructionStatusPending); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionApplied(ctx, instruction.ID, reviewResult, nowMs)
			if reconcileErr == nil && recovered {
				return logicdomain.ManualProfileInstructionApplyResult{
					InstructionID: instruction.ID,
					AcceptedNodes: accepted,
					RetiredNodes:  retired,
				}, nil
			}
			if reconcileErr != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "mark manual profile instruction applied",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
				Operation: "mark manual profile instruction applied",
				Message:   err.Error(),
			}
		}
		return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("mark manual profile instruction applied: %w", err))
	}
	return logicdomain.ManualProfileInstructionApplyResult{
		InstructionID: instruction.ID,
		AcceptedNodes: accepted,
		RetiredNodes:  retired,
	}, nil
}

// standaloneSQLiteProfileRetireDecisions removes retire decisions already persisted by a replacement-node supersede update.
// standaloneSQLiteProfileRetireDecisions 用于移除已经通过替代节点 supersede 更新持久化的退役决策。
func standaloneSQLiteProfileRetireDecisions(retired []logicdomain.ProfileRetireDecision, supersededNodeIDs map[uint64]struct{}) []logicdomain.ProfileRetireDecision {
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

// ConvergeExpiredProfileNodes marks due active profile nodes as expired and returns the affected user/project targets with their remaining renderable active nodes.
// ConvergeExpiredProfileNodes 用于把已到期的 active 画像节点收敛为 expired，并返回受影响的 user/project 目标及其剩余可渲染活跃节点。
func (s *Store) ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	if limit <= 0 {
		limit = 256
	}
	nowMs := time.Now().UTC().UnixMilli()

	// Serialize expiry convergence so status flips and follow-up profile snapshots are observed from one deterministic SQL window.
	// 串行化过期收敛，确保状态切换与后续画像快照都来自同一个确定性的 SQL 窗口。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT id, turn_id, profile_type, bind_id, content, profile_status,
       priority, profile_level, level_reason, refresh_weight,
       source_kind, source_id, status_reason,
       expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE profile_status = ? AND expires_timestamp > 0 AND expires_timestamp <= ?
ORDER BY expires_timestamp ASC, id ASC
LIMIT ?
`, logicdomain.ProfileStatusActive, nowMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query expired profile nodes: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	expiredIDs := make([]uint64, 0, len(rows))
	targetIndex := map[string]int{}
	targets := make([]logicdomain.ProfileRenderTargetSnapshot, 0)
	for _, row := range rows {
		expiredIDs = append(expiredIDs, row.ID)
		key := strconv.Itoa(row.ProfileType) + ":" + strconv.FormatUint(row.BindID, 10)
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
	expireReason := "expired by lifecycle convergence"
	expireStatement, ok := parameterizedProfileNodesExpireStatement(expiredIDs, expireReason, nowMs)
	if !ok {
		return nil, fmt.Errorf("mark expired profile nodes: no valid expired node ids")
	}
	// Verify the exact expired-row boundary because the following render snapshots assume every selected node has already left the active set.
	// 校验精确的过期行边界，因为后续渲染快照默认所有已选节点都已经离开 active 集合。
	expireRecovered := false
	expireResult, err := s.execResult(ctx, expireStatement.SQL, expireStatement.Params...)
	if err != nil {
		writeErr := fmt.Errorf("mark expired profile nodes: %w", err)
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileNodesExpired(ctx, expiredIDs, expireReason, nowMs)
			if reconcileErr != nil {
				return nil, logicdomain.OutcomeUncertainError{
					Operation: "converge expired profile nodes",
					Message:   writeErr.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !recovered {
				return nil, logicdomain.OutcomeUncertainError{
					Operation: "converge expired profile nodes",
					Message:   writeErr.Error(),
				}
			}
			expireRecovered = true
		} else {
			return nil, writeErr
		}
	}
	if expectedRows := int64(len(expiredIDs)); !expireRecovered && expireResult.RowsChanged != expectedRows {
		err := fmt.Errorf("expire sqlite profile nodes by lifecycle convergence affected %d rows, want %d", expireResult.RowsChanged, expectedRows)
		return nil, sqlitePartialMutationError(expireResult.RowsChanged > 0, "converge expired profile nodes", err)
	}
	for idx := range targets {
		nodes, err := s.loadActiveProfileNodes(ctx, targets[idx].ProfileType, targets[idx].BindID, nowMs)
		if err != nil {
			return nil, fmt.Errorf("load active profile nodes for render target: %w", err)
		}
		targets[idx].Nodes = nodes
	}
	return targets, nil
}

// ReplaceRenderedProfiles writes the already rendered scope-level profile blobs back into SQLite after lifecycle convergence, manual instruction review, or batch review.
// ReplaceRenderedProfiles 用于在生命周期收敛、手工画像评审或批量评审之后，把已经渲染好的 scope 画像文本回写到 SQLite。
func (s *Store) ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error {
	if len(updates.UserProfiles) == 0 && len(updates.TeamProfiles) == 0 && len(updates.SpaceProfiles) == 0 && len(updates.ProjectProfiles) == 0 {
		return nil
	}

	// Serialize durable profile-blob replacement so the cached profile text stays aligned with the lifecycle decisions already committed in SQL.
	// 串行化长期 profile Blob 的替换，确保缓存画像文本与前面已经提交到 SQL 的生命周期决策保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nowRFC3339 := time.Now().UTC().Format(time.RFC3339Nano)
	// Track successful earlier scope batches because a later miss means rendered-profile replacement has already partially committed.
	// 记录前序 scope 批量写入是否已成功，因为后续漏命中表示渲染画像替换已经部分提交。
	mutated := false
	userIDs := sortedProfileBindingIDs(updates.UserProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeUser, `
UPDATE vmm_users
SET profile = ?, updated_at = ?
WHERE id = ?
`, userIDs, updates.UserProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered user profiles: %w", err))
	}
	if len(userIDs) > 0 {
		mutated = true
	}
	teamIDs := sortedProfileBindingIDs(updates.TeamProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeTeam, `
UPDATE vmm_teams
SET profile = ?, updated_at = ?
WHERE id = ?
`, teamIDs, updates.TeamProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered team profiles: %w", err))
	}
	if len(teamIDs) > 0 {
		mutated = true
	}
	spaceIDs := sortedProfileBindingIDs(updates.SpaceProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeSpace, `
UPDATE vmm_spaces
SET profile = ?, updated_at = ?
WHERE id = ?
`, spaceIDs, updates.SpaceProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered space profiles: %w", err))
	}
	if len(spaceIDs) > 0 {
		mutated = true
	}
	projectIDs := sortedProfileBindingIDs(updates.ProjectProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeProject, `
UPDATE vmm_projects
SET profile = ?, updated_at = ?
WHERE id = ?
`, projectIDs, updates.ProjectProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered project profiles: %w", err))
	}
	return nil
}

// replaceRenderedProfileBatch reuses SQLite ExecuteBatch for one scope table so repeated profile-blob updates stay in the sqlite-native transport path.
// replaceRenderedProfileBatch 用于对单个 scope 表复用 SQLite ExecuteBatch，让重复的 profile Blob 更新保持在 sqlite 原生传输路径内。
func (s *Store) replaceRenderedProfileBatch(ctx context.Context, profileType int, sql string, ids []uint64, profiles map[uint64]string, nowRFC3339 string) error {
	if len(ids) == 0 {
		return nil
	}
	items := make([][]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, []any{profiles[id], nowRFC3339, id})
	}
	result, err := s.execBatchResult(ctx, sql, items)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileRenderedProfileBatch(ctx, profileType, ids, profiles, nowRFC3339)
			if reconcileErr != nil {
				return fmt.Errorf("%w; reconcile rendered profile batch: %v", err, reconcileErr)
			}
			if recovered {
				return nil
			}
		}
		return err
	}
	if expectedRows := int64(len(ids)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("replace rendered profile batch affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "replace rendered profile batch", err)
	}
	return nil
}
