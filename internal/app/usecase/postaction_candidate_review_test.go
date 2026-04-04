// postaction_candidate_review_test.go verifies the new post-action admission filter, shared-scope dedupe recall, and unified reviewer path.
// postaction_candidate_review_test.go 用于验证新的 post-action 准入过滤、对等作用域去重召回以及统一 reviewer 路径。
package usecase

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// TestApplyPostActionAdmissionFilterDropsQAEchoAndNonDurableCandidates verifies first-pass admission removes QA echoes and ephemeral external results, while still keeping durable research-derived facts.
// TestApplyPostActionAdmissionFilterDropsQAEchoAndNonDurableCandidates 用于验证首轮准入会去掉问答回显和短暂外部结果，同时保留具备长期价值的外部研究事实。
func TestApplyPostActionAdmissionFilterDropsQAEchoAndNonDurableCandidates(t *testing.T) {
	analysis := logicdomain.TurnAnalysis{
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:        logicdomain.MemoryNodeCategoryRequirementTODO,
				Abstract:        "只是回显用户刚刚的问题",
				Details:         "只是回显用户刚刚的问题",
				EvidenceSource:  logicdomain.TurnAnalysisEvidenceSourceAssistantRecalledMemory,
				Admission:       logicdomain.TurnAnalysisAdmissionDrop,
				AdmissionReason: logicdomain.TurnAnalysisAdmissionReasonQAAnswerOnly,
			},
			{
				Category:       logicdomain.MemoryNodeCategoryTechSpecAPI,
				Abstract:       "官方文档确认当前 SDK 需要显式关闭自动重试。",
				Details:        "助手通过外部检索与文档归纳后确认当前 SDK 需要显式关闭自动重试。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantExternalResearch,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{
			{
				ProfileType:     logicdomain.ProfileTypeProject,
				Content:         "当前 CPU 温度是 67 度。",
				EvidenceSource:  logicdomain.TurnAnalysisEvidenceSourceAssistantExternalResearch,
				Admission:       logicdomain.TurnAnalysisAdmissionDrop,
				AdmissionReason: logicdomain.TurnAnalysisAdmissionReasonNonDurable,
			},
			{
				ProfileType:    logicdomain.ProfileTypeProject,
				Content:        "当前项目需要遵循内部网关接入规范。",
				EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered,
				Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			},
		},
	}
	stats := newPostActionCompactionStats(analysis)

	applyPostActionAdmissionFilter(&analysis, &stats)

	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected one durable memory candidate, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.ProfileNodes) != 1 {
		t.Fatalf("expected one durable profile candidate, got %+v", analysis.ProfileNodes)
	}
	if stats.AdmissionDroppedCount != 2 {
		t.Fatalf("expected two first-pass drops, got %+v", stats)
	}
	if analysis.MemoryNodes[0].EvidenceSource != logicdomain.TurnAnalysisEvidenceSourceAssistantExternalResearch {
		t.Fatalf("expected durable external research memory to survive, got %+v", analysis.MemoryNodes[0])
	}
	if analysis.ProfileNodes[0].EvidenceSource != logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered {
		t.Fatalf("expected durable tool-discovered profile to survive, got %+v", analysis.ProfileNodes[0])
	}
}

// TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredDedupeScope verifies post-action duplicate recall reuses the same effective search-scope policy as pre-check.
// TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredDedupeScope 用于验证 post-action 的重复召回会复用与 pre-check 相同的有效检索作用域策略。
func TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredDedupeScope(t *testing.T) {
	tests := []struct {
		name      string
		rawScope  string
		wantScope string
	}{
		{name: "default-space", rawScope: "", wantScope: preCheckSearchScopeSpace},
		{name: "project", rawScope: preCheckSearchScopeProject, wantScope: preCheckSearchScopeProject},
		{name: "team", rawScope: preCheckSearchScopeTeam, wantScope: preCheckSearchScopeTeam},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &stubPostActionMemorySearcher{
				result: MemoryQueryResult{
					Results: []MemoryQueryGroupResult{{
						QueryIndex: 0,
						Query:      "记录部署规范",
						Hits: []MemoryQueryHit{{
							MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 9001},
							SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 701},
							ScopeLevel:     logicdomain.MemoryScopeLevelProject,
							Category:       logicdomain.MemoryNodeCategoryTechSpecAPI,
							Score:          0.96,
							Origin:         "vector",
							Abstract:       "已有部署规范",
							DetailsPreview: "已有部署规范详情",
						}},
					}},
				},
			}
			reviewer := &stubPostActionCandidateReviewer{
				result: logicdomain.PostActionCandidateReviewResult{
					Memory: &logicdomain.PostActionMemoryReviewSection{
						AcceptedCandidateIndexes: []int{0},
						DroppedCandidateIndexes:  nil,
						Reason:                   "保留唯一候选。",
					},
				},
			}
			uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
				DedupeSearchTopK:    7,
				DedupeSearchScope:   tt.rawScope,
				DedupeMinSimilarity: 0.90,
			}, nil, false)
			analysis := &logicdomain.TurnAnalysis{
				UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
				MemoryNodes: []logicdomain.MemoryNodeCandidate{{
					Category:       logicdomain.MemoryNodeCategoryTechSpecAPI,
					Abstract:       "部署时必须显式关闭自动升级。",
					Details:        "部署时必须显式关闭自动升级，避免生产侧滚动重启。",
					EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
					Admission:      logicdomain.TurnAnalysisAdmissionKeep,
				}},
			}

			err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
				SessionID:  41,
				SessionKey: "sess-dedupe-scope",
				UserID:     7,
				TeamID:     3,
				SpaceID:    5,
				ProjectID:  9,
			}, logicdomain.PersistedTurnRecord{
				ID:        88,
				SessionID: 41,
				ProjectID: 9,
				CreatedAt: time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC),
			}, logicdomain.TurnRecord{
				UserContent:      "把部署规范记下来",
				AssistantContent: "我来整理成长期规则。",
			}, analysis, &postActionCompactionStats{})
			if err != nil {
				t.Fatalf("review turn candidates: %v", err)
			}
			if len(searcher.commands) != 1 {
				t.Fatalf("expected one dedupe search call, got %+v", searcher.commands)
			}
			if searcher.commands[0].ScopeOverride != tt.wantScope {
				t.Fatalf("expected dedupe scope %q, got %+v", tt.wantScope, searcher.commands[0])
			}
			if searcher.commands[0].TopK != 7 {
				t.Fatalf("expected dedupe top-k 7, got %+v", searcher.commands[0])
			}
			if reviewer.calls != 1 {
				t.Fatalf("expected one unified reviewer call, got %d", reviewer.calls)
			}
			if len(analysis.MemoryNodes) != 1 {
				t.Fatalf("expected memory candidate to survive review, got %+v", analysis.MemoryNodes)
			}
		})
	}
}

// TestPostActionUseCaseReviewTurnCandidatesUsesUnifiedReviewerOnceForMemoryAndProfile verifies one LLM review call jointly handles memory dedupe and profile acceptance when both candidate families exist.
// TestPostActionUseCaseReviewTurnCandidatesUsesUnifiedReviewerOnceForMemoryAndProfile 用于验证当记忆与画像候选同时存在时，会用一次统一 LLM 评审同时处理二者。
func TestPostActionUseCaseReviewTurnCandidatesUsesUnifiedReviewerOnceForMemoryAndProfile(t *testing.T) {
	store := &testRelationalStore{
		profileReviewTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{{
				ID:            12,
				TurnID:        70,
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "用户之前更偏好 Go。",
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				ProfileDate:   "2026-04-01",
				CreatedAt:     time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
			}},
		},
	}
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex: 0,
				Query:      "用户确认团队以 Rust 为主语言",
				Hits: []MemoryQueryHit{
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 501},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 33},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
						Score:          0.97,
						Origin:         "vector",
						Abstract:       "项目此前已经讨论过 Rust 主语言。",
						DetailsPreview: "项目此前已经讨论过 Rust 主语言，但未形成明确规则。",
					},
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 502},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 34},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
						Score:          0.82,
						Origin:         "vector",
						Abstract:       "弱相关旧讨论",
						DetailsPreview: "这条低于 post-action 判重阈值，不应送进 reviewer。",
					},
				},
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  nil,
				Reason:                   "当前记忆是新规则，不是旧节点原样重复。",
			},
			User: &logicdomain.ProfileReviewSection{
				AcceptedCandidates: []logicdomain.ProfileReviewAcceptedCandidate{{
					CandidateIndex:    0,
					NormalizedContent: "用户偏好使用 Rust 作为主要开发语言",
					Priority:          logicdomain.ProfilePriorityP1,
					ProfileLevel:      logicdomain.ProfileLevelStable,
					LevelReason:       "这是稳定开发偏好。",
					SupersedeNodeIDs:  []uint64{12},
				}},
				InvalidCandidateIndexes: nil,
				RetireOnlyNodeIDs:       nil,
				Reason:                  "新偏好明确且需要替代旧节点。",
			},
		},
	}
	uc := newPostActionUseCase(nil, store, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
		DedupeSearchTopK:    5,
		DedupeSearchScope:   preCheckSearchScopeTeam,
		DedupeMinSimilarity: 0.90,
	}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputMixed,
		Details:       "用户明确要求团队以后默认使用 Rust，并确认这是长期偏好。",
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
			Abstract:       "团队默认使用 Rust 作为主要开发语言。",
			Details:        "用户明确要求团队以后默认使用 Rust 作为主要开发语言。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeUser,
			Content:        "我以后主要还是用 Rust。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}
	stats := postActionCompactionStats{}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  42,
		SessionKey: "sess-unified-review",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        89,
		SessionID: 42,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 4, 11, 0, 0, 0, time.UTC),
	}, logicdomain.TurnRecord{
		UserContent:      "以后主要用 Rust，帮我记住。",
		AssistantContent: "我会把它视为长期偏好和项目规则。",
	}, analysis, &stats)
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected one unified reviewer call, got %d", reviewer.calls)
	}
	if len(searcher.commands) != 1 {
		t.Fatalf("expected one dedupe search call, got %+v", searcher.commands)
	}
	if len(reviewer.inputs) != 1 {
		t.Fatalf("expected one captured reviewer input, got %+v", reviewer.inputs)
	}
	if len(reviewer.inputs[0].MemoryCandidates) != 1 || len(reviewer.inputs[0].ProfileCandidates) != 1 {
		t.Fatalf("expected one memory and one profile candidate, got %+v", reviewer.inputs[0])
	}
	if len(reviewer.inputs[0].MemoryCandidates[0].SimilarMemories) != 1 {
		t.Fatalf("expected only one high-similarity memory to survive thresholding, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories)
	}
	if reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0].ScopeLevel != logicdomain.MemoryScopeLevelLabel(logicdomain.MemoryScopeLevelProject) {
		t.Fatalf("expected similar memory scope label to be normalized, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0])
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected memory node to survive unified review, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.ProfileNodes) != 1 {
		t.Fatalf("expected one active profile node after unified review, got %+v", analysis.ProfileNodes)
	}
	if analysis.ProfileNodes[0].Status != logicdomain.ProfileStatusActive {
		t.Fatalf("expected accepted profile node to become active, got %+v", analysis.ProfileNodes[0])
	}
	if analysis.ProfileNodes[0].Content != "用户偏好使用 Rust 作为主要开发语言" {
		t.Fatalf("expected normalized profile content, got %+v", analysis.ProfileNodes[0])
	}
	if !analysis.UserProfileMerged || !strings.Contains(analysis.MergedUserProfile, "用户偏好使用 Rust 作为主要开发语言") {
		t.Fatalf("expected merged user profile text, got %+v", analysis)
	}
}

// TestPostActionUseCaseReviewTurnCandidatesKeepsInvalidProfilesForPersistence verifies unified review keeps invalid profile evidence in the persistence payload while excluding it from the merged active profile view.
// TestPostActionUseCaseReviewTurnCandidatesKeepsInvalidProfilesForPersistence 用于验证统一评审会把 invalid 画像证据保留在待持久化载荷中，同时不把它混入合并后的活跃画像视图。
func TestPostActionUseCaseReviewTurnCandidatesKeepsInvalidProfilesForPersistence(t *testing.T) {
	store := &testRelationalStore{
		profileReviewTargets: logicdomain.ProfileReviewTargetsSnapshot{},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			User: &logicdomain.ProfileReviewSection{
				AcceptedCandidates:      nil,
				InvalidCandidateIndexes: []int{0},
				RetireOnlyNodeIDs:       nil,
				Reason:                  "这条候选不具备长期画像价值。",
			},
		},
	}
	uc := newPostActionUseCase(nil, store, nil, nil, nil, nil, reviewer, PostActionAnalysisConfig{}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		Details:       "用户给出了一条不应纳入长期画像的短期偏好。",
		ProfileNodes: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeUser,
			Content:        "用户今天临时想试试另一套主题。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			Status:         logicdomain.ProfileStatusPending,
		}},
	}
	stats := postActionCompactionStats{}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  43,
		SessionKey: "sess-invalid-profile",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        90,
		SessionID: 43,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 4, 13, 0, 0, 0, time.UTC),
	}, logicdomain.TurnRecord{
		UserContent:      "这条偏好只是今天临时的，不要长期记住。",
		AssistantContent: "我会把它视为短期信息，不放进长期画像。",
	}, analysis, &stats)
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if len(analysis.ProfileNodes) != 1 {
		t.Fatalf("expected invalid profile node to stay in persistence payload, got %+v", analysis.ProfileNodes)
	}
	if analysis.ProfileNodes[0].Status != logicdomain.ProfileStatusInvalid {
		t.Fatalf("expected profile node to be marked invalid, got %+v", analysis.ProfileNodes[0])
	}
	if analysis.UserProfileMerged || strings.TrimSpace(analysis.MergedUserProfile) != "" {
		t.Fatalf("expected invalid profile node to stay out of merged active profile view, got %+v", analysis)
	}
	if stats.ReviewDroppedCount != 0 {
		t.Fatalf("expected invalid profiles to stay persisted instead of counted as dropped, got %+v", stats)
	}
}

// TestPostActionAnalysisLogIncludesCompactionMetrics verifies the redacted analysis log now carries raw/final counts plus the compaction-rate diagnostics needed for rollout monitoring.
// TestPostActionAnalysisLogIncludesCompactionMetrics 用于验证脱敏后的分析日志会携带原始/最终节点数量以及 rollout 监控所需的压缩率诊断字段。
func TestPostActionAnalysisLogIncludesCompactionMetrics(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, nil, nil, PostActionAnalysisConfig{}, logger, false)

	uc.logPostActionAnalysisResult(logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-compaction",
		UserID:     7,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        88,
		SessionID: 41,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
	}, logicdomain.TurnAnalysisInput{
		TargetTurn: logicdomain.TurnAnalysisTargetTurn{TurnID: 88},
	}, logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputMixed,
		TurnID:        88,
		Details:       "这轮最终只留下了一条长期画像。",
		ProfileNodes: []logicdomain.ProfileNodeCandidate{{
			ProfileType: logicdomain.ProfileTypeProject,
			Content:     "当前项目必须复用统一 reviewer。",
			Status:      logicdomain.ProfileStatusActive,
		}},
	}, nil, postActionCompactionStats{
		RawMemoryCandidates:       2,
		RawProfileCandidates:      1,
		FinalMemoryNodes:          0,
		FinalProfileNodes:         1,
		AdmissionDroppedCount:     1,
		ReviewDroppedCount:        1,
		ExternalResearchKeptCount: 1,
	})

	logs := logBuf.String()
	if !strings.Contains(logs, "post-action turn analysis result") {
		t.Fatalf("expected analysis result log, got %s", logs)
	}
	for _, field := range []string{"raw_candidates", "final_stored_nodes", "compaction_rate", "admission_drop_count", "review_drop_count", "external_research_kept_count"} {
		if !strings.Contains(logs, field) {
			t.Fatalf("expected %s in log output, got %s", field, logs)
		}
	}
}

// stubPostActionMemorySearcher records dedupe-search commands and replays one canned grouped result.
// stubPostActionMemorySearcher 用于记录去重检索命令，并回放预设的分组检索结果。
type stubPostActionMemorySearcher struct {
	commands []MemoryQueryCommand
	result   MemoryQueryResult
	err      error
}

// Search captures the command so tests can assert scope and query fan-out decisions.
// Search 用于记录命令，方便测试断言作用域和查询展开策略。
func (s *stubPostActionMemorySearcher) Search(_ context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	s.commands = append(s.commands, cmd)
	if s.err != nil {
		return MemoryQueryResult{}, s.err
	}
	return s.result, nil
}

// stubPostActionCandidateReviewer records unified reviewer inputs and replays one canned joint result.
// stubPostActionCandidateReviewer 用于记录统一 reviewer 输入，并回放预设的联合评审结果。
type stubPostActionCandidateReviewer struct {
	calls  int
	inputs []logicdomain.PostActionCandidateReviewInput
	result logicdomain.PostActionCandidateReviewResult
	err    error
}

// Review captures the joint memory/profile payload so tests can assert one shared LLM review context.
// Review 用于捕获记忆/画像联合载荷，方便测试断言只会使用一个共享的 LLM 评审上下文。
func (s *stubPostActionCandidateReviewer) Review(_ context.Context, input logicdomain.PostActionCandidateReviewInput) (logicdomain.PostActionCandidateReviewResult, error) {
	s.calls++
	copied := logicdomain.PostActionCandidateReviewInput{
		UserInputKind:     input.UserInputKind,
		UserContent:       input.UserContent,
		AssistantContent:  input.AssistantContent,
		MemoryCandidates:  append([]logicdomain.PostActionMemoryReviewCandidate(nil), input.MemoryCandidates...),
		ProfileTargets:    input.ProfileTargets,
		ProfileCandidates: append([]logicdomain.ProfileNodeCandidate(nil), input.ProfileCandidates...),
	}
	s.inputs = append(s.inputs, copied)
	if s.err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, s.err
	}
	return s.result, nil
}
