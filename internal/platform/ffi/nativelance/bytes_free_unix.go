//go:build linux || darwin || freebsd

// bytes_free_unix.go keeps buffer release on the purego function binding path.
// bytes_free_unix.go 让缓冲区释放保持在 purego 函数绑定路径上。
package nativelance

// callWindowsBytesFree is unreachable on Unix and exists only for shared source compilation.
// callWindowsBytesFree 在 Unix 上不可达，仅为共享源码编译提供定义。
func (lib *Library) callWindowsBytesFree(output bytesPod) {}
