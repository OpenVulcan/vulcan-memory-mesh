// memory_query_test.go verifies the grouped memory-query and turn-detail lookup use cases.
// memory_query_test.go 用于验证分组记忆查询与 turn 详情读取用例。
package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestMemoryUseCaseSearchEchoesGroupedQueries verifies the grouped JSON payload is parsed, echoed back, and resolved against the scoped vector filter.
// TestMemoryUseCaseSearchEchoesGroupedQueries 用于验证分组 JSON 载荷会被解析、原样回显，并按已解析 scope 过滤向量检索。
func TestMemoryUseCaseSearchEchoesGroupedQueries(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
				UserName:    "alice",
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
				TeamName:    "TeamA",
				SpaceName:   "SpaceA",
				ProjectName: "ProjectA",
			},
		},
	}
	turns := &stubTurnLookupStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{
				ID:    "vec-1",
				Text:  "用户喜欢吃香蕉。",
				Score: 0.91,
				Metadata: map[string]string{
					"turn_id":    "41",
					"session_id": "12",
					"category":   "3",
					"details":    "来自近期饮食偏好提炼。",
				},
			},
		},
	}
	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"background":"用户最近一直在讨论水果和饮品。","query":"喜欢的水果"}]`,
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d", len(result.Results))
	}
	group := result.Results[0]
	if group.QueryIndex != 0 || group.Background != "用户最近一直在讨论水果和饮品。" || group.Query != "喜欢的水果" {
		t.Fatalf("unexpected group echo: %+v", group)
	}
	if len(group.Hits) != 1 || group.Hits[0].TurnID != 41 || group.Hits[0].SessionID != 12 || group.Hits[0].Category != 3 {
		t.Fatalf("unexpected hits: %+v", group.Hits)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 || !strings.Contains(embedding.requests[0].Texts[0], "关键语句") {
		t.Fatalf("unexpected embedding request: %+v", embedding.requests)
	}
	if len(vector.searchFilters) != 1 {
		t.Fatalf("search filter count = %d", len(vector.searchFilters))
	}
	filter := vector.searchFilters[0]
	if filter.UserID != 7 || filter.TeamID != 3 || filter.SpaceID != 5 || filter.ProjectID != 9 || filter.SessionID != 0 {
		t.Fatalf("unexpected vector filter: %+v", filter)
	}
}

// TestMemoryUseCaseGetTurnsPreservesRequestedOrder verifies turn lookups keep the caller-supplied order while deduplicating repeated ids.
// TestMemoryUseCaseGetTurnsPreservesRequestedOrder 用于验证 turn 查询会在去重后保持调用方给定的顺序。
func TestMemoryUseCaseGetTurnsPreservesRequestedOrder(t *testing.T) {
	uc := NewMemoryUseCase(nil, &stubTurnLookupStore{
		rows: []logicdomain.SessionTurnRecord{
			{ID: 7, SessionID: 1, ProjectID: 9, DehydratedContent: `{"user":"a"}`, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(2000)},
			{ID: 9, SessionID: 1, ProjectID: 9, DehydratedContent: `{"user":"b"}`, CreatedAt: time.UnixMilli(3000), UpdatedAt: time.UnixMilli(4000)},
		},
	}, nil, nil, nil)

	result, err := uc.GetTurns(context.Background(), TurnDetailCommand{TurnIDs: []uint64{9, 7, 9}})
	if err != nil {
		t.Fatalf("get turns: %v", err)
	}
	if len(result.Turns) != 2 || result.Turns[0].ID != 9 || result.Turns[1].ID != 7 {
		t.Fatalf("unexpected turn order: %+v", result.Turns)
	}
}

// stubTurnLookupStore supplies deterministic turn rows for memory-query tests.
// stubTurnLookupStore 用于为记忆查询测试提供确定性的 turn 行。
type stubTurnLookupStore struct {
	turnIDs []uint64
	rows    []logicdomain.SessionTurnRecord
	err     error
}

// LoadTurnsByIDs records the requested ids and returns the canned rows.
// LoadTurnsByIDs 用于记录请求的 id，并返回预设行。
func (s *stubTurnLookupStore) LoadTurnsByIDs(_ context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	s.turnIDs = append([]uint64(nil), turnIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.SessionTurnRecord(nil), s.rows...), nil
}
