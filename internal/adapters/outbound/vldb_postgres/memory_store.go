// memory_store.go implements the unified durable-memory read/write methods backed by the shared PostgreSQL combined-store table layout.
// memory_store.go 用于实现由 PostgreSQL 组合库统一长期记忆表支撑的记忆读写方法。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LoadMemoryNodesByIDs loads unified durable memory rows by numeric ids while preserving ascending id order for deterministic detail responses.
// LoadMemoryNodesByIDs 用于按数字 id 读取统一长期记忆行，并保持升序返回，保证详情响应可预测。
func (s *Store) LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.id = ANY($1)
ORDER BY m.id ASC
`, memoryNodeSelectColumns("m"), s.memoryNodesTable())
	rows, err := s.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), toInt64List(memoryIDs))
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
func (s *Store) LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryContextEdge{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_at, last_rebutted_at, created_at, updated_at
FROM %s
WHERE memory_id = ANY($1)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, s.memoryContextEdgesTable())
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(callCtx, strings.TrimSpace(sqlText), toInt64List(memoryIDs))
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
func (s *Store) LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	vectorIDs = normalizeStringList(vectorIDs)
	if len(vectorIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.vector_id = ANY($1)
  AND %s
ORDER BY m.created_at ASC, m.id ASC
`, memoryNodeSelectColumns("m"), s.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := s.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), vectorIDs)
	if err != nil {
		return nil, fmt.Errorf("query postgres memory nodes by vector ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// SearchLexicalMemory delegates lexical SQL generation to the active dialect so ParadeDB and standard PostgreSQL can share one relational materialization flow.
// SearchLexicalMemory 用于把 lexical SQL 生成委托给当前方言，让 ParadeDB 与 standard PostgreSQL 共享同一条关系回表链路。
func (s *Store) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	query = strings.TrimSpace(query)
	if query == "" || topK <= 0 {
		return []logicdomain.MemoryLexicalHit{}, nil
	}
	if topK > 32 {
		topK = 32
	}
	sqlText, args := s.dialect.BuildLexicalSearchSQL(s, query, topK, filter)
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(callCtx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("search postgres lexical memory: %w", err)
	}
	defer rows.Close()

	hits := make([]logicdomain.MemoryLexicalHit, 0)
	for rows.Next() {
		var (
			memoryID uint64
			score    float64
		)
		if err := rows.Scan(&memoryID, &score); err != nil {
			return nil, fmt.Errorf("scan postgres lexical hit: %w", err)
		}
		hits = append(hits, logicdomain.MemoryLexicalHit{
			MemoryID: memoryID,
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
func (s *Store) FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	if s == nil || s.pool == nil {
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
`, memoryNodeSelectColumns("m"), s.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := s.queryMemoryNodes(ctx, strings.TrimSpace(sqlText),
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

// CreateDirectMemoryNode upserts one fully materialized direct-write durable memory row into the shared combined-store table.
// CreateDirectMemoryNode 用于把一条已完整物化的主动写长期记忆 upsert 到共享组合库表中。
func (s *Store) CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if s == nil || s.pool == nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if len(record.Vector) != s.cfg.EmbeddingDimension {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", s.cfg.EmbeddingDimension)}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}
	now := time.Now().UTC()
	record = normalizeDirectMemoryNodeRecord(session, record, now)
	vectorLiteral := encodePGVectorLiteral(record.Vector)
	sqlText := fmt.Sprintf(`
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
ON CONFLICT (vector_id)
DO UPDATE SET
	team_id = EXCLUDED.team_id,
	space_id = EXCLUDED.space_id,
	project_id = EXCLUDED.project_id,
	user_id = EXCLUDED.user_id,
	origin_session_id = EXCLUDED.origin_session_id,
	source_turn_id = EXCLUDED.source_turn_id,
	embedding = EXCLUDED.embedding,
	source_kind = EXCLUDED.source_kind,
	scope_level = EXCLUDED.scope_level,
	category = EXCLUDED.category,
	abstract = EXCLUDED.abstract,
	details = EXCLUDED.details,
	memory_status = EXCLUDED.memory_status,
	priority = EXCLUDED.priority,
	memory_level = EXCLUDED.memory_level,
	refresh_weight = EXCLUDED.refresh_weight,
	support_count = EXCLUDED.support_count,
	rebuttal_count = EXCLUDED.rebuttal_count,
	status_reason = EXCLUDED.status_reason,
	expires_at = EXCLUDED.expires_at,
	last_recalled_at = EXCLUDED.last_recalled_at,
	last_adopted_at = EXCLUDED.last_adopted_at,
	last_reinforced_at = EXCLUDED.last_reinforced_at,
	recalled_count = EXCLUDED.recalled_count,
	adopted_count = EXCLUDED.adopted_count,
	reinforcement_count = EXCLUDED.reinforcement_count,
	cross_session_adopted_count = EXCLUDED.cross_session_adopted_count,
	decay_disabled = EXCLUDED.decay_disabled,
	dedupe_hash = EXCLUDED.dedupe_hash,
	updated_at = EXCLUDED.updated_at
RETURNING %s
`, s.memoryNodesTable(), memoryNodeSelectColumns(""))
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()

	var row memoryNodeScanRow
	if err := s.pool.QueryRow(
		callCtx,
		strings.TrimSpace(sqlText),
		int64(record.TeamID),
		int64(record.SpaceID),
		int64(record.ProjectID),
		int64(record.UserID),
		int64(record.OriginSessionID),
		nullableUint64(record.SourceTurnID),
		record.VectorID,
		vectorLiteral,
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
		&row.ID,
		&row.TeamID,
		&row.SpaceID,
		&row.ProjectID,
		&row.UserID,
		&row.OriginSessionID,
		&row.SourceTurnID,
		&row.VectorID,
		&row.EmbeddingText,
		&row.SourceKind,
		&row.ScopeLevel,
		&row.Category,
		&row.Abstract,
		&row.Details,
		&row.MemoryStatus,
		&row.Priority,
		&row.MemoryLevel,
		&row.RefreshWeight,
		&row.SupportCount,
		&row.RebuttalCount,
		&row.StatusReason,
		&row.ExpiresAt,
		&row.LastRecalledAt,
		&row.LastAdoptedAt,
		&row.LastReinforcedAt,
		&row.RecalledCount,
		&row.AdoptedCount,
		&row.ReinforcementCount,
		&row.CrossSessionAdoptedCount,
		&row.DecayDisabled,
		&row.DedupeHash,
		&row.CreatedAt,
		&row.UpdatedAt,
	); err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("upsert postgres direct memory node: %w", err)
	}
	return row.toMemoryNodeRecord(), nil
}

// ListProjectMemories returns active unified durable memory rows for one project so shared admin flows can rebuild or migrate recall state deterministically.
// ListProjectMemories 用于返回某个项目下的 active 统一长期记忆行，让共享管理流程可以稳定地重建或迁移召回状态。
func (s *Store) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT %s
FROM %s AS m
WHERE m.project_id = $1
  AND %s
ORDER BY m.created_at ASC, m.id ASC
`, memoryNodeSelectColumns("m"), s.memoryNodesTable(), activeUnexpiredMemoryCondition("m"))
	rows, err := s.queryMemoryNodes(ctx, strings.TrimSpace(sqlText), int64(projectID))
	if err != nil {
		return nil, fmt.Errorf("list postgres project memories: %w", err)
	}
	out := make([]logicdomain.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		filter := logicdomain.SearchFilter{
			UserID:    record.UserID,
			TeamID:    record.TeamID,
			SpaceID:   record.SpaceID,
			ProjectID: record.ProjectID,
		}
		if record.SourceKind == logicdomain.MemorySourceKindTurnExtract || record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			filter.SessionID = record.OriginSessionID
		}
		out = append(out, logicdomain.MemoryRecord{
			ID:           record.VectorID,
			Text:         record.Abstract,
			Vector:       append([]float32(nil), record.Vector...),
			Filter:       filter,
			SourceTurnID: record.SourceTurnID,
			Metadata:     memoryRecordMetadataFromNode(record),
			CreatedAt:    record.CreatedAt,
		})
	}
	return out, nil
}

// queryMemoryNodes executes one PostgreSQL memory query and decodes the rows into scan structs shared by multiple memory-facing methods.
// queryMemoryNodes 用于执行 PostgreSQL 记忆查询，并把结果解码为多个记忆方法共享的扫描结构。
func (s *Store) queryMemoryNodes(ctx context.Context, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	return s.queryMemoryNodesWithQueryer(ctx, s.pool, sqlText, args...)
}

// queryMemoryNodesWithQueryer executes one PostgreSQL memory query against either the pool or a transaction so lifecycle updates can safely read locked rows.
// queryMemoryNodesWithQueryer 用于在连接池或事务上执行 PostgreSQL 记忆查询，让生命周期更新可以安全读取已加锁的行。
func (s *Store) queryMemoryNodesWithQueryer(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := q.Query(callCtx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]memoryNodeScanRow, 0)
	for rows.Next() {
		var row memoryNodeScanRow
		if err := rows.Scan(
			&row.ID,
			&row.TeamID,
			&row.SpaceID,
			&row.ProjectID,
			&row.UserID,
			&row.OriginSessionID,
			&row.SourceTurnID,
			&row.VectorID,
			&row.EmbeddingText,
			&row.SourceKind,
			&row.ScopeLevel,
			&row.Category,
			&row.Abstract,
			&row.Details,
			&row.MemoryStatus,
			&row.Priority,
			&row.MemoryLevel,
			&row.RefreshWeight,
			&row.SupportCount,
			&row.RebuttalCount,
			&row.StatusReason,
			&row.ExpiresAt,
			&row.LastRecalledAt,
			&row.LastAdoptedAt,
			&row.LastReinforcedAt,
			&row.RecalledCount,
			&row.AdoptedCount,
			&row.ReinforcementCount,
			&row.CrossSessionAdoptedCount,
			&row.DecayDisabled,
			&row.DedupeHash,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres memory node: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres memory nodes: %w", err)
	}
	return items, nil
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
