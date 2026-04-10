// retention_store.go implements PostgreSQL retention recycle and trash purge helpers for the combined runtime.
// retention_store.go 用于实现组合运行时下 PostgreSQL 的 retention 回收与回收站清理逻辑。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// recycleBatchLookupRow stores one recycle-batch identifier selected for one trash purge pass.
// recycleBatchLookupRow 用于保存某次回收站清理中选中的回收批次标识。
type recycleBatchLookupRow struct {
	ID uint64
}

// postgresIdleSessionRecycleResult stores one single-session recycle outcome before the outer loop aggregates multiple session batches.
// postgresIdleSessionRecycleResult 用于保存单个 session 回收结果，再由外层循环聚合成多 session 批次统计。
type postgresIdleSessionRecycleResult struct {
	BatchID              uint64
	SessionID            uint64
	RecycledMemoryCount  int
	RecycledContextCount int
	RecycledTurnCount    int
	RecycledVectorIDs    []string
}

// RecycleColdMemories transactionally moves terminal durable memories and their context edges into PostgreSQL trash tables, then removes them from the hot tables.
// RecycleColdMemories 用于在一个 PostgreSQL 事务里把终态长期记忆及其情境边迁入回收站，并从热表删除。
func (r *retentionRepository) RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("postgres store is not initialized")
	}
	limit := normalizeRetentionBatchLimit(query.Limit, 128)
	recycledAt := chooseNonZeroTime(query.RecycledAt, time.Now().UTC())
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonColdTerminalMemory
	}

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
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
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), strings.Join(whereClauses, " AND "), limitPlaceholder)
	rows, err := (&memoryRepository{shared: r.shared}).queryMemoryNodesWithQueryer(callCtx, tx, selectSQL, args.Args()...)
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
`, r.recycleBatchesTable())
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
`, r.memoryNodesTrashTable(), r.memoryNodesTable())
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
`, r.memoryContextEdgesTrashTable(), r.memoryContextEdgesTable())
	insertContextTag, err := tx.Exec(callCtx, strings.TrimSpace(insertContextTrashSQL), batchID, recycledAt, reason, idArgs)
	if err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("copy postgres memory context edges into trash: %w", err)
	}

	deleteContextSQL := fmt.Sprintf(`DELETE FROM %s WHERE memory_id = ANY($1)`, r.memoryContextEdgesTable())
	if _, err := tx.Exec(callCtx, deleteContextSQL, idArgs); err != nil {
		return logicdomain.MemoryRecycleResult{}, fmt.Errorf("delete postgres memory context edges: %w", err)
	}
	deleteMemorySQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, r.memoryNodesTable())
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

// RecycleIdleSessions compacts long-idle PostgreSQL sessions by moving stale session memories and eligible old turns into trash tables before removing them from the hot tables.
// RecycleIdleSessions 用于压缩长期空闲的 PostgreSQL session，把陈旧 session 记忆和符合条件的旧 turn 迁入回收站，再从热表删除。
func (r *retentionRepository) RecycleIdleSessions(ctx context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.SessionIdleRecycleResult{}, fmt.Errorf("postgres store is not initialized")
	}
	limit := normalizeRetentionBatchLimit(query.Limit, 32)
	recycledAt := chooseNonZeroTime(query.RecycledAt, time.Now().UTC())
	idleBefore := chooseNonZeroTime(query.IdleBefore, time.Now().UTC())
	turnHotWindowSize := normalizeRetentionTurnHotWindowSize(query.TurnHotWindowSize)
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonIdleSessionCompact
	}
	return collectPostgresIdleSessionRecyclePass(limit, func(excludedSessionIDs []uint64) (postgresIdleSessionRecycleResult, error) {
		return r.recycleOnePostgresIdleSession(ctx, idleBefore, turnHotWindowSize, recycledAt, reason, excludedSessionIDs)
	})
}

// collectPostgresIdleSessionRecyclePass aggregates one PostgreSQL idle-session recycle pass while skipping no-op sessions inside the same scan so one inert oldest session cannot starve later recyclable work.
// collectPostgresIdleSessionRecyclePass 用于聚合一次 PostgreSQL idle-session 回收过程，并在同一轮扫描内跳过 no-op session，避免一个最老但无可回收内容的 session 饿死后续真正可回收的批次。
func collectPostgresIdleSessionRecyclePass(limit int, recycleOne func(excludedSessionIDs []uint64) (postgresIdleSessionRecycleResult, error)) (logicdomain.SessionIdleRecycleResult, error) {
	result := logicdomain.SessionIdleRecycleResult{}
	if limit <= 0 {
		return result, nil
	}
	excludedSessionIDs := make([]uint64, 0, limit)
	inspectionBudget := postgresIdleSessionInspectionBudget(limit)
	inspectedSessionCount := 0
	for len(result.BatchIDs) < limit && inspectedSessionCount < inspectionBudget {
		sessionResult, recycleErr := recycleOne(excludedSessionIDs)
		if recycleErr != nil {
			result.BatchIDs = normalizeUint64List(result.BatchIDs)
			result.SessionIDs = normalizeUint64List(result.SessionIDs)
			result.RecycledVectorIDs = normalizeStringList(result.RecycledVectorIDs)
			return result, recycleErr
		}
		if sessionResult.BatchID == 0 {
			if sessionResult.SessionID == 0 {
				break
			}
			inspectedSessionCount++
			excludedSessionIDs = append(excludedSessionIDs, sessionResult.SessionID)
			excludedSessionIDs = normalizeUint64List(excludedSessionIDs)
			continue
		}
		inspectedSessionCount++
		result.BatchIDs = append(result.BatchIDs, sessionResult.BatchID)
		result.SessionIDs = append(result.SessionIDs, sessionResult.SessionID)
		result.RecycledMemoryCount += sessionResult.RecycledMemoryCount
		result.RecycledContextCount += sessionResult.RecycledContextCount
		result.RecycledTurnCount += sessionResult.RecycledTurnCount
		result.RecycledVectorIDs = append(result.RecycledVectorIDs, sessionResult.RecycledVectorIDs...)
	}
	result.BatchIDs = normalizeUint64List(result.BatchIDs)
	result.SessionIDs = normalizeUint64List(result.SessionIDs)
	result.RecycledVectorIDs = normalizeStringList(result.RecycledVectorIDs)
	return result, nil
}

// postgresIdleSessionInspectionBudget gives each recycle pass a small bounded amount of extra inspection headroom so a few race-window no-op sessions do not starve later real work, without turning one maintenance pass into an unbounded scan.
// postgresIdleSessionInspectionBudget 用于为每次回收提供少量有界的额外检查预算，让少数并发窗口里的 no-op session 不会饿死后续真实可回收工作，同时避免把单轮维护放大成无界扫描。
func postgresIdleSessionInspectionBudget(limit int) int {
	if limit <= 0 {
		return 0
	}
	return limit + 4
}

// PurgeExpiredTrash permanently deletes old PostgreSQL trash batches once their soft-backup retention window has elapsed, including the batch metadata row itself so recycle bookkeeping does not grow forever after the backup window ends.
// PurgeExpiredTrash 用于在 PostgreSQL 回收站保留窗口到期后，永久删除旧的垃圾批次，并一并删除批次元数据行，避免软备份窗口结束后回收台账无限增长。
func (r *retentionRepository) PurgeExpiredTrash(ctx context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("postgres store is not initialized")
	}
	purgeBefore := chooseNonZeroTime(before, time.Now().UTC())
	limit = normalizeRetentionBatchLimit(limit, 64)

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("begin postgres trash purge tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	selectBatchSQL := fmt.Sprintf(`
SELECT id
FROM %s
WHERE purged_at IS NULL
  AND recycled_at <= $1
ORDER BY recycled_at ASC, id ASC
LIMIT $2
FOR UPDATE SKIP LOCKED
`, r.recycleBatchesTable())
	rows, err := tx.Query(callCtx, strings.TrimSpace(selectBatchSQL), purgeBefore, limit)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("query postgres trash batches: %w", err)
	}
	defer rows.Close()
	batchIDs := make([]uint64, 0, limit)
	for rows.Next() {
		var row recycleBatchLookupRow
		if err := rows.Scan(&row.ID); err != nil {
			return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("scan postgres trash batch id: %w", err)
		}
		batchIDs = append(batchIDs, row.ID)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("iterate postgres trash batches: %w", err)
	}
	normalizedBatchIDs := normalizeUint64List(batchIDs)
	if len(normalizedBatchIDs) == 0 {
		return logicdomain.RetentionTrashPurgeResult{}, nil
	}

	batchArgs := toInt64List(normalizedBatchIDs)
	deleteContextTrashSQL := fmt.Sprintf(`DELETE FROM %s WHERE batch_id = ANY($1)`, r.memoryContextEdgesTrashTable())
	contextTag, err := tx.Exec(callCtx, deleteContextTrashSQL, batchArgs)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("delete postgres memory-context trash rows: %w", err)
	}
	deleteMemoryTrashSQL := fmt.Sprintf(`DELETE FROM %s WHERE batch_id = ANY($1)`, r.memoryNodesTrashTable())
	memoryTag, err := tx.Exec(callCtx, deleteMemoryTrashSQL, batchArgs)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("delete postgres memory trash rows: %w", err)
	}
	deleteTurnTrashSQL := fmt.Sprintf(`DELETE FROM %s WHERE batch_id = ANY($1)`, r.turnsTrashTable())
	turnTag, err := tx.Exec(callCtx, deleteTurnTrashSQL, batchArgs)
	if err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("delete postgres turn trash rows: %w", err)
	}
	deleteBatchSQL := buildPostgresRecycleBatchDeleteSQL(r.recycleBatchesTable())
	if _, err := tx.Exec(callCtx, deleteBatchSQL, batchArgs); err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("delete postgres recycle batch metadata: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.RetentionTrashPurgeResult{}, fmt.Errorf("commit postgres trash purge tx: %w", err)
	}

	return logicdomain.RetentionTrashPurgeResult{
		BatchIDs:           normalizedBatchIDs,
		PurgedMemoryCount:  int(memoryTag.RowsAffected()),
		PurgedContextCount: int(contextTag.RowsAffected()),
		PurgedTurnCount:    int(turnTag.RowsAffected()),
	}, nil
}

// buildPostgresRecycleBatchDeleteSQL builds the final metadata-delete statement used after one purge pass has already detached every trash row in the selected batch set.
// buildPostgresRecycleBatchDeleteSQL 用于构建 purge 末尾删除批次元数据的 SQL；调用点会先确保该批次对应的所有 trash 行已被移除。
func buildPostgresRecycleBatchDeleteSQL(table string) string {
	return fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, table)
}

// recycleOnePostgresIdleSession locks and compacts one concrete long-idle session so concurrent workers and foreground writers cannot split one recycle batch across overlapping hot-table states.
// recycleOnePostgresIdleSession 用于锁定并压缩单个长期空闲 session，避免并发工作器和前台写入把一次回收批次切割到重叠的热表状态。
func (r *retentionRepository) recycleOnePostgresIdleSession(ctx context.Context, idleBefore time.Time, turnHotWindowSize int, recycledAt time.Time, reason string, excludedSessionIDs []uint64) (postgresIdleSessionRecycleResult, error) {
	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("begin postgres idle-session recycle tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	sessionArgs := &sqlArgsBuilder{}
	sessionWhereClauses := []string{
		"s.updated_at <= " + sessionArgs.Add(idleBefore),
		fmt.Sprintf(`NOT EXISTS (
    SELECT 1
    FROM %s tr
    WHERE tr.session_id = s.id
      AND tr.extracted_status = %s
  )`, r.turnsTable(), sessionArgs.Add(logicdomain.TurnExtractedStatusPending)),
		buildPostgresIdleSessionCandidateAvailabilityClause(sessionArgs, r, idleBefore, turnHotWindowSize),
	}
	if len(excludedSessionIDs) > 0 {
		sessionWhereClauses = append(sessionWhereClauses, "s.id <> ALL("+sessionArgs.Add(toInt64List(excludedSessionIDs))+")")
	}
	sessionSQL := fmt.Sprintf(`
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_at, last_extract_completed_at, last_compacted_at,
       created_at, updated_at
FROM %s AS s
WHERE %s
ORDER BY s.updated_at ASC, s.id ASC
LIMIT 1
FOR UPDATE SKIP LOCKED
`, r.sessionsTable(), strings.Join(sessionWhereClauses, "\n  AND "))
	var session sessionScanRow
	if err := tx.QueryRow(callCtx, strings.TrimSpace(sessionSQL), sessionArgs.Args()...).Scan(
		&session.ID,
		&session.SessionKey,
		&session.UserID,
		&session.TeamID,
		&session.SpaceID,
		&session.ProjectID,
		&session.TurnCount,
		&session.LastSummarizedID,
		&session.LastCompactedTurnID,
		&session.SummarizeContent,
		&session.SummarizeBudget,
		&session.LastExtractObservedAt,
		&session.LastExtractCompletedAt,
		&session.LastCompactedAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return postgresIdleSessionRecycleResult{}, nil
		}
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("query postgres idle session candidate: %w", err)
	}

	memorySQL := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.origin_session_id = $1
  AND m.scope_level = $2
  AND m.memory_status = $3
  AND m.expires_at IS NOT NULL
  AND m.expires_at <= $4
  AND GREATEST(
    COALESCE(m.last_recalled_at, TIMESTAMPTZ 'epoch'),
    COALESCE(m.last_adopted_at, TIMESTAMPTZ 'epoch'),
    COALESCE(m.last_reinforced_at, TIMESTAMPTZ 'epoch'),
    m.created_at
  ) <= $4
ORDER BY m.updated_at ASC, m.id ASC
FOR UPDATE
`, memoryNodeSelectColumns("m"), r.memoryNodesTable())
	memoryRows, err := (&memoryRepository{shared: r.shared}).queryMemoryNodesWithQueryer(callCtx, tx, strings.TrimSpace(memorySQL), int64(session.ID), logicdomain.MemoryScopeLevelSession, logicdomain.MemoryStatusActive, idleBefore)
	if err != nil {
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("query postgres idle-session memories for session %d: %w", session.ID, err)
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

	contextCount := 0
	if len(normalizedMemoryIDs) > 0 {
		contextCountSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE memory_id = ANY($1)`, r.memoryContextEdgesTable())
		if err := tx.QueryRow(callCtx, contextCountSQL, toInt64List(normalizedMemoryIDs)).Scan(&contextCount); err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("count postgres idle-session context edges for session %d: %w", session.ID, err)
		}
	}

	turnArgs := &sqlArgsBuilder{}
	turnWhereClauses := []string{
		"tr.session_id = " + turnArgs.Add(int64(session.ID)),
		"tr.extracted_status <> " + turnArgs.Add(logicdomain.TurnExtractedStatusPending),
	}
	recentTurnsCTE := fmt.Sprintf(`
WITH recent_turns AS (
	SELECT id
	FROM %s
	WHERE session_id = %s
	ORDER BY id DESC
	LIMIT %d
)
`, r.turnsTable(), turnArgs.Add(int64(session.ID)), turnHotWindowSize)
	turnWhereClauses = append(turnWhereClauses, "tr.id NOT IN (SELECT id FROM recent_turns)")
	turnWhereClauses = append(turnWhereClauses, buildPostgresIdleSessionTurnReferenceClause(turnArgs, r.memoryNodesTable(), normalizedMemoryIDs))
	turnWhereClauses = append(turnWhereClauses, fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM %s pn WHERE pn.turn_id = tr.id)`, r.profileNodesTable()))
	turnSQL := fmt.Sprintf(`
%s
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM %s AS tr
WHERE %s
ORDER BY tr.id ASC
`, strings.TrimSpace(recentTurnsCTE), r.turnsTable(), strings.Join(turnWhereClauses, " AND "))
	turnRows, err := (&turnRepository{shared: r.shared}).queryTurnRecordsWithQueryer(callCtx, tx, strings.TrimSpace(turnSQL), turnArgs.Args()...)
	if err != nil {
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("query postgres idle-session turns for session %d: %w", session.ID, err)
	}
	if len(normalizedMemoryIDs) == 0 && len(turnRows) == 0 {
		return postgresIdleSessionRecycleResult{SessionID: session.ID}, nil
	}

	insertBatchSQL := fmt.Sprintf(`
INSERT INTO %s (recycle_type, session_id, project_id, reason, recycled_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5, $5)
RETURNING id
`, r.recycleBatchesTable())
	var batchID uint64
	if err := tx.QueryRow(callCtx, strings.TrimSpace(insertBatchSQL), logicdomain.RecycleTypeSessionIdle, int64(session.ID), int64(session.ProjectID), reason, recycledAt).Scan(&batchID); err != nil {
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("insert postgres idle-session recycle batch for session %d: %w", session.ID, err)
	}

	recycledMemoryCount := 0
	recycledTurnCount := 0
	if len(normalizedMemoryIDs) > 0 {
		idArgs := toInt64List(normalizedMemoryIDs)
		insertMemoryTrashSQL := fmt.Sprintf(`
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
`, r.memoryNodesTrashTable(), r.memoryNodesTable())
		memoryTag, err := tx.Exec(callCtx, strings.TrimSpace(insertMemoryTrashSQL), batchID, recycledAt, reason, idArgs)
		if err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("copy postgres idle-session memories into trash for session %d: %w", session.ID, err)
		}
		recycledMemoryCount = int(memoryTag.RowsAffected())

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
`, r.memoryContextEdgesTrashTable(), r.memoryContextEdgesTable())
		if _, err := tx.Exec(callCtx, strings.TrimSpace(insertContextTrashSQL), batchID, recycledAt, reason, idArgs); err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("copy postgres idle-session memory context edges into trash for session %d: %w", session.ID, err)
		}
		deleteContextSQL := fmt.Sprintf(`DELETE FROM %s WHERE memory_id = ANY($1)`, r.memoryContextEdgesTable())
		if _, err := tx.Exec(callCtx, deleteContextSQL, idArgs); err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("delete postgres idle-session memory context edges for session %d: %w", session.ID, err)
		}
		deleteMemorySQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, r.memoryNodesTable())
		if _, err := tx.Exec(callCtx, deleteMemorySQL, idArgs); err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("delete postgres idle-session memories for session %d: %w", session.ID, err)
		}
	}

	if len(turnRows) > 0 {
		turnIDs := make([]uint64, 0, len(turnRows))
		for _, row := range turnRows {
			turnIDs = append(turnIDs, row.ID)
		}
		normalizedTurnIDs := normalizeUint64List(turnIDs)
		turnArgs := toInt64List(normalizedTurnIDs)
		insertTurnTrashSQL := fmt.Sprintf(`
INSERT INTO %s (
	batch_id, recycled_at, recycle_reason,
	id, session_id, project_id, dehydrated_content, dehydrated_budget,
	extracted_status, details, details_budget, created_at, updated_at
)
SELECT $1, $2, $3,
       id, session_id, project_id, dehydrated_content, dehydrated_budget,
       extracted_status, details, details_budget, created_at, updated_at
FROM %s
WHERE id = ANY($4)
`, r.turnsTrashTable(), r.turnsTable())
		turnTag, err := tx.Exec(callCtx, strings.TrimSpace(insertTurnTrashSQL), batchID, recycledAt, reason, turnArgs)
		if err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("copy postgres idle-session turns into trash for session %d: %w", session.ID, err)
		}
		recycledTurnCount = int(turnTag.RowsAffected())
		deleteTurnSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, r.turnsTable())
		if _, err := tx.Exec(callCtx, deleteTurnSQL, turnArgs); err != nil {
			return postgresIdleSessionRecycleResult{}, fmt.Errorf("delete postgres idle-session turns for session %d: %w", session.ID, err)
		}
	}

	if err := tx.Commit(callCtx); err != nil {
		return postgresIdleSessionRecycleResult{}, fmt.Errorf("commit postgres idle-session recycle tx for session %d: %w", session.ID, err)
	}
	return postgresIdleSessionRecycleResult{
		BatchID:              batchID,
		SessionID:            session.ID,
		RecycledMemoryCount:  recycledMemoryCount,
		RecycledContextCount: contextCount,
		RecycledTurnCount:    recycledTurnCount,
		RecycledVectorIDs:    normalizeStringList(vectorIDs),
	}, nil
}

// buildPostgresIdleSessionCandidateAvailabilityClause prefilters idle-session candidates to only sessions that already expose recyclable stale session memories or old turns, so no-op oldest sessions do not block later useful work.
// buildPostgresIdleSessionCandidateAvailabilityClause 用于为 idle-session 候选追加“确实存在可回收数据”的预过滤，避免最老但无可回收内容的 session 阻塞后续真正有收益的回收工作。
func buildPostgresIdleSessionCandidateAvailabilityClause(args *sqlArgsBuilder, r *retentionRepository, idleBefore time.Time, turnHotWindowSize int) string {
	if args == nil || r == nil {
		return "TRUE"
	}
	staleMemoryIdleBeforePlaceholder := args.Add(idleBefore)
	staleMemoryScopePlaceholder := args.Add(logicdomain.MemoryScopeLevelSession)
	staleMemoryStatusPlaceholder := args.Add(logicdomain.MemoryStatusActive)
	staleMemoryFreshnessPlaceholder := args.Add(idleBefore)
	oldTurnPendingStatusPlaceholder := args.Add(logicdomain.TurnExtractedStatusPending)
	return fmt.Sprintf(`(
EXISTS (
  SELECT 1
  FROM %s AS m
  WHERE m.origin_session_id = s.id
    AND m.scope_level = %s
    AND m.memory_status = %s
    AND m.expires_at IS NOT NULL
    AND m.expires_at <= %s
    AND GREATEST(
      COALESCE(m.last_recalled_at, TIMESTAMPTZ 'epoch'),
      COALESCE(m.last_adopted_at, TIMESTAMPTZ 'epoch'),
      COALESCE(m.last_reinforced_at, TIMESTAMPTZ 'epoch'),
      m.created_at
    ) <= %s
)
OR EXISTS (
  WITH recent_turns AS (
    SELECT id
    FROM %s
    WHERE session_id = s.id
    ORDER BY id DESC
    LIMIT %d
  )
  SELECT 1
  FROM %s AS tr
  WHERE tr.session_id = s.id
    AND tr.extracted_status <> %s
    AND tr.id NOT IN (SELECT id FROM recent_turns)
    AND NOT EXISTS (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)
    AND NOT EXISTS (SELECT 1 FROM %s pn WHERE pn.turn_id = tr.id)
)
 )`, r.memoryNodesTable(), staleMemoryScopePlaceholder, staleMemoryStatusPlaceholder, staleMemoryIdleBeforePlaceholder, staleMemoryFreshnessPlaceholder, r.turnsTable(), turnHotWindowSize, r.turnsTable(), oldTurnPendingStatusPlaceholder, r.memoryNodesTable(), r.profileNodesTable())
}

// buildPostgresIdleSessionCandidateAvailabilityClauseForTest provides a Store-compatible wrapper so legacy unit tests can still call the helper without constructing a repository literal.
// buildPostgresIdleSessionCandidateAvailabilityClauseForTest 用于为旧单元测试提供 Store 兼容包装，让测试无需构造 repository 字面量即可调用该辅助函数。
func (s *Store) buildPostgresIdleSessionCandidateAvailabilityClauseForTest(args *sqlArgsBuilder, idleBefore time.Time, turnHotWindowSize int) string {
	return buildPostgresIdleSessionCandidateAvailabilityClause(args, &retentionRepository{shared: &storeShared{cfg: s.cfg}}, idleBefore, turnHotWindowSize)
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

// buildPostgresIdleSessionTurnReferenceClause renders the turn-reference predicate used by idle-session recycle, optionally ignoring the memory rows scheduled for deletion in the same recycle batch.
// buildPostgresIdleSessionTurnReferenceClause 用于渲染 idle-session 回收里的 turn 引用谓词，并可选忽略同批将被删除的记忆行。
func buildPostgresIdleSessionTurnReferenceClause(args *sqlArgsBuilder, memoryTable string, recycledMemoryIDs []uint64) string {
	if args == nil {
		return fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)`, memoryTable)
	}
	if len(recycledMemoryIDs) == 0 {
		return fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)`, memoryTable)
	}
	recycledPlaceholder := args.Add(toInt64List(recycledMemoryIDs))
	return fmt.Sprintf(`NOT EXISTS (
SELECT 1
FROM %s mn
WHERE mn.source_turn_id = tr.id
  AND NOT (mn.id = ANY(%s))
)`, memoryTable, recycledPlaceholder)
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

// normalizeRetentionTurnHotWindowSize keeps the runtime hot-window size non-negative even when callers pass an invalid value.
// normalizeRetentionTurnHotWindowSize 用于在调用方传入非法值时，仍把运行时 turn 热窗口保持为非负数。
func normalizeRetentionTurnHotWindowSize(size int) int {
	if size > 0 {
		return size
	}
	return 0
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
