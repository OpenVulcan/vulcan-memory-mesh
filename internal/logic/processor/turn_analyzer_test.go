// turn_analyzer_test.go verifies the structured turn-analysis processor and its JSON normalization rules.
// turn_analyzer_test.go 用于验证结构化逐轮分析处理器及其 JSON 归一规则。
package processor

import (
	"context"
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

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
			{MemoryID: 71, SourceTurnID: 80, Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "旧记忆", Details: "旧记忆详情", SupportCount: 2, RebuttalCount: 1},
		},
		RecentGRPCMemoryWrites: []logicdomain.TurnAnalysisDirectWrite{
			{MemoryID: 301, ScopeLevel: "PROJECT", Abstract: "已主动写入的记忆", Details: "这条记忆已经由工具写入"},
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
	if !strings.Contains(llm.request.UserPrompt, `"support_count": 2`) || !strings.Contains(llm.request.UserPrompt, `"rebuttal_count": 1`) {
		t.Fatalf("expected support/rebuttal counts in turn-analysis request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "绝对禁止把这些历史摘要重新提炼成当前 turn 的新增记忆或画像") {
		t.Fatalf("expected rendered reference rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "这些事实已经由工具链主动写入") {
		t.Fatalf("expected direct-write exclusion rule in system prompt, got %s", llm.request.SystemPrompt)
	}
	if !strings.Contains(llm.request.SystemPrompt, "Unified Output Language Rule") {
		t.Fatalf("expected shared language policy in system prompt, got %s", llm.request.SystemPrompt)
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
	if len(analysis.SupersededMemoryIDs) != 1 || analysis.SupersededMemoryIDs[0] != 71 {
		t.Fatalf("unexpected superseded memory ids: %+v", analysis.SupersededMemoryIDs)
	}
}

// TestParseTurnAnalysisResponseRejectsInvalidCategory verifies invalid enum values are surfaced as structured LLM output errors instead of being silently accepted.
// TestParseTurnAnalysisResponseRejectsInvalidCategory 用于验证无效枚举值会被作为结构化 LLM 输出错误暴露，而不是被静默接受。
func TestParseTurnAnalysisResponseRejectsInvalidCategory(t *testing.T) {
	_, err := parseTurnAnalysisResponse(`{"user_input_kind":"mixed","turn_id":1,"details":"","memory_nodes":[{"category":99,"abstract":"bad","details":"bad","evidence_source":"mixed","admission":"keep"}],"profile_nodes":[],"superseded_memory_ids":[]}`)
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
	_, err := parseTurnAnalysisResponse(`{"user_input_kind":"mixed","turn_id":1,"details":"","memory_nodes":[{"category":4,"abstract":"bad","details":"bad","evidence_source":"mixed","admission":"keep","context_edges":[{"context_key":"stage","context_value":"phase4","relation":"unknown"}]}],"profile_nodes":[],"superseded_memory_ids":[]}`)
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
		"profile_nodes": [],
		"superseded_memory_ids": []
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

// TestParseTurnAnalysisResponseMergesCandidateLocalSupersedes verifies candidate-local supersede ids are preserved on the node itself and also unioned into the top-level compatibility field.
// TestParseTurnAnalysisResponseMergesCandidateLocalSupersedes 用于验证候选级 supersede id 会同时保留在节点自身，并汇总到顶层兼容字段中。
func TestParseTurnAnalysisResponseMergesCandidateLocalSupersedes(t *testing.T) {
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
		"profile_nodes": [],
		"superseded_memory_ids": [22, 23]
	}`)
	if err != nil {
		t.Fatalf("parse turn analysis response: %v", err)
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected one memory node, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.MemoryNodes[0].SupersedeMemoryIDs) != 2 || analysis.MemoryNodes[0].SupersedeMemoryIDs[0] != 21 || analysis.MemoryNodes[0].SupersedeMemoryIDs[1] != 22 {
		t.Fatalf("expected candidate-local supersede ids to stay intact, got %+v", analysis.MemoryNodes[0].SupersedeMemoryIDs)
	}
	if len(analysis.SupersededMemoryIDs) != 3 || analysis.SupersededMemoryIDs[0] != 22 || analysis.SupersededMemoryIDs[1] != 23 || analysis.SupersededMemoryIDs[2] != 21 {
		t.Fatalf("expected top-level supersede ids to merge candidate-local ids, got %+v", analysis.SupersededMemoryIDs)
	}
}

// TestTurnAnalyzerRejectsMismatchedTurnID verifies the model output cannot silently drift onto another target turn when the analyzer already knows which turn it is extracting.
// TestTurnAnalyzerRejectsMismatchedTurnID 用于验证分析器在已知目标 turn 的前提下，不会默默接受模型返回的错误 turn_id。
func TestTurnAnalyzerRejectsMismatchedTurnID(t *testing.T) {
	analyzer := NewTurnAnalyzer(&stubTurnAnalyzerLLM{
		response: logicports.LLMResponse{
			Content: `{"user_input_kind":"mixed","turn_id":999,"details":"","memory_nodes":[],"profile_nodes":[],"superseded_memory_ids":[]}`,
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
	_, err := parseTurnAnalysisResponse(`{"turn_id":1,"details":"","memory_nodes":[{"category":4,"abstract":"keep","details":"keep"}],"profile_nodes":[{"profile_type":1,"content":"项目事实"}],"superseded_memory_ids":[]}`)
	if err == nil {
		t.Fatal("expected strict metadata validation error")
	}
	if _, ok := err.(logicdomain.InvalidLLMOutputError); !ok {
		t.Fatalf("expected InvalidLLMOutputError, got %T", err)
	}
}
