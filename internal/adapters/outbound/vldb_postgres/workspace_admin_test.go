// workspace_admin_test.go verifies PostgreSQL workspace-admin guards that keep destructive and migration plans deterministic without requiring a live database.
// workspace_admin_test.go 用于验证 PostgreSQL workspace 管理路径在无真实数据库时仍能保持删除与迁移计划确定性。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// sequenceWorkspaceCountQueryer replays COUNT(*) rows for workspace-admin planning helpers and records the emitted SQL.
// sequenceWorkspaceCountQueryer 用于向 workspace 管理规划 helper 回放 COUNT(*) 行，并记录实际发出的 SQL。
type sequenceWorkspaceCountQueryer struct {
	args   [][]any
	counts []int
	sql    []string
}

// Exec should stay unused in workspace planning helper tests.
// Exec 在 workspace 规划 helper 测试中不应被调用。
func (q *sequenceWorkspaceCountQueryer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec call")
}

// Query should stay unused in workspace planning helper tests.
// Query 在 workspace 规划 helper 测试中不应被调用。
func (q *sequenceWorkspaceCountQueryer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query call")
}

// QueryRow records one COUNT query and returns the next configured count row.
// QueryRow 用于记录一次 COUNT 查询，并返回下一条预设计数行。
func (q *sequenceWorkspaceCountQueryer) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.sql = append(q.sql, sql)
	q.args = append(q.args, append([]any(nil), args...))
	if len(q.counts) == 0 {
		return sequenceWorkspaceCountRow{err: fmt.Errorf("unexpected QueryRow call for %q", sql)}
	}
	value := q.counts[0]
	q.counts = q.counts[1:]
	return sequenceWorkspaceCountRow{value: value}
}

// sequenceWorkspaceCountRow scans one configured integer count into PostgreSQL countRows destinations.
// sequenceWorkspaceCountRow 用于把一条预设计数扫描到 PostgreSQL countRows 的目标变量中。
type sequenceWorkspaceCountRow struct {
	err   error
	value int
}

// Scan writes the configured count into the only supported destination type used by countRows.
// Scan 用于把预设计数写入 countRows 使用的唯一支持目标类型。
func (r sequenceWorkspaceCountRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("scan destination count = %d, want 1", len(dest))
	}
	target, ok := dest[0].(*int)
	if !ok {
		return fmt.Errorf("unsupported count scan target %T", dest[0])
	}
	*target = r.value
	return nil
}

// TestRequirePostgresWorkspaceRowsAffectedRejectsDrift verifies workspace-admin writes cannot report success when planned row counts drift.
// TestRequirePostgresWorkspaceRowsAffectedRejectsDrift 用于验证 workspace 管理写入在计划行数漂移时不能报告成功。
func TestRequirePostgresWorkspaceRowsAffectedRejectsDrift(t *testing.T) {
	if err := requirePostgresWorkspaceRowsAffected("delete postgres project memory nodes", 4, 4); err != nil {
		t.Fatalf("expected matching row count to succeed, got %v", err)
	}

	err := requirePostgresWorkspaceRowsAffected("migrate postgres project profile nodes", 0, 1)
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("did not expect workspace row drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "migrate postgres project profile nodes affected 0 rows, want 1") {
		t.Fatalf("unexpected workspace row drift error: %v", err)
	}
}

// TestPostgresWorkspaceCommitErrorMarksOutcomeUncertain verifies project-admin commit ambiguity reports relational outcome uncertainty without fresh-vector retention.
// TestPostgresWorkspaceCommitErrorMarksOutcomeUncertain 用于验证项目管理提交结果不明时会上报关系结果不确定，但不会标记新向量保留。
func TestPostgresWorkspaceCommitErrorMarksOutcomeUncertain(t *testing.T) {
	// err represents a failed commit after project delete or migration rows may already be durable.
	// err 表示项目删除或迁移行可能已经持久化后的提交失败。
	err := postgresCommitOutcomeUncertainError("migrate project path", "commit postgres migrate-project tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected workspace commit to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect workspace commit ambiguity to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres migrate-project tx: commit acknowledgement lost") {
		t.Fatalf("unexpected workspace commit error detail: %v", err)
	}
}

// TestPostgresUserConfirmationCommitErrorMarksOutcomeUncertain verifies a generated confirmation code commit ambiguity reports the durable code state as uncertain.
// TestPostgresUserConfirmationCommitErrorMarksOutcomeUncertain 用于验证生成确认码后的提交结果不明会报告持久确认码状态不确定。
func TestPostgresUserConfirmationCommitErrorMarksOutcomeUncertain(t *testing.T) {
	// err represents a failed commit after the guarded confirmation-code update may already be durable.
	// err 表示带条件的确认码更新可能已经持久化后的提交失败。
	err := postgresCommitOutcomeUncertainError("confirm user deletion", "commit postgres user confirmation tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected user confirmation commit to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect user confirmation commit to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres user confirmation tx: commit acknowledgement lost") {
		t.Fatalf("unexpected user confirmation commit error detail: %v", err)
	}
}

// TestResolvePostgresEnsureUserNameInsertConflictRejectsUnexpectedError verifies user creation does not hide a real INSERT failure behind a later successful lookup.
// TestResolvePostgresEnsureUserNameInsertConflictRejectsUnexpectedError 用于验证用户创建不会用后续成功查询掩盖真实 INSERT 故障。
func TestResolvePostgresEnsureUserNameInsertConflictRejectsUnexpectedError(t *testing.T) {
	resolverCalled := false
	_, err := resolvePostgresEnsureUserNameInsertConflict("alice", fmt.Errorf("insert unavailable"), func() (logicdomain.UserRecord, error) {
		resolverCalled = true
		return logicdomain.UserRecord{ID: 7, Name: "alice"}, nil
	})
	if err == nil {
		t.Fatal("expected unexpected insert error to fail")
	}
	if resolverCalled {
		t.Fatal("unexpected insert errors must not trigger conflict resolution")
	}
	if !strings.Contains(err.Error(), "create postgres user: insert unavailable") {
		t.Fatalf("unexpected insert error detail: %v", err)
	}
}

// TestResolvePostgresEnsureUserNameInsertConflictLoadsConcurrentWinner verifies the expected no-row insert outcome resolves the concurrently created user.
// TestResolvePostgresEnsureUserNameInsertConflictLoadsConcurrentWinner 用于验证预期的无返回行插入结果会解析并发创建出的用户。
func TestResolvePostgresEnsureUserNameInsertConflictLoadsConcurrentWinner(t *testing.T) {
	resolverCalled := false
	result, err := resolvePostgresEnsureUserNameInsertConflict("alice", pgx.ErrNoRows, func() (logicdomain.UserRecord, error) {
		resolverCalled = true
		return logicdomain.UserRecord{ID: 7, Name: "alice"}, nil
	})
	if err != nil {
		t.Fatalf("resolvePostgresEnsureUserNameInsertConflict returned error: %v", err)
	}
	if !resolverCalled {
		t.Fatal("expected conflict resolver to load the concurrent winner")
	}
	if !result.Exists || result.User.ID != 7 || result.User.Name != "alice" {
		t.Fatalf("unexpected conflict winner result: %+v", result)
	}
}

// TestBuildPostgresEnsureSessionInsertSQLPreservesConflictUpdatedAt verifies concurrent session creation returns the existing row without refreshing idle-recycle metadata.
// TestBuildPostgresEnsureSessionInsertSQLPreservesConflictUpdatedAt 用于验证并发 session 创建会返回既有行，但不会刷新 idle 回收依赖的时间元数据。
func TestBuildPostgresEnsureSessionInsertSQLPreservesConflictUpdatedAt(t *testing.T) {
	sql := buildPostgresEnsureSessionInsertSQL(`"public"."vmm_sessions"`)
	if !strings.Contains(sql, `INSERT INTO "public"."vmm_sessions" AS existing`) {
		t.Fatalf("ensure-session insert SQL missing target alias: %s", sql)
	}
	if !strings.Contains(sql, "DO UPDATE SET updated_at = existing.updated_at") {
		t.Fatalf("ensure-session conflict branch should preserve updated_at: %s", sql)
	}
	if strings.Contains(sql, "EXCLUDED.updated_at") {
		t.Fatalf("ensure-session conflict branch must not refresh updated_at: %s", sql)
	}
}

// TestPlanPostgresProjectMigrationCountsProfileNodes verifies migration plans include project-bound profile nodes even though the public summary only exposes sessions, messages, and memories.
// TestPlanPostgresProjectMigrationCountsProfileNodes 用于验证迁移计划会纳入项目绑定画像节点，即使公开摘要只暴露 session、message 和 memory 数量。
func TestPlanPostgresProjectMigrationCountsProfileNodes(t *testing.T) {
	repo := &workspaceRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	queryer := &sequenceWorkspaceCountQueryer{counts: []int{2, 5, 3, 1}}

	plan, err := repo.planProjectMigration(context.Background(), queryer, 101)
	if err != nil {
		t.Fatalf("planProjectMigration returned error: %v", err)
	}
	if plan.MigratedSessions != 2 || plan.MigratedMessages != 5 || plan.MigratedMemories != 3 || plan.MigratedProfiles != 1 {
		t.Fatalf("unexpected migration plan: %+v", plan)
	}
	if len(queryer.sql) != 4 {
		t.Fatalf("expected four migration count queries, got %d", len(queryer.sql))
	}
	profileSQL := queryer.sql[3]
	if !strings.Contains(profileSQL, repo.profileNodesTable()) || !strings.Contains(profileSQL, "profile_type = $1") || !strings.Contains(profileSQL, "bind_id = $2") {
		t.Fatalf("profile migration count SQL = %q", profileSQL)
	}
	profileArgs := queryer.args[3]
	if len(profileArgs) != 2 || profileArgs[0] != logicdomain.ProfileTypeProject || profileArgs[1] != int64(101) {
		t.Fatalf("profile migration count args = %#v", profileArgs)
	}
}

// TestPlanPostgresUserDeleteCountsSharedProfileDetach verifies user-delete plans include surviving shared-scope profiles that must be detached before turn deletion.
// TestPlanPostgresUserDeleteCountsSharedProfileDetach 用于验证用户删除计划会纳入删除 turn 前必须脱钩的共享范围画像节点。
func TestPlanPostgresUserDeleteCountsSharedProfileDetach(t *testing.T) {
	repo := &workspaceRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	queryer := &sequenceWorkspaceCountQueryer{counts: []int{2, 5, 3, 4, 6}}

	plan, err := repo.planUserDelete(context.Background(), queryer, 7)
	if err != nil {
		t.Fatalf("planUserDelete returned error: %v", err)
	}
	if plan.DeletedSessions != 2 || plan.DeletedMessages != 5 || plan.DeletedMemories != 3 || plan.DeletedProfiles != 4 || plan.DetachedProfiles != 6 {
		t.Fatalf("unexpected user delete plan: %+v", plan)
	}
	if len(queryer.sql) != 5 {
		t.Fatalf("expected five user-delete count queries, got %d", len(queryer.sql))
	}
	detachSQL := queryer.sql[4]
	if !strings.Contains(detachSQL, repo.profileNodesTable()) || !strings.Contains(detachSQL, repo.turnsTable()) || !strings.Contains(detachSQL, repo.sessionsTable()) {
		t.Fatalf("shared-profile detach count SQL = %q", detachSQL)
	}
	if !strings.Contains(detachSQL, "profile_type <> $1") || !strings.Contains(detachSQL, "se.user_id = $2") {
		t.Fatalf("shared-profile detach count predicate = %q", detachSQL)
	}
	detachArgs := queryer.args[4]
	if len(detachArgs) != 2 || detachArgs[0] != logicdomain.ProfileTypeUser || detachArgs[1] != int64(7) {
		t.Fatalf("shared-profile detach count args = %#v", detachArgs)
	}
}

// TestBuildPostgresUserSharedProfileDetachUpdateUsesSharedPredicate verifies the detach UPDATE keeps SET parameters before the shared count/update predicate.
// TestBuildPostgresUserSharedProfileDetachUpdateUsesSharedPredicate 用于验证共享画像脱钩 UPDATE 会把 SET 参数放在共用 count/update 谓词之前。
func TestBuildPostgresUserSharedProfileDetachUpdateUsesSharedPredicate(t *testing.T) {
	repo := &workspaceRepository{shared: &storeShared{cfg: Config{Schema: "public"}}}
	now := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)

	sql, args := repo.buildUserSharedProfileDetachUpdateSQL(7, now)
	for _, fragment := range []string{
		"source_kind = $1",
		"source_id = $2",
		"status_reason = $3",
		"updated_at = $4",
		"profile_type <> $5",
		"se.user_id = $6",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("detach update SQL missing %q: %s", fragment, sql)
		}
	}
	if len(args) != 6 {
		t.Fatalf("detach update args len = %d, want 6: %#v", len(args), args)
	}
	updatedAt, ok := args[3].(time.Time)
	if !ok || !updatedAt.Equal(now) {
		t.Fatalf("detach update timestamp arg = %#v", args[3])
	}
	if args[0] != logicdomain.ProfileSourceKindRetainedAfterUserDelete ||
		args[1] != int64(7) ||
		args[2] != "source user deleted; shared scope node retained without original turn binding" ||
		args[4] != logicdomain.ProfileTypeUser ||
		args[5] != int64(7) {
		t.Fatalf("detach update args = %#v", args)
	}
}
