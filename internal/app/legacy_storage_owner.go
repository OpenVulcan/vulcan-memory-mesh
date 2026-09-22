// legacy_storage_owner.go gives legacy runtime and migration the same path-based writer exclusion.
// legacy_storage_owner.go 为旧存储运行时与迁移提供共同的路径级写入互斥，避免端口变化绕过停机检查。
package app

import (
	"context"
	"fmt"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/storageguard"
)

// acquireLegacyStorageOwner canonicalizes both legacy resources before acquiring the runtime's shared writer locks.
// acquireLegacyStorageOwner 规范化两个旧存储资源，再取得与运行时共同遵守的写入锁。
func acquireLegacyStorageOwner(layout localStorageLayout) (*nativeStorageOwner, error) {
	sqlitePath, err := storageguard.CanonicalPath(layout.SQLiteDatabase)
	if err != nil {
		return nil, err
	}
	lancePath, err := storageguard.CanonicalPath(layout.LanceDBDirectory)
	if err != nil {
		return nil, err
	}
	return acquireNativeStorageOwner(nativeStorageLayout{SQLiteDatabase: sqlitePath, LanceDBDirectory: lancePath})
}

// buildSplitStorageDependencies composes a legacy pair under writer ownership and optionally defers vector creation.
// buildSplitStorageDependencies 在写入所有权保护下装配旧存储组合，维护流程可推迟创建向量表。
func buildSplitStorageDependencies(cfg config.Config, promptLayout config.PromptLayout, ensureVectorTable bool) (storageDependencies, error) {
	if normalizeProviderAlias(cfg.Relational.Provider) != "sqlite" || normalizeProviderAlias(cfg.Vector.Provider) != "lancedb" {
		return storageDependencies{}, fmt.Errorf("split storage requires sqlite relational and lancedb vector providers")
	}
	layout, err := resolveLocalStorageLayoutForPromptLayout(promptLayout)
	if err != nil {
		return storageDependencies{}, err
	}
	owner, err := acquireLegacyStorageOwner(layout)
	if err != nil {
		return storageDependencies{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = owner.Shutdown(context.Background())
		}
	}()
	relational, err := vldb_sqlite.NewStore(layout.SQLiteLibrary, layout.SQLiteDatabase, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{TokenizerMode: cfg.SQLite.TokenizerMode})
	if err != nil {
		return storageDependencies{}, err
	}
	owner.resources = append(owner.resources, relational)
	var vector *vldb_lancedb.Store
	if ensureVectorTable {
		vector, err = vldb_lancedb.NewStore(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	} else {
		vector, err = vldb_lancedb.NewStoreWithoutInit(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	}
	if err != nil {
		return storageDependencies{}, err
	}
	owner.resources = append(owner.resources, vector)
	success = true
	return storageDependencies{Relational: relational, Vector: vector, Lifecycle: owner, ManageVectorSchema: ensureVectorTable}, nil
}
