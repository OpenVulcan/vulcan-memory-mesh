// Behavioral tests grouped by vector gc jobs test.
// SQLite 适配层向量清理补偿作业行为测试，沿用同包夹具验证对应业务链路。
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

// TestEnqueueVectorGCJobsPersistsRetryRows verifies failed sidecar deletes are persisted into the vector-gc queue table.
// TestEnqueueVectorGCJobsPersistsRetryRows 用于验证失败的旁路删除会被持久化到 vector-gc 队列表。
func TestEnqueueVectorGCJobsPersistsRetryRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 4)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	vectorID := "vec-2'; DROP TABLE vmm_vector_gc_jobs;--"
	jobType := "  custom_gc '; DROP TABLE vmm_vector_gc_jobs;  "
	if err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   jobType,
		VectorIDs: []string{"vec-1", " " + vectorID + " ", "vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	}); err != nil {
		t.Fatalf("EnqueueVectorGCJobs returned error: %v", err)
	}
	requireNoSQLiteTransactionControl(t, executed)
	if len(executed) != 2 {
		t.Fatalf("expected two typed insert requests, got %d", len(executed))
	}
	capturedSQL := sqliteExecutedSQLLog(executed)
	for _, fragment := range []string{"INSERT INTO vmm_vector_gc_jobs", "ON CONFLICT(vector_id, job_type, batch_id) DO UPDATE"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected enqueue sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	for _, leaked := range []string{"DROP TABLE", "vec-1", "custom_gc"} {
		if strings.Contains(capturedSQL, leaked) {
			t.Fatalf("expected enqueue text value %q to stay out of SQL text, got %q", leaked, capturedSQL)
		}
	}
	if got := countSQLiteStringParam(executed, "vec-1"); got != 1 {
		t.Fatalf("expected deduped vec-1 param once, got %d", got)
	}
	if got := countSQLiteStringParam(executed, vectorID); got != 1 {
		t.Fatalf("expected malicious vector id param once, got %d", got)
	}
	if got := countSQLiteStringParam(executed, strings.TrimSpace(jobType)); got != 2 {
		t.Fatalf("expected job type param once per vector id, got %d", got)
	}
}

// TestEnqueueVectorGCJobsRejectsPartialInsert verifies failed vector cleanup compensation reports the confirmed retry rows before a later row is neither inserted nor refreshed.
// TestEnqueueVectorGCJobsRejectsPartialInsert 用于验证向量清理补偿在后续重试行既未插入也未刷新时，会报告此前已确认的重试行。
func TestEnqueueVectorGCJobsRejectsPartialInsert(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		if len(executed) == 2 {
			return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
		}
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1", "vec-2"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector gc enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "after 1 confirmed jobs") {
		t.Fatalf("expected partial enqueue count in error, got %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("expected enqueue to stop after second insert drifted, got %d requests", len(executed))
	}
}

// TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary verifies a first-row enqueue drift without any mutation is not reported as an uncertain durable queue state.
// TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary 用于验证首条入队未产生任何突变时，不会被报告为持久队列状态不确定。
func TestEnqueueVectorGCJobsRejectsInitialInsertDriftAsOrdinary(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executed := make([]*fakeExecuteRequest, 0, 1)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executed = append(executed, req)
		return &fakeExecuteResponse{Success: true, RowsChanged: 0}, nil
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if err == nil {
		t.Fatal("expected initial vector gc enqueue drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect initial vector gc enqueue drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "after 0 confirmed jobs") {
		t.Fatalf("expected zero confirmed job count in error, got %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("expected enqueue to stop after first insert drifted, got %d requests", len(executed))
	}
}

// TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain verifies first-row vector retry enqueue failures preserve commit-boundary ambiguity.
// TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain 用于验证首条 vector 重试入队失败会保留提交边界不确定语义。
func TestEnqueueVectorGCJobsMarksInitialCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while enqueuing vector gc job")
	}

	err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1"},
		NextRunAt: time.Unix(500, 0).UTC(),
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain initial vector enqueue error, got %v", err)
	}
	if !strings.Contains(err.Error(), "enqueue sqlite vector gc job 1 after 0 confirmed jobs") || !strings.Contains(err.Error(), "commit outcome unknown while enqueuing vector gc job") {
		t.Fatalf("unexpected initial vector enqueue error: %v", err)
	}
}

// TestSQLiteVectorGCJobsClaimRetryAndComplete verifies the persistent vector-gc queue still supports claim, retry, and complete flows.
// TestSQLiteVectorGCJobsClaimRetryAndComplete 用于验证持久化 vector-gc 队列仍支持领取、重试与完成流转。
func TestSQLiteVectorGCJobsClaimRetryAndComplete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	querySQL := make([]string, 0, 1)
	executedSQL := make([]string, 0, 2)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		querySQL = append(querySQL, sql)
		if strings.Contains(sql, "UPDATE vmm_vector_gc_jobs") && strings.Contains(sql, "RETURNING") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":51,"batch_id":7,"vector_id":"vec-retry-1","job_type":"retention_recycle_vector_delete","attempt_count":2,"next_run_timestamp":1000,"claimed_timestamp":0,"completed_timestamp":0,"last_error":"old error","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, req.GetSql())
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	jobs, err := store.ClaimPendingVectorGCJobs(context.Background(), time.Unix(2, 0).UTC(), time.Unix(5, 0).UTC(), 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 51 || jobs[0].VectorID != "vec-retry-1" {
		t.Fatalf("unexpected claimed jobs: %+v", jobs)
	}
	if err := store.RetryVectorGCJobs(context.Background(), []uint64{51}, time.Unix(9, 0).UTC(), "retry failed"); err != nil {
		t.Fatalf("RetryVectorGCJobs returned error: %v", err)
	}
	if err := store.CompleteVectorGCJobs(context.Background(), []uint64{51}, time.Unix(12, 0).UTC()); err != nil {
		t.Fatalf("CompleteVectorGCJobs returned error: %v", err)
	}
	if len(querySQL) != 1 || !strings.Contains(querySQL[0], "UPDATE vmm_vector_gc_jobs") {
		t.Fatalf("expected vector gc claim to use one update-returning query, got %#v", querySQL)
	}
	if len(executedSQL) != 2 {
		t.Fatalf("executed sql count = %d, want 2", len(executedSQL))
	}
}

// TestClaimPendingVectorGCJobsUsesUpdateReturning verifies vector-gc leasing selects and updates due rows in one typed SQLite statement.
// TestClaimPendingVectorGCJobsUsesUpdateReturning 用于验证 vector-gc 任务领取会在一条强类型 SQLite 语句中选择并更新到期行。
func TestClaimPendingVectorGCJobsUsesUpdateReturning(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeQueryRequest
	claimUntil := time.Unix(700, 0).UTC()
	dueBefore := time.Unix(600, 0).UTC()
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		captured = req
		return &fakeQueryJSONResponse{JsonData: `[
{"id":52,"batch_id":7,"vector_id":"vec-52","job_type":"retention_recycle_vector_delete","attempt_count":1,"next_run_timestamp":700000,"claimed_timestamp":699000,"completed_timestamp":0,"last_error":"","created_timestamp":1000,"updated_timestamp":699000},
{"id":51,"batch_id":7,"vector_id":"vec-51","job_type":"retention_recycle_vector_delete","attempt_count":0,"next_run_timestamp":700000,"claimed_timestamp":699000,"completed_timestamp":0,"last_error":"","created_timestamp":900,"updated_timestamp":699000}
]`}, nil
	}

	jobs, err := store.ClaimPendingVectorGCJobs(context.Background(), dueBefore, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 51 || jobs[1].ID != 52 {
		t.Fatalf("expected claimed vector jobs sorted by id, got %+v", jobs)
	}
	if captured == nil {
		t.Fatal("expected QueryJSON request to be captured")
	}
	sql := captured.GetSql()
	for _, fragment := range []string{"UPDATE vmm_vector_gc_jobs", "WHERE completed_timestamp = 0", "ORDER BY next_run_timestamp ASC, id ASC", "LIMIT ?", "RETURNING id, batch_id, vector_id"} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected vector claim sql to contain %q, got %q", fragment, sql)
		}
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "ROLLBACK;"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected vector claim sql to stay single-statement, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five vector claim params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 <= 0 || params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 != params[0].Int64 {
		t.Fatalf("expected claimed and updated timestamps to share one positive claim time, got %#v and %#v", params[0], params[2])
	}
	if params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != claimUntil.UnixMilli() {
		t.Fatalf("expected claim-until timestamp param, got %#v", params[1])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != dueBefore.UnixMilli() {
		t.Fatalf("expected due-before timestamp param, got %#v", params[3])
	}
	if params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 8 {
		t.Fatalf("expected limit param, got %#v", params[4])
	}
}

// TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies vector-gc claim failures at SQLite's commit boundary preserve ambiguity for retry orchestration.
// TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证 vector-gc 领取在 SQLite 提交边界失败时会为重试编排保留结果不确定语义。
func TestClaimPendingVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		return nil, errors.New("commit outcome unknown after vector claim")
	}

	_, err := store.ClaimPendingVectorGCJobs(context.Background(), time.Unix(600, 0).UTC(), time.Unix(700, 0).UTC(), 8)
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector claim error, got %v", err)
	}
	if !strings.Contains(err.Error(), "claim sqlite pending vector gc jobs") || !strings.Contains(err.Error(), "commit outcome unknown after vector claim") {
		t.Fatalf("unexpected vector claim error text: %v", err)
	}
}

// TestCompleteVectorGCJobsUsesTypedIDParams verifies completed vector-gc cleanup deletes normalized ids through typed placeholders.
// TestCompleteVectorGCJobsUsesTypedIDParams 用于验证已完成 vector-gc 任务清理会通过强类型占位符删除规范化后的 id。
func TestCompleteVectorGCJobsUsesTypedIDParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	if err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 0, 51, 52}, time.Unix(800, 0).UTC()); err != nil {
		t.Fatalf("CompleteVectorGCJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	sql := captured.GetSql()
	if !strings.Contains(sql, "DELETE FROM vmm_vector_gc_jobs") || !strings.Contains(sql, "WHERE id IN (?,?)") {
		t.Fatalf("expected placeholder vector delete SQL, got %q", sql)
	}
	for _, forbidden := range []string{"BEGIN IMMEDIATE;", "COMMIT;", "51", "52"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expected completed vector job ids to stay out of SQL text, got %q", sql)
		}
	}
	if strings.TrimSpace(captured.GetParamsJson()) != "" {
		t.Fatalf("expected typed params only, got params_json=%q", captured.GetParamsJson())
	}
	params := captured.GetParams()
	if len(params) != 2 {
		t.Fatalf("expected two completed vector job id params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != 51 ||
		params[1].Kind != sqliteffi.SQLValueInt64 || params[1].Int64 != 52 {
		t.Fatalf("expected normalized completed vector job ids 51 and 52, got %#v", params)
	}
}

// TestCompleteVectorGCJobsRejectsPartialDelete verifies vector-GC completion cannot silently miss claimed retry jobs.
// TestCompleteVectorGCJobsRejectsPartialDelete 用于验证向量 GC 完成流程不能静默漏删已领取重试任务。
func TestCompleteVectorGCJobsRejectsPartialDelete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(800, 0).UTC())
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain complete vector gc jobs error, got %v", err)
	}
}

// TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the vector-GC completion outcome uncertain.
// TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留向量 GC 完成结果不确定语义。
func TestCompleteVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite vector gc completion")
	}

	err := store.CompleteVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(800, 0).UTC())
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown complete vector gc jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "complete sqlite vector gc jobs") || !strings.Contains(err.Error(), "failed to commit") {
		t.Fatalf("unexpected complete vector gc jobs commit error: %v", err)
	}
}

// TestRetryVectorGCJobsUsesTypedSQLiteParams verifies vector retry diagnostics and normalized job ids are bound as typed params.
// TestRetryVectorGCJobsUsesTypedSQLiteParams 用于验证向量重试诊断信息和规范化后的 job id 都会作为强类型参数绑定。
func TestRetryVectorGCJobsUsesTypedSQLiteParams(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var captured *fakeExecuteRequest
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		captured = req
		return &fakeExecuteResponse{Success: true, RowsChanged: 2}, nil
	}

	lastError := "  vector gc failed '; DROP TABLE vmm_vector_gc_jobs;  "
	if err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51, 52}, time.Unix(11, 0).UTC(), lastError); err != nil {
		t.Fatalf("RetryVectorGCJobs returned error: %v", err)
	}
	if captured == nil {
		t.Fatal("expected ExecuteScript request to be captured")
	}
	if strings.Contains(captured.GetSql(), "DROP TABLE") || strings.Contains(captured.GetSql(), "vector gc failed") {
		t.Fatalf("retry vector last_error leaked into SQL: %q", captured.GetSql())
	}
	if !strings.Contains(captured.GetSql(), "WHERE id IN (?,?)") || !strings.Contains(captured.GetSql(), "completed_timestamp = 0") {
		t.Fatalf("expected normalized vector retry predicate in SQL, got %q", captured.GetSql())
	}
	for _, forbidden := range []string{"BEGIN", "COMMIT", "ROLLBACK", "51", "52"} {
		if strings.Contains(captured.GetSql(), forbidden) {
			t.Fatalf("retry vector SQL should keep ids and transaction control out of text, got %q", captured.GetSql())
		}
	}
	params := captured.GetParams()
	if len(params) != 5 {
		t.Fatalf("expected five typed params, got %d: %#v", len(params), params)
	}
	if params[0].Kind != sqliteffi.SQLValueInt64 || params[0].Int64 != time.Unix(11, 0).UTC().UnixMilli() {
		t.Fatalf("expected next_run_timestamp param, got %#v", params[0])
	}
	if params[1].Kind != sqliteffi.SQLValueString || params[1].String != strings.TrimSpace(lastError) {
		t.Fatalf("expected last_error string param, got %#v", params[1])
	}
	if params[2].Kind != sqliteffi.SQLValueInt64 || params[2].Int64 == 0 {
		t.Fatalf("expected updated_timestamp int64 param, got %#v", params[2])
	}
	if params[3].Kind != sqliteffi.SQLValueInt64 || params[3].Int64 != 51 ||
		params[4].Kind != sqliteffi.SQLValueInt64 || params[4].Int64 != 52 {
		t.Fatalf("expected normalized retry vector job ids 51 and 52, got %#v", params[3:])
	}
}

// TestRetryVectorGCJobsRejectsPartialUpdate verifies vector-GC retry cannot silently miss claimed retry jobs.
// TestRetryVectorGCJobsRejectsPartialUpdate 用于验证向量 GC 重试流程不能静默漏掉已领取重试任务。
func TestRetryVectorGCJobsRejectsPartialUpdate(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1}, nil
	}

	err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(11, 0).UTC(), "failed")
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain retry vector gc jobs error, got %v", err)
	}
}

// TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain verifies commit-boundary failures keep the vector-GC retry update outcome uncertain.
// TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain 用于验证提交边界失败会保留向量 GC 重试更新结果不确定语义。
func TestRetryVectorGCJobsMarksCommitUnknownAsOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown after vector gc retry")
	}

	err := store.RetryVectorGCJobs(context.Background(), []uint64{52, 51}, time.Unix(11, 0).UTC(), "failed")
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected commit-unknown retry vector gc jobs error to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry sqlite vector gc jobs") || !strings.Contains(err.Error(), "commit outcome unknown") {
		t.Fatalf("unexpected retry vector gc jobs commit error: %v", err)
	}
}
