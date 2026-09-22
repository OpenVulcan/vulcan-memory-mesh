//go:build windows

// runtime_create_windows.go invokes the LanceDB runtime_create export through the Windows syscall ABI because purego cannot pass large structs by value on Windows.
// runtime_create_windows.go 用于通过 Windows syscall ABI 调用 LanceDB runtime_create 导出，因为 purego 在 Windows 上无法按值传递大结构体。
package lancedbffi

import (
	"runtime"
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
	runtime.KeepAlive(options)
	return pointerFromSyscallResult(result)
}

// pointerFromSyscallResult converts one Windows syscall uintptr result into the opaque FFI handle type without relying on a direct uintptr-to-pointer cast at the call site.
// pointerFromSyscallResult 用于把一次 Windows syscall 返回的 uintptr 结果转换成不透明 FFI 句柄类型，避免在调用点直接做 uintptr 到指针的强转。
func pointerFromSyscallResult(result uintptr) unsafe.Pointer {
	var handle unsafe.Pointer
	*(*uintptr)(unsafe.Pointer(&handle)) = result
	return handle
}

// freeBytes releases one FFI-owned byte buffer through the Windows syscall ABI path.
// freeBytes 用于通过 Windows syscall ABI 路径释放一块由 FFI 持有的字节缓冲区。
func (lib *Library) freeBytes(buffer byteBufferPod) {
	if lib == nil || lib.bytesFreeProc == 0 {
		return
	}
	// The v0.1.5 export accepts a 24-byte struct by value; Windows x64 passes that aggregate through a pointer to caller-owned storage.
	// v0.1.5 导出按值接收 24 字节结构体；Windows x64 必须传入调用方保存的结构体地址。
	syscall.SyscallN(lib.bytesFreeProc, uintptr(unsafe.Pointer(&buffer)))
	runtime.KeepAlive(buffer)
}
