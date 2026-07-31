//go:build !windows

// managed_parent_unix.go implements exact Unix parent-process liveness checks.
// managed_parent_unix.go 用于实现精确的 Unix 父进程存活检查。
package main

import "golang.org/x/sys/unix"

// managedParentAlive reports whether the exact Unix process still exists.
// managedParentAlive 用于报告精确的 Unix 进程是否仍然存在。
func managedParentAlive(processID int) bool {
	if processID <= 0 {
		return false
	}
	err := unix.Kill(processID, 0)
	return err == nil || err == unix.EPERM
}
