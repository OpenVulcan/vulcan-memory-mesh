// recycle_jobs_integration_test.go verifies recycle-job SQL paths against the packaged SQLite FFI runtime.
// recycle_jobs_integration_test.go 用于通过已打包 SQLite FFI 运行时验证回收任务 SQL 路径。
package vldb_sqlite

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRealSQLiteClaimPendingRecycleJobsUsesUpdateReturning verifies the real SQLite FFI accepts one typed UPDATE RETURNING statement for atomic job leasing.
// TestRealSQLiteClaimPendingRecycleJobsUsesUpdateReturning 用于验证真实 SQLite FFI 接受单条强类型 UPDATE RETURNING 语句来原子领取任务。
func TestRealSQLiteClaimPendingRecycleJobsUsesUpdateReturning(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(800, 0).UTC()
	claimUntil := now.Add(time.Hour)

	for _, job := range []struct {
		ID        uint64
		SessionID uint64
		ProjectID uint64
		DueAt     time.Time
	}{
		{ID: 901, SessionID: 21, ProjectID: 9, DueAt: now.Add(-2 * time.Minute)},
		{ID: 902, SessionID: 22, ProjectID: 10, DueAt: now.Add(-1 * time.Minute)},
		{ID: 903, SessionID: 23, ProjectID: 11, DueAt: now.Add(30 * time.Minute)},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_recycle_jobs (
  id, session_id, project_id, job_type, attempt_count, next_run_timestamp,
  claimed_timestamp, last_error, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, 0, ?, 0, '', ?, ?);
`, job.ID, job.SessionID, job.ProjectID, logicdomain.RecycleJobTypeColdTurn, job.DueAt.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite recycle job %d: %v", job.ID, err)
		}
	}

	jobs, err := store.ClaimPendingRecycleJobs(ctx, logicdomain.RecycleJobTypeColdTurn, now, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingRecycleJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 901 || jobs[1].ID != 902 {
		t.Fatalf("expected due jobs 901 and 902, got %+v", jobs)
	}
	for _, job := range jobs {
		if !job.NextRunAt.Equal(claimUntil) {
			t.Fatalf("job %d next run = %v, want %v", job.ID, job.NextRunAt, claimUntil)
		}
		if job.ClaimedAt.IsZero() {
			t.Fatalf("job %d was returned without claimed_at", job.ID)
		}
	}

	rows, err := queryRows[sqliteRecycleJobRow](store, ctx, `
SELECT id, session_id, project_id, job_type, attempt_count,
       next_run_timestamp, claimed_timestamp, last_error, created_timestamp, updated_timestamp
FROM vmm_recycle_jobs
WHERE id IN (?, ?, ?)
ORDER BY id ASC
`, uint64(901), uint64(902), uint64(903))
	if err != nil {
		t.Fatalf("query real sqlite recycle jobs after claim: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected three recycle job rows, got %d", len(rows))
	}
	if rows[0].NextRunTimestamp != claimUntil.UnixMilli() || rows[1].NextRunTimestamp != claimUntil.UnixMilli() {
		t.Fatalf("expected due rows to be leased until %d, got %+v", claimUntil.UnixMilli(), rows[:2])
	}
	if rows[2].NextRunTimestamp == claimUntil.UnixMilli() {
		t.Fatalf("future row should not be claimed, got %+v", rows[2])
	}
}

// TestRealSQLiteClaimPendingVectorGCJobsUsesUpdateReturning verifies the real SQLite FFI accepts the vector-gc UPDATE RETURNING lease statement with the incomplete-job guard.
// TestRealSQLiteClaimPendingVectorGCJobsUsesUpdateReturning 用于验证真实 SQLite FFI 接受带未完成任务保护条件的 vector-gc UPDATE RETURNING 领取语句。
func TestRealSQLiteClaimPendingVectorGCJobsUsesUpdateReturning(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(900, 0).UTC()
	claimUntil := now.Add(time.Hour)

	for _, job := range []struct {
		ID          uint64
		BatchID     uint64
		VectorID    string
		DueAt       time.Time
		CompletedAt time.Time
	}{
		{ID: 1001, BatchID: 31, VectorID: "vec-real-1001", DueAt: now.Add(-2 * time.Minute)},
		{ID: 1002, BatchID: 31, VectorID: "vec-real-1002", DueAt: now.Add(-1 * time.Minute)},
		{ID: 1003, BatchID: 31, VectorID: "vec-real-1003", DueAt: now.Add(30 * time.Minute)},
		{ID: 1004, BatchID: 31, VectorID: "vec-real-1004", DueAt: now.Add(-3 * time.Minute), CompletedAt: now.Add(-time.Minute)},
	} {
		completedAtMs := int64(0)
		if !job.CompletedAt.IsZero() {
			completedAtMs = job.CompletedAt.UnixMilli()
		}
		if err := store.exec(ctx, `
INSERT INTO vmm_vector_gc_jobs (
  id, batch_id, vector_id, job_type, attempt_count, next_run_timestamp,
  claimed_timestamp, completed_timestamp, last_error, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, 0, ?, 0, ?, '', ?, ?);
`, job.ID, job.BatchID, job.VectorID, logicdomain.VectorGCJobTypeRetentionRecycle, job.DueAt.UnixMilli(), completedAtMs, now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite vector gc job %d: %v", job.ID, err)
		}
	}

	jobs, err := store.ClaimPendingVectorGCJobs(ctx, now, claimUntil, 8)
	if err != nil {
		t.Fatalf("ClaimPendingVectorGCJobs returned error: %v", err)
	}
	if len(jobs) != 2 || jobs[0].ID != 1001 || jobs[1].ID != 1002 {
		t.Fatalf("expected due incomplete vector jobs 1001 and 1002, got %+v", jobs)
	}
	for _, job := range jobs {
		if !job.NextRunAt.Equal(claimUntil) {
			t.Fatalf("vector job %d next run = %v, want %v", job.ID, job.NextRunAt, claimUntil)
		}
		if job.ClaimedAt.IsZero() {
			t.Fatalf("vector job %d was returned without claimed_at", job.ID)
		}
		if !job.CompletedAt.IsZero() {
			t.Fatalf("vector job %d should still be incomplete after claim, got %v", job.ID, job.CompletedAt)
		}
	}

	rows, err := queryRows[sqliteVectorGCJobRow](store, ctx, `
SELECT id, batch_id, vector_id, job_type, attempt_count,
       next_run_timestamp, claimed_timestamp, completed_timestamp,
       last_error, created_timestamp, updated_timestamp
FROM vmm_vector_gc_jobs
WHERE id IN (?, ?, ?, ?)
ORDER BY id ASC
`, uint64(1001), uint64(1002), uint64(1003), uint64(1004))
	if err != nil {
		t.Fatalf("query real sqlite vector gc jobs after claim: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected four vector gc job rows, got %d", len(rows))
	}
	if rows[0].NextRunTimestamp != claimUntil.UnixMilli() || rows[1].NextRunTimestamp != claimUntil.UnixMilli() {
		t.Fatalf("expected due incomplete vector rows to be leased until %d, got %+v", claimUntil.UnixMilli(), rows[:2])
	}
	if rows[2].NextRunTimestamp == claimUntil.UnixMilli() {
		t.Fatalf("future vector row should not be claimed, got %+v", rows[2])
	}
	if rows[3].CompletedTimestamp == 0 || rows[3].NextRunTimestamp == claimUntil.UnixMilli() {
		t.Fatalf("completed vector row should not be reclaimed, got %+v", rows[3])
	}
}

// TestRealSQLitePurgeExpiredTrashDeletesSelectedBatches verifies typed purge counts and ordered deletes work against the real SQLite FFI runtime.
// TestRealSQLitePurgeExpiredTrashDeletesSelectedBatches 用于验证强类型 purge 计数与顺序删除能在真实 SQLite FFI 运行时中清理选中的批次。
func TestRealSQLitePurgeExpiredTrashDeletesSelectedBatches(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(10000, 0).UTC()
	purgeBefore := now.Add(-time.Hour)

	for _, batch := range []struct {
		ID        uint64
		RecycleAt time.Time
	}{
		{ID: 2001, RecycleAt: purgeBefore.Add(-2 * time.Minute)},
		{ID: 2002, RecycleAt: purgeBefore.Add(-time.Minute)},
		{ID: 2003, RecycleAt: purgeBefore.Add(time.Minute)},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (?, ?, 0, 0, ?, ?, 0, ?, ?);
`, batch.ID, logicdomain.RecycleTypeColdTurn, logicdomain.RecycleReasonColdTurnArchive, batch.RecycleAt.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite recycle batch %d: %v", batch.ID, err)
		}
	}

	for _, contextTrash := range []struct {
		BatchID      uint64
		MemoryID     uint64
		ContextValue string
	}{
		{BatchID: 2001, MemoryID: 3001, ContextValue: "selected"},
		{BatchID: 2003, MemoryID: 3003, ContextValue: "future"},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_memory_context_edges_trash (
  batch_id, recycled_at, recycle_reason,
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?, 0, ?, ?);
`, contextTrash.BatchID, purgeBefore.UnixMilli(), logicdomain.RecycleReasonColdTurnArchive, contextTrash.MemoryID, "phase", contextTrash.ContextValue, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite context trash for batch %d: %v", contextTrash.BatchID, err)
		}
	}
	if err := store.exec(ctx, `
INSERT INTO vmm_turn_records_trash (
  batch_id, recycled_at, recycle_reason,
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, 1, ?, ?);
`, uint64(2002), purgeBefore.UnixMilli(), logicdomain.RecycleReasonColdTurnArchive, uint64(4002), uint64(5002), uint64(6002), `{"user":"old"}`, 1, "old turn", now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatalf("insert real sqlite turn trash: %v", err)
	}

	result, err := store.PurgeExpiredTrash(ctx, purgeBefore, 8)
	if err != nil {
		t.Fatalf("PurgeExpiredTrash returned error: %v", err)
	}
	if len(result.BatchIDs) != 2 || result.BatchIDs[0] != 2001 || result.BatchIDs[1] != 2002 {
		t.Fatalf("expected purged batch ids 2001 and 2002, got %+v", result.BatchIDs)
	}
	if result.PurgedContextCount != 1 || result.PurgedTurnCount != 1 || result.PurgedMemoryCount != 0 {
		t.Fatalf("unexpected purge counts: %+v", result)
	}

	selectedBatchCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_recycle_batches WHERE id IN (?, ?)`, uint64(2001), uint64(2002))
	if err != nil {
		t.Fatalf("count selected recycle batches after purge: %v", err)
	}
	if selectedBatchCount != 0 {
		t.Fatalf("expected selected recycle batches to be deleted, got %d", selectedBatchCount)
	}
	selectedContextCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id IN (?, ?)`, uint64(2001), uint64(2002))
	if err != nil {
		t.Fatalf("count selected context trash after purge: %v", err)
	}
	selectedTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records_trash WHERE batch_id IN (?, ?)`, uint64(2001), uint64(2002))
	if err != nil {
		t.Fatalf("count selected turn trash after purge: %v", err)
	}
	if selectedContextCount != 0 || selectedTurnCount != 0 {
		t.Fatalf("expected selected trash rows to be deleted, got context=%d turn=%d", selectedContextCount, selectedTurnCount)
	}
	futureBatchCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_recycle_batches WHERE id = ?`, uint64(2003))
	if err != nil {
		t.Fatalf("count future recycle batch after purge: %v", err)
	}
	futureContextCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id = ?`, uint64(2003))
	if err != nil {
		t.Fatalf("count future context trash after purge: %v", err)
	}
	if futureBatchCount != 1 || futureContextCount != 1 {
		t.Fatalf("expected future batch and trash to remain, got batch=%d context=%d", futureBatchCount, futureContextCount)
	}
}

// TestRealSQLiteRecycleColdMemoriesUsesTypedMemoryIDs verifies cold-memory recycle copies only terminal rows into trash and removes them through typed memory-id predicates.
// TestRealSQLiteRecycleColdMemoriesUsesTypedMemoryIDs 用于验证终态记忆回收会通过强类型 memory-id 条件复制并删除终态行，同时保留 active 行。
func TestRealSQLiteRecycleColdMemoriesUsesTypedMemoryIDs(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(12000, 0).UTC()

	for _, memory := range []struct {
		ID       uint64
		VectorID string
		Status   int
		Updated  time.Time
	}{
		{ID: 3001, VectorID: "vec-cold-real-3001", Status: logicdomain.MemoryStatusSuperseded, Updated: now.Add(-3 * time.Minute)},
		{ID: 3002, VectorID: "vec-cold-real-3002", Status: logicdomain.MemoryStatusDeleted, Updated: now.Add(-2 * time.Minute)},
		{ID: 3003, VectorID: "vec-active-real-3003", Status: logicdomain.MemoryStatusActive, Updated: now.Add(-time.Minute)},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_memory_nodes (
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
  vector_id, vector_json, source_kind, scope_level, category, abstract, details,
  memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
  expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
  recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_timestamp, updated_timestamp
) VALUES (?, 1, 2, 3, 4, 5, NULL, ?, ?, ?, ?, ?, ?, ?, ?, 2, 1, 0, 0, 0, ?, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?, ?, ?);
`, memory.ID, memory.VectorID, "[0.1,0.2]", logicdomain.MemorySourceKindTurnExtract, logicdomain.MemoryScopeLevelSession, 0, "terminal fact", "terminal details", memory.Status, "terminal status", "", now.UnixMilli(), memory.Updated.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite memory node %d: %v", memory.ID, err)
		}
	}
	for _, edge := range []struct {
		MemoryID     uint64
		ContextValue string
	}{
		{MemoryID: 3001, ContextValue: "selected-a"},
		{MemoryID: 3002, ContextValue: "selected-b"},
		{MemoryID: 3003, ContextValue: "active"},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_memory_context_edges (
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, 1, 0, ?, 0, ?, ?);
`, edge.MemoryID, "phase", edge.ContextValue, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite memory context edge %d: %v", edge.MemoryID, err)
		}
	}

	result, err := store.RecycleColdMemories(ctx, logicdomain.MemoryRecycleQuery{
		Limit:         8,
		RecycledAt:    now,
		RecycleReason: logicdomain.RecycleReasonColdTerminalMemory,
	})
	if err != nil {
		t.Fatalf("RecycleColdMemories returned error: %v", err)
	}
	if result.RecycledMemoryCount != 2 || result.RecycledContextCount != 2 {
		t.Fatalf("unexpected cold-memory recycle result: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 2 || result.RecycledVectorIDs[0] != "vec-cold-real-3001" || result.RecycledVectorIDs[1] != "vec-cold-real-3002" {
		t.Fatalf("unexpected recycled vector ids: %+v", result.RecycledVectorIDs)
	}

	hotTerminalCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE id IN (?, ?)`, uint64(3001), uint64(3002))
	if err != nil {
		t.Fatalf("count terminal hot memories after recycle: %v", err)
	}
	trashMemoryCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes_trash WHERE batch_id = ? AND id IN (?, ?)`, result.BatchID, uint64(3001), uint64(3002))
	if err != nil {
		t.Fatalf("count terminal memory trash after recycle: %v", err)
	}
	trashContextCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id = ? AND memory_id IN (?, ?)`, result.BatchID, uint64(3001), uint64(3002))
	if err != nil {
		t.Fatalf("count terminal context trash after recycle: %v", err)
	}
	activeMemoryCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE id = ?`, uint64(3003))
	if err != nil {
		t.Fatalf("count active hot memory after recycle: %v", err)
	}
	activeContextCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_context_edges WHERE memory_id = ?`, uint64(3003))
	if err != nil {
		t.Fatalf("count active context edge after recycle: %v", err)
	}
	if hotTerminalCount != 0 || trashMemoryCount != 2 || trashContextCount != 2 {
		t.Fatalf("expected terminal rows moved to trash, got hot=%d trashMemory=%d trashContext=%d", hotTerminalCount, trashMemoryCount, trashContextCount)
	}
	if activeMemoryCount != 1 || activeContextCount != 1 {
		t.Fatalf("expected active memory and context to remain, got memory=%d context=%d", activeMemoryCount, activeContextCount)
	}
}

// TestRealSQLiteRecycleColdTurnsUsesTypedTurnIDs verifies cold-turn recycle moves only unreferenced turns outside the hot window through typed turn-id predicates.
// TestRealSQLiteRecycleColdTurnsUsesTypedTurnIDs 用于验证冷 turn 回收会通过强类型 turn-id 条件迁移热窗口外且无引用的 turn。
func TestRealSQLiteRecycleColdTurnsUsesTypedTurnIDs(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(13000, 0).UTC()
	session := sessionRow{
		ID:         7101,
		SessionKey: "cold-turn-real-session",
		UserID:     1,
		TeamID:     1,
		SpaceID:    1,
		ProjectID:  1,
	}
	if err := store.exec(ctx, `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id, turn_count,
  last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 4, 0, 0, '', 0, 0, 0, 0, ?, ?);
`, session.ID, session.SessionKey, session.UserID, session.TeamID, session.SpaceID, session.ProjectID, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("insert real sqlite cold-turn session: %v", err)
	}
	for _, turn := range []struct {
		ID      uint64
		Content string
	}{
		{ID: 8101, Content: `{"user":"old selected 1"}`},
		{ID: 8102, Content: `{"assistant":"old selected 2"}`},
		{ID: 8103, Content: `{"user":"referenced"}`},
		{ID: 8104, Content: `{"assistant":"hot window"}`},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, 1, ?, ?, 1, ?, ?);
`, turn.ID, session.ID, session.ProjectID, turn.Content, logicdomain.TurnExtractedStatusDone, "cold turn details", now.Add(-time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite cold turn %d: %v", turn.ID, err)
		}
	}

	statement := parameterizedMemoryNodeInsertStatement(logicdomain.MemoryNodeRecord{
		ID:              9101,
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.ID,
		SourceTurnID:    8103,
		VectorID:        "vec-cold-turn-real-9101",
		Vector:          []float32{0.1, 0.2},
		SourceKind:      logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel:      logicdomain.MemoryScopeLevelSession,
		Category:        4,
		Abstract:        "referenced cold turn memory",
		Details:         "keeps turn 8103 in hot storage",
		Status:          logicdomain.MemoryStatusActive,
		Priority:        logicdomain.MemoryPriorityP2,
		MemoryLevel:     logicdomain.MemoryLevelSession,
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
	})
	if err := store.exec(ctx, statement.SQL, statement.Params...); err != nil {
		t.Fatalf("insert real sqlite cold-turn memory reference: %v", err)
	}

	result, err := store.RecycleColdTurns(ctx, logicdomain.ColdTurnRecycleQuery{
		SessionID:         session.ID,
		RecycledAt:        now,
		TurnHotWindowSize: 1,
		RecycleReason:     logicdomain.RecycleReasonColdTurnArchive,
	})
	if err != nil {
		t.Fatalf("RecycleColdTurns returned error: %v", err)
	}
	if result.BatchID == 0 || result.SessionID != session.ID || result.ProjectID != session.ProjectID || result.RecycledTurnCount != 2 {
		t.Fatalf("unexpected cold-turn recycle result: %+v", result)
	}

	hotSelectedTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id IN (?, ?)`, uint64(8101), uint64(8102))
	if err != nil {
		t.Fatalf("count recycled cold hot turns: %v", err)
	}
	trashSelectedTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records_trash WHERE batch_id = ? AND id IN (?, ?)`, result.BatchID, uint64(8101), uint64(8102))
	if err != nil {
		t.Fatalf("count recycled cold turn trash: %v", err)
	}
	referencedTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id = ?`, uint64(8103))
	if err != nil {
		t.Fatalf("count preserved referenced cold turn: %v", err)
	}
	hotWindowTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id = ?`, uint64(8104))
	if err != nil {
		t.Fatalf("count preserved hot-window cold turn: %v", err)
	}
	if hotSelectedTurnCount != 0 || trashSelectedTurnCount != 2 {
		t.Fatalf("expected selected cold turns moved to trash, got hot=%d trash=%d", hotSelectedTurnCount, trashSelectedTurnCount)
	}
	if referencedTurnCount != 1 || hotWindowTurnCount != 1 {
		t.Fatalf("expected referenced and hot-window turns to remain, got referenced=%d hot=%d", referencedTurnCount, hotWindowTurnCount)
	}
}

// TestRealSQLiteRecycleOneIdleSessionUsesTypedMemoryAndTurnIDs verifies idle-session recycle moves selected memory and turn rows while preserving rows still referenced by active memory.
// TestRealSQLiteRecycleOneIdleSessionUsesTypedMemoryAndTurnIDs 用于验证 idle-session 回收会迁移选中的 memory 与 turn 行，同时保留仍被 active memory 引用的行。
func TestRealSQLiteRecycleOneIdleSessionUsesTypedMemoryAndTurnIDs(t *testing.T) {
	store := newRealSQLiteRecycleJobTestStore(t)
	ctx := context.Background()
	now := time.Unix(14000, 0).UTC()
	idleBefore := now.Add(-time.Hour)
	session := sessionRow{
		ID:         7001,
		SessionKey: "idle-real-session",
		UserID:     1,
		TeamID:     1,
		SpaceID:    1,
		ProjectID:  1,
	}
	if err := store.exec(ctx, `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id, turn_count,
  last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 3, 0, 0, '', 0, 0, 0, 0, ?, ?);
`, session.ID, session.SessionKey, session.UserID, session.TeamID, session.SpaceID, session.ProjectID, idleBefore.Add(-time.Hour).UnixMilli(), idleBefore.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("insert real sqlite idle session: %v", err)
	}
	for _, turn := range []struct {
		ID      uint64
		Content string
	}{
		{ID: 8001, Content: `{"user":"old selected 1"}`},
		{ID: 8002, Content: `{"assistant":"old selected 2"}`},
		{ID: 8003, Content: `{"user":"still referenced"}`},
	} {
		if err := store.exec(ctx, `
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, 1, ?, ?, 1, ?, ?);
`, turn.ID, session.ID, session.ProjectID, turn.Content, logicdomain.TurnExtractedStatusDone, "old details", idleBefore.Add(-30*time.Minute).UnixMilli(), idleBefore.Add(-30*time.Minute).UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite idle turn %d: %v", turn.ID, err)
		}
	}
	for _, memory := range []struct {
		ID           uint64
		SourceTurnID uint64
		VectorID     string
		ExpiresAt    time.Time
	}{
		{ID: 9001, SourceTurnID: 8001, VectorID: "vec-idle-real-9001", ExpiresAt: idleBefore.Add(-2 * time.Minute)},
		{ID: 9002, SourceTurnID: 8002, VectorID: "vec-idle-real-9002", ExpiresAt: idleBefore.Add(-time.Minute)},
		{ID: 9003, SourceTurnID: 8003, VectorID: "vec-idle-real-active-9003", ExpiresAt: now.Add(time.Hour)},
	} {
		statement := parameterizedMemoryNodeInsertStatement(logicdomain.MemoryNodeRecord{
			ID:              memory.ID,
			TeamID:          session.TeamID,
			SpaceID:         session.SpaceID,
			ProjectID:       session.ProjectID,
			UserID:          session.UserID,
			OriginSessionID: session.ID,
			SourceTurnID:    memory.SourceTurnID,
			VectorID:        memory.VectorID,
			Vector:          []float32{0.1, 0.2},
			SourceKind:      logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel:      logicdomain.MemoryScopeLevelSession,
			Category:        4,
			Abstract:        "idle memory",
			Details:         "idle memory details",
			Status:          logicdomain.MemoryStatusActive,
			Priority:        logicdomain.MemoryPriorityP2,
			MemoryLevel:     logicdomain.MemoryLevelSession,
			ExpiresAt:       memory.ExpiresAt,
			CreatedAt:       idleBefore.Add(-time.Hour),
			UpdatedAt:       idleBefore.Add(-time.Hour),
		})
		if err := store.exec(ctx, statement.SQL, statement.Params...); err != nil {
			t.Fatalf("insert real sqlite idle memory %d: %v", memory.ID, err)
		}
		if err := store.exec(ctx, `
INSERT INTO vmm_memory_context_edges (
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, 1, 0, ?, 0, ?, ?);
`, memory.ID, "phase", memory.VectorID, idleBefore.UnixMilli(), idleBefore.UnixMilli(), idleBefore.UnixMilli()); err != nil {
			t.Fatalf("insert real sqlite idle context edge %d: %v", memory.ID, err)
		}
	}

	result, err := store.recycleOneSQLiteIdleSession(ctx, session, idleBefore.UnixMilli(), 0, now.UnixMilli(), logicdomain.RecycleReasonIdleSessionCompact)
	if err != nil {
		t.Fatalf("recycleOneSQLiteIdleSession returned error: %v", err)
	}
	if result.BatchID == 0 || result.SessionID != session.ID || result.RecycledMemoryCount != 2 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 2 {
		t.Fatalf("unexpected idle-session recycle result: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 2 || result.RecycledVectorIDs[0] != "vec-idle-real-9001" || result.RecycledVectorIDs[1] != "vec-idle-real-9002" {
		t.Fatalf("unexpected idle-session recycled vector ids: %+v", result.RecycledVectorIDs)
	}

	hotMemoryCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE id IN (?, ?)`, uint64(9001), uint64(9002))
	if err != nil {
		t.Fatalf("count recycled idle hot memories: %v", err)
	}
	trashMemoryCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes_trash WHERE batch_id = ? AND id IN (?, ?)`, result.BatchID, uint64(9001), uint64(9002))
	if err != nil {
		t.Fatalf("count recycled idle memory trash: %v", err)
	}
	hotTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id IN (?, ?)`, uint64(8001), uint64(8002))
	if err != nil {
		t.Fatalf("count recycled idle hot turns: %v", err)
	}
	trashTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records_trash WHERE batch_id = ? AND id IN (?, ?)`, result.BatchID, uint64(8001), uint64(8002))
	if err != nil {
		t.Fatalf("count recycled idle turn trash: %v", err)
	}
	activeMemoryCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE id = ?`, uint64(9003))
	if err != nil {
		t.Fatalf("count preserved idle active memory: %v", err)
	}
	referencedTurnCount, err := store.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id = ?`, uint64(8003))
	if err != nil {
		t.Fatalf("count preserved referenced idle turn: %v", err)
	}
	if hotMemoryCount != 0 || trashMemoryCount != 2 || hotTurnCount != 0 || trashTurnCount != 2 {
		t.Fatalf("expected selected idle rows moved to trash, got hotMemory=%d trashMemory=%d hotTurn=%d trashTurn=%d", hotMemoryCount, trashMemoryCount, hotTurnCount, trashTurnCount)
	}
	if activeMemoryCount != 1 || referencedTurnCount != 1 {
		t.Fatalf("expected active memory and referenced turn to remain, got memory=%d turn=%d", activeMemoryCount, referencedTurnCount)
	}
}

// newRealSQLiteRecycleJobTestStore opens one temporary SQLite store through the packaged dynamic library.
// newRealSQLiteRecycleJobTestStore 用于通过已打包动态库打开一个临时 SQLite 存储。
func newRealSQLiteRecycleJobTestStore(t *testing.T) *Store {
	t.Helper()

	libraryPath := locatePackagedSQLiteLibraryForAdapterTest(t)
	dbPath := filepath.Join(t.TempDir(), "database", "sqlite.db")
	store, err := NewStore(libraryPath, dbPath, 5*time.Second, StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		t.Fatalf("open real sqlite ffi store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown real sqlite ffi store: %v", err)
		}
	})
	return store
}

// locatePackagedSQLiteLibraryForAdapterTest resolves the packaged SQLite library from the repository output directory and skips when it is absent.
// locatePackagedSQLiteLibraryForAdapterTest 用于从仓库 output 目录解析已打包 SQLite 动态库；缺失时跳过测试。
func locatePackagedSQLiteLibraryForAdapterTest(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	libPath := filepath.Join(root, "output", "libs", sqliteAdapterLibraryFileName())
	if _, err := os.Stat(libPath); err != nil {
		t.Skipf("packaged sqlite library not found at %s: %v", libPath, err)
	}
	return libPath
}

// sqliteAdapterLibraryFileName returns the host-specific SQLite FFI library filename.
// sqliteAdapterLibraryFileName 用于返回当前宿主系统对应的 SQLite FFI 动态库文件名。
func sqliteAdapterLibraryFileName() string {
	switch runtime.GOOS {
	case "windows":
		return "vldb_sqlite.dll"
	case "darwin":
		return "libvldb_sqlite.dylib"
	default:
		return "libvldb_sqlite.so"
	}
}
