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

	"github.com/openvulcan/vmm/internal/buildinfo"
	"github.com/openvulcan/vmm/internal/config"
)

// maintenanceRuntimeConfiguration preserves the layered configuration and resolved prompt layout used by one-shot maintenance.
// maintenanceRuntimeConfiguration 用于保存一次性维护使用的分层配置与解析后的提示词布局。
type maintenanceRuntimeConfiguration struct {
	Config config.Config
	Layout config.PromptLayout
}

// main executes the standalone maintenance bootstrap and dispatches to the selected one-shot action.
// main 用于执行独立维护工具的启动流程，并分发到选中的一次性动作。
func main() {
	if hasRemovedManagedConfigFlag(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "-vulcan-managed-config is no longer supported; use standalone layered configuration via -config")
		os.Exit(2)
	}
	if hasVersionJSONFlag(os.Args[1:]) {
		if err := buildinfo.WriteVersionJSON(os.Stdout, "vmm-migrate"); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write version document: %v\n", err)
			os.Exit(1)
		}
		return
	}
	cfgPath := flag.String("config", "", "user override root (~/.vmm by default); explicit config files must use .yaml or .yml")
	cleanTarget := flag.String("clean", "", "maintenance cleanup target: sqlite, lancedb, postgres, or all")
	migrateTarget := flag.String("migrate", "", "maintenance migration target: split-to-combined, split-to-native, or controller-to-native")
	// Native migration always writes a new destination and requires explicit unattended acknowledgement.
	// 原生迁移始终写入新目标，并要求无人值守调用明确确认。
	nativeOutput := flag.String("native-output", "", "new empty output directory for native migration")
	confirmMigrate := flag.Bool("confirm-migrate", false, "confirm migration into a new native output directory")
	ftsRebuild := flag.Bool("fts-rebuild", false, "rebuild the native SQLite full-text index from durable original text")
	vectorRebuild := flag.Bool("vector-rebuild", false, "rebuild vectors with the currently configured embedding model")
	confirmVectorRebuild := flag.Bool("confirm-vector-rebuild", false, "explicitly confirms vector rebuild for non-interactive host orchestration")
	// vectorRebuildProgressFile carries the host-owned progress destination into the vector rebuild action.
	// vectorRebuildProgressFile 将宿主持有的进度目标传入向量重建动作。
	vectorRebuildProgressFile := flag.String("vector-rebuild-progress-file", "", "host-owned JSON file for machine-readable vector rebuild progress")
	flag.Parse()

	ctx, stop := buildSignalAwareMainContext()
	defer stop()

	if err := runWithMaintenanceOptions(ctx, os.Args[0], *cfgPath, *cleanTarget, *migrateTarget, *vectorRebuild, *confirmVectorRebuild, *vectorRebuildProgressFile, nativeMaintenanceOptions{OutputDirectory: *nativeOutput, Confirmed: *confirmMigrate, RebuildFTS: *ftsRebuild}); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// hasRemovedManagedConfigFlag detects the retired hosted-runtime flag before the standard parser can treat it as an unknown option.
// hasRemovedManagedConfigFlag 用于在标准解析器将旧托管参数识别为未知选项前检测它。
func hasRemovedManagedConfigFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-vulcan-managed-config" || arg == "--vulcan-managed-config" || strings.HasPrefix(arg, "-vulcan-managed-config=") || strings.HasPrefix(arg, "--vulcan-managed-config=") {
			return true
		}
	}
	return false
}

// hasVersionJSONFlag detects the standalone version-document switch before maintenance flag parsing.
// hasVersionJSONFlag 在维护参数解析前检测独立版本文档开关。
func hasVersionJSONFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-version-json" {
			return true
		}
	}
	return false
}

// buildSignalAwareMainContext creates one process-lifetime context that is canceled by operator interrupts so destructive maintenance flows can observe aborts and unwind through their existing rollback paths.
// buildSignalAwareMainContext 用于创建一个会被操作者中断信号取消的进程级上下文，让破坏性维护流程可以感知终止并沿用现有回滚路径有序退出。
func buildSignalAwareMainContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// run resolves config layering once and dispatches exactly one maintenance action without booting the gRPC runtime.
// run 用于一次性解析配置层，并在不启动 gRPC 运行时的前提下分发唯一的维护动作。
func run(ctx context.Context, argv0, cfgPath, cleanTarget, migrateTarget string, vectorRebuild, vectorRebuildConfirmed bool) error {
	return runWithProgressFile(ctx, argv0, cfgPath, cleanTarget, migrateTarget, vectorRebuild, vectorRebuildConfirmed, "")
}

// runWithProgressFile resolves configuration and optionally supplies a progress side channel to vector rebuild.
// runWithProgressFile 解析配置，并在向量重建时可选地提供进度旁路文件。
func runWithProgressFile(ctx context.Context, argv0, cfgPath, cleanTarget, migrateTarget string, vectorRebuild, vectorRebuildConfirmed bool, progressFile string) error {
	return runWithMaintenanceOptions(ctx, argv0, cfgPath, cleanTarget, migrateTarget, vectorRebuild, vectorRebuildConfirmed, progressFile, nativeMaintenanceOptions{})
}

// nativeMaintenanceOptions carries explicit native-only actions without changing existing maintenance callers.
// nativeMaintenanceOptions 承载明确的原生维护选项，同时保持已有维护调用兼容。
type nativeMaintenanceOptions struct {
	OutputDirectory string
	Confirmed       bool
	RebuildFTS      bool
}

// runWithMaintenanceOptions validates mutually exclusive actions before loading configuration or opening storage.
// runWithMaintenanceOptions 在加载配置或打开存储前校验维护动作互斥关系。
func runWithMaintenanceOptions(ctx context.Context, argv0, cfgPath, cleanTarget, migrateTarget string, vectorRebuild, vectorRebuildConfirmed bool, progressFile string, native nativeMaintenanceOptions) error {
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
	if native.RebuildFTS {
		actionCount++
	}
	isNativeMigration := strings.EqualFold(strings.TrimSpace(migrateTarget), "split-to-native") || strings.EqualFold(strings.TrimSpace(migrateTarget), "controller-to-native")
	if (native.OutputDirectory != "" || native.Confirmed) && !isNativeMigration {
		return fmt.Errorf("-native-output and -confirm-migrate require split-to-native or controller-to-native migration")
	}
	if isNativeMigration && (strings.TrimSpace(native.OutputDirectory) == "" || !native.Confirmed) {
		return fmt.Errorf("native migration requires -native-output <new-empty-directory> and -confirm-migrate")
	}
	if strings.TrimSpace(progressFile) != "" && !vectorRebuild {
		return fmt.Errorf("-vector-rebuild-progress-file requires -vector-rebuild")
	}
	if actionCount != 1 {
		return fmt.Errorf("exactly one maintenance action is required: use one of -clean, -migrate, -vector-rebuild, or -fts-rebuild")
	}

	runtimeConfig, err := loadMaintenanceRuntimeConfiguration(argv0, cfgPath)
	if err != nil {
		return err
	}

	switch {
	case strings.TrimSpace(cleanTarget) != "":
		return runMaintenanceClean(ctx, runtimeConfig.Config, runtimeConfig.Layout, cleanTarget)
	case strings.TrimSpace(migrateTarget) != "":
		if isNativeMigration {
			return runNativeStorageMigration(ctx, runtimeConfig.Config, runtimeConfig.Layout, strings.ToLower(strings.TrimSpace(migrateTarget)), native.OutputDirectory)
		}
		return runMaintenanceMigrate(ctx, runtimeConfig.Config, migrateTarget)
	case native.RebuildFTS:
		return runNativeFTSRebuild(ctx, runtimeConfig.Config)
	case vectorRebuild:
		return runMaintenanceVectorRebuildWithProgressFile(
			ctx,
			runtimeConfig.Config,
			os.Stdin,
			os.Stdout,
			vectorRebuildConfirmed,
			progressFile,
		)
	default:
		return fmt.Errorf("no maintenance action selected")
	}
}

// loadMaintenanceRuntimeConfiguration resolves the standalone layered configuration used by maintenance actions.
// loadMaintenanceRuntimeConfiguration 用于解析维护动作使用的独立分层配置。
func loadMaintenanceRuntimeConfiguration(argv0, cfgPath string) (maintenanceRuntimeConfiguration, error) {
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
