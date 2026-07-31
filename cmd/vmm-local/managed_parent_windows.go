//go:build windows

// managed_parent_windows.go implements exact Windows parent-process liveness checks.
// managed_parent_windows.go 用于实现精确的 Windows 父进程存活检查。
package main

import "golang.org/x/sys/windows"

// managedParentAlive reports whether the exact Windows process still exists.
// managedParentAlive 用于报告精确的 Windows 进程是否仍然存在。
func managedParentAlive(processID int) bool {
	if processID <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(processID))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
