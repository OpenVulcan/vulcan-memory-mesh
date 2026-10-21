// This file verifies bounded profile lifetimes used by automatic and manual profile replacement.
// 本文件验证自动与手工画像替换使用的有限生命周期。
package usecase

import (
	"fmt"
	"math"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestProfileExpiryCapsBeforeDurationMultiplication verifies long replacement histories reach the contractual lifetime cap without overflowing into an earlier expiry.
// TestProfileExpiryCapsBeforeDurationMultiplication 验证长替换历史达到约定生命周期上限，不会因乘法溢出而提前过期。
func TestProfileExpiryCapsBeforeDurationMultiplication(t *testing.T) {
	base := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		level            int
		initial, maximum time.Duration
	}{
		{logicdomain.ProfileLevelTransient, 72 * time.Hour, 14 * 24 * time.Hour},
		{logicdomain.ProfileLevelSituational, 14 * 24 * time.Hour, 90 * 24 * time.Hour},
		{logicdomain.ProfileLevelStable, 90 * 24 * time.Hour, 365 * 24 * time.Hour},
	} {
		for _, weight := range []int{0, 4096, 65536, math.MaxInt} {
			t.Run(fmt.Sprintf("level_%d_weight_%d", tc.level, weight), func(t *testing.T) {
				want := base.Add(tc.maximum)
				if weight == 0 {
					want = base.Add(tc.initial)
				}
				if got := computeProfileExpiry(base, tc.level, weight); !got.Equal(want) {
					t.Fatalf("expiry = %s, want %s", got, want)
				}
			})
		}
	}
}
