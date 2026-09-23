// local_storage_acl_other.go enforces private local-data directory mode bits on non-Windows systems.
// local_storage_acl_other.go 在非 Windows 系统上强制本地数据目录使用私有权限位。
//go:build !windows

package app

import (
	"fmt"
	"os"
)

// ensurePrivateDirectoryTree creates a local-data directory tree with the requested Unix mode.
// ensurePrivateDirectoryTree 按指定 Unix 权限创建本地数据目录树。
func ensurePrivateDirectoryTree(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create private storage directory %q: %w", path, err)
	}
	return nil
}

// validatePrivateDirectory validates Unix mode bits without changing an existing directory.
// validatePrivateDirectory 校验 Unix 权限位，且不修改已有目录。
func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat storage.local_data_root %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("storage.local_data_root %q is not a directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("storage.local_data_root %q must be private mode 0700", path)
	}
	return nil
}
