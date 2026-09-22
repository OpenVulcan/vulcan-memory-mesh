// native_retry_test.go pins the commit-result boundary shared by native and legacy SQL stores.
// native_retry_test.go 固定原生与旧 SQL 存储共用的提交结果边界。
package vldb_sqlite

import (
	"context"
	"errors"
	"testing"
)

// TestSQLiteRetryPreservesConfirmedSuccessAfterCancellation prevents a completed write from being reported as retryable failure.
// TestSQLiteRetryPreservesConfirmedSuccessAfterCancellation 防止已经完成的写入被报告为可重试失败。
func TestSQLiteRetryPreservesConfirmedSuccessAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &Store{}
	calls := 0
	err := store.withSQLiteRetry(ctx, func() error { calls++; cancel(); return nil })
	if err != nil || calls != 1 {
		t.Fatalf("confirmed operation lost its success: calls=%d, err=%v", calls, err)
	}
}

// TestSQLiteRetryRejectsCancellationBeforeDispatch verifies no operation runs after cancellation is already known.
// TestSQLiteRetryRejectsCancellationBeforeDispatch 验证已知取消后不会再分派数据库操作。
func TestSQLiteRetryRejectsCancellationBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &Store{}
	err := store.withSQLiteRetry(ctx, func() error { t.Fatal("canceled operation was dispatched"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
