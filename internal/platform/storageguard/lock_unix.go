//go:build linux || darwin || freebsd

// This file implements native writer exclusion with POSIX advisory file locks.
// 本文件通过类 Unix 文件锁实现原生数据库写入者互斥。
package storageguard

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile acquires an exclusive file lock without waiting for another writer.
// lockFile 非阻塞地获取独占文件锁，其他写入者占用时立即返回错误。
func lockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// unlockFile releases the lock associated with this open descriptor.
// unlockFile 释放当前打开句柄关联的文件锁，返回系统释放错误。
func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
