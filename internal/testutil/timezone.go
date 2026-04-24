// timezone.go provides a shared test-only helper for pinning time.Local while date-formatting assertions execute.
// timezone.go 用于提供共享的仅测试辅助函数，在日期格式断言执行期间固定 time.Local。
package testutil

import (
	"sync"
	"testing"
	"time"
)

var (
	// testLocalTimeMu serializes test sections that temporarily replace the process-wide time.Local pointer.
	// testLocalTimeMu 用于串行化那些会临时替换进程级 time.Local 指针的测试片段。
	testLocalTimeMu sync.Mutex
)

// UseFixedLocalTime swaps time.Local to one named location for the lifetime of the current test and restores the original pointer during cleanup.
// UseFixedLocalTime 用于在当前测试生命周期内把 time.Local 切换到指定时区，并在清理阶段恢复原始指针。
func UseFixedLocalTime(tb testing.TB, locationName string) *time.Location {
	tb.Helper()
	location, err := time.LoadLocation(locationName)
	if err != nil {
		tb.Fatalf("load location %q: %v", locationName, err)
	}
	testLocalTimeMu.Lock()
	original := time.Local
	time.Local = location
	tb.Cleanup(func() {
		time.Local = original
		testLocalTimeMu.Unlock()
	})
	return location
}
