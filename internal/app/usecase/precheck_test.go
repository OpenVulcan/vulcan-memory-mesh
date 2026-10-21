// Behavioral tests grouped by precheck test.
// 应用用例层 pre-check 入口与无召回分支行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"testing"
)

// TestPreCheckExecuteRejectsNilReceiver verifies the exported pre-check use case returns one stable error instead of panicking when direct tests or manual integrations accidentally invoke Execute on a nil receiver.
// TestPreCheckExecuteRejectsNilReceiver 用于验证导出的 pre-check 用例在直接测试或手工集成误把 Execute 调到 nil 接收者上时，会返回稳定错误，而不是直接 panic。
func TestPreCheckExecuteRejectsNilReceiver(t *testing.T) {
	var uc *PreCheckUseCase

	_, err := uc.Execute(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			ProjectID:  9,
		},
		UserContent: "这次接口要怎么设计？",
	})
	if err == nil || err.Error() != "pre-check use case is nil" {
		t.Fatalf("unexpected nil receiver error: %v", err)
	}
}

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

// TestPreCheckExecuteReturnsEmptyContextWhenMemoryIsNotNeeded verifies the first-stage gate can skip long-term recall and return an empty pre-check payload instead of mixing in profile data.
// TestPreCheckExecuteReturnsEmptyContextWhenMemoryIsNotNeeded 用于验证当第一层判断不需要长期记忆时，pre-check 会返回空上下文，而不是混入画像数据。
func TestPreCheckExecuteReturnsEmptyContextWhenMemoryIsNotNeeded(t *testing.T) {
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			NeedMemory: false,
			Reason:     "question is self-contained",
		},
	}
	assembler := &stubPreCheckAssembler{}
	uc := NewPreCheckUseCase(
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
	if result.ShouldInject {
		t.Fatalf("expected should_inject=false, got %#v", result)
	}
	if result.ContextText != "" {
		t.Fatalf("unexpected context text: %q", result.ContextText)
	}
	if result.Degraded {
		t.Fatal("expected degraded=false")
	}
	if result.TraceID != "trace-pre-persona" {
		t.Fatalf("unexpected trace id: %q", result.TraceID)
	}
	if assembler.called {
		t.Fatalf("expected assembler to be skipped when no memory is needed, got %#v", assembler)
	}
	if got := intent.current; got != "这次接口要怎么设计？" {
		t.Fatalf("unexpected current input: %q", got)
	}
}

// TestPreCheckExecuteSkipsForgottenSession verifies a forgotten tombstone returns success without touching recall dependencies.
// TestPreCheckExecuteSkipsForgottenSession 用于验证遗忘墓碑会直接成功返回且不访问召回依赖。
func TestPreCheckExecuteSkipsForgottenSession(t *testing.T) {
	uc := NewPreCheckUseCase(nil, nil, nil, nil, nil, PreCheckConfig{}, nil)
	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-forgotten"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID: 41, SessionKey: "sess-forgotten", UserID: 7, ProjectID: 9,
			MemoryStatus: logicdomain.SessionMemoryStatusForgotten,
		},
		UserContent: "不会触发召回",
	})
	if err != nil {
		t.Fatalf("Execute(forgotten) error = %v", err)
	}
	if result.ShouldInject || len(result.ContextItems) != 0 || result.TraceID != "trace-forgotten" {
		t.Fatalf("Execute(forgotten) = %+v", result)
	}
}

// TestPreCheckExecuteSkipsAssemblerWhenNoMemoryIsNeeded verifies that when stage one decides no memory is needed, pre-check returns directly instead of calling the assembler fallback path.
// TestPreCheckExecuteSkipsAssemblerWhenNoMemoryIsNeeded 用于验证当第一层判断无需记忆时，pre-check 会直接返回，而不会进入 assembler 或 fallback 路径。
func TestPreCheckExecuteSkipsAssemblerWhenNoMemoryIsNeeded(t *testing.T) {
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			NeedMemory: false,
			Reason:     "question is self-contained",
		},
	}
	assembler := &stubPreCheckAssembler{
		err: context.DeadlineExceeded,
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{},
		&stubPreCheckStore{},
		intent,
		&stubPreCheckReviewer{},
		assembler,
		PreCheckConfig{},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-assembler-fallback"), PreCheckCommand{
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
	if result.ShouldInject || result.Degraded {
		t.Fatalf("expected direct empty result without assembler fallback, got %#v", result)
	}
	if assembler.called {
		t.Fatalf("expected assembler to be skipped, got %#v", assembler)
	}
}
