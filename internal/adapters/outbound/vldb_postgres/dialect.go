// dialect.go defines the minimal search-dialect contract used to keep the PostgreSQL combined-store core shared while routing flavor-specific extension checks.
// dialect.go 用于定义最小搜索方言契约，让 PostgreSQL 组合库核心逻辑保持共享，同时路由 flavor 特有的扩展检查。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// searchDialect describes the narrow lexical-search and index-initialization behavior that differs between the shared PostgreSQL flavors.
// searchDialect 用于描述共享 PostgreSQL 各个 flavor 之间狭窄但必要的 lexical 检索和索引初始化差异。
type searchDialect interface {
	Name() string
	EnsureSearchExtensions(ctx context.Context, pool *pgxpool.Pool, autoCreate bool) error
	EnsureSearchIndexes(ctx context.Context, store *Store) error
	BuildLexicalSearchSQL(store *Store, query string, topK int, filter logicdomain.SearchFilter) (string, []any)
	BuildHybridSearchSQL(store *Store, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) (string, []any)
}

// newSearchDialect resolves the configured flavor into one concrete dialect implementation.
// newSearchDialect 用于把配置指定的 flavor 解析成具体的方言实现。
func newSearchDialect(flavor string) searchDialect {
	switch strings.ToLower(strings.TrimSpace(flavor)) {
	case "standard":
		return standardDialect{}
	default:
		return paradeDBDialect{}
	}
}

// ensureExtensions verifies one list of required extensions and optionally creates the missing ones before startup continues.
// ensureExtensions 用于校验一组必需扩展，并在允许自动创建时先补齐缺失扩展，再继续启动流程。
func ensureExtensions(ctx context.Context, pool *pgxpool.Pool, autoCreate bool, extensions ...string) error {
	if pool == nil {
		return fmt.Errorf("postgres pool is not initialized")
	}
	for _, extension := range extensions {
		extension = strings.TrimSpace(extension)
		if extension == "" {
			continue
		}
		if autoCreate {
			if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE EXTENSION IF NOT EXISTS %s`, quoteIdentifier(extension))); err != nil {
				return fmt.Errorf("create extension %s: %w", extension, err)
			}
		}
		var found bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, extension).Scan(&found); err != nil {
			return fmt.Errorf("check extension %s: %w", extension, err)
		}
		if !found {
			return fmt.Errorf("required extension %s is not installed", extension)
		}
	}
	return nil
}
