// handlers.go implements the inbound HTTP adapter layer.
// handlers.go 用于实现入站 HTTP 适配层。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// Handler groups HTTP-facing use cases and timeout settings for the inbound adapter.
// Handler 用于聚合入站 HTTP 适配层依赖的用例和超时配置。
type Handler struct {
	preCheck    usecase.PreCheckExecutor
	postAction  usecase.PostActionExecutor
	seedMemory  usecase.SeedMemoryExecutor
	preTimeout  time.Duration
	postTimeout time.Duration
	seedTimeout time.Duration
	logger      *log.Logger
}

// NewHandler creates a Handler instance.
// NewHandler 用于创建 Handler 实例。
func NewHandler(preCheck usecase.PreCheckExecutor, postAction usecase.PostActionExecutor, seedMemory usecase.SeedMemoryExecutor, preTimeout, postTimeout, seedTimeout time.Duration, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{preCheck: preCheck, postAction: postAction, seedMemory: seedMemory, preTimeout: preTimeout, postTimeout: postTimeout, seedTimeout: seedTimeout, logger: logger}
}

// Healthz executes the Healthz logic.
// Healthz 用于执行 Healthz 逻辑。
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: map[string]string{"status": "ok"}, TraceID: trace.IDFromContext(r.Context())})
}

// PreCheck executes the PreCheck logic.
// PreCheck 用于执行 PreCheck 逻辑。
func (h *Handler) PreCheck(w http.ResponseWriter, r *http.Request) {
	// Bind and normalize the transport request before entering the use case layer.
	// 在进入用例层之前，先绑定并归一化传输层请求。
	var req PreCheckRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, logicdomain.ValidationError{Field: "request", Message: err.Error()})
		return
	}
	if req.HistoryContent == nil {
		req.HistoryContent = []HistorySnippetDTO{}
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
		HistoryContent: toHistoryDomain(req.HistoryContent),
		CurrentContent: req.CurrentContent,
		IsFirstTurn:    req.IsFirstTurn,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	// Translate the use case result back into a transport-safe response DTO.
	// 将用例结果转换回适合传输层输出的响应 DTO。
	items := make([]ContextItemDTO, 0, len(resp.ContextItems))
	for _, item := range resp.ContextItems {
		items = append(items, ContextItemDTO{Kind: item.Kind, Title: item.Title, Text: item.Text, Source: item.Source, Score: item.Score})
	}
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: PreCheckResponseDTO{ShouldInject: resp.ShouldInject, ContextText: resp.ContextText, ContextItems: items, Degraded: resp.Degraded}, TraceID: trace.IDFromContext(r.Context())})
}

// PostAction executes the PostAction logic.
// PostAction 用于执行 PostAction 逻辑。
func (h *Handler) PostAction(w http.ResponseWriter, r *http.Request) {
	// Bind the raw snapshot request and preserve an empty array shape for downstream logic.
	// 绑定原始快照请求，并为下游逻辑保留空数组形态。
	var req PostActionRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, logicdomain.ValidationError{Field: "request", Message: err.Error()})
		return
	}
	if req.RawMessagesSnapshot == nil {
		req.RawMessagesSnapshot = []RawMessageDTO{}
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
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: PostActionResponseDTO{Accepted: resp.Accepted}, TraceID: trace.IDFromContext(r.Context())})
}

// SeedMemory executes the SeedMemory logic.
// SeedMemory 用于执行 SeedMemory 逻辑。
func (h *Handler) SeedMemory(w http.ResponseWriter, r *http.Request) {
	// Reject the request immediately when the seed route is intentionally disabled.
	// 当 seed 路由被显式关闭时，立即拒绝请求。
	if h.seedMemory == nil {
		h.writeError(w, r, logicdomain.ValidationError{Field: "route", Message: "seed-memory is disabled"})
		return
	}
	var req SeedMemoryRequestDTO
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, logicdomain.ValidationError{Field: "request", Message: err.Error()})
		return
	}
	ctx, cancel := withTimeout(r.Context(), h.seedTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, trace.IDFromContext(r.Context()))

	// Forward the seed request into the use case and return the resulting record ID.
	// 将灌库请求下发到用例层，并返回生成的记录 ID。
	resp, err := h.seedMemory.Execute(ctx, usecase.SeedMemoryCommand{UserID: req.UserID, ProjectID: req.ProjectID, MemoryText: req.MemoryText, SpaceID: req.SpaceID})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: SeedMemoryResponseDTO{Accepted: resp.Accepted, MemoryID: resp.MemoryID}, TraceID: trace.IDFromContext(r.Context())})
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

// toHistoryDomain executes the toHistoryDomain logic.
// toHistoryDomain 用于执行 toHistoryDomain 逻辑。
func toHistoryDomain(items []HistorySnippetDTO) []logicdomain.HistorySnippet {
	out := make([]logicdomain.HistorySnippet, 0, len(items))
	for _, item := range items {
		out = append(out, logicdomain.HistorySnippet{Role: item.Role, Content: item.Content})
	}
	return out
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
		out = append(out, logicdomain.RawMessage{Role: item.Role, Content: content, ToolCalls: toolCalls, Meta: item.Meta})
	}
	return out
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
	status, msg := mapStatus(err)
	writeJSON(w, status, Envelope{Code: status, Msg: sanitizeErrorMessage(msg), TraceID: trace.IDFromContext(r.Context())})
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
