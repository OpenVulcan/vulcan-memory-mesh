// runtime_storage.go implements application-layer storage builders and capability resolution used by the local runtime composition root.
// runtime_storage.go 用于实现本地运行时组合根使用的应用层存储构建与能力解析逻辑。
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_postgres"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// storageDependencies bundles the relational and vector ports selected for one runtime mode together with the startup schema workflow the composition root must apply.
// storageDependencies 用于打包某个运行模式选中的关系端口、向量端口，以及组合根启动时需要执行的 schema 工作流。
type storageDependencies struct {
	Relational         appports.RelationalStore
	Vector             appports.VectorStore
	ManageVectorSchema bool
}

// runtimeStorageCapabilities stores the narrowed storage-facing capabilities that the runtime needs after the selected adapters are built and validated.
// runtimeStorageCapabilities 用于保存运行时在构建并校验完选中存储适配器后所需的收窄能力集合。
type runtimeStorageCapabilities struct {
	Relational         appports.RelationalStore
	Vector             appports.VectorStore
	NoiseCache         appports.NoiseEmbeddingCache
	ScopeResolver      appports.RequestScopeResolver
	WorkspaceStore     appports.WorkspaceStore
	SchemaVersions     appports.SchemaVersionStore
	ProfileStore       appports.ProfileStore
	MemoryStore        appports.MemoryStore
	ChatCompactStore   usecase.ChatCompactStore
	RetentionStore     appports.RetentionStore
	ManageVectorSchema bool
}

// initRuntimeStorageCapabilities builds the configured storage adapters, resolves the runtime-facing capabilities, and runs vector schema synchronization when the selected mode requires it.
// initRuntimeStorageCapabilities 用于构建当前配置下的存储适配器、解析运行时需要的能力集合，并在所选模式需要时执行向量 schema 同步。
func initRuntimeStorageCapabilities(cfg config.Config, logger *logx.Logger, layout config.PromptLayout) (runtimeStorageCapabilities, error) {
	storageDeps, err := buildStorageDependencies(cfg, layout)
	if err != nil {
		return runtimeStorageCapabilities{}, err
	}
	caps, err := resolveRuntimeStorageCapabilities(storageDeps)
	if err != nil {
		return runtimeStorageCapabilities{}, err
	}
	if caps.ManageVectorSchema {
		schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer schemaCancel()
		if err := ensureVectorSchema(schemaCtx, caps.SchemaVersions, caps.WorkspaceStore, caps.Vector, logger); err != nil {
			return runtimeStorageCapabilities{}, err
		}
	}
	return caps, nil
}

// resolveRuntimeStorageCapabilities validates that the selected relational adapter exposes the precise runtime capability set required by the local application.
// resolveRuntimeStorageCapabilities 用于校验所选关系适配器是否暴露了本地应用运行时所需的完整能力集合。
func resolveRuntimeStorageCapabilities(storageDeps storageDependencies) (runtimeStorageCapabilities, error) {
	relational := storageDeps.Relational
	vector := storageDeps.Vector
	if relational == nil {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store is nil")
	}
	if vector == nil {
		return runtimeStorageCapabilities{}, fmt.Errorf("vector store is nil")
	}
	noiseCache, ok := relational.(appports.NoiseEmbeddingCache)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support noise embedding cache")
	}
	scopeResolver, ok := relational.(appports.RequestScopeResolver)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support request scope resolution")
	}
	workspaceStore, ok := relational.(appports.WorkspaceStore)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support workspace management")
	}
	schemaVersions, ok := relational.(appports.SchemaVersionStore)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support schema version tracking")
	}
	profileStore, ok := relational.(appports.ProfileStore)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support profile management")
	}
	memoryStore, ok := relational.(appports.MemoryStore)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support unified memory lookup")
	}
	chatCompactStore, ok := relational.(usecase.ChatCompactStore)
	if !ok {
		return runtimeStorageCapabilities{}, fmt.Errorf("relational store does not support session compact updates")
	}
	retentionStore, err := usecase.EnsureRetentionStore(relational)
	if err != nil {
		return runtimeStorageCapabilities{}, err
	}
	return runtimeStorageCapabilities{
		Relational:         relational,
		Vector:             vector,
		NoiseCache:         noiseCache,
		ScopeResolver:      scopeResolver,
		WorkspaceStore:     workspaceStore,
		SchemaVersions:     schemaVersions,
		ProfileStore:       profileStore,
		MemoryStore:        memoryStore,
		ChatCompactStore:   chatCompactStore,
		RetentionStore:     retentionStore,
		ManageVectorSchema: storageDeps.ManageVectorSchema,
	}, nil
}

// normalizeProviderAlias keeps runtime adapter selection aligned with config validation by trimming accidental surrounding whitespace before lower-casing provider aliases.
// normalizeProviderAlias 用于在 provider 别名转小写前先裁掉意外的首尾空白，让运行时适配器选择与配置校验保持一致。
func normalizeProviderAlias(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// buildStorageDependencies selects either the historical split stores or the unified PostgreSQL combined store and returns the matching runtime ports.
// buildStorageDependencies 用于选择历史分离存储或统一 PostgreSQL 组合库，并返回对应的运行时端口集合。
func buildStorageDependencies(cfg config.Config, layout config.PromptLayout) (storageDependencies, error) {
	if cfg.UsesCombinedPostgres() {
		combined, err := buildCombinedStore(cfg)
		if err != nil {
			return storageDependencies{}, err
		}
		return storageDependencies{
			Relational:         combined,
			Vector:             combined,
			ManageVectorSchema: false,
		}, nil
	}
	relational, err := buildRelationalForLayout(cfg, layout)
	if err != nil {
		return storageDependencies{}, err
	}
	vector, err := buildVectorForLayout(cfg, layout)
	if err != nil {
		return storageDependencies{}, err
	}
	return storageDependencies{
		Relational:         relational,
		Vector:             vector,
		ManageVectorSchema: true,
	}, nil
}

// buildCombinedStore selects the unified PostgreSQL-backed combined store used by the new dialect pattern runtime.
// buildCombinedStore 用于选择 Dialect Pattern 运行时使用的统一 PostgreSQL 组合库。
func buildCombinedStore(cfg config.Config) (*vldb_postgres.Store, error) {
	if !cfg.UsesCombinedPostgres() {
		return nil, fmt.Errorf("combined postgres store is disabled for storage.mode=%s", cfg.StorageMode())
	}
	return vldb_postgres.NewStore(vldb_postgres.Config{
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
	})
}

// buildVector selects the configured vector backend used by retrieval and destructive cleanup flows.
// buildVector 用于选择当前配置的向量后端，服务检索和破坏性清理流程。
func buildVector(cfg config.Config) (appports.VectorStore, error) {
	return buildVectorForLayout(cfg, config.PromptLayout{})
}

// buildVectorForLayout selects the configured vector backend while allowing tests to pin a synthetic packaged layout instead of relying on the current process executable path.
// buildVectorForLayout 用于选择当前配置的向量后端，同时允许测试显式固定一个打包布局，而不是依赖当前进程的可执行文件路径。
func buildVectorForLayout(cfg config.Config, promptLayout config.PromptLayout) (appports.VectorStore, error) {
	layout, err := resolveLocalStorageLayoutForPromptLayout(promptLayout)
	if err != nil {
		return nil, err
	}
	switch normalizeProviderAlias(cfg.Vector.Provider) {
	case "lancedb":
		return vldb_lancedb.NewStore(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// buildRelational selects the configured durable SQL backend used by workspace/session/turn persistence.
// buildRelational 用于选择当前配置的长期 SQL 后端，服务层级、session 和 turn 持久化。
func buildRelational(cfg config.Config) (appports.RelationalStore, error) {
	return buildRelationalForLayout(cfg, config.PromptLayout{})
}

// buildRelationalForLayout selects the configured relational backend while allowing tests to reuse their explicit packaged config root for database placement.
// buildRelationalForLayout 用于选择当前配置的关系存储后端，同时允许测试复用显式的打包配置根目录来放置数据库文件。
func buildRelationalForLayout(cfg config.Config, promptLayout config.PromptLayout) (appports.RelationalStore, error) {
	layout, err := resolveLocalStorageLayoutForPromptLayout(promptLayout)
	if err != nil {
		return nil, err
	}
	switch normalizeProviderAlias(cfg.Relational.Provider) {
	case "sqlite":
		return vldb_sqlite.NewStore(layout.SQLiteLibrary, layout.SQLiteDatabase, cfg.SQLite.Timeout.Duration, vldb_sqlite.StoreOptions{
			TokenizerMode: cfg.SQLite.TokenizerMode,
		})
	default:
		return nil, fmt.Errorf("unsupported relational provider: %s", cfg.Relational.Provider)
	}
}
