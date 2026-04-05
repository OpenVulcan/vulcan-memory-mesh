// postaction_candidate_reviewer_test.go verifies the unified post-action reviewer request and response contract.
// postaction_candidate_reviewer_test.go 用于验证统一 post-action reviewer 的请求与响应契约。
package processor

import (
	"context"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestPostActionCandidateReviewerBuildsUnifiedRequest verifies one review call carries memory dedupe evidence and profile candidates together.
// TestPostActionCandidateReviewerBuildsUnifiedRequest 用于验证一次评审调用会同时携带记忆去重证据与画像候选。
func TestPostActionCandidateReviewerBuildsUnifiedRequest(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "memory": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "supersede_memory_ids": [501]
      }
    ],
    "dropped_candidate_indexes": [],
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
		UserInputKind:    logicdomain.TurnAnalysisUserInputMixed,
		UserContent:      "以后主要用 Rust，帮我记住。",
		AssistantContent: "我会把它整理成长期规则。",
		MemoryCandidates: []logicdomain.PostActionMemoryReviewCandidate{{
			CandidateIndex:  0,
			Category:        logicdomain.MemoryNodeCategoryArchitectureDecision,
			Abstract:        "团队默认使用 Rust 作为主要开发语言。",
			Details:         "用户确认团队默认使用 Rust 作为主要开发语言。",
			EvidenceSource:  logicdomain.TurnAnalysisEvidenceSourceUserConfirmed,
			AdmissionReason: "",
			SimilarMemories: []logicdomain.PostActionSimilarMemoryCandidate{{
				MemoryID:     501,
				SourceTurnID: 88,
				ScopeLevel:   "project",
				Category:     logicdomain.MemoryNodeCategoryArchitectureDecision,
				Score:        0.97,
				Origin:       "vector",
				Abstract:     "项目之前提过 Rust。",
				Details:      "项目之前提过 Rust，但还没有形成明确规则。",
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
	if prompts.scene != "review_postaction_candidates" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if !strings.Contains(llm.request.UserPrompt, `"similar_memories"`) || !strings.Contains(llm.request.UserPrompt, `"new_candidates"`) {
		t.Fatalf("expected memory dedupe and profile candidate sections, got %s", llm.request.UserPrompt)
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
}

// TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage verifies every memory candidate must be classified exactly once by the unified reviewer.
// TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage 用于验证统一 reviewer 必须且只能为每条记忆候选给出一次分类结果。
func TestParsePostActionCandidateReviewResponseRejectsMissingMemoryCoverage(t *testing.T) {
	_, err := parsePostActionCandidateReviewResponse(`{
  "memory": {
    "accepted_candidate_indexes": [],
    "dropped_candidate_indexes": [],
    "reason": "遗漏了记忆分类。"
  }
}`, 1, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "must classify all 1 candidates exactly once") {
		t.Fatalf("unexpected error: %v", err)
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
