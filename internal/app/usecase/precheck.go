// precheck.go implements the current conservative pre-check entrypoint on top of resolved numeric session scope.
// precheck.go 用于基于已解析数字 session 范围实现当前保守的 pre-check 入口。
package usecase

import (
	"context"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// PreCheckCommand carries the resolved session scope plus the current user text into the pre-check workflow.
// PreCheckCommand 用于承载已解析的 session 范围和当前用户文本，进入 pre-check 工作流。
type PreCheckCommand struct {
	Session     logicdomain.SessionRef
	UserContent string
}

// PreCheckResult returns the assembled injection context and execution state back to the gRPC adapter.
// PreCheckResult 用于把组装后的注入上下文和执行状态返回给 gRPC 适配层。
type PreCheckResult struct {
	ShouldInject bool
	ContextText  string
	ContextItems []logicdomain.ContextItem
	Degraded     bool
	TraceID      string
}

// PreCheckExecutor is the interface consumed by the gRPC adapter to trigger the pre-check workflow.
// PreCheckExecutor 用于让 gRPC 适配层触发 pre-check 工作流。
type PreCheckExecutor interface {
	Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error)
}

// PreCheckUseCase keeps pre-check deterministic and disabled while the new storage and extraction pipeline is still being stabilized.
// PreCheckUseCase 用于在新存储与提炼链路仍在收敛期间，让 pre-check 保持确定性的禁用状态。
type PreCheckUseCase struct {
	logger *logx.Logger
}

// NewPreCheckUseCase creates a PreCheckUseCase instance.
// NewPreCheckUseCase 用于创建 PreCheckUseCase 实例。
func NewPreCheckUseCase(logger *logx.Logger) *PreCheckUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &PreCheckUseCase{logger: logger}
}

// Execute validates the resolved scope and then returns a stable no-injection response.
// Execute 用于校验已解析范围，并返回稳定的“不需要记忆”响应。
func (u *PreCheckUseCase) Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error) {
	if err := validatePreCheck(cmd); err != nil {
		return PreCheckResult{}, err
	}
	traceID := trace.IDFromContext(ctx)
	if u.logger != nil {
		u.logger.Info("pre-check bypassed", "trace_id", traceID, "session_key", cmd.Session.SessionKey, "project_id", cmd.Session.ProjectID, "user_id", cmd.Session.UserID)
	}
	return PreCheckResult{
		ShouldInject: false,
		ContextText:  "",
		ContextItems: []logicdomain.ContextItem{},
		Degraded:     false,
		TraceID:      traceID,
	}, nil
}

// validatePreCheck checks the resolved identifiers and current user content before the use case short-circuits.
// validatePreCheck 用于在用例短路前校验已解析标识和当前用户文本。
func validatePreCheck(cmd PreCheckCommand) error {
	if cmd.Session.SessionID == 0 || strings.TrimSpace(cmd.Session.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if cmd.Session.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if cmd.Session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if strings.TrimSpace(cmd.UserContent) == "" {
		return logicdomain.ValidationError{Field: "user_content", Message: "is required"}
	}
	return nil
}
