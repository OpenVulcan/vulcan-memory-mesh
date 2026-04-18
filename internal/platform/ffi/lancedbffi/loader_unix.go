//go:build linux || darwin || freebsd

// loader_unix.go loads the LanceDB dynamic library on Unix-like systems.
// loader_unix.go 用于在类 Unix 系统上加载 LanceDB 动态库。
package lancedbffi

import "github.com/ebitengine/purego"

// openLibrary opens the target dynamic library on Unix-like systems.
// openLibrary 用于在类 Unix 系统上打开目标动态库。
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

// closeLibrary closes the loaded dynamic library handle on Unix-like systems.
// closeLibrary 用于在类 Unix 系统上关闭已加载的动态库句柄。
func closeLibrary(handle uintptr) error {
	return purego.Dlclose(handle)
}
