// maintenance.go exposes the narrow runtime wiring used by one-shot maintenance tools so they can reuse the same adapter composition without booting the gRPC server.
// maintenance.go 用于暴露一次性维护工具需要的狭窄运行时装配能力，让它们在不启动 gRPC 服务的前提下复用同一套适配器组合。
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
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

// BuildMaintenanceDependencies composes the shared embedding, relational, and vector adapters needed by standalone maintenance tools without constructing the gRPC runtime.
// BuildMaintenanceDependencies 用于为独立维护工具装配共享的 embedding、关系库与向量适配器，而不会构建 gRPC 运行时。
func BuildMaintenanceDependencies(cfg config.Config) (MaintenanceDependencies, error) {
	embedding, err := buildEmbedding(cfg)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance embedding: %w", err)
	}
	storageDeps, err := buildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance storage: %w", err)
	}
	return MaintenanceDependencies{
		Embedding:  embedding,
		Relational: storageDeps.Relational,
		Vector:     storageDeps.Vector,
		Shutdowns:  buildUniqueShutdownSequence(storageDeps.Relational, storageDeps.Vector),
	}, nil
}

// buildMaintenanceStorageDependencies composes the storage adapters needed by one-shot maintenance tools while avoiding eager split-mode sidecar table creation before the destructive workflow actually starts.
// buildMaintenanceStorageDependencies 用于装配一次性维护工具需要的存储适配器，并避免在 split 模式下于真正进入破坏性流程前就提前创建 sidecar 表。
func buildMaintenanceStorageDependencies(cfg config.Config) (storageDependencies, error) {
	if cfg.UsesCombinedPostgres() {
		return buildStorageDependencies(cfg)
	}

	relational, err := buildRelational(cfg)
	if err != nil {
		return storageDependencies{}, err
	}
	vector, err := buildMaintenanceVector(cfg)
	if err != nil {
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
	switch normalizeProviderAlias(cfg.Vector.Provider) {
	case "lancedb":
		return vldb_lancedb.NewStoreWithoutInit(cfg.LanceDB.Address, cfg.LanceDB.Timeout.Duration, cfg.LanceDB.TableName, cfg.LanceDB.VectorColumn, cfg.Embedding.Dimension)
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
