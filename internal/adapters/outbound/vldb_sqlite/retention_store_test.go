// retention_store_test.go verifies SQLite retention helpers build the expected recycle and purge statements without regressing to partial hot-table deletes.
// retention_store_test.go 用于验证 SQLite retention 辅助逻辑会构造预期的回收与 purge 语句，避免回归成只删热表的半实现。
package vldb_sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
)

// TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS verifies SQLite recycle now writes both trash tables and clears the FTS mirror inside the same script.
// TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS 用于验证 SQLite 回收现在会同时写入两张回收站表，并在同一脚本里清理 FTS 镜像。
func TestRecycleColdMemoriesCopiesRowsIntoTrashAndDeletesFTS(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "memory_status IN"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":11,"team_id":1,"space_id":2,"project_id":3,"user_id":4,"origin_session_id":5,"source_turn_id":6,"vector_id":"vec-11","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":1,"category":2,"abstract":"阶段已变更","details":"项目阶段从 A 进入 B","memory_status":1,"priority":2,"memory_level":1,"refresh_weight":0,"support_count":0,"rebuttal_count":0,"status_reason":"superseded","expires_timestamp":0,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"memory_id":11,"context_key":"phase","context_value":"delivery","support_count":1,"rebuttal_count":0,"last_supported_timestamp":1,"last_rebutted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":7}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleColdMemories(context.Background(), logicdomain.MemoryRecycleQuery{
		Limit:         32,
		RecycledAt:    time.Unix(100, 0).UTC(),
		RecycleReason: "terminal memory recycle",
	})
	if err != nil {
		t.Fatalf("RecycleColdMemories returned error: %v", err)
	}
	if result.BatchID != 7 {
		t.Fatalf("batch id = %d, want 7", result.BatchID)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 1 {
		t.Fatalf("recycle counts = %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-11" {
		t.Fatalf("vector ids = %v", result.RecycledVectorIDs)
	}
	for _, fragment := range []string{
		"INSERT INTO vmm_memory_nodes_trash",
		"INSERT INTO vmm_memory_context_edges_trash",
		"DELETE FROM vmm_memory_nodes_fts",
		"DELETE FROM vmm_memory_nodes",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash verifies idle-session recycle copies stale session memories and eligible turns into the trash tables, while still clearing the hot FTS mirror.
// TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash 用于验证 idle-session 回收会把陈旧 session 记忆和可归档 turn 迁入回收站，同时清理热表 FTS 镜像。
func TestRecycleIdleSessionsMovesStaleSessionRowsIntoTrash(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":21,"session_key":"sess-idle","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":31,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":21,"source_turn_id":41,"vector_id":"vec-31","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"该 session 里的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":41,"session_id":21,"project_id":4,"dehydrated_content":"{\"user\":\"old\"}","dehydrated_budget":5,"extracted_status":1,"details":"旧轮次摘要","details_budget":2,"created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":9}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
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
		t.Fatalf("batch ids = %v", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 21 {
		t.Fatalf("session ids = %v", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 1 {
		t.Fatalf("idle-session recycle counts = %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-31" {
		t.Fatalf("idle-session vector ids = %v", result.RecycledVectorIDs)
	}
	for _, fragment := range []string{
		"INSERT INTO vmm_memory_nodes_trash",
		"INSERT INTO vmm_turn_records_trash",
		"DELETE FROM vmm_memory_nodes_fts",
		"DELETE FROM vmm_turn_records",
		"VALUES (9, 'session_idle_recycle', 21, 4",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected idle-session recycle sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestRecycleIdleSessionsSkipsNoOpOldestSessions verifies SQLite idle-session recycle now prefilters to sessions with recyclable rows, so one oldest no-op session cannot starve a later eligible session when the batch limit is tight.
// TestRecycleIdleSessionsSkipsNoOpOldestSessions 用于验证 SQLite idle-session 回收现在会先过滤出确实存在可回收数据的 session，避免在批量限制较小时最老但 no-op 的 session 饿死后续可回收 session。
func TestRecycleIdleSessionsSkipsNoOpOldestSessions(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	sessionQueryCount := 0
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_sessions"):
			sessionQueryCount++
			if !strings.Contains(sql, "FROM vmm_memory_nodes m") || !strings.Contains(sql, "FROM vmm_turn_records tr") {
				return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":21,"session_key":"sess-noop","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
			}
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":22,"session_key":"sess-eligible","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":10,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":3}
]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes") && strings.Contains(sql, "origin_session_id = ?"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":32,"team_id":2,"space_id":3,"project_id":4,"user_id":1,"origin_session_id":22,"source_turn_id":42,"vector_id":"vec-32","vector_json":"[0.1,0.2]","source_kind":0,"scope_level":0,"category":4,"abstract":"短期待办已过期","details":"后续 session 的临时 TODO 已失效","memory_status":0,"priority":2,"memory_level":0,"refresh_weight":1,"support_count":0,"rebuttal_count":0,"status_reason":"","expires_timestamp":1000,"last_recalled_timestamp":0,"last_adopted_timestamp":0,"last_reinforced_timestamp":0,"recalled_count":0,"adopted_count":0,"reinforcement_count":0,"cross_session_adopted_count":0,"decay_disabled":0,"dedupe_hash":"","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_memory_context_edges"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":1}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records tr"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":10}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, _ *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.RecycleIdleSessions(context.Background(), logicdomain.SessionIdleRecycleQuery{
		Limit:             1,
		RecycledAt:        time.Unix(300, 0).UTC(),
		IdleBefore:        time.Unix(200, 0).UTC(),
		TurnHotWindowSize: 8,
		RecycleReason:     "idle session recycle",
	})
	if err != nil {
		t.Fatalf("RecycleIdleSessions returned error: %v", err)
	}
	if sessionQueryCount != 1 {
		t.Fatalf("session query count = %d, want 1", sessionQueryCount)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 10 {
		t.Fatalf("batch ids = %v, want [10]", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 22 {
		t.Fatalf("session ids = %v, want [22]", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 1 || result.RecycledTurnCount != 0 {
		t.Fatalf("idle-session recycle counts = %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-32" {
		t.Fatalf("idle-session vector ids = %v", result.RecycledVectorIDs)
	}
}

// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows verifies the SQLite session prefilter keeps both stale-memory and old-turn existence checks in one clause so no-op oldest sessions do not block later useful work.
// TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows 用于验证 SQLite session 预过滤会同时包含陈旧记忆和旧 turn 的存在性检查，避免最老但 no-op 的 session 阻塞后续真正有收益的回收工作。
func TestBuildSQLiteIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows(t *testing.T) {
	clause, args := buildSQLiteIdleSessionCandidateAvailabilityClause(12345, 8)
	if !strings.Contains(clause, "FROM vmm_memory_nodes m") {
		t.Fatalf("candidate availability clause missing stale-memory branch: %q", clause)
	}
	if !strings.Contains(clause, "FROM vmm_turn_records tr") {
		t.Fatalf("candidate availability clause missing old-turn branch: %q", clause)
	}
	if !strings.Contains(clause, "NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)") {
		t.Fatalf("candidate availability clause missing profile reference guard: %q", clause)
	}
	if len(args) != 5 {
		t.Fatalf("candidate availability args len = %d, want 5", len(args))
	}
}

// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata verifies SQLite purge now hard-deletes old trash rows from every trash table and then removes the recycle-batch metadata row itself, so batch bookkeeping cannot accumulate forever after the backup window ends.
// TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata 用于验证 SQLite purge 现在会从每张回收站表硬删除过期行，并继续删除回收批次元数据本身，避免软备份窗口结束后批次台账持续累积。
func TestPurgeExpiredTrashDeletesAllTrashTablesAndBatchMetadata(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_batches"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"id":7},{"id":8}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_nodes_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":2}]`}, nil
		case strings.Contains(sql, "FROM vmm_memory_context_edges_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":3}]`}, nil
		case strings.Contains(sql, "FROM vmm_turn_records_trash"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"count":4}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	result, err := store.PurgeExpiredTrash(context.Background(), time.Unix(200, 0).UTC(), 16)
	if err != nil {
		t.Fatalf("PurgeExpiredTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.BatchIDs[0] != 7 || result.BatchIDs[1] != 8 {
		t.Fatalf("purged batch ids = %v", result.BatchIDs)
	}
	if result.PurgedMemoryCount != 2 || result.PurgedContextCount != 3 || result.PurgedTurnCount != 4 {
		t.Fatalf("purge counts = %+v", result)
	}
	for _, fragment := range []string{
		"DELETE FROM vmm_memory_context_edges_trash",
		"DELETE FROM vmm_memory_nodes_trash",
		"DELETE FROM vmm_turn_records_trash",
		"DELETE FROM vmm_recycle_batches",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected purge sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestEnqueueVectorGCJobsPersistsRetryRows verifies SQLite uses the existing vector-gc queue table to persist failed sidecar deletes instead of keeping the failure only in logs.
// TestEnqueueVectorGCJobsPersistsRetryRows 用于验证 SQLite 会使用现有 vector-gc 队列表持久化失败的旁路删除，而不是只把失败留在日志里。
func TestEnqueueVectorGCJobsPersistsRetryRows(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		if strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_vector_gc_jobs") {
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":41}]`}, nil
		}
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	if err := store.EnqueueVectorGCJobs(context.Background(), logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   7,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: []string{"vec-1", "vec-2"},
		NextRunAt: time.Unix(500, 0).UTC(),
	}); err != nil {
		t.Fatalf("EnqueueVectorGCJobs returned error: %v", err)
	}
	for _, fragment := range []string{
		"INSERT OR IGNORE INTO vmm_vector_gc_jobs",
		"'vec-1'",
		"'vec-2'",
		"'retention_recycle_vector_delete'",
		"BEGIN IMMEDIATE;",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected enqueue sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestSQLiteVectorGCJobsClaimRetryAndComplete verifies SQLite can lease due retry rows and then reschedule or complete them through the same persistent queue table.
// TestSQLiteVectorGCJobsClaimRetryAndComplete 用于验证 SQLite 可以领取到期重试行，并通过同一持久化队列表完成重新调度或完成。
func TestSQLiteVectorGCJobsClaimRetryAndComplete(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executedSQL := make([]string, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_vector_gc_jobs"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":51,"batch_id":7,"vector_id":"vec-retry-1","job_type":"retention_recycle_vector_delete","attempt_count":2,"next_run_timestamp":1000,"claimed_timestamp":0,"completed_timestamp":0,"last_error":"old error","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executedSQL = append(executedSQL, req.GetSql())
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	jobs, err := store.ClaimPendingVectorGCJobs(context.Background(), time.Unix(2, 0).UTC(), time.Unix(5, 0).UTC(), 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 51 || jobs[0].VectorID != "vec-retry-1" {
		t.Fatalf("claimed jobs = %+v", jobs)
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
	for idx, fragment := range []string{
		"UPDATE vmm_vector_gc_jobs\nSET claimed_timestamp",
		"attempt_count = attempt_count + 1",
		"DELETE FROM vmm_vector_gc_jobs",
	} {
		if !strings.Contains(executedSQL[idx], fragment) {
			t.Fatalf("expected executed sql #%d to contain %q, got %q", idx, fragment, executedSQL[idx])
		}
	}
}

// TestBuildSQLiteColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns verifies the independent cold-turn scan only queues sessions whose old turns are both outside the hot window and free from memory/profile references.
// TestBuildSQLiteColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns 用于验证独立冷 turn 扫描只会为“超出热窗口且没有记忆/画像引用”的旧 turn 所在 session 入队。
func TestBuildSQLiteColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns(t *testing.T) {
	clause, args := buildSQLiteColdTurnJobSessionAvailabilityClause(8)
	if !strings.Contains(clause, "LIMIT 8") {
		t.Fatalf("cold-turn availability clause missing hot-window limit: %q", clause)
	}
	if !strings.Contains(clause, "FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id") {
		t.Fatalf("cold-turn availability clause missing memory reference guard: %q", clause)
	}
	if !strings.Contains(clause, "FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id") {
		t.Fatalf("cold-turn availability clause missing profile reference guard: %q", clause)
	}
	if len(args) != 1 {
		t.Fatalf("cold-turn availability args len = %d, want 1", len(args))
	}
}

// TestEnqueueColdTurnRecycleJobsPersistsQueueRows verifies SQLite persists bounded cold-turn recycle jobs into the dedicated recycle-job queue instead of executing scan and archive in one coupled script.
// TestEnqueueColdTurnRecycleJobsPersistsQueueRows 用于验证 SQLite 会把有界冷 turn 回收任务持久化到独立回收队列表，而不是把扫描和归档耦合在同一段脚本里直接执行。
func TestEnqueueColdTurnRecycleJobsPersistsQueueRows(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	var capturedSQL string
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "SELECT id, session_key, user_id, team_id, space_id, project_id") && strings.Contains(sql, "FROM vmm_sessions"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":41,"session_key":"sess-cold-turn","user_id":1,"team_id":2,"space_id":3,"project_id":4,"turn_count":12,"last_summarized_id":0,"last_compacted_turn_id":0,"summarize_content":"","summarize_budget":0,"last_extract_observed_timestamp":0,"last_extract_completed_timestamp":0,"last_compacted_timestamp":0,"created_timestamp":1,"updated_timestamp":2}
]`}, nil
		case strings.Contains(sql, "SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_recycle_jobs"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[{"next_id":51}]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		capturedSQL = req.GetSql()
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	count, err := store.EnqueueColdTurnRecycleJobs(context.Background(), logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             4,
		ScannedAt:         time.Unix(100, 0).UTC(),
		NextRunAt:         time.Unix(100, 0).UTC(),
		TurnHotWindowSize: 8,
	})
	if err != nil {
		t.Fatalf("EnqueueColdTurnRecycleJobs returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("enqueued job count = %d, want 1", count)
	}
	for _, fragment := range []string{
		"INSERT OR IGNORE INTO vmm_recycle_jobs",
		"'cold_turn_recycle_job'",
		"BEGIN IMMEDIATE;",
		"COMMIT;",
	} {
		if !strings.Contains(capturedSQL, fragment) {
			t.Fatalf("expected enqueue sql to contain %q, got %q", fragment, capturedSQL)
		}
	}
}

// TestSQLiteRecycleJobsClaimRetryAndComplete verifies SQLite can lease due cold-turn recycle jobs and then reschedule or complete them through the dedicated persistent queue.
// TestSQLiteRecycleJobsClaimRetryAndComplete 用于验证 SQLite 可以领取到期的冷 turn 回收任务，并通过独立持久化队列表完成重新调度或关闭。
func TestSQLiteRecycleJobsClaimRetryAndComplete(t *testing.T) {
	fake := &fakeSqliteClient{}
	store := &Store{client: fake, timeout: time.Second}

	executedSQL := make([]string, 0, 3)
	fake.queryJSONFunc = func(_ context.Context, req *sqlitev1.QueryRequest, _ ...grpc.CallOption) (*sqlitev1.QueryJsonResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_recycle_jobs"):
			return &sqlitev1.QueryJsonResponse{JsonData: `[
{"id":61,"session_id":41,"project_id":4,"job_type":"cold_turn_recycle_job","attempt_count":1,"next_run_timestamp":1000,"claimed_timestamp":0,"last_error":"old error","created_timestamp":10,"updated_timestamp":20}
]`}, nil
		default:
			return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
		}
	}
	fake.executeScriptFunc = func(_ context.Context, req *sqlitev1.ExecuteRequest, _ ...grpc.CallOption) (*sqlitev1.ExecuteResponse, error) {
		executedSQL = append(executedSQL, req.GetSql())
		return &sqlitev1.ExecuteResponse{Success: true}, nil
	}

	jobs, err := store.ClaimPendingRecycleJobs(context.Background(), logicdomain.RecycleJobTypeColdTurn, time.Unix(2, 0).UTC(), time.Unix(5, 0).UTC(), 8)
	if err != nil {
		t.Fatalf("ClaimPendingRecycleJobs returned error: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != 61 || jobs[0].SessionID != 41 {
		t.Fatalf("claimed recycle jobs = %+v", jobs)
	}
	if err := store.RetryRecycleJobs(context.Background(), []uint64{61}, time.Unix(9, 0).UTC(), "retry failed"); err != nil {
		t.Fatalf("RetryRecycleJobs returned error: %v", err)
	}
	if err := store.CompleteRecycleJobs(context.Background(), []uint64{61}, time.Unix(12, 0).UTC()); err != nil {
		t.Fatalf("CompleteRecycleJobs returned error: %v", err)
	}
	if len(executedSQL) != 3 {
		t.Fatalf("executed sql count = %d, want 3", len(executedSQL))
	}
	for idx, fragment := range []string{
		"UPDATE vmm_recycle_jobs\nSET claimed_timestamp",
		"attempt_count = attempt_count + 1",
		"DELETE FROM vmm_recycle_jobs",
	} {
		if !strings.Contains(executedSQL[idx], fragment) {
			t.Fatalf("expected executed sql #%d to contain %q, got %q", idx, fragment, executedSQL[idx])
		}
	}
}
