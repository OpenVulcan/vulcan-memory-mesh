//go:build !windows

// native_parent_sync_unix.go durably flushes directory metadata after native sidecar publication on Unix-like hosts.
// native_parent_sync_unix.go 用于在类 Unix 主机上发布原生 sidecar 后持久化目录元数据。
package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// syncNativeParentDirectory flushes the parent directory so a rename or removal cannot be acknowledged before its directory entry is durable.
// syncNativeParentDirectory 刷新父目录，避免在目录项真正持久化前确认重命名或删除已经完成。
func syncNativeParentDirectory(path string) error {
	directory, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("resolve native parent directory: %w", err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open native parent directory for sync: %w", err)
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return fmt.Errorf("sync native parent directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native parent directory after sync: %w", closeErr)
	}
	return nil
}

// syncNativeFile flushes an already-published sidecar before a retry treats it as durable.
// syncNativeFile 刷新已经发布的 sidecar，避免重试把未确认持久化的文件当成可靠状态。
func syncNativeFile(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open native file for sync: %w", err)
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if syncErr != nil {
		return fmt.Errorf("sync native file: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native file after sync: %w", closeErr)
	}
	return nil
}

// renameNativeAtomicFile publishes a temporary native sidecar with the host's atomic replacement primitive.
// renameNativeAtomicFile 使用宿主系统的原子替换原语发布临时 native sidecar。
func renameNativeAtomicFile(source, target string) error {
	return os.Rename(source, target)
}
