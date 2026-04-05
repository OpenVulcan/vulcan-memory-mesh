// llm.go declares the AI-generation contract owned by the logic layer.
// llm.go 用于声明由 logic 层拥有的模型生成契约。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LLMResponseFormat describes the response shape that processors expect from the model call.
// LLMResponseFormat 用于描述处理器期望模型返回的响应形态。
type LLMResponseFormat string

// Constants enumerate the supported response formats for the LLM port.
// Constants 用于枚举 LLM 端口支持的响应格式。
const (
	LLMResponseFormatText LLMResponseFormat = "text"
	LLMResponseFormatJSON LLMResponseFormat = "json"
)

// LLMRequest carries the provider-neutral generation payload built by processors and use cases.
// LLMRequest 用于承载处理器和用例层构建的 provider 无关生成请求。
type LLMRequest struct {
	Model          string
	SystemPrompt   string
	UserPrompt     string
	ResponseFormat LLMResponseFormat
	ProviderHints  map[string]any
}

// LLMResponse returns the raw model output plus token usage in the internal contract shape.
// LLMResponse 用于返回内部契约形态的原始模型输出和 token 用量。
type LLMResponse struct {
	Content string
	Usage   logicdomain.LLMUsage
}

// LLMClient is the port that lets processors call a model without binding to a specific provider SDK.
// LLMClient 用于让处理器在不绑定具体模型 SDK 的前提下调用大模型。
type LLMClient interface {
	Generate(ctx context.Context, req LLMRequest) (LLMResponse, error)
}
