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

// main executes the standalone maintenance bootstrap and dispatches to the selected one-shot action.
// main 用于执行独立维护工具的启动流程，并分发到选中的一次性动作。
func main() {
	cfgPath := flag.String("config", "", "user override root (~/.vmm by default); explicit config files must use .yaml or .yml")
	cleanTarget := flag.String("clean", "", "maintenance cleanup target: sqlite, lancedb, postgres, or all")
	migrateTarget := flag.String("migrate", "", "maintenance migration target: split-to-combined")
	vectorRebuild := flag.Bool("vector-rebuild", false, "rebuild vectors with the currently configured embedding model")
	flag.Parse()

	ctx, stop := buildSignalAwareMainContext()
	defer stop()

	if err := run(ctx, os.Args[0], *cfgPath, *cleanTarget, *migrateTarget, *vectorRebuild); err != nil {
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
func run(ctx context.Context, argv0, cfgPath, cleanTarget, migrateTarget string, vectorRebuild bool) error {
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

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	layout, err := config.ResolvePromptLayout(exePath, wd, cfgPath, "config")
	if err != nil {
		return fmt.Errorf("resolve config layout: %w", err)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	switch {
	case strings.TrimSpace(cleanTarget) != "":
		return runMaintenanceClean(ctx, cfg, layout, cleanTarget)
	case strings.TrimSpace(migrateTarget) != "":
		return runMaintenanceMigrate(ctx, cfg, migrateTarget)
	case vectorRebuild:
		return runMaintenanceVectorRebuild(ctx, cfg, os.Stdin, os.Stdout)
	default:
		return fmt.Errorf("no maintenance action selected")
	}
}
