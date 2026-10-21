// Behavioral tests grouped by recycle jobs test.
// SQLite 适配层回收作业行为测试，沿用同包夹具验证对应业务链路。
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

// TestClaimPendingRecycleJobsUsesUpdateReturning verifies recycle-job leasing selects and updates due rows in one typed SQLite statement.
// TestClaimPendingRecycleJobsUsesUpdateReturning 用于验证回收任务领取会在一条强类型 SQLite 语句中选择并更新到期行。
func TestClaimPendingRecycleJobsUsesUpdateReturning(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	claimUntil := time.Unix(700, 0).UTC()
	dueBefore := time.Unix(600, 0).UTC()
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":902,"session_id":22,"project_id":10,"job_type":"cold_turn","attempt_count":1,"next_run_timestamp":700000,"claimed_timestamp":699000,"last_error":"","created_timestamp":1000,"updated_timestamp":699000},
{"id":901,"session_id":21,"project_id":9,"job_type":"cold_turn","attempt_count":0,"next_run_timestamp":700000,"claimed_timestamp":699000,"last_error":"","created_timestamp":900,"updated_timestamp":699000}
]`}, nil
	}

	jobs, err := store.ClaimPendingRecycleJobs(context.Background(), logicdomain.RecycleJobTypeColdTurn, dueBefore, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingRecycleJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 901 || jobs[1].ID != 902 {
		t.Fatalf("expected claimed jobs sorted by id, got %+v", jobs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	for _, fragment := range []string{"UPDATE vmm_recycle_jobs", "WHERE id IN (", "ORDER BY next_run_timestamp ASC, id ASC", "LIMIT ?", "RETURNING id, session_id, project_id"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected claim sql to contain %q, got %q", fragment, sql)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "ROLLBACK;"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected claim sql to stay single-statement, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 6 {
		t.Fatalf("expected six claim params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 <= 0 || params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 != params[0].Int64 {
		t.Fatalf("expected claimed and updated timestamps to share one positive claim time, got %#v and %#v", params[0], params[2])
	}
	if params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != claimUntil.UnixMilli() {
		t.Fatalf("expected claim-until timestamp param, got %#v", params[1])
	}
	if params[3].Kind != sqliteffi.SQLValueString || params[3].String != logicdomain.RecycleJobTypeColdTurn {
		t.Fatalf("expected job type param, got %#v", params[3])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != dueBefore.UnixMilli() {
		t.Fatalf("expected due-before timestamp param, got %#v", params[4])
	}
	if params[5].Kind != sqliteffi.SQLValueInt64 || params[5].Int64 != 8 {
		t.Fatalf("expected limit param, got %#v", params[5])
	}
}

// TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies claim failures at SQLite's commit boundary are surfaced as ambiguous queue mutations.
// TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证 SQLite 提交边界上的领取失败会作为不确定队列变更向上暴露。
func TestClaimPendingRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return nil, errors.New("failed to commit recycle claim update")
	}

	_, err := store.ClaimPendingRecycleJobs(context.Background(), logicdomain.RecycleJobTypeColdTurn, time.Unix(600, 0).UTC(), time.Unix(700, 0).UTC(), 8)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain claim error, got %v", err)
	}
	if !strings.Contains(err.Error(), "claim sqlite pending recycle jobs") || !strings.Contains(err.Error(), "failed to commit recycle claim update") {
		t.Fatalf("unexpected claim error text: %v", err)
	}
}

// TestCompleteRecycleJobsUsesTypedIDParams verifies completed recycle-job cleanup deletes normalized ids through typed placeholders.
// TestCompleteRecycleJobsUsesTypedIDParams 用于验证已完成回收任务清理会通过强类型占位符删除规范化后的 id。
func TestCompleteRecycleJobsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	if err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 0, 43, 44}, time.Unix(800, 0).UTC()); err != nil {
		t.Fatalf("CompleteRecycleJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "DELETE FROM vmm_recycle_jobs") || !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder delete SQL, got %q", sql)
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "43", "44"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected completed job ids to stay out of SQL text, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two completed job id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 43 ||
		params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != 44 {
		t.Fatalf("expected normalized completed job ids 43 and 44, got %#v", params)
	}
}

// TestCompleteRecycleJobsRejectsPartialDelete verifies job completion cannot report success when the queue delete misses claimed job ids.
// TestCompleteRecycleJobsRejectsPartialDelete 用于验证队列删除未命中全部已领取 job id 时，任务完成不能报告成功。
func TestCompleteRecycleJobsRejectsPartialDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 43}, time.Unix(800, 0).UTC())
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain complete recycle jobs error, got %v", err)
	}
}

// TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the recycle-job completion outcome uncertain.
// TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留回收任务完成结果不确定语义。
func TestCompleteRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite transaction")
	}

	err := store.CompleteRecycleJobs(context.Background(), []uint64{44, 43}, time.Unix(800, 0).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown complete recycle jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "complete sqlite recycle jobs") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected complete recycle jobs commit error: %v", err)
	}
}

// TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams verifies recycle-job enqueue writes keep the stable job type out of executable SQL while avoiding unsupported cross-call transaction control.
// TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams 用于验证回收任务入队会把稳定 job type 留在强类型参数中，并避免不受支持的跨调用事务控制。
func TestEnqueueColdTurnRecycleJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	// Capture the insert writes because enqueue emits one typed INSERT per candidate session.
	// 捕获入队写入，因为入队会为每个候选 session 发出一条强类型 INSERT。
	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			},{
				"id":12,
				"session_key":"session-b",
				"user_id":8,
				"team_id":2,
				"space_id":3,
				"project_id":10,
				"turn_count":24,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":3
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	scannedAt := time.Unix(100, 0).UTC()
	nextRunAt := time.Unix(120, 0).UTC()
	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             2,
		ScannedAt:         scannedAt,
		NextRunAt:         nextRunAt,
		TurnHotWindowSize: 4,
	})
	if err != nil {
		t.Fatalf("EnqueueColdTurnRecycleJobs returned error: %v", err)
	}
	if enqueued != 2 {
		t.Fatalf("expected two enqueued jobs, got %d", enqueued)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two typed insert requests, got %d", len(requests))
	}
	requireNoSQLiteTransactionControl(t, requests)

	capturedSQL := sqliteExecutedSQLLog(requests)
	if strings.Contains(capturedSQL, logicdomain.RecycleJobTypeColdTurn) {
		t.Fatalf("recycle job type leaked into SQL: %q", capturedSQL)
	}
	if strings.Contains(capturedSQL, "INSERT OR IGNORE") {
		t.Fatalf("recycle job enqueue should not hide non-target constraint failures: %q", capturedSQL)
	}
	if !strings.Contains(capturedSQL, "ON CONFLICT(session_id, job_type) DO UPDATE") {
		t.Fatalf("recycle job enqueue should refresh equivalent existing jobs: %q", capturedSQL)
	}
	for _, request := range requests {
		if strings.TrimSpace(request.GetParamsJson()) != "" {
			t.Fatalf("expected typed params only, got params_json=%q", request.GetParamsJson())
		}
	}
	if got := countSQLiteStringParam(requests, logicdomain.RecycleJobTypeColdTurn); got != 2 {
		t.Fatalf("expected cold-turn job type param twice, got %d", got)
	}

	firstInsertParams := requests[0].GetParams()
	if len(firstInsertParams) != 8 {
		t.Fatalf("expected eight first insert params, got %d: %#v", len(firstInsertParams), firstInsertParams)
	}
	if firstInsertParams[0].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[0].Int64 != 900 {
		t.Fatalf("expected first job id 900, got %#v", firstInsertParams[0])
	}
	if firstInsertParams[1].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[1].Int64 != 11 {
		t.Fatalf("expected first session id 11, got %#v", firstInsertParams[1])
	}
	if firstInsertParams[3].Kind != sqliteffi.SQLValueString || firstInsertParams[3].String != logicdomain.RecycleJobTypeColdTurn {
		t.Fatalf("expected first job type param, got %#v", firstInsertParams[3])
	}
	if firstInsertParams[4].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[4].Int64 != nextRunAt.UnixMilli() {
		t.Fatalf("expected first next_run_timestamp param, got %#v", firstInsertParams[4])
	}
	if firstInsertParams[5].Kind != sqliteffi.SQLValueString || firstInsertParams[5].String != "" {
		t.Fatalf("expected first last_error empty string param, got %#v", firstInsertParams[5])
	}
	if firstInsertParams[6].Kind != sqliteffi.SQLValueInt64 || firstInsertParams[6].Int64 != scannedAt.UnixMilli() {
		t.Fatalf("expected first created timestamp param, got %#v", firstInsertParams[6])
	}

	secondInsertParams := requests[1].GetParams()
	if len(secondInsertParams) != 8 {
		t.Fatalf("expected eight second insert params, got %d: %#v", len(secondInsertParams), secondInsertParams)
	}
	if secondInsertParams[0].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[0].Int64 != 901 {
		t.Fatalf("expected second job id 901, got %#v", secondInsertParams[0])
	}
	if secondInsertParams[1].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[1].Int64 != 12 {
		t.Fatalf("expected second session id 12, got %#v", secondInsertParams[1])
	}
	if secondInsertParams[2].Kind != sqliteffi.SQLValueInt64 || secondInsertParams[2].Int64 != 10 {
		t.Fatalf("expected second project id 10, got %#v", secondInsertParams[2])
	}
}

// TestEnqueueColdTurnRecycleJobsRejectsPartialInsert verifies enqueue counts preserve earlier durable jobs without counting the candidate that SQLite neither inserted nor refreshed.
// TestEnqueueColdTurnRecycleJobsRejectsPartialInsert 用于验证入队数量会保留更早已持久化的任务，但不会计入 SQLite 既未插入也未刷新的候选任务。
func TestEnqueueColdTurnRecycleJobsRejectsPartialInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	requests := []*fakeExecuteRequest{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		requests = append(requests, req)
		if len(requests) == 2 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			},{
				"id":12,
				"session_key":"session-b",
				"user_id":8,
				"team_id":2,
				"space_id":3,
				"project_id":10,
				"turn_count":24,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":3
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             2,
		ScannedAt:         time.Unix(100, 0).UTC(),
		NextRunAt:         time.Unix(120, 0).UTC(),
		TurnHotWindowSize: 4,
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain enqueue recycle jobs error, got %v", err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueued count = %d, want 1 confirmed durable job before drift", enqueued)
	}
	if len(requests) != 2 {
		t.Fatalf("expected enqueue to stop after the second insert drifted, got %d requests", len(requests))
	}
}

// TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain verifies the first autocommit enqueue failure still preserves commit-boundary ambiguity.
// TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain 用于验证首条自动提交入队失败仍会保留提交边界不确定语义。
func TestEnqueueColdTurnRecycleJobsMarksInitialCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := req.GetSql()
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[{
				"id":11,
				"session_key":"session-a",
				"user_id":7,
				"team_id":2,
				"space_id":3,
				"project_id":9,
				"turn_count":20,
				"last_summarized_id":0,
				"last_compacted_turn_id":0,
				"summarize_content":"",
				"summarize_budget":0,
				"last_extract_observed_timestamp":0,
				"last_extract_completed_timestamp":0,
				"last_compacted_timestamp":0,
				"created_timestamp":1,
				"updated_timestamp":2
			}]`}, nil
		case strings.Contains(sql, "MAX(id)") && strings.Contains(sql, "vmm_recycle_jobs"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":900}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit cold-turn enqueue insert")
	}

	enqueued, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             1,
		ScannedAt:         time.Unix(100, 0).UTC(),
		NextRunAt:         time.Unix(120, 0).UTC(),
		TurnHotWindowSize: 4,
	})
	if enqueued != 0 {
		t.Fatalf("enqueued count = %d, want 0 before the first commit-unknown result", enqueued)
	}
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain initial enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "enqueue sqlite cold-turn recycle job 1") || !strings.Contains(err.Error(), "failed to commit cold-turn enqueue insert") {
		t.Fatalf("unexpected initial enqueue error: %v", err)
	}
}

// TestRetryRecycleJobsUsesTypedSQLiteParams verifies retry diagnostics and normalized job ids are bound as typed params.
// TestRetryRecycleJobsUsesTypedSQLiteParams 用于验证重试诊断信息和规范化后的 job id 都会作为强类型参数绑定。
func TestRetryRecycleJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	lastError := "  recycle failed '; DROP TABLE vmm_recycle_jobs;  "
	if err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21, 22}, time.Unix(9, 0).UTC(), lastError); err != nil {
		t.Fatalf("RetryRecycleJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "recycle failed") {
		t.Fatalf("retry recycle last_error leaked into SQL: %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder id list in SQL, got %q", captured.GetSql())
	}
	for _, forbidden := range []string{"BEGIN", "COMMIT", "21", "22"} {
		if strings.Contains(captured.GetSql(), forbidden) {
			t.Fatalf("retry recycle SQL should keep ids and transaction control out of text, got %q", captured.GetSql())
		}
	}
	if strings.Contains(captured.GetSql(), "ROLLBACK") {
		t.Fatalf("retry recycle should stay a single atomic UPDATE, got %q", captured.GetSql())
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != time.Unix(9, 0).UTC().UnixMilli() {
		t.Fatalf("expected next_run_timestamp param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(lastError) {
		t.Fatalf("expected last_error string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 == 0 {
		t.Fatalf("expected updated_timestamp int64 param, got %#v", params[2])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != 21 ||
		params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 22 {
		t.Fatalf("expected normalized retry job ids 21 and 22, got %#v", params[3:])
	}
}

// TestRetryRecycleJobsRejectsPartialUpdate verifies failed job reschedule cannot silently miss claimed job ids.
// TestRetryRecycleJobsRejectsPartialUpdate 用于验证失败任务重调度不能静默漏掉已领取 job id。
func TestRetryRecycleJobsRejectsPartialUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21}, time.Unix(9, 0).UTC(), "failed")
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain retry recycle jobs error, got %v", err)
	}
}

// TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the retry update outcome uncertain.
// TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留回收任务重试更新结果不确定语义。
func TestRetryRecycleJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown after retry update")
	}

	err := store.RetryRecycleJobs(context.Background(), []uint64{22, 21}, time.Unix(9, 0).UTC(), "failed")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown retry recycle jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry sqlite recycle jobs") || !strings.Contains(err.Error(), "commit outcome unknown") {
		t.Fatalf("unexpected retry recycle jobs commit error: %v", err)
	}
}
