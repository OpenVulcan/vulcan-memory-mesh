// context_timeout_test.go verifies PostgreSQL maintenance timeout helpers so long maintenance reads and writes do not inherit online request budgets.
// context_timeout_test.go 用于验证 PostgreSQL 维护超时辅助方法，确保长维护读取与写入不会继承在线请求预算。
package vldb_postgres

import (
	"context"
	"testing"
	"time"
)

// TestMaintenanceReadContextUsesDedicatedConfiguredTimeout verifies maintenance/admin rebuild reads now honor the dedicated maintenance-tool timeout instead of inheriting the normal online query budget.
// TestMaintenanceReadContextUsesDedicatedConfiguredTimeout 用于验证维护/管理重建读取现在会使用专用维护工具超时，而不是继续继承普通在线查询预算。
func TestMaintenanceReadContextUsesDedicatedConfiguredTimeout(t *testing.T) {
	store := &Store{cfg: Config{QueryTimeout: 5 * time.Second, MaintenanceReadTimeout: 45 * time.Second}}
	ctx, cancel := store.maintenanceReadContext(context.Background())
	defer cancel()

	assertContextDeadlineAtLeast(t, ctx, 44*time.Second)
	assertContextDeadlineAtMost(t, ctx, 46*time.Second)
}

// TestMaintenanceReadContextFallsBackToDefaultBudget verifies direct store construction still falls back to the shipped maintenance-tool default when no dedicated read timeout is provided.
// TestMaintenanceReadContextFallsBackToDefaultBudget 用于验证当未显式提供维护读取超时时，直接构造 store 仍会回退到仓库内建的维护工具默认预算。
func TestMaintenanceReadContextFallsBackToDefaultBudget(t *testing.T) {
	store := &Store{cfg: Config{QueryTimeout: 5 * time.Second}}
	ctx, cancel := store.maintenanceReadContext(context.Background())
	defer cancel()

	assertContextDeadlineAtLeast(t, ctx, 29*time.Second)
	assertContextDeadlineAtMost(t, ctx, 31*time.Second)
}

// TestMaintenanceWriteContextUsesDedicatedConfiguredTimeout verifies destructive maintenance transactions honor the dedicated maintenance-tool write timeout instead of deriving from startup or online request settings.
// TestMaintenanceWriteContextUsesDedicatedConfiguredTimeout 用于验证破坏性维护事务会遵循专用维护工具写超时，而不是继续从启动期或在线请求配置派生。
func TestMaintenanceWriteContextUsesDedicatedConfiguredTimeout(t *testing.T) {
	store := &Store{cfg: Config{QueryTimeout: 5 * time.Second, MaintenanceWriteTimeout: 12 * time.Minute}}
	ctx, cancel := store.maintenanceWriteContext(context.Background())
	defer cancel()

	assertContextDeadlineAtLeast(t, ctx, 11*time.Minute+59*time.Second)
	assertContextDeadlineAtMost(t, ctx, 12*time.Minute+1*time.Second)
}

// assertContextDeadlineAtLeast verifies one derived context keeps at least the expected remaining budget.
// assertContextDeadlineAtLeast 用于验证派生上下文至少保留了期望的剩余预算。
func assertContextDeadlineAtLeast(t *testing.T, ctx context.Context, wantAtLeast time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context deadline")
	}
	remaining := time.Until(deadline)
	if remaining < wantAtLeast {
		t.Fatalf("remaining deadline = %s, want at least %s", remaining, wantAtLeast)
	}
}

// assertContextDeadlineAtMost verifies one derived context does not exceed the expected remaining budget by too much, which keeps floor-based tests stable without relying on an exact timestamp.
// assertContextDeadlineAtMost 用于验证派生上下文不会明显超过期望剩余预算，避免地板类测试依赖精确时间戳。
func assertContextDeadlineAtMost(t *testing.T, ctx context.Context, wantAtMost time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context deadline")
	}
	remaining := time.Until(deadline)
	if remaining > wantAtMost {
		t.Fatalf("remaining deadline = %s, want at most %s", remaining, wantAtMost)
	}
}
