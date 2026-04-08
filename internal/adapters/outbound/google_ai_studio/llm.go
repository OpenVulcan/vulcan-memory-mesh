// llm.go implements the Google AI Studio native LLM adapter.
// llm.go 用于实现 Google AI Studio 原生 LLM 适配器。
package google_ai_studio

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/genai"
)

// LLMClient adapts the internal generation port onto the official Google GenAI GenerateContent API.
// LLMClient 用于把内部生成端口适配到官方 Google GenAI 的 GenerateContent API。
type LLMClient struct {
	client      *Client
	model       string
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewLLMClient creates one Google AI Studio native LLM adapter bound to one fixed model plus optional route-level default parameters.
// NewLLMClient 用于创建一个绑定固定模型和可选路由级默认参数的 Google AI Studio 原生 LLM 适配器。
func NewLLMClient(endpoint, apiKey, model string, params map[string]any, modelParams map[string]map[string]any) *LLMClient {
	return &LLMClient{
		client:      NewClient(endpoint, apiKey, nil),
		model:       strings.TrimSpace(model),
		params:      cloneHintMap(params),
		modelParams: cloneNestedHintMap(modelParams),
	}
}

// Generate executes one provider-neutral LLM request against Google AI Studio and returns the first text candidate plus token usage when available.
// Generate 用于把一次 provider 无关的 LLM 请求发送到 Google AI Studio，并返回第一条文本候选以及可用的 token 用量统计。
func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	// Resolve the target SDK client and fixed model before we translate internal hints into Gemini-native request fields.
	// 在把内部 hint 翻译成 Gemini 原生请求字段之前，先解析目标 SDK 客户端与固定模型。
	if c == nil || c.client == nil {
		return appports.LLMResponse{}, fmt.Errorf("google ai studio llm client is nil")
	}
	sdkClient, err := c.client.SDK()
	if err != nil {
		return appports.LLMResponse{}, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.LLMResponse{}, fmt.Errorf("google ai studio model is required")
	}
	userPrompt := strings.TrimSpace(req.UserPrompt)
	if userPrompt == "" {
		return appports.LLMResponse{}, fmt.Errorf("google ai studio user prompt is required")
	}

	// Build one Gemini-native generation config so request-level response contracts still override route defaults.
	// 组装一份 Gemini 原生生成配置，保证请求级响应契约仍然优先于路由默认值。
	config := &genai.GenerateContentConfig{}
	if systemPrompt := strings.TrimSpace(req.SystemPrompt); systemPrompt != "" {
		config.SystemInstruction = genai.NewContentFromText(systemPrompt, genai.RoleUser)
	}
	applyGenerateHints(config, mergeProviderHints(c.params, c.modelParams, model, req.ProviderHints))
	applyResponseFormat(config, req.ResponseFormat)
	if httpOptions := requestHTTPOptionsFromContext(ctx); httpOptions != nil {
		config.HTTPOptions = httpOptions
	}

	// Execute the upstream call and reject structurally empty text responses to keep processor-side error handling deterministic.
	// 执行上游调用，并拒绝结构上为空的文本响应，保持处理器侧错误处理的确定性。
	resp, err := sdkClient.Models.GenerateContent(ctx, model, genai.Text(userPrompt), config)
	if err != nil {
		return appports.LLMResponse{}, err
	}
	content := strings.TrimSpace(resp.Text())
	if content == "" {
		return appports.LLMResponse{}, fmt.Errorf("google ai studio generate content empty text")
	}

	// Convert the provider usage shape into the internal response contract while tolerating providers that omit token accounting.
	// 将 provider 的 usage 结构转换成内部响应契约，同时容忍某些响应省略 token 统计。
	usage := logicdomain.LLMUsage{}
	if resp != nil && resp.UsageMetadata != nil {
		usage = logicdomain.LLMUsage{
			PromptTokens:     int(resp.UsageMetadata.PromptTokenCount),
			CompletionTokens: int(resp.UsageMetadata.CandidatesTokenCount),
			TotalTokens:      int(resp.UsageMetadata.TotalTokenCount),
		}
	}
	return appports.LLMResponse{Content: content, Model: model, Usage: usage}, nil
}

// applyGenerateHints maps portable and Google-specific hint fields onto the GenerateContentConfig used by the Gemini API.
// applyGenerateHints 用于把可移植和 Google 专属的 hint 字段映射到 Gemini API 使用的 GenerateContentConfig。
func applyGenerateHints(config *genai.GenerateContentConfig, hints map[string]any) {
	if config == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "temperature":
			if value, ok := floatHint(rawValue); ok {
				config.Temperature = float32Ptr(value)
			}
		case "top_p":
			if value, ok := floatHint(rawValue); ok {
				config.TopP = float32Ptr(value)
			}
		case "top_k":
			if value, ok := floatHint(rawValue); ok {
				config.TopK = float32Ptr(value)
			}
		case "candidate_count", "n":
			if value, ok := intHint(rawValue); ok {
				config.CandidateCount = int32(value)
			}
		case "max_output_tokens", "max_tokens", "max_completion_tokens":
			if value, ok := intHint(rawValue); ok {
				config.MaxOutputTokens = int32(value)
			}
		case "stop", "stop_sequences":
			if value, ok := stringListHint(rawValue); ok {
				config.StopSequences = value
			}
		case "response_logprobs":
			if value, ok := boolHint(rawValue); ok {
				config.ResponseLogprobs = value
			}
		case "logprobs":
			if value, ok := intHint(rawValue); ok {
				config.Logprobs = int32Ptr(value)
				config.ResponseLogprobs = true
			}
		case "presence_penalty":
			if value, ok := floatHint(rawValue); ok {
				config.PresencePenalty = float32Ptr(value)
			}
		case "frequency_penalty":
			if value, ok := floatHint(rawValue); ok {
				config.FrequencyPenalty = float32Ptr(value)
			}
		case "seed":
			if value, ok := intHint(rawValue); ok {
				config.Seed = int32Ptr(value)
			}
		case "response_mime_type":
			if value, ok := stringHint(rawValue); ok {
				config.ResponseMIMEType = value
			}
		}
	}
}

// applyResponseFormat enforces the internal text-vs-JSON contract after provider hints have been merged so processor expectations always win.
// applyResponseFormat 用于在 provider hint 合并之后强制应用内部 text/JSON 响应契约，确保处理器的预期始终优先。
func applyResponseFormat(config *genai.GenerateContentConfig, format appports.LLMResponseFormat) {
	if config == nil {
		return
	}
	switch format {
	case appports.LLMResponseFormatJSON:
		config.ResponseMIMEType = "application/json"
	case appports.LLMResponseFormatText, "":
		config.ResponseMIMEType = "text/plain"
	default:
		config.ResponseMIMEType = "text/plain"
	}
}
