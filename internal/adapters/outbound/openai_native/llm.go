// llm.go implements the OpenAI-compatible outbound adapters.
// llm.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerhint"
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
		params:      providerhint.CloneMap(params),
		modelParams: providerhint.CloneNestedMap(modelParams),
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
	applyProviderHints(&params, providerhint.Merge(c.params, c.modelParams[strings.TrimSpace(model)], req.ProviderHints))

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
		Content:   content,
		Model:     strings.TrimSpace(resp.Model),
		RequestID: strings.TrimSpace(resp.ID),
		Usage: logicdomain.LLMUsage{
			PromptTokens:      int(resp.Usage.PromptTokens),
			CompletionTokens:  int(resp.Usage.CompletionTokens),
			TotalTokens:       int(resp.Usage.TotalTokens),
			CachedInputTokens: int(resp.Usage.PromptTokensDetails.CachedTokens),
			ReasoningTokens:   int(resp.Usage.CompletionTokensDetails.ReasoningTokens),
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
	extraFields := map[string]any{}
	for rawKey, rawValue := range hints {
		originalKey := strings.TrimSpace(rawKey)
		key := strings.ToLower(originalKey)
		switch key {
		case "temperature":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.Temperature = openai.Float(value)
			}
		case "top_p":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.TopP = openai.Float(value)
			}
		case "presence_penalty":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.PresencePenalty = openai.Float(value)
			}
		case "frequency_penalty":
			if value, ok := providerhint.Float64(rawValue); ok {
				params.FrequencyPenalty = openai.Float(value)
			}
		case "max_tokens":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.MaxTokens = openai.Int(value)
			}
		case "max_completion_tokens":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.MaxCompletionTokens = openai.Int(value)
			}
		case "n":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.N = openai.Int(value)
			}
		case "seed":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.Seed = openai.Int(value)
			}
		case "logprobs":
			if value, ok := providerhint.Bool(rawValue); ok {
				params.Logprobs = openai.Bool(value)
			}
		case "top_logprobs":
			if value, ok := providerhint.Int64(rawValue); ok {
				params.TopLogprobs = openai.Int(value)
			}
		case "store":
			if value, ok := providerhint.Bool(rawValue); ok {
				params.Store = openai.Bool(value)
			}
		case "parallel_tool_calls":
			if value, ok := providerhint.Bool(rawValue); ok {
				params.ParallelToolCalls = openai.Bool(value)
			}
		case "user":
			if value, ok := providerhint.String(rawValue); ok {
				params.User = openai.String(value)
			}
		case "safety_identifier":
			if value, ok := providerhint.String(rawValue); ok {
				params.SafetyIdentifier = openai.String(value)
			}
		case "prompt_cache_key":
			if value, ok := providerhint.String(rawValue); ok {
				params.PromptCacheKey = openai.String(value)
			}
		case "reasoning_effort":
			params.ReasoningEffort = shared.ReasoningEffort("none")
		case "reasoning":
			if disabled, ok := disabledReasoningObject(rawValue); ok {
				extraFields["reasoning"] = disabled
			}
		case "thinking":
			extraFields["thinking"] = map[string]any{"type": "disabled"}
		case "service_tier":
			if value, ok := providerhint.String(rawValue); ok {
				params.ServiceTier = openai.ChatCompletionNewParamsServiceTier(value)
			}
		case "verbosity":
			if value, ok := providerhint.String(rawValue); ok {
				params.Verbosity = openai.ChatCompletionNewParamsVerbosity(value)
			}
		case "stop":
			if union, ok := stopHint(rawValue); ok {
				params.Stop = union
			}
		case "enable_thinking":
			extraFields["enable_thinking"] = false
		case "include_reasoning":
			// include_reasoning controls response visibility and does not prove reasoning execution is disabled.
			// include_reasoning 仅控制响应可见性，不能证明推理执行已经关闭。
			continue
		default:
			if unregisteredReasoningHint(key) {
				// Unknown reasoning controls are discarded because forwarding them could contradict the declared disabled dialect.
				// 未注册的思考控制字段会被丢弃，因为透传可能与已声明的关闭方言冲突。
				continue
			}
			if rawValue != nil {
				extraFields[originalKey] = rawValue
			}
		}
	}
	if len(extraFields) > 0 {
		params.SetExtraFields(extraFields)
	}
}

// disabledReasoningObject projects one declared reasoning-object dialect to its minimal disabled form.
// disabledReasoningObject 用于把一个已声明的 reasoning 对象方言投影为最小关闭形态。
//
// Parameters:
// 参数：
//   - value: configured reasoning object whose field names declare the provider dialect.
//   - value：通过字段名声明 Provider 方言的 reasoning 配置对象。
//
// Returns:
// 返回值：
//   - map[string]any: minimal effort-none or enabled-false object.
//   - map[string]any：最小的 effort-none 或 enabled-false 对象。
//   - bool: false when the object is missing a unique supported dialect marker.
//   - bool：对象缺少唯一受支持方言标记时为 false。
func disabledReasoningObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	hasEffort := false
	hasEnabled := false
	for rawKey := range object {
		switch strings.ToLower(strings.TrimSpace(rawKey)) {
		case "effort":
			hasEffort = true
		case "enabled":
			hasEnabled = true
		}
	}
	if hasEffort == hasEnabled {
		return nil, false
	}
	if hasEffort {
		return map[string]any{"effort": "none"}, true
	}
	return map[string]any{"enabled": false}, true
}

// unregisteredReasoningHint reports whether an otherwise unknown provider hint could control hidden model reasoning.
// unregisteredReasoningHint 用于判断一个其他未知 Provider hint 是否可能控制模型隐藏思考。
//
// Parameters:
// 参数：
//   - normalizedKey: trimmed lowercase top-level provider field name.
//   - normalizedKey：已去除空白并转为小写的 Provider 顶层字段名。
//
// Returns:
// 返回值：
//   - bool: true when the field name contains the closed reasoning or thinking control roots.
//   - bool：字段名包含封闭的 reasoning 或 thinking 控制词根时返回 true。
func unregisteredReasoningHint(normalizedKey string) bool {
	compact := strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalizedKey)
	return strings.Contains(compact, "reasoning") || strings.Contains(compact, "thinking")
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
