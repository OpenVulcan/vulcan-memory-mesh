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
	// Resolve runtime paths from the executable location and current workspace.
	// 根据可执行文件位置和当前工作区解析运行时路径。
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取程序运行路径失败: %v\n", err)
		os.Exit(1)
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取工作目录失败: %v\n", err)
		os.Exit(1)
	}
	cfgPath := flag.String("config", "", "user config dir (~/.vmm by default); legacy json config file path is still supported")
	flag.Parse()

	// Build the prompt/config layout before any application dependency is created.
	// 在创建任何应用依赖之前先构建提示词与配置布局。
	layout, err := config.ResolvePromptLayout(exePath, wd, *cfgPath, "local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve prompt layout: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[vmm-boot] SystemDir: %s\n", layout.SystemDir)
	fmt.Printf("[vmm-boot] UserDir: %s\n", layout.UserDir)
	for idx, path := range layout.ConfigPaths() {
		fmt.Printf("[vmm-boot] ConfigChain[%d]: %s\n", idx, path)
	}

	// Load prompt assets and merged configuration layers for the local runtime.
	// 为本地运行时加载提示词资产和合并后的配置层。
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.DefaultLocal())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	// Compose the application and start the gRPC service.
	// 完成应用装配并启动 gRPC 服务。
	application, err := app.NewLocal(cfg, prompts, layout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build app: %v\n", err)
		os.Exit(1)
	}
	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "run app: %v\n", err)
		os.Exit(1)
	}
}
