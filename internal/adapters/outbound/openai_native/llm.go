// llm.go implements the OpenAI-compatible outbound adapters.
// llm.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// LLMClient adapts the internal generation port onto the official OpenAI chat completions SDK.
// LLMClient 用于把内部生成端口适配到官方 OpenAI Chat Completions SDK。
type LLMClient struct {
	client      *Client
	model       string
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewLLMClient binds one chat model and cloned provider hints to the native OpenAI generation adapter.
// NewLLMClient 用于把单个对话模型及克隆后的 provider hint 绑定到原生 OpenAI 生成适配器。
func NewLLMClient(endpoint, apiKey, model, organization, project string, params map[string]any, modelParams map[string]map[string]any) *LLMClient {
	return &LLMClient{
		client:      NewClient(endpoint, apiKey, organization, project, nil),
		model:       strings.TrimSpace(model),
		params:      cloneHintMap(params),
		modelParams: cloneNestedHintMap(modelParams),
	}
}

// Generate merges configured and request-level hints, calls Chat Completions, and maps usage into the application port response.
// Generate 用于合并配置级与请求级 hint、调用 Chat Completions，并把用量映射为应用端口响应。
func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	// Resolve the target model and translate the internal request into SDK params.
	// 解析目标模型，并将内部请求翻译成 SDK 参数。
	if c == nil || c.client == nil || c.client.sdkClient == nil {
		return appports.LLMResponse{}, fmt.Errorf("openai native client is nil")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.LLMResponse{}, fmt.Errorf("openai native model is required")
	}
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: buildChatMessages(req.SystemPrompt, req.UserPrompt),
	}
	if responseFormat := mapResponseFormat(req.ResponseFormat); responseFormat != nil {
		params.ResponseFormat = *responseFormat
	}
	applyProviderHints(&params, mergeProviderHints(c.params, c.modelParams, model, req.ProviderHints))

	// Execute the provider call and reject structurally empty results.
	// 执行模型调用，并拒绝结构上为空的返回结果。
	resp, err := c.client.sdkClient.Chat.Completions.New(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return appports.LLMResponse{}, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return appports.LLMResponse{}, fmt.Errorf("openai native chat completion empty choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if strings.TrimSpace(content) == "" {
		return appports.LLMResponse{}, fmt.Errorf("openai native chat completion empty content")
	}

	// Convert the SDK usage shape into the internal response contract.
	// 将 SDK 的 usage 结构转换为内部响应契约。
	return appports.LLMResponse{
		Content: content,
		Model:   model,
		Usage: logicdomain.LLMUsage{
			PromptTokens:     int(resp.Usage.PromptTokens),
			CompletionTokens: int(resp.Usage.CompletionTokens),
			TotalTokens:      int(resp.Usage.TotalTokens),
		},
	}, nil
}

// buildChatMessages builds the target dependency.
// buildChatMessages 用于构建目标依赖。
func buildChatMessages(systemPrompt, userPrompt string) []openai.ChatCompletionMessageParamUnion {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 2)
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		messages = append(messages, openai.SystemMessage(prompt))
	}
	if prompt := strings.TrimSpace(userPrompt); prompt != "" {
		messages = append(messages, openai.UserMessage(prompt))
	}
	return messages
}

// mapResponseFormat maps values into the target shape.
// mapResponseFormat 用于将值映射到目标结构。
func mapResponseFormat(format appports.LLMResponseFormat) *openai.ChatCompletionNewParamsResponseFormatUnion {
	switch format {
	case appports.LLMResponseFormatJSON:
		jsonObject := shared.NewResponseFormatJSONObjectParam()
		return &openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &jsonObject}
	case appports.LLMResponseFormatText, "":
		text := shared.NewResponseFormatTextParam()
		return &openai.ChatCompletionNewParamsResponseFormatUnion{OfText: &text}
	default:
		text := shared.NewResponseFormatTextParam()
		return &openai.ChatCompletionNewParamsResponseFormatUnion{OfText: &text}
	}
}

// applyProviderHints applies the target settings.
// applyProviderHints 用于应用目标设置。
func applyProviderHints(params *openai.ChatCompletionNewParams, hints map[string]any) {
	// Apply portable hint fields and forward provider-specific extras through the SDK extra payload.
	// 应用可移植的 hint 字段，并通过 SDK 扩展载荷转发 provider 专属参数。
	if params == nil {
		return
	}
	extraFields := map[string]any{
		"include_reasoning": false,
	}
	for rawKey, rawValue := range hints {
		originalKey := strings.TrimSpace(rawKey)
		key := strings.ToLower(originalKey)
		switch key {
		case "temperature":
			if value, ok := floatHint(rawValue); ok {
				params.Temperature = openai.Float(value)
			}
		case "top_p":
			if value, ok := floatHint(rawValue); ok {
				params.TopP = openai.Float(value)
			}
		case "presence_penalty":
			if value, ok := floatHint(rawValue); ok {
				params.PresencePenalty = openai.Float(value)
			}
		case "frequency_penalty":
			if value, ok := floatHint(rawValue); ok {
				params.FrequencyPenalty = openai.Float(value)
			}
		case "max_tokens":
			if value, ok := intHint(rawValue); ok {
				params.MaxTokens = openai.Int(value)
			}
		case "max_completion_tokens":
			if value, ok := intHint(rawValue); ok {
				params.MaxCompletionTokens = openai.Int(value)
			}
		case "n":
			if value, ok := intHint(rawValue); ok {
				params.N = openai.Int(value)
			}
		case "seed":
			if value, ok := intHint(rawValue); ok {
				params.Seed = openai.Int(value)
			}
		case "logprobs":
			if value, ok := boolHint(rawValue); ok {
				params.Logprobs = openai.Bool(value)
			}
		case "top_logprobs":
			if value, ok := intHint(rawValue); ok {
				params.TopLogprobs = openai.Int(value)
			}
		case "store":
			if value, ok := boolHint(rawValue); ok {
				params.Store = openai.Bool(value)
			}
		case "parallel_tool_calls":
			if value, ok := boolHint(rawValue); ok {
				params.ParallelToolCalls = openai.Bool(value)
			}
		case "user":
			if value, ok := stringHint(rawValue); ok {
				params.User = openai.String(value)
			}
		case "safety_identifier":
			if value, ok := stringHint(rawValue); ok {
				params.SafetyIdentifier = openai.String(value)
			}
		case "prompt_cache_key":
			if value, ok := stringHint(rawValue); ok {
				params.PromptCacheKey = openai.String(value)
			}
		case "reasoning_effort":
			if value, ok := stringHint(rawValue); ok {
				params.ReasoningEffort = shared.ReasoningEffort(value)
			}
		case "service_tier":
			if value, ok := stringHint(rawValue); ok {
				params.ServiceTier = openai.ChatCompletionNewParamsServiceTier(value)
			}
		case "verbosity":
			if value, ok := stringHint(rawValue); ok {
				params.Verbosity = openai.ChatCompletionNewParamsVerbosity(value)
			}
		case "stop":
			if union, ok := stopHint(rawValue); ok {
				params.Stop = union
			}
		case "enable_thinking":
			if value, ok := boolHint(rawValue); ok {
				extraFields["enable_thinking"] = value
			}
		case "include_reasoning":
			if value, ok := boolHint(rawValue); ok {
				extraFields["include_reasoning"] = value
			}
		default:
			if rawValue != nil {
				extraFields[originalKey] = rawValue
			}
		}
	}
	if len(extraFields) > 0 {
		params.SetExtraFields(extraFields)
	}
}

// mergeProviderHints merges adapter defaults, model-specific defaults, and request overrides in order.
// mergeProviderHints 用于按顺序合并适配器默认参数、模型级默认参数和请求级覆盖参数。
func mergeProviderHints(base map[string]any, modelParams map[string]map[string]any, model string, request map[string]any) map[string]any {
	// Build one merged hint set so request-level overrides always win over configured defaults.
	// 组装一份合并后的参数集，保证请求级覆盖始终优先于配置默认值。
	merged := cloneHintMap(base)
	if overrides, ok := modelParams[strings.TrimSpace(model)]; ok {
		for key, value := range overrides {
			merged[key] = value
		}
	}
	for key, value := range request {
		merged[key] = value
	}
	return merged
}

// cloneHintMap copies one flat hint map so adapter-level defaults are never mutated by requests.
// cloneHintMap 用于复制一份扁平参数表，避免请求覆盖时篡改适配器默认参数。
func cloneHintMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	return maps.Clone(input)
}

// cloneNestedHintMap copies one nested model-parameter map so model defaults stay immutable at runtime.
// cloneNestedHintMap 用于复制模型级嵌套参数表，保证运行时不会修改模型默认参数。
func cloneNestedHintMap(input map[string]map[string]any) map[string]map[string]any {
	if len(input) == 0 {
		return map[string]map[string]any{}
	}
	cloned := make(map[string]map[string]any, len(input))
	for model, params := range input {
		cloned[strings.TrimSpace(model)] = cloneHintMap(params)
	}
	return cloned
}

// requestOptionsFromContext forwards the current trace ID through both provider correlation headers.
// requestOptionsFromContext 用于通过两个 provider 关联请求头透传当前 trace ID。
func requestOptionsFromContext(ctx context.Context) []option.RequestOption {
	// Forward trace identifiers to the provider for cross-system request correlation.
	// 将 trace 标识透传给模型提供方，便于跨系统请求关联。
	traceID := strings.TrimSpace(trace.IDFromContext(ctx))
	if traceID == "" {
		return nil
	}
	return []option.RequestOption{
		option.WithHeader("X-Trace-ID", traceID),
		option.WithHeader("X-Client-Request-Id", traceID),
	}
}

// floatHint converts supported integer and floating-point hint values into float64 without accepting strings.
// floatHint 用于把受支持的整数与浮点 hint 值转换为 float64，且不接受字符串。
func floatHint(value any) (float64, bool) {
	switch tv := value.(type) {
	case float64:
		return tv, true
	case float32:
		return float64(tv), true
	case int:
		return float64(tv), true
	case int64:
		return float64(tv), true
	case int32:
		return float64(tv), true
	default:
		return 0, false
	}
}

// intHint converts supported numeric hint values into the SDK's int64 representation.
// intHint 用于把受支持的数值 hint 转换为 SDK 使用的 int64 表示。
func intHint(value any) (int64, bool) {
	switch tv := value.(type) {
	case int:
		return int64(tv), true
	case int64:
		return tv, true
	case int32:
		return int64(tv), true
	case float64:
		return int64(tv), true
	case float32:
		return int64(tv), true
	default:
		return 0, false
	}
}

// boolHint accepts only native boolean hint values so configuration type errors remain visible.
// boolHint 仅接受原生布尔 hint 值，使配置类型错误保持可见。
func boolHint(value any) (bool, bool) {
	tv, ok := value.(bool)
	return tv, ok
}

// stringHint returns a trimmed non-empty string hint and rejects other value types.
// stringHint 用于返回清理后的非空字符串 hint，并拒绝其他值类型。
func stringHint(value any) (string, bool) {
	tv, ok := value.(string)
	if !ok {
		return "", false
	}
	tv = strings.TrimSpace(tv)
	if tv == "" {
		return "", false
	}
	return tv, true
}

// stopHint normalizes one string or a string list into the SDK stop-sequence union while dropping blank entries.
// stopHint 用于把单个字符串或字符串列表规范为 SDK 停止序列联合类型，并丢弃空项。
func stopHint(value any) (openai.ChatCompletionNewParamsStopUnion, bool) {
	switch tv := value.(type) {
	case string:
		if stop := strings.TrimSpace(tv); stop != "" {
			return openai.ChatCompletionNewParamsStopUnion{OfString: openai.String(stop)}, true
		}
	case []string:
		stops := make([]string, 0, len(tv))
		for _, item := range tv {
			if item = strings.TrimSpace(item); item != "" {
				stops = append(stops, item)
			}
		}
		if len(stops) > 0 {
			return openai.ChatCompletionNewParamsStopUnion{OfStringArray: stops}, true
		}
	case []any:
		stops := make([]string, 0, len(tv))
		for _, item := range tv {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if text = strings.TrimSpace(text); text != "" {
				stops = append(stops, text)
			}
		}
		if len(stops) > 0 {
			return openai.ChatCompletionNewParamsStopUnion{OfStringArray: stops}, true
		}
	}
	return openai.ChatCompletionNewParamsStopUnion{}, false
}
