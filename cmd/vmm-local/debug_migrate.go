// debug_migrate.go implements the debug-only split-to-combined migration entrypoint exposed by the local binary.
// debug_migrate.go 用于实现本地二进制暴露的调试专用 split-to-combined 迁移入口。
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_postgres"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
)

const (
	// debugMigrateSplitToCombined is the currently supported maintenance action that replays the split SQLite fact store into the PostgreSQL combined store.
	// debugMigrateSplitToCombined 用于表示当前支持的运维动作：把 split 模式 SQLite 事实库回放到 PostgreSQL 组合库。
	debugMigrateSplitToCombined = "split-to-combined"
)

// parseDebugMigrateTarget normalizes the maintenance selector so unsupported migration actions fail before any storage connection is opened.
// parseDebugMigrateTarget 用于规范化迁移动作选择器，让不受支持的动作在建立任何存储连接前就失败。
func parseDebugMigrateTarget(raw string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(raw))
	switch target {
	case debugMigrateSplitToCombined:
		return target, nil
	case "":
		return "", fmt.Errorf("debug-migrate target is required: use %s", debugMigrateSplitToCombined)
	default:
		return "", fmt.Errorf("unsupported debug-migrate target %q: use %s", raw, debugMigrateSplitToCombined)
	}
}

// runDebugMigrate exports the split SQLite fact snapshot and imports it into the PostgreSQL combined store without starting the gRPC runtime.
// runDebugMigrate 用于导出 split 模式 SQLite 事实快照并导入 PostgreSQL 组合库，且不会启动 gRPC 运行时。
func runDebugMigrate(ctx context.Context, cfg config.Config, target string) error {
	target, err := parseDebugMigrateTarget(target)
	if err != nil {
		return err
	}
	switch target {
	case debugMigrateSplitToCombined:
		return runSplitToCombinedMigration(ctx, cfg)
	default:
		return fmt.Errorf("unsupported debug-migrate target %q", target)
	}
}

// runSplitToCombinedMigration treats SQLite as the only migration fact source, parses vectors in Go memory, and writes them straight into PostgreSQL native vector columns.
// runSplitToCombinedMigration 用于把 SQLite 作为唯一迁移事实源，在 Go 内存解析向量后直接写入 PostgreSQL 原生向量列。
func runSplitToCombinedMigration(ctx context.Context, cfg config.Config) error {
	if strings.TrimSpace(cfg.SQLite.Address) == "" {
		return fmt.Errorf("sqlite.address is required for debug-migrate split-to-combined")
	}
	postgresCfg, err := buildDebugPostgresConfig(cfg)
	if err != nil {
		return err
	}

	snapshot, err := vldb_sqlite.DebugExportManagedSnapshot(ctx, cfg.SQLite.Address, cfg.SQLite.Timeout.Duration, cfg.Postgres.MigrationBatchSize)
	if err != nil {
		return fmt.Errorf("export split sqlite snapshot: %w", err)
	}
	report, err := vldb_postgres.DebugImportManagedSnapshot(ctx, postgresCfg, snapshot)
	if err != nil {
		return fmt.Errorf("import snapshot into postgres combined store: %w", err)
	}

	// Print one stable summary so operators can audit exactly how many rows moved without digging through database internals.
	// 输出一份稳定汇总，让运维人员无需翻数据库内部即可审计本次迁移了多少行数据。
	fmt.Printf("[vmm-debug-migrate] target=%s flavor=%s schema=%s total_rows=%d\n", debugMigrateSplitToCombined, postgresCfg.Flavor, postgresCfg.Schema, report.TotalRows())
	for _, line := range formatMigrationReportLines(report) {
		fmt.Printf("[vmm-debug-migrate] %s\n", line)
	}
	return nil
}

// buildDebugPostgresConfig extracts the PostgreSQL combined-store settings required by destructive maintenance commands without depending on storage.mode routing.
// buildDebugPostgresConfig 用于提取破坏性维护命令所需的 PostgreSQL 组合库配置，而不依赖 storage.mode 的运行时路由。
func buildDebugPostgresConfig(cfg config.Config) (vldb_postgres.Config, error) {
	postgresCfg := vldb_postgres.Config{
		DSN:                     cfg.Postgres.DSN,
		Schema:                  cfg.Postgres.Schema,
		Flavor:                  cfg.Postgres.Flavor,
		QueryTimeout:            cfg.Postgres.QueryTimeout.Duration,
		ConnectTimeout:          cfg.Postgres.ConnectTimeout.Duration,
		MaxOpenConns:            cfg.Postgres.MaxOpenConns,
		MinIdleConns:            cfg.Postgres.MinIdleConns,
		AutoCreateExtensions:    cfg.Postgres.AutoCreateExtensions,
		BM25IndexConcurrently:   cfg.Postgres.BM25IndexConcurrently,
		BM25IndexName:           cfg.Postgres.BM25IndexName,
		TRGMSimilarityThreshold: cfg.Postgres.TRGMSimilarityThreshold,
		VectorLists:             cfg.Postgres.VectorLists,
		VectorProbes:            cfg.Postgres.VectorProbes,
		MigrationBatchSize:      cfg.Postgres.MigrationBatchSize,
		EmbeddingDimension:      cfg.Embedding.Dimension,
	}
	if strings.TrimSpace(postgresCfg.DSN) == "" {
		return vldb_postgres.Config{}, fmt.Errorf("postgres.dsn is required for debug PostgreSQL maintenance")
	}
	if strings.TrimSpace(postgresCfg.Schema) == "" {
		return vldb_postgres.Config{}, fmt.Errorf("postgres.schema is required for debug PostgreSQL maintenance")
	}
	if postgresCfg.EmbeddingDimension <= 0 {
		return vldb_postgres.Config{}, fmt.Errorf("embedding.dimension must be > 0 for debug PostgreSQL maintenance")
	}
	return postgresCfg, nil
}

// formatMigrationReportLines renders a stable per-table summary so automation and manual operators can both read the migration result quickly.
// formatMigrationReportLines 用于渲染稳定的逐表汇总，方便自动化和人工运维都能快速阅读迁移结果。
func formatMigrationReportLines(report storagemigrate.Report) []string {
	return []string{
		fmt.Sprintf("noise_embeddings=%d", report.NoiseEmbeddings),
		fmt.Sprintf("users=%d", report.Users),
		fmt.Sprintf("teams=%d", report.Teams),
		fmt.Sprintf("spaces=%d", report.Spaces),
		fmt.Sprintf("projects=%d", report.Projects),
		fmt.Sprintf("sessions=%d", report.Sessions),
		fmt.Sprintf("turns=%d", report.Turns),
		fmt.Sprintf("memory_nodes=%d", report.MemoryNodes),
		fmt.Sprintf("memory_context_edges=%d", report.MemoryContextEdges),
		fmt.Sprintf("profile_nodes=%d", report.ProfileNodes),
		fmt.Sprintf("profile_instructions=%d", report.ProfileInstructions),
	}
}
