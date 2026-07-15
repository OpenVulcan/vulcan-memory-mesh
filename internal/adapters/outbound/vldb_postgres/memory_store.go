// memory_store.go implements the unified durable-memory read/write methods backed by the shared PostgreSQL combined-store table layout.
// memory_store.go 用于实现由 PostgreSQL 组合库统一长期记忆表支撑的记忆读写方法。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LoadMemoryNodesByIDs loads unified durable memory rows by numeric ids while preserving ascending id order for deterministic detail responses.
// LoadMemoryNodesByIDs 用于按数字 id 读取统一长期记忆行，并保持升序返回，保证详情响应可预测。
func (r *memoryRepository) LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.id = ANY($1)
ORDER BY m.id ASC
`, memoryNodeSelectColumns("m"), r.memoryNodesTable())
	rows, err := r.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), toInt64List(memoryIDs))
	if err != nil {
		return nil, fmt.Errorf("query postgres memory nodes by ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// LoadMemoryContextEdgesByMemoryIDs loads the contextual evidence rows attached to one durable-memory batch.
// LoadMemoryContextEdgesByMemoryIDs 用于按长期记忆批次加载附着的情境证据边。
func (r *memoryRepository) LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryContextEdge{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_at, last_rebutted_at, created_at, updated_at
FROM %s
WHERE memory_id = ANY($1)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, r.memoryContextEdgesTable())
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, strings.TrimSpace(sqlText), toInt64List(memoryIDs))
	if err != nil {
		return nil, fmt.Errorf("query postgres memory context edges: %w", err)
	}
	defer rows.Close()

	edges := make([]logicdomain.MemoryContextEdge, 0)
	for rows.Next() {
		var row memoryContextEdgeScanRow
		if err := rows.Scan(
			&row.MemoryID,
			&row.ContextKey,
			&row.ContextValue,
			&row.SupportCount,
			&row.RebuttalCount,
			&row.LastSupportedAt,
			&row.LastRebuttedAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres memory context edge: %w", err)
		}
		edges = append(edges, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres memory context edges: %w", err)
	}
	return edges, nil
}

// LoadMemoryNodesByVectorIDs materializes active durable memory rows by vector ids so semantic recall hits can be enriched with relational metadata.
// LoadMemoryNodesByVectorIDs 用于按 vector id 回表加载 active 长期记忆行，让语义召回结果补齐关系元数据。
func (r *memoryRepository) LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	vectorIDs = storageutil.NormalizeStringList(vectorIDs)
	if len(vectorIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.vector_id = ANY($1)
  AND %s
ORDER BY m.created_at ASC, m.id ASC
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := r.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), vectorIDs)
	if err != nil {
		return nil, fmt.Errorf("query postgres memory nodes by vector ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// SearchLexicalMemory delegates lexical SQL generation to the active dialect and returns materialized active memory rows for app-layer fusion.
// SearchLexicalMemory 用于把 lexical SQL 生成委托给当前方言，并返回应用层融合所需的已物化 active 记忆行。
func (r *memoryRepository) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	query = strings.TrimSpace(query)
	if query == "" || topK <= 0 {
		return []logicdomain.MemoryLexicalHit{}, nil
	}
	if topK > 32 {
		topK = 32
	}
	sqlText, args := r.shared.dialect.BuildLexicalSearchSQL(r, query, topK, filter)
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("search postgres lexical memory: %w", err)
	}
	defer rows.Close()

	hits := make([]logicdomain.MemoryLexicalHit, 0)
	for rows.Next() {
		var (
			row   memoryNodeScanRow
			score float64
		)
		scanTargets := append([]any{&score}, memoryNodeScanDestinations(&row)...)
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, fmt.Errorf("scan postgres lexical hit: %w", err)
		}
		record := row.toMemoryNodeRecord()
		hits = append(hits, logicdomain.MemoryLexicalHit{
			MemoryID: record.ID,
			Record:   record,
			Score:    score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres lexical hits: %w", err)
	}
	return hits, nil
}

// FindRecentActiveMemoryByDedupe loads the newest active direct-write memory inside the same resolved session and dedupe window.
// FindRecentActiveMemoryByDedupe 用于在同一已解析 session 和去重时间窗内加载最新的 active 主动写记忆。
func (r *memoryRepository) FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	if r == nil || r.shared.pool == nil {
		return logicdomain.MemoryNodeRecord{}, false, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	dedupeHash = strings.TrimSpace(dedupeHash)
	if dedupeHash == "" {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "dedupe_hash", Message: "is required"}
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.origin_session_id = $1
  AND m.project_id = $2
  AND m.user_id = $3
  AND m.source_kind = $4
  AND m.scope_level = $5
  AND m.dedupe_hash = $6
  AND %s
  AND m.created_at >= $7
ORDER BY m.created_at DESC, m.id DESC
LIMIT 1
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := r.queryMemoryNodes(ctx, strings.TrimSpace(sqlText),
		int64(session.SessionID),
		int64(session.ProjectID),
		int64(session.UserID),
		sourceKind,
		scopeLevel,
		dedupeHash,
		notBefore.UTC(),
	)
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, false, fmt.Errorf("query postgres memory dedupe row: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.MemoryNodeRecord{}, false, nil
	}
	return rows[0].toMemoryNodeRecord(), true, nil
}

// CreateDirectMemoryNode inserts one fully materialized direct-write durable memory row into the shared combined-store table.
// CreateDirectMemoryNode 用于把一条已完整物化的主动写长期记忆插入共享组合库表。
func (r *memoryRepository) CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if r == nil || r.shared.pool == nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if len(record.Vector) != r.shared.cfg.EmbeddingDimension {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}
	now := time.Now().UTC()
	record = normalizeDirectMemoryNodeRecord(session, record, now)
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()

	// Use an explicit transaction even for the single fallback insert so insert failures remain rollback-safe while commit ambiguity can preserve the fresh vector.
	// 即使 fallback 只有单条插入也使用显式事务，让插入阶段失败保持可回滚，而提交结果不明时可以保留新向量。
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("begin postgres direct memory node tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	created, err := r.scanDirectMemoryNodeInsertRow(callCtx, tx.QueryRow(callCtx, directMemoryNodeInsertSQL(r.memoryNodesTable()), directMemoryNodeInsertArgs(record)...))
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("insert postgres direct memory node: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.MemoryNodeRecord{}, postgresFreshVectorCommitOutcomeUncertainError("create direct memory node", "commit postgres direct memory node tx", err)
	}
	return created, nil
}

// ApplyDirectMemoryWrite atomically inserts one direct-write durable memory row and supersedes any replaced active rows so explicit writes share the same replacement semantics as post-action.
// ApplyDirectMemoryWrite 用于原子写入一条主动长期记忆，并同时 supersede 被其替代的活跃旧行，让显式写入与 post-action 共享同一套替代语义。
func (r *memoryRepository) ApplyDirectMemoryWrite(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	if r == nil || r.shared.pool == nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if len(record.Vector) != r.shared.cfg.EmbeddingDimension {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}

	now := time.Now().UTC()
	record = normalizeDirectMemoryNodeRecord(session, record, now)
	supersededMemoryIDs = storageutil.NormalizeUint64List(supersededMemoryIDs)

	// Keep the insert and the old-row retirement inside one explicit transaction so combined mode never exposes the fresh direct-write memory and the stale rows as simultaneously active.
	// 把新插入和旧行退役都放进一个显式事务，确保组合模式不会同时暴露刚写入的新主动记忆和仍处于 active 的旧行。
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("begin postgres direct memory write tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	supersededVectorIDs, err := r.analysis.loadActiveMemoryVectorIDsTx(callCtx, tx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("load postgres direct-write superseded vector ids: %w", err)
	}
	created, err := r.scanDirectMemoryNodeInsertRow(callCtx, tx.QueryRow(callCtx, directMemoryNodeInsertSQL(r.memoryNodesTable()), directMemoryNodeInsertArgs(record)...))
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("insert postgres direct memory write row: %w", err)
	}
	if len(supersededMemoryIDs) > 0 {
		updateSupersededSQL := buildPostgresMemoryNodesSupersedeSQL(r.memoryNodesTable())
		supersedeTag, err := tx.Exec(
			callCtx,
			strings.TrimSpace(updateSupersededSQL),
			logicdomain.MemoryStatusSuperseded,
			now,
			logicdomain.MemoryStatusActive,
			toInt64List(supersededMemoryIDs),
		)
		if err != nil {
			return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("supersede postgres direct-write memory nodes: %w", err)
		}
		if err := requirePostgresDirectMemoryWriteRowsAffected("supersede postgres direct-write memory nodes", supersedeTag.RowsAffected(), len(supersededMemoryIDs)); err != nil {
			return logicdomain.DirectMemoryWriteApplyResult{}, err
		}
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, postgresFreshVectorCommitOutcomeUncertainError("apply direct memory write", "commit postgres direct memory write tx", err)
	}
	return logicdomain.DirectMemoryWriteApplyResult{
		InsertedMemoryNode:  created,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// buildPostgresMemoryNodesSupersedeSQL builds the shared active-memory retirement statement used by post-action and direct-write replacement paths.
// buildPostgresMemoryNodesSupersedeSQL 用于构造 post-action 与主动写替代路径共享的 active 记忆退役语句。
func buildPostgresMemoryNodesSupersedeSQL(memoryNodesTable string) string {
	return strings.TrimSpace(fmt.Sprintf(`
UPDATE %s
SET memory_status = $1,
    updated_at = $2
WHERE memory_status = $3
  AND id = ANY($4)
`, memoryNodesTable))
}

// requirePostgresDirectMemoryWriteRowsAffected rejects direct-write lifecycle drift before transaction commit, so callers can still roll back the uncommitted fresh memory row.
// requirePostgresDirectMemoryWriteRowsAffected 用于在事务提交前拒绝主动写生命周期行数漂移，使调用方仍可回滚尚未提交的新主动记忆行。
func requirePostgresDirectMemoryWriteRowsAffected(action string, rowsAffected int64, expectedRows int) error {
	return postgresRowsAffectedDriftError(action, rowsAffected, int64(expectedRows))
}

// DeleteMemoryNodes marks active PostgreSQL memory rows as deleted inside the provided hierarchy filter while preserving immutable turn/detail rows.
// DeleteMemoryNodes 用于在给定层级过滤范围内把 PostgreSQL active 记忆行标记为 deleted，同时保留不可变的 turn/detail 行。
func (r *memoryRepository) DeleteMemoryNodes(ctx context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error) {
	if r == nil || r.shared.pool == nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("postgres store is not initialized")
	}
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return logicdomain.MemoryDeleteResult{}, nil
	}

	// Keep row selection and status transition in one transaction so concurrent lifecycle work cannot delete or supersede a different active-row snapshot.
	// 把行选择和状态切换放进同一个事务，避免并发生命周期任务基于不同 active 快照进行删除或替代。
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("begin postgres memory delete tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	args := &sqlArgsBuilder{}
	whereClauses := []string{
		"m.id = ANY(" + args.Add(toInt64List(memoryIDs)) + ")",
		"m.memory_status = " + args.Add(logicdomain.MemoryStatusActive),
	}
	appendScopedMemoryFilter(&whereClauses, args, filter, "m")
	selectSQL := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE %s
ORDER BY m.id ASC
FOR UPDATE
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), strings.Join(whereClauses, " AND "))
	rows, err := r.queryMemoryNodesWithQueryer(callCtx, tx, strings.TrimSpace(selectSQL), args.Args()...)
	if err != nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("query postgres memory nodes for delete: %w", err)
	}

	eligible := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		eligible[record.ID] = record
	}
	deletedMemoryIDs := make([]uint64, 0, len(eligible))
	notFoundMemoryIDs := make([]uint64, 0, len(memoryIDs))
	deletedVectorIDs := make([]string, 0, len(eligible))
	for _, memoryID := range memoryIDs {
		record, ok := eligible[memoryID]
		if !ok {
			notFoundMemoryIDs = append(notFoundMemoryIDs, memoryID)
			continue
		}
		deletedMemoryIDs = append(deletedMemoryIDs, memoryID)
		if strings.TrimSpace(record.VectorID) != "" {
			deletedVectorIDs = append(deletedVectorIDs, strings.TrimSpace(record.VectorID))
		}
	}
	if len(deletedMemoryIDs) == 0 {
		// Let the deferred rollback release the locked read set because no active memory row will be mutated.
		// 因为没有任何 active memory 行会被修改，所以让延迟 rollback 释放已锁定的只读集合即可。
		return logicdomain.MemoryDeleteResult{
			NotFoundMemoryIDs: notFoundMemoryIDs,
		}, nil
	}

	// Flip active rows to deleted instead of physically removing them so source linkage, audit fields, and later retention governance stay intact.
	// 将 active 行切换为 deleted 而不是物理删除，保留来源关联、审计字段和后续 retention 治理所需信息。
	deletedAt = chooseNonZeroTime(deletedAt, time.Now().UTC()).UTC()
	updateSQL := fmt.Sprintf(`
UPDATE %s
SET memory_status = $1,
    status_reason = $2,
    updated_at = $3
WHERE memory_status = $4
  AND id = ANY($5)
`, r.memoryNodesTable())
	if _, err := tx.Exec(
		callCtx,
		strings.TrimSpace(updateSQL),
		logicdomain.MemoryStatusDeleted,
		strings.TrimSpace(reason),
		deletedAt,
		logicdomain.MemoryStatusActive,
		toInt64List(deletedMemoryIDs),
	); err != nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("delete postgres memory nodes: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.MemoryDeleteResult{
			DeletedMemoryIDs:  deletedMemoryIDs,
			NotFoundMemoryIDs: notFoundMemoryIDs,
			DeletedVectorIDs:  storageutil.NormalizeStringList(deletedVectorIDs),
		}, postgresMemoryDeleteCommitOutcomeUncertainError(err)
	}
	return logicdomain.MemoryDeleteResult{
		DeletedMemoryIDs:  deletedMemoryIDs,
		NotFoundMemoryIDs: notFoundMemoryIDs,
		DeletedVectorIDs:  storageutil.NormalizeStringList(deletedVectorIDs),
	}, nil
}

// postgresMemoryDeleteCommitOutcomeUncertainError marks delete commits whose durable memory status flips may already be visible.
// postgresMemoryDeleteCommitOutcomeUncertainError 用于标记长期记忆删除提交失败，此时 durable 状态切换可能已经可见。
func postgresMemoryDeleteCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("delete memory nodes", "commit postgres memory delete tx", err)
}

// pgRowScanner captures the Scan method shared by pgx row implementations so direct-memory upsert helpers can work with both pool and transaction query paths.
// pgRowScanner 用于抽象 pgx 行对象共享的 Scan 方法，让主动记忆 upsert 辅助函数同时适用于连接池和事务查询路径。
type pgRowScanner interface {
	Scan(dest ...any) error
}

// directMemoryNodeInsertSQL returns the shared PostgreSQL INSERT statement used by direct-write paths that must create a new durable memory row.
// directMemoryNodeInsertSQL 用于返回主动写路径共享的 PostgreSQL INSERT 语句，确保每次主动写都创建新的长期记忆行。
func directMemoryNodeInsertSQL(table string) string {
	return strings.TrimSpace(fmt.Sprintf(`
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
`, table, memoryNodeSelectColumns("")))
}

// directMemoryNodeInsertArgs materializes the ordered INSERT arguments once so pool and transaction code paths cannot drift on column order.
// directMemoryNodeInsertArgs 用于一次性生成 INSERT 参数顺序，避免连接池和事务两条代码路径在列顺序上发生漂移。
func directMemoryNodeInsertArgs(record logicdomain.MemoryNodeRecord) []any {
	return []any{
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
	}
}

// scanDirectMemoryNodeInsertRow scans one direct-memory INSERT result row into the shared durable memory record model.
// scanDirectMemoryNodeInsertRow 用于把一条主动记忆 INSERT 返回行扫描成共享的长期记忆记录模型。
func (r *memoryRepository) scanDirectMemoryNodeInsertRow(_ context.Context, row pgRowScanner) (logicdomain.MemoryNodeRecord, error) {
	var scan memoryNodeScanRow
	if err := row.Scan(
		&scan.ID,
		&scan.TeamID,
		&scan.SpaceID,
		&scan.ProjectID,
		&scan.UserID,
		&scan.OriginSessionID,
		&scan.SourceTurnID,
		&scan.VectorID,
		&scan.EmbeddingText,
		&scan.SourceKind,
		&scan.ScopeLevel,
		&scan.Category,
		&scan.Abstract,
		&scan.Details,
		&scan.MemoryStatus,
		&scan.Priority,
		&scan.MemoryLevel,
		&scan.RefreshWeight,
		&scan.SupportCount,
		&scan.RebuttalCount,
		&scan.StatusReason,
		&scan.ExpiresAt,
		&scan.LastRecalledAt,
		&scan.LastAdoptedAt,
		&scan.LastReinforcedAt,
		&scan.RecalledCount,
		&scan.AdoptedCount,
		&scan.ReinforcementCount,
		&scan.CrossSessionAdoptedCount,
		&scan.DecayDisabled,
		&scan.DedupeHash,
		&scan.CreatedAt,
		&scan.UpdatedAt,
	); err != nil {
		return logicdomain.MemoryNodeRecord{}, err
	}
	return scan.toMemoryNodeRecord(), nil
}

// ListProjectMemories returns active unified durable memory rows for one project so shared admin flows can rebuild or migrate recall state deterministically.
// ListProjectMemories 用于返回某个项目下的 active 统一长期记忆行，让共享管理流程可以稳定地重建或迁移召回状态。
func (r *memoryRepository) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	return r.listProjectMemoriesWithQueryerAndContextBuilder(ctx, r.shared.pool, r.queryContext, projectID)
}

// ListProjectMemoriesForMaintenance returns the same active project memories as the online admin path but wraps the scan in the dedicated maintenance read timeout so one-shot rebuild/export tools can read large projects safely.
// ListProjectMemoriesForMaintenance 用于返回与在线管理路径相同的 active 项目记忆，但会套用专用维护读取超时，让一次性重建/导出工具可以安全扫描大项目。
func (r *memoryRepository) ListProjectMemoriesForMaintenance(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	return r.listProjectMemoriesWithQueryerAndContextBuilder(ctx, r.shared.pool, r.maintenanceReadContext, projectID)
}

// listProjectMemoriesWithQueryerAndContextBuilder centralizes project-memory enumeration so online callers keep the normal query timeout while maintenance callers can explicitly opt into the longer maintenance budget.
// listProjectMemoriesWithQueryerAndContextBuilder 用于集中承载项目记忆枚举逻辑，让在线调用方继续使用常规查询超时，而维护调用方可以显式切到更长的维护预算。
func (r *memoryRepository) listProjectMemoriesWithQueryerAndContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc), projectID uint64) ([]logicdomain.MemoryRecord, error) {
	if r == nil || q == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.project_id = $1
  AND %s
ORDER BY m.created_at ASC, m.id ASC
`, memoryNodeSelectColumns("m"), r.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := r.queryMemoryNodesWithContextBuilder(ctx, q, buildContext, strings.TrimSpace(sqlText), int64(projectID))
	if err != nil {
		return nil, fmt.Errorf("list postgres project memories: %w", err)
	}
	out := make([]logicdomain.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		out = append(out, memoryRecordFromNode(record))
	}
	return out, nil
}

// ReplaceMemoryVectors rewrites one durable-memory batch's inline embedding column so combined PostgreSQL mode can fully rebuild vectors without replaying business writes.
// ReplaceMemoryVectors 用于重写一批长期记忆行的内联 embedding 列，让组合 PostgreSQL 模式可以在不重放业务写入的前提下完整重建向量。
func (r *memoryRepository) ReplaceMemoryVectors(ctx context.Context, records []logicdomain.MemoryRecord) error {
	if r == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if len(records) == 0 {
		return nil
	}

	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres vector rebuild tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	sqlText := buildReplaceMemoryVectorSQL(r.memoryNodesTable())
	for idx, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].id", idx), Message: "is required"}
		}
		if len(record.Vector) != r.shared.cfg.EmbeddingDimension {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].vector", idx), Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
		}
		tag, execErr := tx.Exec(callCtx, sqlText, encodePGVectorLiteral(record.Vector), vectorID)
		if execErr != nil {
			return fmt.Errorf("replace postgres memory vector %s: %w", vectorID, execErr)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("replace postgres memory vector %s affected %d rows", vectorID, tag.RowsAffected())
		}
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresMemoryVectorReplaceCommitOutcomeUncertainError(err)
	}
	return nil
}

// postgresMemoryVectorReplaceCommitOutcomeUncertainError marks maintenance vector-rewrite commits whose inline embeddings may already be durable.
// postgresMemoryVectorReplaceCommitOutcomeUncertainError 用于标记维护向量重写提交失败，此时内联 embedding 可能已经持久化。
func postgresMemoryVectorReplaceCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("replace memory vectors", "commit postgres vector rebuild tx", err)
}

// buildReplaceMemoryVectorSQL returns the narrow maintenance UPDATE used to rewrite one combined-store memory row's inline embedding payload by stable vector_id.
// buildReplaceMemoryVectorSQL 用于返回狭窄维护 UPDATE 语句，让组合库存储按稳定的 vector_id 重写单条记忆行的内联 embedding 载荷。
func buildReplaceMemoryVectorSQL(table string) string {
	return strings.TrimSpace(fmt.Sprintf(`
UPDATE %s
SET embedding = $1::vector
WHERE vector_id = $2
`, table))
}

// queryMemoryNodes executes one PostgreSQL memory query and decodes the rows into scan structs shared by multiple memory-facing methods.
// queryMemoryNodes 用于执行 PostgreSQL 记忆查询，并把结果解码为多个记忆方法共享的扫描结构。
func (r *memoryRepository) queryMemoryNodes(ctx context.Context, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	return r.queryMemoryNodesWithQueryer(ctx, r.shared.pool, sqlText, args...)
}

// queryMemoryNodesWithQueryer executes one PostgreSQL memory query against either the pool or a transaction so lifecycle updates can safely read locked rows.
// queryMemoryNodesWithQueryer 用于在连接池或事务上执行 PostgreSQL 记忆查询，让生命周期更新可以安全读取已加锁的行。
func (r *memoryRepository) queryMemoryNodesWithQueryer(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	return r.queryMemoryNodesWithContextBuilder(ctx, q, r.queryContext, sqlText, args...)
}

// queryMemoryNodesWithContextBuilder executes one PostgreSQL memory query against either the pool or a transaction while letting callers choose the timeout policy that wraps the SQL work.
// queryMemoryNodesWithContextBuilder 用于在连接池或事务上执行 PostgreSQL 记忆查询，并允许调用方选择包裹该 SQL 工作的超时策略。
func (r *memoryRepository) queryMemoryNodesWithContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc), sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	if buildContext == nil {
		buildContext = r.queryContext
	}
	callCtx, cancel := buildContext(ctx)
	defer cancel()
	return scanMemoryNodeRows(callCtx, q, sqlText, args...)
}

// memoryNodeSelectColumns keeps every memory-row query aligned on one stable column order so scanning remains deterministic.
// memoryNodeSelectColumns 用于让所有记忆行查询共享稳定的列顺序，确保扫描过程保持确定性。
func memoryNodeSelectColumns(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	return strings.Join([]string{
		alias + "id",
		alias + "team_id",
		alias + "space_id",
		alias + "project_id",
		alias + "user_id",
		alias + "origin_session_id",
		alias + "source_turn_id",
		alias + "vector_id",
		alias + "embedding::text AS embedding_text",
		alias + "source_kind",
		alias + "scope_level",
		alias + "category",
		alias + "abstract",
		alias + "details",
		alias + "memory_status",
		alias + "priority",
		alias + "memory_level",
		alias + "refresh_weight",
		alias + "support_count",
		alias + "rebuttal_count",
		alias + "status_reason",
		alias + "expires_at",
		alias + "last_recalled_at",
		alias + "last_adopted_at",
		alias + "last_reinforced_at",
		alias + "recalled_count",
		alias + "adopted_count",
		alias + "reinforcement_count",
		alias + "cross_session_adopted_count",
		alias + "decay_disabled",
		alias + "dedupe_hash",
		alias + "created_at",
		alias + "updated_at",
	}, ", ")
}

// nullableUint64 returns nil for zero-valued optional numeric identifiers so PostgreSQL can persist SQL NULL instead of a sentinel zero.
// nullableUint64 用于在可选数字标识为零时返回 nil，让 PostgreSQL 持久化 SQL NULL 而不是哨兵零值。
func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return int64(value)
}

// LoadMemoryNodesByIDs delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// LoadMemoryNodesByIDs 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.LoadMemoryNodesByIDs(ctx, memoryIDs)
}

// LoadMemoryContextEdgesByMemoryIDs delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// LoadMemoryContextEdgesByMemoryIDs 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.LoadMemoryContextEdgesByMemoryIDs(ctx, memoryIDs)
}

// LoadMemoryNodesByVectorIDs delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// LoadMemoryNodesByVectorIDs 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.LoadMemoryNodesByVectorIDs(ctx, vectorIDs)
}

// SearchLexicalMemory delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// SearchLexicalMemory 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.SearchLexicalMemory(ctx, query, topK, filter)
}

// FindRecentActiveMemoryByDedupe delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// FindRecentActiveMemoryByDedupe 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.FindRecentActiveMemoryByDedupe(ctx, session, sourceKind, scopeLevel, dedupeHash, notBefore)
}

// CreateDirectMemoryNode delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// CreateDirectMemoryNode 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.CreateDirectMemoryNode(ctx, session, record)
}

// ApplyDirectMemoryWrite delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// ApplyDirectMemoryWrite 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ApplyDirectMemoryWrite(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.ApplyDirectMemoryWrite(ctx, session, record, supersededMemoryIDs)
}

// DeleteMemoryNodes delegates explicit memory-node deletion to the memory repository so combined PostgreSQL storage updates the owning relational row.
// DeleteMemoryNodes 用于把显式记忆条目删除委托给 memory repository，让 PostgreSQL 组合存储更新拥有该记忆的关系行。
func (s *Store) DeleteMemoryNodes(ctx context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.DeleteMemoryNodes(ctx, memoryIDs, filter, deletedAt, reason)
}

// ListProjectMemories delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// ListProjectMemories 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.ListProjectMemories(ctx, projectID)
}

// ListProjectMemoriesForMaintenance delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// ListProjectMemoriesForMaintenance 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ListProjectMemoriesForMaintenance(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.ListProjectMemoriesForMaintenance(ctx, projectID)
}

// ReplaceMemoryVectors delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// ReplaceMemoryVectors 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ReplaceMemoryVectors(ctx context.Context, records []logicdomain.MemoryRecord) error {
	s.ensureMemoryRepo()
	return s.repos.memory.ReplaceMemoryVectors(ctx, records)
}

// queryMemoryNodes delegates to the memory repository so internal callers can still execute memory queries through the Store facade.
// queryMemoryNodes 用于让内部调用方仍能通过 Store 门面执行记忆查询。
func (s *Store) queryMemoryNodes(ctx context.Context, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.queryMemoryNodes(ctx, sqlText, args...)
}

// queryMemoryNodesWithQueryer delegates to the memory repository so internal callers can execute memory queries against a specific queryer (pool or transaction).
// queryMemoryNodesWithQueryer 用于让内部调用方仍能针对特定查询器（连接池或事务）执行记忆查询。
func (s *Store) queryMemoryNodesWithQueryer(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.queryMemoryNodesWithQueryer(ctx, q, sqlText, args...)
}

// listProjectMemoriesWithQueryerAndContextBuilder delegates to the memory repository so internal callers and tests can still use the shared project-memory listing helper through the Store facade.
// listProjectMemoriesWithQueryerAndContextBuilder 用于让内部调用方和测试仍能通过 Store 门面使用共享的项目记忆枚举辅助方法。
func (s *Store) listProjectMemoriesWithQueryerAndContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc), projectID uint64) ([]logicdomain.MemoryRecord, error) {
	s.ensureMemoryRepo()
	return s.repos.memory.listProjectMemoriesWithQueryerAndContextBuilder(ctx, q, buildContext, projectID)
}

// ensureMemoryRepo lazily wires a memoryRepository from Store compatibility mirrors so tests that construct bare Store literals via cfg/pool continue to work.
// ensureMemoryRepo 用于从 Store 兼容镜像延迟组装 memoryRepository，让通过 cfg/pool 字面量构造的轻量测试仍能正常工作。
func (s *Store) ensureMemoryRepo() {
	if s.repos.memory.shared == nil {
		s.repos.memory.shared = &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}
		s.repos.memory.analysis = analysisRepository{shared: s.repos.memory.shared}
	}
}
