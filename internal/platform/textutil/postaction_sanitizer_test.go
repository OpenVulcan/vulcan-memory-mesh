// postaction_sanitizer_test.go verifies the storage-oriented post-action sanitizer end-to-end.
// postaction_sanitizer_test.go 用于端到端验证面向存储的 post-action 清洗器。
package textutil

import (
	"strings"
	"testing"
)

// TestCleanConversationTextForMemoryPreservesStructure verifies that media stripping keeps useful line boundaries for later folding.
// TestCleanConversationTextForMemoryPreservesStructure 用于验证媒体清理会保留对后续折叠有用的行边界。
func TestCleanConversationTextForMemoryPreservesStructure(t *testing.T) {
	input := "<think>hidden</think>\n第一行\n![猫](https://cdn.example.com/cat.jpg)\n第二行"
	got := CleanConversationTextForMemory(input)
	want := "第一行\n[图片: 猫]\n第二行"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// TestPostActionTextSanitizerSanitize verifies that the post-action sanitizer removes media noise and clips oversized machine text in one pipeline.
// TestPostActionTextSanitizerSanitize 用于验证 post-action 清洗器会在同一条流水线中去除媒体噪声并裁剪超大机器文本。
func TestPostActionTextSanitizerSanitize(t *testing.T) {
	sanitizer := NewPostActionTextSanitizer()
	input := "<think>hidden</think>\n请看 ![图](https://cdn.example.com/cat.jpg)\n" + strings.Repeat("longword ", 1700)
	got := sanitizer.Sanitize(input)
	if strings.Contains(got, "<think>") {
		t.Fatalf("expected thought tags removed, got %q", got)
	}
	if !strings.Contains(got, "[图片: 图]") {
		t.Fatalf("expected image placeholder, got %q", got)
	}
	if !strings.Contains(got, "[已按 Token 预算截断]") {
		t.Fatalf("expected token budget marker, got %q", got)
	}
}
