// postaction_test.go implements application use case tests.
// postaction_test.go 用于实现应用用例层测试。
package usecase

import (
	"context"
	"sort"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
)

// TestPostActionUseCaseDefaultsScopeFields verifies the TestPostActionUseCaseDefaultsScopeFields behavior.
// TestPostActionUseCaseDefaultsScopeFields 用于验证 TestPostActionUseCaseDefaultsScopeFields 行为。
func TestPostActionUseCaseDefaultsScopeFields(t *testing.T) {
	store := newTestRelationalStore()
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
	store := newTestRelationalStore()
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
	store := newTestRelationalStore()
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

// testSessionMeta stores lightweight session bookkeeping for post-action unit tests.
// testSessionMeta 用于保存 post-action 单测里使用的轻量会话元数据。
type testSessionMeta struct {
	Session   logicdomain.SessionRef
	UpdatedAt time.Time
	TurnCount int
}

// testRelationalStore is the minimal in-test relational store used after removing runtime memory fallbacks.
// testRelationalStore 用于在移除运行时内存回退后，为单测提供最小化关系存储桩。
type testRelationalStore struct {
	turns    map[string]map[int]logicdomain.NormalizedTurn
	sessions map[string]testSessionMeta
}

// newTestRelationalStore creates one isolated relational-store stub for a post-action test case.
// newTestRelationalStore 用于为单个 post-action 测试用例创建隔离的关系存储桩。
func newTestRelationalStore() *testRelationalStore {
	return &testRelationalStore{
		turns:    map[string]map[int]logicdomain.NormalizedTurn{},
		sessions: map[string]testSessionMeta{},
	}
}

// UpsertChatLogs appends normalized turns into the per-session bucket observed by the tests.
// UpsertChatLogs 用于把标准化轮次追加到测试可观察的会话桶中。
func (s *testRelationalStore) UpsertChatLogs(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.NormalizedTurn) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	bucket := s.turns[session.SessionID]
	if bucket == nil {
		bucket = map[int]logicdomain.NormalizedTurn{}
		s.turns[session.SessionID] = bucket
	}
	for _, turn := range turns {
		bucket[turn.TurnIndex] = turn
	}
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.TurnCount = len(bucket)
	s.sessions[session.SessionID] = meta
	return nil
}

// RefreshSession updates the stored scope metadata so tests can assert default-value backfilling.
// RefreshSession 用于更新保存的作用域元数据，方便测试断言默认值补齐逻辑。
func (s *testRelationalStore) RefreshSession(ctx context.Context, session logicdomain.SessionRef) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.UpdatedAt = time.Now().UTC()
	if bucket, ok := s.turns[session.SessionID]; ok {
		meta.TurnCount = len(bucket)
	}
	s.sessions[session.SessionID] = meta
	return nil
}

// Shutdown returns immediately because the stub owns no external resources.
// Shutdown 用于立即返回，因为该桩不持有任何外部资源。
func (s *testRelationalStore) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Turns returns persisted turns sorted by turn index so assertions stay deterministic.
// Turns 用于按 turn index 排序返回已持久化轮次，确保断言结果稳定。
func (s *testRelationalStore) Turns(sessionID string) []logicdomain.NormalizedTurn {
	bucket := s.turns[sessionID]
	out := make([]logicdomain.NormalizedTurn, 0, len(bucket))
	for _, turn := range bucket {
		out = append(out, turn)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TurnIndex < out[j].TurnIndex })
	return out
}

// Session returns the stored session metadata snapshot for assertions about default scope values.
// Session 用于返回保存的会话元数据快照，供默认作用域断言使用。
func (s *testRelationalStore) Session(sessionID string) (testSessionMeta, bool) {
	meta, ok := s.sessions[sessionID]
	return meta, ok
}
