// analysis_store.go implements PostgreSQL-backed turn-analysis, active-memory, and memory-adoption flows for the combined store runtime.
// analysis_store.go 用于实现组合库运行时基于 PostgreSQL 的 turn 分析写回、活跃记忆读取和记忆采纳流程。
package vldb_postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LoadActiveSessionMemoryNodes returns the active memory rows belonging to one session so turn analysis can decide what older facts should be superseded.
// LoadActiveSessionMemoryNodes 用于返回某个 session 当前活跃的记忆行，让 turn 分析可以判断哪些旧事实应被覆盖。
func (r *analysisRepository) LoadActiveSessionMemoryNodes(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.origin_session_id = $1
  AND %s
ORDER BY COALESCE(m.source_turn_id, 0) ASC, m.id ASC
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := r.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), int64(session.SessionID))
	if err != nil {
		return nil, fmt.Errorf("query postgres active session memory nodes: %w", err)
	}
	nodes := make([]logicdomain.SessionMemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, row.toSessionMemoryNodeRecord())
	}
	return nodes, nil
}

// LoadRecentDirectMemoryWrites returns the direct AI-written memory rows created inside one exclusion window so the turn analyzer can avoid duplicate extraction.
// LoadRecentDirectMemoryWrites 用于返回某个排斥窗口内新建的 AI 主动写记忆行，让 turn analyzer 避免重复提炼。
func (r *analysisRepository) LoadRecentDirectMemoryWrites(ctx context.Context, session logicdomain.SessionRef, observedAfter, observedBefore time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedBefore.IsZero() {
		return []logicdomain.TurnAnalysisDirectWrite{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.origin_session_id = $1
  AND m.source_kind = $2
  AND %s
  AND m.created_at > $3
  AND m.created_at <= $4
ORDER BY m.created_at ASC, m.id ASC
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := r.queryMemoryNodes(
		ctx,
		strings.TrimSpace(sqlText),
		int64(session.SessionID),
		logicdomain.MemorySourceKindGRPCAIWrite,
		observedAfter.UTC(),
		observedBefore.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query postgres recent direct memory writes: %w", err)
	}
	items := make([]logicdomain.TurnAnalysisDirectWrite, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toTurnAnalysisDirectWrite())
	}
	return items, nil
}

// ApplyMemoryAdoption increments lifecycle counters for the memory rows selected by pre-check and promotes hot session facts when they prove useful across sessions.
// ApplyMemoryAdoption 用于为被 pre-check 采纳的记忆行递增生命周期计数，并在 session 级事实跨会话多次命中后将其升级。
func (r *analysisRepository) ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil
	}
	if adoptedAt.IsZero() {
		adoptedAt = time.Now().UTC()
	} else {
		adoptedAt = adoptedAt.UTC()
	}

	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres memory adoption tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	// Lock the adoption targets inside the same transaction so concurrent pre-check requests cannot evolve stale counters and overwrite each other.
	// 在同一个事务里锁定采纳目标，避免并发 pre-check 请求基于过期计数分别演化后相互覆盖。
	selectAdoptionTargetsSQL := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.id = ANY($1)
ORDER BY m.id ASC
FOR UPDATE
`, memoryNodeSelectColumns("m"), r.memoryNodesTable())
	rows, err := r.queryMemoryNodesWithQueryer(callCtx, tx, strings.TrimSpace(selectAdoptionTargetsSQL), toInt64List(memoryIDs))
	if err != nil {
		return fmt.Errorf("load postgres memory adoption targets: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	updateSQL := fmt.Sprintf(`
UPDATE %s
SET scope_level = $1,
    memory_status = $2,
    memory_level = $3,
    refresh_weight = $4,
    expires_at = $5,
    last_recalled_at = $6,
    last_adopted_at = $7,
    last_reinforced_at = $8,
    recalled_count = $9,
    adopted_count = $10,
    reinforcement_count = $11,
    cross_session_adopted_count = $12,
    decay_disabled = $13,
    updated_at = $14
WHERE id = $15
`, r.memoryNodesTable())
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(record, adoptedAt) {
			continue
		}
		evolved := evolveAdoptedMemoryRecord(session, record, adoptedAt)
		if _, err := tx.Exec(
			callCtx,
			strings.TrimSpace(updateSQL),
			evolved.ScopeLevel,
			evolved.Status,
			evolved.MemoryLevel,
			evolved.RefreshWeight,
			nullableTime(evolved.ExpiresAt),
			nullableTime(evolved.LastRecalledAt),
			nullableTime(evolved.LastAdoptedAt),
			nullableTime(evolved.LastReinforcedAt),
			evolved.RecalledCount,
			evolved.AdoptedCount,
			evolved.ReinforcementCount,
			evolved.CrossSessionAdoptedCount,
			evolved.DecayDisabled,
			evolved.UpdatedAt.UTC(),
			int64(evolved.ID),
		); err != nil {
			return fmt.Errorf("update postgres adopted memory node %d: %w", evolved.ID, err)
		}
	}
	if err := tx.Commit(callCtx); err != nil {
		return fmt.Errorf("commit postgres memory adoption tx: %w", err)
	}
	return nil
}

// ApplyTurnAnalysis writes the extracted turn summary back to the turn row, inserts durable memory/profile nodes, and returns follow-up vector cleanup coordinates.
// ApplyTurnAnalysis 用于把提炼出的 turn 总结回写到 turn 行、插入长期记忆/画像节点，并返回后续向量清理坐标。
func (r *analysisRepository) ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("postgres store is not initialized")
	}
	if turn.ID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	if session.ProjectID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if session.UserID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if analysis.DetailsBudget <= 0 {
		analysis.DetailsBudget = estimateTokenBudget(analysis.Details)
	}
	now := time.Now().UTC()

	// Keep every multi-table mutation inside one explicit transaction so combined mode never commits turn details, memory rows, and profile facts out of sync.
	// 把全部多表变更放进一个显式事务，确保组合模式不会出现 turn 细节、记忆行和画像事实彼此不同步的提交结果。
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("begin postgres turn analysis tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	supersededMemoryIDs := normalizeUint64List(analysis.SupersededMemoryIDs)
	supersededVectorIDs, err := r.loadActiveMemoryVectorIDsTx(callCtx, tx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load postgres superseded vector ids: %w", err)
	}

	updateTurnSQL := fmt.Sprintf(`
UPDATE %s
SET details = $1,
    details_budget = $2,
    extracted_status = $3,
    updated_at = $4
WHERE id = $5
`, r.turnsTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateTurnSQL), strings.TrimSpace(analysis.Details), analysis.DetailsBudget, logicdomain.TurnExtractedStatusDone, now, int64(turn.ID)); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update postgres turn analysis details: %w", err)
	}

	if analysis.UserProfileMerged {
		updateUserSQL := fmt.Sprintf(`UPDATE %s SET profile = $1, updated_at = $2 WHERE id = $3`, r.usersTable())
		if _, err := tx.Exec(callCtx, updateUserSQL, strings.TrimSpace(analysis.MergedUserProfile), now, int64(session.UserID)); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update postgres merged user profile: %w", err)
		}
	}
	if analysis.ProjectProfileMerged {
		updateProjectSQL := fmt.Sprintf(`UPDATE %s SET profile = $1, updated_at = $2 WHERE id = $3`, r.projectsTable())
		if _, err := tx.Exec(callCtx, updateProjectSQL, strings.TrimSpace(analysis.MergedProjectProfile), now, int64(session.ProjectID)); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update postgres merged project profile: %w", err)
		}
	}

	insertedMemoryNodes := make([]logicdomain.MemoryNodeRecord, 0, len(analysis.MemoryNodes))
	for idx, node := range analysis.MemoryNodes {
		if strings.TrimSpace(node.VectorID) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].vector_id", Message: "is required after vector persistence"}
		}
		if len(node.Vector) != r.shared.cfg.EmbeddingDimension {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].vector", Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
		}
		if !logicdomain.ValidMemoryNodeCategory(node.Category) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].category", Message: "must be one supported memory category"}
		}

		record := normalizeTurnMemoryNodeRecord(session, turn, node, now)
		previewEdges := normalizeTurnMemoryContextEdges(1, node.ContextEdges, now)
		record.SupportCount, record.RebuttalCount = summarizeMemoryContextEdges(previewEdges)

		insertSQL := fmt.Sprintf(`
INSERT INTO %s (
	team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
	vector_id, embedding, source_kind, scope_level, category, abstract, details,
	memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
	expires_at, last_recalled_at, last_adopted_at, last_reinforced_at,
	recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
	created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6,
	$7, $8::vector, $9, $10, $11, $12, $13,
	$14, $15, $16, $17, $18, $19, $20,
	$21, $22, $23, $24,
	$25, $26, $27, $28, $29, $30,
	$31, $32
)
RETURNING %s
`, r.memoryNodesTable(), memoryNodeSelectColumns(""))
		var inserted memoryNodeScanRow
		if err := tx.QueryRow(
			callCtx,
			strings.TrimSpace(insertSQL),
			int64(record.TeamID),
			int64(record.SpaceID),
			int64(record.ProjectID),
			int64(record.UserID),
			int64(record.OriginSessionID),
			nullableUint64(record.SourceTurnID),
			record.VectorID,
			encodePGVectorLiteral(record.Vector),
			record.SourceKind,
			record.ScopeLevel,
			record.Category,
			record.Abstract,
			record.Details,
			record.Status,
			record.Priority,
			record.MemoryLevel,
			record.RefreshWeight,
			record.SupportCount,
			record.RebuttalCount,
			record.StatusReason,
			nullableTime(record.ExpiresAt),
			nullableTime(record.LastRecalledAt),
			nullableTime(record.LastAdoptedAt),
			nullableTime(record.LastReinforcedAt),
			record.RecalledCount,
			record.AdoptedCount,
			record.ReinforcementCount,
			record.CrossSessionAdoptedCount,
			record.DecayDisabled,
			record.DedupeHash,
			record.CreatedAt.UTC(),
			record.UpdatedAt.UTC(),
		).Scan(
			&inserted.ID,
			&inserted.TeamID,
			&inserted.SpaceID,
			&inserted.ProjectID,
			&inserted.UserID,
			&inserted.OriginSessionID,
			&inserted.SourceTurnID,
			&inserted.VectorID,
			&inserted.EmbeddingText,
			&inserted.SourceKind,
			&inserted.ScopeLevel,
			&inserted.Category,
			&inserted.Abstract,
			&inserted.Details,
			&inserted.MemoryStatus,
			&inserted.Priority,
			&inserted.MemoryLevel,
			&inserted.RefreshWeight,
			&inserted.SupportCount,
			&inserted.RebuttalCount,
			&inserted.StatusReason,
			&inserted.ExpiresAt,
			&inserted.LastRecalledAt,
			&inserted.LastAdoptedAt,
			&inserted.LastReinforcedAt,
			&inserted.RecalledCount,
			&inserted.AdoptedCount,
			&inserted.ReinforcementCount,
			&inserted.CrossSessionAdoptedCount,
			&inserted.DecayDisabled,
			&inserted.DedupeHash,
			&inserted.CreatedAt,
			&inserted.UpdatedAt,
		); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("insert postgres memory node %d: %w", idx, err)
		}
		insertedRecord := inserted.toMemoryNodeRecord()
		contextEdges := normalizeTurnMemoryContextEdges(insertedRecord.ID, node.ContextEdges, now)
		if len(contextEdges) > 0 {
			insertEdgeSQL := fmt.Sprintf(`
INSERT INTO %s (
	memory_id, context_key, context_value, support_count, rebuttal_count,
	last_supported_at, last_rebutted_at, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5,
	$6, $7, $8, $9
)
`, r.memoryContextEdgesTable())
			for _, edge := range contextEdges {
				if _, err := tx.Exec(
					callCtx,
					strings.TrimSpace(insertEdgeSQL),
					int64(edge.MemoryID),
					edge.ContextKey,
					edge.ContextValue,
					edge.SupportCount,
					edge.RebuttalCount,
					nullableTime(edge.LastSupportedAt),
					nullableTime(edge.LastRebuttedAt),
					edge.CreatedAt.UTC(),
					edge.UpdatedAt.UTC(),
				); err != nil {
					return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("insert postgres memory context edge for memory %d: %w", insertedRecord.ID, err)
				}
			}
		}
		insertedMemoryNodes = append(insertedMemoryNodes, insertedRecord)
	}

	updateSupersededProfilesSQL := fmt.Sprintf(`
UPDATE %s
SET profile_status = $1,
    superseded_by_id = $2,
    status_reason = $3,
    updated_at = $4
WHERE profile_status = $5
  AND id = ANY($6)
`, r.profileNodesTable())
	for idx, node := range analysis.ProfileNodes {
		if !logicdomain.ValidProfileType(node.ProfileType) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].profile_type", Message: "must be one supported profile type"}
		}
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].status", Message: "must be one supported profile status"}
		}
		if !logicdomain.ValidProfilePriority(node.Priority) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].priority", Message: "must be one supported profile priority"}
		}
		if !logicdomain.ValidProfileLevel(node.ProfileLevel) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].profile_level", Message: "must be one supported profile level"}
		}
		if !logicdomain.ValidProfileSourceKind(node.SourceKind) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].source_kind", Message: "must be one supported profile source kind"}
		}
		bindID := session.ProjectID
		switch node.ProfileType {
		case logicdomain.ProfileTypeUser:
			bindID = session.UserID
		case logicdomain.ProfileTypeTeam:
			bindID = session.TeamID
		case logicdomain.ProfileTypeSpace:
			bindID = session.SpaceID
		}
		profileDate := strings.TrimSpace(node.ProfileDate)
		if profileDate == "" {
			profileDate = turn.CreatedAt.UTC().Format("2006-01-02")
		}
		insertProfileSQL := fmt.Sprintf(`
INSERT INTO %s (
	turn_id, profile_type, bind_id, content, profile_status, priority, profile_level,
	level_reason, refresh_weight, source_kind, source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7,
	$8, $9, $10, $11, $12, $13, 0, $14, $15, $15
)
RETURNING id
`, r.profileNodesTable())
		var insertedProfileID uint64
		if err := tx.QueryRow(
			callCtx,
			strings.TrimSpace(insertProfileSQL),
			int64(turn.ID),
			node.ProfileType,
			int64(bindID),
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
		).Scan(&insertedProfileID); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("insert postgres profile node %d: %w", idx, err)
		}
		supersedeNodeIDs := normalizeUint64List(node.SupersedeNodeIDs)
		if len(supersedeNodeIDs) == 0 {
			continue
		}
		// Retire the replaced active profile nodes in the same transaction so readers never observe the reviewed replacement and the superseded facts together.
		// 在同一事务里退役被替换的活跃画像节点，避免读取链路同时看到评审通过的新事实与旧事实。
		if _, err := tx.Exec(
			callCtx,
			strings.TrimSpace(updateSupersededProfilesSQL),
			logicdomain.ProfileStatusSuperseded,
			int64(insertedProfileID),
			strings.TrimSpace(node.StatusReason),
			now,
			logicdomain.ProfileStatusActive,
			toInt64List(supersedeNodeIDs),
		); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("supersede postgres profile nodes for %d: %w", insertedProfileID, err)
		}
	}

	if len(supersededMemoryIDs) > 0 {
		updateSupersededSQL := fmt.Sprintf(`
UPDATE %s
SET memory_status = $1,
    updated_at = $2
WHERE memory_status = $3
  AND id = ANY($4)
`, r.memoryNodesTable())
		if _, err := tx.Exec(
			callCtx,
			strings.TrimSpace(updateSupersededSQL),
			logicdomain.MemoryStatusSuperseded,
			now,
			logicdomain.MemoryStatusActive,
			toInt64List(supersededMemoryIDs),
		); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("supersede postgres memory nodes: %w", err)
		}
	}

	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("commit postgres turn analysis tx: %w", err)
	}
	return logicdomain.TurnAnalysisApplyResult{
		InsertedMemoryNodes: insertedMemoryNodes,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// loadActiveMemoryVectorIDsTx loads the vector ids of active memory rows by memory id inside one transaction so later status flips can return deterministic cleanup coordinates.
// loadActiveMemoryVectorIDsTx 用于在单个事务内按记忆 id 加载 active 行的 vector id，确保后续状态切换返回确定性的清理坐标。
func (r *analysisRepository) loadActiveMemoryVectorIDsTx(ctx context.Context, tx pgx.Tx, memoryIDs []uint64) ([]string, error) {
	if r == nil || r.shared == nil {
		return nil, fmt.Errorf("postgres analysis store is not initialized")
	}
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	sqlText := fmt.Sprintf(`
SELECT vector_id
FROM %s
WHERE memory_status = $1
  AND id = ANY($2)
ORDER BY id ASC
`, r.memoryNodesTable())
	rows, err := tx.Query(ctx, strings.TrimSpace(sqlText), logicdomain.MemoryStatusActive, toInt64List(memoryIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]string, 0, len(memoryIDs))
	for rows.Next() {
		var vectorID string
		if err := rows.Scan(&vectorID); err != nil {
			return nil, fmt.Errorf("scan postgres superseded vector id: %w", err)
		}
		vectorID = strings.TrimSpace(vectorID)
		if vectorID == "" {
			continue
		}
		ids = append(ids, vectorID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres superseded vector ids: %w", err)
	}
	return ids, nil
}
