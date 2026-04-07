// memory_store_timeout_test.go verifies project-memory listing timeout selection so online paths keep their normal query budget while maintenance flows can opt into longer reads.
// memory_store_timeout_test.go 用于验证项目记忆枚举的超时选择，确保在线路径继续使用常规查询预算，而维护流程可以显式切到更长读取预算。
package vldb_postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestListProjectMemoriesWithQueryerUsesOnlineQueryTimeout verifies the shared project-memory listing helper keeps normal online callers on the standard query timeout budget.
// TestListProjectMemoriesWithQueryerUsesOnlineQueryTimeout 用于验证共享项目记忆枚举辅助逻辑会让普通在线调用方继续走标准 query timeout 预算。
func TestListProjectMemoriesWithQueryerUsesOnlineQueryTimeout(t *testing.T) {
	store := &Store{cfg: Config{Schema: "public", QueryTimeout: 5 * time.Second, MaintenanceReadTimeout: 45 * time.Second}}
	queryer := &captureProjectMemoryQueryer{}

	records, err := store.listProjectMemoriesWithQueryerAndContextBuilder(context.Background(), queryer, store.queryContext, 7)
	if err != nil {
		t.Fatalf("listProjectMemoriesWithQueryerAndContextBuilder returned error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no rows, got %+v", records)
	}
	assertContextDeadlineAtLeast(t, queryer.lastCtx, 4*time.Second)
	assertContextDeadlineAtMost(t, queryer.lastCtx, 6*time.Second)
}

// TestListProjectMemoriesWithQueryerUsesMaintenanceReadTimeout verifies maintenance callers can explicitly switch the same project-memory listing helper onto the dedicated maintenance read timeout.
// TestListProjectMemoriesWithQueryerUsesMaintenanceReadTimeout 用于验证维护调用方可以显式把同一套项目记忆枚举辅助逻辑切到专用 maintenance read timeout。
func TestListProjectMemoriesWithQueryerUsesMaintenanceReadTimeout(t *testing.T) {
	store := &Store{cfg: Config{Schema: "public", QueryTimeout: 5 * time.Second, MaintenanceReadTimeout: 45 * time.Second}}
	queryer := &captureProjectMemoryQueryer{}

	records, err := store.listProjectMemoriesWithQueryerAndContextBuilder(context.Background(), queryer, store.maintenanceReadContext, 9)
	if err != nil {
		t.Fatalf("listProjectMemoriesWithQueryerAndContextBuilder returned error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected no rows, got %+v", records)
	}
	assertContextDeadlineAtLeast(t, queryer.lastCtx, 44*time.Second)
	assertContextDeadlineAtMost(t, queryer.lastCtx, 46*time.Second)
}

// captureProjectMemoryQueryer records the query context chosen by the project-memory listing helper so timeout-selection tests can assert which budget was applied without a live database.
// captureProjectMemoryQueryer 用于记录项目记忆枚举辅助逻辑选中的查询上下文，让超时测试无需真实数据库也能断言具体套用了哪一类预算。
type captureProjectMemoryQueryer struct {
	lastCtx  context.Context
	lastSQL  string
	lastArgs []any
}

// Exec should stay unused in these timeout-selection tests.
// Exec 在这些超时选择测试中不应被调用。
func (*captureProjectMemoryQueryer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec call")
}

// Query records the chosen context and returns one empty row set so the caller can finish the scan path without touching a live database.
// Query 用于记录被选中的上下文，并返回空结果集，让调用方在不接触真实数据库的情况下走完扫描路径。
func (q *captureProjectMemoryQueryer) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.lastCtx = ctx
	q.lastSQL = sql
	q.lastArgs = append([]any(nil), args...)
	return emptyProjectMemoryRows{}, nil
}

// QueryRow should stay unused in these timeout-selection tests.
// QueryRow 在这些超时选择测试中不应被调用。
func (*captureProjectMemoryQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return unexpectedProjectMemoryRow{}
}

// unexpectedProjectMemoryRow fails immediately when a QueryRow-based path is accidentally used in these tests.
// unexpectedProjectMemoryRow 用于在测试意外走到 QueryRow 路径时立即失败。
type unexpectedProjectMemoryRow struct{}

// Scan always reports misuse because QueryRow is not part of the project-memory listing path under test.
// Scan 会始终报错，因为当前测试覆盖的项目记忆枚举路径不应依赖 QueryRow。
func (unexpectedProjectMemoryRow) Scan(...any) error {
	return fmt.Errorf("unexpected QueryRow scan")
}

// emptyProjectMemoryRows is the minimal pgx.Rows stub needed by timeout-selection tests that only care about the context used to start the query.
// emptyProjectMemoryRows 用于提供超时选择测试所需的最小 pgx.Rows 存根；这些测试只关心查询启动时使用的上下文。
type emptyProjectMemoryRows struct{}

// Close is a no-op because the stub never owns external resources.
// Close 为空操作，因为该存根不持有外部资源。
func (emptyProjectMemoryRows) Close() {}

// Err reports no row-iteration failure.
// Err 用于报告“没有行迭代错误”。
func (emptyProjectMemoryRows) Err() error { return nil }

// CommandTag returns one zero tag because these tests never inspect row-count metadata.
// CommandTag 用于返回零值标签，因为这些测试不会检查行数元数据。
func (emptyProjectMemoryRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

// FieldDescriptions returns nil because the stub never exposes actual columns.
// FieldDescriptions 返回 nil，因为该存根不会暴露真实列定义。
func (emptyProjectMemoryRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

// Next always reports the empty result set immediately.
// Next 会立刻报告空结果集。
func (emptyProjectMemoryRows) Next() bool { return false }

// Scan fails because the empty-row stub should never be scanned.
// Scan 会报错，因为空结果集存根不应进入扫描阶段。
func (emptyProjectMemoryRows) Scan(...any) error {
	return fmt.Errorf("unexpected Scan call on empty rows")
}

// Values reports no decoded values because the stub contains no rows.
// Values 用于报告“没有可解码值”，因为该存根不包含任何行。
func (emptyProjectMemoryRows) Values() ([]any, error) { return nil, nil }

// RawValues returns nil because there is no backing row payload.
// RawValues 返回 nil，因为这里不存在底层行数据。
func (emptyProjectMemoryRows) RawValues() [][]byte { return nil }

// Conn returns nil because the stub is detached from a live pgx connection.
// Conn 返回 nil，因为该存根并未绑定真实 pgx 连接。
func (emptyProjectMemoryRows) Conn() *pgx.Conn { return nil }
