// reviewer_test_helpers_test.go stores shared test doubles reused by multiple reviewer-oriented processor tests after legacy prompt scenes were removed.
// reviewer_test_helpers_test.go 用于保存多个 reviewer 处理器测试共享的测试替身，避免删除旧提示词场景后遗留无主桩代码。
package processor

import (
	"context"

	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

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
