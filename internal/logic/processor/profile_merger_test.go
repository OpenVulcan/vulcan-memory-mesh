// profile_merger_test.go verifies the batched user/project profile merge processor against the new single-call JSON contract.
// profile_merger_test.go 用于围绕新的单次调用 JSON 契约验证批量 user/project 画像合并处理器。
package processor

import (
	"context"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestProfileMergerBatchesUserAndProjectTargets verifies one merge call carries both target groups together and parses both result blocks back into the internal contract.
// TestProfileMergerBatchesUserAndProjectTargets 用于验证一次合并调用会把两类目标一起送入，并把两块结果都解析回内部契约。
func TestProfileMergerBatchesUserAndProjectTargets(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "user": {
    "updated_profile": "合并后的用户画像",
    "merged_candidate_indexes": [0, 1],
    "invalid_candidate_indexes": [],
    "reason": "两条用户画像都应保留。"
  },
  "project": {
    "updated_profile": "合并后的项目画像",
    "merged_candidate_indexes": [0],
    "invalid_candidate_indexes": [1],
    "reason": "项目兼容范围尚不稳定。"
  }
}`,
		},
	}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	merger := NewProfileMerger(llm, prompts, "qwen-test")

	result, err := merger.Merge(context.Background(), logicdomain.ProfileTargetsSnapshot{
		UserProfile:    "旧用户画像",
		ProjectProfile: "旧项目画像",
	}, []logicdomain.ProfileNodeCandidate{
		{ProfileType: logicdomain.ProfileTypeUser, Content: "用户偏好 Rust。"},
		{ProfileType: logicdomain.ProfileTypeProject, Content: "项目仍处于设计阶段。"},
		{ProfileType: logicdomain.ProfileTypeUser, Content: "用户希望支持多个 AI 工具。"},
		{ProfileType: logicdomain.ProfileTypeProject, Content: "项目兼容工具范围尚未最终确定。"},
	})
	if err != nil {
		t.Fatalf("merge profiles: %v", err)
	}
	if prompts.scene != "merge_profile" || prompts.modelName != "qwen-test" {
		t.Fatalf("unexpected prompt lookup: %+v", prompts)
	}
	if llm.request.ResponseFormat != logicports.LLMResponseFormatJSON {
		t.Fatalf("expected json response format, got %+v", llm.request)
	}
	if !strings.Contains(llm.request.UserPrompt, `"user": {`) {
		t.Fatalf("expected user block in request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"project": {`) {
		t.Fatalf("expected project block in request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"current_profile": "旧用户画像"`) {
		t.Fatalf("expected current user profile in request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"current_profile": "旧项目画像"`) {
		t.Fatalf("expected current project profile in request body, got %s", llm.request.UserPrompt)
	}
	if !strings.Contains(llm.request.UserPrompt, `"candidates"`) {
		t.Fatalf("expected both candidate groups in request body, got %s", llm.request.UserPrompt)
	}
	if result.User == nil || result.Project == nil {
		t.Fatalf("expected both merge sections, got %+v", result)
	}
	if result.User.UpdatedProfile != "合并后的用户画像" || result.Project.UpdatedProfile != "合并后的项目画像" {
		t.Fatalf("unexpected merge result: %+v", result)
	}
}

// TestProfileMergerAllowsMissingTargetBlockWhenNoCandidates verifies the parser accepts one omitted target block when that side had no candidates in the input batch.
// TestProfileMergerAllowsMissingTargetBlockWhenNoCandidates 用于验证当某一侧输入没有候选时，解析器会接受缺省的目标结果块。
func TestProfileMergerAllowsMissingTargetBlockWhenNoCandidates(t *testing.T) {
	llm := &stubProfileMergerLLM{
		response: logicports.LLMResponse{
			Content: `{
  "user": {
    "updated_profile": "合并后的用户画像",
    "merged_candidate_indexes": [0],
    "invalid_candidate_indexes": [],
    "reason": "只有一条用户画像候选。"
  }
}`,
		},
	}
	merger := NewProfileMerger(llm, &stubProfilePromptSource{prompt: "return json only"}, "qwen-test")

	result, err := merger.Merge(context.Background(), logicdomain.ProfileTargetsSnapshot{
		UserProfile:    "旧用户画像",
		ProjectProfile: "旧项目画像",
	}, []logicdomain.ProfileNodeCandidate{
		{ProfileType: logicdomain.ProfileTypeUser, Content: "用户偏好 Rust。"},
	})
	if err != nil {
		t.Fatalf("merge profiles with user-only candidates: %v", err)
	}
	if !strings.Contains(llm.request.UserPrompt, `"user": {`) {
		t.Fatalf("expected user block in user-only request, got %s", llm.request.UserPrompt)
	}
	if strings.Contains(llm.request.UserPrompt, `"project": {`) {
		t.Fatalf("did not expect project block in user-only request, got %s", llm.request.UserPrompt)
	}
	if result.User == nil {
		t.Fatalf("expected user merge section, got %+v", result)
	}
	if result.Project != nil {
		t.Fatalf("expected missing project merge section, got %+v", result)
	}
}

// stubProfilePromptSource returns one canned prompt and records the requested scene/model for assertions.
// stubProfilePromptSource 用于返回预设提示词，并记录请求的场景和模型，方便断言。
type stubProfilePromptSource struct {
	scene     string
	modelName string
	prompt    string
}

// GetPrompt captures the requested scene and returns the canned prompt body.
// GetPrompt 用于捕获请求场景，并返回预设提示词内容。
func (s *stubProfilePromptSource) GetPrompt(scene, modelName string) (string, error) {
	s.scene = scene
	s.modelName = modelName
	return s.prompt, nil
}

// stubProfileMergerLLM returns one canned JSON response and records the outgoing request.
// stubProfileMergerLLM 用于返回预设 JSON 响应，并记录发出的请求。
type stubProfileMergerLLM struct {
	request  logicports.LLMRequest
	response logicports.LLMResponse
	err      error
}

// Generate captures the request and replays the configured response or error.
// Generate 用于记录请求，并回放预设响应或错误。
func (s *stubProfileMergerLLM) Generate(_ context.Context, req logicports.LLMRequest) (logicports.LLMResponse, error) {
	s.request = req
	if s.err != nil {
		return logicports.LLMResponse{}, s.err
	}
	return s.response, nil
}
