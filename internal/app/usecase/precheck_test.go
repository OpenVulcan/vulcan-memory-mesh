// precheck_test.go verifies the current conservative pre-check flow that only validates scope and returns a deterministic no-injection result.
// precheck_test.go 用于验证当前保守的 pre-check 流程：它只校验范围并返回确定性的“不注入”结果。
package usecase

import (
	"context"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestValidatePreCheckRejectsMissingScope verifies the latest pre-check contract requires one resolved session/user/project scope.
// TestValidatePreCheckRejectsMissingScope 用于验证最新 pre-check 契约要求必须带上已解析的 session/user/project 范围。
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

// TestValidatePreCheckRejectsEmptyUserContent verifies the current pre-check contract still requires one non-empty user text.
// TestValidatePreCheckRejectsEmptyUserContent 用于验证当前 pre-check 契约仍要求非空用户文本。
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

// TestPreCheckExecuteReturnsDeterministicNoInjection verifies the latest runtime always short-circuits pre-check to a stable no-injection response.
// TestPreCheckExecuteReturnsDeterministicNoInjection 用于验证最新运行时总是把 pre-check 短路成稳定的“不注入”响应。
func TestPreCheckExecuteReturnsDeterministicNoInjection(t *testing.T) {
	uc := NewPreCheckUseCase(nil)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "帮我看下当前空间",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if result.ShouldInject {
		t.Fatal("expected should_inject=false")
	}
	if result.ContextText != "" {
		t.Fatalf("expected empty context text, got %q", result.ContextText)
	}
	if len(result.ContextItems) != 0 {
		t.Fatalf("expected no context items, got %#v", result.ContextItems)
	}
	if result.Degraded {
		t.Fatal("expected degraded=false")
	}
	if result.TraceID != "trace-pre" {
		t.Fatalf("unexpected trace id: %q", result.TraceID)
	}
}
