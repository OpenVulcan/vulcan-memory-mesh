package httpapi

import (
	"context"
	"log"
	nethttp "net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/services"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type Handler struct {
	preCheck    *services.PreCheckService
	postAction  *services.PostActionService
	seedMemory  *services.SeedMemoryService
	preTimeout  time.Duration
	postTimeout time.Duration
	seedTimeout time.Duration
	logger      *log.Logger
}

func NewHandler(preCheck *services.PreCheckService, postAction *services.PostActionService, seedMemory *services.SeedMemoryService, preTimeout, postTimeout, seedTimeout time.Duration, logger *log.Logger) *Handler {
	if logger == nil { logger = log.Default() }
	return &Handler{preCheck: preCheck, postAction: postAction, seedMemory: seedMemory, preTimeout: preTimeout, postTimeout: postTimeout, seedTimeout: seedTimeout, logger: logger}
}

func (h *Handler) Healthz(c *gin.Context) {
	c.JSON(nethttp.StatusOK, Envelope{Code: nethttp.StatusOK, Msg: "ok", Data: map[string]string{"status":"ok"}, TraceID: traceIDFromContext(c)})
}

func (h *Handler) PreCheck(c *gin.Context) {
	var req PreCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil { h.writeError(c, domain.ValidationError{Field:"request", Message:err.Error()}); return }
	if req.HistoryContent == nil { req.HistoryContent = []domain.HistorySnippet{} }
	ctx, cancel := h.withTimeout(c, h.preTimeout); defer cancel()
	resp, err := h.preCheck.Execute(ctx, domain.PreCheckRequest{
		SessionID: req.SessionID, UserID: req.UserID, TeamID: req.TeamID, SpaceID: req.SpaceID,
		ProjectID: req.ProjectID, HistoryContent: req.HistoryContent, CurrentContent: req.CurrentContent, IsFirstTurn: req.IsFirstTurn,
	})
	if err != nil { h.writeError(c, err); return }
	h.writeOK(c, resp)
}

func (h *Handler) PostAction(c *gin.Context) {
	var req PostActionRequest
	if err := c.ShouldBindJSON(&req); err != nil { h.writeError(c, domain.ValidationError{Field:"request", Message:err.Error()}); return }
	if req.RawMessagesSnapshot == nil { req.RawMessagesSnapshot = []domain.RawMessage{} }
	ctx, cancel := h.withTimeout(c, h.postTimeout); defer cancel()
	resp, err := h.postAction.Execute(ctx, domain.PostActionRequest{
		SessionID: req.SessionID, UserID: req.UserID, TeamID: req.TeamID, SpaceID: req.SpaceID,
		ProjectID: req.ProjectID, RawMessagesSnapshot: req.RawMessagesSnapshot,
	})
	if err != nil { h.writeError(c, err); return }
	h.writeOK(c, resp)
}

func (h *Handler) SeedMemory(c *gin.Context) {
	if h.seedMemory == nil { h.writeError(c, domain.ValidationError{Field:"route", Message:"seed-memory is disabled"}); return }
	var req SeedMemoryRequest
	if err := c.ShouldBindJSON(&req); err != nil { h.writeError(c, domain.ValidationError{Field:"request", Message:err.Error()}); return }
	ctx, cancel := h.withTimeout(c, h.seedTimeout); defer cancel()
	resp, err := h.seedMemory.Execute(ctx, domain.SeedMemoryRequest{UserID:req.UserID, ProjectID:req.ProjectID, MemoryText:req.MemoryText, SpaceID:req.SpaceID})
	if err != nil { h.writeError(c, err); return }
	h.writeOK(c, resp)
}

func (h *Handler) withTimeout(c *gin.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 { return context.WithCancel(c.Request.Context()) }
	ctx := trace.WithTraceID(c.Request.Context(), traceIDFromContext(c))
	return context.WithTimeout(ctx, timeout)
}

func (h *Handler) writeOK(c *gin.Context, data any) {
	c.JSON(nethttp.StatusOK, Envelope{Code: nethttp.StatusOK, Msg: "ok", Data: data, TraceID: traceIDFromContext(c)})
}

func (h *Handler) writeError(c *gin.Context, err error) {
	status, msg := mapStatus(err)
	c.AbortWithStatusJSON(status, Envelope{Code: status, Msg: msg, TraceID: traceIDFromContext(c)})
}
