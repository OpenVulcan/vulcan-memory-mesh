// Memory use-case tests for memory hybrid test.
// 记忆应用用例层混合检索测试，覆盖召回或写入链路。
package usecase

import (
	"bytes"
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"math"
	"strings"
	"testing"
)

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

// TestMemoryUseCaseSearchKeepsCallerFacingScoresStableWhenLexicalBM25MagnitudeChanges verifies lexical BM25 magnitude changes do not directly alter caller-facing scores as long as the lexical rank order stays the same.
// TestMemoryUseCaseSearchKeepsCallerFacingScoresStableWhenLexicalBM25MagnitudeChanges 用于验证只要 lexical 排名顺序不变，BM25 数值大小变化就不会直接改写对外分数。
func TestMemoryUseCaseSearchKeepsCallerFacingScoresStableWhenLexicalBM25MagnitudeChanges(t *testing.T) {
	buildUseCase := func(lexicalHits []logicdomain.MemoryLexicalHit) *MemoryUseCase {
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
				{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "候选一", Details: "第一条 lexical 候选。", VectorID: "vec-1", Status: logicdomain.MemoryStatusActive},
				{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "候选二", Details: "第二条 lexical 候选。", VectorID: "vec-2", Status: logicdomain.MemoryStatusActive},
			},
			lexicalHits: lexicalHits,
		}
		embedding := &stubEmbeddingClient{
			response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
		}
		vector := &stubVectorStore{}
		uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
		uc.ConfigureHybrid(true, 5, 60)
		return uc
	}

	// Keep the lexical order identical and only flip the BM25-like score magnitude so we can verify the exposed score contract is rank-based rather than raw-score based.
	// 保持 lexical 顺序完全一致，只翻转类似 BM25 的数值大小，用于验证对外分数契约依赖的是 rank 而不是原始分值。
	firstResult, err := buildUseCase([]logicdomain.MemoryLexicalHit{
		{MemoryID: 201, Score: 0.99},
		{MemoryID: 202, Score: 0.01},
	}).Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"部署偏好"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("first hybrid search returned error: %v", err)
	}
	secondResult, err := buildUseCase([]logicdomain.MemoryLexicalHit{
		{MemoryID: 201, Score: 0.01},
		{MemoryID: 202, Score: 0.99},
	}).Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"部署偏好"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("second hybrid search returned error: %v", err)
	}

	firstHits := firstResult.Results[0].Hits
	secondHits := secondResult.Results[0].Hits
	if len(firstHits) != 2 || len(secondHits) != 2 {
		t.Fatalf("expected two lexical-only hybrid hits, got first=%+v second=%+v", firstHits, secondHits)
	}
	if firstHits[0].MemoryRef.ID != 201 || firstHits[1].MemoryRef.ID != 202 {
		t.Fatalf("expected first lexical order to stay [201 202], got %+v", firstHits)
	}
	if secondHits[0].MemoryRef.ID != 201 || secondHits[1].MemoryRef.ID != 202 {
		t.Fatalf("expected second lexical order to stay [201 202], got %+v", secondHits)
	}
	if math.Abs(firstHits[0].Score-secondHits[0].Score) > 1e-9 || math.Abs(firstHits[1].Score-secondHits[1].Score) > 1e-9 {
		t.Fatalf("expected caller-facing scores to ignore lexical raw-score magnitude, got first=%+v second=%+v", firstHits, secondHits)
	}
	if math.Abs(firstHits[0].Score-1.0) > 1e-9 || math.Abs(firstHits[1].Score-0.75) > 1e-9 {
		t.Fatalf("expected lexical-only scores to follow rank normalization, got %+v", firstHits)
	}
}

// TestMemoryUseCaseSearchChangesCandidateOrderWhenLexicalRankChanges verifies lexical rank drift still propagates into the final candidate order, which is the real BM25/tokenizer migration risk that needs regression coverage.
// TestMemoryUseCaseSearchChangesCandidateOrderWhenLexicalRankChanges 用于验证 lexical 排名漂移仍会传导到最终候选顺序，这才是 BM25 或 tokenizer 切换后真正需要回归覆盖的风险点。
func TestMemoryUseCaseSearchChangesCandidateOrderWhenLexicalRankChanges(t *testing.T) {
	buildUseCase := func(lexicalHits []logicdomain.MemoryLexicalHit) *MemoryUseCase {
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
				{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "候选一", Details: "第一条 lexical 候选。", VectorID: "vec-1", Status: logicdomain.MemoryStatusActive},
				{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "候选二", Details: "第二条 lexical 候选。", VectorID: "vec-2", Status: logicdomain.MemoryStatusActive},
			},
			lexicalHits: lexicalHits,
		}
		embedding := &stubEmbeddingClient{
			response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
		}
		vector := &stubVectorStore{}
		uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
		uc.ConfigureHybrid(true, 5, 60)
		return uc
	}

	// Swap only the lexical rank order to prove the real downstream sensitivity is candidate ordering, not the raw BM25 number itself.
	// 只交换 lexical 排名顺序，用于证明当前下游真正敏感的是候选顺序，而不是 BM25 原始数值本身。
	firstResult, err := buildUseCase([]logicdomain.MemoryLexicalHit{
		{MemoryID: 201, Score: 0.42},
		{MemoryID: 202, Score: 0.41},
	}).Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"部署偏好"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("first lexical-order search returned error: %v", err)
	}
	secondResult, err := buildUseCase([]logicdomain.MemoryLexicalHit{
		{MemoryID: 202, Score: 0.42},
		{MemoryID: 201, Score: 0.41},
	}).Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"部署偏好"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("second lexical-order search returned error: %v", err)
	}

	firstHits := firstResult.Results[0].Hits
	secondHits := secondResult.Results[0].Hits
	if len(firstHits) != 2 || len(secondHits) != 2 {
		t.Fatalf("expected two lexical-only hybrid hits, got first=%+v second=%+v", firstHits, secondHits)
	}
	if firstHits[0].MemoryRef.ID != 201 || firstHits[1].MemoryRef.ID != 202 {
		t.Fatalf("expected first lexical order [201 202], got %+v", firstHits)
	}
	if secondHits[0].MemoryRef.ID != 202 || secondHits[1].MemoryRef.ID != 201 {
		t.Fatalf("expected second lexical order [202 201], got %+v", secondHits)
	}
	if math.Abs(firstHits[0].Score-1.0) > 1e-9 || math.Abs(firstHits[1].Score-0.75) > 1e-9 {
		t.Fatalf("expected first lexical scores to stay rank-normalized, got %+v", firstHits)
	}
	if math.Abs(secondHits[0].Score-1.0) > 1e-9 || math.Abs(secondHits[1].Score-0.75) > 1e-9 {
		t.Fatalf("expected second lexical scores to stay rank-normalized, got %+v", secondHits)
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
