// storage_snapshot_test.go verifies source protection and real legacy SQLite snapshot behavior.
// storage_snapshot_test.go 验证源库保护与真实旧版 SQLite 快照行为。
package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

// TestValidateLegacySQLiteSnapshotPathsRejectsUnsafeTargets ensures an existing target and a non-private root fail before any backend is opened.
// TestValidateLegacySQLiteSnapshotPathsRejectsUnsafeTargets 确保已存在目标和非私有目录会在打开后端前失败。
func TestValidateLegacySQLiteSnapshotPathsRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	if err := os.WriteFile(source, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	layout := localStorageLayout{SQLiteDatabase: source}
	destinationRoot := t.TempDir()
	destination := filepath.Join(destinationRoot, "snapshot.db")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLegacySQLiteSnapshotPaths(layout, destination); err == nil {
		t.Fatal("expected an existing destination to be rejected")
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	privateRoot := filepath.Join(destinationRoot, "private")
	if err := os.Mkdir(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := validateLegacySQLiteSnapshotPaths(layout, filepath.Join(privateRoot, "snapshot.db")); err != nil {
		t.Fatalf("private destination should be accepted: %v", err)
	}
}

// TestLegacySQLiteFileLockRejectsSecondRuntime proves the legacy FFI default lock prevents two independent runtimes from opening one database.
// TestLegacySQLiteFileLockRejectsSecondRuntime 证明旧 FFI 默认锁会阻止两个独立 runtime 同时打开同一数据库。
func TestLegacySQLiteFileLockRejectsSecondRuntime(t *testing.T) {
	libraryPath := testLegacySQLiteLibraryPath(t)
	root := t.TempDir()
	databasePath := filepath.Join(root, "source.db")
	firstLibrary, err := sqliteffi.Open(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstLibrary.Close()
	firstRuntime, err := firstLibrary.CreateRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer firstRuntime.Close()
	firstDatabase, err := firstRuntime.OpenDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDatabase.Close()

	secondLibrary, err := sqliteffi.Open(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLibrary.Close()
	secondRuntime, err := secondLibrary.CreateRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer secondRuntime.Close()
	if _, err := secondRuntime.OpenDatabase(databasePath); err == nil {
		t.Fatal("expected the second runtime to be rejected by the legacy database file lock")
	}
}

// TestExportLegacySQLiteSnapshotPreservesSourceAndOrdinaryRows uses the shipped legacy DLL and verifies one consistent VACUUM INTO snapshot.
// TestExportLegacySQLiteSnapshotPreservesSourceAndOrdinaryRows 使用仓库中的旧 DLL，验证一次一致性 VACUUM INTO 快照。
func TestExportLegacySQLiteSnapshotPreservesSourceAndOrdinaryRows(t *testing.T) {
	libraryPath := testLegacySQLiteLibraryPath(t)
	root := t.TempDir()
	promptLayout := config.PromptLayout{SystemDir: filepath.Join(root, "output", "configs")}
	layout, err := ResolveLocalStorageLayoutForPromptLayout(promptLayout)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.SQLiteDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.LanceDBDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	seedLegacySQLiteDatabase(t, libraryPath, layout.SQLiteDatabase)

	before := readOrdinarySQLiteSnapshot(t, layout.SQLiteDatabase)
	assertLegacyFTSSearchStillWorks(t, libraryPath, layout.SQLiteDatabase)
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	cfg := config.DefaultBase()
	cfg.Storage.Mode = "split"
	cfg.SQLite.Timeout.Duration = 30 * time.Second
	if err := ExportLegacySQLiteSnapshot(context.Background(), cfg, promptLayout, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal(err)
	}
	after := readOrdinarySQLiteSnapshot(t, layout.SQLiteDatabase)
	if before != after {
		t.Fatalf("source ordinary schema/data changed: before=%+v after=%+v", before, after)
	}

	copyDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(destination)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var copiedCount int
	if err := copyDB.QueryRow("SELECT count(*) FROM snapshot_probe").Scan(&copiedCount); err != nil {
		t.Fatal(err)
	}
	if copiedCount != 2 {
		t.Fatalf("snapshot row count = %d, want 2", copiedCount)
	}
	var copiedDDL string
	if err := copyDB.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='snapshot_probe'").Scan(&copiedDDL); err != nil {
		t.Fatal(err)
	}
	if copiedDDL == "" {
		t.Fatal("snapshot omitted ordinary table schema")
	}
	var ftsObjects int
	if err := copyDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='snapshot_search'").Scan(&ftsObjects); err != nil {
		t.Fatal(err)
	}
	if ftsObjects == 0 {
		t.Fatal("snapshot omitted legacy FTS schema")
	}
	assertLegacyFTSSearchStillWorks(t, libraryPath, layout.SQLiteDatabase)
}

// TestLegacySQLiteSnapshotShutdownRetainsOwnershipOnCloseError proves parent handles stay owned until each close succeeds.
// TestLegacySQLiteSnapshotShutdownRetainsOwnershipOnCloseError 验证每个关闭成功前都保留父句柄所有权。
func TestLegacySQLiteSnapshotShutdownRetainsOwnershipOnCloseError(t *testing.T) {
	database := &snapshotDatabaseCloseProbe{}
	database.closeErr = errors.New("database close failed")
	runtimeHandle := &snapshotCloseProbe{closeErr: errors.New("runtime close failed")}
	library := &snapshotCloseProbe{closeErr: errors.New("library close failed")}
	session := &LegacySQLiteSnapshotSession{
		database:      database,
		runtimeHandle: runtimeHandle,
		library:       library,
	}

	if err := session.Shutdown(context.Background()); err == nil {
		t.Fatal("expected database close failure")
	}
	if session.closed || session.database == nil || runtimeHandle.closeCalls != 0 || library.closeCalls != 0 {
		t.Fatal("database close failure must retain all parent handles")
	}

	database.closeErr = nil
	if err := session.Shutdown(context.Background()); err == nil {
		t.Fatal("expected runtime close failure")
	}
	if session.closed || session.database != nil || session.runtimeHandle == nil || library.closeCalls != 0 {
		t.Fatal("runtime close failure must retain the library handle")
	}

	runtimeHandle.closeErr = nil
	if err := session.Shutdown(context.Background()); err == nil {
		t.Fatal("expected library close failure")
	}
	if session.closed || session.runtimeHandle != nil || session.library == nil {
		t.Fatal("library close failure must retain the library ownership marker")
	}

	library.closeErr = nil
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("final shutdown: %v", err)
	}
	if !session.closed || session.database != nil || session.runtimeHandle != nil || session.library != nil {
		t.Fatal("successful retry must release every handle and mark the session closed")
	}
	if err := session.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
}

// snapshotCloseProbe records close attempts and can inject a deterministic cleanup failure.
// snapshotCloseProbe 记录关闭尝试次数并可注入确定性的清理失败。
type snapshotCloseProbe struct {
	closeErr   error
	closeCalls int
}

// Close returns the configured probe error while recording ownership cleanup attempts.
// Close 返回配置的探针错误并记录所有权清理尝试。
func (probe *snapshotCloseProbe) Close() error {
	probe.closeCalls++
	return probe.closeErr
}

// snapshotDatabaseCloseProbe supplies the database execution method required by the session interface.
// snapshotDatabaseCloseProbe 提供会话接口所需的数据库执行方法。
type snapshotDatabaseCloseProbe struct {
	snapshotCloseProbe
}

// ExecuteScript is unused by the cleanup test and returns an empty successful result.
// ExecuteScript 在清理测试中不会调用，仅返回空的成功结果。
func (*snapshotDatabaseCloseProbe) ExecuteScript(context.Context, string, []sqliteffi.SQLValue, string) (sqliteffi.ExecuteResult, error) {
	return sqliteffi.ExecuteResult{}, nil
}

// sqliteOrdinaryObservation records only ordinary source data so the test never queries a legacy tokenizer virtual table through modernc.
// sqliteOrdinaryObservation 只记录普通源表数据，避免测试通过 modernc 查询旧分词虚拟表。
type sqliteOrdinaryObservation struct {
	count         int
	ddl           string
	schemaVersion int
}

// readOrdinarySQLiteSnapshot reads ordinary source facts through a read-only modernc connection.
// readOrdinarySQLiteSnapshot 使用只读 modernc 连接读取普通源事实。
func readOrdinarySQLiteSnapshot(t *testing.T, path string) sqliteOrdinaryObservation {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var observation sqliteOrdinaryObservation
	if err := db.QueryRow("SELECT count(*) FROM snapshot_probe").Scan(&observation.count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='snapshot_probe'").Scan(&observation.ddl); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA schema_version").Scan(&observation.schemaVersion); err != nil {
		t.Fatal(err)
	}
	return observation
}

// seedLegacySQLiteDatabase creates ordinary rows and one old Jieba FTS index through the real legacy FFI.
// seedLegacySQLiteDatabase 通过真实旧 FFI 创建普通数据行和一个旧 Jieba FTS 索引。
func seedLegacySQLiteDatabase(t *testing.T, libraryPath, databasePath string) {
	t.Helper()
	library, err := sqliteffi.Open(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	runtimeHandle, err := library.CreateRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeHandle.Close()
	database, err := runtimeHandle.OpenDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(context.Background(), "CREATE TABLE snapshot_probe(id INTEGER PRIMARY KEY, body TEXT NOT NULL); INSERT INTO snapshot_probe(id, body) VALUES (1, '一条中文内容'), (2, '第二条中文内容');", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureFtsIndex(context.Background(), "snapshot_search", sqliteffi.TokenizerJieba); err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpsertFtsDocument(context.Background(), "snapshot_search", sqliteffi.TokenizerJieba, "1", "", "标题", "中文内容"); err != nil {
		t.Fatal(err)
	}
}

// assertLegacyFTSSearchStillWorks reopens the source with the old FFI and checks the tokenizer-backed search surface.
// assertLegacyFTSSearchStillWorks 使用旧 FFI 重新打开源库并检查分词器搜索面仍然可用。
func assertLegacyFTSSearchStillWorks(t *testing.T, libraryPath, databasePath string) {
	t.Helper()
	library, err := sqliteffi.Open(libraryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	runtimeHandle, err := library.CreateRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeHandle.Close()
	database, err := runtimeHandle.OpenDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	result, err := database.SearchFts(context.Background(), "snapshot_search", sqliteffi.TokenizerJieba, "中文", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("legacy FTS search returned no hits after snapshot")
	}
}

// testLegacySQLiteLibraryPath resolves the checked-in test dependency and skips hosts without the old FFI artifact.
// testLegacySQLiteLibraryPath 解析仓库中的旧 FFI 测试依赖，缺少对应产物时跳过测试。
func testLegacySQLiteLibraryPath(t *testing.T) string {
	t.Helper()
	libraryName := "libvldb_sqlite.so"
	if runtime.GOOS == "windows" {
		libraryName = "vldb_sqlite.dll"
	} else if runtime.GOOS == "darwin" {
		libraryName = "libvldb_sqlite.dylib"
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "third_party", "deps", libraryName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("legacy SQLite library is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	return path
}
