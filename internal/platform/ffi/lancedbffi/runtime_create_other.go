//go:build !windows

// runtime_create_other.go invokes the LanceDB runtime_create export through purego on non-Windows platforms.
// runtime_create_other.go 用于在非 Windows 平台上通过 purego 调用 LanceDB runtime_create 导出。
package lancedbffi

import "unsafe"

// lookupSymbol is unused on non-Windows platforms because runtime_create is bound directly through purego.
// lookupSymbol 在非 Windows 平台上不会被使用，因为 runtime_create 直接通过 purego 绑定。
func lookupSymbol(_ uintptr, _ string) (uintptr, error) {
	return 0, nil
}

// callRuntimeCreate invokes the bound purego runtime_create function on non-Windows platforms.
// callRuntimeCreate 用于在非 Windows 平台上调用已绑定的 purego runtime_create 函数。
func (lib *Library) callRuntimeCreate(options runtimeOptionsPod) unsafe.Pointer {
	if lib == nil || lib.runtimeCreate == nil {
		return nil
	}
	return lib.runtimeCreate(options)
}

// freeBytes releases one FFI-owned byte buffer through the bound purego function on non-Windows platforms.
// freeBytes 用于在非 Windows 平台上通过已绑定的 purego 函数释放一块 FFI 持有的字节缓冲区。
func (lib *Library) freeBytes(buffer byteBufferPod) {
	if lib == nil || lib.bytesFree == nil {
		return
	}
	lib.bytesFree(buffer)
}
