// Memory use-case tests for memory query test.
// 记忆应用用例层分组查询与轮次读取测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
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
