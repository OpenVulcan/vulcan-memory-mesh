// dialect_standard.go implements the standard PostgreSQL extension validation used by the cloud-compatible fallback flavor.
// dialect_standard.go 用于实现云环境兼容的 standard flavor 所需的 PostgreSQL 扩展校验逻辑。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// standardDialect validates the pg_trgm and pgvector extensions required by the standard PostgreSQL fallback flavor.
// standardDialect 用于校验 standard PostgreSQL 兜底 flavor 需要的 pg_trgm 与 pgvector 扩展。
type standardDialect struct{}

// Name returns the stable flavor label used by diagnostics and startup errors.
// Name 用于返回诊断信息和启动错误里使用的稳定 flavor 名称。
func (standardDialect) Name() string { return "standard" }

// EnsureSearchExtensions verifies the standard PostgreSQL fallback search dependencies before the combined runtime starts serving traffic.
// EnsureSearchExtensions 用于在组合运行时开始服务前，校验 standard PostgreSQL 兜底搜索依赖是否齐备。
func (standardDialect) EnsureSearchExtensions(ctx context.Context, pool *pgxpool.Pool, autoCreate bool) error {
	return ensureExtensions(ctx, pool, autoCreate, "pg_trgm", "vector")
}

// EnsureSearchIndexes creates the trigram GIN indexes used by the standard PostgreSQL fallback flavor for Chinese-friendly fuzzy lexical recall.
// EnsureSearchIndexes 用于创建 trigram GIN 索引，服务 standard PostgreSQL 兜底 flavor 的中文友好模糊 lexical 召回。
func (standardDialect) EnsureSearchIndexes(ctx context.Context, store *Store) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	for _, statement := range standardTrigramIndexStatements(store) {
		if _, err := store.pool.Exec(ctx, strings.TrimSpace(statement.sql)); err != nil {
			return postgresDDLStatementExecutionError("create standard postgres trigram index", statement, err)
		}
	}
	return nil
}

// standardTrigramIndexStatements renders the standard PostgreSQL lexical index DDL with stable names for precise startup diagnostics.
// standardTrigramIndexStatements 用于渲染 standard PostgreSQL lexical 索引 DDL，并提供稳定名称以便启动诊断精确定位。
func standardTrigramIndexStatements(store *Store) []postgresDDLStatement {
	return []postgresDDLStatement{
		{name: "idx_vmm_memory_nodes_abstract_trgm", sql: fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING GIN (abstract gin_trgm_ops)`, quoteIdentifier("idx_vmm_memory_nodes_abstract_trgm"), store.memoryNodesTable())},
		{name: "idx_vmm_memory_nodes_details_trgm", sql: fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING GIN (details gin_trgm_ops)`, quoteIdentifier("idx_vmm_memory_nodes_details_trgm"), store.memoryNodesTable())},
	}
}

// BuildLexicalSearchSQL renders the pg_trgm-based lexical fallback SQL that combines ILIKE recall with similarity scoring.
// BuildLexicalSearchSQL 用于渲染基于 pg_trgm 的 lexical 兜底 SQL，把 ILIKE 召回与 similarity 打分组合起来。
func (standardDialect) BuildLexicalSearchSQL(r memoryTableResolver, query string, topK int, filter logicdomain.SearchFilter) (string, []any) {
	args := &sqlArgsBuilder{}
	queryPlaceholder := args.Add(strings.TrimSpace(query))
	thresholdPlaceholder := args.Add(r.trgmSimilarityThreshold())
	limitPlaceholder := args.Add(topK)
	likeExpr := "'%' || " + queryPlaceholder + " || '%'"
	whereClauses := []string{
		activeUnexpiredMemoryCondition("m"),
		`(
			m.abstract ILIKE ` + likeExpr + `
			OR m.details ILIKE ` + likeExpr + `
			OR similarity(m.abstract, ` + queryPlaceholder + `) >= ` + thresholdPlaceholder + `
			OR similarity(m.details, ` + queryPlaceholder + `) >= ` + thresholdPlaceholder + `
		)`,
	}
	appendScopedMemoryFilter(&whereClauses, args, filter, "m")
	sqlText := fmt.Sprintf(`
SELECT GREATEST(similarity(m.abstract, %s), similarity(m.details, %s)) AS score,
       %s
FROM %s AS m
WHERE %s
ORDER BY score DESC, m.id ASC
LIMIT %s
`, queryPlaceholder, queryPlaceholder, memoryNodeSelectColumns("m"), r.memoryNodesTable(), strings.Join(whereClauses, " AND "), limitPlaceholder)
	return strings.TrimSpace(sqlText), args.Args()
}

// BuildHybridSearchSQL renders one standard-PostgreSQL first-stage hybrid recall query that fuses pgvector and trigram candidates inside SQL before application-side rerank/MMR continues.
// BuildHybridSearchSQL 用于渲染一条 standard PostgreSQL 首轮混合召回 SQL，在应用层 rerank/MMR 继续处理前，先在 SQL 内融合 pgvector 与 trigram 候选。
func (standardDialect) BuildHybridSearchSQL(r memoryTableResolver, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) (string, []any) {
	args := &sqlArgsBuilder{}
	vectorPlaceholder := args.Add(encodePGVectorLiteral(vector))
	queryPlaceholder := args.Add(strings.TrimSpace(query))
	thresholdPlaceholder := args.Add(r.trgmSimilarityThreshold())
	limitPlaceholder := args.Add(topK)
	rrfPlaceholder := args.Add(rrfK)
	likeExpr := "'%' || " + queryPlaceholder + " || '%'"

	vectorWhereClauses := []string{activeUnexpiredMemoryCondition("m")}
	appendScopedMemoryFilter(&vectorWhereClauses, args, filter, "m")

	lexicalWhereClauses := []string{
		activeUnexpiredMemoryCondition("m"),
		`(
			m.abstract ILIKE ` + likeExpr + `
			OR m.details ILIKE ` + likeExpr + `
			OR similarity(m.abstract, ` + queryPlaceholder + `) >= ` + thresholdPlaceholder + `
			OR similarity(m.details, ` + queryPlaceholder + `) >= ` + thresholdPlaceholder + `
		)`,
	}
	appendScopedMemoryFilter(&lexicalWhereClauses, args, filter, "m")

	sqlText := fmt.Sprintf(`
WITH vector_candidates AS (
	SELECT
		m.id AS memory_id,
		ROW_NUMBER() OVER (ORDER BY m.embedding <=> %s::vector ASC, m.id ASC) AS vector_rank
	FROM %s AS m
	WHERE %s
	ORDER BY m.embedding <=> %s::vector ASC, m.id ASC
	LIMIT %s
),
lexical_candidates AS (
	SELECT
		m.id AS memory_id,
		ROW_NUMBER() OVER (
			ORDER BY GREATEST(similarity(m.abstract, %s), similarity(m.details, %s)) DESC, m.id ASC
		) AS lexical_rank
	FROM %s AS m
	WHERE %s
	ORDER BY GREATEST(similarity(m.abstract, %s), similarity(m.details, %s)) DESC, m.id ASC
	LIMIT %s
),
fused_candidates AS (
	SELECT
		COALESCE(v.memory_id, l.memory_id) AS memory_id,
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
	%s,
	f.fused_score,
	f.origin
FROM fused_candidates AS f
JOIN %s AS m
	ON m.id = f.memory_id
ORDER BY f.fused_score DESC, f.memory_id ASC
LIMIT %s
`, vectorPlaceholder, r.memoryNodesTable(), strings.Join(vectorWhereClauses, " AND "), vectorPlaceholder, limitPlaceholder, queryPlaceholder, queryPlaceholder, r.memoryNodesTable(), strings.Join(lexicalWhereClauses, " AND "), queryPlaceholder, queryPlaceholder, limitPlaceholder, rrfPlaceholder, rrfPlaceholder, memoryNodeSelectColumns("m"), r.memoryNodesTable(), limitPlaceholder)
	return strings.TrimSpace(sqlText), args.Args()
}
