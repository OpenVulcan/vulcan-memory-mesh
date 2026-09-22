//go:build windows

// native_parent_sync_windows.go flushes Windows directory metadata through the documented backup-semantics directory handle.
// native_parent_sync_windows.go 通过 Windows 文档支持的 backup-semantics 目录句柄刷新目录元数据。
package app

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// syncNativeParentDirectory flushes the parent directory handle so sidecar entry changes survive a power-loss boundary.
// syncNativeParentDirectory 刷新父目录句柄，使 sidecar 目录项变更能够跨越断电边界持久化。
func syncNativeParentDirectory(path string) error {
	directory, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("resolve native parent directory: %w", err)
	}
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(directory),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open native parent directory for sync: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.FlushFileBuffers(handle); err != nil {
		return fmt.Errorf("flush native parent directory: %w", err)
	}
	return nil
}

// syncNativeFile flushes an already-published sidecar through a shareable Windows file handle.
// syncNativeFile 通过可共享的 Windows 文件句柄刷新已经发布的 sidecar。
func syncNativeFile(path string) error {
	handle, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return fmt.Errorf("open native file for sync: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.FlushFileBuffers(handle); err != nil {
		return fmt.Errorf("flush native file: %w", err)
	}
	return nil
}

// renameNativeAtomicFile uses MOVEFILE_WRITE_THROUGH so Windows acknowledges the replacement only after the file-system update is flushed.
// renameNativeAtomicFile 使用 MOVEFILE_WRITE_THROUGH，让 Windows 仅在文件系统更新刷新后确认替换完成。
func renameNativeAtomicFile(source, target string) error {
	return windows.MoveFileEx(
		windows.StringToUTF16Ptr(source),
		windows.StringToUTF16Ptr(target),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}
