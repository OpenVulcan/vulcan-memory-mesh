// retention_store.go implements SQLite cold-memory recycle and trash purge helpers for the sqlite-first runtime.
// retention_store.go 用于实现 sqlite-first 运行时下 SQLite 的冷记忆回收与回收站清理逻辑。
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

// RecycleColdMemories transactionally copies terminal durable memories and their context edges into SQLite trash tables before removing them from the hot tables.
// RecycleColdMemories 用于在同一 SQLite 事务里先复制终态长期记忆及其情境边到回收站，再从热表删除。
func (s *Store) RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	if s == nil || s.client == nil {
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

	// Query the terminal-memory candidate set under the write lock so the later trash-copy transaction observes the same hot-table slice.
	// 在写锁保护下查询终态记忆候选，确保后续 trash 复制事务看到的是同一批热表切片。
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
	memoryIDList := sqlUint64List(normalizedMemoryIDs)

	contextRows, err := queryRows[memoryContextEdgeRow](s, ctx, fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, memoryIDList))
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("query sqlite cold memory context edges: %w", err)
	}

	batchID, err := s.nextNumericID(ctx, "vmm_recycle_batches")
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("allocate sqlite recycle batch id: %w", err)
	}
	projectID := sharedSQLiteRecycleProjectID(rows)
	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (%d, %s, 0, %d, %s, %d, 0, %d, %d);

INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason,
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
  source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
  support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
  last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
  decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
)
SELECT %d, %d, %s,
       id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json,
       source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
       support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp,
       last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count,
       decay_disabled, dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s);

INSERT INTO vmm_memory_context_edges_trash (
  batch_id, recycled_at, recycle_reason,
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
)
SELECT %d, %d, %s,
       memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s);

DELETE FROM vmm_memory_context_edges
WHERE memory_id IN (%s);

DELETE FROM vmm_memory_nodes_fts
WHERE memory_id IN (%s);

DELETE FROM vmm_memory_nodes
WHERE id IN (%s);
COMMIT;
`, batchID, sqlStringLiteral(logicdomain.RecycleTypeColdMemory), projectID, sqlStringLiteral(reason), recycledAtMillis, recycledAtMillis, recycledAtMillis,
		batchID, recycledAtMillis, sqlStringLiteral(reason), memoryIDList,
		batchID, recycledAtMillis, sqlStringLiteral(reason), memoryIDList,
		memoryIDList,
		memoryIDList,
		memoryIDList)
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("recycle sqlite cold memories: %w", err)
	}

	return logicdomain.MemoryRecycleResult{
		BatchID:              batchID,
		RecycledMemoryCount:  len(rows),
		RecycledContextCount: len(contextRows),
		RecycledVectorIDs:    normalizeStringList(vectorIDs),
	}, nil
}

// PurgeExpiredMemoryTrash permanently removes SQLite trash batches whose soft-backup retention window has elapsed.
// PurgeExpiredMemoryTrash 用于永久删除已超过软备份保留窗口的 SQLite 回收站批次。
func (s *Store) PurgeExpiredMemoryTrash(ctx context.Context, before time.Time, limit int) (logicdomain.MemoryTrashPurgeResult, error) {
	if s == nil || s.client == nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	limit = normalizeSQLiteRetentionBatchLimit(limit, 64)
	purgeBefore := normalizeSQLiteRecycleTime(before).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	batchRows, err := queryRows[recycleBatchIDRow](s, ctx, `
SELECT id
FROM vmm_recycle_batches
WHERE recycle_type = ?
  AND purged_at = 0
  AND recycled_at > 0
  AND recycled_at <= ?
ORDER BY recycled_at ASC, id ASC
LIMIT ?
`, logicdomain.RecycleTypeColdMemory, purgeBefore, limit)
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("query sqlite recycle batches for purge: %w", err)
	}
	if len(batchRows) == 0 {
		return logicdomain.MemoryTrashPurgeResult{}, nil
	}
	batchIDs := make([]uint64, 0, len(batchRows))
	for _, row := range batchRows {
		batchIDs = append(batchIDs, row.ID)
	}
	normalizedBatchIDs := normalizeUint64List(batchIDs)
	if len(normalizedBatchIDs) == 0 {
		return logicdomain.MemoryTrashPurgeResult{}, nil
	}
	batchIDList := sqlUint64List(normalizedBatchIDs)

	purgedMemoryCount, err := s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_nodes_trash WHERE batch_id IN (%s)`, batchIDList))
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("count sqlite memory trash rows: %w", err)
	}
	purgedContextCount, err := s.countRows(ctx, fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id IN (%s)`, batchIDList))
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("count sqlite memory-context trash rows: %w", err)
	}

	nowMillis := time.Now().UTC().UnixMilli()
	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
DELETE FROM vmm_memory_context_edges_trash
WHERE batch_id IN (%s);

DELETE FROM vmm_memory_nodes_trash
WHERE batch_id IN (%s);

UPDATE vmm_recycle_batches
SET purged_at = %d,
    updated_timestamp = %d
WHERE id IN (%s);
COMMIT;
`, batchIDList, batchIDList, nowMillis, nowMillis, batchIDList)
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("purge sqlite memory trash: %w", err)
	}

	return logicdomain.MemoryTrashPurgeResult{
		BatchIDs:           normalizedBatchIDs,
		PurgedMemoryCount:  purgedMemoryCount,
		PurgedContextCount: purgedContextCount,
	}, nil
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
