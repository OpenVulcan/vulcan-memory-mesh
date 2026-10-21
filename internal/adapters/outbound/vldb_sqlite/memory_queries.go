// SQLite durable memory lookup, lexical recall and scope filtering.
// SQLite 适配层长期记忆查询、词法召回与范围过滤。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// LoadMemoryNodesByIDs loads one mixed batch of unified durable memory rows by numeric ids and returns them in ascending id order.
// LoadMemoryNodesByIDs 用于按数字 id 批量读取统一长期记忆行，并按升序返回。
func (s *Store) LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), memoryIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory nodes by ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.toMemoryNodeRecord()
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// LoadMemoryContextEdgesByMemoryIDs loads the contextual evidence rows attached to one memory-id batch so query-time scoring can align candidate memories with the current situation.
// LoadMemoryContextEdgesByMemoryIDs 用于按 memory id 批量加载情境证据行，让查询期打分可以把候选记忆与当前场景对齐。
func (s *Store) LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryContextEdge{}, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryContextEdgeRow](s, ctx, fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, memoryIDPlaceholders), memoryIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory context edges by memory ids: %w", err)
	}
	edges := make([]logicdomain.MemoryContextEdge, 0, len(rows))
	for _, row := range rows {
		edges = append(edges, row.toDomain())
	}
	return edges, nil
}

// LoadMemoryNodesByVectorIDs loads unified durable memory rows by vector ids so vector search hits can be enriched with relational refs.
// LoadMemoryNodesByVectorIDs 用于按 vector id 读取统一长期记忆行，让向量召回结果补全为关系层 ref。
func (s *Store) LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	vectorIDs = storageutil.NormalizeStringList(vectorIDs)
	if len(vectorIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	vectorIDParams := make([]any, 0, len(vectorIDs))
	for _, vectorID := range vectorIDs {
		vectorIDParams = append(vectorIDParams, vectorID)
	}
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE vector_id IN (%s)
  AND %s
ORDER BY created_timestamp ASC, id ASC
`, sqlitePlaceholders(len(vectorIDs)), buildActiveUnexpiredMemoryCondition("", nowMs)), vectorIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory nodes by vector ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.toMemoryNodeRecord()
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// SearchLexicalMemory runs one SQLite FTS recall over durable memory text and returns ranked materialized memory rows for app-layer RRF fusion.
// SearchLexicalMemory 用于在长期记忆文本上执行一次 SQLite FTS 召回，并返回应用层 RRF 融合所需的已排序物化记忆行。
func (s *Store) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	if strings.TrimSpace(query) == "" || topK <= 0 {
		return []logicdomain.MemoryLexicalHit{}, nil
	}
	if topK > memoryLexicalResultLimit {
		topK = memoryLexicalResultLimit
	}
	if s == nil || s.database == nil {
		return nil, fmt.Errorf("sqlite lexical search requires one local ffi database")
	}
	result, err := s.database.SearchFts(ctx, s.ftsIndexName, s.tokenizerMode, strings.TrimSpace(query), uint32(memoryLexicalSearchCandidateLimit(topK)), 0)
	if err != nil {
		return nil, fmt.Errorf("search lexical memory: %w", err)
	}
	candidates := make([]sqliteLexicalCandidate, 0, len(result.Hits))
	candidateMemoryIDs := make([]uint64, 0, len(result.Hits))
	for _, hit := range result.Hits {
		memoryID, ok := storageutil.ParsePositiveUint64(hit.ID)
		if !ok || memoryID == 0 {
			continue
		}
		candidates = append(candidates, sqliteLexicalCandidate{MemoryID: memoryID, Score: hit.Score})
		candidateMemoryIDs = append(candidateMemoryIDs, memoryID)
	}
	activeRowsByID, err := s.loadActiveMemoryNodesByIDs(ctx, candidateMemoryIDs)
	if err != nil {
		return nil, fmt.Errorf("load lexical memory nodes: %w", err)
	}

	filtered := make([]logicdomain.MemoryLexicalHit, 0, len(candidates))
	for _, candidate := range candidates {
		record, found := activeRowsByID[candidate.MemoryID]
		if !found || !matchesLexicalMemoryFilter(record, filter) {
			continue
		}
		filtered = append(filtered, logicdomain.MemoryLexicalHit{
			MemoryID: candidate.MemoryID,
			Record:   record,
			Score:    candidate.Score,
		})
		if len(filtered) >= topK {
			break
		}
	}
	return filtered, nil
}

// sqliteLexicalCandidate stores one parsed FTS hit before relational active-row validation keeps or drops it.
// sqliteLexicalCandidate 用于保存一条已解析的 FTS 命中，等待关系侧 active 行校验决定保留或丢弃。
type sqliteLexicalCandidate struct {
	MemoryID uint64
	Score    float64
}

// memoryLexicalSearchCandidateLimit expands the pre-filter FTS window while preserving the final caller-visible topK cap.
// memoryLexicalSearchCandidateLimit 用于扩大过滤前 FTS 候选窗口，同时保持调用方可见的最终 topK 上限不变。
func memoryLexicalSearchCandidateLimit(topK int) int {
	if topK <= 0 {
		return 0
	}
	limit := topK * 4
	if limit < topK {
		return topK
	}
	if limit > memoryLexicalCandidateLimit {
		return memoryLexicalCandidateLimit
	}
	return limit
}

// FindRecentActiveMemoryByDedupe finds one recent active direct-write memory row inside the same resolved session scope and soft-idempotency window.
// FindRecentActiveMemoryByDedupe 用于在同一已解析 session 范围和软幂等窗口内查找最近的 active 主动写记忆行。
func (s *Store) FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(dedupeHash) == "" {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "dedupe_hash", Message: "is required"}
	}
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE origin_session_id = ?
  AND project_id = ?
  AND user_id = ?
  AND source_kind = ?
  AND scope_level = ?
  AND dedupe_hash = ?
  AND memory_status = ?
  AND (expires_timestamp <= 0 OR expires_timestamp > ?)
  AND created_timestamp >= ?
ORDER BY created_timestamp DESC, id DESC
LIMIT 1
`, session.SessionID, session.ProjectID, session.UserID, sourceKind, scopeLevel, strings.TrimSpace(dedupeHash), logicdomain.MemoryStatusActive, time.Now().UTC().UnixMilli(), notBefore.UTC().UnixMilli())
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, false, fmt.Errorf("query memory dedupe row: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.MemoryNodeRecord{}, false, nil
	}
	record, err := rows[0].toMemoryNodeRecord()
	return record, err == nil, err
}

// buildActiveUnexpiredMemoryCondition renders the SQL predicate shared by hot-path memory reads that should ignore superseded or expired rows.
// buildActiveUnexpiredMemoryCondition 用于渲染热路径记忆查询共用的 SQL 条件，让这类读取自动忽略 superseded 或已过期行。
func buildActiveUnexpiredMemoryCondition(alias string, nowMs int64) string {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	return fmt.Sprintf(`%smemory_status = %d AND (%sexpires_timestamp <= 0 OR %sexpires_timestamp > %d)`, alias, logicdomain.MemoryStatusActive, alias, alias, nowMs)
}

// loadActiveMemoryNodesByIDs loads active and unexpired memory rows for a parsed FTS candidate set in one relational pass.
// loadActiveMemoryNodesByIDs 用于一次性回表加载已解析 FTS 候选集中的 active 且未过期记忆行。
func (s *Store) loadActiveMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) (map[uint64]logicdomain.MemoryNodeRecord, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}

	// Batch the relational validation so stale FTS documents are filtered by the same active/unexpired predicate without issuing one SQLite query per hit.
	// 批量执行关系侧校验，让陈旧 FTS 文档继续通过同一套 active/未过期谓词过滤，同时避免每个命中都单独查询 SQLite。
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s)
  AND %s
ORDER BY id ASC
`, memoryIDPlaceholders, buildActiveUnexpiredMemoryCondition("", time.Now().UTC().UnixMilli())), memoryIDParams...)
	if err != nil {
		return nil, err
	}
	recordsByID := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		record, err := row.toMemoryNodeRecord()
		if err != nil {
			return nil, err
		}
		recordsByID[record.ID] = record
	}
	return recordsByID, nil
}

// matchesLexicalMemoryFilter reuses the relational-memory scope rules to keep lexical recall aligned with vector recall.
// matchesLexicalMemoryFilter 用于复用关系层的记忆作用域规则，让 lexical 召回与向量召回保持一致。
func matchesLexicalMemoryFilter(record logicdomain.MemoryNodeRecord, filter logicdomain.SearchFilter) bool {
	if filter.TeamID > 0 && record.TeamID != filter.TeamID {
		return false
	}
	if filter.SpaceID > 0 && record.SpaceID != filter.SpaceID {
		return false
	}
	if filter.ProjectID > 0 && record.ProjectID != filter.ProjectID {
		return false
	}
	if filter.UserID > 0 && record.UserID != 0 && record.UserID != filter.UserID {
		return false
	}
	if filter.SessionID > 0 && record.OriginSessionID != filter.SessionID {
		return false
	}
	if filter.BoundarySessionID > 0 && record.OriginSessionID == filter.BoundarySessionID {
		if filter.ExcludeBoundaryTurn {
			return record.SourceTurnID == 0
		}
		return record.SourceTurnID == 0 || record.SourceTurnID <= filter.BoundaryMaxTurnID
	}
	return true
}
