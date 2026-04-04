// dialect_paradedb.go implements the ParadeDB-specific extension validation used by the PostgreSQL dialect-pattern runtime.
// dialect_paradedb.go 用于实现 PostgreSQL 方言模式运行时中的 ParadeDB 扩展校验逻辑。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// paradeDBDialect validates the pg_search and pgvector extensions required by the high-performance ParadeDB flavor.
// paradeDBDialect 用于校验高性能 ParadeDB flavor 需要的 pg_search 与 pgvector 扩展。
type paradeDBDialect struct{}

// Name returns the stable flavor label used by diagnostics and startup errors.
// Name 用于返回诊断信息和启动错误里使用的稳定 flavor 名称。
func (paradeDBDialect) Name() string { return "paradedb" }

// EnsureSearchExtensions verifies the ParadeDB search stack dependencies before the combined runtime starts serving traffic.
// EnsureSearchExtensions 用于在组合运行时开始服务前，校验 ParadeDB 搜索栈依赖是否齐备。
func (paradeDBDialect) EnsureSearchExtensions(ctx context.Context, pool *pgxpool.Pool, autoCreate bool) error {
	return ensureExtensions(ctx, pool, autoCreate, "pg_search", "vector")
}

// EnsureSearchIndexes creates the ParadeDB BM25 index with the explicit jieba tokenizer so Chinese lexical recall can use native Tantivy analysis.
// EnsureSearchIndexes 用于创建 ParadeDB BM25 索引，并显式指定 jieba 分词器，让中文 lexical 召回走原生 Tantivy 分析链路。
func (paradeDBDialect) EnsureSearchIndexes(ctx context.Context, store *Store) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	concurrently := ""
	if store.cfg.BM25IndexConcurrently {
		concurrently = "CONCURRENTLY "
	}
	statement := fmt.Sprintf(`
CREATE INDEX %sIF NOT EXISTS %s ON %s USING bm25 (
	id,
	(abstract::pdb.jieba),
	(details::pdb.jieba),
	team_id,
	space_id,
	project_id,
	user_id,
	origin_session_id,
	source_turn_id,
	memory_status,
	expires_at,
	created_at
) WITH (key_field='id')
`, concurrently, quoteIdentifier(store.cfg.BM25IndexName), store.memoryNodesTable())
	if _, err := store.pool.Exec(ctx, strings.TrimSpace(statement)); err != nil {
		return fmt.Errorf("create paradedb bm25 index: %w", err)
	}
	return nil
}

// BuildLexicalSearchSQL renders the ParadeDB-native lexical recall SQL that uses the @@@ operator plus pdb.parse query builder output.
// BuildLexicalSearchSQL 用于渲染 ParadeDB 原生 lexical 召回 SQL，使用 @@@ 操作符和 pdb.parse 查询构建器。
func (paradeDBDialect) BuildLexicalSearchSQL(store *Store, query string, topK int, filter logicdomain.SearchFilter) (string, []any) {
	args := &sqlArgsBuilder{}
	queryPlaceholder := args.Add(strings.TrimSpace(query))
	limitPlaceholder := args.Add(topK)
	whereClauses := []string{
		activeUnexpiredMemoryCondition("m"),
		"m.id @@@ pdb.parse(" + queryPlaceholder + ", lenient => true)",
	}
	appendScopedMemoryFilter(&whereClauses, args, filter, "m")
	sqlText := fmt.Sprintf(`
SELECT m.id AS memory_id, pdb.score(m.id) AS score
FROM %s AS m
WHERE %s
ORDER BY pdb.score(m.id) DESC, m.id ASC
LIMIT %s
`, store.memoryNodesTable(), strings.Join(whereClauses, " AND "), limitPlaceholder)
	return strings.TrimSpace(sqlText), args.Args()
}

// BuildHybridSearchSQL renders one ParadeDB-first-stage hybrid recall query that fuses pgvector and BM25 candidates inside PostgreSQL before the application rerank/MMR stages run.
// BuildHybridSearchSQL 用于渲染一条 ParadeDB 首轮混合召回 SQL，在进入应用层 rerank/MMR 之前，先在 PostgreSQL 内部融合 pgvector 与 BM25 候选。
func (paradeDBDialect) BuildHybridSearchSQL(store *Store, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) (string, []any) {
	args := &sqlArgsBuilder{}
	vectorPlaceholder := args.Add(encodePGVectorLiteral(vector))
	queryPlaceholder := args.Add(strings.TrimSpace(query))
	limitPlaceholder := args.Add(topK)
	rrfPlaceholder := args.Add(rrfK)

	vectorWhereClauses := []string{activeUnexpiredMemoryCondition("m")}
	appendScopedMemoryFilter(&vectorWhereClauses, args, filter, "m")

	lexicalWhereClauses := []string{
		activeUnexpiredMemoryCondition("m"),
		"m.id @@@ pdb.parse(" + queryPlaceholder + ", lenient => true)",
	}
	appendScopedMemoryFilter(&lexicalWhereClauses, args, filter, "m")

	sqlText := fmt.Sprintf(`
WITH vector_candidates AS (
	SELECT
		m.id AS memory_id,
		m.vector_id,
		m.abstract,
		m.team_id,
		m.space_id,
		m.project_id,
		m.origin_session_id,
		m.user_id,
		COALESCE(m.source_turn_id, 0) AS source_turn_id,
		ROW_NUMBER() OVER (ORDER BY m.embedding <=> %s::vector ASC, m.id ASC) AS vector_rank
	FROM %s AS m
	WHERE %s
	ORDER BY m.embedding <=> %s::vector ASC, m.id ASC
	LIMIT %s
),
lexical_candidates AS (
	SELECT
		m.id AS memory_id,
		m.vector_id,
		m.abstract,
		m.team_id,
		m.space_id,
		m.project_id,
		m.origin_session_id,
		m.user_id,
		COALESCE(m.source_turn_id, 0) AS source_turn_id,
		ROW_NUMBER() OVER (ORDER BY pdb.score(m.id) DESC, m.id ASC) AS lexical_rank
	FROM %s AS m
	WHERE %s
	ORDER BY pdb.score(m.id) DESC, m.id ASC
	LIMIT %s
),
fused_candidates AS (
	SELECT
		COALESCE(v.memory_id, l.memory_id) AS memory_id,
		COALESCE(v.vector_id, l.vector_id) AS vector_id,
		COALESCE(v.abstract, l.abstract) AS abstract,
		COALESCE(v.team_id, l.team_id) AS team_id,
		COALESCE(v.space_id, l.space_id) AS space_id,
		COALESCE(v.project_id, l.project_id) AS project_id,
		COALESCE(v.origin_session_id, l.origin_session_id) AS origin_session_id,
		COALESCE(v.user_id, l.user_id) AS user_id,
		COALESCE(v.source_turn_id, l.source_turn_id, 0) AS source_turn_id,
		CASE
			WHEN v.vector_rank IS NULL THEN 0
			ELSE 1.0 / (%s::double precision + v.vector_rank::double precision)
		END
		+
		CASE
			WHEN l.lexical_rank IS NULL THEN 0
			ELSE 1.0 / (%s::double precision + l.lexical_rank::double precision)
		END AS fused_score,
		CASE
			WHEN v.vector_rank IS NOT NULL AND l.lexical_rank IS NOT NULL THEN 'hybrid_rrf'
			WHEN l.lexical_rank IS NOT NULL THEN 'lexical_search'
			ELSE 'vector_search'
		END AS origin
	FROM vector_candidates AS v
	FULL OUTER JOIN lexical_candidates AS l
		ON l.memory_id = v.memory_id
)
SELECT
	vector_id,
	abstract,
	team_id,
	space_id,
	project_id,
	origin_session_id,
	user_id,
	source_turn_id,
	fused_score,
	origin
FROM fused_candidates
ORDER BY fused_score DESC, memory_id ASC
LIMIT %s
`, vectorPlaceholder, store.memoryNodesTable(), strings.Join(vectorWhereClauses, " AND "), vectorPlaceholder, limitPlaceholder, store.memoryNodesTable(), strings.Join(lexicalWhereClauses, " AND "), limitPlaceholder, rrfPlaceholder, rrfPlaceholder, limitPlaceholder)
	return strings.TrimSpace(sqlText), args.Args()
}
