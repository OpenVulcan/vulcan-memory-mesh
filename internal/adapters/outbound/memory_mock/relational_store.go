// relational_store.go implements the in-memory mock outbound adapters.
// relational_store.go 用于实现内存版 mock 出站适配器。
package memory_mock

import (
	"context"
	"sort"
	"sync"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// SessionMeta stores lightweight session bookkeeping for the in-memory relational mock.
// SessionMeta 用于保存内存版关系存储 mock 的轻量会话元数据。
type SessionMeta struct {
	Session   logicdomain.SessionRef
	UpdatedAt time.Time
	TurnCount int
}

// RelationalStore is the in-memory relational adapter used by post-action flows in local mode and tests.
// RelationalStore 用于作为本地模式和测试中的内存版关系存储适配器。
type RelationalStore struct {
	mu       sync.RWMutex
	turns    map[string]map[int]logicdomain.NormalizedTurn
	sessions map[string]SessionMeta
}

// NewRelationalStore creates a RelationalStore instance.
// NewRelationalStore 用于创建 RelationalStore 实例。
func NewRelationalStore() *RelationalStore {
	return &RelationalStore{turns: map[string]map[int]logicdomain.NormalizedTurn{}, sessions: map[string]SessionMeta{}}
}

// UpsertChatLogs executes the UpsertChatLogs logic.
// UpsertChatLogs 用于执行 UpsertChatLogs 逻辑。
func (s *RelationalStore) UpsertChatLogs(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.NormalizedTurn) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket := s.turns[session.SessionID]
	if bucket == nil {
		bucket = map[int]logicdomain.NormalizedTurn{}
		s.turns[session.SessionID] = bucket
	}
	for _, turn := range turns {
		bucket[turn.TurnIndex] = turn
	}
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.TurnCount = len(bucket)
	s.sessions[session.SessionID] = meta
	return nil
}

// RefreshSession executes the RefreshSession logic.
// RefreshSession 用于执行 RefreshSession 逻辑。
func (s *RelationalStore) RefreshSession(ctx context.Context, session logicdomain.SessionRef) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.sessions[session.SessionID]
	meta.Session = session
	meta.UpdatedAt = time.Now().UTC()
	if bucket, ok := s.turns[session.SessionID]; ok {
		meta.TurnCount = len(bucket)
	}
	s.sessions[session.SessionID] = meta
	return nil
}

// Turns executes the Turns logic.
// Turns 用于执行 Turns 逻辑。
func (s *RelationalStore) Turns(sessionID string) []logicdomain.NormalizedTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket := s.turns[sessionID]
	out := make([]logicdomain.NormalizedTurn, 0, len(bucket))
	for _, turn := range bucket {
		out = append(out, turn)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TurnIndex < out[j].TurnIndex })
	return out
}

// Session executes the Session logic.
// Session 用于执行 Session 逻辑。
func (s *RelationalStore) Session(sessionID string) (SessionMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, ok := s.sessions[sessionID]
	return meta, ok
}

// Shutdown executes the Shutdown logic.
// Shutdown 用于执行 Shutdown 逻辑。
func (s *RelationalStore) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
