// native_copy_test.go verifies native snapshot copying with temporary SQLite files and typed values.
// native_copy_test.go 使用临时 SQLite 文件验证原生快照复制和类型保真值。
package storagemigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestCopyNativeSnapshotPreservesTypedRowsAndSchemaVersions verifies IDs, blobs, NULLs, and timestamps survive the copy unchanged.
// TestCopyNativeSnapshotPreservesTypedRowsAndSchemaVersions 验证 ID、二进制、NULL 和时间戳在复制后保持不变。
func TestCopyNativeSnapshotPreservesTypedRowsAndSchemaVersions(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)

	source := openNativeCopyTestDatabase(t, sourcePath)
	_, err := source.Exec(`INSERT INTO vmm_memory_nodes (id, payload) VALUES (?, ?), (?, ?)`, int64(9007199254740993), []byte{0, 1, 255}, int64(2), nil)
	if err != nil {
		source.Close()
		t.Fatalf("insert typed source rows: %v", err)
	}
	if _, err := source.Exec(`INSERT INTO vmm_schema_versions (component, schema_version, updated_at) VALUES ('sqlite', 22, '2026-09-20T01:02:03Z'), ('lancedb', 3, '2026-09-20T04:05:06Z')`); err != nil {
		source.Close()
		t.Fatalf("insert source schema versions: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err != nil {
		t.Fatalf("copy native snapshot: %v", err)
	}
	if report.SourceSchemaVersion != nativeSQLiteSchemaVersion {
		t.Fatalf("source schema version = %d, want %d", report.SourceSchemaVersion, nativeSQLiteSchemaVersion)
	}
	if len(report.Tables) != len(nativeCopyBusinessTables)+1 {
		t.Fatalf("report table count = %d, want %d", len(report.Tables), len(nativeCopyBusinessTables)+1)
	}
	if report.TotalRows != 4 {
		t.Fatalf("report total rows = %d, want 4", report.TotalRows)
	}
	for _, tableReport := range report.Tables {
		if len(tableReport.SHA256) != 64 {
			t.Fatalf("table %s hash length = %d, want 64", tableReport.Name, len(tableReport.SHA256))
		}
	}

	target := openNativeCopyTestDatabase(t, targetPath)
	defer target.Close()
	var id int64
	var payload []byte
	if err := target.QueryRow(`SELECT id, payload FROM vmm_memory_nodes WHERE id = ?`, int64(9007199254740993)).Scan(&id, &payload); err != nil {
		t.Fatalf("read large integer row: %v", err)
	}
	if id != int64(9007199254740993) || string(payload) != string([]byte{0, 1, 255}) {
		t.Fatalf("typed row = id %d payload %v", id, payload)
	}
	var nullPayload any
	if err := target.QueryRow(`SELECT payload FROM vmm_memory_nodes WHERE id = 2`).Scan(&nullPayload); err != nil {
		t.Fatalf("read NULL payload row: %v", err)
	}
	if nullPayload != nil {
		t.Fatalf("NULL payload became %#v", nullPayload)
	}
	var updatedAt string
	if err := target.QueryRow(`SELECT updated_at FROM vmm_schema_versions WHERE component = 'lancedb'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read lancedb schema version: %v", err)
	}
	if updatedAt != "2026-09-20T04:05:06Z" {
		t.Fatalf("lancedb updated_at = %q", updatedAt)
	}
}

// TestCopyNativeSnapshotRejectsUnknownLegacyTable verifies the old-source table allowlist fails before target mutation.
// TestCopyNativeSnapshotRejectsUnknownLegacyTable 验证旧库未知表会在修改目标前被拒绝。
func TestCopyNativeSnapshotRejectsUnknownLegacyTable(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	source := openNativeCopyTestDatabase(t, sourcePath)
	if _, err := source.Exec(`CREATE TABLE unexpected_user_table (id INTEGER PRIMARY KEY)`); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO vmm_schema_versions (component, schema_version, updated_at) VALUES ('sqlite', 22, '')`); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err == nil || !strings.Contains(err.Error(), "unknown user table") {
		t.Fatalf("unknown table error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 0)
}

// TestCopyNativeSnapshotRejectsSchemaMismatch verifies declared type changes are detected before the transaction starts.
// TestCopyNativeSnapshotRejectsSchemaMismatch 验证声明类型变化会在事务开始前被检测。
func TestCopyNativeSnapshotRejectsSchemaMismatch(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "TEXT", true)
	seedNativeCopySchemaVersion(t, sourcePath)

	_, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err == nil || !strings.Contains(err.Error(), "schema mismatch for vmm_memory_nodes") {
		t.Fatalf("schema mismatch error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 0)
}

// TestCopyNativeSnapshotRejectsUnsupportedLegacySchemaVersion verifies older snapshots are never implicitly upgraded.
// TestCopyNativeSnapshotRejectsUnsupportedLegacySchemaVersion 验证旧版本快照不会被隐式升级。
func TestCopyNativeSnapshotRejectsUnsupportedLegacySchemaVersion(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	source := openNativeCopyTestDatabase(t, sourcePath)
	if _, err := source.Exec(`INSERT INTO vmm_schema_versions (component, schema_version, updated_at) VALUES ('sqlite', 21, '')`); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err == nil || !strings.Contains(err.Error(), "required 22") {
		t.Fatalf("unsupported schema version error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 0)
}

// TestCopyNativeSnapshotRejectsNonEmptyTarget verifies migration never overwrites existing native business rows.
// TestCopyNativeSnapshotRejectsNonEmptyTarget 验证迁移不会覆盖已有原生业务行。
func TestCopyNativeSnapshotRejectsNonEmptyTarget(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	seedNativeCopySchemaVersion(t, sourcePath)
	target := openNativeCopyTestDatabase(t, targetPath)
	if _, err := target.Exec(`INSERT INTO vmm_memory_nodes (id, payload) VALUES (7, 'existing')`); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("non-empty target error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 1)
}

// TestCopyNativeSnapshotRollsBackAfterTargetWriteFailure verifies a failed second insert cannot leave the first row committed.
// TestCopyNativeSnapshotRollsBackAfterTargetWriteFailure 验证第二行写入失败时第一行也不会提交。
func TestCopyNativeSnapshotRollsBackAfterTargetWriteFailure(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	seedNativeCopySchemaVersion(t, sourcePath)
	source := openNativeCopyTestDatabase(t, sourcePath)
	if _, err := source.Exec(`INSERT INTO vmm_memory_nodes (id, payload) VALUES (1, 'first'), (2, 'second')`); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target := openNativeCopyTestDatabase(t, targetPath)
	if _, err := target.Exec(`CREATE TRIGGER fail_native_copy AFTER INSERT ON vmm_memory_nodes WHEN NEW.id = 2 BEGIN SELECT RAISE(ABORT, 'intentional copy failure'); END`); err != nil {
		target.Close()
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err == nil || !strings.Contains(err.Error(), "copy table vmm_memory_nodes") {
		t.Fatalf("rollback error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 0)
	assertNativeCopyTargetRows(t, targetPath, "vmm_schema_versions", 1)
}

// TestCopyNativeSnapshotHonorsCanceledContext verifies cancellation prevents even the target connection from being opened for writes.
// TestCopyNativeSnapshotHonorsCanceledContext 验证取消上下文时不会对目标执行写入。
func TestCopyNativeSnapshotHonorsCanceledContext(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "legacy.db")
	targetPath := filepath.Join(t.TempDir(), "native.db")
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	seedNativeCopySchemaVersion(t, sourcePath)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CopyNativeSnapshot(ctx, sourcePath, targetPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy error = %v", err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_memory_nodes", 0)
}

// TestCopyNativeSnapshotEscapesSpecialFilesystemCharacters verifies URI escaping and immutable read-only behavior on real special-character paths.
// TestCopyNativeSnapshotEscapesSpecialFilesystemCharacters 验证真实特殊字符路径的 URI 转义和 immutable 只读行为。
func TestCopyNativeSnapshotEscapesSpecialFilesystemCharacters(t *testing.T) {
	pathRoot := filepath.Join(t.TempDir(), "目录 空格")
	if err := os.MkdirAll(pathRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceName := "快照#百分号%空格.db"
	targetName := "原生#百分号%空格.db"
	if runtime.GOOS != "windows" {
		sourceName = "快照#百分号%问号?.db"
		targetName = "原生#百分号%问号?.db"
	}
	sourcePath := filepath.Join(pathRoot, sourceName)
	targetPath := filepath.Join(pathRoot, targetName)
	createNativeCopyTestDatabase(t, sourcePath, "BLOB", false)
	createNativeCopyTestDatabase(t, targetPath, "BLOB", true)
	seedNativeCopySchemaVersion(t, sourcePath)

	truncatedSourcePath := strings.Split(sourcePath, "#")[0]
	if _, err := os.Stat(truncatedSourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected pre-existing truncated source path %q: %v", truncatedSourcePath, err)
	}
	immutableDSN := immutableSQLiteDSN(sourcePath)
	if strings.Contains(immutableDSN, "#") || strings.Contains(immutableDSN, "百分号") || strings.Contains(immutableDSN, " ") {
		t.Fatalf("immutable SQLite DSN is not URI escaped: %q", immutableDSN)
	}
	if !strings.Contains(immutableDSN, "%23") || !strings.Contains(immutableDSN, "%25") {
		t.Fatalf("immutable SQLite DSN missed #/%% escaping: %q", immutableDSN)
	}
	if runtime.GOOS != "windows" && !strings.Contains(immutableDSN, "%3F") {
		t.Fatalf("immutable SQLite DSN missed ? escaping: %q", immutableDSN)
	}
	if runtime.GOOS == "windows" && !strings.HasPrefix(immutableDSN, "file:///") {
		t.Fatalf("Windows immutable SQLite DSN is not an absolute file URI: %q", immutableDSN)
	}
	report, err := CopyNativeSnapshot(context.Background(), sourcePath, targetPath)
	if err != nil {
		t.Fatalf("copy special-character snapshot: %v", err)
	}
	if report.TotalRows != 1 {
		t.Fatalf("special-character copy total rows = %d, want 1", report.TotalRows)
	}
	if _, err := os.Stat(truncatedSourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy created a truncated source path %q: %v", truncatedSourcePath, err)
	}
	assertNativeCopyTargetRows(t, targetPath, "vmm_schema_versions", 1)

	immutable, err := sql.Open("sqlite", immutableDSN)
	if err != nil {
		t.Fatalf("open escaped immutable source: %v", err)
	}
	defer immutable.Close()
	if err := immutable.Ping(); err != nil {
		t.Fatalf("ping escaped immutable source: %v", err)
	}
	if _, err := immutable.Exec(`CREATE TABLE immutable_write_probe (id INTEGER PRIMARY KEY)`); err == nil {
		t.Fatal("immutable source unexpectedly accepted a write")
	}
}

// createNativeCopyTestDatabase creates a compact but complete 25-table schema matching source and target contracts.
// createNativeCopyTestDatabase 创建符合源目标契约的精简完整 25 表测试 schema。
func createNativeCopyTestDatabase(t *testing.T, path, memoryPayloadType string, target bool) {
	t.Helper()
	db := openNativeCopyTestDatabase(t, path)
	defer db.Close()
	for _, tableName := range nativeCopyBusinessTables {
		payloadType := "BLOB"
		if tableName == "vmm_memory_nodes" {
			payloadType = memoryPayloadType
		}
		statement := fmt.Sprintf(`CREATE TABLE "%s" (id INTEGER PRIMARY KEY, payload %s)`, tableName, payloadType)
		if target && tableName == "vmm_memory_nodes" {
			statement = fmt.Sprintf(`CREATE TABLE "%s" (payload %s, id INTEGER PRIMARY KEY)`, tableName, payloadType)
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create test table %s: %v", tableName, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE vmm_schema_versions (component TEXT PRIMARY KEY, schema_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if !target {
		if _, err := db.Exec(`CREATE TABLE _vulcan_dict (word TEXT PRIMARY KEY, weight INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO _vulcan_dict (word, weight, enabled, created_at, updated_at) VALUES ('中文', 3, 1, '', '')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE vmm_memory_nodes_fts (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
			t.Fatal(err)
		}
		for _, shadow := range []string{"_config", "_content", "_data", "_docsize", "_idx"} {
			if _, err := db.Exec(`CREATE TABLE "vmm_memory_nodes_fts` + shadow + `" (id INTEGER PRIMARY KEY)`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if target {
		if _, err := db.Exec(`CREATE TABLE vmm_native_storage_marker (id INTEGER PRIMARY KEY, format_version INTEGER NOT NULL, backend TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO vmm_native_storage_marker (id, format_version, backend, created_at, updated_at) VALUES (1, 1, 'modernc.org/sqlite', '', '')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO vmm_schema_versions (component, schema_version, updated_at) VALUES ('sqlite', 22, '')`); err != nil {
			t.Fatal(err)
		}
	}
}

// openNativeCopyTestDatabase opens one temporary SQLite file with a single connection for deterministic tests.
// openNativeCopyTestDatabase 使用单连接打开一个临时 SQLite 文件，保证测试确定性。
func openNativeCopyTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test sqlite %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping test sqlite %s: %v", path, err)
	}
	return db
}

// seedNativeCopySchemaVersion inserts the required source schema version for tests that do not need extra timestamps.
// seedNativeCopySchemaVersion 为只需要必需版本的测试插入源 schema 版本。
func seedNativeCopySchemaVersion(t *testing.T, path string) {
	t.Helper()
	db := openNativeCopyTestDatabase(t, path)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO vmm_schema_versions (component, schema_version, updated_at) VALUES ('sqlite', 22, '')`); err != nil {
		t.Fatal(err)
	}
}

// assertNativeCopyTargetRows checks one target count after a failed or rejected migration.
// assertNativeCopyTargetRows 在迁移失败或拒绝后检查目标表行数。
func assertNativeCopyTargetRows(t *testing.T, path, tableName string, expected int64) {
	t.Helper()
	db := openNativeCopyTestDatabase(t, path)
	defer db.Close()
	var actual int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM "` + tableName + `"`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("target %s row count = %d, want %d", tableName, actual, expected)
	}
}
