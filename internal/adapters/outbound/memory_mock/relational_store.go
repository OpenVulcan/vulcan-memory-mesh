package memory_mock

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type SessionMeta struct {
	Session   domain.SessionRef
	UpdatedAt time.Time
	TurnCount int
}

type RelationalStore struct {
	mu       sync.RWMutex
	turns    map[string]map[int]domain.NormalizedTurn
	sessions map[string]SessionMeta
}

func NewRelationalStore() *RelationalStore {
	return &RelationalStore{turns: map[string]map[int]domain.NormalizedTurn{}, sessions: map[string]SessionMeta{}}
}

func (s *RelationalStore) UpsertChatLogs(ctx context.Context, session domain.SessionRef, turns []domain.NormalizedTurn) error {
	select { case <-ctx.Done(): return ctx.Err(); default: }
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.turns[session.SessionID]
	if bucket == nil {
		bucket = map[int]domain.NormalizedTurn{}
		s.turns[session.SessionID] = bucket
	}
	for _, turn := range turns { bucket[turn.TurnIndex] = turn }
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.TurnCount = len(bucket)
	s.sessions[session.SessionID] = meta
	return nil
}

func (s *RelationalStore) RefreshSession(ctx context.Context, session domain.SessionRef) error {
	select { case <-ctx.Done(): return ctx.Err(); default: }
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.UpdatedAt = time.Now().UTC()
	if bucket, ok := s.turns[session.SessionID]; ok { meta.TurnCount = len(bucket) }
	s.sessions[session.SessionID] = meta
	return nil
}

func (s *RelationalStore) Turns(sessionID string) []domain.NormalizedTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.turns[sessionID]
	out := make([]domain.NormalizedTurn, 0, len(bucket))
	for _, turn := range bucket { out = append(out, turn) }
	sort.SliceStable(out, func(i, j int) bool { return out[i].TurnIndex < out[j].TurnIndex })
	return out
}

func (s *RelationalStore) Session(sessionID string) (SessionMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, ok := s.sessions[sessionID]
	return meta, ok
}

func (s *RelationalStore) Shutdown(ctx context.Context) error {
	select { case <-ctx.Done(): return ctx.Err(); default: return nil }
}
