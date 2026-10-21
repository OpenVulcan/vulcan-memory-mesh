// This file verifies provider cooldown headers at the outbound adapter boundary.
// 本文件验证出站适配器边界上的供应商冷却时间响应头。
package ai_key_failover

import (
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"

	"google.golang.org/genai"
)

// TestRetryAfterDurationRejectsUnrepresentableHeaders keeps invalid provider delays out of key scheduling while preserving representable milliseconds, fractional seconds, and HTTP dates.
// TestRetryAfterDurationRejectsUnrepresentableHeaders 防止无效供应商延迟进入密钥调度，同时保留有效毫秒、小数秒和 HTTP 日期。
func TestRetryAfterDurationRejectsUnrepresentableHeaders(t *testing.T) {
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header, value string
		want                time.Duration
		valid               bool
	}{
		{"millisecond_overflow", "Retry-After-Ms", strconv.FormatInt(math.MaxInt64/int64(time.Millisecond)+1, 10), 0, false},
		{"millisecond_positive_wrap", "Retry-After-Ms", "18446744073710", 0, false},
		{"infinite_seconds", "Retry-After", "+Inf", 0, false},
		{"seconds_overflow", "Retry-After", "1e308", 0, false},
		{"sub_nanosecond", "Retry-After", "0.0000000001", 0, false},
		{"millisecond_limit", "Retry-After-Ms", strconv.FormatInt(math.MaxInt64/int64(time.Millisecond), 10), time.Duration(math.MaxInt64/int64(time.Millisecond)) * time.Millisecond, true},
		{"fractional_seconds", "Retry-After", "1.25", 1250 * time.Millisecond, true},
		{"http_date", "Retry-After", now.Add(time.Minute).Format(http.TimeFormat), time.Minute, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(tc.header, tc.value)
			got, valid := retryAfterDuration(headers, now)
			if got != tc.want || valid != tc.valid {
				t.Fatalf("delay = %v, valid = %v; want %v, %v", got, valid, tc.want, tc.valid)
			}
		})
	}
}

// TestRetryAfterInvalidMillisecondsKeepsValidSeconds verifies the documented header precedence only accepts a representable higher-priority delay.
// TestRetryAfterInvalidMillisecondsKeepsValidSeconds 验证高优先级响应头仅在延迟有效时生效，无效毫秒值不覆盖有效秒数。
func TestRetryAfterInvalidMillisecondsKeepsValidSeconds(t *testing.T) {
	headers := http.Header{}
	headers.Set("Retry-After-Ms", "18446744073710")
	headers.Set("Retry-After", "3")
	got, valid := retryAfterDuration(headers, time.Now())
	if !valid || got != 3*time.Second {
		t.Fatalf("delay = %v, valid = %v; want 3s, true", got, valid)
	}
}

// TestGoogleRetryDetailsRejectUnrepresentableDurations verifies structured provider hints obey the same duration range as response headers and preserve the configured cooldown on invalid input.
// TestGoogleRetryDetailsRejectUnrepresentableDurations 验证结构化供应商提示遵守响应头相同的时长范围，无效输入保留已配置冷却时长。
func TestGoogleRetryDetailsRejectUnrepresentableDurations(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		value       float64
		want        time.Duration
	}{
		{"millisecond_wrap", "retry_after_ms", 18446744073710, 77 * time.Second},
		{"seconds_overflow", "retry_after", 1e308, 77 * time.Second},
		{"infinite_milliseconds", "retry_after_ms", math.Inf(1), 77 * time.Second},
		{"milliseconds", "retry_after_ms", 1200, 1200 * time.Millisecond},
		{"seconds", "retry_after", 1.25, 1250 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := &genai.APIError{Details: []map[string]any{{tc.field: tc.value}}}
			got := chooseCooldownFromGoogleError(apiErr, time.Now(), true, 77*time.Second)
			if got != tc.want {
				t.Fatalf("cooldown = %v, want %v", got, tc.want)
			}
		})
	}
}
