// config_effective.go connects the read-only CLI export to the runtime's authoritative configuration loader.
// config_effective.go 在命令入口层将只读配置导出连接到运行时权威配置加载器。
package main

import (
	"fmt"
	"io"

	"github.com/openvulcan/vmm/internal/config"
)

// runEffectiveConfig loads the requested layout and writes a redacted document; it returns a nonzero code without dumping invalid input.
// runEffectiveConfig 加载指定布局并写入脱敏文档；失败返回非零码且不输出无效输入内容。
func runEffectiveConfig(exePath, cwd, configPath string, output, errorOutput io.Writer) int {
	layout, err := config.ResolvePromptLayout(exePath, cwd, configPath)
	if err != nil {
		fmt.Fprintln(errorOutput, "effective configuration layout could not be resolved")
		return 1
	}
	// Inspect the same merged, expanded and normalized values as startup, without constructing runtime clients.
	// 检查与启动一致的合并、变量展开和归一化结果，同时不构建运行时客户端。
	cfg, sources, err := config.LoadPathsWithSources(layout.ConfigPaths(), config.Config{})
	if err != nil {
		fmt.Fprintln(errorOutput, "effective configuration could not be loaded and validated")
		return 1
	}
	if err := config.WriteEffectiveConfigWithSources(output, cfg, sources); err != nil {
		fmt.Fprintln(errorOutput, "effective configuration could not be written")
		return 1
	}
	return 0
}
