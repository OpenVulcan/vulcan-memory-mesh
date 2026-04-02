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
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{
				ID:              201,
				OriginSessionID: 12,
				SourceTurnID:    41,
				SourceKind:      logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:      logicdomain.MemoryScopeLevelProject,
				Category:        3,
				Abstract:        "用户喜欢吃香蕉。",
				Details:         "来自近期饮食偏好提炼。",
				VectorID:        "vec-1",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{
				ID:    "vec-1",
				Text:  "用户喜欢吃香蕉。",
				Score: 0.91,
				Filter: logicdomain.SearchFilter{
					SessionID: 12,
				},
				Metadata: map[string]string{
					"turn_id":  "41",
					"category": "3",
					"details":  "来自近期饮食偏好提炼。",
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
	if len(group.Hits) != 1 || group.Hits[0].MemoryRef.ID != 201 || group.Hits[0].SourceRef.ID != 41 || group.Hits[0].SessionID != 12 || group.Hits[0].Category != 3 {
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

// TestMemoryUseCaseSearchAppliesRerank verifies the optional rerank layer can reorder first-stage vector hits before the grouped response is returned.
// TestMemoryUseCaseSearchAppliesRerank 用于验证可选 rerank 层会在返回分组结果前重排首轮向量命中。
func TestMemoryUseCaseSearchAppliesRerank(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser: {
				ProfileType: logicdomain.ProfileTypeUser,
				BindID:      7,
				UserID:      7,
			},
			logicdomain.ProfileTypeProject: {
				ProfileType: logicdomain.ProfileTypeProject,
				BindID:      9,
				UserID:      7,
				TeamID:      3,
				SpaceID:     5,
				ProjectID:   9,
			},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第一条", Details: "第一条详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第二条", Details: "第二条详情", VectorID: "vec-2"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "第一条", Score: 0.91},
			{ID: "vec-2", Text: "第二条", Score: 0.89},
		},
	}
	reranker := &stubRerankerClient{
		results: []appports.RerankerResult{
			{ID: "202", Score: 0.99},
			{ID: "201", Score: 0.27},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureRerank(reranker, 5)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"background":"最近在讨论排序。","query":"文本排序模型"}]`,
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events with rerank: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[0].Score != 0.99 {
		t.Fatalf("unexpected first reranked hit: %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 201 || result.Results[0].Hits[1].Score != 0.27 {
		t.Fatalf("unexpected second reranked hit: %+v", result.Results[0].Hits[1])
	}
	if len(reranker.requests) != 1 || reranker.requests[0].query == "" || len(reranker.requests[0].docs) != 2 {
		t.Fatalf("unexpected rerank requests: %+v", reranker.requests)
	}
}

// TestMemoryUseCaseSearchDegradesWhenRerankFails verifies the main search flow still succeeds when the optional rerank backend errors.
// TestMemoryUseCaseSearchDegradesWhenRerankFails 用于验证可选 rerank 后端报错时，主搜索流程仍会降级成功返回。
func TestMemoryUseCaseSearchDegradesWhenRerankFails(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第一条", Details: "第一条详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第二条", Details: "第二条详情", VectorID: "vec-2"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "第一条", Score: 0.91},
			{ID: "vec-2", Text: "第二条", Score: 0.89},
		},
	}
	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureRerank(&stubRerankerClient{err: context.DeadlineExceeded}, 5)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"query":"文本排序模型"}]`,
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events degrade: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 201 || result.Results[0].Hits[0].Score != 0.91 {
		t.Fatalf("unexpected degraded first hit: %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 202 || result.Results[0].Hits[1].Score != 0.89 {
		t.Fatalf("unexpected degraded second hit: %+v", result.Results[0].Hits[1])
	}
}

// TestMemoryUseCaseGetTurnsPreservesRequestedOrder verifies turn lookups keep the caller-supplied order while deduplicating repeated ids.
// TestMemoryUseCaseGetTurnsPreservesRequestedOrder 用于验证 turn 查询会在去重后保持调用方给定的顺序。
func TestMemoryUseCaseGetTurnsPreservesRequestedOrder(t *testing.T) {
	uc := NewMemoryUseCase(nil, &stubTurnLookupStore{
		rows: []logicdomain.SessionTurnRecord{
			{ID: 7, SessionID: 1, ProjectID: 9, DehydratedContent: `{"user":"a","timeline":[{"type":"assistant","content":"mid-a"}],"assistant":"aa"}`, CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(2000)},
			{ID: 9, SessionID: 1, ProjectID: 9, DehydratedContent: `{"user":"b","timeline":[],"assistant":"bb"}`, CreatedAt: time.UnixMilli(3000), UpdatedAt: time.UnixMilli(4000)},
		},
		windows: map[uint64]logicdomain.TurnDetailWindow{
			7: {TurnID: 7, PreviousTurnIDs: []uint64{4, 5, 6}, NextTurnIDs: []uint64{8, 9, 10}},
			9: {TurnID: 9, PreviousTurnIDs: []uint64{6, 7, 8}, NextTurnIDs: []uint64{10, 11, 12}},
		},
	}, nil, nil, nil)

	result, err := uc.GetTurns(context.Background(), TurnDetailCommand{TurnIDs: []uint64{9, 7, 9}})
	if err != nil {
		t.Fatalf("get turns: %v", err)
	}
	if len(result.Turns) != 2 || result.Turns[0].Turn.ID != 9 || result.Turns[1].Turn.ID != 7 {
		t.Fatalf("unexpected turn order: %+v", result.Turns)
	}
	if result.Turns[0].UserContent != "b" || result.Turns[0].AssistantContent != "bb" {
		t.Fatalf("unexpected parsed turn content: %+v", result.Turns[0])
	}
	if len(result.Turns[0].PreviousTurnIDs) != 3 || result.Turns[0].PreviousTurnIDs[0] != 6 || len(result.Turns[1].Timeline) != 1 {
		t.Fatalf("unexpected turn windows or timeline: %+v %+v", result.Turns[0], result.Turns[1])
	}
}

// stubTurnLookupStore supplies deterministic turn rows for memory-query tests.
// stubTurnLookupStore 用于为记忆查询测试提供确定性的 turn 行。
type stubTurnLookupStore struct {
	turnIDs            []uint64
	rows               []logicdomain.SessionTurnRecord
	windows            map[uint64]logicdomain.TurnDetailWindow
	memoryRowsByID     []logicdomain.MemoryNodeRecord
	memoryRowsByVector []logicdomain.MemoryNodeRecord
	err                error
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

// LoadTurnWindows returns canned neighboring turn ids for deterministic turn-detail assertions.
// LoadTurnWindows 用于返回预设的相邻 turn 编号，保证 turn 详情断言稳定。
func (s *stubTurnLookupStore) LoadTurnWindows(_ context.Context, _ []uint64, _ int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.windows == nil {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
	cloned := make(map[uint64]logicdomain.TurnDetailWindow, len(s.windows))
	for key, value := range s.windows {
		cloned[key] = logicdomain.TurnDetailWindow{
			TurnID:          value.TurnID,
			PreviousTurnIDs: append([]uint64(nil), value.PreviousTurnIDs...),
			NextTurnIDs:     append([]uint64(nil), value.NextTurnIDs...),
		}
	}
	return cloned, nil
}

// LoadMemoryNodesByIDs returns canned memory rows for mixed memory-detail assertions.
// LoadMemoryNodesByIDs 用于返回混合记忆详情断言所需的预设记忆行。
func (s *stubTurnLookupStore) LoadMemoryNodesByIDs(_ context.Context, _ []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryNodeRecord(nil), s.memoryRowsByID...), nil
}

// LoadMemoryNodesByVectorIDs returns canned memory rows for search-hit enrichment assertions.
// LoadMemoryNodesByVectorIDs 用于返回搜索命中补全断言所需的预设记忆行。
func (s *stubTurnLookupStore) LoadMemoryNodesByVectorIDs(_ context.Context, _ []string) ([]logicdomain.MemoryNodeRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryNodeRecord(nil), s.memoryRowsByVector...), nil
}

// FindRecentActiveMemoryByDedupe keeps the stub interface-complete for tests that only exercise search and turn-detail flows.
// FindRecentActiveMemoryByDedupe 用于补齐测试桩接口，因为当前这些测试只覆盖搜索和 turn 详情流程。
func (s *stubTurnLookupStore) FindRecentActiveMemoryByDedupe(_ context.Context, _ logicdomain.SessionRef, _, _ int, _ string, _ time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	if s.err != nil {
		return logicdomain.MemoryNodeRecord{}, false, s.err
	}
	return logicdomain.MemoryNodeRecord{}, false, nil
}

// CreateDirectMemoryNode keeps the stub interface-complete for tests that do not exercise direct-write persistence.
// CreateDirectMemoryNode 用于补齐测试桩接口，因为当前这些测试不覆盖主动写入持久化。
func (s *stubTurnLookupStore) CreateDirectMemoryNode(_ context.Context, _ logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if s.err != nil {
		return logicdomain.MemoryNodeRecord{}, s.err
	}
	record.ID = 1
	return record, nil
}

// stubRerankerClient records rerank calls and returns one canned response so search tests can verify ordering changes without a live provider.
// stubRerankerClient 用于记录 rerank 调用并返回预设结果，让搜索测试无需真实 provider 也能验证排序变化。
type stubRerankerClient struct {
	requests []stubRerankRequest
	results  []appports.RerankerResult
	err      error
}

// stubRerankRequest stores one recorded rerank query plus the candidate documents seen by the stub.
// stubRerankRequest 用于保存一条被桩记录下来的 rerank query 以及其候选文档。
type stubRerankRequest struct {
	query string
	docs  []appports.RerankerDocument
	topN  int
}

// Rerank records the request and returns the canned provider result.
// Rerank 用于记录请求并返回预设 provider 结果。
func (s *stubRerankerClient) Rerank(_ context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	s.requests = append(s.requests, stubRerankRequest{
		query: query,
		docs:  append([]appports.RerankerDocument(nil), docs...),
		topN:  topN,
	})
	if s.err != nil {
		return nil, s.err
	}
	return append([]appports.RerankerResult(nil), s.results...), nil
}
