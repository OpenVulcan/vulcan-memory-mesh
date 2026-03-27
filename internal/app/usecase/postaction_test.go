// postaction_test.go verifies the latest message-level post-action workflow against the new resolved-session contract.
// postaction_test.go 用于围绕新的已解析 session 契约，验证最新的消息级 post-action 工作流。
package usecase

import (
	"context"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestPostActionUseCaseDropsSingleRoundNoise verifies simple user-assistant pairs can still be rejected by the noise gate.
// TestPostActionUseCaseDropsSingleRoundNoise 用于验证简单的单轮 user-assistant 问答仍然会被噪声门拒绝。
func TestPostActionUseCaseDropsSingleRoundNoise(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil)

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
	if len(store.messages) != 0 {
		t.Fatalf("expected no persisted messages, got %#v", store.messages)
	}
}

// TestPostActionUseCaseSkipsNoiseGateForTimeline verifies timeline-driven payloads bypass the single-round noise gate and persist in canonical order.
// TestPostActionUseCaseSkipsNoiseGateForTimeline 用于验证带 timeline 的载荷会跳过单轮噪声门，并按标准顺序持久化。
func TestPostActionUseCaseSkipsNoiseGateForTimeline(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil)

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
	if len(store.messages) != 4 {
		t.Fatalf("expected 4 persisted messages, got %d", len(store.messages))
	}
	assertPersistedMessage(t, store.messages[0], "user", "最开始的问题", "entry_user")
	assertPersistedMessage(t, store.messages[1], "assistant", "中间回答", "timeline")
	assertPersistedMessage(t, store.messages[2], "user", "补充问题", "timeline")
	assertPersistedMessage(t, store.messages[3], "assistant", "最后的回答", "final_assistant")
}

// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval verifies accepted single-round payloads are stored as the new canonical message sequence.
// TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval 用于验证通过噪声门的单轮载荷会按新的标准消息序列落库。
func TestPostActionUseCasePersistsSingleRoundAfterNoiseApproval(t *testing.T) {
	filter := &stubNoiseTurnFilter{filtered: []logicdomain.NormalizedTurn{{TurnIndex: 1, UserMessage: "你好", AssistantReply: "收到"}}}
	store := &testRelationalStore{}
	uc := NewPostActionUseCase(filter, store, nil)

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
	if len(store.messages) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(store.messages))
	}
	assertPersistedMessage(t, store.messages[0], "user", "你好", "entry_user")
	assertPersistedMessage(t, store.messages[1], "assistant", "收到", "final_assistant")
}

// assertPersistedMessage keeps message-level persistence assertions compact and readable.
// assertPersistedMessage 用于让消息级持久化断言保持紧凑和易读。
func assertPersistedMessage(t *testing.T, message logicdomain.ChatMessage, role, content, sourceKind string) {
	t.Helper()
	if message.Role != role || message.Content != content || message.SourceKind != sourceKind {
		t.Fatalf("unexpected persisted message: %+v", message)
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

// testRelationalStore is the minimal relational-store stub needed by post-action tests after the move to message-level persistence.
// testRelationalStore 用于在迁移到消息级持久化后，为 post-action 测试提供最小化关系存储桩。
type testRelationalStore struct {
	session  logicdomain.SessionRef
	messages []logicdomain.ChatMessage
}

// AppendChatMessages records the latest session scope and canonical message sequence for assertions.
// AppendChatMessages 用于记录最新的 session 范围和标准消息序列，供测试断言使用。
func (s *testRelationalStore) AppendChatMessages(_ context.Context, session logicdomain.SessionRef, messages []logicdomain.ChatMessage) error {
	s.session = session
	s.messages = append([]logicdomain.ChatMessage(nil), messages...)
	return nil
}

// Shutdown returns immediately because the stub does not own external resources.
// Shutdown 用于立即返回，因为该桩不持有外部资源。
func (s *testRelationalStore) Shutdown(context.Context) error { return nil }
