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

type Handler struct {
	preCheck    usecase.PreCheckExecutor
	postAction  usecase.PostActionExecutor
	seedMemory  usecase.SeedMemoryExecutor
	preTimeout  time.Duration
	postTimeout time.Duration
	seedTimeout time.Duration
	logger      *log.Logger
}

func NewHandler(preCheck usecase.PreCheckExecutor, postAction usecase.PostActionExecutor, seedMemory usecase.SeedMemoryExecutor, preTimeout, postTimeout, seedTimeout time.Duration, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{preCheck: preCheck, postAction: postAction, seedMemory: seedMemory, preTimeout: preTimeout, postTimeout: postTimeout, seedTimeout: seedTimeout, logger: logger}
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: map[string]string{"status": "ok"}, TraceID: trace.IDFromContext(r.Context())})
}

func (h *Handler) PreCheck(w http.ResponseWriter, r *http.Request) {
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
	items := make([]ContextItemDTO, 0, len(resp.ContextItems))
	for _, item := range resp.ContextItems {
		items = append(items, ContextItemDTO{Kind: item.Kind, Title: item.Title, Text: item.Text, Source: item.Source, Score: item.Score})
	}
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: PreCheckResponseDTO{ShouldInject: resp.ShouldInject, ContextText: resp.ContextText, ContextItems: items, Degraded: resp.Degraded}, TraceID: trace.IDFromContext(r.Context())})
}

func (h *Handler) PostAction(w http.ResponseWriter, r *http.Request) {
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

func (h *Handler) SeedMemory(w http.ResponseWriter, r *http.Request) {
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
	resp, err := h.seedMemory.Execute(ctx, usecase.SeedMemoryCommand{UserID: req.UserID, ProjectID: req.ProjectID, MemoryText: req.MemoryText, SpaceID: req.SpaceID})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, Envelope{Code: http.StatusOK, Msg: "ok", Data: SeedMemoryResponseDTO{Accepted: resp.Accepted, MemoryID: resp.MemoryID}, TraceID: trace.IDFromContext(r.Context())})
}

func decodeJSON(r *http.Request, dst any) error {
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

func toHistoryDomain(items []HistorySnippetDTO) []logicdomain.HistorySnippet {
	out := make([]logicdomain.HistorySnippet, 0, len(items))
	for _, item := range items {
		out = append(out, logicdomain.HistorySnippet{Role: item.Role, Content: item.Content})
	}
	return out
}

func toRawMessagesDomain(items []RawMessageDTO) []logicdomain.RawMessage {
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

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := mapStatus(err)
	writeJSON(w, status, Envelope{Code: status, Msg: sanitizeErrorMessage(msg), TraceID: trace.IDFromContext(r.Context())})
}

func sanitizeErrorMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "internal server error"
	}
	return msg
}
