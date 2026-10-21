// Behavioral tests grouped by precheck recall test.
// 应用用例层 pre-check 召回范围行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"testing"
)

// TestPreCheckExecuteRewritesGenericQueriesToCurrentInput verifies that stage-one deictic queries are rewritten to the full current request before unified retrieval begins.
// TestPreCheckExecuteRewritesGenericQueriesToCurrentInput 用于验证第一层的指代式 query 会在进入统一检索前被改写为完整当前请求。
func TestPreCheckExecuteRewritesGenericQueriesToCurrentInput(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 21, SessionID: 41, Details: "上一轮已经确认 SQLite schema 13 必须保持兼容。", DetailsBudget: 18, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"这个改动为什么这样做"},
				NeedMemory: true,
				Reason:     "model returned a deictic query",
			},
		},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	current := "这个改动会不会影响 SQLite schema 13 的兼容性？"
	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-rewrite"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: current,
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if memories.searchCalls != 1 {
		t.Fatalf("expected one search call, got %d", memories.searchCalls)
	}
	if len(memories.cmd.Queries) != 1 || memories.cmd.Queries[0] != current {
		t.Fatalf("expected rewritten search query to keep full current input, got %#v", memories.cmd.Queries)
	}
}

// TestPreCheckExecuteDeduplicatesSearchQueries verifies that repeated stage-one queries are collapsed before unified retrieval and reviewer input construction so pre-check does not pay duplicate recall cost for the same query.
// TestPreCheckExecuteDeduplicatesSearchQueries 用于验证第一层重复 query 会在统一检索和 reviewer 输入前被折叠，避免 pre-check 为同一 query 支付重复召回成本。
func TestPreCheckExecuteDeduplicatesSearchQueries(t *testing.T) {
	memories := &stubPreCheckMemories{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 0,
					Query:      "phase4 当前方案",
					Hits: []MemoryQueryHit{
						{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
							SourceKind:     logicdomain.MemorySourceKindTurnExtract,
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Abstract:       "phase4 新方案",
							DetailsPreview: "这是被去重 query 命中的候选。",
							Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
							Score:          0.94,
							Origin:         "vector_search",
						},
					},
				},
			},
		},
	}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{1},
		},
	}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 17, SessionID: 41, Details: "上一轮确认当前环境是 phase4。", DetailsBudget: 15, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"SQLite schema 13 compatibility", " sqlite   schema 13 compatibility ", "SQLITE\nschema 13 compatibility"},
				NeedMemory: true,
				Reason:     "needs architecture memory",
			},
		},
		reviewer,
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	if _, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-dedupe-query"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 phase4 下应该继续用哪个方案？",
	}); err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}

	if len(memories.cmd.Queries) != 1 || memories.cmd.Queries[0] != "SQLite schema 13 compatibility" {
		t.Fatalf("expected deduplicated search queries, got %#v", memories.cmd.Queries)
	}
	if len(reviewer.input.SearchQueries) != 1 || reviewer.input.SearchQueries[0] != "SQLite schema 13 compatibility" {
		t.Fatalf("expected reviewer to see deduplicated search queries, got %#v", reviewer.input.SearchQueries)
	}
}

// TestPreCheckExecuteSkipsImmediateContextOnlyFallback verifies that when stage one fails to provide a stable long-term query, short deictic follow-ups stay on the recent-turn window instead of forcing memory retrieval.
// TestPreCheckExecuteSkipsImmediateContextOnlyFallback 用于验证当第一层没给出稳定长期 query 时，短指代追问会留在最近 turn 窗口内处理，而不会强行触发记忆检索。
func TestPreCheckExecuteSkipsImmediateContextOnlyFallback(t *testing.T) {
	memories := &stubPreCheckMemories{}
	assembler := &stubPreCheckAssembler{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{
			recentTurns: []logicdomain.SessionTurnRecord{
				{ID: 31, SessionID: 41, Details: "上一轮已经解释为什么把并发控制改成 channel。", DetailsBudget: 18, ExtractedStatus: logicdomain.TurnExtractedStatusDone},
			},
		},
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				NeedMemory: true,
				Reason:     "model was unsure and returned no stable query",
			},
		},
		&stubPreCheckReviewer{},
		assembler,
		PreCheckConfig{HistoryTurns: 4, MaxInputTokens: 200},
		nil,
	)

	result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-pre-skip"), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "这里为什么这么改？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if memories.searchCalls != 0 {
		t.Fatalf("expected no memory search call, got %d", memories.searchCalls)
	}
	if result.ShouldInject || result.Degraded {
		t.Fatalf("unexpected result: %#v", result)
	}
	if assembler.called {
		t.Fatalf("expected assembler to be skipped for immediate-context-only follow-up, got %#v", assembler)
	}
}

// TestPreCheckSearchCandidatesPassesConfiguredSearchScope verifies pre-check forwards its dedicated recall-scope config into the unified memory search command so scope policy stays caller-configurable.
// TestPreCheckSearchCandidatesPassesConfiguredSearchScope 用于验证 pre-check 会把自己的召回作用域配置透传给统一记忆搜索命令，确保作用域策略保持可配置。
func TestPreCheckSearchCandidatesPassesConfiguredSearchScope(t *testing.T) {
	cases := []struct {
		name      string
		cfgScope  string
		wantScope string
	}{
		{name: "default space", cfgScope: "", wantScope: "space"},
		{name: "project explicit", cfgScope: "project", wantScope: "project"},
		{name: "team explicit", cfgScope: "team", wantScope: "team"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			memories := &stubPreCheckMemories{}
			uc := NewPreCheckUseCase(
				memories,
				&stubPreCheckStore{},
				&stubPreCheckIntentExtractor{},
				&stubPreCheckReviewer{},
				&stubPreCheckAssembler{},
				PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: tc.cfgScope},
				nil,
			)

			_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
				Session: logicdomain.SessionRef{
					SessionID:  41,
					SessionKey: "sess-1",
					UserID:     7,
					TeamID:     3,
					SpaceID:    5,
					ProjectID:  9,
				},
				UserContent: "我之前考虑的 MC20 汽车你还记得么",
			}, logicdomain.IntentResult{
				NeedMemory: true,
				Queries:    []string{"用户之前考虑的 MC20 汽车相关信息"},
			})
			if err != nil {
				t.Fatalf("search memory candidates: %v", err)
			}
			if memories.cmd.ScopeOverride != tc.wantScope {
				t.Fatalf("scope override = %q, want %q", memories.cmd.ScopeOverride, tc.wantScope)
			}
		})
	}
}

// TestPreCheckSearchCandidatesExcludesCurrentSessionTurnsBeforeFirstCompact verifies pre-check blocks same-session turn-extract recall before any compact boundary exists.
// TestPreCheckSearchCandidatesExcludesCurrentSessionTurnsBeforeFirstCompact 用于验证在首次 compact 之前，pre-check 会屏蔽同 session 的 turn-extract 召回。
func TestPreCheckSearchCandidatesExcludesCurrentSessionTurnsBeforeFirstCompact(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: "project"},
		nil,
	)

	_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			TeamID:              3,
			SpaceID:             5,
			ProjectID:           9,
			LastCompactedTurnID: 0,
		},
		UserContent: "我之前比较过哪些车？",
		RecallMode:  PreCheckRecallModeSessionCompact,
	}, logicdomain.IntentResult{
		NeedMemory: true,
		Queries:    []string{"用户之前比较过哪些车"},
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if memories.cmd.BoundarySessionID != 41 {
		t.Fatalf("boundary session id = %d, want 41", memories.cmd.BoundarySessionID)
	}
	if !memories.cmd.ExcludeBoundaryTurn {
		t.Fatalf("expected pre-check to exclude current session turns before compact, got %#v", memories.cmd)
	}
	if memories.cmd.BoundaryMaxTurnID != 0 {
		t.Fatalf("boundary max turn id = %d, want 0", memories.cmd.BoundaryMaxTurnID)
	}
}

// TestPreCheckSearchCandidatesReopensOnlyCompactedTurns verifies pre-check reopens only the turn history at or before the latest compact boundary.
// TestPreCheckSearchCandidatesReopensOnlyCompactedTurns 用于验证 pre-check 只重新开放最近 compact 边界及之前的 turn 历史。
func TestPreCheckSearchCandidatesReopensOnlyCompactedTurns(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: "project"},
		nil,
	)

	_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			TeamID:              3,
			SpaceID:             5,
			ProjectID:           9,
			LastCompactedTurnID: 88,
		},
		UserContent: "帮我回忆一下压缩前的决策",
		RecallMode:  PreCheckRecallModeSessionCompact,
	}, logicdomain.IntentResult{
		NeedMemory: true,
		Queries:    []string{"压缩前的决策"},
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if memories.cmd.BoundarySessionID != 41 {
		t.Fatalf("boundary session id = %d, want 41", memories.cmd.BoundarySessionID)
	}
	if memories.cmd.ExcludeBoundaryTurn {
		t.Fatalf("did not expect full exclusion after compact, got %#v", memories.cmd)
	}
	if memories.cmd.BoundaryMaxTurnID != 88 {
		t.Fatalf("boundary max turn id = %d, want 88", memories.cmd.BoundaryMaxTurnID)
	}
}

// TestPreCheckSearchCandidatesDefaultsToCompactRecallMode verifies omitted mode values use compact-boundary filtering instead of the deprecated legacy branch.
// TestPreCheckSearchCandidatesDefaultsToCompactRecallMode 用于验证省略 mode 时会应用 compact 边界过滤，而不是进入已弃用的 legacy 分支。
func TestPreCheckSearchCandidatesDefaultsToCompactRecallMode(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: "project"},
		nil,
	)

	_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			TeamID:              3,
			SpaceID:             5,
			ProjectID:           9,
			LastCompactedTurnID: 88,
		},
		UserContent: "省略模式时仍需要 compact 判定",
	}, logicdomain.IntentResult{
		NeedMemory: true,
		Queries:    []string{"省略模式时仍需要 compact 判定"},
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if memories.cmd.BoundarySessionID != 41 || memories.cmd.BoundaryMaxTurnID != 88 || memories.cmd.ExcludeBoundaryTurn {
		t.Fatalf("expected omitted mode to use compact boundary filtering, got %#v", memories.cmd)
	}
}

// TestPreCheckSearchCandidatesFullRecallSkipsCurrentSessionBoundary verifies only the explicit FULL mode removes the current-session compact filter.
// TestPreCheckSearchCandidatesFullRecallSkipsCurrentSessionBoundary 用于验证只有显式 FULL 模式会移除当前 session 的 compact 过滤条件。
func TestPreCheckSearchCandidatesFullRecallSkipsCurrentSessionBoundary(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: "project"},
		nil,
	)

	_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			TeamID:              3,
			SpaceID:             5,
			ProjectID:           9,
			LastCompactedTurnID: 88,
		},
		UserContent: "显式请求当前检索范围内的全量记忆",
		RecallMode:  PreCheckRecallModeFull,
	}, logicdomain.IntentResult{
		NeedMemory: true,
		Queries:    []string{"显式请求当前检索范围内的全量记忆"},
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if memories.cmd.BoundarySessionID != 0 || memories.cmd.BoundaryMaxTurnID != 0 || memories.cmd.ExcludeBoundaryTurn {
		t.Fatalf("expected full recall to omit current-session boundary filtering, got %#v", memories.cmd)
	}
}

// TestPreCheckSearchCandidatesFallsBackUnknownRecallModeToCompact verifies unsupported future mode values fall back to the strict compact branch instead of silently reopening the whole current session.
// TestPreCheckSearchCandidatesFallsBackUnknownRecallModeToCompact 用于验证未受支持的未来 mode 值会回退到严格的 compact 分支，而不是静默重新开放整个当前 session。
func TestPreCheckSearchCandidatesFallsBackUnknownRecallModeToCompact(t *testing.T) {
	memories := &stubPreCheckMemories{}
	uc := NewPreCheckUseCase(
		memories,
		&stubPreCheckStore{},
		&stubPreCheckIntentExtractor{},
		&stubPreCheckReviewer{},
		&stubPreCheckAssembler{},
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, SearchScope: "project"},
		nil,
	)

	_, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:           41,
			SessionKey:          "sess-1",
			UserID:              7,
			TeamID:              3,
			SpaceID:             5,
			ProjectID:           9,
			LastCompactedTurnID: 88,
		},
		UserContent: "未来插件发送了更新的 compact 模式",
		RecallMode:  PreCheckRecallMode(9),
	}, logicdomain.IntentResult{
		NeedMemory: true,
		Queries:    []string{"未来插件发送了更新的 compact 模式"},
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if memories.cmd.BoundarySessionID != 41 || memories.cmd.BoundaryMaxTurnID != 88 || memories.cmd.ExcludeBoundaryTurn {
		t.Fatalf("expected unknown mode to fall back to compact boundary filtering, got %#v", memories.cmd)
	}
}

// TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores verifies that when the unified search layer already decided an equal-score order, pre-check preserves that order instead of reordering ties by memory id.
// TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores 用于验证当 unified search 层已经决定了同分顺序时，pre-check 会保留该顺序，而不是再按 memory id 改写 tie-break。
func TestPreCheckSearchCandidatesKeepsUnifiedSearchOrderForEqualScores(t *testing.T) {
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "phase4 当前方案",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "应优先保留的上游第一候选",
								DetailsPreview: "它在 unified search 里已经排在前面。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.91,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 20},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 17},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "不应因为 memory id 更小而顶替前者",
								DetailsPreview: "这条只是同分候选。",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.91,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
						},
					},
				},
			},
		},
		nil,
		nil,
		nil,
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 1},
		nil,
	)

	candidates, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "在 phase4 下应该继续用哪个方案？",
	}, logicdomain.IntentResult{
		Queries:    []string{"phase4 当前方案"},
		NeedMemory: true,
		Reason:     "needs architecture memory",
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one retained candidate after limit, got %#v", candidates)
	}
	if candidates[0].MemoryID != 30 {
		t.Fatalf("expected unified-search first candidate to survive equal-score tie, got %#v", candidates)
	}
	if candidates[0].CandidateNumber != 1 {
		t.Fatalf("expected candidate numbering to follow preserved order, got %#v", candidates)
	}
}

// TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor verifies that pre-check does not discard the first reranked candidate just because the provider-specific rerank score is lower than the vector similarity threshold.
// TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor 用于验证 pre-check 不会仅因 provider 自定义 rerank 分低于向量相似度阈值，就误丢第一条 rerank 候选。
func TestPreCheckSearchCandidatesKeepsTopRerankedHitBelowSimilarityFloor(t *testing.T) {
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "文本排序模型",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 202},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 42},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "第二条",
								DetailsPreview: "它被 rerank 评为当前 query 的第一候选。",
								Category:       logicdomain.MemoryNodeCategoryProjectContext,
								Score:          0.27,
								Origin:         "vector_rerank",
							},
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 201},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 41},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "第一条",
								DetailsPreview: "它是 rerank 后的第二候选。",
								Category:       logicdomain.MemoryNodeCategoryProjectContext,
								Score:          0.18,
								Origin:         "vector_rerank",
							},
						},
					},
				},
			},
		},
		nil,
		nil,
		nil,
		nil,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 4},
		nil,
	)

	candidates, err := uc.searchMemoryCandidates(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "什么是文本排序模型？",
	}, logicdomain.IntentResult{
		Queries:    []string{"文本排序模型"},
		NeedMemory: true,
		Reason:     "needs reranked memory candidate",
	})
	if err != nil {
		t.Fatalf("search memory candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected only the top reranked candidate to survive thresholding, got %#v", candidates)
	}
	if candidates[0].MemoryID != 202 {
		t.Fatalf("expected first reranked candidate to survive, got %#v", candidates)
	}
	if candidates[0].Score < 0.99 {
		t.Fatalf("expected reviewer-facing score to be normalized from rerank order, got %#v", candidates)
	}
	if candidates[0].ScoreLabel != "Very Strong Match" {
		t.Fatalf("expected normalized rerank candidate to receive a strong reviewer label, got %#v", candidates)
	}
}
