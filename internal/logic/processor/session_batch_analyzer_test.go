// session_batch_analyzer_test.go verifies the queued session-batch analysis processor and its JSON normalization rules.
// session_batch_analyzer_test.go 用于验证排队式 session 批处理分析器及其 JSON 归一规则。
package processor

import (
	"context"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestSessionBatchAnalyzerAnalyze verifies the processor loads the dedicated batch scene, calls the model once, and parses the structured payload into canonical per-turn results.
// TestSessionBatchAnalyzerAnalyze 用于验证处理器会加载专用批处理场景、调用模型一次，并把结构化载荷解析成规范的逐 turn 结果。
func TestSessionBatchAnalyzerAnalyze(t *testing.T) {
	llm := &stubTurnAnalyzerLLM{
		response: appports.LLMResponse{
			Content: `{
  "turn_results": [
    {
      "turn_id": 101,
      "details": "第一条待处理 turn 的精要。",
      "memory_nodes": [
        {
          "category": 4,
          "abstract": "当前对话需要先明确 AI 记忆项目的设计建议。",
          "details": "用户当前诉求是获得 AI 记忆项目的方案建议。"
        }
      ],
      "profile_nodes": [
        {
          "profile_type": 1,
          "content": "当前项目仍处于设计阶段。"
        }
      ]
    },
    {
      "turn_id": 102,
      "details": "",
      "memory_nodes": [],
      "profile_nodes": []
    }
  ],
  "obsolete_memory_turn_ids": [88]
}`,
		},
	}
	prompts := &stubTurnAnalyzerPromptSource{prompt: "return json only"}
	analyzer := NewSessionBatchAnalyzer(llm, prompts, "qwen3.5-flash")

	analysis, err := analyzer.Analyze(context.Background(), `{"pending_turns":[]}`)
	if err != nil {
		t.Fatalf("analyze session batch: %v", err)
	}
	if prompts.scene != "analyze_session_batch" {
		t.Fatalf("expected analyze_session_batch scene, got %q", prompts.scene)
	}
	if llm.request.ResponseFormat != appports.LLMResponseFormatJSON {
		t.Fatalf("expected json response format, got %q", llm.request.ResponseFormat)
	}
	if len(analysis.Turns) != 2 {
		t.Fatalf("unexpected turn count: %+v", analysis)
	}
	if analysis.Turns[0].TurnID != 101 || analysis.Turns[1].TurnID != 102 {
		t.Fatalf("unexpected turn ids: %+v", analysis.Turns)
	}
	if analysis.Turns[0].MemoryNodes[0].Category != logicdomain.MemoryNodeCategoryRequirementTODO {
		t.Fatalf("unexpected memory node: %+v", analysis.Turns[0].MemoryNodes[0])
	}
	if len(analysis.ObsoleteMemoryTurnIDs) != 1 || analysis.ObsoleteMemoryTurnIDs[0] != 88 {
		t.Fatalf("unexpected obsolete turn ids: %+v", analysis.ObsoleteMemoryTurnIDs)
	}
}

// TestParseSessionBatchAnalysisResponseRejectsDuplicateTurn verifies duplicate turn ids are surfaced as structured output errors instead of being silently accepted.
// TestParseSessionBatchAnalysisResponseRejectsDuplicateTurn 用于验证重复 turn id 会被作为结构化输出错误暴露，而不是被静默接受。
func TestParseSessionBatchAnalysisResponseRejectsDuplicateTurn(t *testing.T) {
	_, err := parseSessionBatchAnalysisResponse(`{
  "turn_results": [
    {"turn_id": 101, "details": "", "memory_nodes": [], "profile_nodes": []},
    {"turn_id": 101, "details": "", "memory_nodes": [], "profile_nodes": []}
  ],
  "obsolete_memory_turn_ids": []
}`)
	if err == nil {
		t.Fatal("expected duplicate turn id error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}
