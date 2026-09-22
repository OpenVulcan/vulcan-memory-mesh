// These integration tests pin the legacy connection semantics used by offline migration.
// 本文件通过真实旧后端验证离线迁移依赖的连接与事务语义。
package sqliteffi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"
)

// TestSQLiteSnapshotSemantics characterizes legacy script boundaries before selecting a snapshot operation.
// TestSQLiteSnapshotSemantics 在选择快照操作前验证旧后端脚本边界的实际语义。
func TestSQLiteSnapshotSemantics(t *testing.T) {
	libraryName := "libvldb_sqlite.so"
	if runtime.GOOS == "windows" {
		libraryName = "vldb_sqlite.dll"
	} else if runtime.GOOS == "darwin" {
		libraryName = "libvldb_sqlite.dylib"
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "third_party", "deps", libraryName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("legacy SQLite library is not installed")
	}
	library, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer library.Close()
	owner, err := library.CreateRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	database, err := owner.OpenDatabase(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := database.EnsureFtsIndex(ctx, "snapshot_search", TokenizerJieba); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"CREATE TABLE snapshot_probe(id INTEGER PRIMARY KEY); INSERT INTO snapshot_probe VALUES (1);", "BEGIN IMMEDIATE", "INSERT INTO snapshot_probe VALUES (2)"} {
		if _, err := database.ExecuteScript(ctx, sql, nil, ""); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	inside, err := database.QueryJSON(ctx, "SELECT count(*) AS count FROM snapshot_probe", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecuteScript(ctx, "ROLLBACK", nil, ""); err == nil {
		t.Fatal("legacy scripts unexpectedly retained a cross-call transaction")
	}
	after, err := database.QueryJSON(ctx, "SELECT count(*) AS count FROM snapshot_probe", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var insideRows, afterRows []struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(inside.JSONData), &insideRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after.JSONData), &afterRows); err != nil {
		t.Fatal(err)
	}
	if len(insideRows) != 1 || insideRows[0].Count != 2 || len(afterRows) != 1 || afterRows[0].Count != 2 {
		t.Fatalf("unexpected legacy script boundaries: inside=%s after=%s", inside.JSONData, after.JSONData)
	}
	backup := filepath.Join(t.TempDir(), "snapshot.db")
	if _, err := database.ExecuteScript(ctx, "VACUUM INTO ?", []SQLValue{{Kind: SQLValueString, String: backup}}, ""); err != nil {
		t.Fatalf("consistent backup operation: %v", err)
	}
	// Read only ordinary tables from the immutable snapshot; never connect to its legacy FTS table.
	// 仅从不可变快照读取普通表，不访问其中的旧版 FTS 虚拟表。
	backupPath := filepath.ToSlash(backup)
	if filepath.VolumeName(backup) != "" {
		backupPath = "/" + backupPath
	}
	backupURI := url.URL{Scheme: "file", Path: backupPath, RawQuery: "mode=ro&immutable=1"}
	copyDB, err := sql.Open("sqlite", backupURI.String())
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var count int
	if err := copyDB.QueryRow("SELECT count(*) FROM snapshot_probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("snapshot count = %d", count)
	}
}
