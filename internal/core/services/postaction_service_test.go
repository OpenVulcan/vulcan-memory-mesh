package services

import (
	"context"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/core/domain"
)

func TestPostActionCleansAndPersistsTurns(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	service := NewPostActionService(store, nil)
	resp, err := service.Execute(context.Background(), domain.PostActionRequest{
		SessionID:"s1", UserID:"u1", TeamID:"t1", ProjectID:"p1",
		RawMessagesSnapshot: []domain.RawMessage{
			{Role:"system", Content:"system"},
			{Role:"user", Content:"请帮我实现"},
			{Role:"assistant", Content:"<think>hidden</think>最终答案"},
			{Role:"tool", Content:"tool"},
		},
	})
	if err != nil { t.Fatal(err) }
	if !resp.Accepted { t.Fatalf("expected accepted") }
	turns := store.Turns("s1")
	if len(turns) != 1 { t.Fatalf("expected 1 turn, got %d", len(turns)) }
	if turns[0].AssistantReply != "最终答案" { t.Fatalf("unexpected assistant reply: %s", turns[0].AssistantReply) }
}

func TestPostActionEmptyAfterCleaningShortCircuits(t *testing.T) {
	store := memory_mock.NewRelationalStore()
	service := NewPostActionService(store, nil)
	_, err := service.Execute(context.Background(), domain.PostActionRequest{
		SessionID:"s1", UserID:"u1", TeamID:"t1", ProjectID:"p1",
		RawMessagesSnapshot: []domain.RawMessage{{Role:"tool", Content:"x"}, {Role:"assistant", ToolCalls: []domain.ToolCall{{ID:"1"}}}},
	})
	if err != nil { t.Fatal(err) }
	if turns := store.Turns("s1"); len(turns) != 0 { t.Fatalf("expected no turns, got %d", len(turns)) }
}
