// intent_extractor_test.go keeps parser-level assertions deterministic while real-model coverage lives in higher-level integration tests.
// intent_extractor_test.go 用于保持解析器级断言的确定性，而真实模型覆盖交由更高层集成测试承担。
package processor

import (
	"errors"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestParseIntentResponseParsesMarkdownJSON verifies that fenced JSON model output is accepted and de-duplicated correctly.
// TestParseIntentResponseParsesMarkdownJSON 用于验证带 fenced code 的 JSON 模型输出能被正确解析并去重。
func TestParseIntentResponseParsesMarkdownJSON(t *testing.T) {
	intent, err := parseIntentResponse("analysis...\n```json\n{\n  \"queries\": [\"go memory 设计\", \"go memory 设计\"],\n  \"need_memory\": true,\n  \"reason\": \"match user question\"\n}\n```\nextra")
	if err != nil {
		t.Fatal(err)
	}
	if !intent.NeedMemory {
		t.Fatal("expected need_memory=true")
	}
	if len(intent.Queries) != 1 {
		t.Fatalf("queries len = %d", len(intent.Queries))
	}
	if intent.Queries[0] != "go memory 设计" {
		t.Fatalf("queries = %#v", intent.Queries)
	}
}

// TestParseIntentResponseRejectsInvalidJSON verifies that malformed payloads are surfaced as InvalidLLMOutputError values.
// TestParseIntentResponseRejectsInvalidJSON 用于验证畸形载荷会被识别为 InvalidLLMOutputError。
func TestParseIntentResponseRejectsInvalidJSON(t *testing.T) {
	_, err := parseIntentResponse("```json\n[1,2,3]\n```")
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidLLMOutputError, got %v", err)
	}
}

// TestStripMarkdownFences verifies that helper-level fence stripping keeps the bare JSON body.
// TestStripMarkdownFences 用于验证围栏剥离辅助函数会保留纯净 JSON 内容。
func TestStripMarkdownFences(t *testing.T) {
	got := stripMarkdownFences("```JSON\n{\"k\":1}\n```")
	if got != "{\"k\":1}" {
		t.Fatalf("got %q", got)
	}
}
