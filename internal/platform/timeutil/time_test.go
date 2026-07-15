// time_test.go characterizes the shared UTC normalization contract used by runtime maintenance paths.
// time_test.go 用于刻画运行时维护路径共同遵循的 UTC 规范化契约。
package timeutil

import (
	"testing"
	"time"
)

// TestUTCOrNowNormalizesProvidedTime verifies non-zero timestamps retain their instant while moving to UTC.
// TestUTCOrNowNormalizesProvidedTime 用于验证非零时间戳在转换为 UTC 时保持同一时刻。
func TestUTCOrNowNormalizesProvidedTime(t *testing.T) {
	input := time.Date(2026, time.July, 15, 12, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	got := UTCOrNow(input)
	if !got.Equal(input) || got.Location() != time.UTC {
		t.Fatalf("UTCOrNow() = %v in %v, want same instant in UTC", got, got.Location())
	}
}

// TestUTCOrNowSuppliesCurrentTime verifies zero timestamps become bounded current UTC values.
// TestUTCOrNowSuppliesCurrentTime 用于验证零时间戳会变成有界的当前 UTC 时间。
func TestUTCOrNowSuppliesCurrentTime(t *testing.T) {
	before := time.Now().UTC()
	got := UTCOrNow(time.Time{})
	after := time.Now().UTC()
	if got.Before(before) || got.After(after) || got.Location() != time.UTC {
		t.Fatalf("UTCOrNow(zero) = %v, want current UTC time between %v and %v", got, before, after)
	}
}
