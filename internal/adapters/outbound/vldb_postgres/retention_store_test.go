// retention_store_test.go verifies PostgreSQL retention helpers keep their protected-shared-memory predicate and batch metadata derivation stable without a live database.
// retention_store_test.go 用于验证 PostgreSQL retention 辅助逻辑在无真实数据库时仍能保持共享记忆保护谓词和批次元数据推导的稳定性。
package vldb_postgres

import (
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
		default:
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
	if len(excludedSnapshots) < 2 || len(excludedSnapshots[1]) != 1 || excludedSnapshots[1][0] != 41 {
		t.Fatalf("excluded snapshots = %v", excludedSnapshots)
	}
}

// TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows verifies the session prefilter keeps the stale-memory and old-turn existence checks in one clause so no-op idle sessions stop blocking later useful work.
// TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows 用于验证 session 预过滤会同时包含陈旧记忆和旧 turn 的存在性检查，避免 no-op idle session 阻塞后续有收益的回收工作。
func TestBuildPostgresIdleSessionCandidateAvailabilityClauseRequiresRecyclableRows(t *testing.T) {
	args := &sqlArgsBuilder{}
	store := &Store{cfg: Config{Schema: "public"}}
	clause := buildPostgresIdleSessionCandidateAvailabilityClause(args, store, time.Unix(120, 0).UTC(), 8)
	if !strings.Contains(clause, "FROM "+store.memoryNodesTable()+" AS m") {
		t.Fatalf("candidate availability clause missing stale-memory branch: %q", clause)
	}
	if !strings.Contains(clause, "FROM "+store.turnsTable()+" AS tr") {
		t.Fatalf("candidate availability clause missing old-turn branch: %q", clause)
	}
	if !strings.Contains(clause, "NOT EXISTS (SELECT 1 FROM "+store.profileNodesTable()+" pn WHERE pn.turn_id = tr.id)") {
		t.Fatalf("candidate availability clause missing profile reference guard: %q", clause)
	}
	if got := len(args.Args()); got != 5 {
		t.Fatalf("candidate availability args len = %d, want 5", got)
	}
}
