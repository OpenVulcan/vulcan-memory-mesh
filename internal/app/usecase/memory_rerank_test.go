// Memory use-case tests for memory rerank test.
// 记忆应用用例层重排测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"math"
	"testing"
)

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

// TestMemoryUseCaseSearchSkipsRerankWhenLessThanTwoProviderDocuments verifies rerank is not called when text normalization leaves fewer than two comparable provider documents.
// TestMemoryUseCaseSearchSkipsRerankWhenLessThanTwoProviderDocuments 用于验证当文本归一化后少于两个可比较 provider 文档时，不会调用 rerank。
func TestMemoryUseCaseSearchSkipsRerankWhenLessThanTwoProviderDocuments(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第一条", Details: "第一条详情", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, VectorID: "vec-2"},
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
		results: []appports.RerankerResult{{ID: "202", Score: 0.99}},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureRerank(reranker, 2)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"文本排序模型"},
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("search memory events with one rerank document: %v", err)
	}
	if len(reranker.requests) != 0 {
		t.Fatalf("expected rerank to be skipped with one provider document, got %+v", reranker.requests)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 201 || result.Results[0].Hits[0].Score != 0.91 {
		t.Fatalf("expected original first hit to stay unchanged, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 202 || result.Results[0].Hits[1].Score != 0.89 {
		t.Fatalf("expected original second hit to stay unchanged, got %+v", result.Results[0].Hits[1])
	}
}

// TestMemoryUseCaseSearchPassesProviderDocumentCountToRerank verifies filtered-out empty candidates do not inflate the topN sent to the rerank provider.
// TestMemoryUseCaseSearchPassesProviderDocumentCountToRerank 用于验证被过滤的空候选不会抬高传给 rerank provider 的 topN。
func TestMemoryUseCaseSearchPassesProviderDocumentCountToRerank(t *testing.T) {
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
			{ID: 203, OriginSessionID: 12, SourceTurnID: 43, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, VectorID: "vec-3"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "第一条", Score: 0.93},
			{ID: "vec-2", Text: "第二条", Score: 0.92},
			{ID: "vec-3", Text: "第三条", Score: 0.91},
		},
	}
	reranker := &stubRerankerClient{
		results: []appports.RerankerResult{
			{ID: "202", Score: 0.99},
			{ID: "201", Score: 0.88},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureRerank(reranker, 3)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"文本排序模型"},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("search memory events with filtered rerank documents: %v", err)
	}
	if len(reranker.requests) != 1 {
		t.Fatalf("expected one rerank request, got %+v", reranker.requests)
	}
	request := reranker.requests[0]
	if request.topN != 2 || len(request.docs) != 2 {
		t.Fatalf("expected rerank request to use two provider documents, got %+v", request)
	}
	if request.docs[0].ID != "201" || request.docs[1].ID != "202" {
		t.Fatalf("expected empty third candidate to be excluded from provider docs, got %+v", request.docs)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 3 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[1].MemoryRef.ID != 201 || result.Results[0].Hits[2].MemoryRef.ID != 203 {
		t.Fatalf("expected reranked docs first and untouched empty-doc candidate last, got %+v", result.Results[0].Hits)
	}
}

// TestMemoryUseCaseSearchClampsRerankScores verifies the final retrieval response keeps the shared 0..1 score contract even when a rerank provider returns out-of-range custom scores.
// TestMemoryUseCaseSearchClampsRerankScores 用于验证即使 rerank provider 返回越界自定义分数，最终检索响应仍会保持统一的 0..1 分数契约。
func TestMemoryUseCaseSearchClampsRerankScores(t *testing.T) {
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
	reranker := &stubRerankerClient{
		results: []appports.RerankerResult{
			{ID: "202", Score: 1.70},
			{ID: "201", Score: -0.20},
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
		t.Fatalf("search memory events with rerank clamp: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[0].Score != 1 {
		t.Fatalf("expected first reranked hit score to clamp to 1, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 201 || result.Results[0].Hits[1].Score != 0 {
		t.Fatalf("expected second reranked hit score to clamp to 0, got %+v", result.Results[0].Hits[1])
	}
}

// TestMemoryUseCaseSearchClampsNonFiniteRerankScores verifies non-finite rerank provider scores are collapsed into safe finite 0..1 values before they reach final search results.
// TestMemoryUseCaseSearchClampsNonFiniteRerankScores 用于验证 rerank provider 返回的非有限分数会在进入最终检索结果前被压平成安全的有限 0..1 值。
func TestMemoryUseCaseSearchClampsNonFiniteRerankScores(t *testing.T) {
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
	reranker := &stubRerankerClient{
		results: []appports.RerankerResult{
			{ID: "202", Score: math.Inf(1)},
			{ID: "201", Score: math.NaN()},
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
		t.Fatalf("search memory events with non-finite rerank scores: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[0].Score != 1 {
		t.Fatalf("expected positive infinity rerank score to clamp to 1, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 201 || result.Results[0].Hits[1].Score != 0 {
		t.Fatalf("expected NaN rerank score to clamp to 0, got %+v", result.Results[0].Hits[1])
	}
	if math.IsNaN(result.Results[0].Hits[0].Score) || math.IsNaN(result.Results[0].Hits[1].Score) {
		t.Fatalf("expected final scores to stay finite, got %+v", result.Results[0].Hits)
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
