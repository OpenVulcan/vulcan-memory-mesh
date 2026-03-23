// postaction_test.go implements application use case tests.
// postaction_test.go 用于实现应用用例层测试。
package usecase

import (
	"context"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
)

// TestPostActionUseCaseDefaultsScopeFields verifies the TestPostActionUseCaseDefaultsScopeFields behavior.
// TestPostActionUseCaseDefaultsScopeFields 用于验证 TestPostActionUseCaseDefaultsScopeFields 行为。
func TestPostActionUseCaseDefaultsScopeFields(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), nil, store, nil)
	result, err := usecase.Execute(context.Background(), PostActionCommand{
		SessionID: "sess-1",
		RawMessagesSnapshot: []logicdomain.RawMessage{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "收到"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	meta, ok := store.Session("sess-1")
	if !ok {
		t.Fatal("expected session metadata")
	}
	if meta.Session.UserID != "default" || meta.Session.TeamID != "default" || meta.Session.ProjectID != "default" || meta.Session.SpaceID != "default" {
		t.Fatalf("unexpected default scope session=%+v", meta.Session)
	}
}

// TestPostActionUseCaseDropsNoiseTurns verifies the TestPostActionUseCaseDropsNoiseTurns behavior.
// TestPostActionUseCaseDropsNoiseTurns 用于验证 TestPostActionUseCaseDropsNoiseTurns 行为。
func TestPostActionUseCaseDropsNoiseTurns(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	filter := &stubNoiseTurnFilter{}
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), filter, store, nil)
	result, err := usecase.Execute(context.Background(), PostActionCommand{
		SessionID: "sess-noise",
		RawMessagesSnapshot: []logicdomain.RawMessage{
			{Role: "user", Content: "你还记得我上次说过什么吗"},
			{Role: "assistant", Content: "我不记得"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if turns := store.Turns("sess-noise"); len(turns) != 0 {
		t.Fatalf("expected no persisted turns, got %d", len(turns))
	}
	if len(filter.seen) != 1 {
		t.Fatalf("expected one normalized turn to be checked, got %d", len(filter.seen))
	}
	if filter.seen[0].UserMessage != "你还记得我上次说过什么吗" {
		t.Fatalf("unexpected normalized user message: %q", filter.seen[0].UserMessage)
	}
}

// TestPostActionUseCaseSkipsNoiseGateWhenTimelineFlowIsMarked verifies that timeline-driven flows bypass noise filtering and still persist.
// TestPostActionUseCaseSkipsNoiseGateWhenTimelineFlowIsMarked 用于验证时间线驱动流程会跳过噪声门过滤并继续持久化。
func TestPostActionUseCaseSkipsNoiseGateWhenTimelineFlowIsMarked(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	filter := &stubNoiseTurnFilter{}
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), filter, store, nil)
	result, err := usecase.Execute(context.Background(), PostActionCommand{
		SessionID:     "sess-timeline",
		SkipNoiseGate: true,
		RawMessagesSnapshot: []logicdomain.RawMessage{
			{Role: "user", Content: "你还记得我上次说过什么吗"},
			{Role: "assistant", Content: "我不记得"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted result")
	}
	if len(filter.seen) != 0 {
		t.Fatalf("expected noise gate to be skipped, got %d seen turns", len(filter.seen))
	}
	if turns := store.Turns("sess-timeline"); len(turns) != 1 {
		t.Fatalf("expected one persisted turn, got %d", len(turns))
	}
}

// stubNoiseTurnFilter captures normalized turns and returns a caller-controlled filtered result.
// stubNoiseTurnFilter 用于捕获标准化轮次，并返回调用方控制的过滤结果。
type stubNoiseTurnFilter struct {
	seen     []logicdomain.NormalizedTurn
	filtered []logicdomain.NormalizedTurn
}

// FilterPersistableTurns records the normalized turns observed by the use case and returns the configured keep set.
// FilterPersistableTurns 用于记录 usecase 传入的标准化轮次，并返回预设的保留结果。
func (s *stubNoiseTurnFilter) FilterPersistableTurns(_ context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn {
	s.seen = append([]logicdomain.NormalizedTurn(nil), turns...)
	return append([]logicdomain.NormalizedTurn(nil), s.filtered...)
}
