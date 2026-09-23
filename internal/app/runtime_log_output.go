// runtime_log_output.go resolves the filesystem location used by the packaged local runtime to persist mirrored log files.
// runtime_log_output.go 用于解析打包后的本地运行时持久化镜像日志文件时使用的文件系统位置。
package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openvulcan/vmm/internal/config"
)

// ResolveRuntimeLogDir uses an explicit configured directory for service accounts and preserves the package-sibling default for existing configurations.
// ResolveRuntimeLogDir 为服务账户使用显式配置目录，并为旧配置保留包目录同级的默认位置。
func ResolveRuntimeLogDir(cfg config.Config, layout config.PromptLayout) (string, error) {
	if directory := cfg.Logging.Directory; directory != "" {
		// Reject relative and ambiguous roots before any file writer creates directories.
		// 在文件写入器创建目录之前拒绝相对路径和含糊的根路径。
		if directory != strings.TrimSpace(directory) || strings.ContainsAny(directory, "\x00\r\n\t") || !filepath.IsAbs(directory) {
			return "", fmt.Errorf("logging.directory must be an absolute path without surrounding whitespace or control characters")
		}
		return filepath.Clean(directory), nil
	}
	systemDir := strings.TrimSpace(layout.SystemDir)
	if systemDir == "" {
		return "", fmt.Errorf("runtime system dir is empty")
	}
	return filepath.Clean(filepath.Join(systemDir, "..", "logs")), nil
}
