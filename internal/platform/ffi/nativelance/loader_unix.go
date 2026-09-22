//go:build linux || darwin || freebsd

// loader_unix.go loads the native official LanceDB library on Unix-like systems.
// loader_unix.go 在类 Unix 系统上加载官方原生 LanceDB 动态库。
package nativelance

import "github.com/ebitengine/purego"

// openLibrary opens one shared object with local symbol visibility.
// openLibrary 以本地符号可见性打开一个共享对象。
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

// closeLibrary closes one shared object handle.
// closeLibrary 关闭一个共享对象句柄。
func closeLibrary(handle uintptr) error {
	return purego.Dlclose(handle)
}
