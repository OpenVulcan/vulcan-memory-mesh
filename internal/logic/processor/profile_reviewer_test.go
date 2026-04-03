// profile_reviewer_test.go verifies the atomic profile-node reviewer against the batched JSON contract.
// profile_reviewer_test.go 用于围绕批量 JSON 契约验证原子化画像节点评审器。
package processor

import (
	"context"
	"strings"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestProfileReviewerBuildsAtomicRequest verifies one review call carries active nodes and fresh candidates instead of one legacy profile blob.
// TestProfileReviewerBuildsAtomicRequest 用于验证一次评审调用会携带活跃节点和新候选，而不是旧式画像 Blob。
func TestProfileReviewerBuildsAtomicRequest(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: appports.LLMResponse{
			Content: `{
  "user": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "偏好使用 Rust 作为主要开发语言",
        "priority": "P1",
        "level": "L2",
        "level_reason": "这是稳定开发偏好。",
        "supersede_node_ids": [12]
      }
    ],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "Rust 偏好被再次确认。"
  },
  "project": {
    "accepted_candidates": [],
    "invalid_candidate_indexes": [0],
    "retire_only_node_ids": [],
    "reason": "这条项目候选缺少长期价值。"
  }
}`,
		},
	}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	reviewer := NewProfileReviewer(llm, prompts, "qwen-test")

	result, err := reviewer.Review(context.Background(), logicdomain.ProfileReviewTargetsSnapshot{
		UserNodes: []logicdomain.ProfileActiveNodeRecord{
			{ID: 12, ProfileType: logicdomain.ProfileTypeUser, ProfileDate: "2026-03-29", Priority: logicdomain.ProfilePriorityP1, ProfileLevel: logicdomain.ProfileLevelStable, RefreshWeight: 2, Content: "偏好 Rust"},
		},
		ProjectNodes: []logicdomain.ProfileActiveNodeRecord{
			{ID: 34, ProfileType: logicdomain.ProfileTypeProject, ProfileDate: "2026-03-28", Priority: logicdomain.ProfilePriorityP2, ProfileLevel: logicdomain.ProfileLevelSituational, RefreshWeight: 0, Content: "项目还在探索技术路线"},
		},
	}, []logicdomain.ProfileNodeCandidate{
		{ProfileType: logicdomain.ProfileTypeUser, Content: "我还是更喜欢 Rust。", SourceTurnID: 101, ProfileDate: "2026-03-30"},
		{ProfileType: logicdomain.ProfileTypeProject, Content: "当前项目先随便看看。", SourceTurnID: 102, ProfileDate: "2026-03-30"},
	})
	if err != nil {
		t.Fatalf("review profiles: %v", err)
	}
	if prompts.scene != "review_profile_nodes" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if !strings.Contains(llm.request.UserPrompt, `"active_nodes"`) || !strings.Contains(llm.request.UserPrompt, `"new_candidates"`) {
		t.Fatalf("expected active_nodes and new_candidates in request body, got %s", llm.request.UserPrompt)
	}
	if strings.Contains(llm.request.UserPrompt, `"current_profile"`) {
		t.Fatalf("did not expect legacy current_profile blob in request body, got %s", llm.request.UserPrompt)
	}
	if result.User == nil || len(result.User.AcceptedCandidates) != 1 {
		t.Fatalf("unexpected user review result: %+v", result)
	}
	if result.Project == nil || len(result.Project.InvalidCandidateIndexes) != 1 {
		t.Fatalf("unexpected project review result: %+v", result)
	}
}

// TestParseProfileReviewResponseRejectsMissingCoverage verifies every candidate must be classified exactly once.
// TestParseProfileReviewResponseRejectsMissingCoverage 用于验证每条候选都必须被且仅被分类一次。
func TestParseProfileReviewResponseRejectsMissingCoverage(t *testing.T) {
	_, err := parseProfileReviewResponse(`{
  "user": {
    "accepted_candidates": [],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "遗漏了候选分类。"
  }
}`, 1, 0)
	if err == nil || !strings.Contains(err.Error(), "must classify all 1 candidates exactly once") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestParseProfileReviewResponseRecoversDuplicateAcceptedCandidateIndexes verifies duplicate accepted candidate indexes are collapsed into one stable winner so one repeated LLM item no longer invalidates the whole review block.
// TestParseProfileReviewResponseRecoversDuplicateAcceptedCandidateIndexes 用于验证重复的 accepted candidate index 会被收敛成一个稳定结果，避免模型重复输出同一候选时整块评审直接失效。
func TestParseProfileReviewResponseRecoversDuplicateAcceptedCandidateIndexes(t *testing.T) {
	result, err := parseProfileReviewResponse(`{
  "user": {
    "accepted_candidates": [
      {
        "candidate_index": 0,
        "normalized_content": "喜欢喝咖啡",
        "priority": "P2",
        "level": "L1",
        "level_reason": "阶段性习惯",
        "supersede_node_ids": [12]
      },
      {
        "candidate_index": 0,
        "normalized_content": "用户有稳定的生活习惯：喜欢喝咖啡",
        "priority": "P2",
        "level": "L2",
        "level_reason": "这是更稳定的生活习惯偏好。",
        "supersede_node_ids": [12, 13]
      }
    ],
    "invalid_candidate_indexes": [],
    "retire_only_node_ids": [],
    "reason": "重复输出了同一候选。"
  }
}`, 1, 0)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if result.User == nil || len(result.User.AcceptedCandidates) != 1 {
		t.Fatalf("expected one accepted candidate after duplicate recovery, got %+v", result)
	}
	accepted := result.User.AcceptedCandidates[0]
	if accepted.CandidateIndex != 0 {
		t.Fatalf("unexpected candidate index: %+v", accepted)
	}
	if accepted.NormalizedContent != "用户有稳定的生活习惯：喜欢喝咖啡" {
		t.Fatalf("expected richer normalized content to survive duplicate recovery, got %+v", accepted)
	}
	if accepted.ProfileLevel != logicdomain.ProfileLevelStable {
		t.Fatalf("expected stronger level metadata to survive duplicate recovery, got %+v", accepted)
	}
	if len(accepted.SupersedeNodeIDs) != 2 || accepted.SupersedeNodeIDs[0] != 12 || accepted.SupersedeNodeIDs[1] != 13 {
		t.Fatalf("expected supersede ids to be merged and normalized, got %+v", accepted.SupersedeNodeIDs)
	}
}
