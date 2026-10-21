// SQLite adapter tests for turn analysis edges test.
// SQLite 适配层分析情境边与记忆替换测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup verifies uncertain context-edge cleanup can recover when no edge rows remain.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup 用于验证情境边清理提交未知时，如果回读确认没有残留边即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeCleanup(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "201")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "DELETE FROM vmm_memory_context_edges") {
			return nil, errors.New("failed to commit memory context edge cleanup")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected recovered context cleanup result: %+v", result.InsertedMemoryNodes)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, memory insert, and context-edge cleanup, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert verifies uncertain edge inserts recover from the durable edge-table prefix.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert 用于验证情境边插入提交未知时，可通过长期边表前缀状态恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryContextEdgeInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "201")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	edgeInsertRequests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_context_edges") {
			edgeInsertRequests = append(edgeInsertRequests, req)
			if len(edgeInsertRequests) == 1 {
				return nil, errors.New("failed to commit memory context edge insert")
			}
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			if len(edgeInsertRequests) != 1 {
				t.Fatalf("expected first context edge insert before reconcile read, got %d", len(edgeInsertRequests))
			}
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryContextEdgeRowsJSONFromInsertRequests(t, edgeInsertRequests...)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   "deployment mode",
					ContextValue: "local oss",
					Relation:     logicdomain.MemoryContextRelationSupport,
				}, {
					ContextKey:   "phase",
					ContextValue: "analysis",
					Relation:     logicdomain.MemoryContextRelationRebuttal,
				}},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected recovered context insert result: %+v", result.InsertedMemoryNodes)
	}
	if len(edgeInsertRequests) != 2 {
		t.Fatalf("expected both context edge inserts to run after reconciliation, got %d", len(edgeInsertRequests))
	}
	if len(requests) != 5 {
		t.Fatalf("expected turn update, memory insert, cleanup, and two context inserts, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh verifies unresolved edge insert ambiguity keeps fresh vectors protected.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh 用于验证情境边插入无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryContextEdgeInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_context_edges") {
			return nil, errors.New("failed to commit memory context edge insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:    "vec-new-turn",
				Vector:      []float32{0.25, 0.75},
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:    "project status",
				Details:     "project status",
				SourceKind:  logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   "deployment mode",
					ContextValue: "local oss",
					Relation:     logicdomain.MemoryContextRelationSupport,
				}},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain context edge insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved context edge insert to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected context edge insert commit-unknown error: %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, cleanup, and context insert before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede verifies uncertain memory supersede updates recover before FTS cleanup runs.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede 用于验证 memory supersede 提交未知时，如果长期行已达标即可恢复并继续执行 FTS 清理。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemorySupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "201")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"},{"vector_id":"vec-old-78"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?,?)"):
			if supersedeRequest == nil {
				t.Fatal("expected memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77, 78)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusSuperseded, supersedeRequest.GetParams()[1].Int64, 77, 78)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
				SupersedeMemoryIDs: []uint64{78, 0, 77, 78},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.SupersededVectorIDs) != 2 || result.SupersededVectorIDs[0] != "vec-old-77" || result.SupersededVectorIDs[1] != "vec-old-78" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	if len(deletedFTSIDs) != 2 || deletedFTSIDs[0] != "77" || deletedFTSIDs[1] != "78" {
		t.Fatalf("expected recovered supersede to continue FTS cleanup, got %+v", deletedFTSIDs)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, context cleanup, and memory supersede, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh verifies unresolved memory supersede ambiguity keeps fresh vectors protected.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh 用于验证 memory supersede 无法对账时，会保留新向量已被长期 memory 行引用的不确定状态。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemorySupersedeCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var supersedeRequest *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_memory_nodes") && strings.Contains(sqlText, "memory_status = ?") {
			supersedeRequest = req
			return nil, errors.New("failed to commit memory supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-77"}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if supersedeRequest == nil {
				t.Fatal("expected memory supersede request before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 77)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeStatusRowsJSON(logicdomain.MemoryStatusActive, supersedeRequest.GetParams()[1].Int64, 77)}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	deletedFTSIDs := []string{}
	fake.deleteFtsDocumentFunc = func(_ string, id string) (sqliteffi.FtsMutationResult, error) {
		deletedFTSIDs = append(deletedFTSIDs, id)
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
				SupersedeMemoryIDs: []uint64{77},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory supersede error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved memory supersede to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "supersede turn analysis memory nodes") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected memory supersede commit-unknown error: %v", err)
	}
	if len(deletedFTSIDs) != 0 {
		t.Fatalf("did not expect FTS cleanup after unresolved supersede, got %+v", deletedFTSIDs)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, context cleanup, and memory supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}
