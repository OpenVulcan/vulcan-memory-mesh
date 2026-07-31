// main.go implements the standalone maintenance CLI used for destructive cleanup, storage migration, and vector rebuild operations.
// main.go 用于实现独立维护 CLI，承接破坏性清理、存储迁移和向量重建操作。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/openvulcan/vmm/internal/config"
)

// maintenanceRuntimeConfiguration preserves both the derived runtime values and the managed inference authority needed by one-shot maintenance.
// maintenanceRuntimeConfiguration 用于同时保留派生运行时值与一次性维护所需的托管推理权威。
type maintenanceRuntimeConfiguration struct {
	Config        config.Config
	Layout        config.PromptLayout
	ManagedConfig *config.ManagedConfig
}

// main executes the standalone maintenance bootstrap and dispatches to the selected one-shot action.
// main 用于执行独立维护工具的启动流程，并分发到选中的一次性动作。
func main() {
	cfgPath := flag.String("config", "", "user override root (~/.vmm by default); explicit config files must use .yaml or .yml")
	managedConfigPath := flag.String("vulcan-managed-config", "", "strict Vulcan Code managed-runtime manifest used instead of layered config")
	cleanTarget := flag.String("clean", "", "maintenance cleanup target: sqlite, lancedb, postgres, or all")
	migrateTarget := flag.String("migrate", "", "maintenance migration target: split-to-combined")
	vectorRebuild := flag.Bool("vector-rebuild", false, "rebuild vectors with the currently configured embedding model")
	confirmVectorRebuild := flag.Bool("confirm-vector-rebuild", false, "explicitly confirms vector rebuild for non-interactive host orchestration")
	flag.Parse()

	ctx, stop := buildSignalAwareMainContext()
	defer stop()

	if err := run(ctx, os.Args[0], *cfgPath, *managedConfigPath, *cleanTarget, *migrateTarget, *vectorRebuild, *confirmVectorRebuild); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// buildSignalAwareMainContext creates one process-lifetime context that is canceled by operator interrupts so destructive maintenance flows can observe aborts and unwind through their existing rollback paths.
// buildSignalAwareMainContext 用于创建一个会被操作者中断信号取消的进程级上下文，让破坏性维护流程可以感知终止并沿用现有回滚路径有序退出。
func buildSignalAwareMainContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// run resolves config layering once and dispatches exactly one maintenance action without booting the gRPC runtime.
// run 用于一次性解析配置层，并在不启动 gRPC 运行时的前提下分发唯一的维护动作。
func run(ctx context.Context, argv0, cfgPath, managedConfigPath, cleanTarget, migrateTarget string, vectorRebuild, vectorRebuildConfirmed bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	actionCount := 0
	if strings.TrimSpace(cleanTarget) != "" {
		actionCount++
	}
	if strings.TrimSpace(migrateTarget) != "" {
		actionCount++
	}
	if vectorRebuild {
		actionCount++
	}
	if actionCount != 1 {
		return fmt.Errorf("exactly one maintenance action is required: use one of -clean, -migrate, or -vector-rebuild")
	}

	runtimeConfig, err := loadMaintenanceRuntimeConfiguration(argv0, cfgPath, managedConfigPath)
	if err != nil {
		return err
	}

	switch {
	case strings.TrimSpace(cleanTarget) != "":
		return runMaintenanceClean(ctx, runtimeConfig.Config, runtimeConfig.Layout, cleanTarget)
	case strings.TrimSpace(migrateTarget) != "":
		return runMaintenanceMigrate(ctx, runtimeConfig.Config, migrateTarget)
	case vectorRebuild:
		return runMaintenanceVectorRebuild(
			ctx,
			runtimeConfig.Config,
			runtimeConfig.ManagedConfig,
			os.Stdin,
			os.Stdout,
			vectorRebuildConfirmed,
		)
	default:
		return fmt.Errorf("no maintenance action selected")
	}
}

// loadMaintenanceRuntimeConfiguration selects exactly one configuration authority and retains the managed manifest so vector rebuild uses Vulcan Code inference.
// loadMaintenanceRuntimeConfiguration 用于选择唯一配置权威，并保留托管清单，使向量重建继续使用 Vulcan Code 推理。
func loadMaintenanceRuntimeConfiguration(argv0, cfgPath, managedConfigPath string) (maintenanceRuntimeConfiguration, error) {
	if strings.TrimSpace(cfgPath) != "" && strings.TrimSpace(managedConfigPath) != "" {
		return maintenanceRuntimeConfiguration{}, fmt.Errorf("-config and -vulcan-managed-config are mutually exclusive")
	}
	if strings.TrimSpace(managedConfigPath) != "" {
		bundle, err := config.LoadVulcanManagedConfig(managedConfigPath)
		if err != nil {
			return maintenanceRuntimeConfiguration{}, fmt.Errorf("load Vulcan managed config: %w", err)
		}
		manifest := bundle.Manifest
		return maintenanceRuntimeConfiguration{
			Config:        bundle.Config,
			Layout:        bundle.Layout,
			ManagedConfig: &manifest,
		}, nil
	}
	exePath, err := os.Executable()
	if err != nil {
		return maintenanceRuntimeConfiguration{}, fmt.Errorf("resolve executable path: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return maintenanceRuntimeConfiguration{}, fmt.Errorf("resolve working directory: %w", err)
	}
	layout, err := config.ResolvePromptLayout(exePath, wd, cfgPath)
	if err != nil {
		return maintenanceRuntimeConfiguration{}, fmt.Errorf("resolve config layout: %w", err)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return maintenanceRuntimeConfiguration{}, fmt.Errorf("load config: %w", err)
	}
	return maintenanceRuntimeConfiguration{
		Config: cfg,
		Layout: layout,
	}, nil
}
