// scratchpad_test.go verifies PostgreSQL scratchpad SQL helpers and debug-clean coverage without requiring a live database connection.
// scratchpad_test.go 用于在无需真实数据库连接的前提下验证 PostgreSQL scratchpad SQL 助手与 debug-clean 覆盖范围。
package vldb_postgres

import (
	"strings"
	"testing"
)

// TestBuildPostgresScratchpadPlanInsertSQLUsesConflictGuard verifies first-writer scratchpad creation now uses INSERT ... ON CONFLICT DO NOTHING RETURNING, so concurrent creators can converge on one canonical row before the loser re-reads it.
// TestBuildPostgresScratchpadPlanInsertSQLUsesConflictGuard 用于验证 scratchpad 首写 SQL 现在采用 INSERT ... ON CONFLICT DO NOTHING RETURNING，让并发创建者先收敛到一条 canonical 行，再由败者回读复用。
func TestBuildPostgresScratchpadPlanInsertSQLUsesConflictGuard(t *testing.T) {
	sql := buildPostgresScratchpadPlanInsertSQL("public.vmm_scratchpad_plans")
	for _, fragment := range []string{
		"INSERT INTO public.vmm_scratchpad_plans",
		"ON CONFLICT (project_id, user_id, session_key) DO NOTHING",
		"RETURNING id, project_id, user_id, session_key, plan_name, plan_name_norm, created_at, updated_at",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("expected scratchpad insert sql to contain %q, got %q", fragment, sql)
		}
	}
}

// TestPostgresManagedTableNamesForDebugCleanIncludesRetentionTables verifies debug-clean covers the current managed retention, recycle, scratchpad, and vector-GC tables so local reset semantics stay aligned with the live schema.
// TestPostgresManagedTableNamesForDebugCleanIncludesRetentionTables 用于验证 debug-clean 已覆盖当前受管的 retention、recycle、scratchpad 与 vector-GC 表，让本地清库语义与真实 schema 持续对齐。
func TestPostgresManagedTableNamesForDebugCleanIncludesRetentionTables(t *testing.T) {
	names := postgresManagedTableNamesForDebugClean()
	joined := strings.Join(names, ",")
	for _, tableName := range []string{
		"vmm_scratchpad_plans",
		"vmm_scratchpad_nodes",
		"vmm_memory_nodes_trash",
		"vmm_memory_context_edges_trash",
		"vmm_turn_records_trash",
		"vmm_recycle_jobs",
		"vmm_recycle_batches",
		"vmm_vector_gc_jobs",
		"vmm_schema_versions",
	} {
		if !strings.Contains(joined, tableName) {
			t.Fatalf("expected debug-clean table list to include %q, got %v", tableName, names)
		}
	}
}
