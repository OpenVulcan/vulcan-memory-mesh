// chatcompact.go implements the explicit session-compaction acknowledgment flow used by plugins after they summarize or trim live context.
// chatcompact.go 用于实现显式 session 压缩确认流程，供插件在总结或裁剪实时上下文后调用。
package usecase

import (
	"context"
	"fmt"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// ChatCompactCommand carries the resolved session scope into the explicit compact-acknowledgement workflow.
// ChatCompactCommand 用于把已解析 session 范围带入显式 compact 确认流程。
type ChatCompactCommand struct {
	Session logicdomain.SessionRef
}

// ChatCompactResult returns the persisted compact boundary so the transport layer can acknowledge whether the session state changed.
// ChatCompactResult 用于返回已持久化的 compact 边界，让传输层可以确认 session 状态是否发生变化。
type ChatCompactResult struct {
	Accepted        bool
	Updated         bool
	CompactedTurnID uint64
	TraceID         string
}

// ChatCompactExecutor is the interface consumed by the gRPC adapter to acknowledge one session compaction event.
// ChatCompactExecutor 用于让 gRPC 适配层确认一次 session 压缩事件。
type ChatCompactExecutor interface {
	Execute(ctx context.Context, cmd ChatCompactCommand) (ChatCompactResult, error)
}

// ChatCompactStore persists the latest compact boundary for one resolved session.
// ChatCompactStore 用于为某个已解析 session 持久化最新 compact 边界。
type ChatCompactStore interface {
	MarkSessionCompacted(ctx context.Context, session logicdomain.SessionRef, compactedAt time.Time) (uint64, bool, error)
}

// ChatCompactUseCase records the latest persisted turn id as the compact boundary after upstream callers summarize a session.
// ChatCompactUseCase 用于在上游完成 session 压缩后，把最新持久化 turn id 记录为 compact 边界。
type ChatCompactUseCase struct {
	store ChatCompactStore
}

// NewChatCompactUseCase creates a ChatCompactUseCase instance.
// NewChatCompactUseCase 用于创建 ChatCompactUseCase 实例。
func NewChatCompactUseCase(store ChatCompactStore) *ChatCompactUseCase {
	return &ChatCompactUseCase{store: store}
}

// Execute validates the resolved session and stores the newest persisted turn as the active compact boundary.
// Execute 用于校验已解析 session，并把最新持久化 turn 记录为当前生效的 compact 边界。
func (u *ChatCompactUseCase) Execute(ctx context.Context, cmd ChatCompactCommand) (ChatCompactResult, error) {
	if u == nil || u.store == nil {
		return ChatCompactResult{}, fmt.Errorf("chat compact store is nil")
	}
	if cmd.Session.SessionID == 0 {
		return ChatCompactResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	compactedTurnID, updated, err := u.store.MarkSessionCompacted(ctx, cmd.Session, time.Now().UTC())
	if err != nil {
		return ChatCompactResult{}, err
	}
	return ChatCompactResult{
		Accepted:        true,
		Updated:         updated,
		CompactedTurnID: compactedTurnID,
		TraceID:         trace.IDFromContext(ctx),
	}, nil
}
