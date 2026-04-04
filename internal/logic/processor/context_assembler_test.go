// context_assembler_test.go verifies the shared context assembler keeps main-path memory labels aligned with the fallback pre-check renderer.
// context_assembler_test.go 用于验证共享上下文组装器会让主路径的记忆标题与 pre-check 的 fallback 渲染保持一致。
package processor

import (
	"context"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestContextAssemblerKeepsMemoryTitlesAligned verifies the shared assembler produces the same mixed-memory title in both structured items and summary text, so main-path output stays aligned with fallback output.
// TestContextAssemblerKeepsMemoryTitlesAligned 用于验证共享组装器会在结构化条目和摘要文本里同时输出一致的“混合召回记忆”标题，避免主路径和 fallback 输出再次分叉。
func TestContextAssemblerKeepsMemoryTitlesAligned(t *testing.T) {
	assembler := NewContextAssembler(stubPromptSource{prompt: "ok"}, "test-model")

	contextText, items, err := assembler.Assemble(context.Background(), logicdomain.PersonaContext{}, []logicdomain.MemoryHit{
		{ID: "20", Text: "phase4 新方案\n这是共享 assembler summary 里的记忆内容。", Score: 0.96, Metadata: map[string]string{"turn_id": "88"}},
	})
	if err != nil {
		t.Fatalf("assemble context: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one context item, got %#v", items)
	}
	if items[0].Title != "混合召回记忆" || items[0].Source != "memory" {
		t.Fatalf("expected shared assembler memory item to expose mixed-memory title/source, got %#v", items[0])
	}
	if items[0].TurnID != 88 {
		t.Fatalf("expected shared assembler memory item to expose source turn id, got %#v", items[0])
	}
	if contextText == "" {
		t.Fatal("expected non-empty context summary")
	}
	if contextText != "[混合召回记忆]\n1. phase4 新方案\n这是共享 assembler summary 里的记忆内容。 (score=0.960)" {
		t.Fatalf("unexpected context summary: %q", contextText)
	}
}

// stubPromptSource is the minimal prompt-source double used by context assembler tests.
// stubPromptSource 用于作为 context assembler 测试里的最小提示词来源桩。
type stubPromptSource struct {
	prompt string
	err    error
}

// GetPrompt executes the stubbed prompt lookup logic.
// GetPrompt 用于执行桩化的提示词读取逻辑。
func (s stubPromptSource) GetPrompt(scene, modelName string) (string, error) {
	_ = scene
	_ = modelName
	return s.prompt, s.err
}
