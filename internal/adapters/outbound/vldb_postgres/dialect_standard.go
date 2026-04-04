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
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING GIN (abstract gin_trgm_ops)`, quoteIdentifier("idx_vmm_memory_nodes_abstract_trgm"), store.memoryNodesTable()),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s USING GIN (details gin_trgm_ops)`, quoteIdentifier("idx_vmm_memory_nodes_details_trgm"), store.memoryNodesTable()),
	}
	for _, statement := range statements {
		if _, err := store.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("create standard postgres trigram index: %w", err)
		}
	}
	return nil
}

// BuildLexicalSearchSQL renders the pg_trgm-based lexical fallback SQL that combines ILIKE recall with similarity scoring.
// BuildLexicalSearchSQL 用于渲染基于 pg_trgm 的 lexical 兜底 SQL，把 ILIKE 召回与 similarity 打分组合起来。
func (standardDialect) BuildLexicalSearchSQL(store *Store, query string, topK int, filter logicdomain.SearchFilter) (string, []any) {
	args := &sqlArgsBuilder{}
	queryPlaceholder := args.Add(strings.TrimSpace(query))
	thresholdPlaceholder := args.Add(store.cfg.TRGMSimilarityThreshold)
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
SELECT m.id AS memory_id,
       GREATEST(similarity(m.abstract, %s), similarity(m.details, %s)) AS score
FROM %s AS m
WHERE %s
ORDER BY score DESC, m.id ASC
LIMIT %s
`, queryPlaceholder, queryPlaceholder, store.memoryNodesTable(), strings.Join(whereClauses, " AND "), limitPlaceholder)
	return strings.TrimSpace(sqlText), args.Args()
}
