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
