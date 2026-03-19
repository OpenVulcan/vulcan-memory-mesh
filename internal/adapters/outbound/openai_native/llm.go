package openai_native

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type LLMClient struct {
	client *Client
	model  string
}

func NewLLMClient(endpoint, apiKey, model, organization, project string) *LLMClient {
	return &LLMClient{client: NewClient(endpoint, apiKey, organization, project, nil), model: strings.TrimSpace(model)}
}
func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
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
	applyProviderHints(&params, req.ProviderHints, c.client.compatibleMode)
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
	return appports.LLMResponse{
		Content: content,
		Usage: logicdomain.LLMUsage{
			PromptTokens:     int(resp.Usage.PromptTokens),
			CompletionTokens: int(resp.Usage.CompletionTokens),
			TotalTokens:      int(resp.Usage.TotalTokens),
		},
	}, nil
}

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

func applyProviderHints(params *openai.ChatCompletionNewParams, hints map[string]any, compatibleMode bool) {
	if params == nil {
		return
	}
	extraFields := map[string]any{}
	if compatibleMode {
		extraFields["enable_thinking"] = false
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
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
		}
	}
	if len(extraFields) > 0 {
		params.SetExtraFields(extraFields)
	}
}

func requestOptionsFromContext(ctx context.Context) []option.RequestOption {
	traceID := strings.TrimSpace(trace.IDFromContext(ctx))
	if traceID == "" {
		return nil
	}
	return []option.RequestOption{
		option.WithHeader("X-Trace-ID", traceID),
		option.WithHeader("X-Client-Request-Id", traceID),
	}
}

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

func boolHint(value any) (bool, bool) {
	tv, ok := value.(bool)
	return tv, ok
}

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
