// retention_store_test.go verifies PostgreSQL retention helpers keep their protected-shared-memory predicate and batch metadata derivation stable without a live database.
// retention_store_test.go 用于验证 PostgreSQL retention 辅助逻辑在无真实数据库时仍能保持共享记忆保护谓词和批次元数据推导的稳定性。
package vldb_postgres

import (
	"strings"
	"testing"

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
