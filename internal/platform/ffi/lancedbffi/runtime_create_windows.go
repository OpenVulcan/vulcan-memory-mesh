//go:build windows

// runtime_create_windows.go invokes the LanceDB runtime_create export through the Windows syscall ABI because purego cannot pass large structs by value on Windows.
// runtime_create_windows.go 用于通过 Windows syscall ABI 调用 LanceDB runtime_create 导出，因为 purego 在 Windows 上无法按值传递大结构体。
package lancedbffi

import (
	"syscall"
	"unsafe"
)

// lookupSymbol resolves one exported procedure address from the loaded DLL.
// lookupSymbol 用于从已加载 DLL 中解析一个导出过程地址。
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	addr, err := syscall.GetProcAddress(syscall.Handle(handle), name)
	if err != nil {
		return 0, err
	}
	return addr, nil
}

// callRuntimeCreate invokes the runtime_create export using the Windows x64 ABI path.
// callRuntimeCreate 用于通过 Windows x64 ABI 路径调用 runtime_create 导出。
func (lib *Library) callRuntimeCreate(options runtimeOptionsPod) unsafe.Pointer {
	if lib == nil || lib.runtimeCreateProc == 0 {
		return nil
	}
	result, _, _ := syscall.SyscallN(lib.runtimeCreateProc, uintptr(unsafe.Pointer(&options)))
	return unsafe.Pointer(result)
}

// freeBytes releases one FFI-owned byte buffer through the Windows syscall ABI path.
// freeBytes 用于通过 Windows syscall ABI 路径释放一块由 FFI 持有的字节缓冲区。
func (lib *Library) freeBytes(buffer byteBufferPod) {
	if lib == nil || lib.bytesFreeProc == 0 {
		return
	}
	syscall.SyscallN(
		lib.bytesFreeProc,
		uintptr(unsafe.Pointer(buffer.Data)),
		buffer.Len,
		buffer.Cap,
	)
}
