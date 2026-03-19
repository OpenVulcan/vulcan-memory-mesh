package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type PostActionService struct {
	store  ports.RelationalStore
	logger *log.Logger
}

func NewPostActionService(store ports.RelationalStore, logger *log.Logger) *PostActionService {
	if logger == nil { logger = log.Default() }
	return &PostActionService{store: store, logger: logger}
}

func (s *PostActionService) Execute(ctx context.Context, req domain.PostActionRequest) (domain.PostActionResponse, error) {
	if err := s.validate(req); err != nil { return domain.PostActionResponse{}, err }
	traceID := trace.IDFromContext(ctx)
	turns := normalizeTurns(req.RawMessagesSnapshot)
	if len(turns) == 0 { return domain.PostActionResponse{Accepted: true, TraceID: traceID}, nil }
	session := req.SessionRef()
	if err := s.store.UpsertChatLogs(ctx, session, turns); err != nil { return domain.PostActionResponse{}, fmt.Errorf("upsert chat logs: %w", err) }
	if err := s.store.RefreshSession(ctx, session); err != nil { return domain.PostActionResponse{}, fmt.Errorf("refresh session: %w", err) }
	return domain.PostActionResponse{Accepted: true, TraceID: traceID}, nil
}

func (s *PostActionService) validate(req domain.PostActionRequest) error {
	if strings.TrimSpace(req.SessionID) == "" { return domain.ValidationError{Field: "session_id", Message: "is required"} }
	if strings.TrimSpace(req.UserID) == "" { return domain.ValidationError{Field: "user_id", Message: "is required"} }
	if strings.TrimSpace(req.TeamID) == "" { return domain.ValidationError{Field: "team_id", Message: "is required"} }
	if strings.TrimSpace(req.ProjectID) == "" { return domain.ValidationError{Field: "project_id", Message: "is required"} }
	if req.RawMessagesSnapshot == nil { return domain.ValidationError{Field: "raw_messages_snapshot", Message: "is required"} }
	return nil
}

func normalizeTurns(messages []domain.RawMessage) []domain.NormalizedTurn {
	type cleaned struct{ role, text string }
	filtered := make([]cleaned, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role != "user" && role != "assistant" { continue }
		if len(msg.ToolCalls) > 0 { continue }
		text := textutil.StripThoughtTags(textutil.ExtractTextFromAny(msg.Content))
		if text == "" { continue }
		filtered = append(filtered, cleaned{role: role, text: text})
	}
	turns := make([]domain.NormalizedTurn, 0)
	pendingUser, pendingAssistant := "", ""
	flush := func() {
		if pendingUser == "" || pendingAssistant == "" { return }
		turns = append(turns, domain.NormalizedTurn{
			TurnIndex:      len(turns) + 1,
			UserMessage:    pendingUser,
			AssistantReply: pendingAssistant,
			CreatedAt:      time.Now().UTC(),
		})
	}
	for _, msg := range filtered {
		if msg.role == "user" {
			flush()
			pendingUser = msg.text
			pendingAssistant = ""
			continue
		}
		if pendingUser == "" { continue }
		pendingAssistant = msg.text
	}
	flush()
	return turns
}
