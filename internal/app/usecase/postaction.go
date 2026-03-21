// postaction.go implements application use cases.
// postaction.go 用于实现应用用例层。
package usecase

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// PostActionCommand carries the raw chat snapshot from the HTTP adapter into the persistence flow.
// PostActionCommand 用于承载从 HTTP 适配层进入持久化流程的原始对话快照。
type PostActionCommand struct {
	SessionID, UserID, TeamID, SpaceID, ProjectID string
	RawMessagesSnapshot                           []logicdomain.RawMessage
}

// PostActionResult returns the persistence acknowledgement back to the transport layer.
// PostActionResult 用于把持久化确认结果返回给传输层。
type PostActionResult struct {
	Accepted bool
	TraceID  string
}

// PostActionExecutor is the interface consumed by the HTTP adapter to run the post-action workflow.
// PostActionExecutor 用于让 HTTP 适配层执行 post-action 工作流。
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

// PostActionUseCase orchestrates message normalization and relational persistence after a chat round completes.
// PostActionUseCase 用于在对话轮次结束后编排消息清洗和关系存储持久化。
type PostActionUseCase struct {
	normalizer *processor.MessageNormalizer
	noiseGate  *processor.NoiseGate
	store      appports.RelationalStore
	logger     *logx.Logger
}

// NewPostActionUseCase creates a PostActionUseCase instance.
// NewPostActionUseCase 用于创建 PostActionUseCase 实例。
func NewPostActionUseCase(normalizer *processor.MessageNormalizer, noiseGate *processor.NoiseGate, store appports.RelationalStore, logger *logx.Logger) *PostActionUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &PostActionUseCase{normalizer: normalizer, noiseGate: noiseGate, store: store, logger: logger}
}

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (u *PostActionUseCase) Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error) {
	cmd.UserID = defaultScopeValue(cmd.UserID)
	cmd.TeamID = defaultScopeValue(cmd.TeamID)
	cmd.SpaceID = defaultScopeValue(cmd.SpaceID)
	cmd.ProjectID = defaultScopeValue(cmd.ProjectID)

	// Validate the incoming snapshot before touching the relational store.
	// 在访问关系存储之前先校验输入快照。
	if err := validatePostAction(cmd); err != nil {
		return PostActionResult{}, err
	}
	traceID := trace.IDFromContext(ctx)

	// Normalize the raw messages and short-circuit empty results.
	// 规范化原始消息，并对空结果做短路返回。
	turns := u.normalizer.Normalize(cmd.RawMessagesSnapshot)
	if len(turns) == 0 {
		return PostActionResult{Accepted: true, TraceID: traceID}, nil
	}
	turns, _ = u.noiseGate.FilterTurns(ctx, turns)
	if len(turns) == 0 {
		return PostActionResult{Accepted: true, TraceID: traceID}, nil
	}
	session := logicdomain.SessionRef{SessionID: cmd.SessionID, UserID: cmd.UserID, TeamID: cmd.TeamID, SpaceID: cmd.SpaceID, ProjectID: cmd.ProjectID}

	// Persist cleaned turns first, then refresh the session metadata.
	// 先持久化清洗后的轮次，再刷新会话元数据。
	if err := u.store.UpsertChatLogs(ctx, session, turns); err != nil {
		return PostActionResult{}, fmt.Errorf("upsert chat logs: %w", err)
	}
	if err := u.store.RefreshSession(ctx, session); err != nil {
		return PostActionResult{}, fmt.Errorf("refresh session: %w", err)
	}
	return PostActionResult{Accepted: true, TraceID: traceID}, nil
}

// validatePostAction validates the input value.
// validatePostAction 用于校验输入值。
func validatePostAction(cmd PostActionCommand) error {
	// Check the minimum identifiers required for relational persistence.
	// 校验关系持久化所需的最小标识字段。
	if strings.TrimSpace(cmd.SessionID) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if cmd.RawMessagesSnapshot == nil {
		return logicdomain.ValidationError{Field: "raw_messages_snapshot", Message: "is required"}
	}
	return nil
}

// defaultScopeValue fills one blank scope field with the local default namespace.
// defaultScopeValue 用于把单个空范围字段补成默认命名空间值。
func defaultScopeValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "default"
	}
	return strings.TrimSpace(value)
}
