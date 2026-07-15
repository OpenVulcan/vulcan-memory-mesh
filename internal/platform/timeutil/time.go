// time.go centralizes UTC normalization for maintenance and compensation timestamps shared across runtime layers.
// time.go 用于集中管理运行时各层维护与补偿时间戳共享的 UTC 规范化逻辑。
package timeutil

import "time"

// UTCOrNow returns the supplied time in UTC, or the current UTC time when the input is zero.
// UTCOrNow 用于返回输入时间对应的 UTC 值；输入为零时间时返回当前 UTC 时间。
func UTCOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
