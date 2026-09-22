//go:build linux || darwin || freebsd

// lookup_unix.go keeps symbol binding on the purego path used by existing runtime FFI code.
// lookup_unix.go 保持现有运行库 FFI 使用的 purego 符号绑定路径。
package nativelance

import "github.com/ebitengine/purego"

// lookupSymbol resolves one exported shared-object symbol before purego binding.
// lookupSymbol 在 purego 绑定前解析一个共享对象导出符号。
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}
