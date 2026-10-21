// Behavioral tests grouped by precheck review test.
// 应用用例层 pre-check 评审与降级行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"strings"
	"testing"
	"time"
)

// TestPreCheckExecuteKeepsFallbackSummaryTitlesAlignedWithItems verifies that when pre-check falls back to local summary rendering after selecting memories, the summary section titles stay aligned with the returned ContextItems instead of reverting to stale hard-coded labels.
// TestPreCheckExecuteKeepsFallbackSummaryTitlesAlignedWithItems 用于验证当 pre-check 在选中记忆后回退到本地 summary 渲染时，摘要 section 标题会和返回的 ContextItems 保持一致，而不是退回到过期的硬编码标签。
func TestPreCheckExecuteKeepsFallbackSummaryTitlesAlignedWithItems(t *testing.T) {
	assembler := &stubPreCheckAssembler{
		err: context.DeadlineExceeded,
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "phase4 新方案",
								DetailsPreview: "这是 fallback summary 里的记忆内容。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.96,
								Origin:         "hybrid_rrf",
								CreatedAt:      time.UnixMilli(1775000004000),
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 7, SessionID: 41, Details: "上一轮确认当前环境仍是 phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs architecture memory",
			},
		},
		&stubPreCheckReviewer{
			result: logicdomain.PreCheckMemoryReviewResult{
				SelectedCandidateNumbers: []int{1},
			},
		},
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-fallback-title"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 phase4 下应该继续用哪个方案？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.Degraded || !result.ShouldInject {
		t.Fatalf("expected degraded fallback injection, got %#v", result)
	}
	if !strings.Contains(result.ContextText, "[混合召回记忆]") {
		t.Fatalf("expected fallback summary to use item-aligned memory title, got %q", result.ContextText)
	}
	if strings.Contains(result.ContextText, "[向量召回记忆]") {
		t.Fatalf("expected fallback summary to avoid stale vector-only title, got %q", result.ContextText)
	}
	if len(result.ContextItems) == 0 || result.ContextItems[0].Title != "混合召回记忆" {
		t.Fatalf("expected fallback items to expose mixed memory title, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].MemoryID != 20 {
		t.Fatalf("expected fallback items to preserve memory id for downstream memory management, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].TurnID != 8 {
		t.Fatalf("expected fallback items to preserve source turn id for downstream transport, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].CreatedTimestamp != 1775000004000 {
		t.Fatalf("expected fallback items to preserve created timestamp for downstream transport, got %#v", result.ContextItems)
	}
}

// TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates verifies stage one sees mixed refined/raw recent turns, then stage two adopts numbered candidates.
// TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates 用于验证第一层会看到 refined/raw 混合最近 turn，随后第二层按编号采纳候选。
func TestPreCheckExecuteUsesMixedRecentTurnsAndAdoptsSelectedCandidates(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "项目为什么从 mutex 改成 channel 做并发控制",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "项目已经决定用 channel 替代 mutex。",
							DetailsPreview: "这是当前项目的并发实现决策。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.93,
						},
					},
				},
				{
					QueryIndex: 1,
					Query:      "之前有没有确认过 channel 方案",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 21},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 7},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "上轮已经确认 channel 方案。",
							DetailsPreview: "它是最近刚被确认的实现方向。",
							Category:       logicdomain.MemoryNodeCategoryProjectContext,
							Score:          0.88,
						},
					},
				},
			},
		},
	}
	store := &stubPreCheckStore{
		recentTurns: []logicdomain.SessionTurnRecord{
			{
				ID:                7,
				SessionID:         41,
				DehydratedContent: `{"user":"把并发控制改成 channel","timeline":[],"assistant":"已经改掉 mutex"}`,
				ExtractedStatus:   logicdomain.TurnExtractedStatusDone,
				Details:           "上一轮确认并发控制改成 channel。",
				DetailsBudget:     20,
			},
			{
				ID:                8,
				SessionID:         41,
				DehydratedContent: `{"user":"为什么这里要改","timeline":[],"assistant":"因为 channel 更适合当前模型"}`,
				DehydratedBudget:  25,
				ExtractedStatus:   logicdomain.TurnExtractedStatusPending,
			},
		},
	}
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			Queries:    []string{"项目为什么从 mutex 改成 channel 做并发控制", "之前有没有确认过 channel 方案"},
			NeedMemory: true,
			Reason:     "needs recent architecture context",
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{2, 1},
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled adopted context",
		items: []logicdomain.ContextItem{
			{Kind: "memory", Title: "向量召回记忆", Text: "上轮已经确认 channel 方案。", Source: "vector", Score: 0.88, TurnID: 7},
			{Kind: "memory", Title: "向量召回记忆", Text: "项目已经决定用 channel 替代 mutex。", Source: "vector", Score: 0.93, TurnID: 8},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		store,
		intent,
		reviewer,
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-adopt"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "为什么这里要换成 channel？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatal("expected should_inject=true")
	}
	if result.ContextText != "assembled adopted context" {
		t.Fatalf("unexpected context text: %q", result.ContextText)
	}
	if result.Degraded {
		t.Fatal("expected degraded=false")
	}
	if len(store.adoptedIDs) != 2 || store.adoptedIDs[0] != 21 || store.adoptedIDs[1] != 20 {
		t.Fatalf("unexpected adopted ids: %#v", store.adoptedIDs)
	}
	if len(intent.turns) != 2 {
		t.Fatalf("unexpected recent turns: %#v", intent.turns)
	}
	if intent.turns[0].ContentType != "DETAILS" || intent.turns[0].Content != "上一轮确认并发控制改成 channel。" {
		t.Fatalf("unexpected extracted turn context: %#v", intent.turns[0])
	}
	if intent.turns[1].ContentType != "RAW_TURN" || !strings.Contains(intent.turns[1].Content, `"assistant":"因为 channel 更适合当前模型"`) {
		t.Fatalf("unexpected pending turn context: %#v", intent.turns[1])
	}
	if len(reviewer.input.Candidates) != 2 {
		t.Fatalf("unexpected reviewer candidates: %#v", reviewer.input.Candidates)
	}
	if reviewer.input.Candidates[0].CandidateNumber != 1 || reviewer.input.Candidates[1].CandidateNumber != 2 {
		t.Fatalf("unexpected candidate numbering: %#v", reviewer.input.Candidates)
	}
	if len(assembler.hits) != 2 || assembler.hits[0].ID != "21" || assembler.hits[1].ID != "20" {
		t.Fatalf("unexpected assembled hits: %#v", assembler.hits)
	}
	if len(memories.cmd.Queries) != 2 || memories.cmd.Queries[0] != "项目为什么从 mutex 改成 channel 做并发控制" || memories.cmd.Queries[1] != "之前有没有确认过 channel 方案" {
		t.Fatalf("unexpected search queries: %#v", memories.cmd.Queries)
	}
}

// TestPreCheckExecuteDeduplicatesReviewerSelectedCandidateNumbers verifies the usecase boundary removes repeated reviewer selections before adoption write-back and final assembly.
// TestPreCheckExecuteDeduplicatesReviewerSelectedCandidateNumbers 用于验证 usecase 边界会在采纳写回和最终组装前移除 reviewer 重复选择的候选编号。
func TestPreCheckExecuteDeduplicatesReviewerSelectedCandidateNumbers(t *testing.T) {
	store := &stubPreCheckStore{}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{1, 1, 1},
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled once",
		items: []logicdomain.ContextItem{
			{Kind: "memory", Title: "混合召回记忆", Text: "项目已经决定用 channel 替代 mutex。", Source: "memory", Score: 0.93, TurnID: 8},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "为什么改成 channel",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "项目已经决定用 channel 替代 mutex。",
								DetailsPreview: "这是当前项目的并发实现决策。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.93,
								Origin:         "hybrid_rrf",
							},
						},
					},
				},
			},
		},
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"为什么改成 channel"},
				NeedMemory: true,
				Reason:     "needs architecture memory",
			},
		},
		reviewer,
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-review-dedupe"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "为什么这里要换成 channel？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject || result.ContextText != "assembled once" {
		t.Fatalf("expected one assembled injection result, got %#v", result)
	}
	if len(store.adoptedIDs) != 1 || store.adoptedIDs[0] != 20 {
		t.Fatalf("expected duplicate reviewer selection to write adoption once, got %#v", store.adoptedIDs)
	}
	if len(assembler.hits) != 1 || assembler.hits[0].ID != "20" {
		t.Fatalf("expected duplicate reviewer selection to assemble once, got %#v", assembler.hits)
	}
}

// TestPreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID verifies that when the reviewer selects multiple memories from the same source turn, pre-check still compacts the final injected context to one representative item while preserving lifecycle write-back for every reviewer-selected memory id.
// TestPreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID 用于验证当评审器同时选中多个来自同一来源 turn 的记忆时，pre-check 仍会把最终注入上下文压缩成一个代表项，同时保留对全部选中 memory id 的生命周期写回。
func TestPreCheckExecuteDeduplicatesSelectedCandidatesBySourceTurnID(t *testing.T) {
	store := &stubPreCheckStore{}
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "记忆工具的能力与问题",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 93},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "记忆工具支持结构化信息存储。",
							DetailsPreview: "这条是同一 turn 中分数更高的能力摘要。",
							Category:       logicdomain.MemoryNodeCategoryProjectContext,
							Score:          0.96,
							Origin:         "hybrid_rrf",
							CreatedAt:      time.UnixMilli(1775000005000),
						},
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 21},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 93},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "记忆工具存在索引延迟问题。",
							DetailsPreview: "这条来自同一 turn，但不应在最终注入里与上一条同时保留。",
							Category:       logicdomain.MemoryNodeCategoryProjectContext,
							Score:          0.91,
							Origin:         "hybrid_rrf",
							CreatedAt:      time.UnixMilli(1775000005000),
						},
					},
				},
			},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"记忆工具的能力与问题"},
				NeedMemory: true,
				Reason:     "needs prior tool memory",
			},
		},
		&stubPreCheckReviewer{
			result: logicdomain.PreCheckMemoryReviewResult{
				SelectedCandidateNumbers: []int{1, 2},
			},
		},
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 4},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-turn-dedup"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "之前总结过记忆工具的能力和问题吗？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatalf("expected should_inject=true, got %#v", result)
	}
	if len(result.ContextItems) != 1 {
		t.Fatalf("expected same-turn candidates to deduplicate into one final item, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].TurnID != 93 {
		t.Fatalf("expected final retained item to keep source turn id, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].MemoryID != 20 {
		t.Fatalf("expected final retained item to keep source memory id, got %#v", result.ContextItems)
	}
	if result.ContextItems[0].CreatedTimestamp != 1775000005000 {
		t.Fatalf("expected final retained item to keep created timestamp, got %#v", result.ContextItems)
	}
	if !strings.Contains(result.ContextItems[0].Text, "结构化信息存储") {
		t.Fatalf("expected higher-scored same-turn summary to survive, got %#v", result.ContextItems)
	}
	if strings.Contains(result.ContextText, "索引延迟问题") {
		t.Fatalf("expected fallback context text to drop the duplicate same-turn summary, got %q", result.ContextText)
	}
	if len(store.adoptedIDs) != 2 || store.adoptedIDs[0] != 20 || store.adoptedIDs[1] != 21 {
		t.Fatalf("expected lifecycle write-back to preserve all reviewer-selected same-turn memories, got %#v", store.adoptedIDs)
	}
}

// TestPreCheckExecuteDegradesToEmptyResult verifies that second-stage reviewer failures do not fail the whole request, but pre-check also no longer falls back to profile-only injection.
// TestPreCheckExecuteDegradesToEmptyResult 用于验证当第二层评审器失败时，请求不会整体失败，但 pre-check 也不再回退成仅画像注入。
func TestPreCheckExecuteDegradesToEmptyResult(t *testing.T) {
	assembler := &stubPreCheckAssembler{}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "兼容",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								Abstract:       "兼容性改造需要保留旧字段。",
								DetailsPreview: "这是项目级历史规则。",
								Score:          0.95,
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 11, SessionID: 41, Details: "上一轮已经强调兼容优先。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{Queries: []string{"兼容"}, NeedMemory: true},
		},
		&stubPreCheckReviewer{err: context.DeadlineExceeded},
		assembler,
		PreCheckConfig{},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-degraded"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这个改动会不会影响兼容？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if result.ShouldInject {
		t.Fatalf("expected should_inject=false, got %#v", result)
	}
	if !result.Degraded {
		t.Fatal("expected degraded=true")
	}
	if assembler.called {
		t.Fatalf("expected assembler to be skipped on reviewer degradation with no selected memory, got %#v", assembler)
	}
}
