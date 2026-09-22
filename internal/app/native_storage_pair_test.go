// native_storage_pair_test.go verifies the path-independent native storage pairing contract.
// native_storage_pair_test.go 验证与路径无关的原生存储组合契约。
package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestEnsureNativeStoragePairInitializesFreshPair verifies both markers are created with one identity and private permissions.
// TestEnsureNativeStoragePairInitializesFreshPair 验证全新组合创建两个相同身份且权限私有的 marker。
func TestEnsureNativeStoragePairInitializesFreshPair(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := ensureNativeStoragePair(layout); err != nil {
		t.Fatalf("ensure fresh pair: %v", err)
	}
	sqliteMarker := testReadPairMarkerFile(t, layout.SQLiteDatabase+nativeSQLitePairMarkerSuffix)
	lanceMarker := testReadPairMarkerFile(t, filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName))
	if sqliteMarker != lanceMarker {
		t.Fatalf("pair markers differ: SQLite=%+v LanceDB=%+v", sqliteMarker, lanceMarker)
	}
	if sqliteMarker.FormatVersion != nativeStoragePairFormatVersion || strings.TrimSpace(sqliteMarker.PairID) == "" {
		t.Fatalf("unexpected fresh marker: %+v", sqliteMarker)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{layout.SQLiteDatabase + nativeSQLitePairMarkerSuffix, filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName)} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat marker %q: %v", path, err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("marker %q has permission %o, want 600", path, info.Mode().Perm())
			}
		}
	}
	if err := ensureNativeStoragePair(layout); err != nil {
		t.Fatalf("recheck initialized pair: %v", err)
	}
}

// TestEnsureNativeStoragePairAcceptsMatchingExistingPair verifies an already initialized pair is reusable after relocation.
// TestEnsureNativeStoragePairAcceptsMatchingExistingPair 验证已有匹配组合可在路径搬迁后继续使用。
func TestEnsureNativeStoragePairAcceptsMatchingExistingPair(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := os.MkdirAll(layout.LanceDBDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.SQLiteDatabase, []byte("native database"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := nativeStoragePairMarker{FormatVersion: nativeStoragePairFormatVersion, PairID: "relocated-pair"}
	testWritePairMarkerFile(t, layout.SQLiteDatabase+nativeSQLitePairMarkerSuffix, marker)
	testWritePairMarkerFile(t, filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName), marker)
	if err := ensureNativeStoragePair(layout); err != nil {
		t.Fatalf("ensure matching existing pair: %v", err)
	}
}

// TestEnsureNativeStoragePairRejectsMismatchedMarkers verifies two valid but unrelated stores cannot be combined.
// TestEnsureNativeStoragePairRejectsMismatchedMarkers 验证两个格式有效但身份不同的存储不能被拼接。
func TestEnsureNativeStoragePairRejectsMismatchedMarkers(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := os.MkdirAll(layout.LanceDBDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	markerA := nativeStoragePairMarker{FormatVersion: nativeStoragePairFormatVersion, PairID: "pair-a"}
	markerB := nativeStoragePairMarker{FormatVersion: nativeStoragePairFormatVersion, PairID: "pair-b"}
	testWritePairMarkerFile(t, layout.SQLiteDatabase+nativeSQLitePairMarkerSuffix, markerA)
	testWritePairMarkerFile(t, filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName), markerB)
	if err := ensureNativeStoragePair(layout); err == nil || !strings.Contains(err.Error(), "identities differ") {
		t.Fatalf("expected pair identity mismatch, got %v", err)
	}
}

// TestEnsureNativeStoragePairRejectsIncompletePair verifies a single surviving marker is never repaired automatically.
// TestEnsureNativeStoragePairRejectsIncompletePair 验证只剩一个 marker 时绝不自动修复或猜测。
func TestEnsureNativeStoragePairRejectsIncompletePair(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	marker := nativeStoragePairMarker{FormatVersion: nativeStoragePairFormatVersion, PairID: "partial-pair"}
	testWritePairMarkerFile(t, layout.SQLiteDatabase+nativeSQLitePairMarkerSuffix, marker)
	if err := ensureNativeStoragePair(layout); err == nil || !strings.Contains(err.Error(), "markers are incomplete") {
		t.Fatalf("expected incomplete pair rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.LanceDBDirectory, nativeLancePairMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected LanceDB marker after incomplete pair rejection: %v", err)
	}
}

// TestEnsureNativeStoragePairRejectsExistingSQLiteWithoutMarker verifies an unknown existing database cannot be adopted.
// TestEnsureNativeStoragePairRejectsExistingSQLiteWithoutMarker 验证已有但未知的 SQLite 数据库不能被接管。
func TestEnsureNativeStoragePairRejectsExistingSQLiteWithoutMarker(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := os.MkdirAll(layout.LanceDBDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.SQLiteDatabase, []byte("pre-existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeStoragePair(layout); err == nil || !strings.Contains(err.Error(), "exists without a pair marker") {
		t.Fatalf("expected existing SQLite rejection, got %v", err)
	}
}

// TestEnsureNativeStoragePairRejectsNonEmptyLanceDirectory verifies only the exact owner lock is tolerated during initialization.
// TestEnsureNativeStoragePairRejectsNonEmptyLanceDirectory 验证初始化时只允许精确的所有权锁存在。
func TestEnsureNativeStoragePairRejectsNonEmptyLanceDirectory(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := os.MkdirAll(layout.LanceDBDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.LanceDBDirectory, "old-table.data"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeStoragePair(layout); err == nil || !strings.Contains(err.Error(), "is not new") {
		t.Fatalf("expected non-empty LanceDB rejection, got %v", err)
	}
	if _, err := os.Stat(layout.SQLiteDatabase + nativeSQLitePairMarkerSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected SQLite marker after non-empty LanceDB rejection: %v", err)
	}
}

// TestEnsureNativeStoragePairRejectsNonFileOwnerLock verifies a directory cannot masquerade as the owner lock.
// TestEnsureNativeStoragePairRejectsNonFileOwnerLock 验证目录不能冒充所有权锁文件。
func TestEnsureNativeStoragePairRejectsNonFileOwnerLock(t *testing.T) {
	layout := testNativeStoragePairLayout(t)
	if err := os.MkdirAll(filepath.Join(layout.LanceDBDirectory, nativeWriterLockName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeStoragePair(layout); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-file owner lock rejection, got %v", err)
	}
}

// TestDecodeNativeStoragePairMarkerRejectsMalformedInput verifies strict object shape, duplicate detection, and bounded values.
// TestDecodeNativeStoragePairMarkerRejectsMalformedInput 验证对象结构、重复字段和有界输入的严格拒绝规则。
func TestDecodeNativeStoragePairMarkerRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "unknown field", data: `{"format_version":1,"pair_id":"x","path":"/tmp"}`},
		{name: "duplicate field", data: `{"format_version":1,"pair_id":"x","pair_id":"y"}`},
		{name: "missing field", data: `{"format_version":1}`},
		{name: "empty identity", data: `{"format_version":1,"pair_id":"  "}`},
		{name: "wrong version", data: `{"format_version":2,"pair_id":"x"}`},
		{name: "short read", data: `{"format_version":1,"pair_id":"`},
		{name: "trailing value", data: `{"format_version":1,"pair_id":"x"}{}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := decodeNativeStoragePairMarker([]byte(testCase.data)); err == nil {
				t.Fatalf("expected malformed marker rejection for %s", testCase.name)
			}
		})
	}
}

// TestReadNativeStoragePairMarkerRejectsInsecureOrNonRegularFiles verifies marker ownership and permission boundaries.
// TestReadNativeStoragePairMarkerRejectsInsecureOrNonRegularFiles 验证 marker 的文件类型和权限边界。
func TestReadNativeStoragePairMarkerRejectsInsecureOrNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits through os.Stat")
	}
	root := t.TempDir()
	path := filepath.Join(root, nativeLancePairMarkerName)
	marker := nativeStoragePairMarker{FormatVersion: nativeStoragePairFormatVersion, PairID: "permission-pair"}
	testWritePairMarkerFile(t, path, marker)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readNativeStoragePairMarker(path, "LanceDB"); err == nil || !strings.Contains(err.Error(), "must be private") {
		t.Fatalf("expected insecure marker rejection, got %v", err)
	}
}

// TestWriteNativeStoragePairMarkerDoesNotReplaceExistingFile verifies first-writer-wins behavior on marker creation.
// TestWriteNativeStoragePairMarkerDoesNotReplaceExistingFile 验证 marker 创建遵循首写者优先且不会覆盖已有文件。
func TestWriteNativeStoragePairMarkerDoesNotReplaceExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, nativeSQLitePairMarkerSuffix)
	original := []byte(`{"format_version":1,"pair_id":"original"}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeStoragePairMarker(path, nativeStoragePairMarker{FormatVersion: 1, PairID: "replacement"}, "SQLite"); err == nil {
		t.Fatal("expected existing marker creation to fail")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing marker was replaced: got %q", got)
	}
}

// testNativeStoragePairLayout creates isolated paths without touching any configured or user database.
// testNativeStoragePairLayout 创建隔离路径，不接触任何配置数据库或用户数据。
func testNativeStoragePairLayout(t *testing.T) nativeStorageLayout {
	t.Helper()
	root := t.TempDir()
	return nativeStorageLayout{
		SQLiteDatabase:   filepath.Join(root, "native", "sqlite.db"),
		LanceDBDirectory: filepath.Join(root, "native", "lancedb"),
	}
}

// testWritePairMarkerFile writes a test marker using the production JSON and permission contract.
// testWritePairMarkerFile 使用生产 JSON 与权限契约写入测试 marker。
func testWritePairMarkerFile(t *testing.T, path string, marker nativeStoragePairMarker) {
	t.Helper()
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// testReadPairMarkerFile reads a valid test marker and fails the test on any malformed result.
// testReadPairMarkerFile 读取有效测试 marker，遇到任何格式错误立即失败。
func testReadPairMarkerFile(t *testing.T, path string) nativeStoragePairMarker {
	t.Helper()
	marker, present, err := readNativeStoragePairMarker(path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("marker %q is missing", path)
	}
	return marker
}
