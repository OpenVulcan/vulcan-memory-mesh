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
	usecase := NewPostActionUseCase(processor.NewMessageNormalizer(), store, nil)
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
