//go:build windows

// lookup_windows.go resolves native DLL procedure addresses for ABI calls that need scalar syscall arguments.
// lookup_windows.go 解析原生 DLL 过程地址，供需要标量 syscall 参数的 ABI 调用使用。
package nativelance

import "syscall"

// lookupSymbol resolves one exported procedure address from a Windows DLL.
// lookupSymbol 从 Windows DLL 解析一个导出过程地址。
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return syscall.GetProcAddress(syscall.Handle(handle), name)
}
