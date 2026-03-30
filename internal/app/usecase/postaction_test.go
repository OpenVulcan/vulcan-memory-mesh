// postaction_test.go verifies the latest turn-level post-action workflow against the new resolved-session contract.
// postaction_test.go 用于围绕新的已解析 session 契约，验证最新的 turn 级 post-action 工作流。
package usecase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// TestPostActionUseCaseDropsSingleRoundNoise verifies simple user-assistant pairs can still be rejected by the noise gate.
// TestPostActionUseCaseDropsSingleRoundNoise 用于验证简单的单轮 user-assistant 问答仍然会被噪声门拒绝。
func TestPostActionUseCaseDropsSingleRoundNoise(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-noise",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent:      "你还记得我上次说过什么吗",
		AssistantContent: "我不记得",
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 1 {
		t.Fatalf("expected one normalized turn to be inspected, got %d", len(filter.seen))
	}
	if got := filter.seen[0].UserMessage; got != "你还记得我上次说过什么吗" {
		t.Fatalf("unexpected normalized user message: %q", got)
	}
	if store.turn.UserContent != "" || store.turn.AssistantContent != "" || len(store.turn.Timeline) != 0 {
		t.Fatalf("expected no persisted turn, got %#v", store.turn)
	}
}

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads bypass the single-round noise gate and persist one canonical turn.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷会跳过单轮噪声门，并持久化为一条标准 turn。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  52,
			SessionKey: "sess-timeline",
			UserID:     8,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  10,
		},
		UserContent:      "最开始的问题",
		AssistantContent: "最后的回答",
		Timeline: []PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 0 {
		t.Fatalf("expected noise gate to be skipped, got %#v", filter.seen)
	}
	assertPersistedTurn(t, store.turn, "最开始的问题", "最后的回答", []logicdomain.TurnTimelineItem{
		{Type: "assistant", Content: "中间回答"},
		{Type: "user", Content: "补充问题"},
	})
}

// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval verifies accepted single-round payloads are stored as one canonical turn.
// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval 用于验证通过噪声门的单轮载荷会按新的标准 turn 落库。
func TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  88,
			SessionKey: "sess-ok",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
		},
		UserContent:      "你好",
		AssistantContent: "收到",
	})
	if err != nil {
		t.Fatalf("execute post-action: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 1 {
		t.Fatalf("expected one normalized turn, got %d", len(filter.seen))
	}
	assertPersistedTurn(t, store.turn, "你好", "收到", nil)
}

// TestPostActionUseCaseAlwaysRunsTurnAnalysis verifies the debug-stage analysis now sends every persisted turn through the structured turn-analysis prompt and writes the extracted result back to the relational store.
// TestPostActionUseCaseAlwaysRunsTurnAnalysis 用于验证当前调试阶段会把每一条已落库 turn 都送入结构化逐轮分析提示词，并把提炼结果回写到关系存储。
func TestPostActionUseCaseAlwaysRunsTurnAnalysis(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "clean-user", AssistantReply: "clean-assistant"}}}
	store := &testRelationalStore{persistedTurn: logicdomain.PersistedTurnRecord{ID: 501, SessionID: 91, ProjectID: 12, DehydratedBudget: 9}}
	analyzer := &stubPostActionAnalyzer{result: logicdomain.TurnAnalysis{
		Details: "这轮对话明确了 AI 记忆子项目的方向建议需求。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{Category: logicdomain.MemoryNodeCategoryRequirementTODO, Abstract: "当前对话需要先提供 AI 记忆子项目的方向建议。", Details: "用户当前诉求是先获得 AI 记忆子项目的设计建议。"},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{ProfileType: logicdomain.ProfileTypeProject, Content: "当前项目关注 AI 记忆能力设计。"},
		},
	}}
	uc := NewPostActionUseCase(filter, store, analyzer, PostActionAnalysisConfig{}, nil)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  91,
			SessionKey: "sess-always",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
			TurnCount:  0,
		},
		UserContent:         "clean-user",
		AssistantContent:    "clean-assistant",
		RawUserContent:      "raw-user",
		RawAssistantContent: "raw-assistant",
		RawTimeline: []PostActionTimelineItem{
			{Type: "assistant", Content: "raw-middle"},
		},
	})
	if err != nil {
		t.Fatalf("execute post-action with debug summary: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	assertPersistedTurn(t, store.turn, "clean-user", "clean-assistant", nil)
	if analyzer.calls != 1 {
		t.Fatalf("expected one turn analysis call, got %d", analyzer.calls)
	}
	if !strings.Contains(analyzer.transcript, `"user": "raw-user"`) {
		t.Fatalf("expected raw user content in analysis transcript, got %s", analyzer.transcript)
	}
	if !strings.Contains(analyzer.transcript, `"assistant": "raw-assistant"`) {
		t.Fatalf("expected raw assistant content in analysis transcript, got %s", analyzer.transcript)
	}
	if !strings.Contains(analyzer.transcript, `"content": "raw-middle"`) {
		t.Fatalf("expected raw timeline content in analysis transcript, got %s", analyzer.transcript)
	}
	if store.analysisTurn.ID != 501 {
		t.Fatalf("expected persisted turn id to flow into analysis persistence, got %+v", store.analysisTurn)
	}
	if got := store.analysis.Details; got != "这轮对话明确了 AI 记忆子项目的方向建议需求。" {
		t.Fatalf("unexpected stored analysis details: %+v", store.analysis)
	}
	if store.analysis.DetailsBudget <= 0 {
		t.Fatalf("expected computed details budget, got %+v", store.analysis)
	}
	if len(store.analysis.MemoryNodes) != 1 || len(store.analysis.ProfileNodes) != 1 {
		t.Fatalf("unexpected stored analysis nodes: %+v", store.analysis)
	}
}

// TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails verifies the new debug-stage turn analysis never breaks the main post-action persistence flow.
// TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails 用于验证新的调试阶段逐轮分析即使失败，也绝不会破坏主 post-action 持久化链路。
func TestPostActionUseCaseKeepsPersistenceSuccessfulWhenTurnAnalysisFails(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{persistedTurn: logicdomain.PersistedTurnRecord{ID: 777, SessionID: 92, ProjectID: 12, DehydratedBudget: 11}}
	analyzer := &stubPostActionAnalyzer{err: errors.New("llm failed")}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := NewPostActionUseCase(
		filter,
		store,
		analyzer,
		PostActionAnalysisConfig{IdleTimeout: 15 * time.Minute},
		logger,
	)

	result, err := uc.Execute(context.Background(), PostActionCommand{
		Session: logicdomain.SessionRef{
			SessionID:  92,
			SessionKey: "sess-idle",
			UserID:     9,
			TeamID:     4,
			SpaceID:    6,
			ProjectID:  12,
			TurnCount:  1,
			UpdatedAt:  time.Now().Add(-20 * time.Minute),
		},
		UserContent:         "你好",
		AssistantContent:    "收到",
		RawUserContent:      "你好 raw",
		RawAssistantContent: "收到 raw",
	})
	if err != nil {
		t.Fatalf("execute post-action with failing debug summary: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	assertPersistedTurn(t, store.turn, "你好", "收到", nil)
	if analyzer.calls != 1 {
		t.Fatalf("expected one turn analysis attempt, got %d", analyzer.calls)
	}
	if store.analysisTurn.ID != 0 {
		t.Fatalf("did not expect failed analysis to be persisted, got %+v", store.analysisTurn)
	}
	if !strings.Contains(logBuf.String(), "post-action turn analysis failed") {
		t.Fatalf("expected turn analysis failure to be logged, got %s", logBuf.String())
	}
}

// assertPersistedTurn keeps turn-level persistence assertions compact and readable.
// assertPersistedTurn 用于让 turn 级持久化断言保持紧凑和易读。
func assertPersistedTurn(t *testing.T, turn logicdomain.TurnRecord, userContent, assistantContent string, timeline []logicdomain.TurnTimelineItem) {
	t.Helper()
	if turn.UserContent != userContent || turn.AssistantContent != assistantContent {
		t.Fatalf("unexpected persisted turn header: %+v", turn)
	}
	if len(turn.Timeline) != len(timeline) {
		t.Fatalf("unexpected persisted timeline length: %+v", turn.Timeline)
	}
	for idx := range timeline {
		if turn.Timeline[idx] != timeline[idx] {
			t.Fatalf("unexpected persisted timeline item[%d]: %+v", idx, turn.Timeline[idx])
		}
	}
}

// stubNoiseTurnFilter records observed turns and returns the configured keep set.
// stubNoiseTurnFilter 用于记录观察到的轮次，并返回预设的保留集合。
type stubNoiseTurnFilter struct {
	seen     []logicdomain.NormalizedTurn
	filtered []logicdomain.NormalizedTurn
}

// FilterPersistableTurns captures the incoming turns so tests can assert when the noise gate ran.
// FilterPersistableTurns 用于捕获传入轮次，方便测试断言噪声门是否执行。
func (s *stubNoiseTurnFilter) FilterPersistableTurns(_ context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn {
	s.seen = append([]logicdomain.NormalizedTurn(nil), turns...)
	return append([]logicdomain.NormalizedTurn(nil), s.filtered...)
}

// testRelationalStore is the minimal relational-store stub needed by post-action tests after the move to turn-level persistence.
// testRelationalStore 用于在迁移到 turn 级持久化后，为 post-action 测试提供最小化关系存储桩。
type testRelationalStore struct {
	session       logicdomain.SessionRef
	turn          logicdomain.TurnRecord
	persistedTurn logicdomain.PersistedTurnRecord
	analysisTurn  logicdomain.PersistedTurnRecord
	analysis      logicdomain.TurnAnalysis
}

// AppendTurnRecord records the latest session scope and canonical turn payload for assertions.
// AppendTurnRecord 用于记录最新的 session 范围和标准 turn 载荷，供测试断言使用。
func (s *testRelationalStore) AppendTurnRecord(_ context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	s.session = session
	s.turn = logicdomain.TurnRecord{
		UserContent:      turn.UserContent,
		AssistantContent: turn.AssistantContent,
		CreatedAt:        turn.CreatedAt,
		Timeline:         append([]logicdomain.TurnTimelineItem(nil), turn.Timeline...),
	}
	return s.persistedTurn, nil
}

// ApplyTurnAnalysis records the extracted turn-analysis payload so tests can verify the post-action chain writes back structured results.
// ApplyTurnAnalysis 用于记录提炼后的 turn 分析载荷，方便测试验证 post-action 链路会回写结构化结果。
func (s *testRelationalStore) ApplyTurnAnalysis(_ context.Context, _ logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) error {
	s.analysisTurn = turn
	s.analysis = analysis
	return nil
}

// Shutdown returns immediately because the stub does not own external resources.
// Shutdown 用于立即返回，因为该桩不持有外部资源。
func (s *testRelationalStore) Shutdown(context.Context) error { return nil }

// stubPostActionAnalyzer records debug turn-analysis invocations and returns one canned result or error.
// stubPostActionAnalyzer 用于记录调试逐轮分析调用，并返回预设结果或错误。
type stubPostActionAnalyzer struct {
	calls      int
	transcript string
	result     logicdomain.TurnAnalysis
	err        error
}

// Analyze captures the transcript so tests can assert the trigger path and payload shape.
// Analyze 用于捕获传入 transcript，方便测试断言触发路径和载荷形态。
func (s *stubPostActionAnalyzer) Analyze(_ context.Context, transcript string) (logicdomain.TurnAnalysis, error) {
	s.calls++
	s.transcript = transcript
	if s.err != nil {
		return logicdomain.TurnAnalysis{}, s.err
	}
	return s.result, nil
}
