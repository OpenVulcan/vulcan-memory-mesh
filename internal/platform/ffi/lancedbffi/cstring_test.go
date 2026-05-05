// cstring_test.go verifies the bounded LanceDB FFI C-string reader keeps normal strings intact and rejects malformed unterminated buffers.
// cstring_test.go 用于验证带上界的 LanceDB FFI C 字符串读取器既能保留正常字符串，也会拒绝畸形的非终止缓冲区。
package lancedbffi

import (
	"bytes"
	"strings"
	"testing"
)

// TestReadCStringReturnsTerminatedPrefix verifies the bounded reader still returns the exact prefix before the first NUL terminator.
// TestReadCStringReturnsTerminatedPrefix 用于验证带上界的读取器仍会返回首个 NUL 终止符之前的精确前缀。
func TestReadCStringReturnsTerminatedPrefix(t *testing.T) {
	buffer := append([]byte("hello"), 0, 'x')

	got, err := readCString(&buffer[0])
	if err != nil {
		t.Fatalf("read c string: %v", err)
	}
	if got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
}

// TestReadCStringRejectsUnterminatedBuffer verifies the bounded reader stops with one explicit error instead of scanning past the audit limit when the buffer never terminates.
// TestReadCStringRejectsUnterminatedBuffer 用于验证当缓冲区始终没有终止符时，带上界的读取器会在审计上界处显式报错，而不是继续无界扫描。
func TestReadCStringRejectsUnterminatedBuffer(t *testing.T) {
	buffer := bytes.Repeat([]byte{'a'}, maxCStringReadBytes)

	got, err := readCString(&buffer[0])
	if err == nil {
		t.Fatal("expected unterminated buffer error")
	}
	if got != "" {
		t.Fatalf("expected empty string on failure, got %q", got)
	}
	if !strings.Contains(err.Error(), "not NUL-terminated") {
		t.Fatalf("unexpected error: %v", err)
	}
}
