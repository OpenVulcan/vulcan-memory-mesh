package usecase

import (
	"context"
	"fmt"
	"log"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type PostActionCommand struct {
	SessionID, UserID, TeamID, SpaceID, ProjectID string
	RawMessagesSnapshot                           []logicdomain.RawMessage
}
type PostActionResult struct {
	Accepted bool
	TraceID  string
}
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

type PostActionUseCase struct {
	normalizer *processor.MessageNormalizer
	store      appports.RelationalStore
	logger     *log.Logger
}

func NewPostActionUseCase(normalizer *processor.MessageNormalizer, store appports.RelationalStore, logger *log.Logger) *PostActionUseCase {
	if logger == nil {
		logger = log.Default()
	}
	return &PostActionUseCase{normalizer: normalizer, store: store, logger: logger}
}

func (u *PostActionUseCase) Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error) {
	if err := validatePostAction(cmd); err != nil {
		return PostActionResult{}, err
	}
	traceID := trace.IDFromContext(ctx)
	turns := u.normalizer.Normalize(cmd.RawMessagesSnapshot)
	if len(turns) == 0 {
		return PostActionResult{Accepted: true, TraceID: traceID}, nil
	}
	session := logicdomain.SessionRef{SessionID: cmd.SessionID, UserID: cmd.UserID, TeamID: cmd.TeamID, SpaceID: cmd.SpaceID, ProjectID: cmd.ProjectID}
	if err := u.store.UpsertChatLogs(ctx, session, turns); err != nil {
		return PostActionResult{}, fmt.Errorf("upsert chat logs: %w", err)
	}
	if err := u.store.RefreshSession(ctx, session); err != nil {
		return PostActionResult{}, fmt.Errorf("refresh session: %w", err)
	}
	return PostActionResult{Accepted: true, TraceID: traceID}, nil
}

func validatePostAction(cmd PostActionCommand) error {
	if strings.TrimSpace(cmd.SessionID) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.UserID) == "" {
		return logicdomain.ValidationError{Field: "user_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.TeamID) == "" {
		return logicdomain.ValidationError{Field: "team_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.ProjectID) == "" {
		return logicdomain.ValidationError{Field: "project_id", Message: "is required"}
	}
	if cmd.RawMessagesSnapshot == nil {
		return logicdomain.ValidationError{Field: "raw_messages_snapshot", Message: "is required"}
	}
	return nil
}
