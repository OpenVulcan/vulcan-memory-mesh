// store_test.go verifies the sqlite-first transport helpers so the adapter keeps using typed params,
// ExecuteBatch, and retryable gateway semantics instead of silently regressing to an older compatibility path.
// store_test.go 用于验证 sqlite-first 传输助手，确保适配器持续使用强类型参数、ExecuteBatch 和可重试网关语义，
// 而不是悄悄回退到旧的兼容实现路径。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeSqliteClient is a tiny in-memory sqlite RPC stub used by transport-focused adapter tests.
// fakeSqliteClient 用于作为轻量级内存 sqlite RPC 桩，供聚焦传输层的适配器测试使用。
type fakeSqliteClient struct {
	executeScriptFunc func(context.Context, *sqlitev1.ExecuteRequest, ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error)
	executeBatchFunc  func(context.Context, *sqlitev1.ExecuteBatchRequest, ...grpc.CallOption) (*sqlitev1.ExecuteBatchResponse, error)
	queryJSONFunc     func(context.Context, *sqlitev1.QueryRequest, ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error)
}

// ExecuteScript forwards the call to the configured fake function.
// ExecuteScript 用于把调用转发到测试里配置的 fake 函数。
func (f *fakeSqliteClient) ExecuteScript(ctx context.Context, req *sqlitev1.ExecuteRequest, opts ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
	if f.executeScriptFunc == nil {
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}
	return f.executeScriptFunc(ctx, req, opts...)
}

// ExecuteBatch forwards the call to the configured fake function.
// ExecuteBatch 用于把调用转发到测试里配置的 fake 函数。
func (f *fakeSqliteClient) ExecuteBatch(ctx context.Context, req *sqlitev1.ExecuteBatchRequest, opts ...grpc.CallOption) (*sqlitev1.ExecuteBatchResponse, error) {
	if f.executeBatchFunc == nil {
		return &sqlitev1.ExecuteBatchResponse{Success: true}, nil
	}
	return f.executeBatchFunc(ctx, req, opts...)
}

// QueryJson forwards the call to the configured fake function.
// QueryJson 用于把调用转发到测试里配置的 fake 函数。
func (f *fakeSqliteClient) QueryJson(ctx context.Context, req *sqlitev1.QueryRequest, opts ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
	if f.queryJSONFunc == nil {
		return &sqlitev1.QueryJsonResponse{JsonData: "[]"}, nil
	}
	return f.queryJSONFunc(ctx, req, opts...)
}

// QueryStream is unused in these transport tests and should never be invoked.
// QueryStream 在这些传输测试里不会被使用；如果被调用说明测试范围跑偏了。
func (*fakeSqliteClient) QueryStream(context.Context, *sqlitev1.QueryRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[sqlitev1.QueryResponse], error) {
	return nil, status.Error(codes.Unimplemented, "query stream is not used in sqlite transport tests")
}

// TestStoreLoadUserByIDUsesTypedSQLiteParams verifies single-row lookups now prefer native sqlite typed params instead of params_json.
// TestStoreLoadUserByIDUsesTypedSQLiteParams 用于验证单行查询现在优先走 sqlite 原生强类型参数，而不是 params_json。
func TestStoreLoadUserByIDUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{
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
		t.Fatalf("expected QueryJson request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if len(captured.GetParams()) != 1 {
		t.Fatalf("expected one typed param, got %d", len(captured.GetParams()))
	}
	value, ok := captured.GetParams()[0].Kind.(*sqlitev1.SqliteValue_Int64Value)
	if !ok || value.Int64Value != 1 {
		t.Fatalf("expected int64 sqlite param 1, got %#v", captured.GetParams()[0].Kind)
	}
}

// TestStoreLoadRenderedProfileUsesTypedSQLiteParams verifies scope-profile lookups now keep the target id in typed sqlite params instead of interpolating it into raw SQL.
// TestStoreLoadRenderedProfileUsesTypedSQLiteParams 用于验证 scope 画像读取现在会把目标 id 保持在 sqlite 强类型参数里，而不是直接插入原始 SQL。
func TestStoreLoadRenderedProfileUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{JsonData: `[{"profile":"project profile"}]`}, nil
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
		t.Fatal("expected QueryJson request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id = ? LIMIT 1") {
		t.Fatalf("expected placeholder-based profile lookup sql, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 1 {
		t.Fatalf("expected one typed param, got %d", len(captured.GetParams()))
	}
	value, ok := captured.GetParams()[0].Kind.(*sqlitev1.SqliteValue_Int64Value)
	if !ok || value.Int64Value != 9 {
		t.Fatalf("expected int64 sqlite param 9, got %#v", captured.GetParams()[0].Kind)
	}
}

// TestStoreExecRetriesRetryableGatewayErrors verifies SQLITE_BUSY / SQLITE_LOCKED style responses are retried inside the adapter before surfacing to callers.
// TestStoreExecRetriesRetryableGatewayErrors 用于验证 SQLITE_BUSY / SQLITE_LOCKED 这类响应会先在适配器内部重试，而不是立刻暴露给调用方。
func TestStoreExecRetriesRetryableGatewayErrors(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	calls := 0
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, opts ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		calls++
		if calls == 1 {
			setTrailerOptions(opts, metadata.Pairs(
				"x-vldb-retryable", "true",
				"x-vldb-sqlite-code", "SQLITE_BUSY",
			))
			return nil, status.Error(codes.Unavailable, "SQLITE_BUSY: database is locked")
		}
		if strings.TrimSpace(req.GetParamsJson()) != "" {
			t.Fatalf("expected retry attempt to keep typed params, got params_json=%q", req.GetParamsJson())
		}
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	if err := store.exec(context.Background(), `UPDATE vmm_users SET profile = ? WHERE id = ?`, "vmm", 1); err != nil {
		t.Fatalf("exec returned error after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected one retry and two total calls, got %d", calls)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch verifies high-frequency repeated inserts are merged into ExecuteBatch instead of one RPC per row.
// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch 用于验证高频重复插入会合并成 ExecuteBatch，而不是退化成“一行一次 RPC”。
func TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *sqlitev1.ExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		execCalls++
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *sqlitev1.ExecuteBatchRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteBatchResponse, error) {
		batchCalls++
		capturedBatch = req
		return &sqlitev1.ExecuteBatchResponse{Success: true, StatementsExecuted: int64(len(req.GetItems()))}, nil
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
	if execCalls != 1 {
		t.Fatalf("expected one delete ExecuteScript call, got %d", execCalls)
	}
	if batchCalls != 1 {
		t.Fatalf("expected one ExecuteBatch call, got %d", batchCalls)
	}
	if capturedBatch == nil {
		t.Fatalf("expected ExecuteBatch request to be captured")
	}
	if len(capturedBatch.GetItems()) != 2 {
		t.Fatalf("expected two ExecuteBatch items, got %d", len(capturedBatch.GetItems()))
	}
	if first := capturedBatch.GetItems()[0].GetParams(); len(first) != 9 {
		t.Fatalf("expected nine typed params in first batch item, got %d", len(first))
	}
}

// TestStoreGetSchemaComponentVersionFallsBackToLegacySingleton verifies the reusable version framework can read old sqlite-only version rows before the new component table has been populated.
// TestStoreGetSchemaComponentVersionFallsBackToLegacySingleton 用于验证在新版组件表尚未填充前，可复用版本框架仍能回退读取旧版 sqlite 单例版本行。
func TestStoreGetSchemaComponentVersionFallsBackToLegacySingleton(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_schema_versions"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_version"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"schema_version":14}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}

	version, err := store.GetSchemaComponentVersion(context.Background(), "sqlite")
	if err != nil {
		t.Fatalf("GetSchemaComponentVersion returned error: %v", err)
	}
	if version != 14 {
		t.Fatalf("schema version = %d, want 14", version)
	}
}

// TestStoreSetSchemaComponentVersionPersistsComponentAndLegacyRows verifies version writes keep the new component table and the legacy sqlite singleton row synchronized.
// TestStoreSetSchemaComponentVersionPersistsComponentAndLegacyRows 用于验证版本写入会同时保持新版组件表和旧版 sqlite 单例版本行同步。
func TestStoreSetSchemaComponentVersionPersistsComponentAndLegacyRows(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	if err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 15); err != nil {
		t.Fatalf("SetSchemaComponentVersion returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") {
		t.Fatalf("expected schema version bootstrap SQL, got %q", joined)
	}
	if !strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected component version upsert SQL, got %q", joined)
	}
	if !strings.Contains(joined, "INSERT INTO vmm_version") {
		t.Fatalf("expected legacy sqlite version sync SQL, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaResetsLegacyPre14Version verifies very old local sqlite stores still recover by rebuilding to the current schema instead of failing with a missing migration path error.
// TestStoreEnsureSQLiteSchemaResetsLegacyPre14Version 用于验证非常老的本地 sqlite 库仍会通过重建当前 schema 恢复，而不会因为缺少迁移路径直接失败。
func TestStoreEnsureSQLiteSchemaResetsLegacyPre14Version(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_schema_versions"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "FROM vmm_version"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"schema_version":13}]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_projects"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":0}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}

	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "DROP TABLE IF EXISTS vmm_sessions;") {
		t.Fatalf("expected legacy schema reset SQL, got %q", joined)
	}
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_sessions") {
		t.Fatalf("expected current schema bootstrap SQL, got %q", joined)
	}
	if !strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected schema version persistence after legacy reset, got %q", joined)
	}
}

// TestStoreSearchLexicalMemoryUsesTypedSQLiteParams verifies hybrid lexical recall keeps using MATCH with typed params instead of falling back to params_json.
// TestStoreSearchLexicalMemoryUsesTypedSQLiteParams 用于验证混合 lexical 召回仍通过 MATCH 和强类型参数执行，而不是退回 params_json。
func TestStoreSearchLexicalMemoryUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{
			JsonData: `[{"memory_id":201,"rank":0.42}]`,
		}, nil
	}

	hits, err := store.SearchLexicalMemory(context.Background(), "文本排序模型", 5, logicdomain.SearchFilter{
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	})
	if err != nil {
		t.Fatalf("SearchLexicalMemory returned error: %v", err)
	}
	if len(hits) != 1 || hits[0].MemoryID != 201 {
		t.Fatalf("unexpected lexical hits: %+v", hits)
	}
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	if !strings.Contains(captured.GetSql(), "vmm_memory_nodes_fts MATCH ?") {
		t.Fatalf("expected MATCH clause in lexical sql, got %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "bm25(vmm_memory_nodes_fts, 2.0, 1.0)") {
		t.Fatalf("expected bm25 clause in lexical sql, got %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "(n.user_id = 0 OR n.user_id = ?)") {
		t.Fatalf("expected shared user scope filter in lexical sql, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 6 {
		t.Fatalf("expected six typed params, got %d", len(captured.GetParams()))
	}
	queryValue, ok := captured.GetParams()[0].Kind.(*sqlitev1.SqliteValue_StringValue)
	if !ok || !strings.Contains(queryValue.StringValue, "文本排序模型") {
		t.Fatalf("expected sanitized lexical query as first param, got %#v", captured.GetParams()[0].Kind)
	}
}

// TestStoreSearchLexicalMemoryPretokenizesChineseQuery verifies the sqlite adapter emits tokenized MATCH input when application-side Chinese pre-tokenization is enabled.
// TestStoreSearchLexicalMemoryPretokenizesChineseQuery 用于验证当启用应用层中文预分词时，sqlite 适配器会发出分词后的 MATCH 查询参数。
func TestStoreSearchLexicalMemoryPretokenizesChineseQuery(t *testing.T) {
	tokenizer, err := textutil.NewLexicalTokenizer(textutil.LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("NewLexicalTokenizer returned error: %v", err)
	}

	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second, lexicalTokenizer: tokenizer}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}

	_, err = store.SearchLexicalMemory(context.Background(), "文本排序模型", 5, logicdomain.SearchFilter{})
	if err != nil {
		t.Fatalf("SearchLexicalMemory returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	queryValue, ok := captured.GetParams()[0].Kind.(*sqlitev1.SqliteValue_StringValue)
	if !ok {
		t.Fatalf("expected string sqlite param, got %#v", captured.GetParams()[0].Kind)
	}
	want := `"文本 排序 模型" OR "文本" OR "排序" OR "模型"`
	if queryValue.StringValue != want {
		t.Fatalf("tokenized lexical query = %q, want %q", queryValue.StringValue, want)
	}
}

// TestStoreSearchLexicalMemoryAppliesCompactBoundaryFilter verifies BM25/FTS recall applies the current-session compact boundary directly inside SQL instead of filtering after retrieval.
// TestStoreSearchLexicalMemoryAppliesCompactBoundaryFilter 用于验证 BM25/FTS 召回会直接在 SQL 中应用当前 session compact 边界，而不是检索后再过滤。
func TestStoreSearchLexicalMemoryAppliesCompactBoundaryFilter(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}

	_, err := store.SearchLexicalMemory(context.Background(), "压缩前决策", 5, logicdomain.SearchFilter{
		UserID:            7,
		TeamID:            3,
		SpaceID:           5,
		ProjectID:         9,
		BoundarySessionID: 41,
		BoundaryMaxTurnID: 88,
	})
	if err != nil {
		t.Fatalf("SearchLexicalMemory returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "n.origin_session_id != ? OR n.source_turn_id IS NULL OR n.source_turn_id = 0 OR n.source_turn_id <= ?") {
		t.Fatalf("expected compact boundary clause in lexical sql, got %q", captured.GetSql())
	}
	if len(captured.GetParams()) != 8 {
		t.Fatalf("expected eight typed params, got %d", len(captured.GetParams()))
	}
}

// TestStoreListProjectMemoriesFiltersExpiredRows verifies vector rebuild source queries stay aligned with the runtime active/unexpired memory contract.
// TestStoreListProjectMemoriesFiltersExpiredRows 用于验证向量重建数据源查询会与运行时 active/未过期记忆口径保持一致。
func TestStoreListProjectMemoriesFiltersExpiredRows(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}

	rows, err := store.ListProjectMemories(context.Background(), 9)
	if err != nil {
		t.Fatalf("ListProjectMemories returned error: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows from canned response, got %#v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "memory_status =") || !strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected active/unexpired filter in rebuild SQL, got %q", captured.GetSql())
	}
}

// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn verifies the compact flow now acquires the shared write lock before reading MAX(id), closing the stale-boundary race with turn appends.
// TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn 用于验证 compact 流程现在会先获取共享写锁再读取 MAX(id)，从而关闭与 turn 追加并发时的过期边界竞态。
func TestStoreMarkSessionCompactedWaitsForWriteLockBeforeReadingLatestTurn(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	latestTurnQueryObserved := make(chan struct{}, 1)
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") {
			latestTurnQueryObserved <- struct{}{}
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"latest_turn_id":88}]`}, nil
		}
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		return &sqlitev1.ExecuteResponse{Success: true}, nil
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

	select {
	case <-latestTurnQueryObserved:
	default:
		t.Fatal("expected latest-turn query after write lock release")
	}
}

// TestStoreBuildMemoryNodeFTSUpsertSQLPretokenizesIndexText verifies durable memory rows are mirrored into FTS with application-side token spacing instead of raw unsplit Chinese text.
// TestStoreBuildMemoryNodeFTSUpsertSQLPretokenizesIndexText 用于验证长期记忆行写入 FTS 时会使用应用层预分词后的空格文本，而不是未切分的原始中文。
func TestStoreBuildMemoryNodeFTSUpsertSQLPretokenizesIndexText(t *testing.T) {
	tokenizer, err := textutil.NewLexicalTokenizer(textutil.LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("NewLexicalTokenizer returned error: %v", err)
	}

	store := &Store{lexicalTokenizer: tokenizer}
	sqlText := store.buildMemoryNodeFTSUpsertSQL(logicdomain.MemoryNodeRecord{
		ID:       101,
		Abstract: "文本排序模型",
		Details:  "vmm-local FTS5 中文分词",
	})

	if !strings.Contains(sqlText, `'文本 排序 模型'`) {
		t.Fatalf("expected pretokenized abstract in sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, `'vmm local fts5 中文 分词 vmm-local'`) {
		t.Fatalf("expected pretokenized details in sql, got %q", sqlText)
	}
}

// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams verifies the project-profile delete/count helpers now emit placeholder SQL plus a stable typed-param order instead of embedding ids directly into the statement text.
// TestBuildProjectProfileNodesDeleteSQLUsesTypedParams 用于验证项目画像节点删除与统计辅助逻辑现在会输出占位符 SQL 和稳定参数顺序，而不是把 id 直接嵌进语句文本。
func TestBuildProjectProfileNodesDeleteSQLUsesTypedParams(t *testing.T) {
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(9, 5, 3, true, true)
	if strings.Contains(deleteSQL, "bind_id = 9") || strings.Contains(deleteSQL, "project_id = 9") {
		t.Fatalf("expected placeholder-based delete sql, got %q", deleteSQL)
	}
	if strings.Count(deleteSQL, "?") != 7 {
		t.Fatalf("expected seven placeholders in delete sql, got %q", deleteSQL)
	}
	expectedDeleteParams := []any{
		logicdomain.ProfileTypeProject, uint64(9), uint64(9),
		logicdomain.ProfileTypeSpace, uint64(5),
		logicdomain.ProfileTypeTeam, uint64(3),
	}
	if fmt.Sprint(deleteParams) != fmt.Sprint(expectedDeleteParams) {
		t.Fatalf("unexpected delete params: got=%v want=%v", deleteParams, expectedDeleteParams)
	}

	countSQL, countParams := buildProjectProfileNodesCountSQL(9, 5, 3, true, true)
	if strings.Contains(countSQL, "bind_id = 9") || strings.Contains(countSQL, "project_id = 9") {
		t.Fatalf("expected placeholder-based count sql, got %q", countSQL)
	}
	if strings.Count(countSQL, "?") != 7 {
		t.Fatalf("expected seven placeholders in count sql, got %q", countSQL)
	}
	if fmt.Sprint(countParams) != fmt.Sprint(expectedDeleteParams) {
		t.Fatalf("unexpected count params: got=%v want=%v", countParams, expectedDeleteParams)
	}
}

// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows verifies vector-hit enrichment now ignores inactive or expired durable rows before they re-enter recall flows.
// TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows 用于验证向量命中回表现在会先排除 inactive 或已过期的长期行，避免它们重新进入召回链。
func TestStoreLoadMemoryNodesByVectorIDsFiltersExpiredRows(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}

	_, err := store.LoadMemoryNodesByVectorIDs(context.Background(), []string{"vec-1", "vec-2"})
	if err != nil {
		t.Fatalf("LoadMemoryNodesByVectorIDs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	if !strings.Contains(captured.GetSql(), fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) {
		t.Fatalf("expected active status filter in vector lookup sql, got %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected expiry filter in vector lookup sql, got %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "vector_id IN ('vec-1','vec-2')") {
		t.Fatalf("expected vector id list in vector lookup sql, got %q", captured.GetSql())
	}
}

// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically verifies context-edge lookup batches memory ids into one stable relational read.
// TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically 用于验证 context-edge 查询会把 memory ids 合并成一次稳定的关系层读取。
func TestStoreLoadMemoryContextEdgesByMemoryIDsQueriesDeterministically(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var captured *sqlitev1.QueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		captured = req
		return &sqlitev1.QueryJsonResponse{
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
	if captured == nil {
		t.Fatal("expected QueryJson request to be captured")
	}
	if !strings.Contains(captured.GetSql(), "FROM vmm_memory_context_edges") {
		t.Fatalf("expected context edge table in sql, got %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "memory_id IN (201,202)") {
		t.Fatalf("expected normalized memory id list in sql, got %q", captured.GetSql())
	}
}

// TestEvolveAdoptedMemoryRecordStrengthensLifecycle verifies memory adoption now records reinforcement evidence and promotes hot cross-session facts.
// TestEvolveAdoptedMemoryRecordStrengthensLifecycle 用于验证记忆采纳现在会记录强化证据，并把跨会话高频命中的事实提升生命周期等级。
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

	if !updated.LastReinforcedAt.Equal(adoptedAt) {
		t.Fatalf("last reinforced timestamp = %v", updated.LastReinforcedAt)
	}
	if updated.ReinforcementCount != 2 {
		t.Fatalf("reinforcement count = %d", updated.ReinforcementCount)
	}
	if updated.ScopeLevel != logicdomain.MemoryScopeLevelProject {
		t.Fatalf("scope level = %d", updated.ScopeLevel)
	}
	if updated.MemoryLevel != logicdomain.MemoryLevelPersistent {
		t.Fatalf("memory level = %d", updated.MemoryLevel)
	}
	if updated.CrossSessionAdoptedCount != 4 {
		t.Fatalf("cross-session adopted count = %d", updated.CrossSessionAdoptedCount)
	}
	if !updated.ExpiresAt.After(row.ExpiresAt) {
		t.Fatalf("expected expiry to extend, before=%v after=%v", row.ExpiresAt, updated.ExpiresAt)
	}
}

// TestCurrentSchemaSQLContainsContextualMemoryTables verifies the managed SQLite schema now includes context-evidence counters on memory rows plus the dedicated edge table.
// TestCurrentSchemaSQLContainsContextualMemoryTables 用于验证受管 SQLite schema 现在包含主记忆行上的证据计数，以及独立的情境边表。
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
	if !strings.Contains(resetManagedSchemaSQL, "DROP TABLE IF EXISTS vmm_memory_context_edges;") {
		t.Fatal("expected reset schema script to drop vmm_memory_context_edges")
	}
}

// TestNormalizeTurnMemoryContextEdgesAggregatesCounts verifies one memory candidate's repeated context labels collapse into deterministic support/rebuttal counters.
// TestNormalizeTurnMemoryContextEdgesAggregatesCounts 用于验证同一记忆候选上的重复情境标签会折叠成确定性的支持/反驳统计。
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

// TestBuildMemoryContextEdgesReplaceSQLRendersDeterministicStatements verifies edge persistence always clears the old rows first and then inserts the normalized replacements.
// TestBuildMemoryContextEdgesReplaceSQLRendersDeterministicStatements 用于验证情境边持久化会先清空旧行，再插入规范化后的替代行。
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
	if !strings.Contains(sqlText, "DELETE FROM vmm_memory_context_edges") {
		t.Fatalf("expected delete statement in context edge sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, "INSERT INTO vmm_memory_context_edges") {
		t.Fatalf("expected insert statement in context edge sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, "'task_stage'") || !strings.Contains(sqlText, "'phase4'") {
		t.Fatalf("expected key/value literals in context edge sql, got %q", sqlText)
	}
}

// setTrailerOptions populates grpc.Trailer call options so the adapter sees the same retry metadata shape a real gateway would emit.
// setTrailerOptions 用于填充 grpc.Trailer 调用选项，让适配器能看到真实网关会返回的重试 metadata 形态。
func setTrailerOptions(opts []grpc.CallOption, trailer metadata.MD) {
	for _, opt := range opts {
		trailerOpt, ok := opt.(grpc.TrailerCallOption)
		if !ok || trailerOpt.TrailerAddr == nil {
			continue
		}
		*trailerOpt.TrailerAddr = trailer
	}
}
