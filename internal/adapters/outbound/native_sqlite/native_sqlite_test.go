// native_sqlite_test.go verifies native SQL semantics, ownership gating, and recoverable FTS projections on temporary databases.
// native_sqlite_test.go 在临时数据库上验证原生 SQL 语义、所有权门禁及可恢复 FTS 派生索引。
package native_sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	storagecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
)

const nativeTestMemorySchema = `
CREATE TABLE vmm_memory_nodes (
  id INTEGER PRIMARY KEY,
  vector_id TEXT NOT NULL,
  abstract TEXT NOT NULL,
  details TEXT NOT NULL,
  updated_timestamp INTEGER NOT NULL
);`

// TestNativeDatabaseRejectsExistingNonNativeDatabase proves the compatibility gate runs before native DDL or PRAGMA ownership changes.
// TestNativeDatabaseRejectsExistingNonNativeDatabase 验证兼容门禁在原生 DDL 或 PRAGMA 接管前拒绝既有非原生数据库。
func TestNativeDatabaseRejectsExistingNonNativeDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy sqlite database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy_business_data (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite database: %v", err)
	}

	if _, err := Open(databasePath, time.Second); err == nil || !strings.Contains(err.Error(), "existing non-native database") {
		t.Fatalf("expected existing non-native database rejection, got %v", err)
	}
}

// TestNativeDatabaseRejectsSQLiteLikeUserTable verifies that sqliteXdata is not hidden by a LIKE wildcard.
// TestNativeDatabaseRejectsSQLiteLikeUserTable 验证 sqliteXdata 不会被 LIKE 通配符错误隐藏。
func TestNativeDatabaseRejectsSQLiteLikeUserTable(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy#% 中文-sqlite-prefix.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy sqlite database: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE sqliteXdata (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create sqlite-like table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite database: %v", err)
	}
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read legacy sqlite database before probe: %v", err)
	}
	if _, err := Open(databasePath, time.Second); err == nil || !strings.Contains(err.Error(), "existing non-native database") {
		t.Fatalf("expected sqlite-like existing database rejection, got %v", err)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatalf("read legacy sqlite database after probe: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only ownership probe changed the legacy database")
	}
	check, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("reopen legacy sqlite database: %v", err)
	}
	defer check.Close()
	var markerCount int
	if err := check.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'vmm_native_storage_marker'").Scan(&markerCount); err != nil {
		t.Fatalf("inspect legacy marker: %v", err)
	}
	if markerCount != 0 {
		t.Fatalf("native marker was created in rejected database: %d", markerCount)
	}
}

// TestSQLiteReadOnlyDSNEncodesSpecialPath verifies path characters cannot truncate the mode query.
// TestSQLiteReadOnlyDSNEncodesSpecialPath 验证路径字符不会截断只读模式查询参数。
func TestSQLiteReadOnlyDSNEncodesSpecialPath(t *testing.T) {
	path := `C:\probe\legacy#question?percent% space中文.db`
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		t.Fatalf("build read-only DSN: %v", err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse read-only DSN: %v", err)
	}
	if parsed.Scheme != "file" || parsed.Query().Get("mode") != "ro" {
		t.Fatalf("unexpected read-only DSN: %q", dsn)
	}
	for _, encoded := range []string{"%23", "%3F", "%25", "%20"} {
		if !strings.Contains(strings.ToUpper(dsn), encoded) {
			t.Fatalf("DSN %q does not encode %q", dsn, encoded)
		}
	}
	if !strings.Contains(strings.ToUpper(dsn), "%E4") {
		t.Fatalf("DSN %q does not percent-encode Unicode path bytes", dsn)
	}
}

// TestNativeSQLSemanticsPreserveIntegerAndBatchRollback checks typed values, JSON row mapping, and all-or-nothing batches.
// TestNativeSQLSemanticsPreserveIntegerAndBatchRollback 验证类型化参数、JSON 行映射及批处理的全有或全无事务语义。
func TestNativeSQLSemanticsPreserveIntegerAndBatchRollback(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "native.db")
	database, err := Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()

	if _, err := database.ExecuteScript(ctx, `CREATE TABLE values_probe (id INTEGER PRIMARY KEY, label TEXT NOT NULL, payload BLOB);`, nil, ""); err != nil {
		t.Fatalf("create values probe: %v", err)
	}
	items := [][]storagecontract.SQLValue{
		{{Kind: storagecontract.SQLValueInt64, Int64: 1}, {Kind: storagecontract.SQLValueString, String: "中文"}, {Kind: storagecontract.SQLValueBytes, Bytes: []byte{1, 2, 3}}},
		{{Kind: storagecontract.SQLValueInt64, Int64: 2}, {Kind: storagecontract.SQLValueString, String: "第二行"}, {Kind: storagecontract.SQLValueNull}},
	}
	if result, err := database.ExecuteBatch(ctx, "INSERT INTO values_probe (id, label, payload) VALUES (?, ?, ?)", items); err != nil || !result.Success || result.StatementsExecuted != 2 {
		t.Fatalf("execute values batch: result=%+v err=%v", result, err)
	}
	query, err := database.QueryJSON(ctx, "SELECT id, label, payload FROM values_probe ORDER BY id", nil, "")
	if err != nil {
		t.Fatalf("query values probe: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(query.JSONData), &rows); err != nil {
		t.Fatalf("decode values probe JSON: %v", err)
	}
	if len(rows) != 2 || rows[0]["id"] != float64(1) || rows[0]["label"] != "中文" {
		t.Fatalf("unexpected values probe rows: %s", query.JSONData)
	}
	if payload, ok := rows[0]["payload"].([]any); !ok || len(payload) != 3 {
		t.Fatalf("expected blob byte array in JSON, got %#v", rows[0]["payload"])
	}

	failedBatch := [][]storagecontract.SQLValue{
		{{Kind: storagecontract.SQLValueInt64, Int64: 3}, {Kind: storagecontract.SQLValueString, String: "暂存"}, {Kind: storagecontract.SQLValueNull}},
		{{Kind: storagecontract.SQLValueInt64, Int64: 1}, {Kind: storagecontract.SQLValueString, String: "冲突"}, {Kind: storagecontract.SQLValueNull}},
	}
	if _, err := database.ExecuteBatch(ctx, "INSERT INTO values_probe (id, label, payload) VALUES (?, ?, ?)", failedBatch); err == nil {
		t.Fatal("expected duplicate-key batch failure")
	}
	count, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM values_probe", nil, "")
	if err != nil {
		t.Fatalf("query values probe count: %v", err)
	}
	if count.JSONData != `[{"count":2}]` {
		t.Fatalf("batch rollback changed row count: %s", count.JSONData)
	}
}

// TestNativeExecuteScriptDoesNotLeakExplicitTransaction verifies cleanup after an unclosed or already-cancelled script.
// TestNativeExecuteScriptDoesNotLeakExplicitTransaction 验证未闭合事务脚本及已取消脚本都不会污染后续连接。
func TestNativeExecuteScriptDoesNotLeakExplicitTransaction(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "native-transaction.db")
	database, err := Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, `CREATE TABLE transaction_probe (id INTEGER PRIMARY KEY, label TEXT NOT NULL);`, nil, ""); err != nil {
		t.Fatalf("create transaction probe: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `BEGIN; INSERT INTO transaction_probe (id, label) VALUES (1, 'dangling');`, nil, ""); err != nil {
		t.Fatalf("execute unclosed transaction script: %v", err)
	}
	count, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM transaction_probe", nil, "")
	if err != nil {
		t.Fatalf("query transaction probe after cleanup: %v", err)
	}
	if count.JSONData != `[{"count":0}]` {
		t.Fatalf("unclosed transaction leaked rows: %s", count.JSONData)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.ExecuteScript(cancelled, `BEGIN; INSERT INTO transaction_probe (id, label) VALUES (2, 'cancelled');`, nil, ""); err == nil {
		t.Fatal("expected cancelled execute script failure")
	}
	if _, err := database.ExecuteBatch(ctx, "INSERT INTO transaction_probe (id, label) VALUES (?, ?)", [][]storagecontract.SQLValue{{
		{Kind: storagecontract.SQLValueInt64, Int64: 3},
		{Kind: storagecontract.SQLValueString, String: "next operation"},
	}}); err != nil {
		t.Fatalf("execute operation after cancellation: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `CREATE TABLE transaction_log (id INTEGER NOT NULL);
CREATE TRIGGER transaction_probe_log AFTER INSERT ON transaction_probe
BEGIN
  INSERT INTO transaction_log (id) VALUES (NEW.id);
END;`, nil, ""); err != nil {
		t.Fatalf("create trigger with BEGIN/END: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO transaction_probe (id, label) VALUES (4, 'trigger');`, nil, ""); err != nil {
		t.Fatalf("execute trigger source insert: %v", err)
	}
	triggerCount, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM transaction_log", nil, "")
	if err != nil || triggerCount.JSONData != `[{"count":1}]` {
		t.Fatalf("trigger BEGIN/END transaction cleanup failed: result=%s err=%v", triggerCount.JSONData, err)
	}
	if _, err := database.ExecuteScript(ctx, `BEGIN;
INSERT INTO transaction_probe (id, label) VALUES (5, 'committed');
COMMIT;
BEGIN;
INSERT INTO transaction_probe (id, label) VALUES (6, 'dangling after commit');`, nil, ""); err != nil {
		t.Fatalf("execute committed and dangling transaction script: %v", err)
	}
	if _, err := database.ExecuteBatch(ctx, "INSERT INTO transaction_probe (id, label) VALUES (?, ?)", [][]storagecontract.SQLValue{{
		{Kind: storagecontract.SQLValueInt64, Int64: 7},
		{Kind: storagecontract.SQLValueString, String: "after cleanup"},
	}}); err != nil {
		t.Fatalf("execute operation after committed dangling transaction: %v", err)
	}
	var danglingCount storagecontract.QueryJSONResult
	danglingCount, err = database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM transaction_probe WHERE id = 6", nil, "")
	if err != nil || danglingCount.JSONData != `[{"count":0}]` {
		t.Fatalf("dangling transaction row survived cleanup: result=%s err=%v", danglingCount.JSONData, err)
	}
}

// TestNativeFTSQueueRecoveryPreservesChineseSource exercises a crash-shaped reopen between source commit and derived-index recovery.
// TestNativeFTSQueueRecoveryPreservesChineseSource 模拟源表提交后、派生索引恢复前重开数据库，并验证中文检索及原文保持不变。
func TestNativeFTSQueueRecoveryPreservesChineseSource(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "native-fts.db")
	database, err := Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		_ = database.Close()
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, "memory", storagecontract.TokenizerGSE); err != nil {
		_ = database.Close()
		t.Fatalf("ensure native fts index: %v", err)
	}
	originalAbstract := "数据库崩溃恢复验证"
	originalDetails := "原文保留，GSE 只生成派生索引"
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)`, []storagecontract.SQLValue{
		{Kind: storagecontract.SQLValueInt64, Int64: 42},
		{Kind: storagecontract.SQLValueString, String: "vec-42"},
		{Kind: storagecontract.SQLValueString, String: originalAbstract},
		{Kind: storagecontract.SQLValueString, String: originalDetails},
		{Kind: storagecontract.SQLValueInt64, Int64: 100},
	}, ""); err != nil {
		_ = database.Close()
		t.Fatalf("insert memory source row: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close source database before recovery: %v", err)
	}

	database, err = Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("reopen native sqlite database: %v", err)
	}
	defer database.Close()
	search, err := database.SearchFts(ctx, "memory", storagecontract.TokenizerGSE, "崩溃 恢复", 20, 0)
	if err != nil {
		t.Fatalf("recover and search native fts: %v", err)
	}
	if search.Total != 1 || len(search.Hits) != 1 || search.Hits[0].ID != "42" {
		t.Fatalf("unexpected recovered search result: %+v", search)
	}
	rows, err := database.QueryJSON(ctx, "SELECT abstract, details FROM vmm_memory_nodes WHERE id = 42", nil, "")
	if err != nil {
		t.Fatalf("query original memory source row: %v", err)
	}
	if !strings.Contains(rows.JSONData, originalAbstract) || !strings.Contains(rows.JSONData, originalDetails) {
		t.Fatalf("native fts changed original source text: %s", rows.JSONData)
	}
	if err := database.CheckHealth(ctx); err != nil {
		t.Fatalf("native health check after recovery: %v", err)
	}

	if _, err := database.ExecuteScript(ctx, `DELETE FROM vmm_memory_nodes WHERE id = ?`, []storagecontract.SQLValue{{Kind: storagecontract.SQLValueInt64, Int64: 42}}, ""); err != nil {
		t.Fatalf("delete source memory row: %v", err)
	}
	search, err = database.SearchFts(ctx, "memory", storagecontract.TokenizerGSE, "崩溃", 20, 0)
	if err != nil {
		t.Fatalf("recover delete queue and search native fts: %v", err)
	}
	if search.Total != 0 || len(search.Hits) != 0 {
		t.Fatalf("deleted source row remained in native fts: %+v", search)
	}
}

// TestNativeFTSTokenizerMetadataSwitchRebuildsDerivedGeneration verifies a mode change rebuilds only derived text and preserves source content.
// TestNativeFTSTokenizerMetadataSwitchRebuildsDerivedGeneration 验证分词模式变化只重建派生文本，同时保持源文内容不变。
func TestNativeFTSTokenizerMetadataSwitchRebuildsDerivedGeneration(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "native-tokenizer-switch.db")
	database, err := Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("create memory source table: %v", err)
	}
	longText := strings.Repeat("中文索引文本与 native storage ", 512)
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)`, []storagecontract.SQLValue{
		{Kind: storagecontract.SQLValueInt64, Int64: 7},
		{Kind: storagecontract.SQLValueString, String: "vec-7"},
		{Kind: storagecontract.SQLValueString, String: "固定中文语料"},
		{Kind: storagecontract.SQLValueString, String: longText},
		{Kind: storagecontract.SQLValueInt64, Int64: 7},
	}, ""); err != nil {
		t.Fatalf("insert long memory source row: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, "memory", storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("ensure GSE index: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, "memory", storagecontract.TokenizerNone); err != nil {
		t.Fatalf("switch to unicode61 index: %v", err)
	}
	metadata, err := database.QueryJSON(ctx, `SELECT tokenizer_mode, tokenizer_algorithm FROM vmm_native_fts_metadata WHERE logical_name = 'memory'`, nil, "")
	if err != nil {
		t.Fatalf("query tokenizer metadata: %v", err)
	}
	if !strings.Contains(metadata.JSONData, `"tokenizer_mode":0`) || !strings.Contains(metadata.JSONData, "unicode61-v1") {
		t.Fatalf("unexpected tokenizer metadata after rebuild: %s", metadata.JSONData)
	}
	source, err := database.QueryJSON(ctx, "SELECT abstract, details FROM vmm_memory_nodes WHERE id = 7", nil, "")
	if err != nil {
		t.Fatalf("query source after tokenizer rebuild: %v", err)
	}
	if !strings.Contains(source.JSONData, "固定中文语料") || !strings.Contains(source.JSONData, longText) {
		t.Fatal("tokenizer rebuild changed source text")
	}
}

// TestNativeFTSRebuildClearsPendingQueue verifies successful full rebuilds publish no stale recovery work.
// TestNativeFTSRebuildClearsPendingQueue 验证成功的全量重建不会留下过期恢复任务。
func TestNativeFTSRebuildClearsPendingQueue(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "native-fts-rebuild.db"), time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("ensure native fts index: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)`, []storagecontract.SQLValue{
		{Kind: storagecontract.SQLValueInt64, Int64: 1},
		{Kind: storagecontract.SQLValueString, String: "vec-1"},
		{Kind: storagecontract.SQLValueString, String: "重建排队"},
		{Kind: storagecontract.SQLValueString, String: "等待全量重建"},
		{Kind: storagecontract.SQLValueInt64, Int64: 1},
	}, ""); err != nil {
		t.Fatalf("insert queued memory source row: %v", err)
	}
	queue, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM vmm_native_fts_sync_queue", nil, "")
	if err != nil || queue.JSONData != `[{"count":1}]` {
		t.Fatalf("expected one pending queue row: result=%s err=%v", queue.JSONData, err)
	}
	if _, err := database.RebuildFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("rebuild native fts index: %v", err)
	}
	queue, err = database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM vmm_native_fts_sync_queue", nil, "")
	if err != nil || queue.JSONData != `[{"count":0}]` {
		t.Fatalf("successful rebuild left pending queue rows: result=%s err=%v", queue.JSONData, err)
	}
}

// TestNativeFTSQueueRecoveryProcessesBatches verifies recovery remains bounded for more than one queue batch.
// TestNativeFTSQueueRecoveryProcessesBatches 验证超过单批大小的队列恢复仍保持有界。
func TestNativeFTSQueueRecoveryProcessesBatches(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "native-fts-queue-batches.db")
	database, err := Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		_ = database.Close()
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		_ = database.Close()
		t.Fatalf("ensure native fts index: %v", err)
	}
	items := make([][]storagecontract.SQLValue, 0, nativeFTSQueueBatchSize+44)
	for index := 1; index <= nativeFTSQueueBatchSize+44; index++ {
		items = append(items, []storagecontract.SQLValue{
			{Kind: storagecontract.SQLValueInt64, Int64: int64(index)},
			{Kind: storagecontract.SQLValueString, String: "batch-vec"},
			{Kind: storagecontract.SQLValueString, String: "批次事实"},
			{Kind: storagecontract.SQLValueString, String: "批次内容" + strconv.Itoa(index)},
			{Kind: storagecontract.SQLValueInt64, Int64: int64(index)},
		})
	}
	if _, err := database.ExecuteBatch(ctx, "INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)", items); err != nil {
		_ = database.Close()
		t.Fatalf("insert queued batch: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close before batched recovery: %v", err)
	}
	database, err = Open(databasePath, time.Second)
	if err != nil {
		t.Fatalf("reopen native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recover queued fts batch: %v", err)
	}
	metadata, err := database.QueryJSON(ctx, "SELECT active_table FROM vmm_native_fts_metadata WHERE logical_name = ?", []storagecontract.SQLValue{{Kind: storagecontract.SQLValueString, String: nativeFTSDefaultLogicalIndex}}, "")
	if err != nil {
		t.Fatalf("read recovered fts metadata: %v", err)
	}
	var metadataRows []struct {
		ActiveTable string `json:"active_table"`
	}
	if err := json.Unmarshal([]byte(metadata.JSONData), &metadataRows); err != nil || len(metadataRows) != 1 {
		t.Fatalf("decode recovered fts metadata: json=%s err=%v", metadata.JSONData, err)
	}
	indexed, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM "+quoteIdentifier(metadataRows[0].ActiveTable), nil, "")
	if err != nil || indexed.JSONData != `[{"count":300}]` {
		t.Fatalf("batched recovery missed source rows: result=%s err=%v", indexed.JSONData, err)
	}
	queue, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM vmm_native_fts_sync_queue", nil, "")
	if err != nil || queue.JSONData != `[{"count":0}]` {
		t.Fatalf("batched recovery left queue rows: result=%s err=%v", queue.JSONData, err)
	}
}

// TestNativeFTSRebuildCancellationPreservesPublishedGeneration verifies cancellation cannot publish a partial rebuild.
// TestNativeFTSRebuildCancellationPreservesPublishedGeneration 验证取消不会发布部分重建代次。
func TestNativeFTSRebuildCancellationPreservesPublishedGeneration(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "native-fts-rebuild-cancel.db"), time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("ensure native fts index: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (1, 'vec-cancel', '取消回滚保留', '已发布内容', 1);`, nil, ""); err != nil {
		t.Fatalf("insert published memory source row: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recover published fts row: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := database.RebuildFtsIndex(cancelled, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err == nil {
		t.Fatal("expected cancelled native fts rebuild to fail")
	}
	result, err := database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "取消回滚保留", 20, 0)
	if err != nil || result.Total != 1 {
		t.Fatalf("cancelled rebuild damaged published generation: result=%+v err=%v", result, err)
	}
	queue, err := database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM vmm_native_fts_sync_queue", nil, "")
	if err != nil || queue.JSONData != `[{"count":0}]` {
		t.Fatalf("cancelled rebuild changed sync queue: result=%s err=%v", queue.JSONData, err)
	}
}

// TestNativeFTSStaleMutationsUseCurrentFact verifies old delete and upsert calls cannot erase or recreate current facts.
// TestNativeFTSStaleMutationsUseCurrentFact 验证旧删除和写入调用不会误删或重建当前事实。
func TestNativeFTSStaleMutationsUseCurrentFact(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "native-fts-stale.db"), time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("ensure native fts index: %v", err)
	}
	insertMemory := func(abstract, details string, revision int64) {
		t.Helper()
		if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)`, []storagecontract.SQLValue{
			{Kind: storagecontract.SQLValueInt64, Int64: 9},
			{Kind: storagecontract.SQLValueString, String: "vec-9"},
			{Kind: storagecontract.SQLValueString, String: abstract},
			{Kind: storagecontract.SQLValueString, String: details},
			{Kind: storagecontract.SQLValueInt64, Int64: revision},
		}, ""); err != nil {
			t.Fatalf("insert current memory source row: %v", err)
		}
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (?, ?, ?, ?, ?)`, []storagecontract.SQLValue{
		{Kind: storagecontract.SQLValueInt64, Int64: 9},
		{Kind: storagecontract.SQLValueString, String: "vec-9"},
		{Kind: storagecontract.SQLValueString, String: "旧事实"},
		{Kind: storagecontract.SQLValueString, String: "旧内容"},
		{Kind: storagecontract.SQLValueInt64, Int64: 1},
	}, ""); err != nil {
		t.Fatalf("insert initial memory source row: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recover initial fts row: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `DELETE FROM vmm_memory_nodes WHERE id = 9`, nil, ""); err != nil {
		t.Fatalf("delete old memory source row: %v", err)
	}
	insertMemory("当前事实", "当前内容", 2)
	if _, err := database.DeleteFtsDocument(ctx, nativeFTSDefaultLogicalIndex, "9"); err != nil {
		t.Fatalf("apply stale delete request: %v", err)
	}
	current, err := database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "当前事实", 20, 0)
	if err != nil || current.Total != 1 {
		t.Fatalf("stale delete removed current fact from FTS: result=%+v err=%v", current, err)
	}
	if _, err := database.ExecuteScript(ctx, `DELETE FROM vmm_memory_nodes WHERE id = 9`, nil, ""); err != nil {
		t.Fatalf("delete current memory source row: %v", err)
	}
	if _, err := database.UpsertFtsDocument(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "9", "stale-path", "旧标题", "旧内容"); err != nil {
		t.Fatalf("apply stale upsert request: %v", err)
	}
	removed, err := database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "当前事实", 20, 0)
	if err != nil || removed.Total != 0 {
		t.Fatalf("stale upsert recreated deleted fact in FTS: result=%+v err=%v", removed, err)
	}
}

// TestNativeResetFTSAndExecuteDropsDerivedState verifies debug cleanup cannot reuse stale generations after ID reuse.
// TestNativeResetFTSAndExecuteDropsDerivedState 验证调试清理后复用 ID 不会继承旧代次词条。
func TestNativeResetFTSAndExecuteDropsDerivedState(t *testing.T) {
	ctx := context.Background()
	database, err := Open(filepath.Join(t.TempDir(), "native-reset-fts.db"), time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	defer database.Close()
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("create memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("ensure native fts index: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (1, 'vec-old', 'legacyOldNeedleXYZ', '旧内容', 1);`, nil, ""); err != nil {
		t.Fatalf("insert old memory source row: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recover old fts row: %v", err)
	}
	old, err := database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "legacyOldNeedleXYZ", 20, 0)
	if err != nil || old.Total == 0 {
		t.Fatalf("old fts fixture was not indexed: result=%+v err=%v", old, err)
	}
	if err := database.ResetFTSAndExecute(ctx, `BEGIN IMMEDIATE;
DROP TABLE IF EXISTS vmm_memory_nodes;
COMMIT;`); err != nil {
		t.Fatalf("reset native fts and business tables: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, nativeTestMemorySchema, nil, ""); err != nil {
		t.Fatalf("recreate memory source table: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recreate native fts index: %v", err)
	}
	if _, err := database.ExecuteScript(ctx, `INSERT INTO vmm_memory_nodes (id, vector_id, abstract, details, updated_timestamp) VALUES (1, 'vec-new', 'freshNewNeedleXYZ', '新内容', 2);`, nil, ""); err != nil {
		t.Fatalf("insert reused memory source row: %v", err)
	}
	if _, err := database.EnsureFtsIndex(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE); err != nil {
		t.Fatalf("recover reused fts row: %v", err)
	}
	old, err = database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "legacyOldNeedleXYZ", 20, 0)
	if err != nil || old.Total != 0 {
		t.Fatalf("old fts term survived native reset: result=%+v err=%v", old, err)
	}
	current, err := database.SearchFts(ctx, nativeFTSDefaultLogicalIndex, storagecontract.TokenizerGSE, "freshNewNeedleXYZ", 20, 0)
	if err != nil || current.Total != 1 {
		t.Fatalf("new reused ID was not indexed: result=%+v err=%v", current, err)
	}
}

// TestNativeDatabaseCloseRace verifies concurrent close and query calls remain serialized and panic-free.
// TestNativeDatabaseCloseRace 验证并发关闭与查询保持串行且不会触发 panic。
func TestNativeDatabaseCloseRace(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "native-close-race.db"), time.Second)
	if err != nil {
		t.Fatalf("open native sqlite database: %v", err)
	}
	ctx := context.Background()
	if _, err := database.ExecuteScript(ctx, `CREATE TABLE close_probe (id INTEGER PRIMARY KEY);`, nil, ""); err != nil {
		t.Fatalf("create close probe: %v", err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for index := 0; index < 32; index++ {
			_, _ = database.QueryJSON(ctx, "SELECT COUNT(*) AS count FROM close_probe", nil, "")
		}
	}()
	go func() {
		defer wait.Done()
		_ = database.Close()
	}()
	wait.Wait()
	if err := database.Close(); err != nil {
		t.Fatalf("idempotent native close: %v", err)
	}
}
