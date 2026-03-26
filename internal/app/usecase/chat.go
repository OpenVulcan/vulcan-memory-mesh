// chat.go implements the local chat archive use case.
// chat.go 用于实现本地聊天归档用例。
package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// ChatCommand carries one inbound chat message into the scrub-and-store flow.
// ChatCommand 用于承载进入脱敏并归档流程的一条聊天消息。
type ChatCommand struct {
	SessionID string
	Message   string
	Language  string
}

// ChatResult returns the scrubbed message content back to the HTTP transport layer.
// ChatResult 用于把脱敏后的消息内容返回给 HTTP 传输层。
type ChatResult struct {
	SessionID string
	Message   string
	Language  string
	TraceID   string
}

// ChatExecutor is the interface consumed by the HTTP adapter for the /chat archive route.
// ChatExecutor 用于让 HTTP 适配层执行 /chat 归档路由。
type ChatExecutor interface {
	Execute(ctx context.Context, cmd ChatCommand) (ChatResult, error)
}

// ChatUseCase orchestrates PII scrubbing and durable archive persistence for inbound chat messages.
// ChatUseCase 用于编排入站聊天消息的 PII 脱敏与长期归档持久化。
type ChatUseCase struct {
	scrubber appports.TextScrubber
	store    appports.MemoryArchiveStore
	ids      appports.IDGenerator
	logger   *logx.Logger
}

// NewChatUseCase creates a ChatUseCase instance.
// NewChatUseCase 用于创建 ChatUseCase 实例。
func NewChatUseCase(scrubber appports.TextScrubber, store appports.MemoryArchiveStore, ids appports.IDGenerator, logger *logx.Logger) *ChatUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &ChatUseCase{scrubber: scrubber, store: store, ids: ids, logger: logger}
}

// Execute scrubs one message, persists the scrubbed text, and returns the sanitized content.
// Execute 用于脱敏单条消息、持久化脱敏文本，并返回清洗后的内容。
func (u *ChatUseCase) Execute(ctx context.Context, cmd ChatCommand) (ChatResult, error) {
	if err := validateChat(cmd); err != nil {
		return ChatResult{}, err
	}
	if u.scrubber == nil {
		return ChatResult{}, fmt.Errorf("chat scrubber is not configured")
	}
	if u.store == nil {
		return ChatResult{}, fmt.Errorf("chat archive store is not configured")
	}
	if u.ids == nil {
		return ChatResult{}, fmt.Errorf("id generator is not configured")
	}

	scrubbed := u.scrubber.Scrub(cmd.Message, cmd.Language)
	record := logicdomain.ArchivedMemory{
		ID:        u.ids.NewID("arc"),
		SessionID: cmd.SessionID,
		Content:   scrubbed,
		CreatedAt: time.Now().UTC(),
	}
	if err := u.store.SaveMemory(ctx, record); err != nil {
		return ChatResult{}, fmt.Errorf("save scrubbed memory: %w", err)
	}
	return ChatResult{
		SessionID: cmd.SessionID,
		Message:   scrubbed,
		Language:  cmd.Language,
		TraceID:   trace.IDFromContext(ctx),
	}, nil
}

// validateChat validates the /chat command before the scrub-and-store flow starts.
// validateChat 用于在脱敏与归档流程开始前校验 /chat 命令。
func validateChat(cmd ChatCommand) error {
	if strings.TrimSpace(cmd.SessionID) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.Message) == "" {
		return logicdomain.ValidationError{Field: "message", Message: "is required"}
	}
	return nil
}
