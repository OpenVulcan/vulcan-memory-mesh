// nativelance_integration_test.go exercises the real Rust cdylib when a test library path is injected.
// nativelance_integration_test.go 在注入测试库路径时验证真实 Rust cdylib。
package nativelance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/platform/storagecontract/lance"
)

// TestNativeLibraryABIAndErrorBoundary verifies version/capability negotiation and non-empty TLS diagnostics.
// TestNativeLibraryABIAndErrorBoundary 验证版本/能力协商及非空 TLS 诊断。
func TestNativeLibraryABIAndErrorBoundary(t *testing.T) {
	libraryPath := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY"))
	if libraryPath == "" {
		t.Skip("VMM_NATIVE_LANCEDB_LIBRARY is not set")
	}
	lib, err := Open(libraryPath)
	if err != nil {
		t.Fatalf("open native library: %v", err)
	}
	defer func() { _ = lib.Close() }()
	version, err := lib.EngineVersion()
	if err != nil || version != "0.39.0" {
		t.Fatalf("unexpected native engine version: %q err=%v", version, err)
	}
	var capabilities map[string]any
	data, err := lib.Capabilities()
	if err != nil || json.Unmarshal(data, &capabilities) != nil {
		t.Fatalf("decode native capabilities: data=%s err=%v", data, err)
	}
	if capabilities["distance_type"] != "l2" || capabilities["context_cancellation"] != "before_call_only" {
		t.Fatalf("capability semantics missing: %#v", capabilities)
	}
	rt, err := lib.CreateRuntimeWithTimeout(t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatalf("create native runtime: %v", err)
	}
	defer func() { _ = rt.Close() }()
	engine, err := rt.OpenDefaultEngine()
	if err != nil {
		t.Fatalf("open native engine: %v", err)
	}
	defer func() { _ = engine.Close() }()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.CountRows(canceled, "missing", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-call cancellation was not preserved: %v", err)
	}
	if _, err := engine.CountRows(context.Background(), "missing", ""); err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("missing-table error lost native diagnostic: %v", err)
	}
}

// TestNativeLibraryRoundTrip verifies table schema, atomic upsert, L2 search, count, delete, and drop against the real DLL.
// TestNativeLibraryRoundTrip 使用真实 DLL 验证建表 Schema、原子 upsert、L2 检索、计数、删除与删表。
func TestNativeLibraryRoundTrip(t *testing.T) {
	libraryPath := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY"))
	if libraryPath == "" {
		t.Skip("VMM_NATIVE_LANCEDB_LIBRARY is not set")
	}
	lib, err := Open(libraryPath)
	if err != nil {
		t.Fatalf("open native library: %v", err)
	}
	defer func() { _ = lib.Close() }()
	rt, err := lib.CreateRuntime(t.TempDir())
	if err != nil {
		t.Fatalf("create native runtime: %v", err)
	}
	defer func() { _ = rt.Close() }()
	engine, err := rt.OpenDefaultEngine()
	if err != nil {
		t.Fatalf("open native engine: %v", err)
	}
	defer func() { _ = engine.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := lanceCreateTableRequest("roundtrip")
	if _, err := engine.CreateTable(ctx, request); err != nil {
		t.Fatalf("create table: %v", err)
	}
	rows := []byte(`[{"id":"one","label":"first","vector":[1.0,0.0]}]`)
	result, err := engine.VectorUpsertRaw(ctx, "roundtrip", lance.InputFormatJSONRows, rows, []string{"id"})
	if err != nil {
		t.Fatalf("upsert rows: %v", err)
	}
	if result.InputRows != 1 || result.InsertedRows != 1 {
		t.Fatalf("unexpected upsert accounting: %+v", result)
	}
	count, err := engine.CountRows(ctx, "roundtrip", "")
	if err != nil || count != 1 {
		t.Fatalf("count rows: count=%d err=%v", count, err)
	}
	schema, err := engine.Schema(ctx, "roundtrip")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if !strings.Contains(string(schema), "vector") {
		t.Fatalf("schema did not contain vector column: %s", schema)
	}
	search, err := engine.VectorSearchF32(ctx, "roundtrip", []float32{1, 0}, 5, "label = 'first'", "vector", lance.OutputFormatJSONRows)
	if err != nil {
		t.Fatalf("search rows: %v", err)
	}
	var hits []map[string]any
	if err := json.Unmarshal(search.Data, &hits); err != nil || len(hits) != 1 {
		t.Fatalf("decode search rows: len=%d err=%v data=%s", len(hits), err, search.Data)
	}
	if label, ok := hits[0]["label"].(string); !ok || label != "first" {
		t.Fatalf("filtered search returned unexpected label: %#v", hits[0]["label"])
	}
	if distance, ok := hits[0]["_distance"].(float64); !ok || distance < 0 || distance > 0.000001 {
		t.Fatalf("L2 search returned unexpected distance: %#v", hits[0]["_distance"])
	}
	if _, err := engine.VectorSearchF32(ctx, "roundtrip", []float32{1}, 5, "", "vector", lance.OutputFormatJSONRows); err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatal("dimension mismatch search unexpectedly succeeded")
	}
	updatedRows := []byte(`[{"id":"one","label":"updated","vector":[0.0,1.0]}]`)
	updated, err := engine.VectorUpsertRaw(ctx, "roundtrip", lance.InputFormatJSONRows, updatedRows, []string{"id"})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if updated.InputRows != 1 || updated.UpdatedRows != 1 || updated.InsertedRows != 0 {
		t.Fatalf("unexpected atomic update accounting: %+v", updated)
	}
	search, err = engine.VectorSearchF32(ctx, "roundtrip", []float32{0, 1}, 5, "label = 'updated'", "vector", lance.OutputFormatJSONRows)
	if err != nil {
		t.Fatalf("search updated row: %v", err)
	}
	hits = nil
	if err := json.Unmarshal(search.Data, &hits); err != nil || len(hits) != 1 || hits[0]["label"] != "updated" {
		t.Fatalf("updated row was not atomically visible: len=%d err=%v data=%s", len(hits), err, search.Data)
	}
	if _, err := engine.Delete(ctx, lance.DeleteRequest{TableName: "roundtrip", Condition: "id = 'one'"}); err != nil {
		t.Fatalf("delete rows: %v", err)
	}
	count, err = engine.CountRows(ctx, "roundtrip", "")
	if err != nil || count != 0 {
		t.Fatalf("count after delete: count=%d err=%v", count, err)
	}
	if _, err := engine.DropTable(ctx, lance.DropTableRequest{TableName: "roundtrip"}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := engine.CountRows(ctx, "roundtrip", ""); err == nil || strings.TrimSpace(err.Error()) == "" {
		t.Fatalf("missing-table count unexpectedly succeeded or lost diagnostic: %v", err)
	}
}

// TestNativeLibraryPendingSchemaRecovery verifies that a durable pending marker is recovered against an already-created Lance table.
// TestNativeLibraryPendingSchemaRecovery 验证已创建 Lance 表对应的持久化 pending 标记可以在重开时恢复。
func TestNativeLibraryPendingSchemaRecovery(t *testing.T) {
	libraryPath := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY"))
	if libraryPath == "" {
		t.Skip("VMM_NATIVE_LANCEDB_LIBRARY is not set")
	}
	databasePath := t.TempDir()
	lib, err := Open(libraryPath)
	if err != nil {
		t.Fatalf("open native library: %v", err)
	}
	defer func() { _ = lib.Close() }()

	request := lanceCreateTableRequest("pending_recovery")
	rt, err := lib.CreateRuntimeWithTimeout(databasePath, 5*time.Second)
	if err != nil {
		t.Fatalf("create native runtime: %v", err)
	}
	defer func() {
		if rt != nil {
			_ = rt.Close()
		}
	}()
	engine, err := rt.OpenDefaultEngine()
	if err != nil {
		t.Fatalf("open native engine: %v", err)
	}
	defer func() {
		if engine != nil {
			_ = engine.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := engine.CreateTable(ctx, request); err != nil {
		t.Fatalf("create recovery table: %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close recovery engine: %v", err)
	}
	engine = nil
	if err := rt.Close(); err != nil {
		t.Fatalf("close recovery runtime: %v", err)
	}
	rt = nil

	// Rewrite only the marker to model a crash after table creation but before identity registration.
	// 仅重写标记来模拟表已创建但身份登记前进程中断的崩溃窗口。
	markerPath := filepath.Join(databasePath, ".vmm-native.json")
	markerData, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read native marker: %v", err)
	}
	var marker struct {
		SchemaVersion           uint32            `json:"schema_version"`
		ABIVersion              uint32            `json:"abi_version"`
		EngineVersion           string            `json:"engine_version"`
		SchemaIdentities        []string          `json:"schema_identities"`
		PendingSchemaIdentities []json.RawMessage `json:"pending_schema_identities"`
	}
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatalf("decode native marker: %v", err)
	}
	if len(marker.SchemaIdentities) != 1 || strings.TrimSpace(marker.SchemaIdentities[0]) == "" {
		t.Fatalf("unexpected registered identities before crash simulation: %#v", marker.SchemaIdentities)
	}
	pending := map[string]any{
		"identity":     marker.SchemaIdentities[0],
		"request":      request,
		"allow_create": true,
	}
	pendingData, err := json.Marshal(pending)
	if err != nil {
		t.Fatalf("encode pending schema intent: %v", err)
	}
	marker.SchemaIdentities = []string{}
	marker.PendingSchemaIdentities = []json.RawMessage{pendingData}
	markerData, err = json.MarshalIndent(marker, "", "  ")
	if err != nil {
		t.Fatalf("encode pending native marker: %v", err)
	}
	if err := os.WriteFile(markerPath, markerData, 0o600); err != nil {
		t.Fatalf("write pending native marker: %v", err)
	}

	// Reopening must validate the durable table and atomically consume the pending intent.
	// 重开必须校验磁盘上的表并原子消费 pending 意图。
	rt, err = lib.CreateRuntimeWithTimeout(databasePath, 5*time.Second)
	if err != nil {
		t.Fatalf("reopen native runtime for recovery: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("close recovered runtime: %v", err)
	}
	rt = nil
	markerData, err = os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read recovered native marker: %v", err)
	}
	if err := json.Unmarshal(markerData, &marker); err != nil {
		t.Fatalf("decode recovered native marker: %v", err)
	}
	if len(marker.SchemaIdentities) != 1 || marker.SchemaIdentities[0] != pending["identity"] {
		t.Fatalf("pending identity was not registered after reopen: %#v", marker.SchemaIdentities)
	}
	if len(marker.PendingSchemaIdentities) != 0 {
		t.Fatalf("pending identity was not consumed after reopen: %s", marker.PendingSchemaIdentities[0])
	}
}

// lanceCreateTableRequest returns the compact schema used by the real-DLL round-trip test.
// lanceCreateTableRequest 返回真实 DLL 往返测试使用的紧凑 Schema。
func lanceCreateTableRequest(tableName string) lance.CreateTableRequest {
	return lance.CreateTableRequest{
		TableName: tableName,
		Columns: []lance.CreateTableColumn{
			{Name: "id", ColumnType: "string", Nullable: false},
			{Name: "label", ColumnType: "string", Nullable: false},
			{Name: "vector", ColumnType: "vector_float32", VectorDim: 2, Nullable: false},
		},
	}
}
