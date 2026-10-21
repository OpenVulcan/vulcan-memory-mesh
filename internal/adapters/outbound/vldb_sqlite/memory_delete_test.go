// SQLite adapter tests for memory delete test.
// SQLite 适配层记忆删除测试，使用同包夹具验证存储行为。
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

// TestStoreDeleteMemoryNodesUsesTypedIDParams verifies manual delete reads and updates bind normalized memory ids instead of rendering them into SQL text.
// TestStoreDeleteMemoryNodesUsesTypedIDParams 用于验证手工删除的读取与更新都会绑定规范化 memory id，而不是把它们渲染进 SQL 文本。
func TestStoreDeleteMemoryNodesUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	queries := make([]*fakeQueryRequest, 0, 1)
	executed := make([]*fakeExecuteRequest, 0, 1)
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		queries = append(queries, req)
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	reason := "  manual delete '; DROP TABLE vmm_memory_nodes;  "
	deletedAt := time.Unix(16000, 0).UTC()
	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{302, 0, 301, 404, 302}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err != nil {
		t.Fatalf("DeleteMemoryNodes returned error: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected deleted vector ids: %+v", result.DeletedVectorIDs)
	}
	if len(queries) != 1 || len(executed) != 1 {
		t.Fatalf("expected one query and one update, got queries=%d executed=%d", len(queries), len(executed))
	}
	querySQL := queries[0].GetSql()
	if !strings.Contains(querySQL, "id IN (?,?,?)") {
		t.Fatalf("expected typed candidate memory placeholders, got %q", querySQL)
	}
	for _, leaked := range []string{"301", "302", "404"} {
		if strings.Contains(querySQL, leaked) {
			t.Fatalf("expected memory id %q to stay out of candidate SQL, got %q", leaked, querySQL)
		}
	}
	requireSQLiteInt64Params(t, queries[0].GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302, 404)

	updateSQL := executed[0].GetSql()
	if !strings.Contains(updateSQL, "id IN (?,?)") {
		t.Fatalf("expected typed deleted memory placeholders, got %q", updateSQL)
	}
	for _, leaked := range []string{"301", "302", "404", "DROP TABLE", "manual delete"} {
		if strings.Contains(updateSQL, leaked) {
			t.Fatalf("expected value %q to stay out of update SQL, got %q", leaked, updateSQL)
		}
	}
	params := executed[0].GetParams()
	if len(params) != 6 {
		t.Fatalf("expected six update params, got %d: %#v", len(params), params)
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{params[0]}, int64(logicdomain.MemoryStatusDeleted))
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(reason) {
		t.Fatalf("expected delete reason param, got %#v", params[1])
	}
	requireSQLiteInt64Params(t, params[2:], deletedAt.UnixMilli(), int64(logicdomain.MemoryStatusActive), 301, 302)
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "301" || deletedFTSIDs[1] != "302" {
		t.Fatalf("unexpected deleted FTS ids: %+v", deletedFTSIDs)
	}
}

// TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip verifies manual delete recovers when the durable rows prove the uncertain status update committed.
// TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip 用于验证手工删除状态更新提交未知时，如果长期行证明已提交则恢复成功。
func TestStoreDeleteMemoryNodesReconcilesCommitUnknownStatusFlip(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	reason := "  manual delete  "
	deletedAt := time.Unix(16000, 0).UTC()
	queryCount := 0
	var capturedUpdate *fakeExecuteRequest
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			if queryCount == 1 {
				requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302, 404)
				return &fakeQueryJSONResponse{JsonData: sqliteManualDeleteCandidateRowsJSON()}, nil
			}
			if capturedUpdate == nil {
				t.Fatal("expected manual-delete update request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301, 302)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeDeleteRowsJSON(logicdomain.MemoryStatusDeleted, reason, capturedUpdate.GetParams()[2].Int64, 301, 302)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedUpdate = req
		return nil, errors.New("failed to commit manual memory delete")
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{302, 301, 404}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err != nil {
		t.Fatalf("DeleteMemoryNodes returned error: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected recovered deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected recovered not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected recovered vector cleanup coordinates: %+v", result.DeletedVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "301" || deletedFTSIDs[1] != "302" {
		t.Fatalf("expected recovered delete to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain verifies unresolved manual-delete ambiguity does not expose vector cleanup coordinates.
// TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain 用于验证手工删除无法对账时，不会暴露 vector 清理坐标。
func TestStoreDeleteMemoryNodesKeepsUnmatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	reason := "manual delete"
	deletedAt := time.Unix(16000, 0).UTC()
	queryCount := 0
	var capturedUpdate *fakeExecuteRequest
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_memory_nodes") {
			queryCount++
			if queryCount == 1 {
				requireSQLiteInt64Params(t, req.GetParams(), int64(logicdomain.MemoryStatusActive), 301, 302)
				return &fakeQueryJSONResponse{JsonData: sqliteManualDeleteCandidateRowsJSON()}, nil
			}
			if capturedUpdate == nil {
				t.Fatal("expected manual-delete update request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301, 302)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeDeleteRowsJSON(logicdomain.MemoryStatusActive, reason, capturedUpdate.GetParams()[2].Int64, 301, 302)}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedUpdate = req
		return nil, errors.New("failed to commit manual memory delete")
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302}, logicdomain.SearchFilter{ProjectID: 9}, deletedAt, reason)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain manual delete error, got %v", err)
	}
	if len(result.DeletedMemoryIDs) != 0 || len(result.NotFoundMemoryIDs) != 0 || len(result.DeletedVectorIDs) != 0 {
		t.Fatalf("expected no cleanup coordinates after unresolved status flip, got %+v", result)
	}
	if !strings.Contains(err.Error(), "delete sqlite memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected manual delete commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved status flip, got %+v", deletedFTSIDs)
	}
	if queryCount != 2 {
		t.Fatalf("expected initial load and reconcile read, got %d memory queries", queryCount)
	}
}

// TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails verifies confirmed manual deletes still expose vector cleanup coordinates when the post-delete FTS sync boundary fails.
// TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails 用于验证手工删除已确认后，即使删除后的 FTS 同步边界失败，也会继续暴露向量清理坐标。
func TestStoreDeleteMemoryNodesReturnsCleanupCoordinatesWhenFTSSyncFails(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	deletedFTSIDs := make([]string, 0, 1)
	rebuildCalls := 0
	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts delete unavailable")
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	result, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302, 404}, logicdomain.SearchFilter{ProjectID: 9}, time.Unix(16000, 0).UTC(), "manual delete")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected FTS sync failure to be outcome-uncertain, got %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 301 || result.DeletedMemoryIDs[1] != 302 {
		t.Fatalf("unexpected deleted ids after FTS failure: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected not-found ids after FTS failure: %+v", result.NotFoundMemoryIDs)
	}
	if len(result.DeletedVectorIDs) != 2 || result.DeletedVectorIDs[0] != "vec-301" || result.DeletedVectorIDs[1] != "vec-302" {
		t.Fatalf("unexpected vector cleanup coordinates after FTS failure: %+v", result.DeletedVectorIDs)
	}
	if len(deletedFTSIDs) != 1 || deletedFTSIDs[0] != "301" {
		t.Fatalf("unexpected attempted FTS delete ids: %+v", deletedFTSIDs)
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after incremental delete failure, got %d", rebuildCalls)
	}
}

// TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup verifies stale delete drift cannot return active vector ids for cleanup.
// TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup 用于验证删除状态切换发生漂移时，不会把仍可能 active 的 vector id 返回给清理链路。
func TestStoreDeleteMemoryNodesRejectsPartialStatusFlipBeforeVectorCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	deletedFTSIDs := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[
{"id":301,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":41,"vector_id":"vec-301","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory a","details":"details a","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20},
{"id":302,"team_id":1,"space_id":2,"project_id":9,"user_id":4,"origin_session_id":5,"source_turn_id":42,"vector_id":"vec-302","vector_json":"[0.3,0.4]","source_kind":0,"scope_level":0,"category":4,"abstract":"memory b","details":"details b","memory_status":0,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":1,"rebuttal_count":0,"status_reason":"","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":11,"updated_timestamp":21}
]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.DeleteMemoryNodes(context.Background(), []uint64{301, 302}, logicdomain.SearchFilter{ProjectID: 9}, time.Unix(16000, 0).UTC(), "manual delete")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial delete status flip to be outcome-uncertain, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected one status update before failure, got %d", len(executed))
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after partial status flip, got %+v", deletedFTSIDs)
	}
}
