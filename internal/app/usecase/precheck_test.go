// precheck_test.go verifies the live pre-check flow, including persona-only injection, second-stage memory adoption, and degraded fallback behavior.
// precheck_test.go 用于验证实时 pre-check 流程，包括仅画像注入、第二层记忆采纳和降级回退行为。
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

// TestPreCheckExecuteReturnsPersonaOnly verifies the first-stage intent gate can skip memory recall while still returning stable profile context.
// TestPreCheckExecuteReturnsPersonaOnly 用于验证第一层意图门控可以跳过记忆召回，同时仍返回稳定画像上下文。
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

// TestPreCheckExecuteAdoptsSelectedMemories verifies the live flow performs first-stage recall, second-stage selection, and lifecycle write-back for adopted ids.
// TestPreCheckExecuteAdoptsSelectedMemories 用于验证实时流程会执行第一层召回、第二层选择，并为采纳 id 写回生命周期。
func TestPreCheckExecuteAdoptsSelectedMemories(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "channel",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "项目已经决定用 channel 替代 mutex。",
							DetailsPreview: "该并发方案已经在上轮对话确认。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.93,
						},
					},
				},
			},
		},
	}
	store := &stubPreCheckStore{
		history: []logicdomain.SessionTurnRecord{
			{
				ID:                8,
				SessionID:         41,
				DehydratedContent: `{"user":"把并发控制改成 channel","timeline":[],"assistant":"已经改掉 mutex"}`,
				ExtractedStatus:   logicdomain.TurnExtractedStatusDone,
				Details:           "上一轮确认并发控制改成 channel。",
			},
		},
		activeMemoryNodes: []logicdomain.SessionMemoryNodeRecord{
			{
				ID:            10,
				TurnID:        8,
				Category:      logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:      "当前 session 正在处理并发控制重构。",
				Details:       "本 session 最近一直在讨论 mutex 到 channel 的替换。",
				SourceKind:    logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:    logicdomain.MemoryScopeLevelSession,
				RefreshWeight: 1,
				UpdatedAt:     time.Unix(1700000200, 0),
			},
		},
	}
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			Keywords:   []string{"channel", "mutex"},
			NeedMemory: true,
			Reason:     "needs recent architecture context",
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedMemoryIDs: []uint64{10, 20},
			Reason:            "one recent session fact plus one stable project fact",
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled adopted context",
		items: []logicdomain.ContextItem{
			{Kind: "memory", Title: "向量召回记忆", Text: "当前 session 正在处理并发控制重构。", Source: "vector", Score: 1},
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
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8},
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
	if len(store.adoptedIDs) != 2 || store.adoptedIDs[0] != 10 || store.adoptedIDs[1] != 20 {
		t.Fatalf("unexpected adopted ids: %#v", store.adoptedIDs)
	}
	if len(reviewer.input.RecentSessionMemories) != 1 {
		t.Fatalf("unexpected recent session inputs: %#v", reviewer.input.RecentSessionMemories)
	}
	if len(reviewer.input.RetrievedMemories) != 1 {
		t.Fatalf("unexpected retrieved memory inputs: %#v", reviewer.input.RetrievedMemories)
	}
	if len(assembler.hits) != 2 {
		t.Fatalf("unexpected assembled hits: %#v", assembler.hits)
	}
	if !strings.Contains(memories.cmd.QueryJSON, "\"query\":\"channel\"") {
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
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{Keywords: []string{"兼容"}, NeedMemory: true},
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
	history           []logicdomain.SessionTurnRecord
	historyErr        error
	activeMemoryNodes []logicdomain.SessionMemoryNodeRecord
	activeErr         error
	adoptedSession    logicdomain.SessionRef
	adoptedIDs        []uint64
	adoptedAt         time.Time
	adoptionErr       error
}

// LoadRecentSessionHistory executes the stubbed LoadRecentSessionHistory logic.
// LoadRecentSessionHistory 用于执行桩化的 LoadRecentSessionHistory 逻辑。
func (s *stubPreCheckStore) LoadRecentSessionHistory(context.Context, logicdomain.SessionRef, int) ([]logicdomain.SessionTurnRecord, error) {
	return append([]logicdomain.SessionTurnRecord(nil), s.history...), s.historyErr
}

// LoadActiveSessionMemoryNodes executes the stubbed LoadActiveSessionMemoryNodes logic.
// LoadActiveSessionMemoryNodes 用于执行桩化的 LoadActiveSessionMemoryNodes 逻辑。
func (s *stubPreCheckStore) LoadActiveSessionMemoryNodes(context.Context, logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error) {
	return append([]logicdomain.SessionMemoryNodeRecord(nil), s.activeMemoryNodes...), s.activeErr
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
	history []logicdomain.HistorySnippet
	current string
	result  logicdomain.IntentResult
	err     error
}

// Extract executes the stubbed Extract logic.
// Extract 用于执行桩化的 Extract 逻辑。
func (s *stubPreCheckIntentExtractor) Extract(_ context.Context, history []logicdomain.HistorySnippet, current string) (logicdomain.IntentResult, error) {
	s.history = append([]logicdomain.HistorySnippet(nil), history...)
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
