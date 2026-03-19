package postgres_store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type Store struct {
	db      *sql.DB
	markers sync.Map
}

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) UpsertChatLogs(ctx context.Context, session domain.SessionRef, turns []domain.NormalizedTurn) error {
	if s.db == nil { return fmt.Errorf("postgres db is nil") }
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil { return fmt.Errorf("begin tx: %w", err) }
	defer func() { _ = tx.Rollback() }()

	const upsertLogSQL = `
INSERT INTO vmm_chat_logs (
    session_id, turn_index, user_id, team_id, project_id, space_id,
    user_message, assistant_reply, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW()
)
ON CONFLICT (session_id, turn_index)
DO UPDATE SET user_message = EXCLUDED.user_message, assistant_reply = EXCLUDED.assistant_reply, updated_at = NOW();`
	for _, turn := range turns {
		if _, err := tx.ExecContext(ctx, upsertLogSQL, session.SessionID, turn.TurnIndex, session.UserID, session.TeamID, session.ProjectID, nullableString(session.SpaceID), turn.UserMessage, turn.AssistantReply); err != nil {
			return fmt.Errorf("upsert chat log: %w", err)
		}
	}
	if err := upsertSession(ctx, tx, session); err != nil { return err }
	if err := tx.Commit(); err != nil { return fmt.Errorf("commit tx: %w", err) }
	s.markers.Store(markerKey(ctx, session.SessionID), struct{}{})
	return nil
}

func (s *Store) RefreshSession(ctx context.Context, session domain.SessionRef) error {
	if s.db == nil { return fmt.Errorf("postgres db is nil") }
	key := markerKey(ctx, session.SessionID)
	if _, ok := s.markers.Load(key); ok {
		s.markers.Delete(key)
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO vmm_chat_sessions (session_id, user_id, team_id, project_id, space_id, last_updated_at, created_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (session_id)
DO UPDATE SET user_id = EXCLUDED.user_id, team_id = EXCLUDED.team_id, project_id = EXCLUDED.project_id, space_id = EXCLUDED.space_id, last_updated_at = NOW();`,
		session.SessionID, session.UserID, session.TeamID, session.ProjectID, nullableString(session.SpaceID),
	); err != nil {
		return fmt.Errorf("refresh session: %w", err)
	}
	return nil
}

func (s *Store) Shutdown(ctx context.Context) error {
	if s.db == nil { return nil }
	done := make(chan error, 1)
	go func() { done <- s.db.Close() }()
	select { case <-ctx.Done(): return ctx.Err(); case err := <-done: return err }
}

func upsertSession(ctx context.Context, tx *sql.Tx, session domain.SessionRef) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO vmm_chat_sessions (session_id, user_id, team_id, project_id, space_id, last_updated_at, created_at)
VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
ON CONFLICT (session_id)
DO UPDATE SET user_id = EXCLUDED.user_id, team_id = EXCLUDED.team_id, project_id = EXCLUDED.project_id, space_id = EXCLUDED.space_id, last_updated_at = NOW();`,
		session.SessionID, session.UserID, session.TeamID, session.ProjectID, nullableString(session.SpaceID),
	); err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func markerKey(ctx context.Context, sessionID string) string {
	traceID := trace.IDFromContext(ctx)
	if traceID == "" { traceID = "no-trace" }
	return traceID + ":" + sessionID
}

func nullableString(v string) any { if v == "" { return nil }; return v }
