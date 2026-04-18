//go:build windows

// loader_windows.go loads the SQLite dynamic library on Windows.
// loader_windows.go 用于在 Windows 上加载 SQLite 动态库。
package sqliteffi

import "syscall"

// openLibrary loads the target DLL on Windows.
// openLibrary 用于在 Windows 上加载目标 DLL。
func openLibrary(path string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(path)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// closeLibrary releases the loaded DLL handle on Windows.
// closeLibrary 用于在 Windows 上释放已加载的 DLL 句柄。
func closeLibrary(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}
