// retention_store_test.go verifies retention SQL helpers still archive, purge, and queue work correctly after the storage adapter became local FFI only.
// retention_store_test.go 用于验证在存储适配器完全切到本地 FFI 后，回收、清理与队列 SQL 助手仍能正确归档、清理并入队。
package vldb_sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRecycleColdMemoriesCopiesRowsIntoTrash verifies recycle writes both trash tables and removes hot rows without touching the retired legacy FTS mirror.
// TestRecycleColdMemoriesCopiesRowsIntoTrash 用于验证回收会写入两张回收站表并删除热表数据，同时不会触碰已退役的旧 FTS 镜像表。
func TestRecycleColdMemoriesCopiesRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段已变更","details":"项目阶段从 A 进入 B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[
{"memory_id":11,"context_key":"phase","context_value":"delivery","support_count":1,"rebuttal_count":0,"last_supported_timestamp":1,"last_rebutted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:         32,
		RecycledAt:    time.Unix(100, 0).UTC(),
		RecycleReason: "terminal memory recycle",
	})
	if err != nil {
		t.Fatalf("RecycleColdMemories returned error: %v", err)
	}
	if result.BatchID != 7 || result.RecycledMemoryCount != 1 || result.RecycledContextCount != 1 {
		t.Fatalf("unexpected recycle result: %+v", result)
	}
	for _, fragment := range []string{"INSERT INTO vmm_memory_nodes_trash", "INSERT INTO vmm_memory_context_edges_trash", "DELETE FROM vmm_memory_nodes", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
	if strings.Contains(capturedSQL, "vmm_memory_nodes_fts") {
		t.Fatalf("expected recycle sql to stop referencing retired legacy FTS table, got %q", capturedSQL)
	}
}

// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash verifies idle-session recycle moves stale memories and eligible turns into the trash tables.
// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash 用于验证 idle-session 回收会把陈旧记忆和可归档 turn 迁入回收站。
func TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &fakeQueryJSONResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             8,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     "idle session recycle",
	})
	if err != nil {
		t.Fatalf("RecycleIdleSessions returned error: %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 {
		t.Fatalf("unexpected batch ids: %v", result.BatchIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 1 {
		t.Fatalf("unexpected idle-session recycle counts: %+v", result)
	}
	for _, fragment := range []string{"INSERT INTO vmm_memory_nodes_trash", "INSERT INTO vmm_turn_records_trash", "DELETE FROM vmm_turn_records", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected idle-session recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows verifies the prefilter keeps both stale-memory and old-turn existence checks.
// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows 用于验证 session 预过滤会同时包含陈旧记忆与旧 turn 的存在性检查。
func TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows(t *testing.T) {
	clause, args := buildSQLiteIdleSessionCandidateAvailabilityClause(12345, 8)
	if !strings.Contains(clause, "FROM vmm_memory_nodes m") ||
		!strings.Contains(clause, "FROM vmm_turn_records tr") ||
		!strings.Contains(clause, "NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)") {
		t.Fatalf("unexpected candidate availability clause: %q", clause)
	}
	if len(args) != 5 {
		t.Fatalf("candidate availability args len = %d, want 5", len(args))
	}
}

// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata verifies purge deletes every trash table plus recycle-batch metadata.
// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata 用于验证 purge 会删除所有回收站表及批次元数据。
func TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &fakeQueryJSONResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	result, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err != nil {
		t.Fatalf("PurgeExpiredTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.PurgedMemoryCount != 2 || result.PurgedContextCount != 3 || result.PurgedTurnCount != 4 {
		t.Fatalf("unexpected purge result: %+v", result)
	}
	for _, fragment := range []string{"DELETE FROM vmm_memory_context_edges_trash", "DELETE FROM vmm_memory_nodes_trash", "DELETE FROM vmm_turn_records_trash", "DELETE FROM vmm_recycle_batches", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected purge sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestEnqueueVectorGCJobsPersistsRetryRows verifies failed sidecar deletes are persisted into the vector-gc queue table.
// TestEnqueueVectorGCJobsPersistsRetryRows 用于验证失败的旁路删除会被持久化到 vector-gc 队列表。
func TestEnqueueVectorGCJobsPersistsRetryRows(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1", "vec-2"},
		NextRunAt: time.Unix(500, 0).UTC(),
	}); err != nil {
		t.Fatalf("EnqueueVectorGCJobs returned error: %v", err)
	}
	for _, fragment := range []string{"INSERT OR IGNORE INTO vmm_vector_gc_jobs", "'vec-1'", "'vec-2'", "'retention_recycle_vector_delete'", "BEGIN IMMEDIATE;", "COMMIT;"} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected enqueue sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestSQLiteVectorGCJobsClaimRetryAndComplete verifies the persistent vector-gc queue still supports claim, retry, and complete flows.
// TestSQLiteVectorGCJobsClaimRetryAndComplete 用于验证持久化 vector-gc 队列仍支持领取、重试与完成流转。
func TestSQLiteVectorGCJobsClaimRetryAndComplete(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_vector_gc_jobs") {
			return &fakeQueryJSONResponse{JsonData: `[
{"id":51,"batch_id":7,"vector_id":"vec-retry-1","job_type":"retention_recycle_vector_delete","attempt_count":2,"next_run_timestamp":1000,"claimed_timestamp":0,"completed_timestamp":0,"last_error":"old error","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, req.GetSql())
		return &fakeExecuteResponse{Success: true}, nil
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
	if len(executedSQL) != 3 {
		t.Fatalf("executed sql count = %d, want 3", len(executedSQL))
	}
}
