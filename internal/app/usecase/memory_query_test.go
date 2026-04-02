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
				SupportCount:    2,
				RebuttalCount:   1,
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
	if group.Hits[0].SupportCount != 2 || group.Hits[0].RebuttalCount != 1 {
		t.Fatalf("expected context evidence counts to be preserved, got %+v", group.Hits[0])
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

// TestMemoryUseCaseSearchFusesHybridRecall verifies vector recall and lexical recall are fused through RRF before the grouped response is returned.
// TestMemoryUseCaseSearchFusesHybridRecall 用于验证向量召回和 lexical 召回会先经过 RRF 融合，再返回最终的分组结果。
func TestMemoryUseCaseSearchFusesHybridRecall(t *testing.T) {
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
		memoryRowsByID: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "向量命中", Details: "向量详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "混合命中", Details: "混合详情", VectorID: "vec-2"},
		},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "向量命中", Details: "向量详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "混合命中", Details: "混合详情", VectorID: "vec-2"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 202, Score: 0.98},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "向量命中", Score: 0.91},
			{ID: "vec-2", Text: "混合命中", Score: 0.84},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureHybrid(true, 5, 60)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"background":"最近在讨论排序和检索。","query":"混合检索"}]`,
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events with hybrid recall: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected hybrid results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 {
		t.Fatalf("expected hybrid candidate to rank first, got %+v", result.Results[0].Hits)
	}
	if result.Results[0].Hits[0].Origin != "hybrid_rrf" {
		t.Fatalf("expected hybrid origin, got %+v", result.Results[0].Hits[0])
	}
	if len(turns.lexicalQueries) != 1 || turns.lexicalQueries[0] != "混合检索" {
		t.Fatalf("unexpected lexical queries: %+v", turns.lexicalQueries)
	}
	if len(turns.lexicalTopKs) != 1 || turns.lexicalTopKs[0] != 5 {
		t.Fatalf("unexpected lexical top-k values: %+v", turns.lexicalTopKs)
	}
	if len(turns.lexicalFilters) != 1 {
		t.Fatalf("unexpected lexical filter count: %+v", turns.lexicalFilters)
	}
	filter := turns.lexicalFilters[0]
	if filter.UserID != 7 || filter.TeamID != 3 || filter.SpaceID != 5 || filter.ProjectID != 9 {
		t.Fatalf("unexpected lexical filter: %+v", filter)
	}
}

// TestMemoryUseCaseSearchAppliesMMRDiversity verifies the final candidate list keeps broader coverage instead of returning multiple near-duplicate high-score memories.
// TestMemoryUseCaseSearchAppliesMMRDiversity 用于验证最终候选列表会保留更广的覆盖面，而不是返回多个近重复的高分记忆。
func TestMemoryUseCaseSearchAppliesMMRDiversity(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉偏好", Details: "喜欢香蕉奶昔", VectorID: "vec-1", Vector: []float32{1, 0}},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉饮品", Details: "经常点香蕉奶昔", VectorID: "vec-2", Vector: []float32{0.99, 0.01}},
			{ID: 203, OriginSessionID: 12, SourceTurnID: 43, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "咖啡偏好", Details: "上午会点美式咖啡", VectorID: "vec-3", Vector: []float32{0, 1}},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "香蕉偏好", Score: 0.95},
			{ID: "vec-2", Text: "香蕉饮品", Score: 0.94},
			{ID: "vec-3", Text: "咖啡偏好", Score: 0.80},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureMMR(true, 0.75)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"query":"饮品偏好"}]`,
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events with mmr: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected mmr results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 201 || result.Results[0].Hits[1].MemoryRef.ID != 203 {
		t.Fatalf("expected diversified hits [201 203], got %+v", result.Results[0].Hits)
	}
	if result.Results[0].Hits[0].Origin != "vector_mmr" || result.Results[0].Hits[1].Origin != "vector_mmr" {
		t.Fatalf("expected mmr origin labels, got %+v", result.Results[0].Hits)
	}
	if len(vector.searchTopKs) != 1 || vector.searchTopKs[0] != 4 {
		t.Fatalf("expected mmr to enlarge candidate pool to 4, got %+v", vector.searchTopKs)
	}
}

// TestMemoryUseCaseSearchKeepsRankOrderWhenMMRDisabled verifies the search order remains unchanged when the diversity stage is not enabled.
// TestMemoryUseCaseSearchKeepsRankOrderWhenMMRDisabled 用于验证在未启用多样性阶段时，检索顺序会保持不变。
func TestMemoryUseCaseSearchKeepsRankOrderWhenMMRDisabled(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉偏好", Details: "喜欢香蕉奶昔", VectorID: "vec-1", Vector: []float32{1, 0}},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉饮品", Details: "经常点香蕉奶昔", VectorID: "vec-2", Vector: []float32{0.99, 0.01}},
			{ID: 203, OriginSessionID: 12, SourceTurnID: 43, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "咖啡偏好", Details: "上午会点美式咖啡", VectorID: "vec-3", Vector: []float32{0, 1}},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "香蕉偏好", Score: 0.95},
			{ID: "vec-2", Text: "香蕉饮品", Score: 0.94},
			{ID: "vec-3", Text: "咖啡偏好", Score: 0.80},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"query":"饮品偏好"}]`,
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events without mmr: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected non-mmr results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 201 || result.Results[0].Hits[1].MemoryRef.ID != 202 {
		t.Fatalf("expected original hits [201 202], got %+v", result.Results[0].Hits)
	}
	if len(vector.searchTopKs) != 1 || vector.searchTopKs[0] != 2 {
		t.Fatalf("expected original candidate pool to stay at 2, got %+v", vector.searchTopKs)
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

// TestMemoryUseCaseSearchAppliesWeibullDecay verifies read-time Weibull decay can demote stale weakly reinforced memories beneath fresher reinforced memories.
// TestMemoryUseCaseSearchAppliesWeibullDecay 用于验证读时 Weibull 衰减会把陈旧且强化较弱的记忆降到更新且强化更强的记忆之后。
func TestMemoryUseCaseSearchAppliesWeibullDecay(t *testing.T) {
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
	now := time.Now().UTC()
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{
				ID:                 201,
				OriginSessionID:    12,
				SourceTurnID:       41,
				SourceKind:         logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:         logicdomain.MemoryScopeLevelProject,
				Priority:           logicdomain.MemoryPriorityP2,
				MemoryLevel:        logicdomain.MemoryLevelPhase,
				Category:           3,
				Abstract:           "旧实现方案",
				Details:            "这是很早以前确认过的一条弱强化记忆",
				VectorID:           "vec-1",
				CreatedAt:          now.Add(-180 * 24 * time.Hour),
				LastReinforcedAt:   now.Add(-120 * 24 * time.Hour),
				ReinforcementCount: 0,
			},
			{
				ID:                       202,
				OriginSessionID:          12,
				SourceTurnID:             42,
				SourceKind:               logicdomain.MemorySourceKindTurnExtract,
				ScopeLevel:               logicdomain.MemoryScopeLevelProject,
				Priority:                 logicdomain.MemoryPriorityP1,
				MemoryLevel:              logicdomain.MemoryLevelStable,
				Category:                 3,
				Abstract:                 "新实现方案",
				Details:                  "这是最近多次被跨会话采纳的一条稳定记忆",
				VectorID:                 "vec-2",
				CreatedAt:                now.Add(-20 * 24 * time.Hour),
				LastReinforcedAt:         now.Add(-2 * 24 * time.Hour),
				ReinforcementCount:       4,
				CrossSessionAdoptedCount: 3,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "旧实现方案", Score: 0.96},
			{ID: "vec-2", Text: "新实现方案", Score: 0.91},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureDecay(true, 1.35, 2160, 0.4, 0.18, 0.12)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"query":"当前实现方案"}]`,
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events with weibull decay: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected weibull results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[1].MemoryRef.ID != 201 {
		t.Fatalf("expected reinforced memory to rank first after decay, got %+v", result.Results[0].Hits)
	}
	if result.Results[0].Hits[0].Score <= result.Results[0].Hits[1].Score {
		t.Fatalf("expected first decayed score to stay above second: %+v", result.Results[0].Hits)
	}
}

// TestMemoryUseCaseSearchAppliesContextAwareScoring verifies matched context edges can boost supportive memories and demote rebutted memories before MMR runs.
// TestMemoryUseCaseSearchAppliesContextAwareScoring 用于验证命中的 context edge 会在 MMR 之前提升支持记忆、压低反驳记忆。
func TestMemoryUseCaseSearchAppliesContextAwareScoring(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 旧方案", Details: "这条记忆被当前场景反驳", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 新方案", Details: "这条记忆被当前场景支持", VectorID: "vec-2"},
		},
		memoryContextEdges: []logicdomain.MemoryContextEdge{
			{MemoryID: 201, ContextKey: "deployment_mode", ContextValue: "local oss", RebuttalCount: 2},
			{MemoryID: 202, ContextKey: "deployment_mode", ContextValue: "local oss", SupportCount: 2},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "phase4 旧方案", Score: 0.91},
			{ID: "vec-2", Text: "phase4 新方案", Score: 0.91},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"background":"当前部署模式仍然是 local_oss。","query":"phase4 当前方案"}]`,
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events with context-aware scoring: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[1].MemoryRef.ID != 201 {
		t.Fatalf("expected supportive context edge to win, got %+v", result.Results[0].Hits)
	}
	if len(turns.contextLookupIDs) != 2 || turns.contextLookupIDs[0] != 201 || turns.contextLookupIDs[1] != 202 {
		t.Fatalf("expected context edge lookup over both candidate ids, got %+v", turns.contextLookupIDs)
	}
}

// TestMemoryUseCaseSearchSkipsContextScoringWhenNothingMatches verifies unrelated context edges do not perturb the existing ranked order.
// TestMemoryUseCaseSearchSkipsContextScoringWhenNothingMatches 用于验证无关 context edge 不会扰动现有排序。
func TestMemoryUseCaseSearchSkipsContextScoringWhenNothingMatches(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "旧排序第一", Details: "第一条", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "旧排序第二", Details: "第二条", VectorID: "vec-2"},
		},
		memoryContextEdges: []logicdomain.MemoryContextEdge{
			{MemoryID: 201, ContextKey: "deployment_mode", ContextValue: "cloud", SupportCount: 3},
			{MemoryID: 202, ContextKey: "task_stage", ContextValue: "phase2", RebuttalCount: 2},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "旧排序第一", Score: 0.93},
			{ID: "vec-2", Text: "旧排序第二", Score: 0.91},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		QueryJSON: `[{"background":"当前仍然是 local_oss。","query":"phase4 当前方案"}]`,
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events without matching context edges: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 201 || result.Results[0].Hits[1].MemoryRef.ID != 202 {
		t.Fatalf("expected original order to remain unchanged, got %+v", result.Results[0].Hits)
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
	memoryContextEdges []logicdomain.MemoryContextEdge
	memoryRowsByVector []logicdomain.MemoryNodeRecord
	lexicalHits        []logicdomain.MemoryLexicalHit
	lexicalQueries     []string
	lexicalTopKs       []int
	lexicalFilters     []logicdomain.SearchFilter
	contextLookupIDs   []uint64
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

// LoadMemoryContextEdgesByMemoryIDs returns canned context edges for context-aware retrieval assertions.
// LoadMemoryContextEdgesByMemoryIDs 用于返回情境感知检索断言需要的预设 context edges。
func (s *stubTurnLookupStore) LoadMemoryContextEdgesByMemoryIDs(_ context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	s.contextLookupIDs = append([]uint64(nil), memoryIDs...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryContextEdge(nil), s.memoryContextEdges...), nil
}

// LoadMemoryNodesByVectorIDs returns canned memory rows for search-hit enrichment assertions.
// LoadMemoryNodesByVectorIDs 用于返回搜索命中补全断言所需的预设记忆行。
func (s *stubTurnLookupStore) LoadMemoryNodesByVectorIDs(_ context.Context, _ []string) ([]logicdomain.MemoryNodeRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryNodeRecord(nil), s.memoryRowsByVector...), nil
}

// SearchLexicalMemory records the lexical search request and returns canned lexical hits for hybrid-recall assertions.
// SearchLexicalMemory 用于记录 lexical 搜索请求，并返回预设 lexical 命中，供混合召回断言使用。
func (s *stubTurnLookupStore) SearchLexicalMemory(_ context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	s.lexicalQueries = append(s.lexicalQueries, query)
	s.lexicalTopKs = append(s.lexicalTopKs, topK)
	s.lexicalFilters = append(s.lexicalFilters, filter)
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryLexicalHit(nil), s.lexicalHits...), nil
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
