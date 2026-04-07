// migrate.go implements standalone storage migration actions so one-shot data replay does not live inside the main service binary.
// migrate.go 用于实现独立的存储迁移动作，让一次性数据回放不再驻留在主服务二进制内部。
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
	// maintenanceMigrateSplitToCombined is the currently supported storage migration action that replays the split SQLite fact store into the PostgreSQL combined store.
	// maintenanceMigrateSplitToCombined 用于表示当前支持的存储迁移动作：把 split 模式 SQLite 事实库回放到 PostgreSQL 组合库。
	maintenanceMigrateSplitToCombined = "split-to-combined"
)

// parseMaintenanceMigrateTarget normalizes the maintenance selector so unsupported migration actions fail before any storage connection is opened.
// parseMaintenanceMigrateTarget 用于规范化迁移动作选择器，让不受支持的迁移动作在建立任何存储连接前就失败。
func parseMaintenanceMigrateTarget(raw string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(raw))
	switch target {
	case maintenanceMigrateSplitToCombined:
		return target, nil
	case "":
		return "", fmt.Errorf("maintenance migrate target is required: use %s", maintenanceMigrateSplitToCombined)
	default:
		return "", fmt.Errorf("unsupported maintenance migrate target %q: use %s", raw, maintenanceMigrateSplitToCombined)
	}
}

// runMaintenanceMigrate exports the split SQLite fact snapshot and imports it into PostgreSQL without starting the gRPC runtime.
// runMaintenanceMigrate 用于导出 split SQLite 事实快照并导入 PostgreSQL，且不会启动 gRPC 运行时。
func runMaintenanceMigrate(ctx context.Context, cfg config.Config, target string) error {
	target, err := parseMaintenanceMigrateTarget(target)
	if err != nil {
		return err
	}
	switch target {
	case maintenanceMigrateSplitToCombined:
		return runSplitToCombinedMigration(ctx, cfg)
	default:
		return fmt.Errorf("unsupported maintenance migrate target %q", target)
	}
}

// runSplitToCombinedMigration treats SQLite as the only migration fact source, parses vectors in Go memory, and writes them into PostgreSQL native vector columns.
// runSplitToCombinedMigration 用于把 SQLite 作为唯一迁移事实源，在 Go 内存中解析向量，并把它们写入 PostgreSQL 原生向量列。
func runSplitToCombinedMigration(ctx context.Context, cfg config.Config) error {
	if strings.TrimSpace(cfg.SQLite.Address) == "" {
		return fmt.Errorf("sqlite.address is required for maintenance migrate split-to-combined")
	}
	postgresCfg, err := buildPostgresMaintenanceConfig(cfg)
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

	fmt.Printf("[vmm-migrate] target=%s flavor=%s schema=%s total_rows=%d\n", maintenanceMigrateSplitToCombined, postgresCfg.Flavor, postgresCfg.Schema, report.TotalRows())
	for _, line := range formatMigrationReportLines(report) {
		fmt.Printf("[vmm-migrate] %s\n", line)
	}
	return nil
}

// buildPostgresMaintenanceConfig extracts the PostgreSQL combined-store settings required by standalone maintenance actions.
// buildPostgresMaintenanceConfig 用于提取独立维护动作所需的 PostgreSQL 组合库配置。
func buildPostgresMaintenanceConfig(cfg config.Config) (vldb_postgres.Config, error) {
	postgresCfg := vldb_postgres.Config{
		DSN:                     cfg.Postgres.DSN,
		Schema:                  cfg.Postgres.Schema,
		Flavor:                  cfg.Postgres.Flavor,
		QueryTimeout:            cfg.Postgres.QueryTimeout.Duration,
		MaintenanceReadTimeout:  cfg.MaintenanceTool.Postgres.ReadTimeout.Duration,
		MaintenanceWriteTimeout: cfg.MaintenanceTool.Postgres.WriteTimeout.Duration,
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
		return vldb_postgres.Config{}, fmt.Errorf("postgres.dsn is required for maintenance PostgreSQL actions")
	}
	if strings.TrimSpace(postgresCfg.Schema) == "" {
		return vldb_postgres.Config{}, fmt.Errorf("postgres.schema is required for maintenance PostgreSQL actions")
	}
	if postgresCfg.EmbeddingDimension <= 0 {
		return vldb_postgres.Config{}, fmt.Errorf("embedding.dimension must be > 0 for maintenance PostgreSQL actions")
	}
	return postgresCfg, nil
}

// formatMigrationReportLines renders one stable per-table summary so automation and manual operators can both read the migration result quickly.
// formatMigrationReportLines 用于渲染稳定的逐表汇总，让自动化和人工运维都能快速阅读迁移结果。
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
