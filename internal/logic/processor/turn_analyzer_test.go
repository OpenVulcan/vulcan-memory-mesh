// turn_analyzer_test.go verifies the structured turn-analysis processor and its JSON normalization rules.
// turn_analyzer_test.go 用于验证结构化逐轮分析处理器及其 JSON 归一规则。
package processor

import (
	"context"
	"errors"
	"strings"
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
  "turn_id": 92,
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
  ],
  "superseded_memory_ids": [71]
}`,
		},
	}
	prompts := &stubTurnAnalyzerPromptSource{prompt: strings.Join([]string{
		"# Role",
		"prompt-body",
		"{#TAG REFERENCE_RULE#}",
		"{#TAG ACTIVE_MEMORY_RULE#}",
		"{#TAG DIRECT_WRITE_EXCLUSION_RULE#}",
	}, "\n")}
	analyzer := NewTurnAnalyzer(llm, prompts, "qwen3.5-flash")

	analysis, err := analyzer.Analyze(context.Background(), logicdomain.TurnAnalysisInput{
		ReferenceTurns: []logicdomain.TurnAnalysisReferenceTurn{
			{TurnID: 91, Details: "上一轮已经明确要给出 AI 记忆子项目设计建议。"},
		},
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  92,
			RawTurn: `{"user":"你好","assistant":"收到"}`,
		},
		ActiveMemoryNodes: []logicdomain.TurnAnalysisActiveMemoryNode{
			{MemoryID: 71, SourceTurnID: 80, Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "旧记忆", Details: "旧记忆详情"},
		},
		RecentGRPCMemoryWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 301, ScopeLevel: "PROJECT", Abstract: "已主动写入的记忆", Details: "这条记忆已经由工具写入"},
		},
	})
	if err != nil {
		t.Fatalf("analyze turn: %v", err)
	}
	if prompts.scene != "analyze_turn" {
		t.Fatalf("expected analyze_turn scene, got %q", prompts.scene)
	}
	if llm.request.ResponseFormat != appports.LLMResponseFormatJSON {
		t.Fatalf("expected json response format, got %q", llm.request.ResponseFormat)
	}
	if !strings.Contains(llm.request.UserPrompt, `"reference_turns"`) || !strings.Contains(llm.request.UserPrompt, `"recent_grpc_memory_writes"`) {
		t.Fatalf("expected structured turn-analysis request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "绝对禁止把这些历史摘要重新提炼成当前 turn 的新增记忆或画像") {
		t.Fatalf("expected rendered reference rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "这些事实已经由工具链主动写入") {
		t.Fatalf("expected direct-write exclusion rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if analysis.TurnID != 92 || analysis.Details == "" || len(analysis.MemoryNodes) != 1 || len(analysis.ProfileNodes) != 1 {
		t.Fatalf("unexpected analysis payload: %+v", analysis)
	}
	if analysis.MemoryNodes[0].Category != logicdomain.MemoryNodeCategoryRequirementTODO {
		t.Fatalf("unexpected memory category: %+v", analysis.MemoryNodes[0])
	}
	if analysis.ProfileNodes[0].ProfileType != logicdomain.ProfileTypeProject {
		t.Fatalf("unexpected profile node: %+v", analysis.ProfileNodes[0])
	}
	if len(analysis.SupersededMemoryIDs) != 1 || analysis.SupersededMemoryIDs[0] != 71 {
		t.Fatalf("unexpected superseded memory ids: %+v", analysis.SupersededMemoryIDs)
	}
}

// TestParseTurnAnalysisResponseRejectsInvalidCategory verifies invalid enum values are surfaced as structured LLM output errors instead of being silently accepted.
// TestParseTurnAnalysisResponseRejectsInvalidCategory 用于验证无效枚举值会被作为结构化 LLM 输出错误暴露，而不是被静默接受。
func TestParseTurnAnalysisResponseRejectsInvalidCategory(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"turn_id":1,"details":"","memory_nodes":[{"category":99,"abstract":"bad","details":"bad"}],"profile_nodes":[],"superseded_memory_ids":[]}`)
	if err == nil {
		t.Fatal("expected invalid category error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}

// TestTurnAnalyzerRejectsMismatchedTurnID verifies the model output cannot silently drift onto another target turn when the analyzer already knows which turn it is extracting.
// TestTurnAnalyzerRejectsMismatchedTurnID 用于验证分析器在已知目标 turn 的前提下，不会默默接受模型返回的错误 turn_id。
func TestTurnAnalyzerRejectsMismatchedTurnID(t *testing.T) {
	analyzer := NewTurnAnalyzer(&stubTurnAnalyzerLLM{
		response: appports.LLMResponse{
			Content: `{"turn_id":999,"details":"","memory_nodes":[],"profile_nodes":[],"superseded_memory_ids":[]}`,
		},
	}, &stubTurnAnalyzerPromptSource{prompt: "prompt-body"}, "qwen3.5-flash")

	_, err := analyzer.Analyze(context.Background(), logicdomain.TurnAnalysisInput{
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  12,
			RawTurn: `{"user":"你好","assistant":"收到"}`,
		},
	})
	if err == nil {
		t.Fatal("expected turn mismatch error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}

// TestRenderTurnAnalysisRequestRejectsInvalidRawTurn verifies the request renderer fails early when the target turn payload is not valid JSON.
// TestRenderTurnAnalysisRequestRejectsInvalidRawTurn 用于验证当目标 turn 载荷不是合法 JSON 时，请求渲染器会尽早失败。
func TestRenderTurnAnalysisRequestRejectsInvalidRawTurn(t *testing.T) {
	_, err := renderTurnAnalysisRequest(logicdomain.TurnAnalysisInput{
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  1,
			RawTurn: `not-json`,
		},
	})
	if err == nil {
		t.Fatal("expected invalid raw_turn error")
	}
}

// TestRenderTurnAnalysisSystemPromptDropsUnusedTags verifies optional TAG lines disappear cleanly when the corresponding input sections are absent.
// TestRenderTurnAnalysisSystemPromptDropsUnusedTags 用于验证当对应输入区块不存在时，可选 TAG 行会被干净删除，不会把模板结构打乱。
func TestRenderTurnAnalysisSystemPromptDropsUnusedTags(t *testing.T) {
	rendered := renderTurnAnalysisSystemPrompt(strings.Join([]string{
		"head",
		"{#TAG REFERENCE_RULE#}",
		"{#TAG ACTIVE_MEMORY_RULE#}",
		"{#TAG DIRECT_WRITE_EXCLUSION_RULE#}",
		"tail",
	}, "\n"), logicdomain.TurnAnalysisInput{
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  1,
			RawTurn: `{"user":"你好","assistant":"收到"}`,
		},
	})
	if strings.Contains(rendered, "{#TAG") {
		t.Fatalf("expected tags to be removed, got %s", rendered)
	}
	if strings.Contains(rendered, "绝对禁止把这些历史摘要重新提炼成当前 turn 的新增记忆或画像") {
		t.Fatalf("expected reference rule to be absent, got %s", rendered)
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
	_, err := analyzer.Analyze(context.Background(), logicdomain.TurnAnalysisInput{
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  1,
			RawTurn: `{"user":"你好","assistant":"收到"}`,
		},
	})
	if !errors.Is(err, expected) {
		t.Fatalf("expected provider error to bubble up, got %v", err)
	}
}
