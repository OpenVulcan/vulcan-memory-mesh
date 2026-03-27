// postaction.go implements the main post-action persistence workflow on top of resolved session scope metadata.
// postaction.go 用于基于已解析 session 范围元数据实现主 post-action 持久化工作流。
package usecase

import (
	"context"
	"strconv"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// PostActionTimelineItem carries one validated timeline message that sits between the first user prompt and final assistant reply.
// PostActionTimelineItem 用于承载一条已经校验过的 timeline 中间消息，它位于首轮用户提问和最终助手回答之间。
type PostActionTimelineItem struct {
	Type    string
	Content string
}

// PostActionCommand carries the resolved session scope plus the new text-only post-action payload.
// PostActionCommand 用于承载已解析的 session 范围以及新的纯文本 post-action 载荷。
type PostActionCommand struct {
	Session          logicdomain.SessionRef
	UserContent      string
	AssistantContent string
	Timeline         []PostActionTimelineItem
}

// PostActionResult returns the asynchronous acknowledgement data back to the transport layer.
// PostActionResult 用于把异步确认结果返回给传输层。
type PostActionResult struct {
	Accepted bool
	TraceID  string
}

// PostActionExecutor is the interface consumed by the gRPC adapter to run the post-action workflow.
// PostActionExecutor 用于让 gRPC 适配层执行 post-action 工作流。
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

// PostActionUseCase stores message-level conversation data and applies the noise gate only to simple single-round flows.
// PostActionUseCase 用于按消息级存储对话数据，并只在简单单轮流程上执行噪声门。
type PostActionUseCase struct {
	noiseGate appports.NoiseTurnFilter
	store     appports.RelationalStore
	logger    *logx.Logger
}

// NewPostActionUseCase creates a PostActionUseCase instance.
// NewPostActionUseCase 用于创建 PostActionUseCase 实例。
func NewPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, logger *logx.Logger) *PostActionUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &PostActionUseCase{noiseGate: noiseGate, store: store, logger: logger}
}

// Execute persists cleaned messages into the resolved session after optional noise screening.
// Execute 用于在可选噪声筛查之后，把清洗后的消息持久化到已解析的 session 中。
func (u *PostActionUseCase) Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error) {
	// Validate the resolved session scope and the new text-only payload before touching storage.
	// 在访问存储前先校验已解析的 session 范围和新的纯文本载荷。
	if err := validatePostAction(cmd); err != nil {
		return PostActionResult{}, err
	}
	traceID := trace.IDFromContext(ctx)

	// Skip the noise gate for timeline-driven flows because the middle messages already indicate one interrupted or branching conversation.
	// 对带 timeline 的流程直接跳过噪声门，因为中间消息已经表明它不是简单的单轮问答。
	if u.noiseGate != nil && len(cmd.Timeline) == 0 {
		turns := []logicdomain.NormalizedTurn{{
			TurnIndex:      1,
			UserMessage:    strings.TrimSpace(cmd.UserContent),
			AssistantReply: strings.TrimSpace(cmd.AssistantContent),
			CreatedAt:      time.Now().UTC(),
		}}
		kept := u.noiseGate.FilterPersistableTurns(ctx, turns)
		if len(kept) == 0 {
			if u.logger != nil {
				u.logger.Info("post-action dropped by noise gate", "trace_id", traceID, "session_key", cmd.Session.SessionKey)
			}
			return PostActionResult{Accepted: true, TraceID: traceID}, nil
		}
	}

	// Persist the canonical message sequence exactly as user -> timeline[] -> assistant so later batch extraction can replay it faithfully.
	// 以 user -> timeline[] -> assistant 的标准顺序持久化消息，保证后续批量提炼可以忠实回放原始流程。
	messages := make([]logicdomain.ChatMessage, 0, len(cmd.Timeline)+2)
	messages = append(messages, logicdomain.ChatMessage{
		Role:       "user",
		Content:    strings.TrimSpace(cmd.UserContent),
		SourceKind: "entry_user",
		CreatedAt:  time.Now().UTC(),
	})
	for _, item := range cmd.Timeline {
		messages = append(messages, logicdomain.ChatMessage{
			Role:       strings.TrimSpace(item.Type),
			Content:    strings.TrimSpace(item.Content),
			SourceKind: "timeline",
			CreatedAt:  time.Now().UTC(),
		})
	}
	messages = append(messages, logicdomain.ChatMessage{
		Role:       "assistant",
		Content:    strings.TrimSpace(cmd.AssistantContent),
		SourceKind: "final_assistant",
		CreatedAt:  time.Now().UTC(),
	})
	if err := u.store.AppendChatMessages(ctx, cmd.Session, messages); err != nil {
		return PostActionResult{}, err
	}
	return PostActionResult{Accepted: true, TraceID: traceID}, nil
}

// validatePostAction checks the resolved identifiers and required message fields for the new message-level persistence flow.
// validatePostAction 用于校验新消息级持久化流程所需的已解析标识和必填消息字段。
func validatePostAction(cmd PostActionCommand) error {
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
	if strings.TrimSpace(cmd.AssistantContent) == "" {
		return logicdomain.ValidationError{Field: "assistant_content", Message: "is required"}
	}
	for idx, item := range cmd.Timeline {
		role := strings.TrimSpace(strings.ToLower(item.Type))
		if role != "user" && role != "assistant" {
			return logicdomain.ValidationError{Field: "timeline[" + strconv.Itoa(idx) + "].type", Message: "must be user or assistant"}
		}
		if strings.TrimSpace(item.Content) == "" {
			return logicdomain.ValidationError{Field: "timeline[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
	}
	return nil
}
