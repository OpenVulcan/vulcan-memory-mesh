// retention_store_test.go verifies PostgreSQL retention helpers keep their protected-shared-memory predicate and batch metadata derivation stable without a live database.
// retention_store_test.go 用于验证 PostgreSQL retention 辅助逻辑在无真实数据库时仍能保持共享记忆保护谓词和批次元数据推导的稳定性。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestAppendProtectedSharedMemoryRecycleFilterAppendsPredicate verifies the shared-memory guard is only added when the config explicitly keeps protection enabled.
// TestAppendProtectedSharedMemoryRecycleFilterAppendsPredicate 用于验证只有在配置明确开启保护时，才会追加共享记忆保护谓词。
func TestAppendProtectedSharedMemoryRecycleFilterAppendsPredicate(t *testing.T) {
	args := &sqlArgsBuilder{}
	whereClauses := appendProtectedSharedMemoryRecycleFilter([]string{"m.memory_status = 1"}, args, logicdomain.MemoryRecycleQuery{
		ProtectPriorityFloor:        logicdomain.MemoryPriorityP1,
		ProtectMemoryLevelFloor:     logicdomain.MemoryLevelStable,
		SkipProtectedSharedMemories: true,
	}, "m")

	if len(whereClauses) != 2 {
		t.Fatalf("where clauses = %v", whereClauses)
	}
	if !strings.Contains(whereClauses[1], "NOT (m.scope_level <> $1") {
		t.Fatalf("protected predicate = %q", whereClauses[1])
	}
	if got := args.Args(); len(got) != 3 {
		t.Fatalf("predicate args = %v", got)
	}
}

// TestSharedRecycleProjectIDCollapsesMixedProjectBatches verifies recycle batches only keep one project id when every recycled row belongs to the same project.
// TestSharedRecycleProjectIDCollapsesMixedProjectBatches 用于验证只有整批回收行都属于同一 project 时，回收批次才会保留该 project id。
func TestSharedRecycleProjectIDCollapsesMixedProjectBatches(t *testing.T) {
	if got := sharedRecycleProjectID(nil); got != 0 {
		t.Fatalf("sharedRecycleProjectID(nil) = %d, want 0", got)
	}
	sameProject := sharedRecycleProjectID([]memoryNodeScanRow{
		{ProjectID: 7},
		{ProjectID: 7},
	})
	if sameProject != 7 {
		t.Fatalf("same project batch id = %d, want 7", sameProject)
	}
	mixedProject := sharedRecycleProjectID([]memoryNodeScanRow{
		{ProjectID: 7},
		{ProjectID: 8},
	})
	if mixedProject != 0 {
		t.Fatalf("mixed project batch id = %d, want 0", mixedProject)
	}
}

// TestBuildPostgresIdleSessionTurnReferenceClauseIgnoresRecycledMemoryIDs verifies idle-session turn recycle can treat the current batch's soon-to-be-deleted session memories as non-blocking references.
// TestBuildPostgresIdleSessionTurnReferenceClauseIgnoresRecycledMemoryIDs 用于验证 idle-session turn 回收会把本批即将删除的 session 记忆视为非阻塞引用。
func TestBuildPostgresIdleSessionTurnReferenceClauseIgnoresRecycledMemoryIDs(t *testing.T) {
	args := &sqlArgsBuilder{}
	clause := buildPostgresIdleSessionTurnReferenceClause(args, "public.vmm_memory_nodes", []uint64{11, 12})
	if !strings.Contains(clause, "NOT (mn.id = ANY($1))") {
		t.Fatalf("idle-session reference clause = %q", clause)
	}
	if got := args.Args(); len(got) != 1 {
		t.Fatalf("idle-session predicate args = %v", got)
	}
}

// TestCollectPostgresIdleSessionRecyclePassSkipsNoOpSessions verifies one idle-session pass can skip a no-op oldest session and still continue to later recyclable batches instead of stopping at the first empty candidate.
// TestCollectPostgresIdleSessionRecyclePassSkipsNoOpSessions 用于验证一次 idle-session 回收会跳过最老但 no-op 的 session，并继续处理后续真正可回收的批次，而不是在第一个空候选处直接停止。
func TestCollectPostgresIdleSessionRecyclePassSkipsNoOpSessions(t *testing.T) {
	callCount := 0
	excludedSnapshots := make([][]uint64, 0, 3)
	result, err := collectPostgresIdleSessionRecyclePass(2, func(excludedSessionIDs []uint64) (postgresIdleSessionRecycleResult, error) {
		excludedSnapshots = append(excludedSnapshots, append([]uint64(nil), excludedSessionIDs...))
		callCount++
		switch callCount {
		case 1:
			return postgresIdleSessionRecycleResult{SessionID: 41}, nil
		case 2:
			if len(excludedSessionIDs) != 1 || excludedSessionIDs[0] != 41 {
				t.Fatalf("second recycle call excluded sessions = %v, want [41]", excludedSessionIDs)
			}
			return postgresIdleSessionRecycleResult{
				BatchID:              81,
				SessionID:            42,
				RecycledMemoryCount:  2,
				RecycledContextCount: 1,
				RecycledTurnCount:    3,
				RecycledVectorIDs:    []string{"vec-1"},
			}, nil
		case 3:
			if len(excludedSessionIDs) != 1 || excludedSessionIDs[0] != 41 {
				t.Fatalf("third recycle call excluded sessions = %v, want [41]", excludedSessionIDs)
			}
			return postgresIdleSessionRecycleResult{}, nil
		default:
			t.Fatalf("unexpected extra recycle call #%d with excluded sessions %v", callCount, excludedSessionIDs)
			return postgresIdleSessionRecycleResult{}, nil
		}
	})
	if err != nil {
		t.Fatalf("collectPostgresIdleSessionRecyclePass returned error: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("recycle call count = %d, want 3", callCount)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 81 {
		t.Fatalf("batch ids = %v, want [81]", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 42 {
		t.Fatalf("session ids = %v, want [42]", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 2 || result.RecycledContextCount != 1 || result.RecycledTurnCount != 3 {
		t.Fatalf("unexpected recycle stats: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-1" {
		t.Fatalf("vector ids = %v, want [vec-1]", result.RecycledVectorIDs)
	}
	if len(excludedSnapshots) < 3 ||
		len(excludedSnapshots[1]) != 1 || excludedSnapshots[1][0] != 41 ||
		len(excludedSnapshots[2]) != 1 || excludedSnapshots[2][0] != 41 {
		t.Fatalf("excluded snapshots = %v", excludedSnapshots)
	}
}

// TestCollectPostgresIdleSessionRecyclePassAllowsBoundedExtraInspection verifies one recycle pass still reaches a later recyclable session when limit=1 and the first candidate becomes no-op inside the concurrent race window.
// TestCollectPostgresIdleSessionRecyclePassAllowsBoundedExtraInspection 用于验证当 limit=1 且第一个候选在并发窗口内变成 no-op 时，回收过程仍能利用有界额外检查预算继续命中后续可回收 session。
func TestCollectPostgresIdleSessionRecyclePassAllowsBoundedExtraInspection(t *testing.T) {
	callCount := 0
	result, err := collectPostgresIdleSessionRecyclePass(1, func(excludedSessionIDs []uint64) (postgresIdleSessionRecycleResult, error) {
		callCount++
		switch callCount {
		case 1:
			if len(excludedSessionIDs) != 0 {
				t.Fatalf("first recycle call excluded sessions = %v, want []", excludedSessionIDs)
			}
			return postgresIdleSessionRecycleResult{SessionID: 41}, nil
		case 2:
			if len(excludedSessionIDs) != 1 || excludedSessionIDs[0] != 41 {
				t.Fatalf("second recycle call excluded sessions = %v, want [41]", excludedSessionIDs)
			}
			return postgresIdleSessionRecycleResult{
				BatchID:              82,
				SessionID:            42,
				RecycledMemoryCount:  1,
				RecycledContextCount: 2,
				RecycledTurnCount:    3,
				RecycledVectorIDs:    []string{"vec-2"},
			}, nil
		default:
			t.Fatalf("unexpected extra recycle call #%d with excluded sessions %v", callCount, excludedSessionIDs)
			return postgresIdleSessionRecycleResult{}, nil
		}
	})
	if err != nil {
		t.Fatalf("collectPostgresIdleSessionRecyclePass returned error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("recycle call count = %d, want 2", callCount)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 82 {
		t.Fatalf("batch ids = %v, want [82]", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 42 {
		t.Fatalf("session ids = %v, want [42]", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 3 {
		t.Fatalf("unexpected recycle stats: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 1 || result.RecycledVectorIDs[0] != "vec-2" {
		t.Fatalf("vector ids = %v, want [vec-2]", result.RecycledVectorIDs)
	}
}

// TestPostgresIdleSessionInspectionBudgetAddsBoundedHeadroom verifies the inspection budget always keeps a small fixed amount of extra room beyond the requested batch limit so race-window no-op sessions do not immediately halt the pass.
// TestPostgresIdleSessionInspectionBudgetAddsBoundedHeadroom 用于验证检查预算总会在批量限制之外保留少量固定余量，避免并发窗口里的 no-op session 立刻终止整轮回收。
func TestPostgresIdleSessionInspectionBudgetAddsBoundedHeadroom(t *testing.T) {
	if got := postgresIdleSessionInspectionBudget(0); got != 0 {
		t.Fatalf("inspection budget for 0 = %d, want 0", got)
	}
	if got := postgresIdleSessionInspectionBudget(1); got != 5 {
		t.Fatalf("inspection budget for 1 = %d, want 5", got)
	}
	if got := postgresIdleSessionInspectionBudget(32); got != 36 {
		t.Fatalf("inspection budget for 32 = %d, want 36", got)
	}
}

// TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows verifies the session prefilter keeps the stale-memory and old-turn existence checks in one clause so no-op idle sessions stop blocking later useful work.
// TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows 用于验证 session 预过滤会同时包含陈旧记忆和旧 turn 的存在性检查，避免 no-op idle session 阻塞后续有收益的回收工作。
func TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows(t *testing.T) {
	args := &sqlArgsBuilder{}
	repository := &retentionRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	clause := buildPostgresIdleSessionCandidateAvailabilityClause(args, repository, time.Unix(120, 0).UTC(), 8)
	if !strings.Contains(clause, "FROM "+repository.memoryNodesTable()+" AS m") {
		t.Fatalf("candidate availability clause missing stale-memory branch: %q", clause)
	}
	if !strings.Contains(clause, "FROM "+repository.turnsTable()+" AS tr") {
		t.Fatalf("candidate availability clause missing old-turn branch: %q", clause)
	}
	if !strings.Contains(clause, "NOT EXISTS (SELECT 1 FROM "+repository.profileNodesTable()+" pn WHERE pn.turn_id = tr.id)") {
		t.Fatalf("candidate availability clause missing profile reference guard: %q", clause)
	}
	if got := len(args.Args()); got != 5 {
		t.Fatalf("candidate availability args len = %d, want 5", got)
	}
}

// TestBuildPostgresRecycleBatchDeleteSQLDeletesMetadata verifies the purge tail step now removes recycle-batch metadata rows entirely instead of leaving one ever-growing bookkeeping table behind after trash rows are gone.
// TestBuildPostgresRecycleBatchDeleteSQLDeletesMetadata 用于验证 purge 末尾现在会直接删除回收批次元数据，而不是在 trash 行清空后继续保留一个持续增长的台账表。
func TestBuildPostgresRecycleBatchDeleteSQLDeletesMetadata(t *testing.T) {
	sql := buildPostgresRecycleBatchDeleteSQL("public.vmm_recycle_batches")
	if !strings.Contains(sql, "DELETE FROM public.vmm_recycle_batches") {
		t.Fatalf("delete recycle batch sql = %q", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "UPDATE ") {
		t.Fatalf("delete recycle batch sql should not update metadata rows: %q", sql)
	}
}

// TestBuildPostgresDeleteCompletedVectorGCJobsSQLDeletesRows verifies successful vector-gc completion now hard-deletes retry rows instead of keeping an ever-growing completed ledger.
// TestBuildPostgresDeleteCompletedVectorGCJobsSQLDeletesRows 用于验证向量 GC 成功完成后现在会直接删除重试队列行，而不是保留一张持续增长的已完成台账。
func TestBuildPostgresDeleteCompletedVectorGCJobsSQLDeletesRows(t *testing.T) {
	sql := buildPostgresDeleteCompletedVectorGCJobsSQL("public.vmm_vector_gc_jobs")
	if !strings.Contains(sql, "DELETE FROM public.vmm_vector_gc_jobs") {
		t.Fatalf("delete completed vector gc jobs sql = %q", sql)
	}
	if strings.Contains(strings.ToUpper(sql), "UPDATE ") {
		t.Fatalf("delete completed vector gc jobs sql should not update metadata rows: %q", sql)
	}
}

// TestRequirePostgresVectorGCJobRowsAffectedRejectsDrift verifies vector-GC completion and retry updates cannot silently miss claimed jobs.
// TestRequirePostgresVectorGCJobRowsAffectedRejectsDrift 用于验证 vector-GC 完成和重试更新不能静默漏掉已领取的 job。
func TestRequirePostgresVectorGCJobRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresVectorGCJobRowsAffected("complete postgres vector gc jobs", 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresVectorGCJobRowsAffected("retry postgres vector gc jobs", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect vector-GC row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry postgres vector gc jobs affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestRequirePostgresRecycleJobRowsAffectedRejectsDrift verifies cold-turn recycle completion and retry updates cannot silently miss claimed jobs.
// TestRequirePostgresRecycleJobRowsAffectedRejectsDrift 用于验证冷 turn 回收完成和重试更新不能静默漏掉已领取的 job。
func TestRequirePostgresRecycleJobRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresRecycleJobRowsAffected("complete postgres recycle jobs", 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresRecycleJobRowsAffected("retry postgres recycle jobs", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect recycle-job row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "retry postgres recycle jobs affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestPostgresClaimCommitErrorMarksOutcomeUncertainOnlyAfterLeases verifies claim commits are uncertain only when a returned lease row may already be durable.
// TestPostgresClaimCommitErrorMarksOutcomeUncertainOnlyAfterLeases 用于验证只有已返回租约行可能持久化后，claim 提交错误才会标记为结果不确定。
func TestPostgresClaimCommitErrorMarksOutcomeUncertainOnlyAfterLeases(t *testing.T) {
	// claimedErr represents a failed commit after at least one lease row was returned by UPDATE ... RETURNING.
	// claimedErr 表示 UPDATE ... RETURNING 已返回至少一条租约行后的提交失败。
	claimedErr := postgresClaimCommitError("claim recycle jobs", "commit postgres recycle-job claim tx", 1, errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(claimedErr) {
		t.Fatalf("expected claimed lease commit to be outcome-uncertain, got %v", claimedErr)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(claimedErr) {
		t.Fatalf("did not expect claim commit ambiguity to mark fresh-vector uncertainty, got %v", claimedErr)
	}

	// emptyErr represents a failed commit after the claim query returned no lease rows.
	// emptyErr 表示 claim 查询未返回任何租约行后的提交失败。
	emptyErr := postgresClaimCommitError("claim vector gc jobs", "commit postgres vector gc claim tx", 0, errors.New("commit acknowledgement lost"))
	if logicdomain.IsOutcomeUncertain(emptyErr) {
		t.Fatalf("did not expect no-op claim commit to be outcome-uncertain, got %v", emptyErr)
	}
	if !strings.Contains(emptyErr.Error(), "commit postgres vector gc claim tx: commit acknowledgement lost") {
		t.Fatalf("unexpected no-op claim error detail: %v", emptyErr)
	}
}

// TestPostgresQueueMutationCommitErrorMarksOutcomeUncertain verifies terminal queue updates report ambiguous commits without pretending to know the final queue state.
// TestPostgresQueueMutationCommitErrorMarksOutcomeUncertain 用于验证终态队列更新在提交结果不明时会上报结果不确定，而不是假装已知最终队列状态。
func TestPostgresQueueMutationCommitErrorMarksOutcomeUncertain(t *testing.T) {
	// err represents a failed commit after a queue completion or retry update already matched every intended row.
	// err 表示队列完成或重试更新已经命中全部目标行后的提交失败。
	err := postgresCommitOutcomeUncertainError("complete vector gc jobs", "commit postgres vector gc completion tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected queue mutation commit to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect queue mutation commit to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres vector gc completion tx: commit acknowledgement lost") {
		t.Fatalf("unexpected queue mutation commit error detail: %v", err)
	}
}

// TestRequirePostgresColdTurnRowsAffectedRejectsDrift verifies cold-turn recycle cannot commit when trash copy or hot-table delete misses selected turns.
// TestRequirePostgresColdTurnRowsAffectedRejectsDrift 用于验证当 trash 复制或热表删除漏掉已选 turn 时，cold-turn 回收不能继续提交。
func TestRequirePostgresColdTurnRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresColdTurnRowsAffected("copy postgres cold turns into trash", 42, 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresColdTurnRowsAffected("delete postgres cold turns", 42, 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect cold-turn row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete postgres cold turns for session 42 affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestPostgresColdTurnRecycleCommitErrorMarksOutcomeUncertain verifies cold-turn archive commits expose ambiguous trash-copy and hot-table delete state.
// TestPostgresColdTurnRecycleCommitErrorMarksOutcomeUncertain 用于验证冷 turn 归档提交会暴露 trash 复制与热表删除结果不确定。
func TestPostgresColdTurnRecycleCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := postgresColdTurnRecycleCommitOutcomeUncertainError(42, errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain cold-turn recycle commit error, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect cold-turn recycle commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres cold-turn recycle tx for session 42: commit acknowledgement lost") {
		t.Fatalf("unexpected cold-turn recycle commit error detail: %v", err)
	}
}

// TestRequirePostgresColdMemoryRowsAffectedRejectsDrift verifies cold-memory recycle cannot commit when memory or context rows drift after selection.
// TestRequirePostgresColdMemoryRowsAffectedRejectsDrift 用于验证当 memory 或 context 行在选定后发生漂移时，cold-memory 回收不能继续提交。
func TestRequirePostgresColdMemoryRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresColdMemoryRowsAffected("copy postgres cold memories to trash", 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresColdMemoryRowsAffected("delete postgres cold memories", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect cold-memory row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete postgres cold memories affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestRequirePostgresIdleSessionRowsAffectedRejectsDrift verifies idle-session recycle cannot commit when copied or deleted rows drift inside the transaction.
// TestRequirePostgresIdleSessionRowsAffectedRejectsDrift 用于验证当事务内复制或删除行数漂移时，idle-session 回收不能继续提交。
func TestRequirePostgresIdleSessionRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresIdleSessionRowsAffected("copy postgres idle-session turns to trash", 42, 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresIdleSessionRowsAffected("delete postgres idle-session memories", 42, 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect idle-session row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete postgres idle-session memories for session 42 affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestCollectPostgresIdleSessionRecyclePassPreservesErroredSessionResult verifies commit-uncertain session results still expose vector cleanup coordinates to the usecase.
// TestCollectPostgresIdleSessionRecyclePassPreservesErroredSessionResult 用于验证提交结果不明的 session 回收结果仍会向用例层暴露向量清理坐标。
func TestCollectPostgresIdleSessionRecyclePassPreservesErroredSessionResult(t *testing.T) {
	// uncertainErr represents a commit boundary where PostgreSQL may have already archived the idle-session rows.
	// uncertainErr 表示 PostgreSQL 可能已经归档 idle-session 行后的提交结果不明边界。
	uncertainErr := logicdomain.OutcomeUncertainError{Operation: "recycle idle-session rows", Message: "commit acknowledgement lost"}
	result, err := collectPostgresIdleSessionRecyclePass(1, func(_ []uint64) (postgresIdleSessionRecycleResult, error) {
		return postgresIdleSessionRecycleResult{
			BatchID:              9,
			SessionID:            11,
			RecycledMemoryCount:  1,
			RecycledContextCount: 2,
			RecycledTurnCount:    3,
			RecycledVectorIDs:    []string{"vec-idle-1", "vec-idle-2"},
		}, uncertainErr
	})
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain recycle error, got %v", err)
	}
	if len(result.BatchIDs) != 1 || result.BatchIDs[0] != 9 {
		t.Fatalf("batch ids = %+v, want [9]", result.BatchIDs)
	}
	if len(result.SessionIDs) != 1 || result.SessionIDs[0] != 11 {
		t.Fatalf("session ids = %+v, want [11]", result.SessionIDs)
	}
	if result.RecycledMemoryCount != 1 || result.RecycledContextCount != 2 || result.RecycledTurnCount != 3 {
		t.Fatalf("unexpected recycle counts: %+v", result)
	}
	if len(result.RecycledVectorIDs) != 2 || result.RecycledVectorIDs[0] != "vec-idle-1" || result.RecycledVectorIDs[1] != "vec-idle-2" {
		t.Fatalf("vector ids = %+v, want idle cleanup coordinates", result.RecycledVectorIDs)
	}
}

// TestRequirePostgresTrashPurgeRowsAffectedRejectsDrift verifies permanent trash purge cannot commit when trash or batch metadata deletes drift.
// TestRequirePostgresTrashPurgeRowsAffectedRejectsDrift 用于验证当 trash 行或批次元数据删除漂移时，永久清理不能继续提交。
func TestRequirePostgresTrashPurgeRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresTrashPurgeRowsAffected("delete postgres memory trash rows", 2, 2); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}
	err := requirePostgresTrashPurgeRowsAffected("delete postgres recycle batch metadata", 1, 2)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect trash purge row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "delete postgres recycle batch metadata affected 1 rows, want 2") {
		t.Fatalf("unexpected row drift error: %v", err)
	}
}

// TestPostgresTrashPurgeCommitErrorMarksOutcomeUncertain verifies permanent-delete commit ambiguity reports the final trash state as uncertain without fresh-vector retention.
// TestPostgresTrashPurgeCommitErrorMarksOutcomeUncertain 用于验证永久删除提交结果不明时会报告最终 trash 状态不确定，但不会标记新向量保留。
func TestPostgresTrashPurgeCommitErrorMarksOutcomeUncertain(t *testing.T) {
	// err represents a failed commit after selected trash rows and batch metadata may already be permanently deleted.
	// err 表示已选 trash 行与批次元数据可能已经永久删除后的提交失败。
	err := postgresCommitOutcomeUncertainError("purge expired trash", "commit postgres trash purge tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected trash purge commit to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect trash purge commit to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres trash purge tx: commit acknowledgement lost") {
		t.Fatalf("unexpected trash purge commit error detail: %v", err)
	}
}

// TestBuildPostgresColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns verifies the independent cold-turn scan only queues sessions whose old turns are both outside the hot window and free from memory/profile references.
// TestBuildPostgresColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns 用于验证独立冷 turn 扫描只会为“超出热窗口且没有记忆/画像引用”的旧 turn 所在 session 入队。
func TestBuildPostgresColdTurnJobSessionAvailabilityClauseRequiresUnreferencedTurns(t *testing.T) {
	args := &sqlArgsBuilder{}
	repository := &retentionRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	clause := buildPostgresColdTurnJobSessionAvailabilityClause(args, repository, 8)
	if !strings.Contains(clause, "LIMIT 8") {
		t.Fatalf("cold-turn availability clause missing hot-window limit: %q", clause)
	}
	if !strings.Contains(clause, "FROM "+repository.memoryNodesTable()+" mn WHERE mn.source_turn_id = tr.id") {
		t.Fatalf("cold-turn availability clause missing memory reference guard: %q", clause)
	}
	if !strings.Contains(clause, "FROM "+repository.profileNodesTable()+" pn WHERE pn.turn_id = tr.id") {
		t.Fatalf("cold-turn availability clause missing profile reference guard: %q", clause)
	}
	if got := len(args.Args()); got != 1 {
		t.Fatalf("cold-turn availability args len = %d, want 1", got)
	}
}

// TestBuildPostgresClaimPendingRecycleJobsSQLUsesSkipLocked verifies the recycle-job claim SQL keeps the SKIP LOCKED lease semantics so concurrent maintenance workers do not consume the same cold-turn job twice.
// TestBuildPostgresClaimPendingRecycleJobsSQLUsesSkipLocked 用于验证回收任务领取 SQL 保留了 SKIP LOCKED 租约语义，避免并发维护工作器重复消费同一冷 turn 任务。
func TestBuildPostgresClaimPendingRecycleJobsSQLUsesSkipLocked(t *testing.T) {
	sql := buildPostgresClaimPendingRecycleJobsSQL("public.vmm_recycle_jobs")
	if !strings.Contains(sql, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("claim recycle jobs sql missing SKIP LOCKED: %q", sql)
	}
	if !strings.Contains(sql, "UPDATE public.vmm_recycle_jobs AS jobs") {
		t.Fatalf("claim recycle jobs sql missing queue update target: %q", sql)
	}
	if !strings.Contains(sql, "RETURNING jobs.id, jobs.session_id") {
		t.Fatalf("claim recycle jobs sql missing returned job payload: %q", sql)
	}
}
