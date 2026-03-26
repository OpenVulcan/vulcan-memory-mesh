// token_budget_test.go verifies token estimation and clipping behavior used by the post-action storage sanitizer.
// token_budget_test.go 用于验证 post-action 存储清洗器使用的 token 估算与裁剪行为。
package textutil

import (
	"strings"
	"testing"
)

// TestEnforceTokenBudgetClipsOversizedText verifies that oversized text is clipped with the configured marker.
// TestEnforceTokenBudgetClipsOversizedText 用于验证超大文本会按配置标记被裁剪。
func TestEnforceTokenBudgetClipsOversizedText(t *testing.T) {
	cfg := DefaultTokenBudgetConfig()
	cfg.MaxTokens = 200
	input := "标题\n" + strings.Repeat("A", 4000)
	got := EnforceTokenBudget(input, cfg)
	if !strings.Contains(got, "[已按 Token 预算截断]") {
		t.Fatalf("expected token budget marker, got %q", got)
	}
	if got == input {
		t.Fatal("expected clipped output to differ from input")
	}
}
