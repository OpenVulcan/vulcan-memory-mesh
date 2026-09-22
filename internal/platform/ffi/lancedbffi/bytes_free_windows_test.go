//go:build windows

// bytes_free_windows_test.go verifies the legacy buffer release ABI without freeing test-owned memory.
// bytes_free_windows_test.go 验证旧库缓冲区释放 ABI，不释放测试持有的内存。
package lancedbffi

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// TestLegacyByteBufferReleaseUsesWindowsAggregateABI verifies all three fields reach the native callback through one aggregate pointer.
// TestLegacyByteBufferReleaseUsesWindowsAggregateABI 验证三个字段通过单一结构体地址完整传入原生回调。
func TestLegacyByteBufferReleaseUsesWindowsAggregateABI(t *testing.T) {
	storage := make([]byte, 8)
	// Unlike a Rust allocation, this fixture is Go-owned and must remain pinned across a callback that may grow the Go stack.
	// 此夹具与 Rust 分配不同，属于 Go 内存，须在可能扩展 Go 栈的回调期间固定地址。
	var pinned runtime.Pinner
	pinned.Pin(&storage[0])
	defer pinned.Unpin()
	want := byteBufferPod{Data: &storage[0], Len: 3, Cap: 8}
	var got byteBufferPod
	called := false
	callback := syscall.NewCallback(func(pointer uintptr) uintptr {
		called = true
		got = *(*byteBufferPod)(pointerFromSyscallResult(pointer))
		return 0
	})
	library := &Library{bytesFreeProc: callback}
	library.freeBytes(want)
	runtime.KeepAlive(storage)
	if !called || got.Data != want.Data || got.Len != want.Len || got.Cap != want.Cap {
		t.Fatalf("buffer release ABI mismatch: called=%v pointer=%p len=%d cap=%d", called, unsafe.Pointer(got.Data), got.Len, got.Cap)
	}
}
