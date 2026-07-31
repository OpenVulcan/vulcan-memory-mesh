// runtime_log_output.go resolves the filesystem location used by the packaged local runtime to persist mirrored log files.
// runtime_log_output.go 用于解析打包后的本地运行时持久化镜像日志文件时使用的文件系统位置。
package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openvulcan/vmm/internal/config"
)

// resolveRuntimeLogDir maps the resolved system config root to the sibling `logs` directory so packaged binaries use `output/logs` and go-run development uses the repository-root `logs`.
// resolveRuntimeLogDir 用于把解析出的系统配置根映射到同级 `logs` 目录；这样打包二进制会使用 `output/logs`，而 go run 调试会使用仓库根的 `logs`。
func resolveRuntimeLogDir(layout config.PromptLayout) (string, error) {
	return resolveRuntimeLogDirWithDataRoot(layout, "")
}

// resolveRuntimeLogDirWithDataRoot keeps managed runtime logs beside the explicit data root while preserving standalone layout behavior.
// resolveRuntimeLogDirWithDataRoot 用于把托管运行时日志放在显式数据根旁，同时保留独立运行模式的布局行为。
func resolveRuntimeLogDirWithDataRoot(layout config.PromptLayout, dataRoot string) (string, error) {
	if strings.TrimSpace(dataRoot) != "" {
		if !filepath.IsAbs(dataRoot) {
			return "", fmt.Errorf("managed runtime data root must be absolute")
		}
		return filepath.Clean(filepath.Join(dataRoot, "..", "logs")), nil
	}
	systemDir := strings.TrimSpace(layout.SystemDir)
	if systemDir == "" {
		return "", fmt.Errorf("runtime system dir is empty")
	}
	return filepath.Clean(filepath.Join(systemDir, "..", "logs")), nil
}
