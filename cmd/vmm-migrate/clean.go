// clean.go implements standalone destructive cleanup actions so maintenance operators can wipe managed storage backends without booting the service binary.
// clean.go 用于实现独立的破坏性清理动作，让运维可以在不启动服务二进制的情况下清空受管存储后端。
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_postgres"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// maintenanceCleanSelection records which gateway-backed stores should be wiped during one maintenance-clean run.
// maintenanceCleanSelection 用于记录一次维护清理过程中应当清空哪些网关后端。
type maintenanceCleanSelection struct {
	All      bool
	SQLite   bool
	LanceDB  bool
	Postgres bool
}

// parseMaintenanceCleanSelection normalizes the CLI value and translates it into the concrete cleanup targets requested by the operator.
// parseMaintenanceCleanSelection 用于规范化命令行值，并把它转换成运维人员请求的具体清理目标。
func parseMaintenanceCleanSelection(raw string) (maintenanceCleanSelection, error) {
	selection := maintenanceCleanSelection{}
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(raw)), func(r rune) bool {
		return r == ',' || r == '+' || r == ';'
	})
	if len(parts) == 0 {
		return selection, fmt.Errorf("maintenance clean target is required: use sqlite, lancedb, postgres, or all")
	}
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "all":
			selection.All = true
			selection.SQLite = true
			selection.LanceDB = true
			selection.Postgres = true
		case "sqlite":
			selection.SQLite = true
		case "lancedb":
			selection.LanceDB = true
		case "postgres":
			selection.Postgres = true
		case "":
			continue
		default:
			return maintenanceCleanSelection{}, fmt.Errorf("unsupported maintenance clean target %q: use sqlite, lancedb, postgres, or all", part)
		}
	}
	if !selection.SQLite && !selection.LanceDB && !selection.Postgres {
		return maintenanceCleanSelection{}, fmt.Errorf("maintenance clean target is required: use sqlite, lancedb, postgres, or all")
	}
	return selection, nil
}

// runMaintenanceClean connects only to the requested storage backends, executes the destructive cleanup, and exits immediately afterwards.
// runMaintenanceClean 用于只连接被请求的存储后端，执行破坏性清理，然后立刻退出。
func runMaintenanceClean(ctx context.Context, cfg config.Config, layout config.PromptLayout, target string) error {
	selection, err := parseMaintenanceCleanSelection(target)
	if err != nil {
		return err
	}
	localLayout, err := app.ResolveLocalStorageLayoutForPromptLayout(layout)
	if err != nil {
		return err
	}

	// Execute each destructive cleanup sequentially so operators can see exactly which backend blocked the wipe.
	// 按顺序执行每个破坏性清理动作，确保运维能明确看到究竟是哪个后端阻塞了清空。
	if selection.SQLite {
		if err := vldb_sqlite.DebugCleanManagedSchema(ctx, localLayout.SQLiteLibrary, localLayout.SQLiteDatabase, cfg.SQLite.Timeout.Duration); err != nil {
			return err
		}
		fmt.Printf("[vmm-migrate] SQLite managed schema cleaned via %s\n", strings.TrimSpace(localLayout.SQLiteDatabase))
	}
	if selection.LanceDB {
		tableName, err := vldb_lancedb.DebugDropConfiguredTable(ctx, localLayout.LanceDBLibrary, localLayout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.Embedding.Dimension)
		if err != nil {
			return err
		}
		fmt.Printf("[vmm-migrate] LanceDB table dropped: %s via %s\n", tableName, strings.TrimSpace(localLayout.LanceDBDirectory))
	}
	if selection.Postgres {
		postgresCfg, err := buildPostgresMaintenanceConfig(cfg)
		if err != nil {
			return err
		}
		if err := vldb_postgres.DebugCleanManagedSchema(ctx, postgresCfg); err != nil {
			return err
		}
		fmt.Printf("[vmm-migrate] PostgreSQL managed schema cleaned via %s (schema=%s, flavor=%s)\n", strings.TrimSpace(postgresCfg.DSN), strings.TrimSpace(postgresCfg.Schema), strings.TrimSpace(postgresCfg.Flavor))
	}
	return nil
}
