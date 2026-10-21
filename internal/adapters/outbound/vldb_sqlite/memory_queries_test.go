// SQLite adapter tests for memory queries test.
// SQLite 适配层记忆读取与词法检索测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestStoreLoadMemoryNodesByIDsUsesTypedIDParams verifies memory-detail row lookups bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadMemoryNodesByIDsUsesTypedIDParams 用于验证 memory 详情行查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadMemoryNodesByIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":51,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-51","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":52,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-52","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}

	rows, err := store.LoadMemoryNodesByIDs(context.Background(), []uint64{52, 0, 51, 52})
	if err != nil {
		t.Fatalf("LoadMemoryNodesByIDs returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 51 || rows[1].ID != 52 {
		t.Fatalf("unexpected loaded memory nodes: %+v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders, got %q", sql)
	}
	if strings.Contains(sql, "51") || strings.Contains(sql, "52") {
		t.Fatalf("expected memory ids to stay out of LoadMemoryNodesByIDs SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 51, 52)
}

// TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams verifies context-edge lookups bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams 用于验证情境边查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadMemoryContextEdgesByMemoryIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"memory_id":51,"context_key":"phase","context_value":"alpha","support_count":1,"rebuttal_count":0,"last_supported_timestamp":10,"last_rebutted_timestamp":0,"created_timestamp":10,"updated_timestamp":20},
{"memory_id":52,"context_key":"phase","context_value":"beta","support_count":2,"rebuttal_count":0,"last_supported_timestamp":11,"last_rebutted_timestamp":0,"created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}

	edges, err := store.LoadMemoryContextEdgesByMemoryIDs(context.Background(), []uint64{52, 0, 51, 52})
	if err != nil {
		t.Fatalf("LoadMemoryContextEdgesByMemoryIDs returned error: %v", err)
	}
	if len(edges) != 2 || edges[0].MemoryID != 51 || edges[1].MemoryID != 52 {
		t.Fatalf("unexpected loaded memory context edges: %+v", edges)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE memory_id IN (?,?)") {
		t.Fatalf("expected typed memory placeholders, got %q", sql)
	}
	if strings.Contains(sql, "51") || strings.Contains(sql, "52") {
		t.Fatalf("expected memory ids to stay out of LoadMemoryContextEdgesByMemoryIDs SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 51, 52)
}

// TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams verifies supersede cleanup coordinates bind normalized memory ids instead of rendering them into SQL text.
// TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams 用于验证 supersede 清理坐标查询会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreLoadActiveMemoryVectorIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"vector_id":"vec-81"},
{"vector_id":" "},
{"vector_id":"vec-82"}
]`}, nil
	}

	vectorIDs, err := store.loadActiveMemoryVectorIDs(context.Background(), []uint64{82, 0, 81, 82})
	if err != nil {
		t.Fatalf("loadActiveMemoryVectorIDs returned error: %v", err)
	}
	if len(vectorIDs) != 2 || vectorIDs[0] != "vec-81" || vectorIDs[1] != "vec-82" {
		t.Fatalf("unexpected active vector ids: %+v", vectorIDs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "memory_status = ?") || !strings.Contains(sql, "id IN (?,?)") {
		t.Fatalf("expected typed active memory vector placeholders, got %q", sql)
	}
	for _, leaked := range []string{"81", "82"} {
		if strings.Contains(sql, leaked) {
			t.Fatalf("expected memory id %q to stay out of active vector SQL, got %q", leaked, sql)
		}
	}
	requireSQLiteInt64Params(t, captured.GetParams(), int64(logicdomain.MemoryStatusActive), 81, 82)
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

	vectorID := "vec-1 '; DROP TABLE vmm_memory_nodes; --"
	if _, err := store.LoadMemoryNodesByVectorIDs(context.Background(), []string{" " + vectorID + " ", "vec-2", vectorID}); err != nil {
		t.Fatalf("LoadMemoryNodesByVectorIDs returned error: %v", err)
	}
	if !strings.Contains(captured.GetSql(), fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) ||
		!strings.Contains(captured.GetSql(), "expires_timestamp <= 0 OR expires_timestamp >") ||
		!strings.Contains(captured.GetSql(), "vector_id IN (?,?)") {
		t.Fatalf("unexpected vector lookup sql: %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "vec-1") {
		t.Fatalf("vector ids leaked into SQL text: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two vector id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueString || params[0].String != vectorID {
		t.Fatalf("expected first vector id param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != "vec-2" {
		t.Fatalf("expected second vector id param, got %#v", params[1])
	}
}

// TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering verifies stale FTS rows cannot consume the final topK budget before active-row validation.
// TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering 用于验证陈旧 FTS 行不会在 active 回表校验前耗尽最终 topK 预算。
func TestStoreSearchLexicalMemoryOverfetchesBeforeRelationalFiltering(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedLimit uint32
	var capturedQuery *fakeQueryRequest
	queryCalls := 0
	fake.searchFtsFunc = func(_ string, _ sqliteffi.TokenizerMode, _ string, limit uint32, _ uint32) (sqliteffi.SearchResult, error) {
		capturedLimit = limit
		return sqliteffi.SearchResult{Hits: []sqliteffi.SearchHit{
			{ID: "201", Score: 0.99},
			{ID: "202", Score: 0.75},
		}}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		capturedQuery = req
		queryCalls++
		requireSQLiteInt64Params(t, req.GetParams(), 201, 202)
		return &fakeQueryJSONResponse{JsonData: `[{
			"id":202,
			"team_id":1,
			"space_id":2,
			"project_id":3,
			"user_id":4,
			"origin_session_id":5,
			"source_turn_id":6,
			"vector_id":"vec-202",
			"vector_json":"[0.1,0.2]",
			"source_kind":1,
			"scope_level":3,
			"category":1,
			"abstract":"valid lexical memory",
			"details":"valid lexical memory details",
			"memory_status":0,
			"priority":1,
			"memory_level":2,
			"refresh_weight":1,
			"support_count":1,
			"rebuttal_count":0,
			"status_reason":"",
			"expires_timestamp":4102444800000,
			"last_recalled_timestamp":0,
			"last_adopted_timestamp":0,
			"last_reinforced_timestamp":0,
			"recalled_count":0,
			"adopted_count":0,
			"reinforcement_count":0,
			"cross_session_adopted_count":0,
			"decay_disabled":0,
			"dedupe_hash":"hash-202",
			"created_timestamp":1712131200000,
			"updated_timestamp":1712131200000
		}]`}, nil
	}

	hits, err := store.SearchLexicalMemory(context.Background(), "memory", 1, logicdomain.SearchFilter{ProjectID: 3})
	if err != nil {
		t.Fatalf("SearchLexicalMemory returned error: %v", err)
	}
	if capturedLimit != 4 {
		t.Fatalf("expected overfetch limit 4, got %d", capturedLimit)
	}
	if queryCalls != 1 {
		t.Fatalf("expected one batched lexical materialization query, got %d", queryCalls)
	}
	sql := capturedQuery.GetSql()
	if !strings.Contains(sql, "id IN (?,?)") ||
		!strings.Contains(sql, fmt.Sprintf("memory_status = %d", logicdomain.MemoryStatusActive)) ||
		!strings.Contains(sql, "expires_timestamp <= 0 OR expires_timestamp >") {
		t.Fatalf("expected batched active lexical SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, capturedQuery.GetParams(), 201, 202)
	if len(hits) != 1 || hits[0].MemoryID != 202 {
		t.Fatalf("expected valid second lexical hit after stale row filtering, got %+v", hits)
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
	if !strings.Contains(captured.GetSql(), "FROM vmm_memory_context_edges") || !strings.Contains(captured.GetSql(), "memory_id IN (?,?)") {
		t.Fatalf("unexpected context edge sql: %q", captured.GetSql())
	}
	if strings.Contains(captured.GetSql(), "201") || strings.Contains(captured.GetSql(), "202") {
		t.Fatalf("expected memory ids to stay out of context edge SQL, got %q", captured.GetSql())
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 201, 202)
}
