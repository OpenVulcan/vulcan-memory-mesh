// precheck_test.go verifies the live turn-centric pre-check flow, including persona-only injection, numbered candidate adoption, and degraded fallback behavior.
// precheck_test.go 用于验证基于 turn 的实时 pre-check 流程，包括仅画像注入、编号候选采纳和降级回退行为。
package usecase

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestValidatePreCheckRejectsMissingScope verifies the live pre-check contract still requires one resolved session/user/project scope.
// TestValidatePreCheckRejectsMissingScope 用于验证实时 pre-check 契约仍要求必须带上已解析的 session/user/project 范围。
func TestValidatePreCheckRejectsMissingScope(t *testing.T) {
	err := validatePreCheck(PreCheckCommand{
		Session:     logicdomain.SessionRef{},
		UserContent: "你好",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	validation, ok := err.(logicdomain.ValidationError)
	if !ok {
		t.Fatalf("expected validation error, got %#v", err)
	}
	if validation.Field != "session_id" {
		t.Fatalf("unexpected validation field: %#v", validation)
	}
}

// TestValidatePreCheckRejectsEmptyUserContent verifies the live pre-check contract still requires one non-empty user text.
// TestValidatePreCheckRejectsEmptyUserContent 用于验证实时 pre-check 契约仍要求非空用户文本。
func TestValidatePreCheckRejectsEmptyUserContent(t *testing.T) {
	err := validatePreCheck(PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			ProjectID:  9,
		},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	validation, ok := err.(logicdomain.ValidationError)
	if !ok {
		t.Fatalf("expected validation error, got %#v", err)
	}
	if validation.Field != "user_content" {
		t.Fatalf("unexpected validation field: %#v", validation)
	}
}

// TestPreCheckExecuteReturnsPersonaOnly verifies the first-stage gate can skip memory recall while still returning stable profile context.
// TestPreCheckExecuteReturnsPersonaOnly 用于验证第一层门控可以跳过记忆召回，同时仍返回稳定画像上下文。
func TestPreCheckExecuteReturnsPersonaOnly(t *testing.T) {
	profiles := &stubPreCheckProfiles{
		result: ProfileBundleResult{
			TeamProfile:    "团队统一要求输出中文。",
			ProjectProfile: "当前项目默认使用 gRPC。",
			UserProfile:    "用户偏好先给结论再解释。",
		},
	}
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			NeedMemory: false,
			Reason:     "question is self-contained",
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled persona context",
		items: []logicdomain.ContextItem{
			{Kind: "project_constraint", Title: "项目约束", Text: "[TEAM]\n团队统一要求输出中文。", Source: "persona"},
			{Kind: "preference", Title: "偏好习惯", Text: "[USER]\n用户偏好先给结论再解释。", Source: "persona"},
		},
	}
	uc := NewPreCheckUseCase(
		profiles,
		&stubPreCheckMemories{},
		&stubPreCheckStore{},
		intent,
		&stubPreCheckReviewer{},
		assembler,
		PreCheckConfig{},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-persona"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这次接口要怎么设计？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
	if result.ContextText != "assembled persona context" {
		t.Fatalf("unexpected context text: %q", result.ContextText)
	}
	if result.Degraded {
		t.Fatal("expected degraded=false")
	}
	if result.TraceID != "trace-pre-persona" {
		t.Fatalf("unexpected trace id: %q", result.TraceID)
	}
	if len(assembler.hits) != 0 {
		t.Fatalf("expected no memory hits, got %#v", assembler.hits)
	}
	if len(assembler.persona.ProjectConstraints) != 2 {
		t.Fatalf("unexpected persona project constraints: %#v", assembler.persona.ProjectConstraints)
	}
	if got := intent.current; got != "这次接口要怎么设计？" {
		t.Fatalf("unexpected current input: %q", got)
	}
}

// TestPreCheckExecuteMarksDegradedWhenAssemblerFallsBack verifies that when the shared assembler fails and pre-check falls back to local deterministic rendering, the RPC result still marks the request as degraded.
// TestPreCheckExecuteMarksDegradedWhenAssemblerFallsBack 用于验证当共享 assembler 失败且 pre-check 回退到本地确定性渲染时，RPC 结果仍会正确标记本次请求为 degraded。
func TestPreCheckExecuteMarksDegradedWhenAssemblerFallsBack(t *testing.T) {
	profiles := &stubPreCheckProfiles{
		result: ProfileBundleResult{
			ProjectProfile: "当前项目默认使用 gRPC。",
		},
	}
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			NeedMemory: false,
			Reason:     "question is self-contained",
		},
	}
	assembler := &stubPreCheckAssembler{
		err: context.DeadlineExceeded,
	}
	uc := NewPreCheckUseCase(
		profiles,
		&stubPreCheckMemories{},
		&stubPreCheckStore{},
		intent,
		&stubPreCheckReviewer{},
		assembler,
		PreCheckConfig{},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-assembler-fallback"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这次接口要怎么设计？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
	if !result.Degraded {
		t.Fatalf("expected degraded=true when assembler falls back, got %#v", result)
	}
	if len(result.ContextItems) == 0 || !strings.Contains(result.ContextText, "当前项目默认使用 gRPC。") {
		t.Fatalf("expected local fallback context to be returned, got %#v", result)
	}
}

// TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates verifies stage one sees mixed refined/raw recent turns, then stage two adopts numbered candidates.
// TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates 用于验证第一层会看到 refined/raw 混合最近 turn，随后第二层按编号采纳候选。
func TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "项目为什么从 mutex 改成 channel 做并发控制",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "项目已经决定用 channel 替代 mutex。",
							DetailsPreview: "这是当前项目的并发实现决策。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.93,
						},
					},
				},
				{
					QueryIndex: 1,
					Query:      "之前有没有确认过 channel 方案",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 21},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 7},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "上轮已经确认 channel 方案。",
							DetailsPreview: "它是最近刚被确认的实现方向。",
							Category:       logicdomain.MemoryNodeCategoryProjectContext,
							Score:          0.88,
						},
					},
				},
			},
		},
	}
	store := &stubPreCheckStore{
		recentTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                7,
				SessionID:         41,
				DehydratedContent: `{"user":"把并发控制改成 channel","timeline":[],"assistant":"已经改掉 mutex"}`,
				ExtractedStatus:   logicdomain.TurnExtractedStatusDone,
				Details:           "上一轮确认并发控制改成 channel。",
				DetailsBudget:     20,
			},
			{
				ID:                8,
				SessionID:         41,
				DehydratedContent: `{"user":"为什么这里要改","timeline":[],"assistant":"因为 channel 更适合当前模型"}`,
				DehydratedBudget:  25,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
			},
		},
	}
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			Queries:    []string{"项目为什么从 mutex 改成 channel 做并发控制", "之前有没有确认过 channel 方案"},
			NeedMemory: true,
			Reason:     "needs recent architecture context",
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{2, 1},
			Reason:                   "先用最近确认，再用稳定决策",
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled adopted context",
		items: []logicdomain.ContextItem{
			{Kind: "memory", Title: "向量召回记忆", Text: "上轮已经确认 channel 方案。", Source: "vector", Score: 0.88},
			{Kind: "memory", Title: "向量召回记忆", Text: "项目已经决定用 channel 替代 mutex。", Source: "vector", Score: 0.93},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		memories,
		store,
		intent,
		reviewer,
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-adopt"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "为什么这里要换成 channel？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
	if result.ContextText != "assembled adopted context" {
		t.Fatalf("unexpected context text: %q", result.ContextText)
	}
	if result.Degraded {
		t.Fatal("expected degraded=false")
	}
	if len(store.adoptedIDs) != 2 || store.adoptedIDs[0] != 21 || store.adoptedIDs[1] != 20 {
		t.Fatalf("unexpected adopted ids: %#v", store.adoptedIDs)
	}
	if len(intent.turns) != 2 {
		t.Fatalf("unexpected recent turns: %#v", intent.turns)
	}
	if intent.turns[0].ContentType != "DETAILS" || intent.turns[0].Content != "上一轮确认并发控制改成 channel。" {
		t.Fatalf("unexpected extracted turn context: %#v", intent.turns[0])
	}
	if intent.turns[1].ContentType != "RAW_TURN" || !strings.Contains(intent.turns[1].Content, `"assistant":"因为 channel 更适合当前模型"`) {
		t.Fatalf("unexpected pending turn context: %#v", intent.turns[1])
	}
	if len(reviewer.input.Candidates) != 2 {
		t.Fatalf("unexpected reviewer candidates: %#v", reviewer.input.Candidates)
	}
	if reviewer.input.Candidates[0].CandidateNumber != 1 || reviewer.input.Candidates[1].CandidateNumber != 2 {
		t.Fatalf("unexpected candidate numbering: %#v", reviewer.input.Candidates)
	}
	if len(assembler.hits) != 2 || assembler.hits[0].ID != "21" || assembler.hits[1].ID != "20" {
		t.Fatalf("unexpected assembled hits: %#v", assembler.hits)
	}
	if !strings.Contains(memories.cmd.QueryJSON, "\"query\":\"项目为什么从 mutex 改成 channel 做并发控制\"") {
		t.Fatalf("unexpected search query json: %s", memories.cmd.QueryJSON)
	}
}

// TestPreCheckExecuteDegradesToPersona verifies that second-stage reviewer failures do not fail the whole request when stable persona context is still available.
// TestPreCheckExecuteDegradesToPersona 用于验证当稳定画像上下文仍可用时，第二层评审器失败不会导致整个请求失败。
func TestPreCheckExecuteDegradesToPersona(t *testing.T) {
	assembler := &stubPreCheckAssembler{
		text: "persona fallback context",
		items: []logicdomain.ContextItem{
			{Kind: "project_constraint", Title: "项目约束", Text: "[PROJECT]\n当前项目优先保证接口兼容。", Source: "persona"},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{
			result: ProfileBundleResult{ProjectProfile: "当前项目优先保证接口兼容。"},
		},
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "兼容",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								Abstract:       "兼容性改造需要保留旧字段。",
								DetailsPreview: "这是项目级历史规则。",
								Score:          0.95,
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 11, SessionID: 41, Details: "上一轮已经强调兼容优先。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{Queries: []string{"兼容"}, NeedMemory: true},
		},
		&stubPreCheckReviewer{err: context.DeadlineExceeded},
		assembler,
		PreCheckConfig{},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-degraded"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这个改动会不会影响兼容？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
	if !result.Degraded {
		t.Fatal("expected degraded=true")
	}
	if len(assembler.hits) != 0 {
		t.Fatalf("expected persona-only fallback, got %#v", assembler.hits)
	}
}

// TestPreCheckExecuteRewritesGenericQueriesToCurrentInput verifies that stage-one deictic queries are rewritten to the full current request before unified retrieval begins.
// TestPreCheckExecuteRewritesGenericQueriesToCurrentInput 用于验证第一层的指代式 query 会在进入统一检索前被改写为完整当前请求。
func TestPreCheckExecuteRewritesGenericQueriesToCurrentInput(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 21, SessionID: 41, Details: "上一轮已经确认 SQLite schema 13 必须保持兼容。", DetailsBudget: 18, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"这个改动为什么这样做"},
				NeedMemory: true,
				Reason:     "model returned a deictic query",
			},
		},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	current := "这个改动会不会影响 SQLite schema 13 的兼容性？"
	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-rewrite"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: current,
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if memories.searchCalls != 1 {
		t.Fatalf("expected one search call, got %d", memories.searchCalls)
	}
	if strings.Count(memories.cmd.QueryJSON, current) < 2 {
		t.Fatalf("expected rewritten query json to keep full current input in both background and query, got %s", memories.cmd.QueryJSON)
	}
}

// TestPreCheckExecuteDeduplicatesSearchQueries verifies that repeated stage-one queries are collapsed before unified retrieval and reviewer input construction so pre-check does not pay duplicate recall cost for the same query.
// TestPreCheckExecuteDeduplicatesSearchQueries 用于验证第一层重复 query 会在统一检索和 reviewer 输入前被折叠，避免 pre-check 为同一 query 支付重复召回成本。
func TestPreCheckExecuteDeduplicatesSearchQueries(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "phase4 当前方案",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "phase4 新方案",
							DetailsPreview: "这是被去重 query 命中的候选。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.94,
							Origin:         "vector_search",
						},
					},
				},
			},
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{1},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 17, SessionID: 41, Details: "上一轮确认当前环境是 phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"phase4 当前方案", " phase4 当前方案 ", "phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs architecture memory",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-dedupe-query"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}

	var items []MemoryQueryItem
	if err := json.Unmarshal([]byte(memories.cmd.QueryJSON), &items); err != nil {
		t.Fatalf("unmarshal query json: %v", err)
	}
	if len(items) != 1 || items[0].Query != "phase4 当前方案" {
		t.Fatalf("expected deduplicated search query json, got %#v", items)
	}
	if len(reviewer.input.SearchQueries) != 1 || reviewer.input.SearchQueries[0] != "phase4 当前方案" {
		t.Fatalf("expected reviewer to see deduplicated search queries, got %#v", reviewer.input.SearchQueries)
	}
}

// TestPreCheckExecuteSkipsImmediateContextOnlyFallback verifies that when stage one fails to provide a stable long-term query, short deictic follow-ups stay on the recent-turn window instead of forcing memory retrieval.
// TestPreCheckExecuteSkipsImmediateContextOnlyFallback 用于验证当第一层没给出稳定长期 query 时，短指代追问会留在最近 turn 窗口内处理，而不会强行触发记忆检索。
func TestPreCheckExecuteSkipsImmediateContextOnlyFallback(t *testing.T) {
	memories := &stubPreCheckMemories{}
	assembler := &stubPreCheckAssembler{
		text: "persona fallback context",
		items: []logicdomain.ContextItem{
			{Kind: "project_constraint", Title: "项目约束", Text: "[PROJECT]\n当前项目优先保持最近上下文一致。", Source: "persona"},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{
			result: ProfileBundleResult{ProjectProfile: "当前项目优先保持最近上下文一致。"},
		},
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 31, SessionID: 41, Details: "上一轮已经解释为什么把并发控制改成 channel。", DetailsBudget: 18, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				NeedMemory: true,
				Reason:     "model was unsure and returned no stable query",
			},
		},
		&stubPreCheckReviewer{},
		assembler,
		PreCheckConfig{HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-skip"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这里为什么这么改？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if memories.searchCalls != 0 {
		t.Fatalf("expected no memory search call, got %d", memories.searchCalls)
	}
	if !result.ShouldInject || result.Degraded {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(assembler.hits) != 0 {
		t.Fatalf("expected persona-only context, got %#v", assembler.hits)
	}
}

// TestPreCheckExecutePassesMatchedContextEvidenceToReviewer verifies that query-time context evidence is preserved when the second-stage reviewer receives numbered candidates.
// TestPreCheckExecutePassesMatchedContextEvidenceToReviewer 用于验证查询期命中的 context evidence 会在第二层 reviewer 收到的候选里被完整保留。
func TestPreCheckExecutePassesMatchedContextEvidenceToReviewer(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 下的 phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "它与 local oss 当前场景匹配。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.96,
								Origin:                      "hybrid_rrf",
								SupportCount:                4,
								RebuttalCount:               1,
								MatchedContextValues:        []string{"deployment_mode=local oss", "task_stage=phase4"},
								MatchedContextSupportCount:  3,
								MatchedContextRebuttalCount: 0,
								MatchedContextScoreDelta:    0.075,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 7, SessionID: 41, Details: "上一轮确认当前环境仍是 local oss。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 下的 phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs environment-specific architecture memory",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-evidence"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("unexpected reviewer candidates: %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if candidate.SupportCount != 4 || candidate.RebuttalCount != 1 {
		t.Fatalf("expected aggregate support/rebuttal counts to be preserved, got %#v", candidate)
	}
	if candidate.Origin != "hybrid_rrf" || candidate.OriginLabel != "Hybrid RRF" {
		t.Fatalf("expected origin explanation to be preserved, got %#v", candidate)
	}
	if candidate.OriginExplanation == "" {
		t.Fatalf("expected non-empty origin explanation, got %#v", candidate)
	}
	if candidate.ScoreLabel != "Very Strong Match" || candidate.ScoreExplanation == "" {
		t.Fatalf("expected score explanation to be preserved, got %#v", candidate)
	}
	if candidate.MatchedContextSupportCount != 3 || candidate.MatchedContextRebuttalCount != 0 || candidate.MatchedContextScoreDelta <= 0 {
		t.Fatalf("expected matched context evidence to be preserved, got %#v", candidate)
	}
	if len(candidate.MatchedContextValues) != 2 || candidate.MatchedContextValues[0] != "deployment_mode=local oss" || candidate.MatchedContextValues[1] != "task_stage=phase4" {
		t.Fatalf("unexpected matched context values: %#v", candidate.MatchedContextValues)
	}
}

// TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits verifies that when the same memory is recalled by multiple query groups, reviewer-facing matched context evidence is merged instead of silently keeping only the first equal-scored hit.
// TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits 用于验证同一 memory 被多个 query group 命中时，reviewer 看到的 matched context 证据会被合并，而不是在分数相等时只保留第一条。
func TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "适用于 local oss，并且这条记录补充了更完整的历史背景和实现约束说明。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.92,
								Origin:                     "vector_search",
								MatchedContextValues:       []string{"deployment_mode=local oss"},
								MatchedContextSupportCount: 1,
								MatchedContextScoreDelta:   0.03,
							},
						},
					},
					{
						QueryIndex: 1,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "适用于 phase4 当前环境。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.92,
								Origin:                     "hybrid_rrf_rerank_mmr",
								MatchedContextValues:       []string{"task_stage=phase4"},
								MatchedContextSupportCount: 2,
								MatchedContextScoreDelta:   0.05,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 7, SessionID: 41, Details: "上一轮确认当前环境是 local oss phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 当前方案", "phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs repeated architecture memory evidence",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-merge"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one merged candidate, got %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if len(candidate.MatchedContextValues) != 2 || candidate.MatchedContextValues[0] != "deployment_mode=local oss" || candidate.MatchedContextValues[1] != "task_stage=phase4" {
		t.Fatalf("expected merged matched context values, got %#v", candidate.MatchedContextValues)
	}
	if candidate.MatchedContextSupportCount != 2 {
		t.Fatalf("expected strongest matched support count to survive merge, got %#v", candidate)
	}
	if candidate.MatchedContextScoreDelta != 0.05 {
		t.Fatalf("expected strongest matched delta to survive merge, got %#v", candidate)
	}
	if candidate.Origin != "hybrid_rrf_rerank_mmr" || candidate.OriginLabel != "Hybrid RRF + Rerank + MMR" {
		t.Fatalf("expected stronger origin explanation to survive merge, got %#v", candidate)
	}
	if candidate.ScoreLabel != "Very Strong Match" || !strings.Contains(candidate.ScoreExplanation, "0.050") {
		t.Fatalf("expected stronger score explanation to survive merge, got %#v", candidate)
	}
	if !strings.Contains(candidate.Details, "更完整的历史背景和实现约束说明") {
		t.Fatalf("expected richer details text to survive merge, got %#v", candidate)
	}
}

// TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary verifies that when a higher-score hit becomes the representative candidate, stronger matched-context stats from the other repeated hit are still preserved.
// TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary 用于验证当更高分命中成为代表候选时，另一条重复命中里更强的 matched-context 统计仍会被保留下来。
func TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckProfiles{},
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "这条命中分更高。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.94,
								Origin:                      "vector_search",
								MatchedContextValues:        []string{"deployment_mode=local oss"},
								MatchedContextSupportCount:  1,
								MatchedContextRebuttalCount: 0,
								MatchedContextScoreDelta:    0.02,
							},
						},
					},
					{
						QueryIndex: 1,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "这条命中的 context evidence 更强。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.91,
								Origin:                      "hybrid_rrf",
								MatchedContextValues:        []string{"task_stage=phase4"},
								MatchedContextSupportCount:  3,
								MatchedContextRebuttalCount: 1,
								MatchedContextScoreDelta:    0.07,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 17, SessionID: 41, Details: "上一轮确认当前环境是 local oss phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 当前方案", "phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs repeated architecture memory evidence",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-secondary-evidence"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one merged candidate, got %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if candidate.Score != 0.94 {
		t.Fatalf("expected higher representative score to remain, got %#v", candidate)
	}
	if candidate.MatchedContextSupportCount != 3 || candidate.MatchedContextRebuttalCount != 1 {
		t.Fatalf("expected stronger secondary matched counts to survive merge, got %#v", candidate)
	}
	if candidate.MatchedContextScoreDelta != 0.07 {
		t.Fatalf("expected stronger secondary matched delta to survive merge, got %#v", candidate)
	}
	if !strings.Contains(candidate.ScoreExplanation, "0.070") {
		t.Fatalf("expected score explanation to reflect merged delta, got %#v", candidate)
	}
}

// TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores verifies that when the unified search layer already decided an equal-score order, pre-check preserves that order instead of reordering ties by memory id.
// TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores 用于验证当 unified search 层已经决定了同分顺序时，pre-check 会保留该顺序，而不是再按 memory id 改写 tie-break。
func TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores(t *testing.T) {
	uc := NewPreCheckUseCase(
		nil,
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "应优先保留的上游第一候选",
								DetailsPreview: "它在 unified search 里已经排在前面。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.91,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 17},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "不应因为 memory id 更小而顶替前者",
								DetailsPreview: "这条只是同分候选。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.91,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
						},
					},
				},
			},
		},
		nil,
		nil,
		nil,
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 1},
		nil,
	)

	candidates, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 phase4 下应该继续用哪个方案？",
	}, logicdomain.IntentResult{
		Queries:    []string{"phase4 当前方案"},
		NeedMemory: true,
		Reason:     "needs architecture memory",
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one retained candidate after limit, got %#v", candidates)
	}
	if candidates[0].MemoryID != 30 {
		t.Fatalf("expected unified-search first candidate to survive equal-score tie, got %#v", candidates)
	}
	if candidates[0].CandidateNumber != 1 {
		t.Fatalf("expected candidate numbering to follow preserved order, got %#v", candidates)
	}
}

// TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor verifies that pre-check does not discard the first reranked candidate just because the provider-specific rerank score is lower than the vector similarity threshold.
// TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor 用于验证 pre-check 不会仅因 provider 自定义 rerank 分低于向量相似度阈值，就误丢第一条 rerank 候选。
func TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor(t *testing.T) {
	uc := NewPreCheckUseCase(
		nil,
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "文本排序模型",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 202},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 42},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "第二条",
								DetailsPreview: "它被 rerank 评为当前 query 的第一候选。",
								Category:       logicdomain.MemoryNodeCategoryProjectContext,
								Score:          0.27,
								Origin:         "vector_rerank",
							},
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 201},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 41},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "第一条",
								DetailsPreview: "它是 rerank 后的第二候选。",
								Category:       logicdomain.MemoryNodeCategoryProjectContext,
								Score:          0.18,
								Origin:         "vector_rerank",
							},
						},
					},
				},
			},
		},
		nil,
		nil,
		nil,
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 4},
		nil,
	)

	candidates, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "什么是文本排序模型？",
	}, logicdomain.IntentResult{
		Queries:    []string{"文本排序模型"},
		NeedMemory: true,
		Reason:     "needs reranked memory candidate",
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected only the top reranked candidate to survive thresholding, got %#v", candidates)
	}
	if candidates[0].MemoryID != 202 {
		t.Fatalf("expected first reranked candidate to survive, got %#v", candidates)
	}
	if candidates[0].Score < 0.99 {
		t.Fatalf("expected reviewer-facing score to be normalized from rerank order, got %#v", candidates)
	}
	if candidates[0].ScoreLabel != "Very Strong Match" {
		t.Fatalf("expected normalized rerank candidate to receive a strong reviewer label, got %#v", candidates)
	}
}

// stubPreCheckProfiles is the profile bundle loader double used by pre-check tests.
// stubPreCheckProfiles 用于作为 pre-check 测试里的画像组合加载桩。
type stubPreCheckProfiles struct {
	result ProfileBundleResult
	err    error
}

// GetBundle executes the stubbed GetBundle logic.
// GetBundle 用于执行桩化的 GetBundle 逻辑。
func (s *stubPreCheckProfiles) GetBundle(context.Context, ProfileBundleCommand) (ProfileBundleResult, error) {
	return s.result, s.err
}

// stubPreCheckMemories is the memory search double used by pre-check tests.
// stubPreCheckMemories 用于作为 pre-check 测试里的记忆检索桩。
type stubPreCheckMemories struct {
	cmd         MemoryQueryCommand
	searchCalls int
	result      MemoryQueryResult
	err         error
}

// Search executes the stubbed Search logic.
// Search 用于执行桩化的 Search 逻辑。
func (s *stubPreCheckMemories) Search(_ context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	s.searchCalls++
	s.cmd = cmd
	return s.result, s.err
}

// GetTurns keeps the stub interface-complete for tests that only exercise Search.
// GetTurns 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) GetTurns(context.Context, TurnDetailCommand) (TurnDetailResult, error) {
	return TurnDetailResult{}, nil
}

// GetDetails keeps the stub interface-complete for tests that only exercise Search.
// GetDetails 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) GetDetails(context.Context, MemoryDetailCommand) (MemoryDetailResult, error) {
	return MemoryDetailResult{}, nil
}

// Write keeps the stub interface-complete for tests that only exercise Search.
// Write 用于在仅测试 Search 的场景下保持桩对象接口完整。
func (s *stubPreCheckMemories) Write(context.Context, WriteMemoriesCommand) (WriteMemoriesResult, error) {
	return WriteMemoriesResult{}, nil
}

// stubPreCheckStore is the relational store double used by pre-check tests.
// stubPreCheckStore 用于作为 pre-check 测试里的关系存储桩。
type stubPreCheckStore struct {
	recentTurns    []logicdomain.SessionTurnRecord
	recentTurnsErr error
	adoptedSession logicdomain.SessionRef
	adoptedIDs     []uint64
	adoptedAt      time.Time
	adoptionErr    error
}

// LoadRecentSessionTurns executes the stubbed LoadRecentSessionTurns logic.
// LoadRecentSessionTurns 用于执行桩化的 LoadRecentSessionTurns 逻辑。
func (s *stubPreCheckStore) LoadRecentSessionTurns(context.Context, logicdomain.SessionRef, int) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.recentTurns...), s.recentTurnsErr
}

// ApplyMemoryAdoption executes the stubbed ApplyMemoryAdoption logic.
// ApplyMemoryAdoption 用于执行桩化的 ApplyMemoryAdoption 逻辑。
func (s *stubPreCheckStore) ApplyMemoryAdoption(_ context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error {
	s.adoptedSession = session
	s.adoptedIDs = append([]uint64(nil), memoryIDs...)
	s.adoptedAt = adoptedAt
	return s.adoptionErr
}

// stubPreCheckIntentExtractor is the first-stage extractor double used by pre-check tests.
// stubPreCheckIntentExtractor 用于作为 pre-check 测试里的第一层意图提取桩。
type stubPreCheckIntentExtractor struct {
	turns   []logicdomain.PreCheckTurnContext
	current string
	result  logicdomain.IntentResult
	err     error
}

// Extract executes the stubbed Extract logic.
// Extract 用于执行桩化的 Extract 逻辑。
func (s *stubPreCheckIntentExtractor) Extract(_ context.Context, turns []logicdomain.PreCheckTurnContext, current string) (logicdomain.IntentResult, error) {
	s.turns = append([]logicdomain.PreCheckTurnContext(nil), turns...)
	s.current = current
	return s.result, s.err
}

// stubPreCheckReviewer is the second-stage reviewer double used by pre-check tests.
// stubPreCheckReviewer 用于作为 pre-check 测试里的第二层评审桩。
type stubPreCheckReviewer struct {
	input  logicdomain.PreCheckMemoryReviewInput
	result logicdomain.PreCheckMemoryReviewResult
	err    error
}

// Review executes the stubbed Review logic.
// Review 用于执行桩化的 Review 逻辑。
func (s *stubPreCheckReviewer) Review(_ context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	s.input = input
	return s.result, s.err
}

// stubPreCheckAssembler is the context assembler double used by pre-check tests.
// stubPreCheckAssembler 用于作为 pre-check 测试里的上下文组装桩。
type stubPreCheckAssembler struct {
	persona logicdomain.PersonaContext
	hits    []logicdomain.MemoryHit
	text    string
	items   []logicdomain.ContextItem
	err     error
}

// Assemble executes the stubbed Assemble logic.
// Assemble 用于执行桩化的 Assemble 逻辑。
func (s *stubPreCheckAssembler) Assemble(_ context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	s.persona = persona
	s.hits = append([]logicdomain.MemoryHit(nil), hits...)
	return s.text, append([]logicdomain.ContextItem(nil), s.items...), s.err
}
