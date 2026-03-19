// intent_extractor_test.go implements reusable business processors.
// intent_extractor_test.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"errors"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// testPromptSource is a deterministic prompt source used by intent extractor unit tests.
// testPromptSource 用于作为意图提取器单元测试中的确定性提示词来源。
type testPromptSource struct{}

// GetPrompt executes the GetPrompt logic.
// GetPrompt 用于执行 GetPrompt 逻辑。
func (testPromptSource) GetPrompt(scene, modelName string) (string, error) {
	return "prompt:" + scene + ":" + modelName, nil
}

// testLLMClient is a controllable LLM test double used to feed structured and malformed responses.
// testLLMClient 用于作为可控的 LLM 测试替身，注入结构化或异常响应。
type testLLMClient struct {
	content string
	err     error
}

// Generate executes the Generate logic.
// Generate 用于执行 Generate 逻辑。
func (c testLLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.err != nil {
		return appports.LLMResponse{}, c.err
	}
	return appports.LLMResponse{Content: c.content}, nil
}

// TestIntentExtractorParsesVikingStyleMarkdownJSON verifies the TestIntentExtractorParsesVikingStyleMarkdownJSON behavior.
// TestIntentExtractorParsesVikingStyleMarkdownJSON 用于验证 TestIntentExtractorParsesVikingStyleMarkdownJSON 行为。
func TestIntentExtractorParsesVikingStyleMarkdownJSON(t *testing.T) {
	extractor := NewIntentExtractor(testLLMClient{content: "analysis...\n```json\n{\n  \"keywords\": [\"go\", \"memory\", \"go\"],\n  \"need_memory\": true,\n  \"reason\": \"match user question\"\n}\n```\nextra"}, testPromptSource{}, "mock", 5)
	intent, err := extractor.Extract(context.Background(), []logicdomain.HistorySnippet{{Role: "user", Content: "hello"}}, "go memory")
	if err != nil {
		t.Fatal(err)
	}
	if !intent.NeedMemory {
		t.Fatal("expected need_memory=true")
	}
	if len(intent.Keywords) != 2 {
		t.Fatalf("keywords len = %d", len(intent.Keywords))
	}
	if intent.Keywords[0] != "go" || intent.Keywords[1] != "memory" {
		t.Fatalf("keywords = %#v", intent.Keywords)
	}
}

// TestIntentExtractorRejectsInvalidJSON verifies the TestIntentExtractorRejectsInvalidJSON behavior.
// TestIntentExtractorRejectsInvalidJSON 用于验证 TestIntentExtractorRejectsInvalidJSON 行为。
func TestIntentExtractorRejectsInvalidJSON(t *testing.T) {
	extractor := NewIntentExtractor(testLLMClient{content: "```json\n[1,2,3]\n```"}, testPromptSource{}, "mock", 5)
	_, err := extractor.Extract(context.Background(), nil, "hello")
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidLLMOutputError, got %v", err)
	}
}

// TestStripMarkdownFences verifies the TestStripMarkdownFences behavior.
// TestStripMarkdownFences 用于验证 TestStripMarkdownFences 行为。
func TestStripMarkdownFences(t *testing.T) {
	got := stripMarkdownFences("```JSON\n{\"k\":1}\n```")
	if got != "{\"k\":1}" {
		t.Fatalf("got %q", got)
	}
}
