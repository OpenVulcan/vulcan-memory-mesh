// turn_analyzer_test.go verifies the structured turn-analysis processor and its JSON normalization rules.
// turn_analyzer_test.go 用于验证结构化逐轮分析处理器及其 JSON 归一规则。
package processor

import (
	"context"
	"errors"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestTurnAnalyzerAnalyze verifies the processor loads the dedicated prompt, calls the model, and parses the structured payload into canonical node candidates.
// TestTurnAnalyzerAnalyze 用于验证处理器会加载专用提示词、调用模型，并把结构化载荷解析成规范节点候选。
func TestTurnAnalyzerAnalyze(t *testing.T) {
	llm := &stubTurnAnalyzerLLM{
		response: appports.LLMResponse{
			Content: `{
  "details": "这轮对话明确需要先给出 AI 记忆子项目建议。",
  "memory_nodes": [
    {
      "category": 4,
      "abstract": "当前对话需要先形成 AI 记忆子项目建议。",
      "details": "用户当前诉求是获得该子项目的设计建议。"
    }
  ],
  "profile_nodes": [
    {
      "profile_type": 1,
      "content": "当前项目聚焦 AI 记忆能力设计。"
    }
  ]
}`,
		},
	}
	prompts := &stubTurnAnalyzerPromptSource{prompt: "prompt-body"}
	analyzer := NewTurnAnalyzer(llm, prompts, "qwen3.5-flash")

	analysis, err := analyzer.Analyze(context.Background(), `{"user":"你好","assistant":"收到"}`)
	if err != nil {
		t.Fatalf("analyze turn: %v", err)
	}
	if prompts.scene != "analyze_turn" {
		t.Fatalf("expected analyze_turn scene, got %q", prompts.scene)
	}
	if llm.request.ResponseFormat != appports.LLMResponseFormatJSON {
		t.Fatalf("expected json response format, got %q", llm.request.ResponseFormat)
	}
	if analysis.Details == "" || len(analysis.MemoryNodes) != 1 || len(analysis.ProfileNodes) != 1 {
		t.Fatalf("unexpected analysis payload: %+v", analysis)
	}
	if analysis.MemoryNodes[0].Category != logicdomain.MemoryNodeCategoryRequirementTODO {
		t.Fatalf("unexpected memory category: %+v", analysis.MemoryNodes[0])
	}
	if analysis.ProfileNodes[0].ProfileType != logicdomain.ProfileTypeProject {
		t.Fatalf("unexpected profile node: %+v", analysis.ProfileNodes[0])
	}
}

// TestParseTurnAnalysisResponseRejectsInvalidCategory verifies invalid enum values are surfaced as structured LLM output errors instead of being silently accepted.
// TestParseTurnAnalysisResponseRejectsInvalidCategory 用于验证无效枚举值会被作为结构化 LLM 输出错误暴露，而不是被静默接受。
func TestParseTurnAnalysisResponseRejectsInvalidCategory(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"details":"","memory_nodes":[{"category":99,"abstract":"bad","details":"bad"}],"profile_nodes":[]}`)
	if err == nil {
		t.Fatal("expected invalid category error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}

type stubTurnAnalyzerLLM struct {
	request  appports.LLMRequest
	response appports.LLMResponse
	err      error
}

func (s *stubTurnAnalyzerLLM) Generate(_ context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	s.request = req
	if s.err != nil {
		return appports.LLMResponse{}, s.err
	}
	return s.response, nil
}

type stubTurnAnalyzerPromptSource struct {
	scene  string
	model  string
	prompt string
	err    error
}

func (s *stubTurnAnalyzerPromptSource) GetPrompt(scene, modelName string) (string, error) {
	s.scene = scene
	s.model = modelName
	if s.err != nil {
		return "", s.err
	}
	return s.prompt, nil
}

var (
	_ appports.LLMClient    = (*stubTurnAnalyzerLLM)(nil)
	_ appports.PromptSource = (*stubTurnAnalyzerPromptSource)(nil)
)

// TestTurnAnalyzerPropagatesModelFailure verifies provider-side failures bubble up unchanged so the caller can decide whether to retry or only log.
// TestTurnAnalyzerPropagatesModelFailure 用于验证 provider 侧失败会原样上抛，便于调用方自行决定重试还是仅记录日志。
func TestTurnAnalyzerPropagatesModelFailure(t *testing.T) {
	expected := errors.New("provider failed")
	analyzer := NewTurnAnalyzer(&stubTurnAnalyzerLLM{err: expected}, &stubTurnAnalyzerPromptSource{prompt: "prompt-body"}, "qwen3.5-flash")
	_, err := analyzer.Analyze(context.Background(), `{"user":"你好","assistant":"收到"}`)
	if !errors.Is(err, expected) {
		t.Fatalf("expected provider error to bubble up, got %v", err)
	}
}
