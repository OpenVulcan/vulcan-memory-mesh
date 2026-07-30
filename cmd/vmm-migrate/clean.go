// clean.go implements standalone destructive cleanup actions so maintenance operators can wipe managed storage backends without booting the service binary.
// clean.go 用于实现独立的破坏性清理动作，让运维可以在不启动服务二进制的情况下清空受管存储后端。
package main

import (
	"context"
	"errors"
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

// managedSQLiteCleaner exposes destructive managed-schema cleanup without leaking a concrete local FFI implementation into the command.
// managedSQLiteCleaner 暴露受管 schema 破坏性清理能力，避免命令依赖具体本地 FFI 实现。
type managedSQLiteCleaner interface {
	DebugCleanManagedSchema(context.Context) error
}

// configuredLanceTableDropper exposes configured-table deletion for either direct FFI or controller-backed stores.
// configuredLanceTableDropper 为直接 FFI 与 controller 存储暴露配置表删除能力。
type configuredLanceTableDropper interface {
	DebugDropConfiguredTable(context.Context) (string, error)
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
	runtimeGuard, err := acquireMaintenanceRuntimeGuard(cfg, "storage clean")
	if err != nil {
		return err
	}
	defer func() {
		_ = runtimeGuard.Close()
	}()
	localLayout, err := app.ResolveLocalStorageLayoutForPromptLayout(layout)
	if err != nil {
		return err
	}

	// Route controller-mode cleanup through one maintenance session so the command never opens either database directly.
	// controller 模式清理统一经一个维护会话执行，确保命令不会直接打开任一数据库。
	if cfg.UsesController() && (selection.SQLite || selection.LanceDB) {
		if err := runControllerMaintenanceClean(ctx, cfg, localLayout.SQLiteDatabase, localLayout.LanceDBDirectory, selection); err != nil {
			return err
		}
		selection.SQLite = false
		selection.LanceDB = false
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

// runControllerMaintenanceClean executes selected local-store cleanup operations through one controller-owned maintenance dependency set.
// runControllerMaintenanceClean 通过一组 controller 持有的维护依赖执行选中的本地存储清理操作。
func runControllerMaintenanceClean(ctx context.Context, cfg config.Config, sqliteDatabase string, lanceDBDirectory string, selection maintenanceCleanSelection) error {
	dependencies, err := app.BuildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return fmt.Errorf("build controller maintenance storage: %w", err)
	}
	var operationErr error
	if selection.SQLite {
		cleaner, ok := dependencies.Relational.(managedSQLiteCleaner)
		if !ok {
			operationErr = fmt.Errorf("controller relational store does not support managed schema cleanup")
		} else if err := cleaner.DebugCleanManagedSchema(ctx); err != nil {
			operationErr = err
		} else {
			fmt.Printf("[vmm-migrate] SQLite managed schema cleaned via controller: %s\n", strings.TrimSpace(sqliteDatabase))
		}
	}
	if operationErr == nil && selection.LanceDB {
		dropper, ok := dependencies.Vector.(configuredLanceTableDropper)
		if !ok {
			operationErr = fmt.Errorf("controller vector store does not support configured table cleanup")
		} else {
			tableName, err := dropper.DebugDropConfiguredTable(ctx)
			if err != nil {
				operationErr = err
			} else {
				fmt.Printf("[vmm-migrate] LanceDB table dropped via controller: %s via %s\n", tableName, strings.TrimSpace(lanceDBDirectory))
			}
		}
	}
	shutdownErr := dependencies.Shutdown(context.Background())
	return errors.Join(operationErr, shutdownErr)
}
