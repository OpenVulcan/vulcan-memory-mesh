// manual_profile_reviewer_test.go verifies the explicit manual profile-instruction reviewer against the single-target JSON contract.
// manual_profile_reviewer_test.go 用于围绕单目标 JSON 契约验证显式手工画像指令评审器。
package processor

import (
	"context"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestManualProfileReviewerBuildsSingleTargetRequest verifies one manual instruction review request carries active nodes, one explicit instruction, and the authority floor.
// TestManualProfileReviewerBuildsSingleTargetRequest 用于验证手工画像评审请求会携带 active 节点、单条显式指令以及权限地板。
func TestManualProfileReviewerBuildsSingleTargetRequest(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "accepted_nodes": [
    {
      "normalized_content": "团队级规范必须统一使用 Go 语言实现服务端。",
      "priority": "P0",
      "level": "L3",
      "level_reason": "这是跨项目共享的强约束。",
      "supersede_nodes": [
        {
          "node_id": 15,
          "reason": "新的团队级规范覆盖了旧的语言约定。"
        }
      ]
    }
  ],
  "retired_nodes": [],
  "reason": "新的团队级显式指令应作为最高权威规则。"
}`,
		},
	}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	reviewer := NewManualProfileReviewer(llm, prompts, "qwen-test")

	result, err := reviewer.Review(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeTeam,
		BindID:      1,
	}, []logicdomain.ProfileNodeRecord{
		{
			ID:            15,
			ProfileType:   logicdomain.ProfileTypeTeam,
			BindID:        1,
			ProfileDate:   "2026-03-29",
			Priority:      logicdomain.ProfilePriorityP1,
			ProfileLevel:  logicdomain.ProfileLevelStable,
			RefreshWeight: 2,
			SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
			SourceID:      8,
			Content:       "团队默认使用 Rust。",
		},
	}, "以后团队服务端统一使用 Go。", logicdomain.ProfilePriorityP0, logicdomain.ProfileLevelPersistent)
	if err != nil {
		t.Fatalf("review manual profile instruction: %v", err)
	}
	if prompts.scene != "review_profile_instruction" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if !strings.Contains(llm.request.UserPrompt, `"target": "TEAM"`) {
		t.Fatalf("expected TEAM target in request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"authority_floor"`) {
		t.Fatalf("expected authority_floor block in request body, got %s", llm.request.UserPrompt)
	}
	if result.Reason != "新的团队级显式指令应作为最高权威规则。" {
		t.Fatalf("unexpected review result: %+v", result)
	}
	if len(result.AcceptedNodes) != 1 || len(result.AcceptedNodes[0].SupersedeNodes) != 1 || result.AcceptedNodes[0].SupersedeNodes[0].NodeID != 15 {
		t.Fatalf("unexpected accepted nodes: %+v", result)
	}
}

// TestManualProfileReviewerRejectsUnknownRetireNode verifies the reviewer output cannot retire nodes that were not present in the active-node input.
// TestManualProfileReviewerRejectsUnknownRetireNode 用于验证评审输出不能退役输入 active 节点集合之外的节点。
func TestManualProfileReviewerRejectsUnknownRetireNode(t *testing.T) {
	_, err := parseManualProfileReviewResponse(`{
  "accepted_nodes": [],
  "retired_nodes": [
    {
      "node_id": 99,
      "reason": "should fail"
    }
  ],
  "reason": "invalid"
}`, []logicdomain.ProfileNodeRecord{
		{ID: 15, ProfileType: logicdomain.ProfileTypeUser, BindID: 7, Content: "偏好 Rust。"},
	})
	if err == nil || !strings.Contains(err.Error(), "was not present in active nodes") {
		t.Fatalf("unexpected error: %v", err)
	}
}
