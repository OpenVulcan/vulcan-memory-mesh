// precheck_test.go verifies the live turn-centric pre-check flow, including persona-only injection, numbered candidate adoption, and degraded fallback behavior.
// precheck_test.go 用于验证基于 turn 的实时 pre-check 流程，包括仅画像注入、编号候选采纳和降级回退行为。
package usecase

import (
	"context"
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
	cmd    MemoryQueryCommand
	result MemoryQueryResult
	err    error
}

// Search executes the stubbed Search logic.
// Search 用于执行桩化的 Search 逻辑。
func (s *stubPreCheckMemories) Search(_ context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
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
