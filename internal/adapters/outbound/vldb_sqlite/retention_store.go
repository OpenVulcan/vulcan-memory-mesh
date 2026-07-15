// retention_store.go implements SQLite retention recycle and purge helpers for the sqlite-first runtime.
// retention_store.go 用于实现 sqlite-first 运行时下 SQLite 的 retention 回收与回收站清理逻辑。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// recycleBatchIDRow stores one recycle-batch identifier selected for one SQLite trash purge pass.
// recycleBatchIDRow 用于保存 SQLite 某次回收站清理中选中的回收批次标识。
type recycleBatchIDRow struct {
	ID uint64 `json:"id"`
}

// sqliteIdleSessionRecycleResult stores one single-session recycle outcome before the outer loop aggregates multiple session batches.
// sqliteIdleSessionRecycleResult 用于保存单个 session 回收结果，再由外层循环聚合成多 session 批次统计。
type sqliteIdleSessionRecycleResult struct {
	BatchID              uint64
	SessionID            uint64
	RecycledMemoryCount  int
	RecycledContextCount int
	RecycledTurnCount    int
	// TurnDriftCount records selected turns whose trash copy or hot-table delete was not fully confirmed after the batch boundary had already been crossed.
	// TurnDriftCount 记录在 batch 边界已跨过后，trash 复制或热表删除未完整确认的已选 turn 数量。
	TurnDriftCount    int
	RecycledVectorIDs []string
}

// RecycleColdMemories copies terminal durable memories and their context edges into SQLite trash tables before removing them from the hot tables under the process write lock.
// RecycleColdMemories 用于在进程写锁下先复制终态长期记忆及其情境边到回收站，再从热表删除。
func (s *Store) RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	if !s.hasSQLiteStore() {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	limit := normalizeSQLiteRetentionBatchLimit(query.Limit, 128)
	recycledAt := normalizeSQLiteRecycleTime(query.RecycledAt)
	recycledAtMillis := recycledAt.UnixMilli()
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonColdTerminalMemory
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Query the terminal-memory candidate set under the write lock so the later trash-copy writes observe the same hot-table slice.
	// 在写锁保护下查询终态记忆候选，确保后续 trash 复制写入看到的是同一批热表切片。
	selectSQL := `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled,
       dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE memory_status IN (?, ?, ?)`
	params := []any{
		logicdomain.MemoryStatusSuperseded,
		logicdomain.MemoryStatusDeleted,
		logicdomain.MemoryStatusExpired,
	}
	protectedClause, protectedParams := buildSQLiteProtectedSharedMemoryRecycleClause(query)
	if protectedClause != "" {
		selectSQL += "\n  AND " + protectedClause
		params = append(params, protectedParams...)
	}
	selectSQL += "\nORDER BY updated_timestamp ASC, id ASC\nLIMIT ?"
	params = append(params, limit)
	rows, err := queryRows[memoryNodeRow](s, ctx, selectSQL, params...)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("query sqlite cold memories: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.MemoryRecycleResult{}, nil
	}

	memoryIDs := make([]uint64, 0, len(rows))
	vectorIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		memoryIDs = append(memoryIDs, row.ID)
		if vectorID := strings.TrimSpace(row.VectorID); vectorID != "" {
			vectorIDs = append(vectorIDs, vectorID)
		}
	}
	normalizedMemoryIDs := normalizeUint64List(memoryIDs)
	if len(normalizedMemoryIDs) == 0 {
		return logicdomain.MemoryRecycleResult{}, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(normalizedMemoryIDs))
	memoryIDParams := sqliteUint64Params(normalizedMemoryIDs)

	contextRows, err := queryRows[memoryContextEdgeRow](s, ctx, fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, memoryIDPlaceholders), memoryIDParams...)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("query sqlite cold memory context edges: %w", err)
	}

	batchID, err := s.nextNumericID(ctx, "vmm_recycle_batches")
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("allocate sqlite recycle batch id: %w", err)
	}
	projectID := sharedSQLiteRecycleProjectID(rows)
	batchInsertStatement := sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (?, ?, 0, ?, ?, ?, 0, ?, ?);
`,
		Params: []any{batchID, logicdomain.RecycleTypeColdMemory, projectID, reason, recycledAtMillis, recycledAtMillis, recycledAtMillis},
	}
	memoryTrashStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason,
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
  source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
  support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
  last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
  decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
       source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
       support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
       last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
       decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s);
`, memoryIDPlaceholders),
		Params: append([]any{batchID, recycledAtMillis, reason}, memoryIDParams...),
	}
	contextTrashStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
INSERT INTO vmm_memory_context_edges_trash (
  batch_id, recycled_at, recycle_reason,
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s);
`, memoryIDPlaceholders),
		Params: append([]any{batchID, recycledAtMillis, reason}, memoryIDParams...),
	}
	contextDeleteStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_context_edges
WHERE memory_id IN (%s);
`, memoryIDPlaceholders),
		Params: memoryIDParams,
	}
	memoryDeleteStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_nodes
WHERE id IN (%s);
`, memoryIDPlaceholders),
		Params: memoryIDParams,
	}
	// Verify every recycle stage before advancing to destructive deletes because trash copies are the only durable recovery source after hot rows leave active tables.
	// 在进入破坏性删除前逐步验收每个回收阶段，因为热表行离开 active 表后，trash 副本就是唯一的长期恢复来源。
	if err := s.execInsertOneRow(ctx, "insert cold memory recycle batch", batchInsertStatement.SQL, batchInsertStatement.Params...); err != nil {
		return logicdomain.MemoryRecycleResult{}, err
	}
	mutated := true
	expectedMemoryRowsChanged := int64(len(normalizedMemoryIDs))
	if err := s.execExpectRowsChanged(ctx, "copy sqlite cold memories to trash", expectedMemoryRowsChanged, memoryTrashStatement.SQL, memoryTrashStatement.Params...); err != nil {
		return logicdomain.MemoryRecycleResult{}, sqlitePartialMutationError(mutated, "recycle cold memories", err)
	}
	expectedContextRowsChanged := int64(len(contextRows))
	if err := s.execExpectRowsChanged(ctx, "copy sqlite cold memory contexts to trash", expectedContextRowsChanged, contextTrashStatement.SQL, contextTrashStatement.Params...); err != nil {
		return logicdomain.MemoryRecycleResult{}, sqlitePartialMutationError(mutated, "recycle cold memories", err)
	}
	if err := s.execExpectRowsChanged(ctx, "delete recycled sqlite cold memory contexts", expectedContextRowsChanged, contextDeleteStatement.SQL, contextDeleteStatement.Params...); err != nil {
		return logicdomain.MemoryRecycleResult{}, sqlitePartialMutationError(mutated, "recycle cold memories", err)
	}
	deleteResult, err := s.execResult(ctx, memoryDeleteStatement.SQL, memoryDeleteStatement.Params...)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, sqlitePartialMutationError(mutated, "recycle cold memories", fmt.Errorf("delete recycled sqlite cold memories: %w", err))
	}
	// Return vector cleanup coordinates only after every copied hot memory row has actually left the active table.
	// 只有所有已复制的热 memory 行都确实离开 active 表后，才返回 vector 清理坐标。
	if deleteResult.RowsChanged != expectedMemoryRowsChanged {
		return logicdomain.MemoryRecycleResult{}, logicdomain.OutcomeUncertainError{
			Operation: "recycle cold memories",
			Message:   fmt.Sprintf("delete recycled sqlite cold memories affected %d rows, want %d", deleteResult.RowsChanged, expectedMemoryRowsChanged),
		}
	}
	result := logicdomain.MemoryRecycleResult{
		BatchID:              batchID,
		RecycledMemoryCount:  len(rows),
		RecycledContextCount: len(contextRows),
		RecycledVectorIDs:    normalizeStringList(vectorIDs),
	}
	if s.database != nil {
		if err := s.syncMemoryFTSAfterWrite(ctx, nil, normalizedMemoryIDs); err != nil {
			return result, sqlitePartialMutationError(mutated, "recycle cold memories", fmt.Errorf("sync sqlite memory fts after cold recycle: %w", err))
		}
	}

	return result, nil
}

// RecycleIdleSessions compacts long-idle SQLite sessions by moving stale session memories and eligible old turns into trash tables before removing them from the hot tables.
// RecycleIdleSessions 用于压缩长期空闲的 SQLite session，把陈旧 session 记忆和符合条件的旧 turn 迁入回收站，再从热表删除。
func (s *Store) RecycleIdleSessions(ctx context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error) {
	if !s.hasSQLiteStore() {
		return logicdomain.SessionIdleRecycleResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	limit := normalizeSQLiteRetentionBatchLimit(query.Limit, 32)
	recycledAt := normalizeSQLiteRecycleTime(query.RecycledAt)
	recycledAtMillis := recycledAt.UnixMilli()
	idleBefore := normalizeSQLiteRecycleTime(query.IdleBefore)
	idleBeforeMillis := idleBefore.UnixMilli()
	turnHotWindowSize := normalizeSQLiteTurnHotWindowSize(query.TurnHotWindowSize)
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonIdleSessionCompact
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Select only long-idle sessions without pending turns so this maintenance pass never races the active post-action extraction path.
	// 只选择长期空闲、没有 pending turn 且确实存在可回收内容的 session，确保维护流程不会和活跃提炼竞争，也避免最老但 no-op 的 session 持续饿死后续可回收批次。
	candidateAvailabilityClause, candidateAvailabilityParams := buildSQLiteIdleSessionCandidateAvailabilityClause(idleBeforeMillis, turnHotWindowSize)
	sessionRows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE updated_timestamp > 0
  AND updated_timestamp <= ?
  AND NOT EXISTS (
    SELECT 1
    FROM vmm_turn_records tr
    WHERE tr.session_id = vmm_sessions.id
      AND tr.extracted_status = ?
  )
  AND `+candidateAvailabilityClause+`
ORDER BY updated_timestamp ASC, id ASC
LIMIT ?
`, append([]any{idleBeforeMillis, logicdomain.TurnExtractedStatusPending}, append(candidateAvailabilityParams, limit)...)...)
	if err != nil {
		return logicdomain.SessionIdleRecycleResult{}, fmt.Errorf("query sqlite idle sessions: %w", err)
	}
	if len(sessionRows) == 0 {
		return logicdomain.SessionIdleRecycleResult{}, nil
	}

	result := logicdomain.SessionIdleRecycleResult{}
	for _, session := range sessionRows {
		sessionResult, recycleErr := s.recycleOneSQLiteIdleSession(ctx, session, idleBeforeMillis, turnHotWindowSize, recycledAtMillis, reason)
		if sessionResult.BatchID != 0 {
			result.BatchIDs = append(result.BatchIDs, sessionResult.BatchID)
			result.SessionIDs = append(result.SessionIDs, sessionResult.SessionID)
			result.RecycledMemoryCount += sessionResult.RecycledMemoryCount
			result.RecycledContextCount += sessionResult.RecycledContextCount
			result.RecycledTurnCount += sessionResult.RecycledTurnCount
			result.TurnDriftCount += sessionResult.TurnDriftCount
			result.RecycledVectorIDs = append(result.RecycledVectorIDs, sessionResult.RecycledVectorIDs...)
		}
		if recycleErr != nil {
			result.BatchIDs = normalizeUint64List(result.BatchIDs)
			result.SessionIDs = normalizeUint64List(result.SessionIDs)
			result.RecycledVectorIDs = normalizeStringList(result.RecycledVectorIDs)
			if len(result.BatchIDs) > 0 {
				return result, sqlitePartialMutationError(true, "recycle idle sessions", recycleErr)
			}
			return logicdomain.SessionIdleRecycleResult{}, recycleErr
		}
	}
	result.BatchIDs = normalizeUint64List(result.BatchIDs)
	result.SessionIDs = normalizeUint64List(result.SessionIDs)
	result.RecycledVectorIDs = normalizeStringList(result.RecycledVectorIDs)
	return result, nil
}

// PurgeExpiredTrash permanently removes SQLite trash batches whose soft-backup retention window has elapsed, including the recycle-batch metadata row so batch bookkeeping cannot grow without bound after trash data is gone.
// PurgeExpiredTrash 用于永久删除已超过软备份保留窗口的 SQLite 回收站批次，并一并删除回收批次元数据，避免在 trash 数据清空后批次台账继续无界增长。
func (s *Store) PurgeExpiredTrash(ctx context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error) {
	if !s.hasSQLiteStore() {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	limit = normalizeSQLiteRetentionBatchLimit(limit, 64)
	purgeBefore := normalizeSQLiteRecycleTime(before).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	batchRows, err := queryRows[recycleBatchIDRow](s, ctx, `
SELECT id
FROM vmm_recycle_batches
WHERE purged_at = 0
  AND recycled_at > 0
  AND recycled_at <= ?
ORDER BY recycled_at ASC, id ASC
LIMIT ?
`, purgeBefore, limit)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("query sqlite recycle batches for purge: %w", err)
	}
	if len(batchRows) == 0 {
		return logicdomain.RetentionTrashPurgeResult{}, nil
	}
	batchIDs := make([]uint64, 0, len(batchRows))
	for _, row := range batchRows {
		batchIDs = append(batchIDs, row.ID)
	}
	normalizedBatchIDs := normalizeUint64List(batchIDs)
	if len(normalizedBatchIDs) == 0 {
		return logicdomain.RetentionTrashPurgeResult{}, nil
	}
	batchIDPlaceholders := sqlitePlaceholders(len(normalizedBatchIDs))
	batchIDParams := sqliteUint64Params(normalizedBatchIDs)

	purgedMemoryCount, err := s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_nodes_trash WHERE batch_id IN (%s)`, batchIDPlaceholders), batchIDParams...)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("count sqlite memory trash rows: %w", err)
	}
	purgedContextCount, err := s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id IN (%s)`, batchIDPlaceholders), batchIDParams...)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("count sqlite memory-context trash rows: %w", err)
	}
	purgedTurnCount, err := s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_turn_records_trash WHERE batch_id IN (%s)`, batchIDPlaceholders), batchIDParams...)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("count sqlite turn trash rows: %w", err)
	}

	// Verify each permanent delete against the pre-counted trash rows so purge cannot report a batch as removed while rows or metadata remain.
	// 用预先统计的 trash 行数校验每一次永久删除，避免仍有数据行或元数据残留时把批次报告为已清理。
	mutated := false
	mutated, err = s.execSQLiteTrashPurgeDelete(ctx, mutated, "delete sqlite memory-context trash rows", int64(purgedContextCount), sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_context_edges_trash
WHERE batch_id IN (%s);
`, batchIDPlaceholders),
		Params: batchIDParams,
	})
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, err
	}
	mutated, err = s.execSQLiteTrashPurgeDelete(ctx, mutated, "delete sqlite memory trash rows", int64(purgedMemoryCount), sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_nodes_trash
WHERE batch_id IN (%s);
`, batchIDPlaceholders),
		Params: batchIDParams,
	})
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, err
	}
	mutated, err = s.execSQLiteTrashPurgeDelete(ctx, mutated, "delete sqlite turn trash rows", int64(purgedTurnCount), sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_turn_records_trash
WHERE batch_id IN (%s);
`, batchIDPlaceholders),
		Params: batchIDParams,
	})
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, err
	}
	mutated, err = s.execSQLiteTrashPurgeDelete(ctx, mutated, "delete sqlite recycle batch metadata", int64(len(normalizedBatchIDs)), sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_recycle_batches
WHERE id IN (%s);
`, batchIDPlaceholders),
		Params: batchIDParams,
	})
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, err
	}

	return logicdomain.RetentionTrashPurgeResult{
		BatchIDs:           normalizedBatchIDs,
		PurgedMemoryCount:  purgedMemoryCount,
		PurgedContextCount: purgedContextCount,
		PurgedTurnCount:    purgedTurnCount,
	}, nil
}

// execSQLiteTrashPurgeDelete executes one permanent purge delete and reports whether this purge pass has already mutated trash state.
// execSQLiteTrashPurgeDelete 用于执行一条永久 purge 删除，并返回本轮 purge 是否已经改变过 trash 状态。
func (s *Store) execSQLiteTrashPurgeDelete(ctx context.Context, mutated bool, operation string, expectedRows int64, statement sqliteWriteStatement) (bool, error) {
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		return mutated, fmt.Errorf("%s: %w", operation, err)
	}
	if result.RowsChanged == expectedRows {
		return mutated || result.RowsChanged > 0, nil
	}
	err = fmt.Errorf("%s affected %d rows, want %d", operation, result.RowsChanged, expectedRows)
	return mutated || result.RowsChanged > 0, sqlitePartialMutationError(mutated || result.RowsChanged > 0, "purge expired trash", err)
}

// recycleOneSQLiteIdleSession compacts one concrete long-idle session under the adapter write lock so the selected stale rows and turn candidates cannot drift before the final recycle script runs.
// recycleOneSQLiteIdleSession 用于在适配器写锁下压缩单个长期空闲 session，确保选中的陈旧行和 turn 候选在最终回收脚本执行前不会漂移。
func (s *Store) recycleOneSQLiteIdleSession(ctx context.Context, session sessionRow, idleBeforeMillis int64, turnHotWindowSize int, recycledAtMillis int64, reason string) (sqliteIdleSessionRecycleResult, error) {
	memoryRows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled,
       dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE origin_session_id = ?
  AND scope_level = ?
  AND memory_status = ?
  AND expires_timestamp > 0
  AND expires_timestamp <= ?
  AND MAX(last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp, created_timestamp) <= ?
ORDER BY updated_timestamp ASC, id ASC
`, session.ID, logicdomain.MemoryScopeLevelSession, logicdomain.MemoryStatusActive, idleBeforeMillis, idleBeforeMillis)
	if err != nil {
		return sqliteIdleSessionRecycleResult{}, fmt.Errorf("query sqlite idle-session memories for session %d: %w", session.ID, err)
	}

	memoryIDs := make([]uint64, 0, len(memoryRows))
	vectorIDs := make([]string, 0, len(memoryRows))
	for _, row := range memoryRows {
		memoryIDs = append(memoryIDs, row.ID)
		if vectorID := strings.TrimSpace(row.VectorID); vectorID != "" {
			vectorIDs = append(vectorIDs, vectorID)
		}
	}
	normalizedMemoryIDs := normalizeUint64List(memoryIDs)
	memoryIDPlaceholders := sqlitePlaceholders(len(normalizedMemoryIDs))
	memoryIDParams := sqliteUint64Params(normalizedMemoryIDs)
	contextCount := 0
	if len(normalizedMemoryIDs) > 0 {
		contextCount, err = s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_context_edges WHERE memory_id IN (%s)`, memoryIDPlaceholders), memoryIDParams...)
		if err != nil {
			return sqliteIdleSessionRecycleResult{}, fmt.Errorf("count sqlite idle-session context edges for session %d: %w", session.ID, err)
		}
	}

	turnReferenceClause, turnReferenceParams := buildSQLiteIdleSessionTurnReferenceClause(normalizedMemoryIDs)
	turnQueryParams := []any{session.ID, turnHotWindowSize, session.ID, logicdomain.TurnExtractedStatusPending}
	turnQueryParams = append(turnQueryParams, turnReferenceParams...)
	turnRows, err := queryRows[turnRecordRow](s, ctx, fmt.Sprintf(`
WITH recent_turns AS (
  SELECT id
  FROM vmm_turn_records
  WHERE session_id = ?
  ORDER BY id DESC
  LIMIT ?
)
SELECT tr.id, tr.session_id, tr.project_id,
       tr.dehydrated_content,
       tr.dehydrated_budget, tr.extracted_status, tr.details, tr.details_budget,
       tr.created_timestamp, tr.updated_timestamp
FROM vmm_turn_records tr
WHERE tr.session_id = ?
  AND tr.extracted_status <> ?
  AND tr.id NOT IN (SELECT id FROM recent_turns)
  AND %s
  AND NOT EXISTS (
    SELECT 1
    FROM vmm_profile_nodes pn
    WHERE pn.turn_id = tr.id
  )
ORDER BY tr.id ASC
`, turnReferenceClause), turnQueryParams...)
	if err != nil {
		return sqliteIdleSessionRecycleResult{}, fmt.Errorf("query sqlite idle-session turns for session %d: %w", session.ID, err)
	}
	if len(normalizedMemoryIDs) == 0 && len(turnRows) == 0 {
		return sqliteIdleSessionRecycleResult{}, nil
	}

	batchID, err := s.nextNumericID(ctx, "vmm_recycle_batches")
	if err != nil {
		return sqliteIdleSessionRecycleResult{}, fmt.Errorf("allocate sqlite idle-session recycle batch id for session %d: %w", session.ID, err)
	}

	turnIDs := make([]uint64, 0, len(turnRows))
	for _, row := range turnRows {
		turnIDs = append(turnIDs, row.ID)
	}
	normalizedTurnIDs := normalizeUint64List(turnIDs)

	batchStatement := sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?);
`,
		Params: []any{batchID, logicdomain.RecycleTypeSessionIdle, session.ID, session.ProjectID, reason, recycledAtMillis, recycledAtMillis, recycledAtMillis},
	}
	var memoryTrashStatement sqliteWriteStatement
	var contextTrashStatement sqliteWriteStatement
	var contextDeleteStatement sqliteWriteStatement
	var memoryDeleteStatement sqliteWriteStatement
	hasMemoryRecycleStatements := false
	var turnTrashStatement sqliteWriteStatement
	var turnDeleteStatement sqliteWriteStatement
	hasTurnRecycleStatements := false

	if len(normalizedMemoryIDs) > 0 {
		memoryTrashStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason,
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
  source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
  support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
  last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
  decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
       source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
       support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
       last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
       decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s);
`, memoryIDPlaceholders),
			Params: append([]any{batchID, recycledAtMillis, reason}, memoryIDParams...),
		}
		contextTrashStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
INSERT INTO vmm_memory_context_edges_trash (
  batch_id, recycled_at, recycle_reason,
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s);
`, memoryIDPlaceholders),
			Params: append([]any{batchID, recycledAtMillis, reason}, memoryIDParams...),
		}
		contextDeleteStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_context_edges
WHERE memory_id IN (%s);
`, memoryIDPlaceholders),
			Params: memoryIDParams,
		}
		memoryDeleteStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
DELETE FROM vmm_memory_nodes
WHERE id IN (%s);
`, memoryIDPlaceholders),
			Params: memoryIDParams,
		}
		hasMemoryRecycleStatements = true
	}

	if len(normalizedTurnIDs) > 0 {
		turnIDPlaceholders := sqlitePlaceholders(len(normalizedTurnIDs))
		turnIDParams := sqliteUint64Params(normalizedTurnIDs)
		turnTrashStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
INSERT INTO vmm_turn_records_trash (
  batch_id, recycled_at, recycle_reason,
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       id, session_id, project_id, dehydrated_content, dehydrated_budget,
       extracted_status, details, details_budget, created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s);
`, turnIDPlaceholders),
			Params: append([]any{batchID, recycledAtMillis, reason}, turnIDParams...),
		}
		turnDeleteStatement = sqliteWriteStatement{
			SQL: fmt.Sprintf(`
DELETE FROM vmm_turn_records
WHERE id IN (%s);
`, turnIDPlaceholders),
			Params: turnIDParams,
		}
		hasTurnRecycleStatements = true
	}

	// Verify the batch metadata row before copying trash rows so orphaned trash data cannot be created under an unconfirmed batch id.
	// 在复制 trash 行之前先验收批次元数据，避免在未确认的 batch id 下创建孤立回收站数据。
	if err := s.execInsertOneRow(ctx, fmt.Sprintf("insert sqlite idle-session recycle batch for session %d", session.ID), batchStatement.SQL, batchStatement.Params...); err != nil {
		return sqliteIdleSessionRecycleResult{}, fmt.Errorf("recycle sqlite idle session %d: %w", session.ID, err)
	}
	if hasMemoryRecycleStatements {
		expectedMemoryRowsChanged := int64(len(normalizedMemoryIDs))
		memoryTrashResult, err := s.execResult(ctx, memoryTrashStatement.SQL, memoryTrashStatement.Params...)
		if err != nil {
			return sqliteIdleSessionRecycleResult{}, sqlitePartialMutationError(true, "recycle idle-session memories", fmt.Errorf("copy sqlite idle-session memories to trash for session %d: %w", session.ID, err))
		}
		// Stop before deleting hot memory rows unless the trash copy covers the exact selected session-memory set.
		// 除非 trash 复制精确覆盖已选中的 session memory 集合，否则在删除热表 memory 前停止。
		if memoryTrashResult.RowsChanged != expectedMemoryRowsChanged {
			return sqliteIdleSessionRecycleResult{}, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session memories",
				Message:   fmt.Sprintf("copy sqlite idle-session memories to trash for session %d affected %d rows, want %d", session.ID, memoryTrashResult.RowsChanged, expectedMemoryRowsChanged),
			}
		}
		expectedContextRowsChanged := int64(contextCount)
		contextTrashResult, err := s.execResult(ctx, contextTrashStatement.SQL, contextTrashStatement.Params...)
		if err != nil {
			return sqliteIdleSessionRecycleResult{}, sqlitePartialMutationError(true, "recycle idle-session memories", fmt.Errorf("copy sqlite idle-session memory contexts to trash for session %d: %w", session.ID, err))
		}
		// Keep hot memory rows in place unless the context-edge audit copy matches the pre-counted context set.
		// 除非 context edge 审计复制与预先计数的 context 集合一致，否则保留热表 memory 行。
		if contextTrashResult.RowsChanged != expectedContextRowsChanged {
			return sqliteIdleSessionRecycleResult{}, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session memories",
				Message:   fmt.Sprintf("copy sqlite idle-session memory contexts to trash for session %d affected %d rows, want %d", session.ID, contextTrashResult.RowsChanged, expectedContextRowsChanged),
			}
		}
		contextDeleteResult, err := s.execResult(ctx, contextDeleteStatement.SQL, contextDeleteStatement.Params...)
		if err != nil {
			return sqliteIdleSessionRecycleResult{}, sqlitePartialMutationError(true, "recycle idle-session memories", fmt.Errorf("delete recycled sqlite idle-session memory contexts for session %d: %w", session.ID, err))
		}
		// Stop before deleting hot memory rows if context removal drifted from the audited context count.
		// 如果 context 删除行数偏离审计计数，则在删除热表 memory 前停止。
		if contextDeleteResult.RowsChanged != expectedContextRowsChanged {
			return sqliteIdleSessionRecycleResult{}, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session memories",
				Message:   fmt.Sprintf("delete recycled sqlite idle-session memory contexts for session %d affected %d rows, want %d", session.ID, contextDeleteResult.RowsChanged, expectedContextRowsChanged),
			}
		}
		deleteResult, err := s.execResult(ctx, memoryDeleteStatement.SQL, memoryDeleteStatement.Params...)
		if err != nil {
			return sqliteIdleSessionRecycleResult{}, sqlitePartialMutationError(true, "recycle idle-session memories", fmt.Errorf("delete recycled sqlite idle-session memories for session %d: %w", session.ID, err))
		}
		// Return vector cleanup coordinates only after every selected session memory row has left the hot table.
		// 只有所有已选中的 session memory 行都离开热表后，才允许返回 vector 清理坐标。
		if deleteResult.RowsChanged != expectedMemoryRowsChanged {
			return sqliteIdleSessionRecycleResult{}, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session memories",
				Message:   fmt.Sprintf("delete recycled sqlite idle-session memories for session %d affected %d rows, want %d", session.ID, deleteResult.RowsChanged, expectedMemoryRowsChanged),
			}
		}
	}
	result := sqliteIdleSessionRecycleResult{
		BatchID:              batchID,
		SessionID:            session.ID,
		RecycledMemoryCount:  len(normalizedMemoryIDs),
		RecycledContextCount: contextCount,
		RecycledTurnCount:    len(normalizedTurnIDs),
		RecycledVectorIDs:    normalizeStringList(vectorIDs),
	}
	if hasTurnRecycleStatements {
		expectedTurnRowsChanged := int64(len(normalizedTurnIDs))
		turnTrashResult, err := s.execResult(ctx, turnTrashStatement.SQL, turnTrashStatement.Params...)
		if err != nil {
			result.RecycledTurnCount = 0
			result.TurnDriftCount = len(normalizedTurnIDs)
			return result, sqlitePartialMutationError(true, "recycle idle-session turns", fmt.Errorf("copy sqlite idle-session turns to trash for session %d: %w", session.ID, err))
		}
		if turnTrashResult.RowsChanged != expectedTurnRowsChanged {
			result.RecycledTurnCount = 0
			result.TurnDriftCount = len(normalizedTurnIDs)
			return result, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session turns",
				Message:   fmt.Sprintf("copy sqlite idle-session turns to trash for session %d affected %d rows, want %d", session.ID, turnTrashResult.RowsChanged, expectedTurnRowsChanged),
			}
		}
		turnDeleteResult, err := s.execResult(ctx, turnDeleteStatement.SQL, turnDeleteStatement.Params...)
		if err != nil {
			result.RecycledTurnCount = 0
			result.TurnDriftCount = len(normalizedTurnIDs)
			return result, sqlitePartialMutationError(true, "recycle idle-session turns", fmt.Errorf("delete recycled sqlite idle-session turns for session %d: %w", session.ID, err))
		}
		if turnDeleteResult.RowsChanged != expectedTurnRowsChanged {
			// Return the already-confirmed memory/vector coordinates while marking turn archival as uncertain, so callers can still clean vectors without logging the turn pass as successful.
			// 返回已确认的 memory/vector 坐标，同时把 turn 归档标记为不确定，让调用方仍可清理向量但不会把 turn 回收记录为成功。
			result.RecycledTurnCount = 0
			result.TurnDriftCount = len(normalizedTurnIDs)
			return result, logicdomain.OutcomeUncertainError{
				Operation: "recycle idle-session turns",
				Message:   fmt.Sprintf("delete recycled sqlite idle-session turns for session %d affected %d rows, want %d", session.ID, turnDeleteResult.RowsChanged, expectedTurnRowsChanged),
			}
		}
	}
	if s.database != nil {
		if err := s.syncMemoryFTSAfterWrite(ctx, nil, normalizedMemoryIDs); err != nil {
			return result, sqlitePartialMutationError(true, "recycle idle-session memories", fmt.Errorf("sync sqlite memory fts after idle recycle %d: %w", session.ID, err))
		}
	}

	return result, nil
}

// buildSQLiteProtectedSharedMemoryRecycleClause renders the shared-memory protection predicate used by SQLite recycle scans.
// buildSQLiteProtectedSharedMemoryRecycleClause 用于渲染 SQLite 回收扫描使用的共享记忆保护谓词。
func buildSQLiteProtectedSharedMemoryRecycleClause(query logicdomain.MemoryRecycleQuery) (string, []any) {
	if !query.SkipProtectedSharedMemories {
		return "", nil
	}
	return `NOT (scope_level <> ? AND (priority <= ? OR memory_level >= ?))`, []any{
		logicdomain.MemoryScopeLevelSession,
		query.ProtectPriorityFloor,
		query.ProtectMemoryLevelFloor,
	}
}

// buildSQLiteIdleSessionCandidateAvailabilityClause prefilters idle-session candidates to only sessions that already expose recyclable stale session memories or old turns, so no-op oldest sessions cannot block later useful work forever.
// buildSQLiteIdleSessionCandidateAvailabilityClause 用于为 SQLite idle-session 候选追加“确实存在可回收数据”的预过滤，避免最老但 no-op 的 session 永远阻塞后续真正有收益的回收工作。
func buildSQLiteIdleSessionCandidateAvailabilityClause(idleBeforeMillis int64, turnHotWindowSize int) (string, []any) {
	return `(
EXISTS (
  SELECT 1
  FROM vmm_memory_nodes m
  WHERE m.origin_session_id = vmm_sessions.id
    AND m.scope_level = ?
    AND m.memory_status = ?
    AND m.expires_timestamp > 0
    AND m.expires_timestamp <= ?
    AND MAX(m.last_recalled_timestamp, m.last_adopted_timestamp, m.last_reinforced_timestamp, m.created_timestamp) <= ?
)
OR EXISTS (
  WITH recent_turns AS (
    SELECT id
    FROM vmm_turn_records
    WHERE session_id = vmm_sessions.id
    ORDER BY id DESC
    LIMIT ?
  )
  SELECT 1
  FROM vmm_turn_records tr
  WHERE tr.session_id = vmm_sessions.id
    AND tr.extracted_status <> ?
    AND tr.id NOT IN (SELECT id FROM recent_turns)
    AND NOT EXISTS (SELECT 1 FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id)
    AND NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)
)
)`, []any{
			logicdomain.MemoryScopeLevelSession,
			logicdomain.MemoryStatusActive,
			idleBeforeMillis,
			idleBeforeMillis,
			turnHotWindowSize,
			logicdomain.TurnExtractedStatusPending,
		}
}

// buildSQLiteIdleSessionTurnReferenceClause renders the turn-reference predicate used by idle-session recycle, optionally ignoring memory rows scheduled for deletion in the same batch.
// buildSQLiteIdleSessionTurnReferenceClause 用于渲染 idle-session 回收里的 turn 引用谓词，并可选忽略同批将被删除的记忆行。
func buildSQLiteIdleSessionTurnReferenceClause(recycledMemoryIDs []uint64) (string, []any) {
	recycledMemoryIDs = normalizeUint64List(recycledMemoryIDs)
	if len(recycledMemoryIDs) == 0 {
		return `NOT EXISTS (
    SELECT 1
    FROM vmm_memory_nodes mn
    WHERE mn.source_turn_id = tr.id
  )`, nil
	}
	return fmt.Sprintf(`NOT EXISTS (
    SELECT 1
    FROM vmm_memory_nodes mn
    WHERE mn.source_turn_id = tr.id
      AND mn.id NOT IN (%s)
  )`, sqlitePlaceholders(len(recycledMemoryIDs))), sqliteUint64Params(recycledMemoryIDs)
}

// normalizeSQLiteRetentionBatchLimit keeps recycle and purge limits positive even when callers pass zero or negative values.
// normalizeSQLiteRetentionBatchLimit 用于在调用方传入零值或负值时，仍把 SQLite 回收与 purge 批量限制保持为正数。
func normalizeSQLiteRetentionBatchLimit(limit, fallback int) int {
	if limit > 0 {
		return limit
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}

// normalizeSQLiteTurnHotWindowSize keeps the runtime hot-window size non-negative even when callers pass an invalid value.
// normalizeSQLiteTurnHotWindowSize 用于在调用方传入非法值时，仍把运行时 turn 热窗口保持为非负数。
func normalizeSQLiteTurnHotWindowSize(size int) int {
	if size > 0 {
		return size
	}
	return 0
}

// normalizeSQLiteRecycleTime preserves the caller-provided recycle timestamp when present, otherwise falls back to the current UTC time.
// normalizeSQLiteRecycleTime 用于在调用方给出回收时间时保留原值，否则回退到当前 UTC 时间。
func normalizeSQLiteRecycleTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

// sharedSQLiteRecycleProjectID keeps one batch-level project id only when every recycled memory row belongs to the same project.
// sharedSQLiteRecycleProjectID 用于仅在整批被回收的记忆都属于同一 project 时保留该批次级 project id。
func sharedSQLiteRecycleProjectID(rows []memoryNodeRow) uint64 {
	if len(rows) == 0 {
		return 0
	}
	projectID := rows[0].ProjectID
	if projectID == 0 {
		return 0
	}
	for _, row := range rows[1:] {
		if row.ProjectID != projectID {
			return 0
		}
	}
	return projectID
}
