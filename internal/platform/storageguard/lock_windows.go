//go:build windows

// This file implements native writer exclusion with Windows byte-range locks.
// 本文件通过 Windows 字节范围锁实现原生数据库写入者互斥。
package storageguard

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive nonblocking lock on the first byte of file.
// lockFile 非阻塞地独占锁定文件的第一个字节，失败时返回系统错误。
func lockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// unlockFile releases the byte range owned by this file descriptor.
// unlockFile 释放当前文件句柄拥有的字节范围锁，返回系统释放错误。
func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
