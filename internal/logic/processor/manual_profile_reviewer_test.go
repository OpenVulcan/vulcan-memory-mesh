// manual_profile_reviewer_test.go verifies the explicit manual profile-instruction reviewer against the single-target JSON contract.
// manual_profile_reviewer_test.go 用于围绕单目标 JSON 契约验证显式手工画像指令评审器。
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

// TestManualProfileReviewerBuildsSingleTargetRequest verifies one manual instruction review request carries active nodes, one explicit instruction, and the authority floor.
// TestManualProfileReviewerBuildsSingleTargetRequest 用于验证手工画像评审请求会携带 active 节点、单条显式指令以及权限地板。
func TestManualProfileReviewerBuildsSingleTargetRequest(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Model:     "provider-profile-model",
			RequestID: "req-profile-review",
			Usage:     logicdomain.LLMUsage{PromptTokens: 90, CompletionTokens: 19, TotalTokens: 109, CachedInputTokens: 66},
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
	if prompts.scene != "profile_instruction_main" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if llm.request.SystemPrompt != "return json only" {
		t.Fatalf("expected runtime to preserve prompt file content without shared injection, got %s", llm.request.SystemPrompt)
	}
	if llm.request.RouteSelectionLevel != logicports.LLMRouteSelectionLevelProfileInstruction {
		t.Fatalf("expected profile instruction route selection level, got %q", llm.request.RouteSelectionLevel)
	}
	assertStructuredOutputRequest(t, llm.request, "vmm_manual_profile_review")
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	if !strings.Contains(llm.request.UserPrompt, `"target":"TEAM"`) {
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
	if result.LLMExecution == nil || result.LLMExecution.ConfiguredModel != "qwen-test" || result.LLMExecution.ResponseModel != "provider-profile-model" || result.LLMExecution.RequestID != "req-profile-review" || result.LLMExecution.Usage.CachedInputTokens != 66 {
		t.Fatalf("unexpected manual profile execution metadata: %+v", result.LLMExecution)
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

// TestManualProfileReviewerUsesLocalFallbackDateForActiveNodes verifies active-node fallback dates follow the shared local calendar-day contract instead of truncating to UTC.
// TestManualProfileReviewerUsesLocalFallbackDateForActiveNodes 用于验证 active 节点缺失显式日期时，会按共享本地自然日契约回退，而不是直接截断成 UTC 日期。
func TestManualProfileReviewerUsesLocalFallbackDateForActiveNodes(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "accepted_nodes": [],
  "retired_nodes": [],
  "reason": "no-op"
}`,
		},
	}
	reviewer := NewManualProfileReviewer(llm, &stubProfilePromptSource{prompt: "return json only"}, "qwen-test")

	_, err := reviewer.Review(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeUser,
		BindID:      7,
	}, []logicdomain.ProfileNodeRecord{{
		ID:            21,
		ProfileType:   logicdomain.ProfileTypeUser,
		BindID:        7,
		Priority:      logicdomain.ProfilePriorityP1,
		ProfileLevel:  logicdomain.ProfileLevelStable,
		RefreshWeight: 1,
		SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
		SourceID:      101,
		Content:       "用户偏好使用本地部署。",
		CreatedAt:     time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
	}}, "以后继续保留这个偏好。", logicdomain.ProfilePriorityP1, logicdomain.ProfileLevelStable)
	if err != nil {
		t.Fatalf("review manual profile instruction: %v", err)
	}
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	if !strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-02 00:30:00"`) {
		t.Fatalf("expected fallback active-node datetime to use local datetime anchor, got %s", llm.request.UserPrompt)
	}
}

// TestManualProfileReviewerCorrectsLegacyUTCProfileDate verifies manual profile review requests repair stored UTC-derived profile_date values before the LLM compares them with the explicit instruction.
// TestManualProfileReviewerCorrectsLegacyUTCProfileDate 用于验证手工画像评审请求会先修正历史上按 UTC 推导的 profile_date，再让 LLM 与显式指令进行比较。
func TestManualProfileReviewerCorrectsLegacyUTCProfileDate(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "accepted_nodes": [],
  "retired_nodes": [],
  "reason": "no-op"
}`,
		},
	}
	reviewer := NewManualProfileReviewer(llm, &stubProfilePromptSource{prompt: "return json only"}, "qwen-test")

	_, err := reviewer.Review(context.Background(), logicdomain.ProfileTargetRef{
		ProfileType: logicdomain.ProfileTypeUser,
		BindID:      7,
	}, []logicdomain.ProfileNodeRecord{{
		ID:                  22,
		ProfileType:         logicdomain.ProfileTypeUser,
		BindID:              7,
		ProfileDate:         "2026-04-01",
		Priority:            logicdomain.ProfilePriorityP1,
		ProfileLevel:        logicdomain.ProfileLevelStable,
		RefreshWeight:       1,
		SourceKind:          logicdomain.ProfileSourceKindTurnExtract,
		SourceID:            101,
		Content:             "用户偏好使用本地部署。",
		ProfileDateAnchorAt: time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
		CreatedAt:           time.Date(2026, 4, 2, 3, 0, 0, 0, time.UTC),
	}}, "以后继续保留这个偏好。", logicdomain.ProfilePriorityP1, logicdomain.ProfileLevelStable)
	if err != nil {
		t.Fatalf("review manual profile instruction: %v", err)
	}
	assertCompactJSONPrompt(t, llm.request.UserPrompt)
	if strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-01`) {
		t.Fatalf("expected legacy UTC-derived datetime to be corrected, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"datetime":"2026-04-02 00:30:00"`) {
		t.Fatalf("expected corrected local datetime in reviewer request, got %s", llm.request.UserPrompt)
	}
}
