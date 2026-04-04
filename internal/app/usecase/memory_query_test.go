// memory_query_test.go verifies the grouped memory-query and turn-detail lookup use cases.
// memory_query_test.go 用于验证分组记忆查询与 turn 详情读取用例。
package usecase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// TestMemoryUseCaseSearchEchoesQueries verifies the simplified query list is normalized, echoed back, and resolved against the scoped vector filter.
// TestMemoryUseCaseSearchEchoesQueries 用于验证简化后的查询列表会被规范化、原样回显，并按已解析 scope 过滤向量检索。
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
		Queries:   []string{"喜欢的水果"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d", len(result.Results))
	}
	group := result.Results[0]
	if group.QueryIndex != 0 || group.Query != "喜欢的水果" {
		t.Fatalf("unexpected group echo: %+v", group)
	}
	if len(group.Hits) != 1 || group.Hits[0].MemoryRef.ID != 201 || group.Hits[0].SourceRef.ID != 41 || group.Hits[0].SessionID != 12 || group.Hits[0].Category != 3 {
		t.Fatalf("unexpected hits: %+v", group.Hits)
	}
	if group.Hits[0].SupportCount != 2 || group.Hits[0].RebuttalCount != 1 {
		t.Fatalf("expected context evidence counts to be preserved, got %+v", group.Hits[0])
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 || embedding.requests[0].Texts[0] != "喜欢的水果" {
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

// TestMemoryUseCaseSearchNormalizesQueryWhitespace verifies the general MemoryQuery entry normalizes query whitespace before echoing it back and before building embedding text.
// TestMemoryUseCaseSearchNormalizesQueryWhitespace 用于验证通用 MemoryQuery 入口会先归一 query 空白，再回显并构建 embedding 文本。
func TestMemoryUseCaseSearchNormalizesGroupedQueryWhitespace(t *testing.T) {
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
			{ID: "vec-1", Text: "用户喜欢吃香蕉。", Score: 0.91},
		},
	}
	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"喜欢的\n水果"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d", len(result.Results))
	}
	group := result.Results[0]
	if group.Query != "喜欢的 水果" {
		t.Fatalf("expected normalized grouped echo, got %+v", group)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 {
		t.Fatalf("unexpected embedding request: %+v", embedding.requests)
	}
	if strings.Contains(embedding.requests[0].Texts[0], "\n一直在讨论   ") || strings.Contains(embedding.requests[0].Texts[0], "喜欢的\n水果") {
		t.Fatalf("expected normalized embedding text, got %q", embedding.requests[0].Texts[0])
	}
}

// TestMemoryUseCaseSearchAppliesScopeOverrideToFilters verifies specialized callers can widen memory recall from project to space or team without changing the default grouped search behavior.
// TestMemoryUseCaseSearchAppliesScopeOverrideToFilters 用于验证特殊调用方可以把记忆召回从 project 放宽到 space 或 team，同时不改变默认分组搜索行为。
func TestMemoryUseCaseSearchAppliesScopeOverrideToFilters(t *testing.T) {
	cases := []struct {
		name          string
		scopeOverride string
		wantTeamID    uint64
		wantSpaceID   uint64
		wantProjectID uint64
	}{
		{name: "default project", scopeOverride: "", wantTeamID: 3, wantSpaceID: 5, wantProjectID: 9},
		{name: "project explicit", scopeOverride: "project", wantTeamID: 3, wantSpaceID: 5, wantProjectID: 9},
		{name: "space", scopeOverride: "space", wantTeamID: 3, wantSpaceID: 5, wantProjectID: 0},
		{name: "team", scopeOverride: "team", wantTeamID: 3, wantSpaceID: 0, wantProjectID: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
					{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户曾考虑卡宴。", VectorID: "vec-1"},
				},
				memoryRowsByVector: []logicdomain.MemoryNodeRecord{
					{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户曾考虑卡宴。", VectorID: "vec-1"},
				},
				lexicalHits: []logicdomain.MemoryLexicalHit{
					{MemoryID: 201, Score: 0.9},
				},
			}
			embedding := &stubEmbeddingClient{
				response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
			}
			vector := &stubVectorStore{
				searchHits: []logicdomain.MemoryHit{
					{ID: "vec-1", Text: "用户曾考虑卡宴。", Score: 0.91},
				},
			}
			uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
			uc.ConfigureHybrid(true, 5, 60)

			if _, err := uc.Search(context.Background(), MemoryQueryCommand{
				UserID:        7,
				ProjectID:     9,
				Queries:       []string{"卡宴"},
				TopK:          5,
				ScopeOverride: tc.scopeOverride,
			}); err != nil {
				t.Fatalf("search memory events: %v", err)
			}

			if len(vector.searchFilters) != 1 {
				t.Fatalf("vector search filter count = %d", len(vector.searchFilters))
			}
			if len(turns.lexicalFilters) != 1 {
				t.Fatalf("lexical filter count = %d", len(turns.lexicalFilters))
			}
			vectorFilter := vector.searchFilters[0]
			lexicalFilter := turns.lexicalFilters[0]
			if vectorFilter.TeamID != tc.wantTeamID || vectorFilter.SpaceID != tc.wantSpaceID || vectorFilter.ProjectID != tc.wantProjectID {
				t.Fatalf("unexpected vector filter: %+v", vectorFilter)
			}
			if lexicalFilter.TeamID != tc.wantTeamID || lexicalFilter.SpaceID != tc.wantSpaceID || lexicalFilter.ProjectID != tc.wantProjectID {
				t.Fatalf("unexpected lexical filter: %+v", lexicalFilter)
			}
			if vectorFilter.UserID != 7 || lexicalFilter.UserID != 7 {
				t.Fatalf("expected user filter to stay intact, got vector=%+v lexical=%+v", vectorFilter, lexicalFilter)
			}
		})
	}
}

// TestMemoryUseCaseSearchReusesEquivalentGroupedQueries verifies one request reuses the full retrieval chain for equivalent query groups instead of repeating embedding/vector/hybrid/rerank work.
// TestMemoryUseCaseSearchReusesEquivalentGroupedQueries 用于验证同一次请求里的等价 query group 会复用整条检索链，而不是重复触发 embedding、向量、hybrid 和 rerank 工作。
func TestMemoryUseCaseSearchReusesEquivalentGroupedQueries(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉偏好", Details: "用户更常提到香蕉。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "苹果偏好", Details: "用户也常提到苹果。", VectorID: "vec-2"},
		},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "香蕉偏好", Details: "用户更常提到香蕉。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "苹果偏好", Details: "用户也常提到苹果。", VectorID: "vec-2"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 202, Score: 0.99},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "香蕉偏好", Score: 0.91},
			{ID: "vec-2", Text: "苹果偏好", Score: 0.84},
		},
	}
	reranker := &stubRerankerClient{
		results: []appports.RerankerResult{
			{ID: "202", Score: 0.97},
			{ID: "201", Score: 0.88},
		},
	}
	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureHybrid(true, 5, 60)
	uc.ConfigureRerank(reranker, 2)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"喜欢的水果", "喜欢的水果"},
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results len = %d", len(result.Results))
	}
	if len(result.Results[0].Hits) != 2 || len(result.Results[1].Hits) != 2 {
		t.Fatalf("expected duplicated groups to keep full hit lists, got %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[1].Hits[0].MemoryRef.ID != 202 {
		t.Fatalf("expected reused reranked order, got %+v", result.Results)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 {
		t.Fatalf("expected one deduped embedding request, got %+v", embedding.requests)
	}
	if len(vector.searchTopKs) != 1 {
		t.Fatalf("expected one deduped vector search, got %+v", vector.searchTopKs)
	}
	if len(turns.lexicalQueries) != 1 {
		t.Fatalf("expected one deduped lexical search, got %+v", turns.lexicalQueries)
	}
	if len(reranker.requests) != 1 {
		t.Fatalf("expected one deduped rerank request, got %+v", reranker.requests)
	}
}

// TestMemoryUseCaseSearchReusesCaseEquivalentGroupedQueries verifies the in-request cache key also collapses case-only group variants so technical English queries do not repeat the full retrieval pipeline.
// TestMemoryUseCaseSearchReusesCaseEquivalentGroupedQueries 用于验证单请求缓存键也会折叠仅有大小写差异的 group，避免英文技术查询因大小写不同而重复执行整条检索链。
func TestMemoryUseCaseSearchReusesCaseEquivalentGroupedQueries(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "Schema Compatibility", Details: "The project stayed on schema version 13 for compatibility.", VectorID: "vec-1"},
		},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "Schema Compatibility", Details: "The project stayed on schema version 13 for compatibility.", VectorID: "vec-1"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "Schema Compatibility", Score: 0.91},
		},
	}
	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"Schema Version 13", "schema version 13"},
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results len = %d", len(result.Results))
	}
	if len(result.Results[0].Hits) != 1 || len(result.Results[1].Hits) != 1 {
		t.Fatalf("expected both groups to reuse one hit set, got %+v", result.Results)
	}
	if result.Results[0].Query != "Schema Version 13" || result.Results[1].Query != "schema version 13" {
		t.Fatalf("expected original caller echo to stay intact, got %+v", result.Results)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 {
		t.Fatalf("expected one deduped embedding request for case variants, got %+v", embedding.requests)
	}
	if len(vector.searchTopKs) != 1 {
		t.Fatalf("expected one deduped vector search for case variants, got %+v", vector.searchTopKs)
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
		Queries:   []string{"混合检索"},
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

// TestMemoryUseCaseSearchPrefersCombinedHybridSQL verifies combined PostgreSQL mode can skip the old lexical fan-out and consume one SQL-fused first-stage candidate list directly.
// TestMemoryUseCaseSearchPrefersCombinedHybridSQL 用于验证 PostgreSQL 组合模式可以跳过旧的 lexical 分叉路径，直接消费单条 SQL 融合后的一阶段候选列表。
func TestMemoryUseCaseSearchPrefersCombinedHybridSQL(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "向量命中", Details: "向量详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "混合命中", Details: "混合详情", VectorID: "vec-2"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubCombinedHybridVectorStore{
		stubVectorStore: stubVectorStore{
			searchHits: []logicdomain.MemoryHit{
				{ID: "vec-1", Text: "向量命中", Score: 0.91},
			},
		},
		hybridSearchHits: []logicdomain.MemoryHit{
			{ID: "vec-2", Text: "混合命中", Score: 0.03278688524590164, Metadata: map[string]string{"origin": "hybrid_rrf"}},
			{ID: "vec-1", Text: "向量命中", Score: 0.01639344262295082, Metadata: map[string]string{"origin": "vector_search"}},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureHybrid(true, 5, 60)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"混合检索"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events with combined hybrid sql: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected combined hybrid results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 {
		t.Fatalf("expected SQL-fused hybrid candidate to rank first, got %+v", result.Results[0].Hits)
	}
	if result.Results[0].Hits[0].Origin != "hybrid_rrf" {
		t.Fatalf("expected SQL-fused origin, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[0].Score < 0.99 {
		t.Fatalf("expected SQL-fused first hit score to be normalized into stable caller-facing semantics, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].Score < 0.74 || result.Results[0].Hits[1].Score > 0.76 {
		t.Fatalf("expected second SQL-fused hit to keep rank-normalized score semantics, got %+v", result.Results[0].Hits[1])
	}
	if len(vector.hybridQueries) != 1 || vector.hybridQueries[0] != "混合检索" {
		t.Fatalf("unexpected hybrid queries: %+v", vector.hybridQueries)
	}
	if len(vector.searchTopKs) != 0 {
		t.Fatalf("expected combined hybrid SQL to skip plain vector.Search, got %+v", vector.searchTopKs)
	}
	if len(turns.lexicalQueries) != 0 {
		t.Fatalf("expected combined hybrid SQL to skip lexical fan-out, got %+v", turns.lexicalQueries)
	}
}

// TestMemoryUseCaseSearchLogsCombinedHybridRawScoreSeparately verifies combined SQL retrieval logs expose stable caller-facing scores while still preserving the tiny raw RRF score for diagnostics.
// TestMemoryUseCaseSearchLogsCombinedHybridRawScoreSeparately 用于验证组合 SQL 检索日志会输出稳定的对外分数，同时把极小的原始 RRF 分单独保留下来供诊断。
func TestMemoryUseCaseSearchLogsCombinedHybridRawScoreSeparately(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
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
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "混合命中", Details: "混合详情", VectorID: "vec-2"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubCombinedHybridVectorStore{
		hybridSearchHits: []logicdomain.MemoryHit{
			{ID: "vec-2", Text: "混合命中", Score: 0.03278688524590164, Metadata: map[string]string{"origin": "hybrid_rrf"}},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, logger)
	uc.ConfigureHybrid(true, 5, 60)

	if _, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"混合检索"},
		TopK:      5,
	}); err != nil {
		t.Fatalf("search memory events with combined hybrid sql logging: %v", err)
	}

	logs := logBuf.String()
	expectedFragments := []string{
		`MSG："memory search first-stage combined sql completed"`,
		`"score": 1`,
		`"raw_score": 0.03278688524590164`,
		`"origin": "hybrid_rrf"`,
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected combined hybrid log to contain %s, got %s", fragment, logs)
		}
	}
}

// TestMemoryUseCaseSearchLogsRetrievalStageCounts verifies unified retrieval now logs vector, hybrid, and final-stage hit counts plus the top candidate snapshots so upstream recall misses are directly observable.
// TestMemoryUseCaseSearchLogsRetrievalStageCounts 用于验证统一检索现在会记录向量、混合和最终阶段的命中数量以及 top 候选快照，让上游召回缺口可以直接观测。
func TestMemoryUseCaseSearchLogsRetrievalStageCounts(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text", DebugPayloads: true})
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户正在比较保时捷卡宴与同价位车型。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "预算范围", Details: "用户把购车预算下调到 100-150 万。", VectorID: "vec-2"},
		},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "卡宴偏好", Details: "用户正在比较保时捷卡宴与同价位车型。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "预算范围", Details: "用户把购车预算下调到 100-150 万。", VectorID: "vec-2"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 202, Score: 0.99},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "用户正在比较保时捷卡宴与同价位车型。", Score: 0.91},
			{ID: "vec-2", Text: "用户把购车预算下调到 100-150 万。", Score: 0.82},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, logger)
	uc.ConfigureHybrid(true, 5, 60)

	if _, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"用户购车预算和车型偏好"},
		TopK:      5,
	}); err != nil {
		t.Fatalf("search memory events: %v", err)
	}

	logs := logBuf.String()
	expectedMessages := []string{
		`MSG："memory search vector stage completed"`,
		`MSG："memory search hybrid stage completed"`,
		`MSG："memory search final stage completed"`,
	}
	for _, message := range expectedMessages {
		if !strings.Contains(logs, message) {
			t.Fatalf("expected retrieval stage log %s, got %s", message, logs)
		}
	}
	expectedFragments := []string{
		`vector_hit_count：2`,
		`lexical_hit_count：1`,
		`final_hit_count：2`,
		`"top_vector_hit":`,
		`"top_hybrid_candidate":`,
		`"top_final_hit":`,
		`"memory_id": 201`,
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(logs, fragment) {
			t.Fatalf("expected retrieval logs to contain %s, got %s", fragment, logs)
		}
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
		Queries:   []string{"饮品偏好"},
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
		Queries:   []string{"饮品偏好"},
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
		Queries:   []string{"文本排序模型"},
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
		Queries:   []string{"文本排序模型"},
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
		Queries:   []string{"当前实现方案"},
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
		Queries:   []string{"当前部署模式仍然是 local_oss。 phase4 当前方案"},
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
	if result.Results[0].Hits[0].MatchedContextSupportCount != 2 || result.Results[0].Hits[0].MatchedContextRebuttalCount != 0 {
		t.Fatalf("expected supportive matched context evidence on first hit, got %+v", result.Results[0].Hits[0])
	}
	if len(result.Results[0].Hits[0].MatchedContextValues) != 1 || result.Results[0].Hits[0].MatchedContextValues[0] != "deployment_mode=local oss" {
		t.Fatalf("expected first hit to expose matched context values, got %+v", result.Results[0].Hits[0].MatchedContextValues)
	}
	if result.Results[0].Hits[0].MatchedContextScoreDelta <= 0 {
		t.Fatalf("expected supportive matched context to produce positive delta, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MatchedContextSupportCount != 0 || result.Results[0].Hits[1].MatchedContextRebuttalCount != 2 || result.Results[0].Hits[1].MatchedContextScoreDelta >= 0 {
		t.Fatalf("expected rebutted matched context evidence on second hit, got %+v", result.Results[0].Hits[1])
	}
	if len(turns.contextLookupIDs) != 2 || turns.contextLookupIDs[0] != 201 || turns.contextLookupIDs[1] != 202 {
		t.Fatalf("expected context edge lookup over both candidate ids, got %+v", turns.contextLookupIDs)
	}
}

// TestMemoryUseCaseSearchRedactsDegradedQueryLogs verifies retrieval degradation logs keep query diagnostics without writing the raw user query into runtime logs.
// TestMemoryUseCaseSearchRedactsDegradedQueryLogs 用于验证检索降级日志会保留 query 诊断信息，但不会把原始用户 query 写入运行时日志。
func TestMemoryUseCaseSearchRedactsDegradedQueryLogs(t *testing.T) {
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
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "部署方案", Score: 0.91},
			{ID: "vec-2", Text: "备用方案", Score: 0.87},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "warn", Format: "text"})

	firstStore := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "部署方案", Details: "向量召回仍然命中。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "备用方案", Details: "用于触发 rerank 降级。", VectorID: "vec-2"},
		},
		lexicalErr: errors.New("fts gateway unavailable"),
	}
	firstReranker := &stubRerankerClient{err: errors.New("rerank timeout")}
	firstQuery := "用户银行卡 1234 的部署方案"
	firstUseCase := NewMemoryUseCase(profiles, firstStore, embedding, vector, logger)
	firstUseCase.ConfigureHybrid(true, 5, 60)
	firstUseCase.ConfigureRerank(firstReranker, 2)

	if _, err := firstUseCase.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{firstQuery},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with lexical/rerank degradation: %v", err)
	}

	secondStore := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "本地部署", Details: "向量召回仍然命中。", VectorID: "vec-1"},
		},
		lexicalHits: []logicdomain.MemoryLexicalHit{
			{MemoryID: 201, Score: 0.99},
		},
		memoryRowsByIDErr: errors.New("load lexical rows failed"),
	}
	secondQuery := "用户身份证 5678 的本地部署"
	secondUseCase := NewMemoryUseCase(profiles, secondStore, embedding, vector, logger)
	secondUseCase.ConfigureHybrid(true, 5, 60)

	if _, err := secondUseCase.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{secondQuery},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with lexical materialization degradation: %v", err)
	}

	logs := logBuf.String()
	if strings.Contains(logs, firstQuery) || strings.Contains(logs, secondQuery) {
		t.Fatalf("expected degraded logs to redact raw queries, got %s", logs)
	}
	if !strings.Contains(logs, "memory lexical search degraded") || !strings.Contains(logs, "memory lexical materialization degraded") || !strings.Contains(logs, "memory search rerank degraded") {
		t.Fatalf("expected all degradation events to be logged, got %s", logs)
	}
	if !strings.Contains(logs, "query_len") || !strings.Contains(logs, "query_sha256") {
		t.Fatalf("expected redacted query diagnostics in logs, got %s", logs)
	}
}

// TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled verifies degraded retrieval logs switch from redacted query summaries to full query text when the shared payload-debug logger flag is enabled for local troubleshooting.
// TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled 用于验证当共享 payload 调试开关开启时，检索降级日志会从脱敏 query 摘要切换为完整 query 文本，方便本地排障。
func TestMemoryUseCaseSearchLogsRawQueriesWhenPayloadDebugEnabled(t *testing.T) {
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
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "部署方案", Score: 0.91},
			{ID: "vec-2", Text: "备用方案", Score: 0.87},
		},
	}
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "warn", Format: "text", DebugPayloads: true})

	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "部署方案", Details: "向量召回仍然命中。", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "备用方案", Details: "用于触发 rerank 降级。", VectorID: "vec-2"},
		},
		lexicalErr: errors.New("fts gateway unavailable"),
	}
	reranker := &stubRerankerClient{err: errors.New("rerank timeout")}
	query := "用户银行卡 1234 的部署方案"
	uc := NewMemoryUseCase(profiles, store, embedding, vector, logger)
	uc.ConfigureHybrid(true, 5, 60)
	uc.ConfigureRerank(reranker, 2)

	if _, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{query},
		TopK:      2,
	}); err != nil {
		t.Fatalf("search with payload-debug degradation logs: %v", err)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, query) || !strings.Contains(logs, `query："用户银行卡 1234 的部署方案"`) {
		t.Fatalf("expected payload-debug logs to include raw query text, got %s", logs)
	}
	if strings.Contains(logs, "query_len") || strings.Contains(logs, "query_sha256") {
		t.Fatalf("expected payload-debug logs to bypass redacted query diagnostics, got %s", logs)
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
		Queries:   []string{"当前仍然是 local_oss。 phase4 当前方案"},
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
	if len(result.Results[0].Hits[0].MatchedContextValues) != 0 || len(result.Results[0].Hits[1].MatchedContextValues) != 0 {
		t.Fatalf("expected unmatched context edges to stay hidden from hits, got %+v", result.Results[0].Hits)
	}
}

// TestAppendUniqueMemoryContextEvidenceValueDeduplicatesEquivalentLabels verifies query-time explanations collapse legacy context-value formatting variants so one semantic context does not appear twice in the returned evidence list.
// TestAppendUniqueMemoryContextEvidenceValueDeduplicatesEquivalentLabels 用于验证查询期解释会折叠历史 context value 的格式变体，避免同一个语义情境在返回证据里出现两次。
func TestAppendUniqueMemoryContextEvidenceValueDeduplicatesEquivalentLabels(t *testing.T) {
	values := appendUniqueMemoryContextEvidenceValue(nil, "deployment_mode", "LOCAL_OSS")
	values = appendUniqueMemoryContextEvidenceValue(values, "deployment mode", "local oss")
	values = appendUniqueMemoryContextEvidenceValue(values, "deployment-mode", " local-oss ")
	if len(values) != 1 {
		t.Fatalf("expected equivalent context labels to collapse into one canonical explanation, got %+v", values)
	}
	if values[0] != "deployment_mode=local oss" {
		t.Fatalf("expected canonical explanation label, got %+v", values)
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

// TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory verifies reviewer-side semantic dedupe reuses the recalled durable memory instead of inserting a second equivalent direct-write row.
// TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory 用于验证 reviewer 触发的语义去重会复用已召回的长期记忆，而不是再插入第二条等价主动写入行。
func TestMemoryUseCaseWriteSemanticDedupeReturnsExistingMemory(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段已经切换到 B。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				DroppedCandidates:        []logicdomain.PostActionDroppedMemoryCandidate{{CandidateIndex: 0, DedupeMemoryID: 901}},
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "旧记忆已经完整表达这条阶段事实。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-direct-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 901 {
		t.Fatalf("expected semantic dedupe to reuse memory 901, got %+v", result.Items)
	}
	if len(vector.upserts) != 0 {
		t.Fatalf("expected semantic dedupe to skip new vector upsert, got %+v", vector.upserts)
	}
	if len(store.directWriteApplyCalls) != 0 || len(store.createdDirectMemoryNodes) != 0 {
		t.Fatalf("expected semantic dedupe to avoid persistence writes, got apply=%+v create=%+v", store.directWriteApplyCalls, store.createdDirectMemoryNodes)
	}
}

// TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates verifies one direct-write RPC collapses repeated identical items before embedding and persistence so the caller does not create duplicate durable memories inside the same batch.
// TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates 用于验证单次主动写入 RPC 会先折叠请求内重复的相同条目，避免同一批请求里生成重复长期记忆。
func TestMemoryUseCaseWriteDeduplicatesSameRequestDuplicates(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1401,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  47,
			SessionKey: "sess-direct-in-request-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
			},
			{
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录新的稳定工程约束。",
				Details:    "记录新的稳定工程约束。",
				Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("result items len = %d, want 2", len(result.Items))
	}
	if result.Items[0].Ref.ID != 1401 || result.Items[0].Deduped {
		t.Fatalf("expected first item to be the canonical create result, got %+v", result.Items[0])
	}
	if result.Items[1].Ref.ID != 1401 || !result.Items[1].Deduped {
		t.Fatalf("expected second item to reuse the same created ref as in-request dedupe, got %+v", result.Items[1])
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one atomic persistence call, got %+v", store.directWriteApplyCalls)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one vector upsert, got %+v", vector.upserts)
	}
	if len(embedding.requests) != 1 {
		t.Fatalf("expected one embedding request, got %+v", embedding.requests)
	}
}

// TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes verifies in-request duplicate collapse only applies to truly identical writes and keeps separate persistence paths when the caller changes category or lifecycle semantics.
// TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes 用于验证单请求内的重复折叠只作用于真正相同的写入；当调用方改变 category 或生命周期语义时，仍应保留独立持久化路径。
func TestMemoryUseCaseWriteDoesNotCollapseDifferentSemanticAttributes(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  48,
			SessionKey: "sess-direct-same-text-different-semantics",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{
			{
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "记录新的实施约束。",
				Details:     "记录新的实施约束。",
				Category:    logicdomain.MemoryNodeCategoryTechSpecAPI,
				Priority:    logicdomain.MemoryPriorityP2,
				MemoryLevel: logicdomain.MemoryLevelStable,
			},
			{
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "记录新的实施约束。",
				Details:     "记录新的实施约束。",
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelPersistent,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 2 || result.Items[0].Ref.ID == 0 || result.Items[1].Ref.ID == 0 {
		t.Fatalf("unexpected write results: %+v", result.Items)
	}
	if result.Items[0].Ref.ID == result.Items[1].Ref.ID {
		t.Fatalf("expected distinct refs for different semantic attributes, got %+v", result.Items)
	}
	if result.Items[0].Deduped || result.Items[1].Deduped {
		t.Fatalf("expected both writes to persist independently, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 2 {
		t.Fatalf("expected two atomic persistence calls, got %+v", store.directWriteApplyCalls)
	}
	if len(vector.upserts) != 2 {
		t.Fatalf("expected two vector upserts, got %+v", vector.upserts)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 2 {
		t.Fatalf("expected one embedding batch covering both writes, got %+v", embedding.requests)
	}
}

// TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash verifies cross-request soft idempotency still reuses the durable row when the new semantic hash matches exactly.
// TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash 用于验证当新的语义哈希完全一致时，跨请求软幂等仍会正确复用长期记忆行。
func TestMemoryUseCaseWriteSoftIdempotencyReusesCurrentSemanticHash(t *testing.T) {
	session := logicdomain.SessionRef{
		SessionID:  49,
		SessionKey: "sess-direct-current-soft-dedupe",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	item := WriteMemoryItem{
		ScopeLevel:  logicdomain.MemoryScopeLevelProject,
		Abstract:    "记录当前稳定的部署约束。",
		Details:     "记录当前稳定的部署约束。",
		Category:    logicdomain.MemoryNodeCategoryProjectContext,
		Priority:    logicdomain.MemoryPriorityP1,
		MemoryLevel: logicdomain.MemoryLevelStable,
		ExpiresAt:   time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
	}
	currentHash := buildDirectMemoryDedupeHash(session, item, time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC))

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		recentDedupeRowsByHash: map[string]logicdomain.MemoryNodeRecord{
			currentHash: {
				ID:          1501,
				Status:      logicdomain.MemoryStatusActive,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
				Abstract:    "记录当前稳定的部署约束。",
				Details:     "记录当前稳定的部署约束。",
				ExpiresAt:   item.ExpiresAt,
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: session,
		Items:   []WriteMemoryItem{item},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 1501 {
		t.Fatalf("expected current semantic hash to reuse memory 1501, got %+v", result.Items)
	}
	if len(store.recentDedupeQueries) != 1 || store.recentDedupeQueries[0] != currentHash {
		t.Fatalf("expected one current-hash lookup, got %+v", store.recentDedupeQueries)
	}
	if len(vector.upserts) != 0 || len(store.directWriteApplyCalls) != 0 || len(embedding.requests) != 0 {
		t.Fatalf("expected soft idempotency to skip new writes, got upserts=%+v apply=%+v embeds=%+v", vector.upserts, store.directWriteApplyCalls, embedding.requests)
	}
}

// TestMemoryUseCaseWriteSoftIdempotencySupportsLegacyHashMigration verifies rows written before the semantic-hash hardening still participate in the short migration window when their lifecycle semantics truly match.
// TestMemoryUseCaseWriteSoftIdempotencySupportsLegacyHashMigration 用于验证在语义哈希加固前写入的历史行，只要生命周期语义确实一致，仍能在短迁移窗口内继续参与软幂等。
func TestMemoryUseCaseWriteSoftIdempotencySupportsLegacyHashMigration(t *testing.T) {
	fixedNow := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	session := logicdomain.SessionRef{
		SessionID:  50,
		SessionKey: "sess-direct-legacy-soft-dedupe",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	item := normalizeWriteMemoryItem(WriteMemoryItem{
		ScopeLevel:  logicdomain.MemoryScopeLevelProject,
		Abstract:    "记录默认生命周期下的部署约束。",
		Details:     "记录默认生命周期下的部署约束。",
		Category:    logicdomain.MemoryNodeCategoryProjectContext,
		Priority:    logicdomain.MemoryPriorityP2,
		MemoryLevel: logicdomain.MemoryLevelStable,
	}, fixedNow)
	currentHash := buildDirectMemoryDedupeHash(session, item, fixedNow)
	legacyHash := buildLegacyDirectMemoryDedupeHash(session, item)

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		recentDedupeRowsByHash: map[string]logicdomain.MemoryNodeRecord{
			legacyHash: {
				ID:          1601,
				Status:      logicdomain.MemoryStatusActive,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP2,
				MemoryLevel: logicdomain.MemoryLevelStable,
				Abstract:    item.Abstract,
				Details:     item.Details,
				CreatedAt:   fixedNow,
				ExpiresAt:   fixedNow.Add(defaultMemoryTTL(logicdomain.MemoryScopeLevelProject)),
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: session,
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    item.Abstract,
			Details:     item.Details,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP2,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || !result.Items[0].Deduped || result.Items[0].Ref.ID != 1601 {
		t.Fatalf("expected legacy hash migration path to reuse memory 1601, got %+v", result.Items)
	}
	if len(store.recentDedupeQueries) != 2 || store.recentDedupeQueries[0] != currentHash || store.recentDedupeQueries[1] != legacyHash {
		t.Fatalf("expected current-hash lookup followed by legacy fallback, got %+v", store.recentDedupeQueries)
	}
	if len(vector.upserts) != 0 || len(store.directWriteApplyCalls) != 0 || len(embedding.requests) != 0 {
		t.Fatalf("expected legacy soft idempotency to skip new writes, got upserts=%+v apply=%+v embeds=%+v", vector.upserts, store.directWriteApplyCalls, embedding.requests)
	}
}

// TestMemoryUseCaseWriteSoftIdempotencyRejectsLegacyHashSemanticMismatch verifies the compatibility fallback never reuses one old coarse-hash row when the incoming direct write changes category or lifecycle semantics.
// TestMemoryUseCaseWriteSoftIdempotencyRejectsLegacyHashSemanticMismatch 用于验证兼容回退不会因为旧版粗粒度哈希相同，就误复用已经改变 category 或生命周期语义的主动写入。
func TestMemoryUseCaseWriteSoftIdempotencyRejectsLegacyHashSemanticMismatch(t *testing.T) {
	fixedNow := time.Date(2026, 4, 5, 11, 0, 0, 0, time.UTC)
	session := logicdomain.SessionRef{
		SessionID:  51,
		SessionKey: "sess-direct-legacy-soft-dedupe-mismatch",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}
	item := normalizeWriteMemoryItem(WriteMemoryItem{
		ScopeLevel:  logicdomain.MemoryScopeLevelProject,
		Abstract:    "记录默认生命周期下的部署约束。",
		Details:     "记录默认生命周期下的部署约束。",
		Category:    logicdomain.MemoryNodeCategoryProjectContext,
		Priority:    logicdomain.MemoryPriorityP2,
		MemoryLevel: logicdomain.MemoryLevelStable,
	}, fixedNow)
	currentHash := buildDirectMemoryDedupeHash(session, item, fixedNow)
	legacyHash := buildLegacyDirectMemoryDedupeHash(session, item)

	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		recentDedupeRowsByHash: map[string]logicdomain.MemoryNodeRecord{
			legacyHash: {
				ID:          1701,
				Status:      logicdomain.MemoryStatusActive,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Category:    logicdomain.MemoryNodeCategoryTechSpecAPI,
				Priority:    logicdomain.MemoryPriorityP2,
				MemoryLevel: logicdomain.MemoryLevelStable,
				Abstract:    item.Abstract,
				Details:     item.Details,
				CreatedAt:   fixedNow,
				ExpiresAt:   fixedNow.Add(defaultMemoryTTL(logicdomain.MemoryScopeLevelProject)),
			},
		},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1702,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   item.Abstract,
				Details:    item.Details,
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: session,
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    item.Abstract,
			Details:     item.Details,
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP2,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1702 {
		t.Fatalf("expected semantic mismatch to create a fresh row, got %+v", result.Items)
	}
	if len(store.recentDedupeQueries) != 2 || store.recentDedupeQueries[0] != currentHash || store.recentDedupeQueries[1] != legacyHash {
		t.Fatalf("expected current-hash lookup followed by legacy mismatch check, got %+v", store.recentDedupeQueries)
	}
	if len(store.directWriteApplyCalls) != 1 || len(vector.upserts) != 1 || len(embedding.requests) != 1 {
		t.Fatalf("expected semantic mismatch to persist a fresh row, got apply=%+v upserts=%+v embeds=%+v", store.directWriteApplyCalls, vector.upserts, embedding.requests)
	}
}

// TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate verifies dropped candidates with similar memories no longer auto-reuse the first hit unless reviewer explicitly names the dedupe target.
// TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate 用于验证当 reviewer 只丢弃候选但没有显式给出 dedupe 目标时，即使存在 similar memory，也不会再自动复用第一条旧记忆。
func TestMemoryUseCaseWriteDroppedCandidateWithoutExplicitDedupeTargetFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1201,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段已经切换到 B。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				DroppedCandidates:        []logicdomain.PostActionDroppedMemoryCandidate{{CandidateIndex: 0}},
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "保守丢弃，但没有明确指定可复用旧记忆。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  44,
			SessionKey: "sess-direct-drop-without-explicit-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1201 {
		t.Fatalf("expected explicit-dedupe-missing path to create a new row, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected fallback create without supersedes, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory verifies direct-write semantic replacement passes reviewer-approved supersede ids into the atomic store path and cleans up obsolete vectors after commit.
// TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory 用于验证主动写入的语义替代会把 reviewer 批准的 supersede id 传入原子存储路径，并在提交后清理旧向量。
func TestMemoryUseCaseWriteAcceptedCandidateSupersedesOldMemory(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段仍然是 A。",
			Details:    "当前项目阶段仍然是 A。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1001,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
			SupersededVectorIDs: []string{"vec-901"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段仍然是 A。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
					CandidateIndex:     0,
					SupersedeMemoryIDs: []uint64{901},
				}},
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "新阶段事实覆盖旧阶段事实。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  42,
			SessionKey: "sess-direct-replace",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    "当前项目阶段已经切换到 B。",
			Details:     "当前项目阶段已经切换到 B。",
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1001 {
		t.Fatalf("expected fresh memory write result, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 1 || store.directWriteApplyCalls[0].SupersededMemoryIDs[0] != 901 {
		t.Fatalf("expected atomic direct-write apply to receive supersede id 901, got %+v", store.directWriteApplyCalls)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one new vector upsert, got %+v", vector.upserts)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != "vec-901" {
		t.Fatalf("expected obsolete vector cleanup for vec-901, got %+v", vector.deleteIDsCalls)
	}
}

// TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist verifies direct-write review still honors legacy index-only accepted payloads returned by older or non-LLM reviewer implementations.
// TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist 用于验证当 reviewer 仍返回旧版 index-only accepted 结果时，主动写入链路依然会正确保留候选而不是误判成 dropped。
func TestMemoryUseCaseWriteLegacyAcceptedIndexesStillPersist(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1251,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录一条新的稳定工程约束。",
				Details:    "记录一条新的稳定工程约束。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "旧实现仍只返回 accepted_candidate_indexes。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  45,
			SessionKey: "sess-direct-legacy-accepted",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一条新的稳定工程约束。",
			Details:    "记录一条新的稳定工程约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1251 {
		t.Fatalf("expected legacy accepted indexes to still create a new row, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one persistence call, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate verifies reviewer drops without any trustworthy similar memories degrade to creating a fresh row instead of losing an explicit tool write.
// TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate 用于验证当 reviewer 丢弃候选但没有可信相似旧记忆时，会退化成新建，而不是丢失显式工具写入。
func TestMemoryUseCaseWriteDroppedCandidateWithoutSimilarFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1101,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "记录一次新的稳定技术约束。",
				Details:    "记录一次新的稳定技术约束。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "这条候选没有高相似旧记忆，但 reviewer 仍然保守拒绝。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  43,
			SessionKey: "sess-direct-fallback-create",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录一次新的稳定技术约束。",
			Details:    "记录一次新的稳定技术约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1101 {
		t.Fatalf("expected fallback create result, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected fallback create to persist without supersedes, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate verifies a dedupe target that became superseded or expired after reviewer selection degrades into a fresh insert instead of returning stale memory refs.
// TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate 用于验证当 dedupe 目标在 reviewer 选中后变成 superseded 或过期时，会退化成新建，而不是返回陈旧记忆引用。
func TestMemoryUseCaseWriteStaleSemanticDedupeTargetFallsBackToCreate(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		memoryRowsByID: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			Status:     logicdomain.MemoryStatusSuperseded,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{{
			ID:         901,
			Status:     logicdomain.MemoryStatusActive,
			SourceKind: logicdomain.MemorySourceKindTurnExtract,
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			VectorID:   "vec-901",
		}},
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:         1301,
				SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel: logicdomain.MemoryScopeLevelProject,
				Abstract:   "当前项目阶段已经切换到 B。",
				Details:    "当前项目阶段已经切换到 B。",
				VectorID:   "vec-new",
			},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{{
			ID:    "vec-901",
			Text:  "当前项目阶段已经切换到 B。",
			Score: 0.96,
		}},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				DroppedCandidates:        []logicdomain.PostActionDroppedMemoryCandidate{{CandidateIndex: 0, DedupeMemoryID: 901}},
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "理论上可复用旧记忆，但目标在并发窗口里已经退役。",
			},
		},
	}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigureMemoryReplace(reviewer, 5, memoryReplaceScopeProject, 0.90)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  46,
			SessionKey: "sess-direct-stale-dedupe",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "当前项目阶段已经切换到 B。",
			Details:    "当前项目阶段已经切换到 B。",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Deduped || result.Items[0].Ref.ID != 1301 {
		t.Fatalf("expected stale dedupe target to fall back to create, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 || len(store.directWriteApplyCalls[0].SupersededMemoryIDs) != 0 {
		t.Fatalf("expected stale dedupe target to create without supersedes, got %+v", store.directWriteApplyCalls)
	}
}

// stubTurnLookupStore supplies deterministic turn rows for memory-query tests.
// stubTurnLookupStore 用于为记忆查询测试提供确定性的 turn 行。
type stubTurnLookupStore struct {
	turnIDs                  []uint64
	rows                     []logicdomain.SessionTurnRecord
	windows                  map[uint64]logicdomain.TurnDetailWindow
	memoryRowsByID           []logicdomain.MemoryNodeRecord
	memoryRowsByIDErr        error
	memoryContextEdges       []logicdomain.MemoryContextEdge
	memoryRowsByVector       []logicdomain.MemoryNodeRecord
	lexicalHits              []logicdomain.MemoryLexicalHit
	lexicalErr               error
	lexicalQueries           []string
	lexicalTopKs             []int
	lexicalFilters           []logicdomain.SearchFilter
	contextLookupIDs         []uint64
	recentDedupeQueries      []string
	recentDedupeRowsByHash   map[string]logicdomain.MemoryNodeRecord
	recentDedupeRow          logicdomain.MemoryNodeRecord
	recentDedupeHit          bool
	createdDirectMemoryNodes []logicdomain.MemoryNodeRecord
	createDirectMemoryErr    error
	directWriteApplyCalls    []stubDirectMemoryWriteApplyCall
	directWriteApplyResult   logicdomain.DirectMemoryWriteApplyResult
	directWriteApplyErr      error
	err                      error
}

// stubDirectMemoryWriteApplyCall records one atomic direct-memory write attempt so tests can assert the store sees the new row plus the intended supersede targets together.
// stubDirectMemoryWriteApplyCall 用于记录一次原子主动写记忆尝试，让测试可以断言存储层同时看到了新行和预期 supersede 目标。
type stubDirectMemoryWriteApplyCall struct {
	Session             logicdomain.SessionRef
	Record              logicdomain.MemoryNodeRecord
	SupersededMemoryIDs []uint64
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
	if s.memoryRowsByIDErr != nil {
		return nil, s.memoryRowsByIDErr
	}
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
	if s.lexicalErr != nil {
		return nil, s.lexicalErr
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]logicdomain.MemoryLexicalHit(nil), s.lexicalHits...), nil
}

// FindRecentActiveMemoryByDedupe records short-window dedupe lookups and can return either hash-specific rows or the legacy canned row for older tests.
// FindRecentActiveMemoryByDedupe 用于记录短窗口幂等查询，并支持按哈希返回特定行；旧测试若未配置哈希映射，则继续走兼容的单行预设返回。
func (s *stubTurnLookupStore) FindRecentActiveMemoryByDedupe(_ context.Context, _ logicdomain.SessionRef, _, _ int, dedupeHash string, _ time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	s.recentDedupeQueries = append(s.recentDedupeQueries, dedupeHash)
	if s.err != nil {
		return logicdomain.MemoryNodeRecord{}, false, s.err
	}
	if len(s.recentDedupeRowsByHash) > 0 {
		row, ok := s.recentDedupeRowsByHash[dedupeHash]
		if !ok {
			return logicdomain.MemoryNodeRecord{}, false, nil
		}
		return row, true, nil
	}
	if !s.recentDedupeHit {
		return logicdomain.MemoryNodeRecord{}, false, nil
	}
	return s.recentDedupeRow, true, nil
}

// CreateDirectMemoryNode keeps the stub interface-complete for tests that do not exercise direct-write persistence.
// CreateDirectMemoryNode 用于补齐测试桩接口，因为当前这些测试不覆盖主动写入持久化。
func (s *stubTurnLookupStore) CreateDirectMemoryNode(_ context.Context, _ logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if s.createDirectMemoryErr != nil {
		return logicdomain.MemoryNodeRecord{}, s.createDirectMemoryErr
	}
	if s.err != nil {
		return logicdomain.MemoryNodeRecord{}, s.err
	}
	if record.ID == 0 {
		record.ID = uint64(len(s.createdDirectMemoryNodes) + 1)
	}
	s.createdDirectMemoryNodes = append(s.createdDirectMemoryNodes, record)
	return record, nil
}

// ApplyDirectMemoryWrite records one atomic direct-write apply call and returns the canned result so write-flow tests can verify supersede ids and vector cleanup behavior.
// ApplyDirectMemoryWrite 用于记录一次原子主动写入调用，并返回预设结果，方便主动写流程测试校验 supersede id 和向量清理行为。
func (s *stubTurnLookupStore) ApplyDirectMemoryWrite(_ context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	call := stubDirectMemoryWriteApplyCall{
		Session:             session,
		Record:              record,
		SupersededMemoryIDs: append([]uint64(nil), supersededMemoryIDs...),
	}
	s.directWriteApplyCalls = append(s.directWriteApplyCalls, call)
	if s.directWriteApplyErr != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, s.directWriteApplyErr
	}
	if s.directWriteApplyResult.InsertedMemoryNode.ID == 0 {
		record.ID = uint64(len(s.directWriteApplyCalls))
		return logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode:  record,
			SupersededVectorIDs: append([]string(nil), s.directWriteApplyResult.SupersededVectorIDs...),
		}, nil
	}
	result := s.directWriteApplyResult
	result.InsertedMemoryNode.Vector = append([]float32(nil), result.InsertedMemoryNode.Vector...)
	result.SupersededVectorIDs = append([]string(nil), result.SupersededVectorIDs...)
	return result, nil
}

// stubCombinedHybridVectorStore extends the shared vector stub with the optional SQL-level hybrid recall fast path used by the PostgreSQL combined-store optimization.
// stubCombinedHybridVectorStore 用于在共享向量桩之上扩展可选的 SQL 级混合召回快速路径，服务 PostgreSQL 组合库优化断言。
type stubCombinedHybridVectorStore struct {
	stubVectorStore
	hybridQueries    []string
	hybridTopKs      []int
	hybridFilters    []logicdomain.SearchFilter
	hybridRRFKs      []int
	hybridSearchHits []logicdomain.MemoryHit
	hybridSearchErr  error
}

// SearchHybridMemory records the combined first-stage request and returns the canned SQL-fused hits.
// SearchHybridMemory 用于记录组合库一阶段请求，并返回预设的 SQL 融合命中结果。
func (s *stubCombinedHybridVectorStore) SearchHybridMemory(_ context.Context, query string, _ []float32, topK int, filter logicdomain.SearchFilter, rrfK int) ([]logicdomain.MemoryHit, error) {
	s.hybridQueries = append(s.hybridQueries, query)
	s.hybridTopKs = append(s.hybridTopKs, topK)
	s.hybridFilters = append(s.hybridFilters, filter)
	s.hybridRRFKs = append(s.hybridRRFKs, rrfK)
	if s.hybridSearchErr != nil {
		return nil, s.hybridSearchErr
	}
	return append([]logicdomain.MemoryHit(nil), s.hybridSearchHits...), nil
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
