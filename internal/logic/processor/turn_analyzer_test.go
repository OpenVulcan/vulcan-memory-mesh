// turn_analyzer_test.go verifies the structured turn-analysis processor and its JSON normalization rules.
// turn_analyzer_test.go 用于验证结构化逐轮分析处理器及其 JSON 归一规则。
package processor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestPostActionL1PromptKeepsUserProfileCandidatesDespiteDirectWrites verifies direct-write memory exclusions cannot suppress user-stated profile candidates.
// TestPostActionL1PromptKeepsUserProfileCandidatesDespiteDirectWrites 用于验证主动写入记忆排斥规则不会压制用户主动陈述产生的画像候选。
func TestPostActionL1PromptKeepsUserProfileCandidatesDespiteDirectWrites(t *testing.T) {
	// Read the checked-in prompt files directly so the regression test protects the runtime prompt contract, not only a test stub.
	// 直接读取仓库内置提示词文件，让回归测试保护运行时提示词契约，而不只是保护测试桩内容。
	promptFiles := map[string]struct {
		path      string
		fragments []string
	}{
		"cn": {
			path: filepath.Join("..", "..", "..", "configs", "prompts", "default_cn", "postaction_l1_main.md"),
			fragments: []string{
				"只能排除重复的 `memory_nodes`",
				"不得因此排除用户主动陈述、确认或纠正产生的稳定 `profile_nodes` 候选",
			},
		},
		"en": {
			path: filepath.Join("..", "..", "..", "configs", "prompts", "default_en", "postaction_l1_main.md"),
			fragments: []string{
				"exclude only duplicate `memory_nodes`",
				"do not exclude stable `profile_nodes` candidates",
			},
		},
	}
	for name, promptFile := range promptFiles {
		body, err := os.ReadFile(filepath.Clean(promptFile.path))
		if err != nil {
			t.Fatalf("read %s postaction_l1_main prompt: %v", name, err)
		}
		prompt := string(body)
		for _, fragment := range promptFile.fragments {
			if !strings.Contains(prompt, fragment) {
				t.Fatalf("expected %s prompt to contain %q, got %s", name, fragment, prompt)
			}
		}
	}
}

// TestTurnAnalyzerAnalyze verifies the processor loads the dedicated prompt, calls the model, and parses the structured payload into canonical node candidates.
// TestTurnAnalyzerAnalyze 用于验证处理器会加载专用提示词、调用模型，并把结构化载荷解析成规范节点候选。
func TestTurnAnalyzerAnalyze(t *testing.T) {
	llm := &stubTurnAnalyzerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "user_input_kind": "question",
  "turn_id": 92,
  "details": "这轮对话明确需要先给出 AI 记忆子项目建议。",
  "memory_nodes": [
    {
      "category": 4,
      "abstract": "当前对话需要先形成 AI 记忆子项目建议。",
      "details": "用户当前诉求是获得该子项目的设计建议。",
      "evidence_source": "assistant_external_research",
      "admission": "keep",
      "admission_reason": "",
      "context_edges": [
        {
          "context_key": "task_stage",
          "context_value": "phase4",
          "relation": "support"
        },
        {
          "context_key": "deployment-mode",
          "context_value": "local oss",
          "relation": "rebuttal"
        }
      ]
    }
  ],
  "profile_nodes": [
    {
      "profile_type": 1,
      "content": "当前项目聚焦 AI 记忆能力设计。",
      "evidence_source": "user_confirmed",
      "admission": "keep",
      "admission_reason": ""
    }
  ]
}`,
		},
	}
	prompts := &stubTurnAnalyzerPromptSource{prompt: strings.Join([]string{
		"# Role",
		"prompt-body",
		"`reference_turns` 只帮助理解语境，不能作为新事实来源。",
		"`recent_grpc_memory_writes` 只用于排除重复。",
	}, "\n")}
	analyzer := NewTurnAnalyzer(llm, prompts, "qwen3.5-flash")

	analysis, err := analyzer.Analyze(context.Background(), logicdomain.TurnAnalysisInput{
		CurrentTimestamp: 1775000009000,
		ReferenceTurns: []logicdomain.TurnAnalysisReferenceTurn{
			{TurnID: 91, Details: "上一轮已经明确要给出 AI 记忆子项目设计建议。"},
		},
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:           92,
			CreatedTimestamp: 1775000000000,
			RawTurn:          `{"user":"你好","assistant":"收到"}`,
		},
		RecentGRPCMemoryWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 301, ScopeLevel: "PROJECT", Abstract: "已主动写入的记忆", Details: "这条记忆已经由工具写入", CreatedTimestamp: 1774990000000},
		},
	})
	if err != nil {
		t.Fatalf("analyze turn: %v", err)
	}
	if prompts.scene != "postaction_l1_main" {
		t.Fatalf("expected postaction_l1_main scene, got %q", prompts.scene)
	}
	if llm.request.ResponseFormat != logicports.LLMResponseFormatJSON {
		t.Fatalf("expected json response format, got %q", llm.request.ResponseFormat)
	}
	if !strings.Contains(llm.request.UserPrompt, `"reference_turns"`) || !strings.Contains(llm.request.UserPrompt, `"recent_grpc_memory_writes"`) {
		t.Fatalf("expected structured turn-analysis request body, got %s", llm.request.UserPrompt)
	}
	currentDateTime, _ := logicdomain.FormatDisplayTimeFromUnixMillis(1775000009000)
	targetDateTime, _ := logicdomain.FormatDisplayTimeFromUnixMillis(1775000000000)
	writeDateTime, _ := logicdomain.FormatDisplayTimeFromUnixMillis(1774990000000)
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	for _, fragment := range []string{
		`"current_time"`,
		`"datetime":"` + currentDateTime + `"`,
		`"created_datetime":"` + targetDateTime + `"`,
		`"created_datetime":"` + writeDateTime + `"`,
	} {
		if !strings.Contains(llm.request.UserPrompt, fragment) {
			t.Fatalf("expected structured turn-analysis request body to contain %s, got %s", fragment, llm.request.UserPrompt)
		}
	}
	for _, removed := range []string{`"date":"`, `"created_date":"`} {
		if strings.Contains(llm.request.UserPrompt, removed) {
			t.Fatalf("expected structured turn-analysis request body to stop exposing %s, got %s", removed, llm.request.UserPrompt)
		}
	}
	if strings.Contains(llm.request.UserPrompt, `"timestamp":`) || strings.Contains(llm.request.UserPrompt, `"created_timestamp":`) {
		t.Fatalf("expected structured turn-analysis request body to stop exposing raw timestamps, got %s", llm.request.UserPrompt)
	}
	if strings.Contains(llm.request.UserPrompt, `"active_memory_nodes"`) {
		t.Fatalf("expected active memory nodes to be removed from turn-analysis request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "`reference_turns` 只帮助理解语境，不能作为新事实来源。") {
		t.Fatalf("expected static reference rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "`recent_grpc_memory_writes` 只用于排除重复。") {
		t.Fatalf("expected static direct-write exclusion rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if strings.Contains(llm.request.SystemPrompt, "{#TAG") {
		t.Fatalf("expected static system prompt without tag placeholders, got %s", llm.request.SystemPrompt)
	}
	if strings.Contains(llm.request.SystemPrompt, "Unified Output Language Rule") {
		t.Fatalf("expected shared language policy injection to be removed, got %s", llm.request.SystemPrompt)
	}
	if analysis.UserInputKind != logicdomain.TurnAnalysisUserInputQuestion {
		t.Fatalf("unexpected user input kind: %+v", analysis)
	}
	if analysis.TurnID != 92 || analysis.Details == "" || len(analysis.MemoryNodes) != 1 || len(analysis.ProfileNodes) != 1 {
		t.Fatalf("unexpected analysis payload: %+v", analysis)
	}
	if analysis.MemoryNodes[0].Category != logicdomain.MemoryNodeCategoryRequirementTODO {
		t.Fatalf("unexpected memory category: %+v", analysis.MemoryNodes[0])
	}
	if len(analysis.MemoryNodes[0].ContextEdges) != 2 {
		t.Fatalf("expected context edges to be parsed, got %+v", analysis.MemoryNodes[0].ContextEdges)
	}
	if analysis.MemoryNodes[0].EvidenceSource != logicdomain.TurnAnalysisEvidenceSourceAssistantExternalResearch || analysis.MemoryNodes[0].Admission != logicdomain.TurnAnalysisAdmissionKeep {
		t.Fatalf("unexpected memory node admission metadata: %+v", analysis.MemoryNodes[0])
	}
	if analysis.MemoryNodes[0].ContextEdges[0].Relation != logicdomain.MemoryContextRelationSupport || analysis.MemoryNodes[0].ContextEdges[1].Relation != logicdomain.MemoryContextRelationRebuttal {
		t.Fatalf("unexpected context edge relations: %+v", analysis.MemoryNodes[0].ContextEdges)
	}
	if analysis.ProfileNodes[0].ProfileType != logicdomain.ProfileTypeProject {
		t.Fatalf("unexpected profile node: %+v", analysis.ProfileNodes[0])
	}
	if analysis.ProfileNodes[0].EvidenceSource != logicdomain.TurnAnalysisEvidenceSourceUserConfirmed || analysis.ProfileNodes[0].Admission != logicdomain.TurnAnalysisAdmissionKeep {
		t.Fatalf("unexpected profile node admission metadata: %+v", analysis.ProfileNodes[0])
	}
}

// TestParseTurnAnalysisResponseRejectsInvalidCategory verifies invalid enum values are surfaced as structured LLM output errors instead of being silently accepted.
// TestParseTurnAnalysisResponseRejectsInvalidCategory 用于验证无效枚举值会被作为结构化 LLM 输出错误暴露，而不是被静默接受。
func TestParseTurnAnalysisResponseRejectsInvalidCategory(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"user_input_kind":"mixed","turn_id":1,"details":"","memory_nodes":[{"category":99,"abstract":"bad","details":"bad","evidence_source":"mixed","admission":"keep"}],"profile_nodes":[]}`)
	if err == nil {
		t.Fatal("expected invalid category error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}

// TestParseTurnAnalysisResponseRejectsInvalidContextRelation verifies unsupported context-edge relations are rejected instead of leaking malformed evidence into persistence.
// TestParseTurnAnalysisResponseRejectsInvalidContextRelation 用于验证不支持的 context-edge relation 会被拒绝，避免畸形证据漏进持久化层。
func TestParseTurnAnalysisResponseRejectsInvalidContextRelation(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"user_input_kind":"mixed","turn_id":1,"details":"","memory_nodes":[{"category":4,"abstract":"bad","details":"bad","evidence_source":"mixed","admission":"keep","context_edges":[{"context_key":"stage","context_value":"phase4","relation":"unknown"}]}],"profile_nodes":[]}`)
	if err == nil {
		t.Fatal("expected invalid context relation error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}

// TestParseTurnAnalysisResponseNormalizesEquivalentContextValues verifies semantically equivalent context values collapse onto one canonical durable label instead of forking into multiple edge candidates.
// TestParseTurnAnalysisResponseNormalizesEquivalentContextValues 用于验证语义等价的 context value 会折叠成同一个规范长期标签，而不会分裂成多个 edge 候选。
func TestParseTurnAnalysisResponseNormalizesEquivalentContextValues(t *testing.T) {
	analysis, err := parseTurnAnalysisResponse(`{
		"user_input_kind": "mixed",
		"turn_id": 1,
		"details": "",
		"memory_nodes": [{
			"category": 4,
			"abstract": "schema compatibility",
			"details": "schema compatibility",
			"evidence_source": "mixed",
			"admission": "keep",
			"context_edges": [
				{"context_key":"deployment-mode","context_value":"LOCAL_OSS","relation":"support"},
				{"context_key":"deployment mode","context_value":"local oss","relation":"support"}
			]
		}],
		"profile_nodes": []
	}`)
	if err != nil {
		t.Fatalf("parse turn analysis response: %v", err)
	}
	if len(analysis.MemoryNodes) != 1 || len(analysis.MemoryNodes[0].ContextEdges) != 1 {
		t.Fatalf("expected equivalent context values to collapse, got %+v", analysis.MemoryNodes)
	}
	edge := analysis.MemoryNodes[0].ContextEdges[0]
	if edge.ContextKey != "deployment_mode" || edge.ContextValue != "local oss" {
		t.Fatalf("expected normalized context edge, got %+v", edge)
	}
}

// TestParseTurnAnalysisResponseRejectsUnexpectedCandidateLocalSupersedes verifies the stricter L1 contract silently ignores deprecated candidate-local supersede payloads instead of promoting them into the normalized analysis result.
// TestParseTurnAnalysisResponseRejectsUnexpectedCandidateLocalSupersedes 用于验证更严格的 L1 契约会忽略废弃的候选级 supersede 载荷，而不会把它提升进归一化分析结果。
func TestParseTurnAnalysisResponseRejectsUnexpectedCandidateLocalSupersedes(t *testing.T) {
	analysis, err := parseTurnAnalysisResponse(`{
		"user_input_kind": "statement",
		"turn_id": 9,
		"details": "当前阶段已经从 A 更新为 B。",
		"memory_nodes": [
			{
				"category": 5,
				"abstract": "当前项目阶段已经切换到 B。",
				"details": "当前项目阶段已经切换到 B，不再处于 A。",
				"evidence_source": "user_confirmed",
				"admission": "keep",
				"admission_reason": "",
				"supersede_memory_ids": [21, 22]
			}
		],
		"profile_nodes": []
	}`)
	if err != nil {
		t.Fatalf("parse turn analysis response: %v", err)
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected one memory node, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.MemoryNodes[0].SupersedeMemoryIDs) != 0 {
		t.Fatalf("expected deprecated supersede ids to be ignored by L1 parser, got %+v", analysis.MemoryNodes[0].SupersedeMemoryIDs)
	}
}

// TestTurnAnalyzerRejectsMismatchedTurnID verifies the model output cannot silently drift onto another target turn when the analyzer already knows which turn it is extracting.
// TestTurnAnalyzerRejectsMismatchedTurnID 用于验证分析器在已知目标 turn 的前提下，不会默默接受模型返回的错误 turn_id。
func TestTurnAnalyzerRejectsMismatchedTurnID(t *testing.T) {
	analyzer := NewTurnAnalyzer(&stubTurnAnalyzerLLM{
		response: logicports.LLMResponse{
			Content: `{"user_input_kind":"mixed","turn_id":999,"details":"","memory_nodes":[],"profile_nodes":[]}`,
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

// TestRenderTurnAnalysisSystemPromptStaysStatic verifies contextual inputs no longer mutate the system prompt, keeping provider cache prefixes stable.
// TestRenderTurnAnalysisSystemPromptStaysStatic 用于验证上下文输入不再改变系统提示词，从而保持 provider cache 前缀稳定。
func TestRenderTurnAnalysisSystemPromptStaysStatic(t *testing.T) {
	template := strings.Join([]string{
		"head",
		"`reference_turns` 只帮助理解语境，不能作为新事实来源。",
		"`recent_grpc_memory_writes` 只用于排除重复。",
		"tail",
	}, "\r\n")
	rendered := renderTurnAnalysisSystemPrompt(strings.Join([]string{
		"head",
		"`reference_turns` 只帮助理解语境，不能作为新事实来源。",
		"`recent_grpc_memory_writes` 只用于排除重复。",
		"tail",
	}, "\r\n"), logicdomain.TurnAnalysisInput{
		ReferenceTurns: []logicdomain.TurnAnalysisReferenceTurn{
			{TurnID: 2, Details: "历史摘要"},
		},
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{
			TurnID:  1,
			RawTurn: `{"user":"你好","assistant":"收到"}`,
		},
		RecentGRPCMemoryWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 3, Abstract: "已写入事实"},
		},
	})
	if strings.Contains(rendered, "{#TAG") {
		t.Fatalf("expected static prompt without tags, got %s", rendered)
	}
	if rendered != strings.ReplaceAll(template, "\r\n", "\n") {
		t.Fatalf("expected prompt to stay static, got %q", rendered)
	}
}

type stubTurnAnalyzerLLM struct {
	request  logicports.LLMRequest
	response logicports.LLMResponse
	err      error
}

func (s *stubTurnAnalyzerLLM) Generate(_ context.Context, req logicports.LLMRequest) (logicports.LLMResponse, error) {
	s.request = req
	if s.err != nil {
		return logicports.LLMResponse{}, s.err
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
	_ logicports.LLMClient    = (*stubTurnAnalyzerLLM)(nil)
	_ logicports.PromptSource = (*stubTurnAnalyzerPromptSource)(nil)
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

// TestParseTurnAnalysisResponseRejectsMissingAdmissionMetadata verifies the stricter postaction_l1_main contract rejects payloads that omit the new admission metadata fields.
// TestParseTurnAnalysisResponseRejectsMissingAdmissionMetadata 用于验证更严格的 postaction_l1_main 契约会拒绝缺失新准入元数据字段的载荷。
func TestParseTurnAnalysisResponseRejectsMissingAdmissionMetadata(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"turn_id":1,"details":"","memory_nodes":[{"category":4,"abstract":"keep","details":"keep"}],"profile_nodes":[{"profile_type":1,"content":"项目事实"}]}`)
	if err == nil {
		t.Fatal("expected strict metadata validation error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}
