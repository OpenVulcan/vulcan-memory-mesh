// These tests verify contention and release without accessing application databases.
// 本文件使用临时文件验证锁竞争与释放，不访问应用数据库。
package storageguard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLockExcludesAnotherOwner checks real OS lock contention and reacquisition after close.
// TestLockExcludesAnotherOwner 验证真实操作系统锁的竞争及关闭后重新获取。
func TestLockExcludesAnotherOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "writer.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := Acquire(path); err == nil {
		_ = second.Close()
		t.Fatal("second owner acquired the same storage lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must retain its identity: %v", err)
	}
}

// TestCanonicalPathHandlesMissingTail verifies deterministic placement before data files exist.
// TestCanonicalPathHandlesMissingTail 验证数据文件尚不存在时仍能得到确定的规范路径。
func TestCanonicalPathHandlesMissingTail(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "data", "sqlite.db")
	got, err := CanonicalPath(path)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(wantRoot, "missing", "data", "sqlite.db") {
		t.Fatalf("canonical path = %q", got)
	}
	if _, err := CanonicalPath(" "); err == nil {
		t.Fatal("blank path accepted")
	}
}
