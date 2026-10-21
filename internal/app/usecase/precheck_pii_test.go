// Behavioral tests grouped by precheck pii test.
// 应用用例层 pre-check 脱敏行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
)

// TestPreCheckExecuteScrubsPIIBeforeIntentReviewAndAssembly verifies pre-check redacts the current request, recent-turn context, and recalled memory payloads before they reach the first-stage extractor, second-stage reviewer, and final assembler.
// TestPreCheckExecuteScrubsPIIBeforeIntentReviewAndAssembly 用于验证 pre-check 会在第一层提取器、第二层评审器和最终组装器看到文本之前，先脱敏当前请求、最近 turn 上下文以及召回记忆内容。
func TestPreCheckExecuteScrubsPIIBeforeIntentReviewAndAssembly(t *testing.T) {
	intent := &stubPreCheckIntentExtractor{
		result: logicdomain.IntentResult{
			Queries:    []string{"联系方式"},
			NeedMemory: true,
			Reason:     "needs contact memory",
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{1},
		},
	}
	assembler := &stubPreCheckAssembler{
		items: []logicdomain.ContextItem{
			{Text: "已脱敏上下文"},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "联系方式",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:            logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:            logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
								SourceKind:           logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:           logicdomain.MemoryScopeLevelProject,
								Abstract:             "用户电话是 13800138000",
								DetailsPreview:       "如果要联系用户，请拨打 13800138000",
								Category:             logicdomain.MemoryNodeCategorySecurityPolicy,
								Score:                0.97,
								Origin:               "hybrid_rrf",
								MatchedContextValues: []string{"13800138000"},
							},
						},
					},
				},
			},
		},
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{
					ID:              7,
					SessionID:       41,
					Details:         "上一轮里用户说他的电话是 13800138000。",
					DetailsBudget:   15,
					ExtractedStatus: logicdomain.TurnExtractedStatusDone,
				},
			},
		},
		intent,
		reviewer,
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)
	uc.ConfigurePIIScrubber(stubPIIScrubber{
		replacements: map[string]string{
			"13800138000": "[PHONE]",
		},
	})

	result, err := uc.Execute(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-pii",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "我电话是 13800138000，帮我找下联系方式。",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatalf("expected assembled injection result, got %#v", result)
	}
	if strings.Contains(intent.current, "13800138000") {
		t.Fatalf("expected current input to be scrubbed before intent extraction, got %q", intent.current)
	}
	if len(intent.turns) != 1 || strings.Contains(intent.turns[0].Content, "13800138000") {
		t.Fatalf("expected recent turns to be scrubbed before intent extraction, got %#v", intent.turns)
	}
	if strings.Contains(reviewer.input.UserContent, "13800138000") {
		t.Fatalf("expected reviewer input to use scrubbed user content, got %#v", reviewer.input)
	}
	if len(reviewer.input.Candidates) != 1 {
		t.Fatalf("expected one reviewer candidate, got %#v", reviewer.input.Candidates)
	}
	if strings.Contains(reviewer.input.Candidates[0].Abstract, "13800138000") || strings.Contains(reviewer.input.Candidates[0].Details, "13800138000") {
		t.Fatalf("expected reviewer candidate text to be scrubbed, got %#v", reviewer.input.Candidates[0])
	}
	if len(reviewer.input.Candidates[0].MatchedContextValues) != 1 || reviewer.input.Candidates[0].MatchedContextValues[0] != "[PHONE]" {
		t.Fatalf("expected matched context values to be scrubbed, got %#v", reviewer.input.Candidates[0].MatchedContextValues)
	}
	if len(assembler.hits) != 1 || strings.Contains(assembler.hits[0].Text, "13800138000") {
		t.Fatalf("expected assembled hits to stay scrubbed, got %#v", assembler.hits)
	}
}

// TestPreCheckSearchMemoryCandidatesScrubsMergedCandidatesOnce verifies repeated query-group hits for the same memory are merged before reviewer-candidate PII redaction runs.
// TestPreCheckSearchMemoryCandidatesScrubsMergedCandidatesOnce 用于验证同一 memory 被多个 query group 命中时，会先合并再执行 reviewer 候选脱敏。
func TestPreCheckSearchMemoryCandidatesScrubsMergedCandidatesOnce(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "联系方式",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
							Abstract:                   "用户电话是 13800138000",
							DetailsPreview:             "需要联系时使用 13800138000",
							Category:                   logicdomain.MemoryNodeCategorySecurityPolicy,
							Score:                      0.95,
							Origin:                     "vector_search",
							MatchedContextValues:       []string{"phone=13800138000"},
							MatchedContextSupportCount: 1,
							MatchedContextScoreDelta:   0.05,
						},
					},
				},
				{
					QueryIndex: 1,
					Query:      "电话",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
							SourceRef:                  logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 8},
							SourceKind:                 logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:                 logicdomain.MemoryScopeLevelProject,
							Abstract:                   "用户电话是 13800138000",
							DetailsPreview:             "需要联系时使用 13800138000",
							Category:                   logicdomain.MemoryNodeCategorySecurityPolicy,
							Score:                      0.94,
							Origin:                     "hybrid_rrf",
							MatchedContextValues:       []string{"phone=13900139000"},
							MatchedContextSupportCount: 2,
							MatchedContextScoreDelta:   0.04,
						},
					},
				},
			},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		nil,
		nil,
		nil,
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 10},
		nil,
	)
	scrubber := &replacingCountingPIIScrubber{
		replacements: map[string]string{
			"13800138000": "masked phone",
			"13900139000": "masked phone",
		},
	}
	uc.ConfigurePIIScrubber(scrubber)

	candidates, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-precheck-merge-scrub",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "帮我查联系方式",
	}, logicdomain.IntentResult{
		Queries:    []string{"联系方式", "电话"},
		NeedMemory: true,
		Reason:     "contact recall",
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected duplicate memory hits to merge into one candidate, got %#v", candidates)
	}
	candidate := candidates[0]
	if strings.Contains(candidate.Abstract, "13800138000") || strings.Contains(candidate.Abstract, "13900139000") ||
		strings.Contains(candidate.Details, "13800138000") || strings.Contains(candidate.Details, "13900139000") {
		t.Fatalf("expected merged candidate text to be scrubbed, got %#v", candidate)
	}
	if len(candidate.MatchedContextValues) != 1 || candidate.MatchedContextValues[0] != "phone=masked phone" {
		t.Fatalf("expected scrubbed context values to be deduplicated, got %#v", candidate.MatchedContextValues)
	}
	if scrubber.calls != 6 {
		t.Fatalf("expected scrub calls to reuse log previews and scrub one merged candidate once, got %d", scrubber.calls)
	}
}
