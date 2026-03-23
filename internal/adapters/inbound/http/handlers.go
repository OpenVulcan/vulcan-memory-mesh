// handlers.go implements the inbound HTTP adapter layer.
// handlers.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// Handler groups HTTP-facing use cases, validation, and timeout settings for the inbound adapter.
// Handler 用于聚合入站 HTTP 适配层依赖的用例、校验器和超时配置。
type Handler struct {
	chat        usecase.ChatExecutor
	preCheck    usecase.PreCheckExecutor
	postAction  usecase.PostActionExecutor
	seedMemory  usecase.SeedMemoryExecutor
	chatTimeout time.Duration
	preTimeout  time.Duration
	postTimeout time.Duration
	seedTimeout time.Duration
	logger      *logx.Logger
	validate    *RequestValidator
}

// NewHandler creates a Handler instance.
// NewHandler 用于创建 Handler 实例。
func NewHandler(chat usecase.ChatExecutor, preCheck usecase.PreCheckExecutor, postAction usecase.PostActionExecutor, seedMemory usecase.SeedMemoryExecutor, chatTimeout, preTimeout, postTimeout, seedTimeout time.Duration, logger *logx.Logger, validate *RequestValidator) *Handler {
	if logger == nil {
		logger = logx.Default()
	}
	if validate == nil {
		validate = NewRequestValidator("")
	}
	return &Handler{
		chat:        chat,
		preCheck:    preCheck,
		postAction:  postAction,
		seedMemory:  seedMemory,
		chatTimeout: chatTimeout,
		preTimeout:  preTimeout,
		postTimeout: postTimeout,
		seedTimeout: seedTimeout,
		logger:      logger,
		validate:    validate,
	}
}

// Healthz executes the Healthz logic.
// Healthz 用于执行 Healthz 逻辑。
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    map[string]string{"status": "ok"},
		TraceID: trace.IDFromContext(r.Context()),
	})
}

// Chat executes the Chat logic.
// Chat 用于执行 Chat 逻辑。
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	// Bind, sanitize, and validate the /chat payload before scrubbing and persistence.
	// 在执行脱敏和持久化之前，先绑定、清洗并校验 /chat 载荷。
	if h.chat == nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), errRouteDisabled)
		return
	}
	var req ChatRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeDecodeError(err))
		return
	}
	normalizeChatRequest(&req)
	if err := h.validate.ValidateChat(req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ctx, cancel := withTimeout(r.Context(), h.chatTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, trace.IDFromContext(r.Context()))
	language := preferredLanguage(r.Header.Get("Accept-Language"))

	// Execute the chat archive use case and return the scrubbed message payload.
	// 执行聊天归档用例，并返回脱敏后的消息载荷。
	resp, err := h.chat.Execute(ctx, usecase.ChatCommand{
		SessionID: req.SessionID,
		Message:   req.Message,
		Language:  language,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    ChatResponseDTO{SessionID: resp.SessionID, Message: resp.Message, Language: resp.Language},
		TraceID: trace.IDFromContext(r.Context()),
	})
}

// PreCheck executes the PreCheck logic.
// PreCheck 用于执行 PreCheck 逻辑。
func (h *Handler) PreCheck(w http.ResponseWriter, r *http.Request) {
	// Bind, sanitize, and validate the transport request before entering the use case layer.
	// 在进入用例层之前，先完成绑定、清洗和传输层校验。
	var req PreCheckRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeDecodeError(err))
		return
	}
	normalizePreCheckRequest(&req)
	if err := h.validate.ValidatePreCheck(req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ctx, cancel := withTimeout(r.Context(), h.preTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, trace.IDFromContext(r.Context()))

	// Map the HTTP DTO into the use case command and execute the pre-check flow.
	// 将 HTTP DTO 映射成用例命令，并执行 pre-check 流程。
	resp, err := h.preCheck.Execute(ctx, usecase.PreCheckCommand{
		SessionID:      req.SessionID,
		UserID:         req.UserID,
		TeamID:         req.TeamID,
		SpaceID:        req.SpaceID,
		ProjectID:      req.ProjectID,
		CurrentContent: req.UserText,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	// Translate the use case result back into a transport-safe response DTO.
	// 将用例结果转换回适合传输层输出的响应 DTO。
	items := make([]ContextItemDTO, 0, len(resp.ContextItems))
	for _, item := range resp.ContextItems {
		items = append(items, ContextItemDTO{
			Kind:   item.Kind,
			Title:  item.Title,
			Text:   item.Text,
			Source: item.Source,
			Score:  item.Score,
		})
	}
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    PreCheckResponseDTO{ShouldInject: resp.ShouldInject, ContextText: resp.ContextText, ContextItems: items, Degraded: resp.Degraded},
		TraceID: trace.IDFromContext(r.Context()),
	})
}

// PostAction executes the new async post-action route that acknowledges immediately and keeps processing in the background.
// PostAction 用于执行新的异步 post-action 路由，会立即确认接收并在后台继续处理。
func (h *Handler) PostAction(w http.ResponseWriter, r *http.Request) {
	// Bind, sanitize, and validate the new text-only contract before the request is accepted.
	// 在接受请求之前，先完成新文本契约的绑定、清洗和校验。
	if h.postAction == nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), errRouteDisabled)
		return
	}
	var req PostActionAsyncRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeDecodeError(err))
		return
	}
	normalizePostActionAsyncRequest(&req)
	if err := h.validate.ValidatePostActionAsync(req); err != nil {
		h.writeError(w, r, err)
		return
	}
	traceID := trace.IDFromContext(r.Context())
	cmd := toAsyncPostActionCommand(req)

	// Return the acceptance envelope first so callers are not blocked by downstream processing.
	// 先返回接收成功的响应包，避免调用方被下游处理阻塞。
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    PostActionResponseDTO{Accepted: true},
		TraceID: traceID,
	})

	// Continue the legacy persistence flow in the background using a detached timeout-bound context.
	// 使用脱离请求生命周期且带超时约束的后台上下文继续执行旧版持久化流程。
	go func(cmd usecase.PostActionCommand, traceID string) {
		bgctx, cancel := withTimeout(context.Background(), h.postTimeout)
		defer cancel()
		bgctx = trace.WithTraceID(bgctx, traceID)
		if _, err := h.postAction.Execute(bgctx, cmd); err != nil && h.logger != nil {
			h.logger.Error("async post-action failed", "trace_id", traceID, "session_id", cmd.SessionID, "err", err)
		}
	}(cmd, traceID)
}

// PostActionOld executes the current legacy post-action persistence flow.
// PostActionOld 用于执行当前旧版 post-action 持久化流程。
func (h *Handler) PostActionOld(w http.ResponseWriter, r *http.Request) {
	// Bind, sanitize, and validate the raw snapshot request before the persistence flow starts.
	// 在持久化流程开始前，先完成原始快照请求的绑定、清洗和校验。
	var req PostActionRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeDecodeError(err))
		return
	}
	if err := h.validate.PreparePostAction(&req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ctx, cancel := withTimeout(r.Context(), h.postTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, trace.IDFromContext(r.Context()))

	// Execute the post-action use case with normalized transport inputs.
	// 使用归一化后的传输层输入执行 post-action 用例。
	resp, err := h.postAction.Execute(ctx, usecase.PostActionCommand{
		SessionID:           req.SessionID,
		UserID:              req.UserID,
		TeamID:              req.TeamID,
		SpaceID:             req.SpaceID,
		ProjectID:           req.ProjectID,
		RawMessagesSnapshot: toRawMessagesDomain(req.RawMessagesSnapshot),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    PostActionResponseDTO{Accepted: resp.Accepted},
		TraceID: trace.IDFromContext(r.Context()),
	})
}

// SeedMemory executes the SeedMemory logic.
// SeedMemory 用于执行 SeedMemory 逻辑。
func (h *Handler) SeedMemory(w http.ResponseWriter, r *http.Request) {
	// Reject the request immediately when the seed route is intentionally disabled.
	// 当 seed 路由被显式关闭时，立即拒绝请求。
	if h.seedMemory == nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), errRouteDisabled)
		return
	}

	var req SeedMemoryRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeDecodeError(err))
		return
	}
	normalizeSeedMemoryRequest(&req)
	if err := h.validate.ValidateSeedMemory(req); err != nil {
		h.writeError(w, r, err)
		return
	}

	ctx, cancel := withTimeout(r.Context(), h.seedTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, trace.IDFromContext(r.Context()))

	// Forward the seed request into the use case and return the resulting record ID.
	// 将灌库请求下发到用例层，并返回生成的记录 ID。
	resp, err := h.seedMemory.Execute(ctx, usecase.SeedMemoryCommand{
		UserID:     req.UserID,
		ProjectID:  req.ProjectID,
		MemoryText: req.MemoryText,
		SpaceID:    req.SpaceID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{
		Code:    http.StatusOK,
		Msg:     "ok",
		Data:    SeedMemoryResponseDTO{Accepted: resp.Accepted, MemoryID: resp.MemoryID},
		TraceID: trace.IDFromContext(r.Context()),
	})
}

// decodeJSON executes the decodeJSON logic.
// decodeJSON 用于执行 decodeJSON 逻辑。
func decodeJSON(r *http.Request, dst any) error {
	// Enforce a single strict JSON object and reject unknown fields early.
	// 强制只接受单个严格 JSON 对象，并尽早拒绝未知字段。
	if r == nil || r.Body == nil {
		return errors.New("empty request body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// toRawMessagesDomain executes the toRawMessagesDomain logic.
// toRawMessagesDomain 用于执行 toRawMessagesDomain 逻辑。
func toRawMessagesDomain(items []RawMessageDTO) []logicdomain.RawMessage {
	// Preserve raw JSON payloads so the normalizer can decide how to extract text.
	// 保留原始 JSON 载荷，让 normalizer 决定如何提取文本。
	out := make([]logicdomain.RawMessage, 0, len(items))
	for _, item := range items {
		toolCalls := make([]logicdomain.ToolCall, 0, len(item.ToolCalls))
		for _, toolCall := range item.ToolCalls {
			toolCalls = append(toolCalls, logicdomain.ToolCall{ID: toolCall.ID, Type: toolCall.Type})
		}
		var content any = json.RawMessage(item.Content)
		if len(item.Content) == 0 {
			content = ""
		}
		out = append(out, logicdomain.RawMessage{
			Role:      item.Role,
			Content:   content,
			ToolCalls: toolCalls,
			Meta:      item.Meta,
		})
	}
	return out
}

// toAsyncPostActionCommand converts the new text-only post-action contract into the legacy raw snapshot command reused by the persistence use case.
// toAsyncPostActionCommand 用于把新的纯文本 post-action 契约转换成持久化用例复用的旧版原始快照命令。
func toAsyncPostActionCommand(req PostActionAsyncRequestDTO) usecase.PostActionCommand {
	// Reuse the provided timeline when present, otherwise synthesize a minimal one-turn conversation from the top-level texts.
	// 优先复用调用方提供的时间线；如果没有，则根据顶层文本合成一个最小单轮对话。
	raw := make([]logicdomain.RawMessage, 0, max(len(req.Timeline), 2))
	if len(req.Timeline) == 0 {
		raw = append(raw,
			logicdomain.RawMessage{Role: "user", Content: req.UserContent},
			logicdomain.RawMessage{Role: "assistant", Content: req.AssistantContent},
		)
	} else {
		for _, item := range req.Timeline {
			raw = append(raw, logicdomain.RawMessage{Role: item.Type, Content: item.Content})
		}
	}
	return usecase.PostActionCommand{
		SessionID:           req.SessionID,
		UserID:              req.UserID,
		TeamID:              req.TeamID,
		SpaceID:             req.SpaceID,
		ProjectID:           req.ProjectID,
		RawMessagesSnapshot: raw,
	}
}

// withTimeout executes the withTimeout logic.
// withTimeout 用于执行 withTimeout 逻辑。
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// Prefer a real timeout when configured, otherwise return a cancellable context only.
	// 有配置超时时优先创建超时上下文，否则只返回可取消上下文。
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// writeError executes the writeError logic.
// writeError 用于执行 writeError 逻辑。
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	// Convert domain/use-case errors into a stable HTTP envelope.
	// 将领域层和用例层错误映射成稳定的 HTTP 响应包。
	writeErrorDescriptor(w, trace.IDFromContext(r.Context()), describeError(err))
}

// sanitizeErrorMessage executes the sanitizeErrorMessage logic.
// sanitizeErrorMessage 用于执行 sanitizeErrorMessage 逻辑。
func sanitizeErrorMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "internal server error"
	}
	return msg
}

// preferredLanguage extracts the strongest language tag from Accept-Language.
// preferredLanguage 用于从 Accept-Language 中提取优先级最高的语言标签。
func preferredLanguage(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	first := strings.Split(header, ",")[0]
	first = strings.TrimSpace(first)
	first = strings.Split(first, ";")[0]
	return first
}
