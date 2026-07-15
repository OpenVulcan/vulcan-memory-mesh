// main.go implements the local VMM executable entrypoint.
// main.go 用于实现本地 VMM 可执行入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// main executes the main logic.
// main 用于执行 main 逻辑。
func main() {
	if code := runMain(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// runMain dispatches service-management commands before falling back to the normal foreground runtime.
// runMain 用于在进入普通前台运行时之前，优先分发服务管理命令。
func runMain(args []string) int {
	// Resolve runtime paths from the executable location and current workspace.
	// 根据可执行文件位置和当前工作区解析运行时路径。
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve executable path: %v\n", err)
		return 1
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get working directory: %v\n", err)
		return 1
	}

	serviceCommand, ok, err := parseServiceCommand(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if ok {
		if err := runServiceCommand(serviceCommand, exePath); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}

	cfgPath, err := parseRuntimeConfigPath(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if err := runRuntime(context.Background(), exePath, wd, cfgPath, nil); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return 0
}

// parseRuntimeConfigPath parses only the normal foreground runtime flags so service commands cannot accidentally inherit user configuration flags.
// parseRuntimeConfigPath 仅解析普通前台运行时参数，避免服务管理命令意外继承用户配置参数。
func parseRuntimeConfigPath(args []string) (string, error) {
	fs := flag.NewFlagSet("vmm-local", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", "", "user override root (~/.vmm by default); explicit config files must use .yaml or .yml")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	return *cfgPath, nil
}

// runRuntime loads configuration, composes the application, and runs the local gRPC runtime until shutdown.
// runRuntime 用于加载配置、装配应用并运行本地 gRPC 运行时直到关闭。
func runRuntime(ctx context.Context, exePath string, wd string, cfgPath string, ready func()) error {
	// Build the prompt/config layout before any application dependency is created.
	// 在创建任何应用依赖之前先构建提示词与配置布局。
	layout, err := config.ResolvePromptLayout(exePath, wd, cfgPath)
	if err != nil {
		return fmt.Errorf("resolve prompt layout: %w", err)
	}
	fmt.Printf("[vmm-boot] SystemDir: %s\n", layout.SystemDir)
	fmt.Printf("[vmm-boot] UserDir: %s\n", layout.UserDir)
	for idx, path := range layout.ConfigPaths() {
		fmt.Printf("[vmm-boot] ConfigChain[%d]: %s\n", idx, path)
	}

	// Load the merged configuration layers before composing the normal runtime.
	// 先加载合并后的配置层，再装配正常运行时。
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Load prompt assets only for the normal runtime path because one-shot maintenance actions now live in the standalone vmm-migrate binary.
	// 仅在正常运行路径加载提示词资产，因为一次性维护动作已经迁移到独立的 vmm-migrate 二进制。
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, cfg.Prompts.PromptLanguage)
	if err != nil {
		return err
	}

	// Compose the application and start the gRPC service.
	// 完成应用装配并启动 gRPC 服务。
	application, err := app.NewLocal(cfg, prompts, layout)
	if err != nil {
		return fmt.Errorf("build app: %w", err)
	}
	if err := application.RunWithReady(ctx, ready); err != nil {
		return fmt.Errorf("run app: %w", err)
	}
	return nil
}
