// store_test.go verifies the SQLite local-FFI adapter keeps typed-parameter writes, schema-version behavior,
// and lifecycle-sensitive SQL helpers stable after the legacy gRPC fallback path was removed.
// store_test.go 用于验证在移除旧 gRPC fallback 路径后，
// SQLite 本地 FFI 适配器仍能保持强类型参数写入、schema 版本行为以及生命周期相关 SQL 助手逻辑稳定。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

// TestStoreLoadUserByIDUsesTypedSQLiteParams verifies single-row lookups still use typed SQLite params instead of params_json.
// TestStoreLoadUserByIDUsesTypedSQLiteParams 用于验证单行查询仍会使用强类型 SQLite 参数，而不是 params_json。
func TestStoreLoadUserByIDUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{
			JsonData: `[{"id":1,"name":"default","profile":"","delete_confirm_code":"","created_at":"2026-04-01T00:00:00Z","updated_at":"2026-04-01T00:00:00Z"}]`,
		}, nil
	}

	record, err := store.loadUserByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("loadUserByID returned error: %v", err)
	}
	if record.ID != 1 {
		t.Fatalf("loadUserByID returned wrong id: %d", record.ID)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if len(captured.GetParams()) != 1 {
		t.Fatalf("expected one typed param, got %d", len(captured.GetParams()))
	}
	value := captured.GetParams()[0]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 1 {
		t.Fatalf("expected int64 sqlite param 1, got %#v", value)
	}
}

// TestStoreLoadRenderedProfileUsesTypedSQLiteParams verifies rendered-profile lookups keep the target id in typed params instead of raw SQL interpolation.
// TestStoreLoadRenderedProfileUsesTypedSQLiteParams 用于验证渲染画像读取会把目标 id 保持在强类型参数中，而不是直接插进 SQL 文本。
func TestStoreLoadRenderedProfileUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[{"profile":"project profile"}]`}, nil
	}

	profile, err := store.LoadRenderedProfile(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	})
	if err != nil {
		t.Fatalf("LoadRenderedProfile returned error: %v", err)
	}
	if profile != "project profile" {
		t.Fatalf("unexpected rendered profile: %q", profile)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id = ? LIMIT 1") {
		t.Fatalf("expected placeholder-based profile lookup sql, got %q", captured.GetSql())
	}
	value := captured.GetParams()[0]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 9 {
		t.Fatalf("expected int64 sqlite param 9, got %#v", value)
	}
}

// TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot verifies active profile-node queries do not append a hidden LIMIT when the caller explicitly requests an unbounded snapshot.
// TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot 用于验证当调用方显式请求不受限快照时，active 画像节点查询不会再偷偷追加隐藏 LIMIT。
func TestStoreListActiveProfileNodesOmitsLimitWhenCallerRequestsFullSnapshot(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	nodes, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 0)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty nodes slice, got %+v", nodes)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if strings.Contains(captured.GetSql(), "LIMIT ?") {
		t.Fatalf("expected unbounded active profile query to omit LIMIT, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 4 {
		t.Fatalf("expected four typed params without limit, got %d", len(captured.GetParams()))
	}
}

// TestStoreListActiveProfileNodesPreservesExplicitLargeLimit verifies callers can now request an explicit large active-node window without being silently capped to 256.
// TestStoreListActiveProfileNodesPreservesExplicitLargeLimit 用于验证调用方现在可以显式请求较大的 active 节点窗口，而不会被静默裁成 256。
func TestStoreListActiveProfileNodesPreservesExplicitLargeLimit(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	nodes, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 500)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty nodes slice, got %+v", nodes)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "LIMIT ?") {
		t.Fatalf("expected bounded active profile query to keep LIMIT placeholder, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 5 {
		t.Fatalf("expected five typed params with explicit limit, got %d", len(captured.GetParams()))
	}
	value := captured.GetParams()[4]
	if value.Kind != sqliteffi.SQLValueInt64 || value.Int64 != 500 {
		t.Fatalf("expected explicit sqlite limit 500, got %#v", value)
	}
}

// TestStoreListActiveProfileNodesOrdersByAnchorTimeline verifies public active-node queries now sort by the resolved profile-date anchor instead of the stale stored profile_date string or priority buckets.
// TestStoreListActiveProfileNodesOrdersByAnchorTimeline 用于验证公开 active 节点查询现在会按解析后的画像日期锚点排序，而不是继续依赖旧的 profile_date 字符串或优先级桶。
func TestStoreListActiveProfileNodesOrdersByAnchorTimeline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ListActiveProfileNodes(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeProject,
		BindID:      9,
	}, 10)
	if err != nil {
		t.Fatalf("ListActiveProfileNodes returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "ORDER BY profile_date_anchor_timestamp DESC, n.id ASC") {
		t.Fatalf("expected public active-node query to sort by anchor timeline desc, got %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "priority ASC") || strings.Contains(captured.GetSql(), "refresh_weight DESC") || strings.Contains(captured.GetSql(), "profile_date DESC") {
		t.Fatalf("expected public active-node query to drop legacy priority/profile_date ordering, got %q", captured.GetSql())
	}
}

// TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline verifies reviewer/lifecycle snapshots use the same corrected date anchor in ascending timeline order instead of the raw stored profile_date string.
// TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline 用于验证 reviewer 与生命周期快照会按修正后的日期锚点正序排序，而不是继续依赖原始 profile_date 字符串。
func TestStoreLoadActiveProfileNodesOrdersByAnchorTimeline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.loadActiveProfileNodes(context.Background(), logicdomain.ProfileTypeProject, 9, time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatalf("loadActiveProfileNodes returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "ORDER BY profile_date_anchor_timestamp ASC, n.id ASC") {
		t.Fatalf("expected internal active snapshot query to sort by anchor timeline asc, got %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "priority ASC") || strings.Contains(captured.GetSql(), "refresh_weight DESC") || strings.Contains(captured.GetSql(), "profile_date ASC") {
		t.Fatalf("expected internal active snapshot query to drop legacy priority/profile_date ordering, got %q", captured.GetSql())
	}
}

// TestStoreExecRetriesRetryableSQLiteErrors verifies SQLITE_BUSY style errors are retried inside the adapter before surfacing.
// TestStoreExecRetriesRetryableSQLiteErrors 用于验证 SQLITE_BUSY 这类错误会先在适配器内部重试，而不是立刻向外暴露。
func TestStoreExecRetriesRetryableSQLiteErrors(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	calls := 0
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("SQLITE_BUSY: database is locked")
		}
		if strings.TrimSpace(req.GetParamsJson()) != "" {
			t.Fatalf("expected retry attempt to keep typed params, got params_json=%q", req.GetParamsJson())
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.exec(context.Background(), `UPDATE vmm_users SET profile = ? WHERE id = ?`, "vmm", 1); err != nil {
		t.Fatalf("exec returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected one retry and two total calls, got %d", calls)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch verifies repeated prototype inserts are merged into one ExecuteBatch call.
// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch 用于验证重复原型写入会合并成一次 ExecuteBatch 调用。
func TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "hello",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{1, 2, 3},
			UpdatedAt:    time.Unix(0, 0).UTC(),
		},
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "world",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{4, 5, 6},
			UpdatedAt:    time.Unix(1, 0).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceNoiseEmbeddingCache returned error: %v", err)
	}
	if execCalls != 1 || batchCalls != 1 {
		t.Fatalf("expected delete+batch write, exec=%d batch=%d", execCalls, batchCalls)
	}
	if capturedBatch == nil || len(capturedBatch.GetItems()) != 2 {
		t.Fatalf("unexpected batch capture: %#v", capturedBatch)
	}
}

// TestStoreReplaceMemoryVectorsUsesExecuteBatch verifies vector_json rebuilds are persisted through one homogeneous ExecuteBatch call.
// TestStoreReplaceMemoryVectorsUsesExecuteBatch 用于验证 vector_json 重建会通过一次同构 ExecuteBatch 持久化。
func TestStoreReplaceMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
		{ID: "vec-2", Vector: []float32{4, 5, 6}},
	})
	if err != nil {
		t.Fatalf("ReplaceMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector rebuild, exec=%d batch=%d", execCalls, batchCalls)
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "[1,2,3]" {
		t.Fatalf("expected vector json string param, got %#v", first[0])
	}
	if first[1].Kind != sqliteffi.SQLValueString || first[1].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[1])
	}
}

// TestStoreClearMemoryVectorsUsesExecuteBatch verifies vector resets clear vector_json through one homogeneous ExecuteBatch call.
// TestStoreClearMemoryVectorsUsesExecuteBatch 用于验证向量重置会通过一次同构 ExecuteBatch 清空 vector_json。
func TestStoreClearMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1", "vec-2"})
	if err != nil {
		t.Fatalf("ClearMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector clear, exec=%d batch=%d", execCalls, batchCalls)
	}
	if !strings.Contains(capturedBatch.GetSql(), "SET vector_json = '[]'") {
		t.Fatalf("expected vector reset sql, got %q", capturedBatch.GetSql())
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[0])
	}
}

// TestStoreGetSchemaComponentVersionReadsComponentTable verifies schema versions are read only from the shared component table.
// TestStoreGetSchemaComponentVersionReadsComponentTable 用于验证 schema 版本只会从共享组件表读取。
func TestStoreGetSchemaComponentVersionReadsComponentTable(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	version, err := store.GetSchemaComponentVersion(context.Background(), "sqlite")
	if err != nil {
		t.Fatalf("GetSchemaComponentVersion returned error: %v", err)
	}
	if version != 20 {
		t.Fatalf("schema version = %d, want 20", version)
	}
}

// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly verifies version writes only touch the shared component row.
// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly 用于验证版本写入只会触达共享组件版本行。
func TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 15); err != nil {
		t.Fatalf("SetSchemaComponentVersion returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected shared component table writes, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected no legacy singleton writes, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version verifies versions older than 19 are explicitly rejected instead of silently reset.
// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version 用于验证旧于 19 的版本会被显式拒绝，而不是静默重建。
func TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":18}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.ensureSQLiteSchema(context.Background())
	if err == nil {
		t.Fatal("expected ensureSQLiteSchema to reject unsupported pre-19 version")
	}
	if !strings.Contains(err.Error(), "minimum supported version is 19") {
		t.Fatalf("unexpected ensureSQLiteSchema error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if strings.Contains(joined, "DROP TABLE IF EXISTS vmm_sessions;") {
		t.Fatalf("expected unsupported version to stop before destructive reset, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline verifies an empty database still bootstraps straight to version 20.
// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline 用于验证空数据库仍会直接初始化到 20 基线。
func TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_schema_versions"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_sessions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected fresh bootstrap SQL, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected bootstrap to ignore legacy version table, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline verifies existing version-19 databases drop removed work-memory tables.
// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline 用于验证既有 19 版本数据库会删除已移除的工作记忆表。
func TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":19}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	oldNodesTable := "vmm_" + "scratch" + "pad_nodes"
	oldPlansTable := "vmm_" + "scratch" + "pad_plans"
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "DROP TABLE IF EXISTS "+oldNodesTable) ||
		!strings.Contains(joined, "DROP TABLE IF EXISTS "+oldPlansTable) ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected old work-memory table drops plus version persistence, got %q", joined)
	}
}

// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step verifies future upgrades can still chain from the current 20 baseline.
// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step 用于验证未来升级仍能从当前 20 基线继续向上衔接。
func TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	stepApplied := false
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.applySchemaMigrationPlan(context.Background(), schemaMigrationPlan{
		Component:               schemaComponentSQLite,
		MinimumSupportedVersion: 20,
		TargetVersion:           21,
		Bootstrap:               bootstrapCurrentSQLiteSchema,
		Steps: []schemaMigrationStep{{
			FromVersion: 20,
			ToVersion:   21,
			Name:        "future migration",
			Up: func(ctx context.Context, s *Store) error {
				stepApplied = true
				return s.exec(ctx, `CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker(id INTEGER PRIMARY KEY)`)
			},
		}},
	})
	if err != nil {
		t.Fatalf("applySchemaMigrationPlan returned error: %v", err)
	}
	if !stepApplied {
		t.Fatal("expected 20->21 migration step to run")
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected future migration SQL plus version persistence, got %q", joined)
	}
}

// TestStoreListProjectMemoriesFiltersExpiredRows verifies rebuild-source queries stay aligned with the active/unexpired memory contract.
// TestStoreListProjectMemoriesFiltersExpiredRows 用于验证重建数据源查询与 active/未过期记忆契约保持一致。
func TestStoreListProjectMemoriesFiltersExpiredRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	rows, err := store.ListProjectMemories(context.Background(), 9)
	if err != nil {
		t.Fatalf("ListProjectMemories returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows from canned response, got %#v", rows)
	}
	if !strings.Contains(captured.GetSql(), "memory_status =") || !strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected active/unexpired filter in rebuild SQL, got %q", captured.GetSql())
	}
}

// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn verifies compact flow acquires the write lock before reading MAX(id).
// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn 用于验证 compact 流程会先拿写锁再读取 MAX(id)。
func TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	latestTurnQueryObserved := make(chan struct{}, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			latestTurnQueryObserved <- struct{}{}
			return &fakeQueryJSONResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true}, nil
	}

	locked := false
	store.writeMu.Lock()
	locked = true
	defer func() {
		if locked {
			store.writeMu.Unlock()
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, _, err := store.MarkSessionCompacted(context.Background(), logicdomain.SessionRef{SessionID: 41}, time.Unix(0, 0).UTC())
		done <- err
	}()

	select {
	case <-latestTurnQueryObserved:
		t.Fatal("expected latest-turn query to wait until write lock is released")
	case <-time.After(50 * time.Millisecond):
	}

	store.writeMu.Unlock()
	locked = false

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MarkSessionCompacted returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("MarkSessionCompacted did not finish after write lock release")
	}
}

// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams verifies delete/count helpers emit placeholder SQL plus stable param ordering.
// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams 用于验证删除/统计辅助逻辑会输出占位符 SQL 与稳定参数顺序。
func TestBuildProjectProfileNodesDeleteSQLUsesTypedParams(t *testing.T) {
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(9, 5, 3, true, true)
	if strings.Contains(deleteSQL, "bind_id = 9") || strings.Count(deleteSQL, "?") != 7 {
		t.Fatalf("unexpected delete sql: %q", deleteSQL)
	}
	expected := []any{
		logicdomain.ProfileTypeProject, uint64(9), uint64(9),
		logicdomain.ProfileTypeSpace, uint64(5),
		logicdomain.ProfileTypeTeam, uint64(3),
	}
	if fmt.Sprint(deleteParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected delete params: got=%v want=%v", deleteParams, expected)
	}

	countSQL, countParams := buildProjectProfileNodesCountSQL(9, 5, 3, true, true)
	if strings.Contains(countSQL, "bind_id = 9") || strings.Count(countSQL, "?") != 7 {
		t.Fatalf("unexpected count sql: %q", countSQL)
	}
	if fmt.Sprint(countParams) != fmt.Sprint(expected) {
		t.Fatalf("unexpected count params: got=%v want=%v", countParams, expected)
	}
}

// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows verifies vector-hit enrichment still ignores inactive or expired rows.
// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows 用于验证向量命中回表仍会忽略 inactive 或已过期行。
func TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	if _, err := store.LoadMemoryNodesByVectorIDs(context.Background(), []string{"vec-1", "vec-2"}); err != nil {
		t.Fatalf("LoadMemoryNodesByVectorIDs returned error: %v", err)
	}
	if !strings.Contains(captured.GetSql(), fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) ||
		!strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") ||
		!strings.Contains(captured.GetSql(), "vector_id IN ('vec-1','vec-2')") {
		t.Fatalf("unexpected vector lookup sql: %q", captured.GetSql())
	}
}

// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically verifies edge lookups normalize ids into one stable relational read.
// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically 用于验证情境边读取会把 id 规范化后合并成一次稳定查询。
func TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{
			JsonData: `[{"memory_id":201,"context_key":"deployment_mode","context_value":"local oss","support_count":2,"rebuttal_count":1,"last_supported_timestamp":1000,"last_rebutted_timestamp":2000,"created_timestamp":3000,"updated_timestamp":4000}]`,
		}, nil
	}

	edges, err := store.LoadMemoryContextEdgesByMemoryIDs(context.Background(), []uint64{202, 201, 202})
	if err != nil {
		t.Fatalf("LoadMemoryContextEdgesByMemoryIDs returned error: %v", err)
	}
	if len(edges) != 1 || edges[0].MemoryID != 201 || edges[0].SupportCount != 2 || edges[0].RebuttalCount != 1 {
		t.Fatalf("unexpected context edges: %+v", edges)
	}
	if !strings.Contains(captured.GetSql(), "FROM vmm_memory_context_edges") || !strings.Contains(captured.GetSql(), "memory_id IN (201,202)") {
		t.Fatalf("unexpected context edge sql: %q", captured.GetSql())
	}
}

// TestEvolveAdoptedMemoryRecordStrengthensLifecycle verifies adoption records reinforcement evidence and promotes cross-session hot facts.
// TestEvolveAdoptedMemoryRecordStrengthensLifecycle 用于验证采纳会记录强化证据，并提升跨会话热点事实的生命周期。
func TestEvolveAdoptedMemoryRecordStrengthensLifecycle(t *testing.T) {
	adoptedAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	row := logicdomain.MemoryNodeRecord{
		ID:                       201,
		OriginSessionID:          11,
		ScopeLevel:               logicdomain.MemoryScopeLevelSession,
		Priority:                 logicdomain.MemoryPriorityP0,
		MemoryLevel:              logicdomain.MemoryLevelSession,
		RefreshWeight:            1,
		ExpiresAt:                adoptedAt.Add(24 * time.Hour),
		AdoptedCount:             1,
		ReinforcementCount:       1,
		CrossSessionAdoptedCount: 3,
	}

	updated := evolveAdoptedMemoryRecord(logicdomain.SessionRef{SessionID: 22}, row, adoptedAt)
	if !updated.LastReinforcedAt.Equal(adoptedAt) || updated.ReinforcementCount != 2 {
		t.Fatalf("unexpected reinforcement writeback: %+v", updated)
	}
	if updated.ScopeLevel != logicdomain.MemoryScopeLevelProject || updated.MemoryLevel != logicdomain.MemoryLevelPersistent {
		t.Fatalf("unexpected promoted lifecycle: %+v", updated)
	}
	if updated.CrossSessionAdoptedCount != 4 || !updated.ExpiresAt.After(row.ExpiresAt) {
		t.Fatalf("unexpected cross-session adoption result: %+v", updated)
	}
}

// TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows verifies expired active rows are not written back during adoption.
// TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows 用于验证已过期 active 行在采纳时不会被回写。
func TestStoreApplyMemoryAdoptionSkipsExpiredActiveRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalled := false
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"过期记忆","details":"这条记忆在采纳回写前已经过期。","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalled = true
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 77}, []uint64{201}, time.Unix(2, 0).UTC()); err != nil {
		t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
	}
	if executeCalled {
		t.Fatal("expected expired active adoption target to skip write-back")
	}
}

// TestStoreApplyMemoryAdoptionLocksBeforeReading verifies adoption acquires the write lock before reading candidates.
// TestStoreApplyMemoryAdoptionLocksBeforeReading 用于验证采纳流程会在读取候选前先拿到写锁。
func TestStoreApplyMemoryAdoptionLocksBeforeReading(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queryStarted := make(chan struct{}, 1)
	allowQueryReturn := make(chan struct{})
	done := make(chan error, 1)

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			select {
			case queryStarted <- struct{}{}:
			default:
			}
			<-allowQueryReturn
			return &fakeQueryJSONResponse{JsonData: `[
{"id":201,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-201","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":3,"abstract":"仍有效记忆","details":"用于验证读阶段位于写锁内。","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":9999999999999,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true}, nil
	}

	store.writeMu.Lock()
	go func() {
		done <- store.ApplyMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 77}, []uint64{201}, time.Unix(100, 0).UTC())
	}()

	select {
	case <-queryStarted:
		t.Fatal("expected adoption query to wait until write lock is released")
	case <-time.After(150 * time.Millisecond):
	}

	store.writeMu.Unlock()
	close(allowQueryReturn)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ApplyMemoryAdoption returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected ApplyMemoryAdoption to finish after releasing write lock")
	}
}

// TestCurrentSchemaSQLContainsContextualMemoryTables verifies the managed schema and debug-clean script both know about contextual edge tables.
// TestCurrentSchemaSQLContainsContextualMemoryTables 用于验证受管 schema 与 debug-clean 脚本都包含情境边表。
func TestCurrentSchemaSQLContainsContextualMemoryTables(t *testing.T) {
	requiredFragments := []string{
		"support_count INTEGER NOT NULL DEFAULT 0",
		"rebuttal_count INTEGER NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS vmm_memory_context_edges",
		"idx_vmm_memory_context_edges_lookup",
		"idx_vmm_memory_context_edges_memory",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(currentSchemaSQL, fragment) {
			t.Fatalf("expected schema to contain %q", fragment)
		}
	}
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS vmm_memory_context_edges;") {
		t.Fatal("expected debug clean schema script to drop vmm_memory_context_edges")
	}
	legacyTablePrefix := "vmm_" + "scratch" + "pad"
	if strings.Contains(currentSchemaSQL, legacyTablePrefix) {
		t.Fatalf("expected current sqlite schema to omit removed work-memory tables")
	}
	legacyNodesTable := legacyTablePrefix + "_nodes"
	legacyPlansTable := legacyTablePrefix + "_plans"
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyNodesTable+";") ||
		!strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyPlansTable+";") {
		t.Fatalf("expected debug-clean script to keep legacy work-memory table drops")
	}
}

// TestNormalizeTurnMemoryContextEdgesAggregatesCounts verifies repeated context labels collapse into deterministic support/rebuttal counters.
// TestNormalizeTurnMemoryContextEdgesAggregatesCounts 用于验证重复情境标签会折叠成确定性的支持/反驳统计。
func TestNormalizeTurnMemoryContextEdgesAggregatesCounts(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	edges := normalizeTurnMemoryContextEdges(201, []logicdomain.MemoryContextEdgeCandidate{
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: logicdomain.MemoryContextRelationSupport},
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: logicdomain.MemoryContextRelationSupport},
		{ContextKey: "task_stage", ContextValue: "phase4", Relation: logicdomain.MemoryContextRelationRebuttal},
		{ContextKey: "deployment_mode", ContextValue: "local_oss", Relation: logicdomain.MemoryContextRelationSupport},
	}, now)

	if len(edges) != 2 {
		t.Fatalf("expected two aggregated context edges, got %+v", edges)
	}
	if edges[0].MemoryID != 201 || edges[0].ContextKey != "deployment_mode" || edges[0].ContextValue != "local oss" || edges[0].SupportCount != 1 || edges[0].RebuttalCount != 0 {
		t.Fatalf("unexpected first edge aggregation: %+v", edges[0])
	}
	if edges[1].ContextKey != "task_stage" || edges[1].SupportCount != 2 || edges[1].RebuttalCount != 1 {
		t.Fatalf("unexpected second edge aggregation: %+v", edges[1])
	}
	supportCount, rebuttalCount := summarizeMemoryContextEdges(edges)
	if supportCount != 3 || rebuttalCount != 1 {
		t.Fatalf("unexpected memory-level evidence summary: support=%d rebuttal=%d", supportCount, rebuttalCount)
	}
}

// TestBuildMemoryContextEdgesReplaceSQLRendersDeterministicStatements verifies edge persistence always clears old rows first and inserts normalized replacements second.
// TestBuildMemoryContextEdgesReplaceSQLRendersDeterministicStatements 用于验证情境边持久化总会先清空旧行，再插入规范化替代行。
func TestBuildMemoryContextEdgesReplaceSQLRendersDeterministicStatements(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	sqlText := buildMemoryContextEdgesReplaceSQL(201, []logicdomain.MemoryContextEdge{
		{
			MemoryID:        201,
			ContextKey:      "task_stage",
			ContextValue:    "phase4",
			SupportCount:    2,
			RebuttalCount:   1,
			LastSupportedAt: now,
			LastRebuttedAt:  now,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	})
	if !strings.Contains(sqlText, "DELETE FROM vmm_memory_context_edges") ||
		!strings.Contains(sqlText, "INSERT INTO vmm_memory_context_edges") ||
		!strings.Contains(sqlText, "'task_stage'") ||
		!strings.Contains(sqlText, "'phase4'") {
		t.Fatalf("unexpected context edge sql: %q", sqlText)
	}
}
