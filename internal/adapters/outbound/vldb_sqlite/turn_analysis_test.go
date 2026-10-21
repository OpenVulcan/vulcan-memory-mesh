// SQLite adapter tests for turn analysis test.
// SQLite 适配层分析结果提交测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites verifies post-action split writes keep extracted text out of SQL templates.
// TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites 用于验证 post-action 拆分写入会让提炼文本远离 SQL 模板。
func TestStoreApplyTurnAnalysisUsesTypedSQLiteParamsForExtractedTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "201")
	expectSQLiteFTSDeletes(t, fake, "77")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "id IN (?,?)") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"user","profile":"","delete_confirm_code":"","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "FROM vmm_projects p"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":9,"team_id":2,"space_id":3,"name":"project","profile":"","team_name":"team","space_name":"space","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_profile_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":301}]`}, nil
		case strings.Contains(sql, "SELECT vector_id") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"vector_id":"vec-old-superseded"}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	turnCreatedAt := time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)
	details := "  turn details '; DROP TABLE vmm_turn_records; --  "
	userProfile := "  merged user profile '; DROP TABLE vmm_users; --  "
	projectProfile := "  merged project profile '; DROP TABLE vmm_projects; --  "
	vectorID := "  vec-new '; DROP TABLE vmm_memory_nodes; --  "
	memoryAbstract := "  memory abstract '; DROP TABLE vmm_memory_nodes; --  "
	memoryDetails := "  memory details '; DROP TABLE vmm_memory_nodes; --  "
	dedupeHash := "  hash '; DROP TABLE vmm_memory_nodes; --  "
	contextKey := "Phase Key '; DROP TABLE vmm_memory_context_edges; --"
	contextValue := "Delivery Value '; DROP TABLE vmm_memory_context_edges; --"
	profileContent := "  profile content '; DROP TABLE vmm_profile_nodes; --  "
	profileLevelReason := "  profile level reason '; DROP TABLE vmm_profile_nodes; --  "
	profileStatusReason := "  profile status reason '; DROP TABLE vmm_profile_nodes; --  "
	result, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: turnCreatedAt, UpdatedAt: turnCreatedAt},
		logicdomain.TurnAnalysis{
			Details:              details,
			DetailsBudget:        123,
			UserProfileMerged:    true,
			MergedUserProfile:    userProfile,
			ProjectProfileMerged: true,
			MergedProjectProfile: projectProfile,
			MemoryNodes: []logicdomain.MemoryNodeCandidate{{
				VectorID:           vectorID,
				Vector:             []float32{0.25, 0.75},
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           memoryAbstract,
				Details:            memoryDetails,
				SupersedeMemoryIDs: []uint64{77},
				ContextEdges: []logicdomain.MemoryContextEdgeCandidate{{
					ContextKey:   contextKey,
					ContextValue: contextValue,
					Relation:     logicdomain.MemoryContextRelationSupport,
				}},
				SourceKind:    logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:    logicdomain.MemoryScopeLevelProject,
				Priority:      logicdomain.MemoryPriorityP1,
				MemoryLevel:   logicdomain.MemoryLevelStable,
				RefreshWeight: 8,
				DedupeHash:    dedupeHash,
			}},
			ProfileNodes: []logicdomain.ProfileNodeCandidate{{
				ProfileType:      logicdomain.ProfileTypeUser,
				Content:          profileContent,
				Status:           logicdomain.ProfileStatusActive,
				Priority:         logicdomain.ProfilePriorityP1,
				ProfileLevel:     logicdomain.ProfileLevelStable,
				LevelReason:      profileLevelReason,
				RefreshWeight:    16,
				ProfileDate:      "2026-07-05",
				SourceKind:       logicdomain.ProfileSourceKindTurnExtract,
				SourceID:         44,
				StatusReason:     profileStatusReason,
				SupersedeNodeIDs: []uint64{12},
			}},
		},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if len(result.InsertedMemoryNodes) != 1 || result.InsertedMemoryNodes[0].ID != 201 {
		t.Fatalf("unexpected inserted memory nodes: %+v", result.InsertedMemoryNodes)
	}
	if len(result.SupersededVectorIDs) != 1 || result.SupersededVectorIDs[0] != "vec-old-superseded" {
		t.Fatalf("unexpected superseded vector ids: %+v", result.SupersededVectorIDs)
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{
		"DROP TABLE",
		"drop table",
		"turn details",
		"merged user profile",
		"merged project profile",
		"vec-new",
		"memory abstract",
		"memory details",
		"profile content",
		"profile level reason",
		"profile status reason",
	} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected extracted text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if !strings.Contains(capturedSQL, "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?)") {
		t.Fatalf("expected profile supersede to guard status, target, and node id, got %q", capturedSQL)
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	for _, value := range []string{
		strings.TrimSpace(details),
		strings.TrimSpace(userProfile),
		strings.TrimSpace(projectProfile),
		strings.TrimSpace(vectorID),
		strings.TrimSpace(memoryAbstract),
		strings.TrimSpace(memoryDetails),
		strings.TrimSpace(dedupeHash),
		logicdomain.NormalizeMemoryContextKey(contextKey),
		logicdomain.NormalizeMemoryContextValue(contextValue),
		strings.TrimSpace(profileContent),
		strings.TrimSpace(profileLevelReason),
	} {
		if got := countSQLiteStringParam(requests, value); got != 1 {
			t.Fatalf("expected string param %q exactly once, got %d", value, got)
		}
	}
	if got := countSQLiteStringParam(requests, strings.TrimSpace(profileStatusReason)); got != 2 {
		t.Fatalf("expected profile status reason in insert and supersede params, got %d", got)
	}
}

// TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn verifies post-action only flips the current session's pending turn.
// TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn 用于验证 post-action 只会切换当前 session 的 pending turn。
func TestParameterizedTurnAnalysisUpdateStatementGuardsSessionPendingTurn(t *testing.T) {
	statement := parameterizedTurnAnalysisUpdateStatement(33, 44, "  turn details  ", 123, 4567)

	if !strings.Contains(statement.SQL, "WHERE id = ? AND session_id = ? AND extracted_status = ?") {
		t.Fatalf("expected turn analysis update to guard id, session, and pending status, got %q", statement.SQL)
	}
	if len(statement.Params) != 7 {
		t.Fatalf("expected seven turn analysis update params, got %d: %#v", len(statement.Params), statement.Params)
	}
	expectedParams := []any{
		"turn details",
		123,
		logicdomain.TurnExtractedStatusDone,
		int64(4567),
		uint64(44),
		uint64(33),
		logicdomain.TurnExtractedStatusPending,
	}
	for idx, expected := range expectedParams {
		if statement.Params[idx] != expected {
			t.Fatalf("param %d mismatch: got %#v, want %#v", idx, statement.Params[idx], expected)
		}
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate verifies the first turn-status mutation can recover when the durable row has reached the exact analysis target.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate 用于验证首个 turn 状态写入提交未知时，如果长期行达到精确分析目标即可恢复。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownTurnUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit analyzed turn")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			requireSQLiteInt64Params(t, req.GetParams(), 44)
			if captured == nil {
				t.Fatal("expected turn update params before reconcile read")
			}
			return &fakeQueryJSONResponse{JsonData: sqliteAnalyzedTurnRowJSON(44, 33, 9, "turn details", 123, captured.GetParams()[3].Int64)}, nil
		}
		t.Fatalf("unexpected turn-analysis reconcile query: %q", req.GetSql())
		return nil, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{Details: "turn details", DetailsBudget: 123},
	)
	if err != nil {
		t.Fatalf("ApplyTurnAnalysis returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected turn update request")
	}
	requireSQLiteInt64Params(t, captured.GetParams()[2:], int64(logicdomain.TurnExtractedStatusDone), captured.GetParams()[3].Int64, 44, 33, int64(logicdomain.TurnExtractedStatusPending))
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain verifies a commit-unknown first mutation is not treated as recoverable when the read-back row differs.
// TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain 用于验证首个写入提交未知且回读行不匹配时，不能把它当作可恢复成功。
func TestStoreApplyTurnAnalysisKeepsUnmatchedTurnUpdateCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return nil, errors.New("failed to commit analyzed turn")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_turn_records") && strings.Contains(req.GetSql(), "WHERE id = ?") {
			return &fakeQueryJSONResponse{JsonData: sqliteAnalyzedTurnRowJSON(44, 33, 9, "different details", 123, time.Now().UTC().UnixMilli())}, nil
		}
		t.Fatalf("unexpected turn-analysis reconcile query: %q", req.GetSql())
		return nil, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{Details: "turn details", DetailsBudget: 123},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain turn update error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected turn update commit-unknown error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only the turn update request before unresolved commit unknown, got %d", len(requests))
	}
}

// TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate verifies profile merge target drift is caught before the turn is marked extracted.
// TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate 用于验证合并画像目标漂移会在 turn 被标记为已提炼之前被拦截。
func TestStoreApplyTurnAnalysisRejectsMissingMergedProfileTargetBeforeTurnUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_users") {
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details:           "turn details",
			UserProfileMerged: true,
			MergedUserProfile: "merged user profile",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "load merged user profile target") {
		t.Fatalf("expected missing merged user target error, got %v", err)
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected missing merged profile target to remain a clean failure before turn update, got %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("expected no writes before missing merged profile target failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference verifies profile update drift after the turn update still allows fresh-vector rollback.
// TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference 用于验证 turn 更新后的画像目标漂移会标记关系结果不确定，但仍允许回滚尚未被引用的新向量。
func TestStoreApplyTurnAnalysisMarksProfileTargetUpdateDriftUncertainWithoutFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_users") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_users"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7,"name":"user","profile":"","delete_confirm_code":"","created_at":"2026-07-05T09:00:00Z","updated_at":"2026-07-05T09:00:00Z"}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	_, err := store.ApplyTurnAnalysis(
		context.Background(),
		logicdomain.SessionRef{SessionID: 33, UserID: 7, TeamID: 2, SpaceID: 3, ProjectID: 9},
		logicdomain.PersistedTurnRecord{ID: 44, SessionID: 33, ProjectID: 9, CreatedAt: time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)},
		logicdomain.TurnAnalysis{
			Details:           "turn details",
			UserProfileMerged: true,
			MergedUserProfile: "merged user profile",
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
		t.Fatalf("expected profile target update drift to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and profile target update before failure, got %d requests", len(requests))
	}
	if strings.Contains(sqliteExecutedSQLLog(requests), "INSERT INTO vmm_memory_nodes") {
		t.Fatalf("did not expect memory insert after profile target drift, got %q", sqliteExecutedSQLLog(requests))
	}
}

// TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference verifies a no-op memory insert stops before vectors become durable references.
// TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference 用于验证 memory insert 未影响行时，会在新向量形成长期引用之前停止。
func TestStoreApplyTurnAnalysisMarksMemoryInsertZeroRowsUncertainWithoutFreshVectorReference(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
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
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row memory insert to be outcome-uncertain after turn update, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect fresh vector reference before memory insert affects a row, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and memory insert before failure, got %d requests", len(requests))
	}
}

// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert verifies recovered memory inserts keep going without forcing vector rollback.
// TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert 用于验证 memory insert 提交未知但可对账时会继续后续写入，不触发向量回滚语义。
func TestStoreApplyTurnAnalysisReconcilesCommitUnknownMemoryInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	expectSQLiteFTSUpserts(t, fake, "201")
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	var memoryInsert *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			memoryInsert = req
			return nil, errors.New("failed to commit memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
			if memoryInsert == nil {
				t.Fatal("expected memory insert params before reconcile read")
			}
			requireSQLiteInt64Params(t, req.GetParams(), 201)
			return &fakeQueryJSONResponse{JsonData: sqliteMemoryNodeRowJSONFromInsertParams(t, memoryInsert.GetParams())}, nil
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
		t.Fatalf("unexpected recovered memory insert result: %+v", result.InsertedMemoryNodes)
	}
	if len(requests) != 3 {
		t.Fatalf("expected turn update, memory insert, and context-edge cleanup, got %d requests", len(requests))
	}
	if memoryInsert == nil {
		t.Fatal("expected memory insert request")
	}
}

// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh verifies unresolved memory insert ambiguity keeps fresh vectors protected from rollback.
// TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh 用于验证 memory insert 提交未知且无法对账时，会保护可能已被引用的新向量不被回滚。
func TestStoreApplyTurnAnalysisKeepsUnmatchedMemoryInsertCommitUnknownFresh(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "INSERT INTO vmm_memory_nodes") {
			return nil, errors.New("failed to commit memory insert")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_memory_nodes"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":201}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "WHERE id IN (?)"):
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
			}},
		},
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain memory insert error, got %v", err)
	}
	if !logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("expected unresolved memory insert to protect fresh vector reference, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply turn analysis") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected memory insert commit-unknown error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected turn update and memory insert before unresolved commit unknown, got %d requests", len(requests))
	}
}
