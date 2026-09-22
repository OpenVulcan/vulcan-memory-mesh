//go:build windows

// bytes_free_windows.go invokes the by-value Rust buffer release using Windows x64 scalar arguments.
// bytes_free_windows.go 使用 Windows x64 标量参数调用按值传递的 Rust 缓冲区释放函数。
package nativelance

import (
	"syscall"
	"unsafe"
)

// callWindowsBytesFree releases one Rust-owned allocation through fixed-width scalar arguments.
// callWindowsBytesFree 通过 syscall.SyscallN 释放一块 Rust 所有的分配。
func (lib *Library) callWindowsBytesFree(output bytesPod) {
	if lib == nil || lib.bytesFreeProc == 0 || output.Data == nil {
		return
	}
	syscall.SyscallN(lib.bytesFreeProc, uintptr(unsafe.Pointer(output.Data)), uintptr(output.Len), uintptr(output.Cap))
}
