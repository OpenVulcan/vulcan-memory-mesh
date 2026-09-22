//go:build windows

// loader_windows.go loads the native official LanceDB library on Windows.
// loader_windows.go 在 Windows 上加载官方原生 LanceDB 动态库。
package nativelance

import "syscall"

// openLibrary loads one native DLL and returns its process handle.
// openLibrary 加载一个原生 DLL 并返回进程句柄。
func openLibrary(path string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(path)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// closeLibrary releases one native DLL process handle.
// closeLibrary 释放一个原生 DLL 进程句柄。
func closeLibrary(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}
