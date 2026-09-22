// runtime_storage_native.go assembles in-process SQLite and LanceDB under one exclusive owner.
// runtime_storage_native.go 在应用组合根中以单一独占所有者装配进程内 SQLite 与 LanceDB。
package app

import (
	"context"
	"fmt"
	"os"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// buildNativeStorageDependencies builds both databases, optionally deferring vector table creation for maintenance.
// buildNativeStorageDependencies 构建两个原生数据库，维护模式可推迟向量建表，失败时逆序释放全部资源。
func buildNativeStorageDependencies(cfg config.Config, promptLayout config.PromptLayout, ensureVectorTable bool) (storageDependencies, error) {
	return buildNativeStorageDependenciesWithRecovery(cfg, promptLayout, ensureVectorTable, false)
}

// buildNativeStorageDependenciesWithRecovery allows an unfinished vector rebuild only for the explicit rebuild command.
// buildNativeStorageDependenciesWithRecovery 仅为显式向量重建命令允许打开未完成的重建数据，其他入口保持隔离。
func buildNativeStorageDependenciesWithRecovery(cfg config.Config, promptLayout config.PromptLayout, ensureVectorTable, allowVectorRecovery bool) (storageDependencies, error) {
	layout, err := resolveNativeStorageLayout(cfg, promptLayout)
	if err != nil {
		return storageDependencies{}, err
	}
	owner, err := acquireNativeStorageOwner(layout)
	if err != nil {
		return storageDependencies{}, fmt.Errorf("acquire native storage ownership: %w", err)
	}
	// Native schema validation must never enter the legacy automatic drop-and-rebuild path.
	// 原生 Schema 校验不能进入历史版本自动删表重建路径。
	dependencies := storageDependencies{Lifecycle: owner, Health: owner, ManageVectorSchema: false}
	success := false
	defer func() {
		if !success {
			shutdownStorageDependencies(dependencies)
		}
	}()
	// An unfinished migration is quarantined until its copy and index verification finish successfully.
	// 未完成迁移在数据复制与索引核验成功前保持隔离，不能进入普通运行或维护流程。
	if _, err := os.Stat(layout.SQLiteDatabase + ".migration-incomplete"); err == nil {
		return storageDependencies{}, fmt.Errorf("native storage belongs to an incomplete migration; use a new empty migration destination")
	} else if !os.IsNotExist(err) {
		return storageDependencies{}, fmt.Errorf("check native migration marker: %w", err)
	}
	if !allowVectorRecovery {
		if _, err := os.Lstat(layout.SQLiteDatabase + ".vector-rebuild-incomplete"); err == nil {
			return storageDependencies{}, fmt.Errorf("native vector rebuild is incomplete; run the explicit vector rebuild command before serving or other maintenance")
		} else if !os.IsNotExist(err) {
			return storageDependencies{}, fmt.Errorf("check native vector rebuild marker: %w", err)
		}
	}
	if err := ensureNativeStoragePair(layout); err != nil {
		return storageDependencies{}, fmt.Errorf("validate native database pair: %w", err)
	}
	if err := ensureNativeEmbeddingIdentity(cfg, layout.SQLiteDatabase, allowVectorRecovery); err != nil {
		return storageDependencies{}, fmt.Errorf("validate native embedding identity: %w", err)
	}

	// Keep SQLite as the authoritative relational store and reject incompatible native data before serving.
	// 以 SQLite 作为关系事实来源，在对外提供服务前拒绝不兼容的原生数据。
	relational, err := vldb_sqlite.NewNativeStore(layout.SQLiteDatabase, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{TokenizerMode: cfg.SQLite.Native.Tokenizer})
	if err != nil {
		return storageDependencies{}, fmt.Errorf("build native SQLite: %w", err)
	}
	dependencies.Relational = relational
	owner.resources = append(owner.resources, relational)
	var vector *vldb_lancedb.Store
	if ensureVectorTable {
		vector, err = vldb_lancedb.NewNativeStore(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	} else {
		vector, err = vldb_lancedb.NewNativeStoreWithoutInit(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	}
	if err != nil {
		return storageDependencies{}, fmt.Errorf("build native LanceDB: %w", err)
	}
	dependencies.Vector = vector
	owner.resources = append(owner.resources, vector)
	owner.checks = []appports.HealthChecker{relational, vector}
	if ensureVectorTable {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.SQLite.Timeout.Duration+cfg.LanceDB.Timeout.Duration)
		defer cancel()
		if err := ensureNativeVectorSchemaVersion(ctx, relational); err != nil {
			return storageDependencies{}, err
		}
		if err := owner.CheckHealth(ctx); err != nil {
			return storageDependencies{}, fmt.Errorf("check native storage: %w", err)
		}
	}
	success = true
	return dependencies, nil
}

// ensureNativeVectorSchemaVersion stamps an unversioned validated table and rejects upgrades requiring maintenance.
// ensureNativeVectorSchemaVersion 为已验证但未记录版本的表登记版本，需要升级时明确要求维护而不重建数据。
func ensureNativeVectorSchemaVersion(ctx context.Context, versions appports.SchemaVersionStore) error {
	version, err := versions.GetSchemaComponentVersion(ctx, "lancedb")
	if err != nil {
		return fmt.Errorf("read native vector schema version: %w", err)
	}
	if version == vldb_lancedb.CurrentSchemaVersion {
		return nil
	}
	if version != 0 {
		return fmt.Errorf("native vector schema version %d differs from required %d; explicit maintenance is required", version, vldb_lancedb.CurrentSchemaVersion)
	}
	if err := versions.SetSchemaComponentVersion(ctx, "lancedb", vldb_lancedb.CurrentSchemaVersion); err != nil {
		return fmt.Errorf("record native vector schema version: %w", err)
	}
	return nil
}
