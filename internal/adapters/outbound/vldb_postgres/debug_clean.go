// debug_clean.go implements destructive PostgreSQL managed-schema cleanup helpers used by debug-only maintenance commands.
// debug_clean.go 用于实现调试专用的 PostgreSQL 受管 schema 清理辅助逻辑。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DebugCleanManagedSchema connects to PostgreSQL, drops every VMM-managed table inside the configured schema, and returns without booting runtime seeds.
// DebugCleanManagedSchema 用于连接 PostgreSQL，删除目标 schema 下所有 VMM 受管表，并在不启动运行时种子逻辑的情况下返回。
func DebugCleanManagedSchema(ctx context.Context, cfg Config) error {
	cfg.DSN = strings.TrimSpace(cfg.DSN)
	cfg.Schema = strings.TrimSpace(cfg.Schema)
	if cfg.DSN == "" {
		return fmt.Errorf("postgres dsn is required")
	}
	if cfg.Schema == "" {
		cfg.Schema = "public"
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return fmt.Errorf("parse postgres dsn for debug clean: %w", err)
	}
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	callCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(callCtx, poolCfg)
	if err != nil {
		return fmt.Errorf("dial postgres for debug clean: %w", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(callCtx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, quoteIdentifier(cfg.Schema))); err != nil {
		return fmt.Errorf("ensure postgres debug-clean schema: %w", err)
	}
	for _, tableName := range []string{
		"vmm_profile_instructions",
		"vmm_profile_nodes",
		"vmm_memory_context_edges",
		"vmm_memory_nodes",
		"vmm_turn_records",
		"vmm_sessions",
		"vmm_projects",
		"vmm_spaces",
		"vmm_teams",
		"vmm_users",
		"vmm_noise_embeddings",
		"vmm_schema_versions",
	} {
		statement := fmt.Sprintf(`DROP TABLE IF EXISTS %s.%s CASCADE`, quoteIdentifier(cfg.Schema), quoteIdentifier(tableName))
		if _, err := pool.Exec(callCtx, statement); err != nil {
			return fmt.Errorf("drop postgres managed table %s: %w", tableName, err)
		}
	}
	return nil
}
