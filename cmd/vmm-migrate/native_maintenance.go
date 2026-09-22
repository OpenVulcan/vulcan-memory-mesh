// native_maintenance.go dispatches offline native migration and FTS maintenance without starting the service.
// native_maintenance.go 在不启动服务的情况下分派离线原生迁移与全文索引维护。
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// nativeFTSRebuilder is the maintenance capability implemented by the shared SQLite store.
// nativeFTSRebuilder 是共用 SQLite 存储实现的全文索引维护能力。
type nativeFTSRebuilder interface {
	RebuildFTSIndex(context.Context) error
}

// runNativeFTSRebuild holds runtime and storage ownership throughout index reconstruction.
// runNativeFTSRebuild 在索引重建全过程持有运行端口与数据库所有权。
func runNativeFTSRebuild(ctx context.Context, cfg config.Config) (result error) {
	if !cfg.UsesNative() {
		return fmt.Errorf("-fts-rebuild requires storage.mode=native")
	}
	guard, err := acquireMaintenanceRuntimeGuard(cfg, "native FTS rebuild")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, guard.Close()) }()
	dependencies, err := app.BuildMaintenanceStorageDependencies(cfg)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, dependencies.Shutdown(context.Background())) }()
	rebuilder, ok := dependencies.Relational.(nativeFTSRebuilder)
	if !ok {
		return fmt.Errorf("native relational store does not expose FTS reconstruction")
	}
	if err := rebuilder.RebuildFTSIndex(ctx); err != nil {
		return fmt.Errorf("rebuild native FTS: %w", err)
	}
	fmt.Println("[vmm-migrate] native FTS rebuild completed")
	return nil
}

// runNativeStorageMigration verifies the selected source mode before acquiring offline runtime ownership.
// runNativeStorageMigration 在取得离线运行所有权前核验选定迁移来源模式。
func runNativeStorageMigration(ctx context.Context, cfg config.Config, layout config.PromptLayout, target, output string) (result error) {
	if (target == "split-to-native" && cfg.StorageMode() != "split") || (target == "controller-to-native" && !cfg.UsesController()) {
		return fmt.Errorf("migration %s does not match configured storage mode %s", target, cfg.StorageMode())
	}
	if target != "split-to-native" && target != "controller-to-native" {
		return fmt.Errorf("unsupported native migration %q", target)
	}
	guard, err := acquireMaintenanceRuntimeGuard(cfg, target)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, guard.Close()) }()
	if err := app.MigrateLegacyStorageToNative(ctx, cfg, layout, output); err != nil {
		return err
	}
	fmt.Printf("[vmm-migrate] target=%s output=%s completed; inspect migration-report.json and merge native-storage-override.fragment.yaml into the original override configuration; do not pass the fragment directly to -config\n", target, output)
	return nil
}
