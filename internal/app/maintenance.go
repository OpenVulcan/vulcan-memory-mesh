// maintenance.go exposes the narrow runtime wiring used by one-shot maintenance tools so they can reuse the same adapter composition without booting the gRPC server.
// maintenance.go 用于暴露一次性维护工具需要的狭窄运行时装配能力，让它们在不启动 gRPC 服务的前提下复用同一套适配器组合。
package app

import (
	"context"
	"errors"
	"fmt"

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

// BuildMaintenanceDependencies composes maintenance storage and the configured standalone embedding provider.
// BuildMaintenanceDependencies 用于装配维护存储与配置中的独立 embedding 供应商。
func BuildMaintenanceDependencies(cfg config.Config) (MaintenanceDependencies, error) {
	embedding, err := buildMaintenanceEmbedding(cfg)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance embedding: %w", err)
	}
	// This entry point is used only by explicit vector rebuilds and may recover their quarantined native pair.
	// 此入口仅供显式向量重建使用，允许恢复被中断标记隔离的原生数据库组合。
	storageDeps, err := buildMaintenanceStorageDependenciesForRebuild(cfg)
	if err != nil {
		return MaintenanceDependencies{}, fmt.Errorf("build maintenance storage: %w", err)
	}
	dependencies := maintenanceDependenciesFromStorage(storageDeps)
	dependencies.Embedding = embedding
	return dependencies, nil
}

// buildMaintenanceEmbedding builds the configured embedding adapter used by standalone vector rebuilds.
// buildMaintenanceEmbedding 用于构建独立向量重建所使用的配置 embedding 适配器。
func buildMaintenanceEmbedding(cfg config.Config) (appports.EmbeddingClient, error) {
	return buildEmbedding(cfg)
}

// BuildMaintenanceStorageDependencies composes only the storage adapters for cleanup and export commands that do not need an embedding provider.
// BuildMaintenanceStorageDependencies 仅装配清理与导出命令需要的存储适配器，不要求配置 embedding provider。
func BuildMaintenanceStorageDependencies(cfg config.Config) (MaintenanceDependencies, error) {
	storageDeps, err := buildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return MaintenanceDependencies{}, err
	}
	return maintenanceDependenciesFromStorage(storageDeps), nil
}

// maintenanceDependenciesFromStorage preserves the common owner lifecycle for one-shot callers.
// maintenanceDependenciesFromStorage 为一次性调用方保留共享所有者的生命周期。
func maintenanceDependenciesFromStorage(storageDeps storageDependencies) MaintenanceDependencies {
	return MaintenanceDependencies{
		Relational: storageDeps.Relational,
		Vector:     storageDeps.Vector,
		Shutdowns:  buildUniqueShutdownSequence(storageDeps.Lifecycle, storageDeps.Relational, storageDeps.Vector),
	}
}

// buildMaintenanceStorageDependenciesForRebuild permits native recovery only for the explicitly destructive rebuild workflow.
// buildMaintenanceStorageDependenciesForRebuild 仅为显式破坏性重建流程允许原生中断恢复。
func buildMaintenanceStorageDependenciesForRebuild(cfg config.Config) (storageDependencies, error) {
	if cfg.UsesNative() {
		return buildNativeStorageDependenciesWithRecovery(cfg, config.PromptLayout{}, false, true)
	}
	return buildMaintenanceStorageDependencies(cfg)
}

// buildMaintenanceStorageDependencies composes the storage adapters needed by one-shot maintenance tools while avoiding eager split-mode sidecar table creation before the destructive workflow actually starts.
// buildMaintenanceStorageDependencies 用于装配一次性维护工具需要的存储适配器，并避免在 split 模式下于真正进入破坏性流程前就提前创建 sidecar 表。
func buildMaintenanceStorageDependencies(cfg config.Config) (storageDependencies, error) {
	// Native maintenance opens both databases under one pair lock and defers vector-table creation until the destructive workflow is ready.
	// 原生维护必须在同一对路径锁下打开两个数据库，并把向量建表延迟到破坏性流程真正开始之后。
	if cfg.UsesNative() {
		return buildNativeStorageDependencies(cfg, config.PromptLayout{}, false)
	}
	if cfg.UsesCombinedPostgres() {
		return buildStorageDependencies(cfg, config.PromptLayout{})
	}
	if cfg.UsesController() {
		return buildControllerStorageDependenciesWithVectorInit(cfg, config.PromptLayout{}, false, true)
	}

	return buildSplitStorageDependencies(cfg, config.PromptLayout{}, false)
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
