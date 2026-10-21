// Memory use-case tests for memory mmr test.
// 记忆应用用例层检索多样性测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	"errors"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"testing"
)

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

// TestMemoryUseCaseSearchExpandsCandidatePoolForHardDedupeRequests verifies internal reviewer-side searches can widen the first-stage recall pool for hard dedupe without inflating the caller-visible final top-k.
// TestMemoryUseCaseSearchExpandsCandidatePoolForHardDedupeRequests 用于验证内部 reviewer 链路可以为 hard dedupe 放大首轮召回窗口，同时不放大调用方可见的最终 top-k。
func TestMemoryUseCaseSearchExpandsCandidatePoolForHardDedupeRequests(t *testing.T) {
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
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第一条", Details: "第一条详情", VectorID: "vec-1", Vector: []float32{1, 0}},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第二条", Details: "第二条详情", VectorID: "vec-2", Vector: []float32{0.9, 0.1}},
			{ID: 203, OriginSessionID: 12, SourceTurnID: 43, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "第三条", Details: "第三条详情", VectorID: "vec-3", Vector: []float32{0.8, 0.2}},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "第一条", Score: 0.95},
			{ID: "vec-2", Text: "第二条", Score: 0.94},
			{ID: "vec-3", Text: "第三条", Score: 0.93},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	uc.ConfigureHardDedupePoolTopK(6)

	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:               7,
		ProjectID:            9,
		Queries:              []string{"候选池放大"},
		TopK:                 2,
		EnableHardDedupePool: true,
	})
	if err != nil {
		t.Fatalf("search memory events with hard dedupe pool: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected hard-dedupe-pool search results: %+v", result.Results)
	}
	if len(vector.searchTopKs) != 1 || vector.searchTopKs[0] != 6 {
		t.Fatalf("expected hard dedupe requests to enlarge candidate pool to 6, got %+v", vector.searchTopKs)
	}
}

// TestApplyMMRSearchHitsDoesNotReloadMissingVectors verifies MMR consumes the vectors already carried by mapped hits instead of hiding a materialization gap with another memory-id lookup.
// TestApplyMMRSearchHitsDoesNotReloadMissingVectors 用于验证 MMR 只消费映射命中已经携带的向量，而不是通过再次按 memory id 回表掩盖物化缺口。
func TestApplyMMRSearchHitsDoesNotReloadMissingVectors(t *testing.T) {
	store := &stubTurnLookupStore{memoryRowsByIDErr: errors.New("unexpected mmr vector backfill")}
	uc := NewMemoryUseCase(nil, store, nil, nil, nil)
	uc.ConfigureMMR(true, 0.75)

	selected := uc.applyMMRSearchHits(2, []MemoryQueryHit{
		{
			MemoryRef: logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 301},
			Abstract:  "已经携带向量的候选",
			Score:     0.9,
			Origin:    "vector_search",
			Vector:    []float32{0.1, 0.2, 0.3},
		},
		{
			MemoryRef: logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 302},
			Abstract:  "缺少向量的候选",
			Score:     0.8,
			Origin:    "lexical_search",
		},
	})
	if len(selected) != 2 {
		t.Fatalf("unexpected mmr hit count: %+v", selected)
	}
	if len(store.memoryRowsByIDCalls) != 0 {
		t.Fatalf("expected mmr not to reload missing vectors, got %+v", store.memoryRowsByIDCalls)
	}
	if len(selected[1].Vector) != 0 || selected[0].Origin != "vector_mmr" || selected[1].Origin != "lexical_mmr" {
		t.Fatalf("unexpected mmr result without vector reload: %+v", selected)
	}
}
