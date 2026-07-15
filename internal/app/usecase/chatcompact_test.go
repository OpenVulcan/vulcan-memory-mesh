// chatcompact_test.go verifies the explicit session compact acknowledgement flow and its idempotent store delegation.
// chatcompact_test.go 用于验证显式 session compact 确认流程及其幂等的存储委托行为。
package usecase

import (
	"context"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestChatCompactExecutePersistsLatestBoundary verifies the use case forwards the resolved session and surfaces the stored compact boundary to callers.
// TestChatCompactExecutePersistsLatestBoundary 用于验证该用例会转发已解析 session，并向调用方返回已持久化的 compact 边界。
func TestChatCompactExecutePersistsLatestBoundary(t *testing.T) {
	store := &stubChatCompactStore{
		compactedTurnID: 77,
		updated:         true,
	}
	uc := NewChatCompactUseCase(store)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-chat-compact"), ChatCompactCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			ProjectID:           9,
			LastCompactedTurnID: 22,
		},
	})
	if err != nil {
		t.Fatalf("execute chat compact: %v", err)
	}
	if !result.Accepted || !result.Updated {
		t.Fatalf("unexpected compact result: %#v", result)
	}
	if result.CompactedTurnID != 77 {
		t.Fatalf("compacted turn id = %d, want 77", result.CompactedTurnID)
	}
	if result.TraceID != "trace-chat-compact" {
		t.Fatalf("trace id = %q, want trace-chat-compact", result.TraceID)
	}
	if store.session.SessionID != 41 {
		t.Fatalf("store session = %#v", store.session)
	}
}

// TestChatCompactExecuteRejectsMissingSession verifies the compact flow still requires one resolved persisted session before it can write a boundary.
// TestChatCompactExecuteRejectsMissingSession 用于验证 compact 流程在写入边界前仍要求必须存在一个已解析的持久化 session。
func TestChatCompactExecuteRejectsMissingSession(t *testing.T) {
	uc := NewChatCompactUseCase(&stubChatCompactStore{})

	_, err := uc.Execute(context.Background(), ChatCompactCommand{})
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

// TestChatCompactExecuteRejectsNilStore verifies direct tests receive one stable error instead of a panic when the use case has not been wired.
// TestChatCompactExecuteRejectsNilStore 用于验证当用例尚未装配存储时，直接测试会拿到稳定错误而不是 panic。
func TestChatCompactExecuteRejectsNilStore(t *testing.T) {
	var uc *ChatCompactUseCase

	_, err := uc.Execute(context.Background(), ChatCompactCommand{
		Session: logicdomain.SessionRef{SessionID: 1},
	})
	if err == nil || err.Error() != "chat compact store is nil" {
		t.Fatalf("unexpected nil store error: %v", err)
	}
}

// stubChatCompactStore is the compact-boundary persistence double used by chat-compact tests.
// stubChatCompactStore 用于作为 chat-compact 测试里的 compact 边界持久化桩。
type stubChatCompactStore struct {
	session         logicdomain.SessionRef
	compactedTurnID uint64
	updated         bool
	err             error
}

// MarkSessionCompacted records the requested session and returns the configured compact-boundary outcome.
// MarkSessionCompacted 用于记录请求的 session，并返回预设的 compact 边界结果。
func (s *stubChatCompactStore) MarkSessionCompacted(_ context.Context, session logicdomain.SessionRef, _ time.Time) (uint64, bool, error) {
	s.session = session
	return s.compactedTurnID, s.updated, s.err
}
