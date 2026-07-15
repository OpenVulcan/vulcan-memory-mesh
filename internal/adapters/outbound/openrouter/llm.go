// llm.go implements the OpenRouter SDK-backed chat completion adapter.
// llm.go 用于实现基于 OpenRouter SDK 的 chat completion 出站适配器。
package openrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerhint"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LLMClient adapts the internal generation port onto OpenRouter's typed chat completion SDK.
// LLMClient 用于把内部生成端口适配到 OpenRouter 的强类型 chat completion SDK。
type LLMClient struct {
	client      *Client
	model       string
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewLLMClient creates one fixed-route OpenRouter chat client with route-level and model-level default parameters.
// NewLLMClient 用于创建一个固定路由的 OpenRouter chat 客户端，并携带路由级与模型级默认参数。
func NewLLMClient(endpoint, apiKey, model string, params map[string]any, modelParams map[string]map[string]any) *LLMClient {
	return &LLMClient{
		client:      NewClient(endpoint, apiKey, 0, nil),
		model:       strings.TrimSpace(model),
		params:      providerhint.CloneMap(params),
		modelParams: providerhint.CloneNestedMap(modelParams),
	}
}

// Generate sends one provider-neutral LLM request to OpenRouter and converts the first assistant choice into the internal response shape.
// Generate 用于把一条 provider 无关的 LLM 请求发送到 OpenRouter，并将第一条 assistant choice 转换为内部响应结构。
func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c == nil || c.client == nil || c.client.sdkClient == nil {
		return appports.LLMResponse{}, fmt.Errorf("openrouter client is nil")
	}
	if strings.TrimSpace(c.client.apiKey) == "" {
		return appports.LLMResponse{}, fmt.Errorf("openrouter api key is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.LLMResponse{}, fmt.Errorf("openrouter model is required")
	}
	stream := false
	params := components.ChatRequest{
		Model:    &model,
		Messages: buildChatMessages(req.SystemPrompt, req.UserPrompt),
		Stream:   &stream,
	}
	if responseFormat := mapResponseFormat(req.ResponseFormat); responseFormat != nil {
		params.ResponseFormat = responseFormat
	}
	applyProviderHints(&params, mergeProviderHints(c.params, c.modelParams, model, req.ProviderHints))

	// Execute through the SDK so transport, auth, and future OpenRouter schema changes stay isolated in this outbound adapter.
	// 通过 SDK 执行请求，让传输、鉴权和未来 OpenRouter schema 变化都被隔离在当前出站适配器内。
	resp, err := c.client.sdkClient.Chat.Send(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return appports.LLMResponse{}, err
	}
	if resp == nil || resp.ChatResult == nil || len(resp.ChatResult.Choices) == 0 {
		return appports.LLMResponse{}, fmt.Errorf("openrouter chat completion empty choices")
	}
	content := extractAssistantContent(resp.ChatResult.Choices[0])
	if content == "" {
		return appports.LLMResponse{}, fmt.Errorf("openrouter chat completion empty content")
	}
	usage := logicdomain.LLMUsage{}
	if resp.ChatResult.Usage != nil {
		usage = logicdomain.LLMUsage{
			PromptTokens:     int(resp.ChatResult.Usage.PromptTokens),
			CompletionTokens: int(resp.ChatResult.Usage.CompletionTokens),
			TotalTokens:      int(resp.ChatResult.Usage.TotalTokens),
		}
	}
	return appports.LLMResponse{
		Content: content,
		Model:   model,
		Usage:   usage,
	}, nil
}

// buildChatMessages maps the internal system/user prompt pair into the role-discriminated OpenRouter message union.
// buildChatMessages 用于把内部 system/user prompt 对映射成 OpenRouter 按 role 区分的 message union。
func buildChatMessages(systemPrompt, userPrompt string) []components.ChatMessages {
	messages := make([]components.ChatMessages, 0, 2)
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		messages = append(messages, components.CreateChatMessagesSystem(components.ChatSystemMessage{
			Content: components.CreateChatSystemMessageContentStr(prompt),
		}))
	}
	if prompt := strings.TrimSpace(userPrompt); prompt != "" {
		messages = append(messages, components.CreateChatMessagesUser(components.ChatUserMessage{
			Content: components.CreateChatUserMessageContentStr(prompt),
		}))
	}
	return messages
}

// mapResponseFormat translates the internal response-format enum into OpenRouter's typed response-format union.
// mapResponseFormat 用于把内部响应格式枚举转换成 OpenRouter 的强类型 response-format union。
func mapResponseFormat(format appports.LLMResponseFormat) *components.ResponseFormat {
	switch format {
	case appports.LLMResponseFormatJSON:
		responseFormat := components.CreateResponseFormatJSONObject(components.FormatJSONObjectConfig{})
		return &responseFormat
	case appports.LLMResponseFormatText, "":
		responseFormat := components.CreateResponseFormatText(components.ChatFormatTextConfig{})
		return &responseFormat
	default:
		responseFormat := components.CreateResponseFormatText(components.ChatFormatTextConfig{})
		return &responseFormat
	}
}

// applyProviderHints maps stable OpenRouter chat hints to SDK fields and ignores unknown keys that the generated SDK cannot safely preserve.
// applyProviderHints 用于把稳定的 OpenRouter chat 参数映射到 SDK 字段，并忽略生成式 SDK 无法安全保留的未知键。
func applyProviderHints(params *components.ChatRequest, hints map[string]any) {
	if params == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "temperature":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.Temperature = optionalValue(value)
			}
		case "top_p":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.TopP = optionalValue(value)
			}
		case "presence_penalty":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.PresencePenalty = optionalValue(value)
			}
		case "frequency_penalty":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.FrequencyPenalty = optionalValue(value)
			}
		case "max_tokens":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.MaxTokens = optionalValue(value)
			}
		case "max_completion_tokens":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.MaxCompletionTokens = optionalValue(value)
			}
		case "seed":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.Seed = optionalValue(value)
			}
		case "logprobs":
			if value, ok := providerhint.Bool(rawValue); ok {
				params.Logprobs = optionalValue(value)
			}
		case "top_logprobs":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.TopLogprobs = optionalValue(value)
			}
		case "parallel_tool_calls":
			if value, ok := providerhint.Bool(rawValue); ok {
				params.ParallelToolCalls = optionalValue(value)
			}
		case "user":
			if value, ok := providerhint.String(rawValue); ok {
				params.User = &value
			}
		case "session_id":
			if value, ok := providerhint.String(rawValue); ok {
				params.SessionID = &value
			}
		case "service_tier":
			if value, ok := providerhint.String(rawValue); ok {
				params.ServiceTier = optionalValue(components.ChatRequestServiceTier(value))
			}
		case "stop":
			if value, ok := stopHint(rawValue); ok {
				params.Stop = optionalValue(value)
			}
		case "metadata":
			if value, ok := metadataHint(rawValue); ok {
				params.Metadata = value
			}
		case "provider":
			if value, ok := providerPreferencesHint(rawValue); ok {
				params.Provider = optionalValue(value)
			}
		case "reasoning_effort":
			if value, ok := providerhint.String(rawValue); ok {
				ensureReasoning(params).Effort = optionalValue(components.Effort(value))
			}
		case "reasoning_summary":
			if value, ok := providerhint.String(rawValue); ok {
				ensureReasoning(params).Summary = optionalValue(components.ChatReasoningSummaryVerbosityEnum(value))
			}
		}
	}
}

// ensureReasoning returns the request reasoning object, creating it only when one reasoning hint is actually present.
// ensureReasoning 用于返回请求的 reasoning 对象，并且只在确实存在 reasoning 参数时才创建该对象。
func ensureReasoning(params *components.ChatRequest) *components.Reasoning {
	if params.Reasoning == nil {
		params.Reasoning = &components.Reasoning{}
	}
	return params.Reasoning
}

// extractAssistantContent reads text content from the first OpenRouter assistant choice and trims surrounding whitespace.
// extractAssistantContent 用于从 OpenRouter 第一条 assistant choice 中读取文本内容，并裁剪首尾空白。
func extractAssistantContent(choice components.ChatChoice) string {
	content, ok := choice.Message.Content.GetOrZero()
	if !ok {
		return ""
	}
	if content.Str != nil {
		return strings.TrimSpace(*content.Str)
	}
	if text, ok := content.Any.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
