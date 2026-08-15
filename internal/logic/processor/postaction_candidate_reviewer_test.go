// postaction_candidate_reviewer_test.go verifies the unified post-action reviewer request and response contract.
// postaction_candidate_reviewer_test.go 用于验证统一 post-action reviewer 的请求与响应契约。
package processor

import (
	"context"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
	"github.com/openvulcan/vmm/internal/testutil"
)

// TestPostActionCandidateReviewerBuildsUnifiedRequest verifies one review call carries memory dedupe evidence and profile candidates together.
// TestPostActionCandidateReviewerBuildsUnifiedRequest 用于验证一次评审调用会同时携带记忆去重证据与画像候选。
func TestPostActionCandidateReviewerBuildsUnifiedRequest(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Model:     "provider-l2-model",
			RequestID: "req-l2-review",
			Usage: logicdomain.LLMUsage{
				PromptTokens:      211,
				CompletionTokens:  31,
				TotalTokens:       242,
				CachedInputTokens: 144,
				ReasoningTokens:   0,
			},
			Content: `{
  "memory": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "supersede_memory_ids": [501]
      }
    ],
    "dropped_candidates": [],
    "reason": "新规则不是旧记忆的原样重复。"
  },
  "user": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "用户偏好使用 Rust 作为主要开发语言",
        "priority": "P1",
        "level": "L2",
        "level_reason": "这是稳定开发偏好。",
        "supersede_node_ids": [12]
      }
    ],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "当前候选应进入长期画像。"
  }
}`,
		},
	}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	reviewer := NewPostActionCandidateReviewer(llm, prompts, "qwen-test")

	result, err := reviewer.Review(context.Background(), logicdomain.PostActionCandidateReviewInput{
		UserInputKind:       logicdomain.TurnAnalysisUserInputMixed,
		CurrentTimestamp:    1775000007000,
		CurrentTurnDateTime: "2026-04-04 10:00:00",
		CurrentTurnDate:     "2026-04-04",
		UserContent:         "以后主要用 Rust，帮我记住。",
		AssistantContent:    "我会把它整理成长期规则。",
		MemoryCandidates: []logicdomain.PostActionMemoryReviewCandidate{{
			CandidateIndex:    0,
			CandidateDateTime: "2026-04-04 10:00:00",
			Category:          logicdomain.MemoryNodeCategoryArchitectureDecision,
			Abstract:          "团队默认使用 Rust 作为主要开发语言。",
			Details:           "用户确认团队默认使用 Rust 作为主要开发语言。",
			EvidenceSource:    logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			AdmissionReason:   "",
			SimilarMemories: []logicdomain.PostActionSimilarMemoryCandidate{{
				MemoryID:        501,
				SourceTurnID:    88,
				CreatedDateTime: "2026-04-01 10:00:00",
				ScopeLevel:      "project",
				Category:        logicdomain.MemoryNodeCategoryArchitectureDecision,
				Score:           0.97,
				Origin:          "vector",
				Abstract:        "项目之前提过 Rust。",
				Details:         "项目之前提过 Rust，但还没有形成明确规则。",
			}},
		}},
		ProfileTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{{
				ID:            12,
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "用户之前更偏好 Go。",
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				ProfileDate:   "2026-04-01",
			}},
		},
		ProfileCandidates: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeUser,
			Content:        "我以后主要还是用 Rust。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			ProfileDate:    "2026-04-04",
			SourceTurnID:   101,
		}},
	})
	if err != nil {
		t.Fatalf("review post-action candidates: %v", err)
	}
	if prompts.scene != "postaction_l2_main" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if llm.request.SystemPrompt != "return json only" {
		t.Fatalf("expected runtime to preserve prompt file content without shared injection, got %s", llm.request.SystemPrompt)
	}
	assertStructuredOutputRequest(t, llm.request, "vmm_postaction_candidate_review")
	if !strings.Contains(llm.request.UserPrompt, `"similar_memories"`) || !strings.Contains(llm.request.UserPrompt, `"new_candidates"`) {
		t.Fatalf("expected memory dedupe and profile candidate sections, got %s", llm.request.UserPrompt)
	}
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	for _, fragment := range []string{`"current_datetime":"2026-04-01 07:33:27"`, `"current_turn_datetime":"2026-04-04 10:00:00"`, `"candidate_datetime":"2026-04-04 10:00:00"`, `"created_datetime":"2026-04-01 10:00:00"`} {
		if !strings.Contains(llm.request.UserPrompt, fragment) {
			t.Fatalf("expected request to contain %s, got %s", fragment, llm.request.UserPrompt)
		}
	}
	for _, removed := range []string{`"current_date":`, `"current_turn_date":`, `"candidate_date":`, `"created_date":`} {
		if strings.Contains(llm.request.UserPrompt, removed) {
			t.Fatalf("expected request to stop exposing %s, got %s", removed, llm.request.UserPrompt)
		}
	}
	if result.Memory == nil || len(result.Memory.AcceptedCandidateIndexes) != 1 || result.Memory.AcceptedCandidateIndexes[0] != 0 {
		t.Fatalf("unexpected memory review result: %+v", result)
	}
	if len(result.Memory.AcceptedCandidates) != 1 || len(result.Memory.AcceptedCandidates[0].SupersedeMemoryIDs) != 1 || result.Memory.AcceptedCandidates[0].SupersedeMemoryIDs[0] != 501 {
		t.Fatalf("expected structured supersede ids, got %+v", result.Memory)
	}
	if result.User == nil || len(result.User.AcceptedCandidates) != 1 {
		t.Fatalf("unexpected user review result: %+v", result)
	}
	if result.LLMExecution == nil {
		t.Fatal("expected reviewer result to retain one physical LLM execution")
	}
	if result.LLMExecution.Purpose != "postaction_l2_main" || result.LLMExecution.ConfiguredModel != "qwen-test" || result.LLMExecution.ResponseModel != "provider-l2-model" || result.LLMExecution.RequestID != "req-l2-review" {
		t.Fatalf("unexpected reviewer LLM identity: %+v", result.LLMExecution)
	}
	if result.LLMExecution.Usage.PromptTokens != 211 || result.LLMExecution.Usage.CompletionTokens != 31 || result.LLMExecution.Usage.TotalTokens != 242 || result.LLMExecution.Usage.CachedInputTokens != 144 || result.LLMExecution.Usage.ReasoningTokens != 0 {
		t.Fatalf("unexpected reviewer LLM usage: %+v", result.LLMExecution.Usage)
	}
}

// TestRenderPostActionCandidateReviewRequestKeepsMemoryMetadataFields verifies the L2 memory-candidate input shape keeps prompt-declared fields even when their values are empty.
// TestRenderPostActionCandidateReviewRequestKeepsMemoryMetadataFields 用于验证 L2 记忆候选输入形状会保留提示词声明的字段，即使字段值为空。
func TestRenderPostActionCandidateReviewRequestKeepsMemoryMetadataFields(t *testing.T) {
	rendered, memoryCount, userCount, projectCount, err := renderPostActionCandidateReviewRequest(logicdomain.PostActionCandidateReviewInput{
		MemoryCandidates: []logicdomain.PostActionMemoryReviewCandidate{{
			CandidateIndex: 0,
			Category:       logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:       "当前项目要求保持 L2 输入契约稳定。",
			Details:        "当前项目要求保持 L2 输入契约稳定。",
		}},
	})
	if err != nil {
		t.Fatalf("render post-action candidate review request: %v", err)
	}
	if memoryCount != 1 || userCount != 0 || projectCount != 0 {
		t.Fatalf("unexpected rendered counts: memory=%d user=%d project=%d", memoryCount, userCount, projectCount)
	}
	for _, fragment := range []string{`"candidate_datetime":""`, `"evidence_source":""`, `"admission_reason":""`, `"similar_memories":[]`} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("expected rendered request to keep %s, got %s", fragment, rendered)
		}
	}
}

// TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage verifies every memory candidate must be classified exactly once by the unified reviewer.
// TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage 用于验证统一 reviewer 必须且只能为每条记忆候选给出一次分类结果。
func TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage(t *testing.T) {
	_, err := parsePostActionCandidateReviewResponse(`{
  "memory": {
    "accepted_candidates": [],
    "dropped_candidates": [],
    "reason": "遗漏了记忆分类。"
  }
}`, 1, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "must classify all 1 candidates exactly once") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParsePostActionCandidateReviewResponseRejectsLegacyMemoryIndexLists verifies historical index-only arrays no longer satisfy the structured L2 output contract.
// TestParsePostActionCandidateReviewResponseRejectsLegacyMemoryIndexLists 用于验证历史纯索引数组不再满足结构化 L2 输出契约。
func TestParsePostActionCandidateReviewResponseRejectsLegacyMemoryIndexLists(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		expectedCount int
	}{
		{
			name: "index-only payload",
			raw: `{
  "memory": {
    "accepted_candidate_indexes": [0],
    "dropped_candidate_indexes": [1],
    "reason": "旧格式覆盖了所有候选。"
  }
}`,
			expectedCount: 2,
		},
		{
			name: "structured payload with empty legacy arrays",
			raw: `{
  "memory": {
    "accepted_candidates": [{"candidate_index": 0, "supersede_memory_ids": []}],
    "dropped_candidates": [],
    "accepted_candidate_indexes": [],
    "dropped_candidate_indexes": [],
    "reason": "结构化结果不应夹带旧字段。"
  }
}`,
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePostActionCandidateReviewResponse(tt.raw, tt.expectedCount, 0, 0)
			if err == nil || !strings.Contains(err.Error(), "json decode failed") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestParsePostActionCandidateReviewResponseParsesDroppedMemoryDedupeTarget verifies the structured dropped memory payload preserves the explicit dedupe target needed by direct-write callers.
// TestParsePostActionCandidateReviewResponseParsesDroppedMemoryDedupeTarget 用于验证结构化 dropped memory 结果会保留 direct-write 所需的显式 dedupe 目标。
func TestParsePostActionCandidateReviewResponseParsesDroppedMemoryDedupeTarget(t *testing.T) {
	result, err := parsePostActionCandidateReviewResponse(`{
  "memory": {
    "accepted_candidates": [],
    "dropped_candidates": [
      {
        "candidate_index": 0,
        "dedupe_memory_id": 601
      }
    ],
    "reason": "旧记忆已经足够完整。"
  }
}`, 1, 0, 0)
	if err != nil {
		t.Fatalf("parse review response: %v", err)
	}
	if result.Memory == nil || len(result.Memory.DroppedCandidates) != 1 {
		t.Fatalf("expected one structured dropped candidate, got %+v", result.Memory)
	}
	if result.Memory.DroppedCandidates[0].CandidateIndex != 0 || result.Memory.DroppedCandidates[0].DedupeMemoryID != 601 {
		t.Fatalf("unexpected structured dropped candidate: %+v", result.Memory.DroppedCandidates[0])
	}
	if len(result.Memory.DroppedCandidateIndexes) != 1 || result.Memory.DroppedCandidateIndexes[0] != 0 {
		t.Fatalf("expected dropped indexes to stay derived from structured payload, got %+v", result.Memory.DroppedCandidateIndexes)
	}
}

// TestParsePostActionCandidateReviewResponseRejectsDuplicateProfileAcceptedCandidate verifies profile review output cannot merge repeated accepted candidate indexes.
// TestParsePostActionCandidateReviewResponseRejectsDuplicateProfileAcceptedCandidate 用于验证画像评审输出不能合并重复的 accepted candidate index。
func TestParsePostActionCandidateReviewResponseRejectsDuplicateProfileAcceptedCandidate(t *testing.T) {
	_, err := parsePostActionCandidateReviewResponse(`{
  "user": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "用户偏好本地部署。",
        "priority": "P1",
        "level": "L2",
        "level_reason": "稳定偏好。",
        "supersede_node_ids": []
      },
      {
        "candidate_index": 0,
        "normalized_content": "用户偏好离线部署。",
        "priority": "P1",
        "level": "L2",
        "level_reason": "重复候选。",
        "supersede_node_ids": []
      }
    ],
    "invalid_candidate_indexes": [1],
    "retire_only_node_ids": [],
    "reason": "重复 accepted index 应失败。"
  }
}`, 0, 2, 0)
	if err == nil || !strings.Contains(err.Error(), "duplicate candidate_index 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParsePostActionCandidateReviewResponseRejectsInvalidReviewerIDLists verifies reviewer id lists cannot hide zero or duplicate ids before use-case ownership checks.
// TestParsePostActionCandidateReviewResponseRejectsInvalidReviewerIDLists 用于验证 reviewer id 列表不能在用例归属校验前隐藏零值或重复 id。
func TestParsePostActionCandidateReviewResponseRejectsInvalidReviewerIDLists(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		memoryCount int
		userCount   int
		want        string
	}{
		{
			name: "memory supersede zero id",
			raw: `{
  "memory": {
    "accepted_candidates": [{"candidate_index": 0, "supersede_memory_ids": [0]}],
    "dropped_candidates": [],
    "reason": "zero id should fail"
  }
}`,
			memoryCount: 1,
			want:        "supersede_memory_ids contains zero id",
		},
		{
			name: "memory supersede duplicate id",
			raw: `{
  "memory": {
    "accepted_candidates": [{"candidate_index": 0, "supersede_memory_ids": [31, 31]}],
    "dropped_candidates": [],
    "reason": "duplicate id should fail"
  }
}`,
			memoryCount: 1,
			want:        "supersede_memory_ids contains duplicate id 31",
		},
		{
			name: "profile supersede zero id",
			raw: `{
  "user": {
    "accepted_candidates": [{
      "candidate_index": 0,
      "normalized_content": "用户偏好本地部署。",
      "priority": "P1",
      "level": "L2",
      "level_reason": "稳定偏好。",
      "supersede_node_ids": [0]
    }],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "zero profile id should fail"
  }
}`,
			userCount: 1,
			want:      "supersede_node_ids contains zero id",
		},
		{
			name: "profile retire duplicate id",
			raw: `{
  "user": {
    "accepted_candidates": [],
    "invalid_candidate_indexes": [0],
    "retire_only_node_ids": [12, 12],
    "reason": "duplicate retire id should fail"
  }
}`,
			userCount: 1,
			want:      "retire_only_node_ids contains duplicate id 12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePostActionCandidateReviewResponse(tt.raw, tt.memoryCount, tt.userCount, 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestPostActionCandidateReviewerAllowsProfileOnlyReview verifies the unified reviewer can handle a profile-only batch without requiring a memory block in the response.
// TestPostActionCandidateReviewerAllowsProfileOnlyReview 用于验证统一 reviewer 在只有画像候选时，不会强制要求返回 memory 结果块。
func TestPostActionCandidateReviewerAllowsProfileOnlyReview(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "project": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "当前项目必须复用统一 reviewer 完成记忆与画像准入判断",
        "priority": "P0",
        "level": "L3",
        "level_reason": "这是长期工程约束。",
        "supersede_node_ids": []
      }
    ],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "这是明确的长期项目规则。"
  }
}`,
		},
	}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	reviewer := NewPostActionCandidateReviewer(llm, prompts, "qwen-test")

	result, err := reviewer.Review(context.Background(), logicdomain.PostActionCandidateReviewInput{
		UserInputKind: logicdomain.TurnAnalysisUserInputStatement,
		UserContent:   "项目以后统一用一个 reviewer 做判断。",
		ProfileCandidates: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeProject,
			Content:        "项目以后统一用一个 reviewer 做判断。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			ProfileDate:    "2026-04-04",
			SourceTurnID:   102,
		}},
	})
	if err != nil {
		t.Fatalf("review profile-only post-action candidates: %v", err)
	}
	if strings.Contains(llm.request.UserPrompt, `"memory"`) {
		t.Fatalf("did not expect memory block in profile-only request, got %s", llm.request.UserPrompt)
	}
	if result.Memory != nil {
		t.Fatalf("expected no memory review section, got %+v", result)
	}
	if result.Project == nil || len(result.Project.AcceptedCandidates) != 1 {
		t.Fatalf("unexpected project review result: %+v", result)
	}
}

// TestPostActionCandidateReviewerUsesLocalFallbackDateForActiveNodes verifies active profile-node fallback dates in the unified reviewer request follow the shared local calendar-day contract.
// TestPostActionCandidateReviewerUsesLocalFallbackDateForActiveNodes 用于验证统一 reviewer 请求里的活跃画像节点 fallback 日期会遵循共享本地自然日契约。
func TestPostActionCandidateReviewerUsesLocalFallbackDateForActiveNodes(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "user": {
    "accepted_candidates": [],
    "invalid_candidate_indexes": [0],
    "retire_only_node_ids": [],
    "reason": "no-op"
  }
}`,
		},
	}
	reviewer := NewPostActionCandidateReviewer(llm, &stubProfilePromptSource{prompt: "return json only"}, "qwen-test")

	_, err := reviewer.Review(context.Background(), logicdomain.PostActionCandidateReviewInput{
		CurrentTimestamp: 1775000007000,
		ProfileTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{{
				ID:            12,
				ProfileType:   logicdomain.ProfileTypeUser,
				Content:       "用户之前偏好本地部署。",
				Priority:      logicdomain.ProfilePriorityP1,
				ProfileLevel:  logicdomain.ProfileLevelStable,
				RefreshWeight: 2,
				CreatedAt:     time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
			}},
		},
		ProfileCandidates: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeUser,
			Content:        "以后继续本地部署。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			ProfileDate:    "2026-04-04",
			SourceTurnID:   102,
		}},
	})
	if err != nil {
		t.Fatalf("review post-action candidates: %v", err)
	}
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	if !strings.Contains(llm.request.UserPrompt, `"latest_active_datetime":"2026-04-02 00:30:00"`) {
		t.Fatalf("expected latest_active_datetime to use local datetime anchor, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-02 00:30:00"`) {
		t.Fatalf("expected active-node fallback datetime to use local datetime anchor, got %s", llm.request.UserPrompt)
	}
}

// TestPostActionCandidateReviewerCorrectsLegacyUTCProfileDate verifies unified reviewer requests repair stored UTC-derived profile_date values before asking the LLM to reason about freshness and replacement.
// TestPostActionCandidateReviewerCorrectsLegacyUTCProfileDate 用于验证统一 reviewer 会先修正历史上按 UTC 推导的 profile_date，再让 LLM 基于正确自然日判断新旧与替换关系。
func TestPostActionCandidateReviewerCorrectsLegacyUTCProfileDate(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "user": {
    "accepted_candidates": [],
    "invalid_candidate_indexes": [0],
    "retire_only_node_ids": [],
    "reason": "no-op"
  }
}`,
		},
	}
	reviewer := NewPostActionCandidateReviewer(llm, &stubProfilePromptSource{prompt: "return json only"}, "qwen-test")

	_, err := reviewer.Review(context.Background(), logicdomain.PostActionCandidateReviewInput{
		CurrentTimestamp: 1775000007000,
		ProfileTargets: logicdomain.ProfileReviewTargetsSnapshot{
			UserNodes: []logicdomain.ProfileActiveNodeRecord{{
				ID:                  32,
				ProfileType:         logicdomain.ProfileTypeUser,
				Content:             "用户长期偏好本地部署。",
				Priority:            logicdomain.ProfilePriorityP1,
				ProfileLevel:        logicdomain.ProfileLevelStable,
				RefreshWeight:       3,
				ProfileDate:         "2026-04-01",
				ProfileDateAnchorAt: time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
				CreatedAt:           time.Date(2026, 4, 2, 3, 0, 0, 0, time.UTC),
			}},
		},
		ProfileCandidates: []logicdomain.ProfileNodeCandidate{{
			ProfileType:    logicdomain.ProfileTypeUser,
			Content:        "以后继续保留本地部署。",
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceUserAsserted,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
			ProfileDate:    "2026-04-04",
			SourceTurnID:   103,
		}},
	})
	if err != nil {
		t.Fatalf("review post-action candidates: %v", err)
	}
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	if strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-01`) {
		t.Fatalf("expected legacy UTC-derived datetime to be corrected, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-02 00:30:00"`) {
		t.Fatalf("expected corrected local datetime in reviewer request, got %s", llm.request.UserPrompt)
	}
}
