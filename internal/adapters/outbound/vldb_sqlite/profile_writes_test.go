// SQLite adapter tests for profile writes test.
// SQLite 适配层画像指令写入测试，使用同包夹具验证存储行为。
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

// TestStoreCreateProfileInstructionUsesTypedSQLiteParams verifies manual instruction text is bound as typed values instead of being interpolated into the SQL script.
// TestStoreCreateProfileInstructionUsesTypedSQLiteParams 用于验证手工画像指令文本会作为强类型参数绑定，而不是被插入 SQL 脚本文本。
func TestStoreCreateProfileInstructionUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, _ *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
	}
	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	instruction := "  keep 'quoted'; DROP TABLE vmm_profile_nodes;  "
	reviewResult := "  {\"decision\":\"needs 'review'\"}  "
	failureReason := "  pending reason '; DROP TABLE vmm_users;  "
	record, err := store.CreateProfileInstruction(context.Background(), logicdomain.ProfileInstructionRecord{
		ProfileType:   logicdomain.ProfileTypeProject,
		BindID:        7,
		Instruction:   instruction,
		Status:        logicdomain.ProfileInstructionStatusPending,
		ReviewResult:  reviewResult,
		FailureReason: failureReason,
	})
	if err != nil {
		t.Fatalf("CreateProfileInstruction returned error: %v", err)
	}
	if record.ID != 41 {
		t.Fatalf("instruction id = %d, want 41", record.ID)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "needs 'review'") {
		t.Fatalf("profile instruction text leaked into SQL: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 9 {
		t.Fatalf("expected nine typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 41 {
		t.Fatalf("expected id int64 param 41, got %#v", params[0])
	}
	if params[3].Kind != sqliteffi.SQLValueString || params[3].String != strings.TrimSpace(instruction) {
		t.Fatalf("expected instruction string param, got %#v", params[3])
	}
	if params[5].Kind != sqliteffi.SQLValueString || params[5].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected review string param, got %#v", params[5])
	}
	if params[6].Kind != sqliteffi.SQLValueString || params[6].String != strings.TrimSpace(failureReason) {
		t.Fatalf("expected failure string param, got %#v", params[6])
	}
}

// TestStoreFailProfileInstructionUsesTypedSQLiteParams verifies failure diagnostics are bound as typed values so reviewer output cannot reshape the UPDATE script.
// TestStoreFailProfileInstructionUsesTypedSQLiteParams 用于验证失败诊断信息会作为强类型参数绑定，避免评审输出改变 UPDATE 脚本结构。
func TestStoreFailProfileInstructionUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	reviewResult := "  {\"reason\":\"bad 'shape'\"}  "
	failureReason := "  failed '; DROP TABLE vmm_profile_instructions;  "
	if err := store.FailProfileInstruction(context.Background(), 77, failureReason, reviewResult); err != nil {
		t.Fatalf("FailProfileInstruction returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "bad 'shape'") {
		t.Fatalf("profile failure text leaked into SQL: %q", captured.GetSql())
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if !strings.Contains(captured.GetSql(), "WHERE id = ?") || !strings.Contains(captured.GetSql(), "instruction_status = ?") {
		t.Fatalf("expected failed update to guard the pending instruction status, got %q", captured.GetSql())
	}
	if len(params) != 6 {
		t.Fatalf("expected six typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != int64(logicdomain.ProfileInstructionStatusFailed) {
		t.Fatalf("expected failed status int64 param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected review string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueString || params[2].String != strings.TrimSpace(failureReason) {
		t.Fatalf("expected failure string param, got %#v", params[2])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 77 {
		t.Fatalf("expected instruction id int64 param, got %#v", params[4])
	}
	if params[5].Kind != sqliteffi.SQLValueInt64 || params[5].Int64 != int64(logicdomain.ProfileInstructionStatusPending) {
		t.Fatalf("expected pending status guard param, got %#v", params[5])
	}
}

// TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain verifies status-only read-back is not enough to recover one failed-instruction commit.
// TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain 用于验证仅凭状态相同不足以恢复一次失败指令的提交未知错误。
func TestStoreFailProfileInstructionKeepsMismatchedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return nil, errors.New("failed to commit profile instruction failed")
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_profile_instructions") {
			t.Fatalf("unexpected failed-instruction reconcile query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 77)
		if captured == nil {
			t.Fatal("expected failed-instruction update before reconcile read")
		}
		updatedMs := captured.GetParams()[3].Int64
		return &fakeQueryJSONResponse{JsonData: sqliteProfileInstructionRowJSON(77, logicdomain.ProfileInstructionStatusFailed, `{"different":true}`, "different failure", updatedMs)}, nil
	}

	err := store.FailProfileInstruction(context.Background(), 77, "expected failure", `{"expected":true}`)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected mismatched failed-instruction commit to stay outcome-uncertain, got %v", err)
	}
}

// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites verifies applied review JSON and rendered profile blobs stay out of raw SQL templates.
// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites 用于验证 applied 评审 JSON 与渲染画像 Blob 不会进入原始 SQL 模板。
func TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForFinalTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	reviewResult := "  {\"accepted\":\"value '; DROP TABLE vmm_profile_nodes;\"}  "
	renderedProfile := "  rendered profile '; DROP TABLE vmm_projects;  "
	result, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		renderedProfile,
		reviewResult,
	)
	if err != nil {
		t.Fatalf("ApplyManualProfileInstruction returned error: %v", err)
	}
	if result.InstructionID != 55 {
		t.Fatalf("instruction id = %d, want 55", result.InstructionID)
	}
	if len(requests) != 2 {
		t.Fatalf("expected applied update and rendered profile update, got %d requests", len(requests))
	}

	rendered := requests[0]
	if strings.Contains(rendered.GetSql(), "DROP TABLE") || strings.Contains(rendered.GetSql(), "rendered profile") {
		t.Fatalf("rendered profile text leaked into SQL: %q", rendered.GetSql())
	}
	if !strings.Contains(rendered.GetSql(), "UPDATE vmm_projects") {
		t.Fatalf("expected project profile update SQL, got %q", rendered.GetSql())
	}
	renderedParams := rendered.GetParams()
	if len(renderedParams) != 3 {
		t.Fatalf("expected three rendered profile params, got %d: %#v", len(renderedParams), renderedParams)
	}
	if renderedParams[0].Kind != sqliteffi.SQLValueString || renderedParams[0].String != strings.TrimSpace(renderedProfile) {
		t.Fatalf("expected rendered profile string param, got %#v", renderedParams[0])
	}
	if renderedParams[2].Kind != sqliteffi.SQLValueInt64 || renderedParams[2].Int64 != 9 {
		t.Fatalf("expected rendered profile target id param, got %#v", renderedParams[2])
	}

	applied := requests[1]
	if strings.Contains(applied.GetSql(), "DROP TABLE") || strings.Contains(applied.GetSql(), "accepted") {
		t.Fatalf("applied review text leaked into SQL: %q", applied.GetSql())
	}
	appliedParams := applied.GetParams()
	if !strings.Contains(applied.GetSql(), "WHERE id = ?") || !strings.Contains(applied.GetSql(), "instruction_status = ?") {
		t.Fatalf("expected applied update to guard the pending instruction status, got %q", applied.GetSql())
	}
	if len(appliedParams) != 5 {
		t.Fatalf("expected five applied params, got %d: %#v", len(appliedParams), appliedParams)
	}
	if appliedParams[1].Kind != sqliteffi.SQLValueString || appliedParams[1].String != strings.TrimSpace(reviewResult) {
		t.Fatalf("expected applied review string param, got %#v", appliedParams[1])
	}
	if appliedParams[3].Kind != sqliteffi.SQLValueInt64 || appliedParams[3].Int64 != 55 {
		t.Fatalf("expected applied instruction id param, got %#v", appliedParams[3])
	}
	if appliedParams[4].Kind != sqliteffi.SQLValueInt64 || appliedParams[4].Int64 != int64(logicdomain.ProfileInstructionStatusPending) {
		t.Fatalf("expected applied pending guard param, got %#v", appliedParams[4])
	}
}

// TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain verifies applied status alone cannot recover the final instruction update.
// TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain 用于验证仅凭 applied 状态不能恢复最终指令状态写入的提交未知错误。
func TestStoreApplyManualProfileInstructionKeepsMismatchedAppliedCommitUnknownUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_profile_instructions") {
			return nil, errors.New("failed to commit manual profile instruction applied")
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if !strings.Contains(req.GetSql(), "FROM vmm_profile_instructions") {
			t.Fatalf("unexpected applied-instruction reconcile query: %q", req.GetSql())
		}
		requireSQLiteInt64Params(t, req.GetParams(), 55)
		if len(requests) != 2 {
			t.Fatalf("expected rendered and applied updates before reconcile read, got %d requests", len(requests))
		}
		updatedMs := requests[1].GetParams()[2].Int64
		return &fakeQueryJSONResponse{JsonData: sqliteProfileInstructionRowJSON(55, logicdomain.ProfileInstructionStatusApplied, `{"different":true}`, "", updatedMs)}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		"rendered profile",
		`{"expected":true}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected mismatched applied-instruction commit to stay outcome-uncertain, got %v", err)
	}
}

// TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing verifies a missing durable scope row cannot leave the manual instruction in applied state.
// TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing 用于验证长期 scope 行缺失时，不会继续把手工画像指令标记为 applied。
func TestStoreApplyManualProfileInstructionStopsBeforeAppliedWhenRenderedTargetMissing(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		nil,
		nil,
		"rendered profile",
		`{"accepted_nodes":[]}`,
	)
	if err == nil {
		t.Fatal("expected missing rendered profile target to fail")
	}
	if !strings.Contains(err.Error(), "update rendered manual profile target affected 0 rows, want 1") {
		t.Fatalf("unexpected missing target error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only rendered profile update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[0].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after missing rendered target, got %q", requests[0].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain verifies post-node-write failures do not look like clean failures to the use case.
// TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain 用于验证节点写入后的后续失败不会被用例层误认为干净失败。
func TestStoreApplyManualProfileInstructionMarksPostMutationFailureUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "UPDATE vmm_projects") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:       "project prefers Go",
			Status:        logicdomain.ProfileStatusActive,
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			RefreshWeight: 64,
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      55,
			ProfileDate:   "2026-07-05",
		}},
		nil,
		"rendered profile",
		`{"accepted_nodes":[{"index":0}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-mutation rendered-profile failure to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and rendered profile update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after post-mutation failure, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain verifies stale active-node drift is surfaced as an uncertain partial write after the replacement node is already inserted.
// TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain 用于验证替代节点已插入后，如果旧 active 节点发生漂移，会以结果不确定暴露。
func TestStoreApplyManualProfileInstructionMarksSupersedeDriftUncertain(t *testing.T) {
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
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:          "project prefers Go",
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP1,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			RefreshWeight:    64,
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         55,
			ProfileDate:      "2026-07-05",
			SupersedeNodeIDs: []uint64{9, 7},
		}},
		nil,
		"rendered profile",
		`{"accepted_nodes":[{"index":0}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-insert supersede drift to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and supersede update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after supersede drift, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain verifies stale standalone retire decisions are not treated as clean failures after a new node has been inserted.
// TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain 用于验证新节点已经插入后，独立退役决策的状态漂移不会被当成干净失败。
func TestStoreApplyManualProfileInstructionMarksRetireDriftUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if strings.Contains(req.GetSql(), "WHERE profile_status = ? AND id = ?") {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	_, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:       "project prefers Go",
			Status:        logicdomain.ProfileStatusActive,
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			RefreshWeight: 64,
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      55,
			ProfileDate:   "2026-07-05",
		}},
		[]logicdomain.ProfileRetireDecision{{NodeID: 12, Reason: "stale profile"}},
		"rendered profile",
		`{"accepted_nodes":[{"index":0}],"retired_nodes":[{"node_id":12}]}`,
	)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected post-insert retire drift to be outcome-uncertain, got %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected insert and retire update before failure, got %d requests", len(requests))
	}
	if strings.Contains(requests[len(requests)-1].GetSql(), "vmm_profile_instructions") {
		t.Fatalf("did not expect applied status update after retire drift, got %q", requests[len(requests)-1].GetSql())
	}
}

// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites verifies manual profile node text and reviewer reasons stay out of executable SQL.
// TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites 用于验证手工画像节点文本与评审原因不会进入可执行 SQL。
func TestStoreApplyManualProfileInstructionUsesTypedSQLiteParamsForNodeTextWrites(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
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
		if strings.Contains(req.GetSql(), "FROM vmm_profile_nodes") && strings.Contains(req.GetSql(), "MAX(id)") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":101}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	content := "  manual node '; DROP TABLE vmm_profile_nodes; --  "
	levelReason := "  level reason '; DROP TABLE vmm_profile_nodes; --  "
	statusReason := "  supersede reason '; DROP TABLE vmm_profile_nodes; --  "
	retireReason := "  retire reason '; DROP TABLE vmm_profile_nodes; --  "
	renderedProfile := "rendered profile after node writes"
	reviewResult := `{"accepted_nodes":[{"index":0}]}`
	result, err := store.ApplyManualProfileInstruction(
		context.Background(),
		logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
		logicdomain.ProfileInstructionRecord{ID: 55},
		[]logicdomain.ProfileNodeCandidate{{
			Content:          content,
			Status:           logicdomain.ProfileStatusActive,
			Priority:         logicdomain.ProfilePriorityP1,
			ProfileLevel:     logicdomain.ProfileLevelStable,
			LevelReason:      levelReason,
			RefreshWeight:    64,
			SourceKind:       logicdomain.ProfileSourceKindManualInstruction,
			SourceID:         55,
			StatusReason:     statusReason,
			ProfileDate:      "2026-07-05",
			SupersedeNodeIDs: []uint64{9, 0, 7, 9},
		}},
		[]logicdomain.ProfileRetireDecision{
			{NodeID: 7, Reason: statusReason},
			{NodeID: 9, Reason: statusReason},
			{NodeID: 12, Reason: retireReason},
		},
		renderedProfile,
		reviewResult,
	)
	if err != nil {
		t.Fatalf("ApplyManualProfileInstruction returned error: %v", err)
	}
	if result.InstructionID != 55 || len(result.AcceptedNodes) != 1 || result.AcceptedNodes[0].ID != 101 {
		t.Fatalf("unexpected manual profile apply result: %+v", result)
	}
	if len(requests) != 5 {
		t.Fatalf("expected insert, supersede, retire, rendered, and applied requests, got %d", len(requests))
	}

	capturedSQL := sqliteExecutedSQLLog(requests)
	for _, leaked := range []string{"DROP TABLE", "manual node", "level reason", "supersede reason", "retire reason"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected profile node text %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}

	insertParams := requests[0].GetParams()
	if len(insertParams) != 17 {
		t.Fatalf("expected seventeen insert params, got %d: %#v", len(insertParams), insertParams)
	}
	if insertParams[1].Kind != sqliteffi.SQLValueNull {
		t.Fatalf("expected manual node turn id to be NULL, got %#v", insertParams[1])
	}
	if insertParams[4].Kind != sqliteffi.SQLValueString || insertParams[4].String != strings.TrimSpace(content) {
		t.Fatalf("expected content param, got %#v", insertParams[4])
	}
	if insertParams[8].Kind != sqliteffi.SQLValueString || insertParams[8].String != strings.TrimSpace(levelReason) {
		t.Fatalf("expected level reason param, got %#v", insertParams[8])
	}
	if insertParams[12].Kind != sqliteffi.SQLValueString || insertParams[12].String != strings.TrimSpace(statusReason) {
		t.Fatalf("expected status reason param, got %#v", insertParams[12])
	}

	supersedeParams := requests[1].GetParams()
	if !strings.Contains(requests[1].GetSql(), "WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (?,?)") {
		t.Fatalf("expected typed guarded supersede placeholders, got %q", requests[1].GetSql())
	}
	if strings.Contains(requests[1].GetSql(), "7") || strings.Contains(requests[1].GetSql(), "9") {
		t.Fatalf("expected supersede node ids to stay out of SQL text, got %q", requests[1].GetSql())
	}
	if len(supersedeParams) != 9 {
		t.Fatalf("expected nine supersede params, got %d: %#v", len(supersedeParams), supersedeParams)
	}
	if supersedeParams[1].Kind != sqliteffi.SQLValueInt64 || supersedeParams[1].Int64 != 101 {
		t.Fatalf("expected superseded_by_id param 101, got %#v", supersedeParams[1])
	}
	if supersedeParams[2].Kind != sqliteffi.SQLValueString || supersedeParams[2].String != strings.TrimSpace(statusReason) {
		t.Fatalf("expected supersede reason param, got %#v", supersedeParams[2])
	}
	requireSQLiteInt64Params(t, []sqliteffi.SQLValue{supersedeParams[0]}, int64(logicdomain.ProfileStatusSuperseded))
	if supersedeParams[3].Kind != sqliteffi.SQLValueInt64 || supersedeParams[3].Int64 <= 0 {
		t.Fatalf("expected supersede updated timestamp param, got %#v", supersedeParams[3])
	}
	requireSQLiteInt64Params(t, supersedeParams[4:], int64(logicdomain.ProfileStatusActive), int64(logicdomain.ProfileTypeProject), 9, 7, 9)

	retireParams := requests[2].GetParams()
	if len(retireParams) != 5 {
		t.Fatalf("expected five retire params, got %d: %#v", len(retireParams), retireParams)
	}
	if retireParams[1].Kind != sqliteffi.SQLValueString || retireParams[1].String != strings.TrimSpace(retireReason) {
		t.Fatalf("expected retire reason param, got %#v", retireParams[1])
	}
	if retireParams[4].Kind != sqliteffi.SQLValueInt64 || retireParams[4].Int64 != 12 {
		t.Fatalf("expected retire node id param, got %#v", retireParams[4])
	}
	if got := countSQLiteStringParam(requests, strings.TrimSpace(statusReason)); got != 2 {
		t.Fatalf("expected status reason to be bound twice, got %d", got)
	}
}

// TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded verifies supersede decisions are not applied again by SQLite's standalone retire loop.
// TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded 用于验证 supersede 决策不会被 SQLite 独立退役循环再次落库。
func TestStandaloneSQLiteProfileRetireDecisionsSkipsSuperseded(t *testing.T) {
	retired := []logicdomain.ProfileRetireDecision{
		{NodeID: 7, Reason: "replaced"},
		{NodeID: 12, Reason: "explicit retire"},
	}
	standalone := standaloneSQLiteProfileRetireDecisions(retired, map[uint64]struct{}{7: {}})
	if len(standalone) != 1 || standalone[0].NodeID != 12 {
		t.Fatalf("expected only standalone retire decision to remain, got %+v", standalone)
	}
}
