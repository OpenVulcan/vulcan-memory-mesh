// Memory use-case tests for memory scoring test.
// 记忆应用用例层衰减与情境评分测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"testing"
	"time"
)

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

// TestMemoryUseCaseSearchAppliesContextEvidenceToSingleHit verifies context evidence still enriches reviewer-facing fields when only one memory survives recall.
// TestMemoryUseCaseSearchAppliesContextEvidenceToSingleHit 用于验证只有一条记忆穿过召回时，context evidence 仍会补充面向 reviewer 的字段。
func TestMemoryUseCaseSearchAppliesContextEvidenceToSingleHit(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "本地 OSS 部署方案", Details: "这条记忆被当前场景支持", VectorID: "vec-1"},
		},
		memoryContextEdges: []logicdomain.MemoryContextEdge{
			{MemoryID: 201, ContextKey: "deployment_mode", ContextValue: "local oss", SupportCount: 2},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "本地 OSS 部署方案", Score: 0.91},
		},
	}

	uc := NewMemoryUseCase(profiles, turns, embedding, vector, nil)
	result, err := uc.Search(context.Background(), MemoryQueryCommand{
		UserID:    7,
		ProjectID: 9,
		Queries:   []string{"当前部署模式是 local_oss"},
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("search memory events with single context-aware hit: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 1 {
		t.Fatalf("unexpected single-hit result: %+v", result.Results)
	}
	hit := result.Results[0].Hits[0]
	if hit.MatchedContextSupportCount != 2 || hit.MatchedContextRebuttalCount != 0 || hit.MatchedContextScoreDelta <= 0 {
		t.Fatalf("expected single hit to carry supportive context evidence, got %+v", hit)
	}
	if len(hit.MatchedContextValues) != 1 || hit.MatchedContextValues[0] != "deployment_mode=local oss" {
		t.Fatalf("expected single hit to expose matched context value, got %+v", hit.MatchedContextValues)
	}
	if len(turns.contextLookupIDs) != 1 || turns.contextLookupIDs[0] != 201 {
		t.Fatalf("expected context lookup for the single candidate, got %+v", turns.contextLookupIDs)
	}
}

// TestMemoryQueryContextSignalsMatchLongBoundedPhrase verifies long context values can match inside a query without allowing substring-only false positives.
// TestMemoryQueryContextSignalsMatchLongBoundedPhrase 用于验证较长 context value 可以在 query 内命中，同时不会允许仅子串相同的误命中。
func TestMemoryQueryContextSignalsMatchLongBoundedPhrase(t *testing.T) {
	signals := buildMemoryQueryContextSignals(MemoryQueryItem{
		Query: "当前 local oss air gapped phase four 部署仍然有效",
	})
	if !signals.Match("local oss air gapped phase four") {
		t.Fatalf("expected long bounded context phrase to match")
	}

	substringSignals := buildMemoryQueryContextSignals(MemoryQueryItem{Query: "boss mode"})
	if substringSignals.Match("oss") {
		t.Fatalf("did not expect substring-only context phrase to match")
	}
}

// TestMemoryUseCaseSearchReusesContextEdgesAcrossQueryGroups verifies one request does not reload the same raw context edges when different query groups share the same candidate memories.
// TestMemoryUseCaseSearchReusesContextEdgesAcrossQueryGroups 用于验证单次请求中不同 query 分组共享同一批候选记忆时，不会重复加载相同的原始 context edges。
func TestMemoryUseCaseSearchReusesContextEdgesAcrossQueryGroups(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 旧方案", Details: "这条记忆被本地 OSS 场景反驳", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 新方案", Details: "这条记忆被本地 OSS 场景支持", VectorID: "vec-2"},
		},
		memoryContextEdges: []logicdomain.MemoryContextEdge{
			{MemoryID: 201, ContextKey: "deployment_mode", ContextValue: "local oss", RebuttalCount: 2},
			{MemoryID: 202, ContextKey: "deployment_mode", ContextValue: "local oss", SupportCount: 2},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}}},
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
		Queries: []string{
			"当前部署模式仍然是 local_oss。 phase4 当前方案",
			"local oss 部署排障清单仍然沿用 phase4 当前方案",
		},
		TopK: 2,
	})
	if err != nil {
		t.Fatalf("search memory events with shared context edge cache: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected two query groups, got %+v", result.Results)
	}
	for idx, group := range result.Results {
		if len(group.Hits) != 2 {
			t.Fatalf("expected two hits in query group %d, got %+v", idx, group.Hits)
		}
		if group.Hits[0].MemoryRef.ID != 202 || group.Hits[1].MemoryRef.ID != 201 {
			t.Fatalf("expected supportive context edge to win in query group %d, got %+v", idx, group.Hits)
		}
		if len(group.Hits[0].MatchedContextValues) != 1 || group.Hits[0].MatchedContextValues[0] != "deployment_mode=local oss" {
			t.Fatalf("expected cached context edge to still score query group %d, got %+v", idx, group.Hits[0])
		}
	}
	if len(vector.searchTopKs) != 2 {
		t.Fatalf("expected two vector searches for distinct query groups, got %+v", vector.searchTopKs)
	}
	if len(turns.contextLookupIDCalls) != 1 {
		t.Fatalf("expected one context edge lookup shared by both query groups, got %+v", turns.contextLookupIDCalls)
	}
	if len(turns.contextLookupIDCalls[0]) != 2 || turns.contextLookupIDCalls[0][0] != 201 || turns.contextLookupIDCalls[0][1] != 202 {
		t.Fatalf("expected the initial context edge lookup to cover both candidate ids, got %+v", turns.contextLookupIDCalls)
	}
}

// TestMemoryUseCaseSearchClampsContextAdjustedScores verifies supportive or rebutting context evidence cannot push the final caller-facing hit score outside the shared 0..1 range.
// TestMemoryUseCaseSearchClampsContextAdjustedScores 用于验证支持或反驳型 context evidence 都不会把最终对外命中分数推到统一 0..1 区间之外。
func TestMemoryUseCaseSearchClampsContextAdjustedScores(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	turns := &stubTurnLookupStore{
		memoryRowsByVector: []logicdomain.MemoryNodeRecord{
			{ID: 201, OriginSessionID: 12, SourceTurnID: 41, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 旧方案", Details: "这条记忆被当前场景强烈反驳", VectorID: "vec-1"},
			{ID: 202, OriginSessionID: 12, SourceTurnID: 42, SourceKind: logicdomain.MemorySourceKindTurnExtract, ScopeLevel: logicdomain.MemoryScopeLevelProject, Category: 3, Abstract: "phase4 新方案", Details: "这条记忆被当前场景强烈支持", VectorID: "vec-2"},
		},
		memoryContextEdges: []logicdomain.MemoryContextEdge{
			{MemoryID: 201, ContextKey: "deployment_mode", ContextValue: "local oss", RebuttalCount: 99},
			{MemoryID: 202, ContextKey: "deployment_mode", ContextValue: "local oss", SupportCount: 99},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{
		searchHits: []logicdomain.MemoryHit{
			{ID: "vec-1", Text: "phase4 旧方案", Score: 0.05},
			{ID: "vec-2", Text: "phase4 新方案", Score: 0.97},
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
		t.Fatalf("search memory events with context score clamp: %v", err)
	}
	if len(result.Results) != 1 || len(result.Results[0].Hits) != 2 {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
	if result.Results[0].Hits[0].MemoryRef.ID != 202 || result.Results[0].Hits[0].Score != 1 {
		t.Fatalf("expected supportive context score to clamp at 1, got %+v", result.Results[0].Hits[0])
	}
	if result.Results[0].Hits[1].MemoryRef.ID != 201 || result.Results[0].Hits[1].Score != 0 {
		t.Fatalf("expected rebutted context score to clamp at 0, got %+v", result.Results[0].Hits[1])
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
