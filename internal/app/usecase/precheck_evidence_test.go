// Behavioral tests grouped by precheck evidence test.
// 应用用例层 pre-check 情境证据行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"strings"
	"testing"
)

// TestPreCheckExecutePassesMatchedContextEvidenceToReviewer verifies that query-time context evidence is preserved when the second-stage reviewer receives numbered candidates.
// TestPreCheckExecutePassesMatchedContextEvidenceToReviewer 用于验证查询期命中的 context evidence 会在第二层 reviewer 收到的候选里被完整保留。
func TestPreCheckExecutePassesMatchedContextEvidenceToReviewer(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 下的 phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "它与 local oss 当前场景匹配。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.96,
								Origin:                      "hybrid_rrf",
								SupportCount:                4,
								RebuttalCount:               1,
								MatchedContextValues:        []string{"deployment_mode=local oss", "task_stage=phase4"},
								MatchedContextSupportCount:  3,
								MatchedContextRebuttalCount: 0,
								MatchedContextScoreDelta:    0.075,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 7, SessionID: 41, Details: "上一轮确认当前环境仍是 local oss。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 下的 phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs environment-specific architecture memory",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-evidence"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("unexpected reviewer candidates: %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if candidate.SupportCount != 4 || candidate.RebuttalCount != 1 {
		t.Fatalf("expected aggregate support/rebuttal counts to be preserved, got %#v", candidate)
	}
	if candidate.Origin != "hybrid_rrf" || candidate.OriginLabel != "Hybrid RRF" {
		t.Fatalf("expected origin explanation to be preserved, got %#v", candidate)
	}
	if candidate.OriginExplanation == "" {
		t.Fatalf("expected non-empty origin explanation, got %#v", candidate)
	}
	if candidate.ScoreLabel != "Very Strong Match" || candidate.ScoreExplanation == "" {
		t.Fatalf("expected score explanation to be preserved, got %#v", candidate)
	}
	if candidate.MatchedContextSupportCount != 3 || candidate.MatchedContextRebuttalCount != 0 || candidate.MatchedContextScoreDelta <= 0 {
		t.Fatalf("expected matched context evidence to be preserved, got %#v", candidate)
	}
	if len(candidate.MatchedContextValues) != 2 || candidate.MatchedContextValues[0] != "deployment_mode=local oss" || candidate.MatchedContextValues[1] != "task_stage=phase4" {
		t.Fatalf("unexpected matched context values: %#v", candidate.MatchedContextValues)
	}
}

// TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits verifies that when the same memory is recalled by multiple query groups, reviewer-facing matched context evidence is merged instead of silently keeping only the first equal-scored hit.
// TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits 用于验证同一 memory 被多个 query group 命中时，reviewer 看到的 matched context 证据会被合并，而不是在分数相等时只保留第一条。
func TestPreCheckExecuteMergesEvidenceAcrossRepeatedMemoryHits(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "适用于 local oss，并且这条记录补充了更完整的历史背景和实现约束说明。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.92,
								Origin:                     "vector_search",
								MatchedContextValues:       []string{"deployment_mode=local oss"},
								MatchedContextSupportCount: 1,
								MatchedContextScoreDelta:   0.03,
							},
						},
					},
					{
						QueryIndex: 1,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "适用于 phase4 当前环境。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.92,
								Origin:                     "hybrid_rrf_rerank_mmr",
								MatchedContextValues:       []string{"task_stage=phase4"},
								MatchedContextSupportCount: 2,
								MatchedContextScoreDelta:   0.05,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 7, SessionID: 41, Details: "上一轮确认当前环境是 local oss phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 当前方案", "phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs repeated architecture memory evidence",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-merge"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one merged candidate, got %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if len(candidate.MatchedContextValues) != 2 || candidate.MatchedContextValues[0] != "deployment_mode=local oss" || candidate.MatchedContextValues[1] != "task_stage=phase4" {
		t.Fatalf("expected merged matched context values, got %#v", candidate.MatchedContextValues)
	}
	if candidate.MatchedContextSupportCount != 2 {
		t.Fatalf("expected strongest matched support count to survive merge, got %#v", candidate)
	}
	if candidate.MatchedContextScoreDelta != 0.05 {
		t.Fatalf("expected strongest matched delta to survive merge, got %#v", candidate)
	}
	if candidate.Origin != "hybrid_rrf_rerank_mmr" || candidate.OriginLabel != "Hybrid RRF + Rerank + MMR" {
		t.Fatalf("expected stronger origin explanation to survive merge, got %#v", candidate)
	}
	if candidate.ScoreLabel != "Very Strong Match" || !strings.Contains(candidate.ScoreExplanation, "0.050") {
		t.Fatalf("expected stronger score explanation to survive merge, got %#v", candidate)
	}
	if !strings.Contains(candidate.Details, "更完整的历史背景和实现约束说明") {
		t.Fatalf("expected richer details text to survive merge, got %#v", candidate)
	}
}

// TestPreCheckExecuteNormalizesEquivalentMatchedEvidenceLabels verifies repeated-hit merging reuses the shared context-evidence canonical surface so reviewer candidates do not show duplicated labels that only differ by formatting drift.
// TestPreCheckExecuteNormalizesEquivalentMatchedEvidenceLabels 用于验证重复命中合并会复用共享的 context evidence 规范表面，避免 reviewer 候选因格式漂移展示重复标签。
func TestPreCheckExecuteNormalizesEquivalentMatchedEvidenceLabels(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 21},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 9},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "第一组命中使用旧格式的 deployment context 标签。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.93,
								Origin:                     "vector_search",
								MatchedContextValues:       []string{"deployment_mode=LOCAL_OSS"},
								MatchedContextSupportCount: 1,
								MatchedContextScoreDelta:   0.03,
							},
						},
					},
					{
						QueryIndex: 1,
						Query:      "deployment mode local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 21},
								SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 9},
								SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
								Abstract:                   "phase4 新方案",
								DetailsPreview:             "第二组命中使用新格式的 deployment context 标签。",
								Category:                   logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                      0.93,
								Origin:                     "hybrid_rrf",
								MatchedContextValues:       []string{"deployment mode= local-oss "},
								MatchedContextSupportCount: 2,
								MatchedContextScoreDelta:   0.05,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 8, SessionID: 41, Details: "上一轮确认当前环境仍为 local oss。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 当前方案", "deployment mode local oss 当前方案"},
				NeedMemory: true,
				Reason:     "needs deployment-specific architecture memory",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-normalized-evidence"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one merged candidate, got %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if len(candidate.MatchedContextValues) != 1 {
		t.Fatalf("expected equivalent matched context labels to collapse into one canonical value, got %#v", candidate.MatchedContextValues)
	}
	if candidate.MatchedContextValues[0] != "deployment_mode=local oss" {
		t.Fatalf("expected canonical matched context value, got %#v", candidate.MatchedContextValues)
	}
	if candidate.MatchedContextSupportCount != 2 || candidate.MatchedContextScoreDelta != 0.05 {
		t.Fatalf("expected stronger matched evidence stats to survive merge, got %#v", candidate)
	}
}

// TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary verifies that when a higher-score hit becomes the representative candidate, stronger matched-context stats from the other repeated hit are still preserved.
// TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary 用于验证当更高分命中成为代表候选时，另一条重复命中里更强的 matched-context 统计仍会被保留下来。
func TestPreCheckExecuteKeepsStrongerMatchedEvidenceFromSecondary(t *testing.T) {
	reviewer := &stubPreCheckReviewer{}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "local oss 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "这条命中分更高。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.94,
								Origin:                      "vector_search",
								MatchedContextValues:        []string{"deployment_mode=local oss"},
								MatchedContextSupportCount:  1,
								MatchedContextRebuttalCount: 0,
								MatchedContextScoreDelta:    0.02,
							},
						},
					},
					{
						QueryIndex: 1,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:                   logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:                  logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:                  logicdomain.MemoryScopeLevelProject,
								Abstract:                    "phase4 新方案",
								DetailsPreview:              "这条命中的 context evidence 更强。",
								Category:                    logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:                       0.91,
								Origin:                      "hybrid_rrf",
								MatchedContextValues:        []string{"task_stage=phase4"},
								MatchedContextSupportCount:  3,
								MatchedContextRebuttalCount: 1,
								MatchedContextScoreDelta:    0.07,
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 17, SessionID: 41, Details: "上一轮确认当前环境是 local oss phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"local oss 当前方案", "phase4 当前方案"},
				NeedMemory: true,
				Reason:     "needs repeated architecture memory evidence",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-secondary-evidence"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 local oss 的 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one merged candidate, got %#v", reviewer.input.Candidates)
	}
	candidate := reviewer.input.Candidates[0]
	if candidate.Score != 0.94 {
		t.Fatalf("expected higher representative score to remain, got %#v", candidate)
	}
	if candidate.MatchedContextSupportCount != 3 || candidate.MatchedContextRebuttalCount != 1 {
		t.Fatalf("expected stronger secondary matched counts to survive merge, got %#v", candidate)
	}
	if candidate.MatchedContextScoreDelta != 0.07 {
		t.Fatalf("expected stronger secondary matched delta to survive merge, got %#v", candidate)
	}
	if !strings.Contains(candidate.ScoreExplanation, "0.070") {
		t.Fatalf("expected score explanation to reflect merged delta, got %#v", candidate)
	}
}
