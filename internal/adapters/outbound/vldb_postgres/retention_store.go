// retention_store.go implements PostgreSQL cold-memory recycle and trash purge helpers for the combined runtime.
// retention_store.go 用于实现组合运行时下 PostgreSQL 的冷记忆回收与回收站清理逻辑。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// recycleBatchLookupRow stores one recycle-batch identifier selected for one trash purge pass.
// recycleBatchLookupRow 用于保存某次回收站清理中选中的回收批次标识。
type recycleBatchLookupRow struct {
	ID uint64
}

// RecycleColdMemories transactionally moves terminal durable memories and their context edges into PostgreSQL trash tables, then removes them from the hot tables.
// RecycleColdMemories 用于在一个 PostgreSQL 事务里把终态长期记忆及其情境边迁入回收站，并从热表删除。
func (s *Store) RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	if s == nil || s.pool == nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("postgres store is not initialized")
	}
	limit := normalizeRetentionBatchLimit(query.Limit, 128)
	recycledAt := chooseNonZeroTime(query.RecycledAt, time.Now().UTC())
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonColdTerminalMemory
	}

	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("begin postgres cold-memory recycle tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	// Lock one bounded candidate set with SKIP LOCKED so concurrent maintenance workers cannot recycle the same hot rows twice.
	// 用 SKIP LOCKED 锁住一批有界候选，避免并发维护工作器重复回收同一批热表行。
	args := &sqlArgsBuilder{}
	whereClauses := []string{
		fmt.Sprintf("m.memory_status IN (%d, %d, %d)", logicdomain.MemoryStatusSuperseded, logicdomain.MemoryStatusDeleted, logicdomain.MemoryStatusExpired),
	}
	whereClauses = appendProtectedSharedMemoryRecycleFilter(whereClauses, args, query, "m")
	limitPlaceholder := args.Add(limit)
	selectSQL := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE %s
ORDER BY m.updated_at ASC, m.id ASC
LIMIT %s
FOR UPDATE SKIP LOCKED
`, memoryNodeSelectColumns("m"), s.memoryNodesTable(), strings.Join(whereClauses, " AND "), limitPlaceholder)
	rows, err := s.queryMemoryNodesWithQueryer(callCtx, tx, selectSQL, args.Args()...)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("query postgres cold memories: %w", err)
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

	// Insert the recycle batch first so the later trash copies and purge path can share one durable batch anchor.
	// 先写入回收批次，再让后续 trash 复制和 purge 路径复用同一个持久批次锚点。
	insertBatchSQL := fmt.Sprintf(`
INSERT INTO %s (recycle_type, project_id, reason, recycled_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4, $4)
RETURNING id
`, s.recycleBatchesTable())
	var batchID uint64
	if err := tx.QueryRow(callCtx, strings.TrimSpace(insertBatchSQL), logicdomain.RecycleTypeColdMemory, sharedRecycleProjectID(rows), reason, recycledAt).Scan(&batchID); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("insert postgres recycle batch: %w", err)
	}

	idArgs := toInt64List(normalizedMemoryIDs)
	insertTrashSQL := fmt.Sprintf(`
INSERT INTO %s (
	batch_id, recycled_at, recycle_reason,
	id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, embedding,
	source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level,
	refresh_weight, support_count, rebuttal_count, status_reason, expires_at, last_recalled_at,
	last_adopted_at, last_reinforced_at, recalled_count, adopted_count, reinforcement_count,
	cross_session_adopted_count, decay_disabled, dedupe_hash, created_at, updated_at
)
SELECT $1, $2, $3,
       id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, embedding,
       source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level,
       refresh_weight, support_count, rebuttal_count, status_reason, expires_at, last_recalled_at,
       last_adopted_at, last_reinforced_at, recalled_count, adopted_count, reinforcement_count,
       cross_session_adopted_count, decay_disabled, dedupe_hash, created_at, updated_at
FROM %s
WHERE id = ANY($4)
`, s.memoryNodesTrashTable(), s.memoryNodesTable())
	insertMemoryTag, err := tx.Exec(callCtx, strings.TrimSpace(insertTrashSQL), batchID, recycledAt, reason, idArgs)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("copy postgres memories into trash: %w", err)
	}

	insertContextTrashSQL := fmt.Sprintf(`
INSERT INTO %s (
	batch_id, recycled_at, recycle_reason,
	memory_id, context_key, context_value, support_count, rebuttal_count,
	last_supported_at, last_rebutted_at, created_at, updated_at
)
SELECT $1, $2, $3,
       memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_at, last_rebutted_at, created_at, updated_at
FROM %s
WHERE memory_id = ANY($4)
`, s.memoryContextEdgesTrashTable(), s.memoryContextEdgesTable())
	insertContextTag, err := tx.Exec(callCtx, strings.TrimSpace(insertContextTrashSQL), batchID, recycledAt, reason, idArgs)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("copy postgres memory context edges into trash: %w", err)
	}

	deleteContextSQL := fmt.Sprintf(`DELETE FROM %s WHERE memory_id = ANY($1)`, s.memoryContextEdgesTable())
	if _, err := tx.Exec(callCtx, deleteContextSQL, idArgs); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("delete postgres memory context edges: %w", err)
	}
	deleteMemorySQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, s.memoryNodesTable())
	if _, err := tx.Exec(callCtx, deleteMemorySQL, idArgs); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("delete postgres cold memories: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("commit postgres cold-memory recycle tx: %w", err)
	}

	return logicdomain.MemoryRecycleResult{
		BatchID:              batchID,
		RecycledMemoryCount:  int(insertMemoryTag.RowsAffected()),
		RecycledContextCount: int(insertContextTag.RowsAffected()),
		RecycledVectorIDs:    normalizeStringList(vectorIDs),
	}, nil
}

// PurgeExpiredMemoryTrash permanently deletes old PostgreSQL trash batches once their soft-backup retention window has elapsed.
// PurgeExpiredMemoryTrash 用于在 PostgreSQL 回收站保留窗口到期后，永久删除旧的垃圾批次。
func (s *Store) PurgeExpiredMemoryTrash(ctx context.Context, before time.Time, limit int) (logicdomain.MemoryTrashPurgeResult, error) {
	if s == nil || s.pool == nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("postgres store is not initialized")
	}
	purgeBefore := chooseNonZeroTime(before, time.Now().UTC())
	limit = normalizeRetentionBatchLimit(limit, 64)

	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("begin postgres trash purge tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	selectBatchSQL := fmt.Sprintf(`
SELECT id
FROM %s
WHERE recycle_type = $1
  AND purged_at IS NULL
  AND recycled_at <= $2
ORDER BY recycled_at ASC, id ASC
LIMIT $3
FOR UPDATE SKIP LOCKED
`, s.recycleBatchesTable())
	rows, err := tx.Query(callCtx, strings.TrimSpace(selectBatchSQL), logicdomain.RecycleTypeColdMemory, purgeBefore, limit)
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("query postgres trash batches: %w", err)
	}
	defer rows.Close()
	batchIDs := make([]uint64, 0, limit)
	for rows.Next() {
		var row recycleBatchLookupRow
		if err := rows.Scan(&row.ID); err != nil {
			return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("scan postgres trash batch id: %w", err)
		}
		batchIDs = append(batchIDs, row.ID)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("iterate postgres trash batches: %w", err)
	}
	normalizedBatchIDs := normalizeUint64List(batchIDs)
	if len(normalizedBatchIDs) == 0 {
		return logicdomain.MemoryTrashPurgeResult{}, nil
	}

	batchArgs := toInt64List(normalizedBatchIDs)
	deleteContextTrashSQL := fmt.Sprintf(`DELETE FROM %s WHERE batch_id = ANY($1)`, s.memoryContextEdgesTrashTable())
	contextTag, err := tx.Exec(callCtx, deleteContextTrashSQL, batchArgs)
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("delete postgres memory-context trash rows: %w", err)
	}
	deleteMemoryTrashSQL := fmt.Sprintf(`DELETE FROM %s WHERE batch_id = ANY($1)`, s.memoryNodesTrashTable())
	memoryTag, err := tx.Exec(callCtx, deleteMemoryTrashSQL, batchArgs)
	if err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("delete postgres memory trash rows: %w", err)
	}
	markPurgedSQL := fmt.Sprintf(`
UPDATE %s
SET purged_at = $2,
    updated_at = $2
WHERE id = ANY($1)
`, s.recycleBatchesTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(markPurgedSQL), batchArgs, time.Now().UTC()); err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("mark postgres recycle batches purged: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.MemoryTrashPurgeResult{}, fmt.Errorf("commit postgres trash purge tx: %w", err)
	}

	return logicdomain.MemoryTrashPurgeResult{
		BatchIDs:           normalizedBatchIDs,
		PurgedMemoryCount:  int(memoryTag.RowsAffected()),
		PurgedContextCount: int(contextTag.RowsAffected()),
	}, nil
}

// appendProtectedSharedMemoryRecycleFilter appends the shared-memory protection predicate so retention does not recycle important shared facts by default.
// appendProtectedSharedMemoryRecycleFilter 用于追加共享记忆保护谓词，避免 retention 默认回收重要共享事实。
func appendProtectedSharedMemoryRecycleFilter(whereClauses []string, args *sqlArgsBuilder, query logicdomain.MemoryRecycleQuery, alias string) []string {
	if args == nil || !query.SkipProtectedSharedMemories {
		return whereClauses
	}
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	sessionScopePlaceholder := args.Add(logicdomain.MemoryScopeLevelSession)
	priorityFloorPlaceholder := args.Add(query.ProtectPriorityFloor)
	levelFloorPlaceholder := args.Add(query.ProtectMemoryLevelFloor)
	whereClauses = append(whereClauses, fmt.Sprintf(
		"NOT (%sscope_level <> %s AND (%spriority <= %s OR %smemory_level >= %s))",
		alias,
		sessionScopePlaceholder,
		alias,
		priorityFloorPlaceholder,
		alias,
		levelFloorPlaceholder,
	))
	return whereClauses
}

// normalizeRetentionBatchLimit keeps recycle and purge limits positive even when callers pass zero or negative values.
// normalizeRetentionBatchLimit 用于在调用方传入零值或负值时，仍把回收和 purge 批量限制保持为正数。
func normalizeRetentionBatchLimit(limit, fallback int) int {
	if limit > 0 {
		return limit
	}
	if fallback > 0 {
		return fallback
	}
	return 1
}

// sharedRecycleProjectID collapses one recycled batch to a single project id only when every recycled row belongs to the same project.
// sharedRecycleProjectID 用于仅在整批回收行都属于同一 project 时保留该 project id，否则回退到 0。
func sharedRecycleProjectID(rows []memoryNodeScanRow) uint64 {
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
