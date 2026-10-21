// SQLite adapter tests for profile lifecycle test.
// SQLite 适配层画像生命周期测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason verifies lifecycle expiry reasons and affected node ids are both bound as params.
// TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason 用于验证生命周期过期原因和受影响节点 id 都会作为参数绑定。
func TestStoreConvergeExpiredProfileNodesUsesTypedSQLiteParamsForStatusReason(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		switch {
		case strings.Contains(req.GetSql(), "expires_timestamp > 0"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired profile node",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		case strings.Contains(req.GetSql(), "LEFT JOIN vmm_turn_records"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if err != nil {
		t.Fatalf("ConvergeExpiredProfileNodes returned error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].ProfileType != logicdomain.ProfileTypeProject || snapshots[0].BindID != 9 {
		t.Fatalf("unexpected expired profile snapshots: %+v", snapshots)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one expiry update request, got %d", len(requests))
	}
	if strings.Contains(requests[0].GetSql(), "expired by lifecycle convergence") {
		t.Fatalf("expected expiry reason to stay out of SQL text, got %q", requests[0].GetSql())
	}
	if !strings.Contains(requests[0].GetSql(), "id IN (?)") {
		t.Fatalf("expected typed expired id placeholder, got %q", requests[0].GetSql())
	}
	if strings.Contains(requests[0].GetSql(), "41") {
		t.Fatalf("expected expired node id to stay out of SQL text, got %q", requests[0].GetSql())
	}
	params := requests[0].GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five expiry params, got %d: %#v", len(params), params)
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != "expired by lifecycle convergence" {
		t.Fatalf("expected lifecycle reason string param, got %#v", params[1])
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{params[0]}, int64(logicdomain.ProfileStatusExpired))
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 <= 0 {
		t.Fatalf("expected expiry updated timestamp param, got %#v", params[2])
	}
	requireSQLiteInt64Params(t, params[3:], int64(logicdomain.ProfileStatusActive), 41)
}

// TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain verifies partial expiry updates are not reported as clean convergence failures.
// TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain 用于验证部分过期更新漂移不会被当作干净收敛失败上报。
func TestStoreConvergeExpiredProfileNodesMarksPartialDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "expires_timestamp > 0") {
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[
{"id":41,"turn_id":null,"profile_type":%d,"bind_id":9,"content":"expired one","profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":"stable reason","refresh_weight":32,"source_kind":%d,"source_id":77,"status_reason":"","expires_timestamp":1,"superseded_by_id":0,"profile_date":"2026-07-05","created_timestamp":1,"updated_timestamp":1},
{"id":42,"turn_id":null,"profile_type":%d,"bind_id":9,"content":"expired two","profile_status":%d,"priority":%d,"profile_level":%d,"level_reason":"stable reason","refresh_weight":32,"source_kind":%d,"source_id":78,"status_reason":"","expires_timestamp":1,"superseded_by_id":0,"profile_date":"2026-07-05","created_timestamp":2,"updated_timestamp":2}
]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 2)
	if err == nil {
		t.Fatal("expected partial expiry drift to fail")
	}
	if snapshots != nil {
		t.Fatalf("expected no snapshots after partial expiry drift, got %+v", snapshots)
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial expiry drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "expire sqlite profile nodes by lifecycle convergence affected 1 rows, want 2") {
		t.Fatalf("unexpected partial expiry drift error: %v", err)
	}
}

// TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip verifies lifecycle expiry can recover when the durable nodes reached the exact expired state.
// TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip 用于验证生命周期过期提交未知时，如果长期节点已经达到精确 expired 状态即可恢复。
func TestStoreConvergeExpiredProfileNodesReconcilesCommitUnknownStatusFlip(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit sqlite profile expiry")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "expires_timestamp > 0"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired one",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			requireSQLiteInt64Params(t, req.GetParams(), 41)
			if captured == nil {
				t.Fatal("expected expiry update before reconcile read")
			}
			updatedMs := captured.GetParams()[2].Int64
			return &fakeQueryJSONResponse{JsonData: sqliteProfileNodeStatusRowsJSON(logicdomain.ProfileStatusExpired, "expired by lifecycle convergence", updatedMs, 41)}, nil
		case strings.Contains(sql, "n.profile_type = ?") && strings.Contains(sql, "n.bind_id = ?"):
			if captured == nil {
				t.Fatal("expected expiry update before active snapshot read")
			}
			updatedMs := captured.GetParams()[2].Int64
			requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.ProfileTypeProject), 9, int64(logicdomain.ProfileStatusActive), updatedMs)
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":43,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"still active",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":16,
				"source_kind":%d,
				"source_id":78,
				"status_reason":"",
				"expires_timestamp":0,
				"superseded_by_id":0,
				"profile_date":"2026-07-06",
				"created_timestamp":2,
				"updated_timestamp":2,
				"profile_date_anchor_timestamp":2
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP2,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		default:
			t.Fatalf("unexpected profile expiry reconcile query: %q", sql)
			return nil, nil
		}
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if err != nil {
		t.Fatalf("ConvergeExpiredProfileNodes returned error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].ProfileType != logicdomain.ProfileTypeProject || snapshots[0].BindID != 9 {
		t.Fatalf("unexpected recovered profile snapshots: %+v", snapshots)
	}
	if len(snapshots[0].Nodes) != 1 || snapshots[0].Nodes[0].ID != 43 {
		t.Fatalf("expected recovered snapshot to reload active nodes, got %+v", snapshots[0].Nodes)
	}
}

// TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain verifies profile lifecycle expiry preserves SQLite commit-boundary ambiguity.
// TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain 用于验证画像生命周期过期会保留 SQLite 提交边界不确定语义。
func TestStoreConvergeExpiredProfileNodesMarksCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite profile expiry")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "expires_timestamp > 0") {
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{
				"id":41,
				"turn_id":null,
				"profile_type":%d,
				"bind_id":9,
				"content":"expired one",
				"profile_status":%d,
				"priority":%d,
				"profile_level":%d,
				"level_reason":"stable reason",
				"refresh_weight":32,
				"source_kind":%d,
				"source_id":77,
				"status_reason":"",
				"expires_timestamp":1,
				"superseded_by_id":0,
				"profile_date":"2026-07-05",
				"created_timestamp":1,
				"updated_timestamp":1
			}]`,
				logicdomain.ProfileTypeProject,
				logicdomain.ProfileStatusActive,
				logicdomain.ProfilePriorityP1,
				logicdomain.ProfileLevelStable,
				logicdomain.ProfileSourceKindManualInstruction,
			)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	snapshots, err := store.ConvergeExpiredProfileNodes(context.Background(), 1)
	if snapshots != nil {
		t.Fatalf("expected no snapshots after commit-unknown expiry update, got %+v", snapshots)
	}
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected profile expiry commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "mark expired profile nodes") || !strings.Contains(err.Error(), "failed to commit sqlite profile expiry") {
		t.Fatalf("unexpected profile expiry commit error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesChecksBatchRowsChanged verifies rendered profile batch writes require every target row to be updated.
// TestStoreReplaceRenderedProfilesChecksBatchRowsChanged 用于验证渲染画像批量写回要求每个目标行都被更新。
func TestStoreReplaceRenderedProfilesChecksBatchRowsChanged(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteBatchRequest
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		ProjectProfiles: map[uint64]string{9: "project profile", 7: "earlier project"},
	})
	if err != nil {
		t.Fatalf("ReplaceRenderedProfiles returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected rendered profile batch request")
	}
	if !strings.Contains(captured.GetSql(), "UPDATE vmm_projects") {
		t.Fatalf("expected project profile update SQL, got %q", captured.GetSql())
	}
	items := captured.GetItems()
	if len(items) != 2 {
		t.Fatalf("expected two rendered profile batch items, got %d", len(items))
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{items[0].GetParams()[2], items[1].GetParams()[2]}, 7, 9)
}

// TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain verifies later clean misses become uncertain after an earlier scope batch has committed.
// TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain 用于验证前序 scope 批量写入已提交后，后续干净漏命中会按结果不确定上报。
func TestStoreReplaceRenderedProfilesMarksPriorBatchDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	batchCalls := 0
	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		if batchCalls == 1 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1, StatementsExecuted: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 0, StatementsExecuted: 1}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
		TeamProfiles: map[uint64]string{2: "team profile"},
	})
	if err == nil {
		t.Fatal("expected rendered profile drift to fail")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected rendered profile drift after prior batch to be outcome-uncertain, got %v", err)
	}
	if batchCalls != 2 {
		t.Fatalf("expected two rendered profile batch calls, got %d", batchCalls)
	}
	if !strings.Contains(err.Error(), "replace rendered team profiles: replace rendered profile batch affected 0 rows, want 1") {
		t.Fatalf("unexpected rendered profile drift error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch verifies a commit-uncertain rendered batch is accepted only when durable rows prove the same payload landed.
// TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch 用于验证提交不确定的渲染画像批量写入只有在长期行证明同一载荷已落库时才会恢复成功。
func TestStoreReplaceRenderedProfilesReconcilesInitialCommitUnknownBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedBatch *fakeExecuteBatchRequest
	var capturedQuery *fakeQueryRequest
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		capturedBatch = req
		return nil, errors.New("commit outcome unknown while replacing rendered project profiles")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		capturedQuery = req
		if capturedBatch == nil {
			t.Fatal("expected rendered profile batch write before reconciliation read")
		}
		if !strings.Contains(req.GetSql(), "SELECT id, profile, updated_at") || !strings.Contains(req.GetSql(), "FROM vmm_projects") {
			t.Fatalf("unexpected rendered profile reconciliation query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 7, 9)
		items := capturedBatch.GetItems()
		if len(items) != 2 {
			t.Fatalf("expected two project profile batch items, got %d", len(items))
		}
		firstUpdatedAt := items[0].GetParams()[1]
		secondUpdatedAt := items[1].GetParams()[1]
		if firstUpdatedAt.Kind != sqliteffi.SQLValueString || secondUpdatedAt.Kind != sqliteffi.SQLValueString || firstUpdatedAt.String != secondUpdatedAt.String {
			t.Fatalf("expected one shared updated_at string across batch, got %#v and %#v", firstUpdatedAt, secondUpdatedAt)
		}
		return &fakeQueryJSONResponse{
			JsonData: sqliteRenderedProfileBatchRowsJSON(map[uint64]string{
				7: "earlier project",
				9: "project profile",
			}, firstUpdatedAt.String, 7, 9),
		}, nil
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		ProjectProfiles: map[uint64]string{9: "project profile", 7: "earlier project"},
	})
	if err != nil {
		t.Fatalf("expected rendered profile commit-unknown batch to reconcile, got %v", err)
	}
	if capturedQuery == nil {
		t.Fatal("expected rendered profile reconciliation query")
	}
}

// TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain verifies the first scope batch keeps SQLite commit-boundary ambiguity visible.
// TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain 用于验证首个 scope 批量写回会保留 SQLite 提交边界不确定语义。
func TestStoreReplaceRenderedProfilesMarksInitialCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while replacing rendered user profile")
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected initial rendered profile commit error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace rendered user profiles") || !strings.Contains(err.Error(), "commit outcome unknown while replacing rendered user profile") {
		t.Fatalf("unexpected rendered profile commit error: %v", err)
	}
}

// TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary verifies ordinary first-scope failures are not promoted without commit-boundary evidence.
// TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary 用于验证没有提交边界证据的首个 scope 普通失败不会被提升为结果不确定。
func TestStoreReplaceRenderedProfilesKeepsInitialBatchErrorOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("rendered profile table unavailable")
	}

	err := store.ReplaceRenderedProfiles(context.Background(), logicdomain.RenderedProfileSet{
		UserProfiles: map[uint64]string{1: "user profile"},
	})
	if err == nil {
		t.Fatal("expected initial rendered profile batch error")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect ordinary initial rendered profile error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace rendered user profiles") || !strings.Contains(err.Error(), "rendered profile table unavailable") {
		t.Fatalf("unexpected rendered profile ordinary error: %v", err)
	}
}
