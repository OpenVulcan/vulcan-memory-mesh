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
	"github.com/openvulcan/vmm/internal/testutil"
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

// TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredMemoryReplaceScope verifies post-action duplicate recall uses the dedicated memory-replace scope policy instead of reusing pre-check defaults.
// TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredMemoryReplaceScope 用于验证 post-action 的重复召回会使用独立的记忆更替作用域策略，而不是继续复用 pre-check 默认值。
func TestPostActionUseCaseReviewTurnCandidatesUsesConfiguredMemoryReplaceScope(t *testing.T) {
	tests := []struct {
		name          string
		rawScope      string
		wantScope     string
		wantSessionID uint64
	}{
		{name: "default-project", rawScope: "", wantScope: memoryReplaceScopeProject},
		{name: "project", rawScope: memoryReplaceScopeProject, wantScope: memoryReplaceScopeProject},
		{name: "team", rawScope: memoryReplaceScopeTeam, wantScope: memoryReplaceScopeTeam},
		{name: "session", rawScope: memoryReplaceScopeSession, wantScope: memoryReplaceScopeSession, wantSessionID: 41},
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
						AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
							CandidateIndex: 0,
						}},
						AcceptedCandidateIndexes: []int{0},
						DroppedCandidateIndexes:  nil,
						Reason:                   "保留唯一候选。",
					},
				},
			}
			uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
				DedupeSearchTopK:          7,
				MemoryReplaceScope:        tt.rawScope,
				DedupeMinSimilarity:       0.90,
				HardDedupeCosineThreshold: 0,
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
			if searcher.commands[0].SessionID != tt.wantSessionID {
				t.Fatalf("expected replace session id %d, got %+v", tt.wantSessionID, searcher.commands[0])
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
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
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
						CreatedAt:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
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
				AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
					CandidateIndex:     0,
					SupersedeMemoryIDs: []uint64{501},
				}},
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
		DedupeSearchTopK:          5,
		MemoryReplaceScope:        memoryReplaceScopeTeam,
		DedupeMinSimilarity:       0.90,
		HardDedupeCosineThreshold: 0,
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
	if reviewer.inputs[0].CurrentTurnDate != "2026-04-04" {
		t.Fatalf("expected reviewer to receive current turn date, got %+v", reviewer.inputs[0].CurrentTurnDate)
	}
	if reviewer.inputs[0].CurrentTurnDateTime == "" {
		t.Fatalf("expected reviewer to receive current turn datetime, got %+v", reviewer.inputs[0].CurrentTurnDateTime)
	}
	if reviewer.inputs[0].CurrentTimestamp <= 0 {
		t.Fatalf("expected reviewer to receive current timestamp, got %+v", reviewer.inputs[0])
	}
	if reviewer.inputs[0].MemoryCandidates[0].CandidateDate != "2026-04-04" {
		t.Fatalf("expected reviewer to receive candidate date, got %+v", reviewer.inputs[0].MemoryCandidates[0])
	}
	if reviewer.inputs[0].MemoryCandidates[0].CandidateDateTime == "" {
		t.Fatalf("expected reviewer to receive candidate datetime, got %+v", reviewer.inputs[0].MemoryCandidates[0])
	}
	if len(reviewer.inputs[0].MemoryCandidates[0].SimilarMemories) != 1 {
		t.Fatalf("expected only one high-similarity memory to survive thresholding, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories)
	}
	if reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0].ScopeLevel != logicdomain.MemoryScopeLevelLabel(logicdomain.MemoryScopeLevelProject) {
		t.Fatalf("expected similar memory scope label to be normalized, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0])
	}
	if reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0].CreatedDate != "2026-04-01" {
		t.Fatalf("expected reviewer to receive similar memory created date, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0])
	}
	if reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0].CreatedDateTime == "" {
		t.Fatalf("expected reviewer to receive similar memory created datetime, got %+v", reviewer.inputs[0].MemoryCandidates[0].SimilarMemories[0])
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected memory node to survive unified review, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.MemoryNodes[0].SupersedeMemoryIDs) != 1 || analysis.MemoryNodes[0].SupersedeMemoryIDs[0] != 501 {
		t.Fatalf("expected accepted memory node to retain reviewer-approved supersede id, got %+v", analysis.MemoryNodes[0].SupersedeMemoryIDs)
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

// TestPostActionUseCaseReviewTurnCandidatesRejectsUnavailableSupersedeIDs verifies reviewer-produced supersede ids must belong to the candidate-local similar-memory set; otherwise the use case degrades instead of silently superseding unrelated rows.
// TestPostActionUseCaseReviewTurnCandidatesRejectsUnavailableSupersedeIDs 用于验证 reviewer 产生的 supersede id 必须来自该候选自己的 similar memory 集合；否则用例会判定输出无效并走降级，而不会静默替换无关行。
func TestPostActionUseCaseReviewTurnCandidatesRejectsUnavailableSupersedeIDs(t *testing.T) {
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex: 0,
				Query:      "新的长期事实",
				Hits: []MemoryQueryHit{{
					MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 9001},
					SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 701},
					ScopeLevel:     logicdomain.MemoryScopeLevelProject,
					Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
					Score:          0.96,
					Origin:         "vector",
					Abstract:       "已有事实",
					DetailsPreview: "已有事实详情",
				}},
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
					CandidateIndex:     0,
					SupersedeMemoryIDs: []uint64{9999},
				}},
				AcceptedCandidateIndexes: []int{0},
				Reason:                   "错误地指向了未出现在 similar_memories 里的旧记忆。",
			},
		},
	}
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
		DedupeSearchTopK:          5,
		MemoryReplaceScope:        memoryReplaceScopeProject,
		DedupeMinSimilarity:       0.90,
		HardDedupeCosineThreshold: 0,
	}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
			Abstract:       "新的长期事实",
			Details:        "新的长期事实详情",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-invalid-supersede",
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
		UserContent:      "新的长期事实",
		AssistantContent: "我会更新长期记忆。",
	}, analysis, &postActionCompactionStats{})
	if err == nil || !strings.Contains(err.Error(), "references unavailable supersede_memory_id") {
		t.Fatalf("expected invalid supersede id error, got %v", err)
	}
}

// TestPostActionUseCaseReviewTurnCandidatesClearsAnalyzerSupersedeWhenAllMemoryCandidatesDrop verifies reviewer-side full rejection clears analyzer-origin supersede ids so old memories are not retired without any accepted replacement.
// TestPostActionUseCaseReviewTurnCandidatesClearsAnalyzerSupersedeWhenAllMemoryCandidatesDrop 用于验证当 reviewer 把记忆候选全部拒绝时，会清空分析器给出的 supersede id，避免在没有任何接纳替代物的情况下退役旧记忆。
func TestPostActionUseCaseReviewTurnCandidatesClearsAnalyzerSupersedeWhenAllMemoryCandidatesDrop(t *testing.T) {
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex: 0,
				Query:      "这条候选最终会被 reviewer 丢弃。",
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates:       nil,
				AcceptedCandidateIndexes: nil,
				DroppedCandidateIndexes:  []int{0},
				Reason:                   "这条候选不应进入长期记忆。",
			},
		},
	}
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
			Abstract:       "这条候选最终会被 reviewer 丢弃。",
			Details:        "这条候选最终会被 reviewer 丢弃。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-drop-all-memory",
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
		UserContent:      "这条候选不要进长期记忆。",
		AssistantContent: "我不会持久化它。",
	}, analysis, &postActionCompactionStats{})
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if len(analysis.MemoryNodes) != 0 {
		t.Fatalf("expected reviewer to drop all memory nodes, got %+v", analysis.MemoryNodes)
	}
}

// TestApplyPostActionAdmissionFilterPreservesSurvivingCandidateSupersedes verifies first-pass admission drops only affect the rejected nodes, while surviving candidate-local supersede mappings remain untouched for later persistence.
// TestApplyPostActionAdmissionFilterPreservesSurvivingCandidateSupersedes 用于验证首轮准入只会移除被拒绝节点，存活候选上的 supersede 映射会保持不变，供后续持久化继续使用。
func TestApplyPostActionAdmissionFilterPreservesSurvivingCandidateSupersedes(t *testing.T) {
	analysis := logicdomain.TurnAnalysis{
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:        logicdomain.MemoryNodeCategoryRequirementTODO,
				Abstract:        "只是回显旧问题",
				Details:         "只是回显旧问题",
				EvidenceSource:  logicdomain.TurnAnalysisEvidenceSourceAssistantRecalledMemory,
				Admission:       logicdomain.TurnAnalysisAdmissionDrop,
				AdmissionReason: logicdomain.TurnAnalysisAdmissionReasonQAAnswerOnly,
			},
			{
				Category:           logicdomain.MemoryNodeCategoryTechSpecAPI,
				Abstract:           "当前项目阶段已经进入 B。",
				Details:            "当前项目阶段已经进入 B。",
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{701},
			},
		},
		ProfileNodes: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeProject,
			Content:        "当前项目进入阶段 B。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}

	applyPostActionAdmissionFilter(&analysis, &postActionCompactionStats{})

	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected one surviving memory node, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.MemoryNodes[0].SupersedeMemoryIDs) != 1 || analysis.MemoryNodes[0].SupersedeMemoryIDs[0] != 701 {
		t.Fatalf("expected surviving candidate-local supersede ids to remain intact, got %+v", analysis.MemoryNodes[0].SupersedeMemoryIDs)
	}
}

// TestPostActionUseCaseReviewTurnCandidatesKeepsAcceptedCandidateLocalSupersedes verifies per-candidate supersede ids survive reviewer partial acceptance while dropped candidates stop contributing stale analyzer supersede ids.
// TestPostActionUseCaseReviewTurnCandidatesKeepsAcceptedCandidateLocalSupersedes 用于验证当 reviewer 只接纳部分候选时，会只保留被接纳候选自己的 supersede id，而被丢弃候选不再贡献陈旧分析器 supersede 结果。
func TestPostActionUseCaseReviewTurnCandidatesKeepsAcceptedCandidateLocalSupersedes(t *testing.T) {
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{QueryIndex: 0, Query: "当前项目阶段已经切换到 B。"},
				{QueryIndex: 1, Query: "当前项目阶段仍然处于 A。"},
			},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidates: []logicdomain.PostActionAcceptedMemoryCandidate{{
					CandidateIndex: 0,
				}},
				AcceptedCandidateIndexes: []int{0},
				DroppedCandidateIndexes:  []int{1},
				Reason:                   "只保留真正更新后的阶段事实。",
			},
		},
	}
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		MemoryNodes: []logicdomain.MemoryNodeCandidate{
			{
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "当前项目阶段已经切换到 B。",
				Details:            "当前项目阶段已经切换到 B。",
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{701},
			},
			{
				Category:           logicdomain.MemoryNodeCategoryProjectContext,
				Abstract:           "当前项目阶段仍然处于 A。",
				Details:            "当前项目阶段仍然处于 A。",
				EvidenceSource:     logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
				Admission:          logicdomain.TurnAnalysisAdmissionKeep,
				SupersedeMemoryIDs: []uint64{702},
			},
		},
	}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  51,
		SessionKey: "sess-candidate-local-supersede",
		UserID:     7,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        91,
		SessionID: 51,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}, logicdomain.TurnRecord{
		UserContent:      "现在阶段已经变成 B。",
		AssistantContent: "我会把旧阶段替换掉。",
	}, analysis, &postActionCompactionStats{})
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected only accepted memory node to survive, got %+v", analysis.MemoryNodes)
	}
	if len(analysis.MemoryNodes[0].SupersedeMemoryIDs) != 1 || analysis.MemoryNodes[0].SupersedeMemoryIDs[0] != 701 {
		t.Fatalf("expected only accepted candidate-local supersede id to remain, got %+v", analysis.MemoryNodes[0].SupersedeMemoryIDs)
	}
}

// TestBuildScopedMemoryReviewCandidatesUsesQueryIndexMapping verifies similar-memory hits are reattached by QueryIndex instead of raw slice position so out-of-order grouped results cannot bind to the wrong candidate.
// TestBuildScopedMemoryReviewCandidatesUsesQueryIndexMapping 用于验证 similar-memory 结果会按 QueryIndex 回贴，而不是按切片顺序绑定，避免乱序分组把旧记忆挂错到别的候选上。
func TestBuildScopedMemoryReviewCandidatesUsesQueryIndexMapping(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{
				{
					QueryIndex: 1,
					Query:      "候选 B",
					Hits: []MemoryQueryHit{{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 802},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 302},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.97,
						Origin:         "vector",
						Abstract:       "旧阶段 B",
						DetailsPreview: "旧阶段 B 详情",
						CreatedAt:      time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC),
					}},
				},
				{
					QueryIndex: 0,
					Query:      "候选 A",
					Hits: []MemoryQueryHit{{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 801},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 301},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.96,
						Origin:         "vector",
						Abstract:       "旧阶段 A",
						DetailsPreview: "旧阶段 A 详情",
						CreatedAt:      time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC),
					}},
				},
			},
		},
	}

	buildResult, err := buildScopedMemoryReviewCandidates(context.Background(), searcher, logicdomain.SessionRef{
		SessionID: 61,
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	}, []logicdomain.MemoryNodeCandidate{
		{
			Category:       logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:       "候选 A",
			Details:        "候选 A 详情",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		},
		{
			Category:       logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:       "候选 B",
			Details:        "候选 B 详情",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		},
	}, 5, memoryReplaceScopeProject, 0.90, 0)
	if err != nil {
		t.Fatalf("build scoped memory review candidates: %v", err)
	}
	candidates := buildResult.Candidates
	if len(candidates) != 2 {
		t.Fatalf("expected two candidates, got %+v", candidates)
	}
	if len(candidates[0].SimilarMemories) != 1 || candidates[0].SimilarMemories[0].MemoryID != 801 {
		t.Fatalf("expected query-index 0 result to attach to candidate 0, got %+v", candidates[0].SimilarMemories)
	}
	if candidates[0].SimilarMemories[0].CreatedDate != "2026-04-01" {
		t.Fatalf("expected candidate 0 similar memory date to be normalized, got %+v", candidates[0].SimilarMemories[0])
	}
	if len(candidates[1].SimilarMemories) != 1 || candidates[1].SimilarMemories[0].MemoryID != 802 {
		t.Fatalf("expected query-index 1 result to attach to candidate 1, got %+v", candidates[1].SimilarMemories)
	}
	if candidates[1].SimilarMemories[0].CreatedDate != "2026-04-02" {
		t.Fatalf("expected candidate 1 similar memory date to be normalized, got %+v", candidates[1].SimilarMemories[0])
	}
}

// TestPostActionUseCaseReviewTurnCandidatesHardDedupeSkipsReviewer verifies hard dedupe scans its dedicated pre-MMR pool instead of the reviewer-visible top-k hits, so an obvious duplicate can still be dropped even after diversity trimming.
// TestPostActionUseCaseReviewTurnCandidatesHardDedupeSkipsReviewer 用于验证硬排重会扫描专用的 MMR 前候选池，而不是只看 reviewer 可见的 top-k 命中；因此即使多样性裁剪后不再可见，明显重复项仍可被直接丢弃。
func TestPostActionUseCaseReviewTurnCandidatesHardDedupeSkipsReviewer(t *testing.T) {
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex:  0,
				Query:       "候选会与第二条旧记忆几乎完全重合",
				QueryVector: []float32{0, 1},
				Hits: []MemoryQueryHit{
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 901},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 301},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.99,
						Origin:         "vector_mmr",
						Abstract:       "排序第一但向量并不等价",
						DetailsPreview: "这条命中在最终排序里第一，但真实向量并不接近。",
						Vector:         []float32{1, 0},
					},
				},
				HardDedupeHits: []MemoryQueryHit{
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 901},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 301},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.99,
						Origin:         "vector_mmr",
						Abstract:       "排序第一但向量并不等价",
						DetailsPreview: "这条命中在最终排序里第一，但真实向量并不接近。",
						Vector:         []float32{1, 0},
					},
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 902},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 302},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.94,
						Origin:         "vector_pre_mmr",
						Abstract:       "真正重复的旧记忆",
						DetailsPreview: "这条命中虽然不在 reviewer 看到的结果里，但真实向量几乎完全一致。",
						Vector:         []float32{0, 1},
					},
				},
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
			},
		},
	}
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
		DedupeSearchTopK:          5,
		MemoryReplaceScope:        memoryReplaceScopeProject,
		DedupeMinSimilarity:       0.80,
		HardDedupeCosineThreshold: 0.99,
	}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Category:       logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:       "候选会与第二条旧记忆几乎完全重合",
			Details:        "候选会与第二条旧记忆几乎完全重合，应该在 reviewer 前就直接 dedupe。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}
	stats := postActionCompactionStats{}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  61,
		SessionKey: "sess-hard-dedupe",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        99,
		SessionID: 61,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC),
	}, logicdomain.TurnRecord{
		UserContent:      "把这条项目背景记下来",
		AssistantContent: "我先尝试去重。",
	}, analysis, &stats)
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("expected hard dedupe to skip reviewer, got %d calls", reviewer.calls)
	}
	if len(analysis.MemoryNodes) != 0 {
		t.Fatalf("expected hard dedupe to drop duplicate memory node, got %+v", analysis.MemoryNodes)
	}
	if stats.ReviewDroppedCount != 1 {
		t.Fatalf("expected one review-stage drop from hard dedupe, got %+v", stats)
	}
	if stats.HardDedupeDroppedCount != 1 {
		t.Fatalf("expected one hard-dedupe drop, got %+v", stats)
	}
}

// TestPostActionUseCaseReviewTurnCandidatesHardDedupeRequiresMatchingCategory verifies reviewer-front hard dedupe stays conservative by refusing to auto-drop a memory when only a different-category hit crosses the cosine threshold.
// TestPostActionUseCaseReviewTurnCandidatesHardDedupeRequiresMatchingCategory 用于验证 reviewer 前硬排重会保持保守：如果只有不同 Category 的命中跨过余弦阈值，也不会自动丢弃记忆。
func TestPostActionUseCaseReviewTurnCandidatesHardDedupeRequiresMatchingCategory(t *testing.T) {
	searcher := &stubPostActionMemorySearcher{
		result: MemoryQueryResult{
			Results: []MemoryQueryGroupResult{{
				QueryIndex:  0,
				Query:       "记录新的发布约束",
				QueryVector: []float32{0, 1},
				Hits: []MemoryQueryHit{
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 910},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 401},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.93,
						Origin:         "vector_mmr",
						Abstract:       "已有项目背景",
						DetailsPreview: "已有项目背景详情",
						Vector:         []float32{1, 0},
					},
				},
				HardDedupeHits: []MemoryQueryHit{
					{
						MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 911},
						SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 402},
						ScopeLevel:     logicdomain.MemoryScopeLevelProject,
						Category:       logicdomain.MemoryNodeCategoryProjectContext,
						Score:          0.91,
						Origin:         "vector_pre_mmr",
						Abstract:       "不同类目的旧记忆",
						DetailsPreview: "不同类目的旧记忆虽然向量极近，但不应被自动复用。",
						Vector:         []float32{0, 1},
					},
				},
			}},
		},
	}
	reviewer := &stubPostActionCandidateReviewer{
		result: logicdomain.PostActionCandidateReviewResult{
			Memory: &logicdomain.PostActionMemoryReviewSection{
				AcceptedCandidateIndexes: []int{0},
			},
		},
	}
	uc := newPostActionUseCase(nil, nil, nil, nil, nil, searcher, reviewer, PostActionAnalysisConfig{
		DedupeSearchTopK:          5,
		MemoryReplaceScope:        memoryReplaceScopeProject,
		DedupeMinSimilarity:       0.80,
		HardDedupeCosineThreshold: 0.99,
	}, nil, false)
	analysis := &logicdomain.TurnAnalysis{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		MemoryNodes: []logicdomain.MemoryNodeCandidate{{
			Category:       logicdomain.MemoryNodeCategoryRequirementTODO,
			Abstract:       "上线前必须跑 smoke test",
			Details:        "这是新的工程约束，不应该被项目背景自动吞掉。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		}},
	}
	stats := postActionCompactionStats{}

	err := uc.reviewTurnCandidates(context.Background(), logicdomain.SessionRef{
		SessionID:  62,
		SessionKey: "sess-hard-dedupe-category",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.PersistedTurnRecord{
		ID:        100,
		SessionID: 62,
		ProjectID: 9,
		CreatedAt: time.Date(2026, 4, 8, 8, 5, 0, 0, time.UTC),
	}, logicdomain.TurnRecord{
		UserContent:      "记下发布前 smoke test 约束",
		AssistantContent: "我会先看是否和旧记忆重复。",
	}, analysis, &stats)
	if err != nil {
		t.Fatalf("review turn candidates: %v", err)
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected reviewer to run when categories differ, got %d calls", reviewer.calls)
	}
	if len(analysis.MemoryNodes) != 1 {
		t.Fatalf("expected candidate to survive hard dedupe and reach reviewer, got %+v", analysis.MemoryNodes)
	}
	if stats.HardDedupeDroppedCount != 0 {
		t.Fatalf("expected no hard-dedupe drop when categories differ, got %+v", stats)
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
		HardDedupeDroppedCount:    1,
		ExternalResearchKeptCount: 1,
	})

	logs := logBuf.String()
	if !strings.Contains(logs, "post-action turn analysis result") {
		t.Fatalf("expected analysis result log, got %s", logs)
	}
	for _, field := range []string{"raw_candidates", "final_stored_nodes", "compaction_rate", "admission_drop_count", "review_drop_count", "hard_dedupe_drop_count", "external_research_kept_count"} {
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
		UserInputKind:       input.UserInputKind,
		CurrentTimestamp:    input.CurrentTimestamp,
		CurrentTurnDateTime: input.CurrentTurnDateTime,
		CurrentTurnDate:     input.CurrentTurnDate,
		UserContent:         input.UserContent,
		AssistantContent:    input.AssistantContent,
		MemoryCandidates:    append([]logicdomain.PostActionMemoryReviewCandidate(nil), input.MemoryCandidates...),
		ProfileTargets:      input.ProfileTargets,
		ProfileCandidates:   append([]logicdomain.ProfileNodeCandidate(nil), input.ProfileCandidates...),
	}
	s.inputs = append(s.inputs, copied)
	if s.err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, s.err
	}
	return s.result, nil
}
