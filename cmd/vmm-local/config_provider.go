// config_provider.go bridges explicitly authorized provider tests to the existing application clients.
// config_provider.go 在命令入口层将明确授权的供应商测试连接到现有应用客户端。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
)

// runProviderConfig validates consent and configuration, then emits a bounded diagnostic result with a matching exit code.
// runProviderConfig 验证用户确认及配置，再输出有界诊断结果与匹配的退出码。
func runProviderConfig(command configCommand, exePath, cwd string, output, errorOutput io.Writer) int {
	if !command.allowNetwork {
		fmt.Fprintln(errorOutput, "provider tests require --allow-network and may incur charges")
		return 1
	}
	result := app.ProviderProbeResult{Version: "v1", Purpose: command.purpose, Route: command.route, Class: "configuration"}
	layout, err := config.ResolvePromptLayout(exePath, cwd, command.configPath)
	if err == nil {
		var cfg config.Config
		cfg, err = config.LoadPaths(layout.ConfigPaths(), config.Config{})
		if err == nil {
			result = app.ProbeProvider(context.Background(), cfg, command.purpose, command.route)
		}
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		fmt.Fprintln(errorOutput, "provider test result could not be written")
		return 1
	}
	if !result.Success {
		return 1
	}
	return 0
}
