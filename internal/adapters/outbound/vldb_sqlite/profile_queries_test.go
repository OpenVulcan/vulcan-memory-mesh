// SQLite adapter tests for profile queries test.
// SQLite 适配层画像读取测试，使用同包夹具验证存储行为。
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

// TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams verifies profile-node reconciliation reads bind normalized node ids.
// TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams 用于验证画像节点对账读取会绑定规范化 node id。
func TestStoreLoadProfileNodeStatusesByIDsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[
{"id":7,"profile_status":%d,"superseded_by_id":101},
{"id":9,"profile_status":%d,"superseded_by_id":101}
]`, logicdomain.ProfileStatusSuperseded, logicdomain.ProfileStatusSuperseded)}, nil
	}

	rows, err := store.loadProfileNodeStatusesByIDs(context.Background(), []uint64{9, 0, 7, 9})
	if err != nil {
		t.Fatalf("loadProfileNodeStatusesByIDs returned error: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 7 || rows[1].ID != 9 {
		t.Fatalf("unexpected profile node statuses: %+v", rows)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected typed profile node placeholders, got %q", sql)
	}
	if strings.Contains(sql, "7") || strings.Contains(sql, "9") {
		t.Fatalf("expected profile node ids to stay out of status SQL, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 7, 9)
}

// TestStoreLoadProfileInstructionByIDUsesTypedIDParam verifies manual-instruction reconciliation reads bind the instruction id.
// TestStoreLoadProfileInstructionByIDUsesTypedIDParam 用于验证手工指令对账读取会绑定 instruction id。
func TestStoreLoadProfileInstructionByIDUsesTypedIDParam(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
			"id":55,
			"profile_type":%d,
			"bind_id":9,
			"instruction":"keep deployment notes current",
			"instruction_status":%d,
			"review_result_json":"",
			"failure_reason":"",
			"created_timestamp":10,
			"updated_timestamp":20
		}]`, logicdomain.ProfileTypeProject, logicdomain.ProfileInstructionStatusPending)}, nil
	}

	record, found, err := store.loadProfileInstructionByID(context.Background(), 55)
	if err != nil {
		t.Fatalf("loadProfileInstructionByID returned error: %v", err)
	}
	if !found || record.ID != 55 || record.BindID != 9 {
		t.Fatalf("unexpected loaded profile instruction: found=%v record=%+v", found, record)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE id = ?") {
		t.Fatalf("expected typed profile instruction id placeholder, got %q", sql)
	}
	if strings.Contains(sql, "55") {
		t.Fatalf("expected profile instruction id to stay out of SQL text, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 55)
}

// TestStoreLoadProfileNodeByIDUsesTypedIDParam verifies manual-node reconciliation reads bind the profile node id.
// TestStoreLoadProfileNodeByIDUsesTypedIDParam 用于验证手工节点对账读取会绑定 profile node id。
func TestStoreLoadProfileNodeByIDUsesTypedIDParam(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
			"id":101,
			"turn_id":null,
			"profile_type":%d,
			"bind_id":9,
			"content":"manual profile node",
			"profile_status":%d,
			"priority":%d,
			"profile_level":%d,
			"level_reason":"stable reason",
			"refresh_weight":32,
			"source_kind":%d,
			"source_id":55,
			"status_reason":"",
			"expires_timestamp":0,
			"superseded_by_id":0,
			"profile_date":"2026-07-05",
			"created_timestamp":10,
			"updated_timestamp":20,
			"profile_date_anchor_timestamp":10
		}]`,
			logicdomain.ProfileTypeProject,
			logicdomain.ProfileStatusActive,
			logicdomain.ProfilePriorityP1,
			logicdomain.ProfileLevelStable,
			logicdomain.ProfileSourceKindManualInstruction,
		)}, nil
	}

	record, found, err := store.loadProfileNodeByID(context.Background(), 101)
	if err != nil {
		t.Fatalf("loadProfileNodeByID returned error: %v", err)
	}
	if !found || record.ID != 101 || record.BindID != 9 {
		t.Fatalf("unexpected loaded profile node: found=%v record=%+v", found, record)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "WHERE n.id = ?") {
		t.Fatalf("expected typed profile node id placeholder, got %q", sql)
	}
	if strings.Contains(sql, "101") {
		t.Fatalf("expected profile node id to stay out of SQL text, got %q", sql)
	}
	requireSQLiteInt64Params(t, captured.GetParams(), 101)
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
