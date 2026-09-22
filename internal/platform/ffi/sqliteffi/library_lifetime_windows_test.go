//go:build windows

// library_lifetime_windows_test.go verifies resident reuse of the shipped legacy SQLite DLL on Windows.
// library_lifetime_windows_test.go 在 Windows 上验证仓库附带旧 SQLite DLL 的驻留复用。
package sqliteffi

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestLegacyLibraryOpenSharesResidentHandle verifies concurrent opens and post-close reopen use one OS handle.
// TestLegacyLibraryOpenSharesResidentHandle 验证并发打开及关闭后重开都复用同一个操作系统句柄。
func TestLegacyLibraryOpenSharesResidentHandle(t *testing.T) {
	path := legacySQLiteLibraryPathForTest(t)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy SQLite library: %v", err)
	}
	defer func() { _ = first.Close() }()

	const workers = 8
	opened := make([]*Library, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range opened {
		go func(index int) {
			defer wait.Done()
			<-start
			library, openErr := Open(path)
			if openErr != nil {
				errs <- openErr
				return
			}
			opened[index] = library
		}(index)
	}
	close(start)
	wait.Wait()
	close(errs)
	for openErr := range errs {
		t.Fatalf("concurrent open legacy SQLite library: %v", openErr)
	}
	for index, library := range opened {
		if library == nil {
			t.Fatalf("concurrent open %d returned nil library", index)
		}
		if library.handle != first.handle {
			t.Fatalf("concurrent open %d returned handle %#x, want %#x", index, library.handle, first.handle)
		}
		_ = library.Close()
	}

	alias := filepath.Join(filepath.Dir(path), "resident-cache-alias", "..", filepath.Base(path))
	reopened, err := Open(alias)
	if err != nil {
		t.Fatalf("reopen legacy SQLite library after Close: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.handle != first.handle {
		t.Fatalf("reopen returned handle %#x, want resident handle %#x", reopened.handle, first.handle)
	}
}

// legacySQLiteLibraryPathForTest resolves the checked-in DLL or an explicit test override.
// legacySQLiteLibraryPathForTest 解析仓库附带 DLL 或显式测试覆盖路径。
func legacySQLiteLibraryPathForTest(t *testing.T) string {
	t.Helper()
	path := os.Getenv("VMM_LEGACY_SQLITE_LIBRARY")
	if path == "" {
		path = filepath.Join("..", "..", "..", "..", "third_party", "deps", "vldb_sqlite.dll")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("legacy SQLite library is unavailable: %v", err)
	}
	return path
}
