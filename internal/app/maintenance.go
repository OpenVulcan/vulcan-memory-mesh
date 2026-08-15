// maintenance.go exposes the narrow runtime wiring used by one-shot maintenance tools so they can reuse the same adapter composition without booting the gRPC server.
// maintenance.go 用于暴露一次性维护工具需要的狭窄运行时装配能力，让它们在不启动 gRPC 服务的前提下复用同一套适配器组合。
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vulcan_inference"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// MaintenanceDependencies bundles the outbound runtime ports required by standalone maintenance tools such as storage cleanup, migration, and vector rebuild.
// MaintenanceDependencies 用于打包独立维护工具所需的出站运行时端口，例如存储清理、迁移和向量重建。
type MaintenanceDependencies struct {
	Embedding  appports.EmbeddingClient
	Relational appports.RelationalStore
	Vector     appports.VectorStore
	Shutdowns  []appports.Shutdowner
}

// BuildMaintenanceDependencies composes maintenance storage and selects the same embedding authority as the owning standalone or managed runtime.
// BuildMaintenanceDependencies 用于装配维护存储，并选择与所属独立或托管运行时一致的向量推理权威。
func BuildMaintenanceDependencies(cfg config.Config, managedConfig *config.ManagedConfig) (MaintenanceDependencies, error) {
	embedding, err := buildMaintenanceEmbedding(cfg, managedConfig)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance embedding: %w", err)
	}
	dependencies, err := BuildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance storage: %w", err)
	}
	dependencies.Embedding = embedding
	return dependencies, nil
}

// buildMaintenanceEmbedding prevents managed vector rebuilds from falling back to empty standalone provider key pools.
// buildMaintenanceEmbedding 用于防止托管向量重建错误回退到没有密钥的独立供应商节点池。
func buildMaintenanceEmbedding(cfg config.Config, managedConfig *config.ManagedConfig) (appports.EmbeddingClient, error) {
	if managedConfig == nil {
		return buildEmbedding(cfg)
	}
	client, err := vulcan_inference.New(vulcan_inference.Config{
		DiscoveryFile:         managedConfig.Runtime.Inference.DiscoveryFile,
		ExpectedProcessID:     managedConfig.Parent.ProcessID,
		ExpectedStartedAt:     managedConfig.Parent.StartedAtUnixMS,
		ExpectedCallerID:      managedConfig.Runtime.Inference.ExpectedCallerID,
		ConsumerProfileID:     managedConfig.Runtime.Inference.ConsumerProfileID,
		StartupTimeout:        managedConfig.Runtime.Inference.StartupTimeout.Duration,
		MaxConnectionsPerHost: managedConfig.Runtime.Inference.MaxConnectionsPerHost,
	})
	if err != nil {
		return nil, fmt.Errorf("build Vulcan managed maintenance inference client: %w", err)
	}
	return client, nil
}

// BuildMaintenanceStorageDependencies composes only the storage adapters for cleanup and export commands that do not need an embedding provider.
// BuildMaintenanceStorageDependencies 仅装配清理与导出命令需要的存储适配器，不要求配置 embedding provider。
func BuildMaintenanceStorageDependencies(cfg config.Config) (MaintenanceDependencies, error) {
	storageDeps, err := buildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return MaintenanceDependencies{}, err
	}
	return MaintenanceDependencies{
		Relational: storageDeps.Relational,
		Vector:     storageDeps.Vector,
		Shutdowns:  buildUniqueShutdownSequence(storageDeps.Lifecycle, storageDeps.Relational, storageDeps.Vector),
	}, nil
}

// buildMaintenanceStorageDependencies composes the storage adapters needed by one-shot maintenance tools while avoiding eager split-mode sidecar table creation before the destructive workflow actually starts.
// buildMaintenanceStorageDependencies 用于装配一次性维护工具需要的存储适配器，并避免在 split 模式下于真正进入破坏性流程前就提前创建 sidecar 表。
func buildMaintenanceStorageDependencies(cfg config.Config) (storageDependencies, error) {
	if cfg.UsesCombinedPostgres() {
		return buildStorageDependencies(cfg, config.PromptLayout{})
	}
	if cfg.UsesController() {
		return buildControllerStorageDependenciesWithVectorInit(cfg, config.PromptLayout{}, false, true)
	}

	relational, err := buildRelational(cfg)
	if err != nil {
		return storageDependencies{}, err
	}
	vector, err := buildMaintenanceVector(cfg)
	if err != nil {
		_ = relational.Shutdown(context.Background())
		return storageDependencies{}, err
	}
	return storageDependencies{
		Relational:         relational,
		Vector:             vector,
		ManageVectorSchema: false,
	}, nil
}

// buildMaintenanceVector builds the split-mode sidecar adapter without eagerly creating the current-dimension LanceDB table so vector-rebuild can first finish its preparation and confirmation steps.
// buildMaintenanceVector 用于在 split 模式下构建 sidecar 适配器，但不会提前创建当前维度的 LanceDB 表，让 vector-rebuild 可以先完成准备与确认流程。
func buildMaintenanceVector(cfg config.Config) (appports.VectorStore, error) {
	layout, err := ResolveLocalStorageLayout()
	if err != nil {
		return nil, err
	}
	switch normalizeProviderAlias(cfg.Vector.Provider) {
	case "lancedb":
		return vldb_lancedb.NewStoreWithoutInit(layout.LanceDBLibrary, layout.LanceDBDirectory, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
	default:
		return nil, fmt.Errorf("unsupported vector provider: %s", cfg.Vector.Provider)
	}
}

// Shutdown releases the maintenance adapters in reverse construction order so standalone tools do not leave sockets or pools open after one-shot work completes.
// Shutdown 用于按构建逆序释放维护工具使用的适配器，避免一次性任务完成后仍遗留连接池或套接字。
func (d MaintenanceDependencies) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var shutdownErrors []error
	for i := len(d.Shutdowns) - 1; i >= 0; i-- {
		shutdowner := d.Shutdowns[i]
		if shutdowner == nil {
			continue
		}
		if err := shutdowner.Shutdown(ctx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown maintenance dependency[%d]: %w", i, err))
		}
	}
	if len(shutdownErrors) > 0 {
		return fmt.Errorf("shutdown maintenance dependencies: %w", errors.Join(shutdownErrors...))
	}
	return nil
}
