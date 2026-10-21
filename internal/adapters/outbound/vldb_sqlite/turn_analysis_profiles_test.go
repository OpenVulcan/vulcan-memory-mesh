// SQLite adapter tests for turn analysis profiles test.
// SQLite 适配层分析画像与失败结果测试，使用同包夹具验证存储行为。
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

// TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain verifies no-op profile inserts do not silently pass after the turn is marked extracted.
// TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain 用于验证 profile insert 未影响行时，不会在 turn 已标记提炼后静默通过。
func TestStoreApplyTurnAnalysisMarksProfileInsertZeroRowsUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_profile_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row profile insert to be outcome-uncertain after turn update, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference for profile-only insert failure, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert verifies profile-node inserts can recover from commit ambiguity when the durable row matches.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert 用于验证画像节点插入提交未知时，如果长期行匹配即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var profileInsert *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			profileInsert = req
			return nil, errors.New("failed to commit profile insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_profile_nodes AS n") && strings.Contains(sql, "WHERE n.id = ?"):
			if profileInsert == nil {
				t.Fatal("expected profile insert params before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 301)
			return &fakeQueryJSONResponse{JsonData: sqliteProfileNodeRowJSONFromInsertParams(t, profileInsert.GetParams())}, nil
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
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				LevelReason:   "stable preference",
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
				StatusReason:  "accepted",
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert, got %d requests", len(requests))
	}
	if profileInsert == nil {
		t.Fatal("expected profile insert request")
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain verifies unresolved profile insert ambiguity stays visible without inventing vector references.
// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain 用于验证无法对账的画像插入提交未知会保持可见，同时不虚构向量引用。
func TestStoreApplyTurnAnalysisKeepsUnmatchedProfileInsertCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_profile_nodes") {
			return nil, errors.New("failed to commit profile insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "FROM vmm_profile_nodes AS n") && strings.Contains(sql, "WHERE n.id = ?"):
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
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "profile content",
				Status:        logicdomain.ProfileStatusActive,
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 16,
				ProfileDate:   "2026-07-05",
				SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
				SourceID:      44,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain profile insert error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect profile-only insert to invent fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected profile insert commit-unknown error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile insert before unresolved commit unknown, got %d", len(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain verifies profile supersede drift is surfaced after the replacement node exists.
// TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain 用于验证替代节点已落库后的画像 supersede 漂移会被明确上报。
func TestStoreApplyTurnAnalysisMarksProfileSupersedeZeroRowsUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_profile_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details: "turn details",
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected profile supersede drift to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference for profile-only supersede failure, got %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede before failure, got %d requests", len(requests))
	}
	if !strings.Contains(requests[len(requests)-1].GetSql(), "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?)") {
		t.Fatalf("expected guarded profile supersede as final request, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede verifies uncertain profile supersede updates recover when all retired nodes reached the replacement id.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede 用于验证画像替代提交未知时，如果所有旧节点都指向替代节点即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownProfileSupersede(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return nil, errors.New("failed to commit profile supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			requireSQLiteInt64Params(t, req.GetParams(), 12, 13)
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"id":12,"profile_status":%d,"superseded_by_id":301},{"id":13,"profile_status":%d,"superseded_by_id":301}]`, logicdomain.ProfileStatusSuperseded, logicdomain.ProfileStatusSuperseded)}, nil
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
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12, 13},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain verifies unresolved profile supersede ambiguity remains visible.
// TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain 用于验证无法对账的画像替代提交未知仍保持结果不确定。
func TestStoreApplyTurnAnalysisKeepsUnmatchedProfileSupersedeCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.HasPrefix(sqlText, "UPDATE vmm_profile_nodes") && strings.Contains(sqlText, "superseded_by_id") {
			return nil, errors.New("failed to commit profile supersede")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT id, profile_status, superseded_by_id") && strings.Contains(sql, "FROM vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: fmt.Sprintf(`[{"id":12,"profile_status":%d,"superseded_by_id":0}]`, logicdomain.ProfileStatusActive)}, nil
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
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          "profile content",
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     "accepted replacement",
				SupersedeNodeIDs: []uint64{12, 13},
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain profile supersede error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect profile-only supersede to invent fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected profile supersede commit-unknown error: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, profile insert, and profile supersede before unresolved commit unknown, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert verifies a stale turn target stops the post-action write before fresh memory rows can reference it.
// TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert 用于验证过期 turn 目标会在新记忆行引用它之前阻断 post-action 写入。
func TestStoreApplyTurnAnalysisRejectsMissingTurnBeforeMemoryInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_turn_records") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
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
			}},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "update turn analysis") {
		t.Fatalf("expected missing turn update error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected missing turn to remain a clean failure before memory insert, got %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn update request before failure, got %d requests", len(requests))
	}
	if strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("did not expect memory insert after missing turn, got %q", sqliteExecutedSQLLog(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain verifies post-action memory replacement drift is surfaced after new durable rows may already exist.
// TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain 用于验证 post-action 记忆替代目标漂移时，会在新持久化行可能已存在后按结果不确定上报。
func TestStoreApplyTurnAnalysisMarksSupersedeDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-turn-1"},{"vector_id":"vec-old-turn-2"}]`}, nil
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
				VectorID:           "vec-new-turn",
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "project status",
				Details:            "project status",
				SupersedeMemoryIDs: []uint64{77, 78},
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP1,
				MemoryLevel:        logicdomain.MemoryLevelStable,
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected turn-analysis supersede drift to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected supersede drift to retain fresh vector references, got %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("expected turn update, memory insert, edge cleanup, and supersede update before failure, got %d requests", len(requests))
	}
	if !strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("expected memory insert before uncertain supersede failure, got %q", sqliteExecutedSQLLog(requests))
	}
	if !strings.Contains(requests[len(requests)-1].GetSql(), "id IN (?,?)") {
		t.Fatalf("expected final request to be supersede update, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyTurnAnalysisMarksFTSFailureUncertain verifies post-action FTS sync failures are unsafe to treat as clean failures after durable memory writes.
// TestStoreApplyTurnAnalysisMarksFTSFailureUncertain 用于验证 post-action 持久化记忆写入后，FTS 同步失败不能再被当作干净失败处理。
func TestStoreApplyTurnAnalysisMarksFTSFailureUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "MAX(id)") && strings.Contains(req.GetSql(), "vmm_memory_nodes") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.upsertFtsDocumentFunc = func(string, sqliteffi.TokenizerMode, string, string, string, string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{}, fmt.Errorf("fts unavailable")
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
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected turn-analysis fts failure to be outcome-uncertain, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected fts failure to retain fresh vector references, got %v", err)
	}
}
