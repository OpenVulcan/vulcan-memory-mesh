package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

func main() {
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

	layout, err := config.ResolvePromptLayout(exePath, wd, *cfgPath, "local")
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve prompt layout: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[vmm-boot] SystemDir: %s\n", layout.SystemDir)
	fmt.Printf("[vmm-boot] UserDir: %s\n", layout.UserDir)
	fmt.Printf("[vmm-boot] AppConfig: %s\n", layout.AppConfigPath)

	if _, err := config.NewPromptManager(layout.SystemDir, layout.UserDir); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(layout.AppConfigPath, config.DefaultLocal())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	application, err := app.NewLocal(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build app: %v\n", err)
		os.Exit(1)
	}

	if err := application.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "run app: %v\n", err)
		os.Exit(1)
	}
}
