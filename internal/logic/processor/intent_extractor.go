// intent_extractor.go implements reusable business processors.
// intent_extractor.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// IntentExtractor drives prompt lookup, LLM invocation, and JSON parsing for the first-stage pre-check scene that expands recent turns into search queries.
// IntentExtractor 用于驱动 pre-check 第一层场景的提示词读取、LLM 调用和 JSON 解析，把最近 turn 窗口展开成检索语句。
type IntentExtractor struct {
	llm     logicports.LLMClient
	prompts logicports.PromptSource
	model   string
	maxKws  int
}

// NewIntentExtractor creates a IntentExtractor instance.
// NewIntentExtractor 用于创建 IntentExtractor 实例。
func NewIntentExtractor(llm logicports.LLMClient, prompts logicports.PromptSource, model string, maxKeywords int) *IntentExtractor {
	if maxKeywords <= 0 {
		maxKeywords = 5
	}
	if maxKeywords > 10 {
		maxKeywords = 10
	}
	return &IntentExtractor{llm: llm, prompts: prompts, model: strings.TrimSpace(model), maxKws: maxKeywords}
}

// Extract runs the first-stage pre-check prompt over the mixed recent-turn window plus the current user request.
// Extract 用于基于混合最近 turn 窗口和当前用户请求，执行 pre-check 第一层提示词。
func (e *IntentExtractor) Extract(ctx context.Context, turns []logicdomain.PreCheckTurnContext, current string) (logicdomain.IntentResult, error) {
	// Load the scene prompt and issue a structured JSON generation request to the LLM.
	// 加载场景提示词，并向 LLM 发起结构化 JSON 生成请求。
	if e.llm == nil {
		return logicdomain.IntentResult{}, fmt.Errorf("llm client is nil")
	}
	prompt, err := e.prompts.GetPrompt("extract_intent", e.model)
	if err != nil {
		return logicdomain.IntentResult{}, fmt.Errorf("load extract_intent prompt: %w", err)
	}
	resp, err := e.llm.Generate(ctx, logicports.LLMRequest{
		Model:               e.model,
		SystemPrompt:        prompt,
		UserPrompt:          renderIntentUserPrompt(turns, current, e.maxKws),
		ResponseFormat:      logicports.LLMResponseFormatJSON,
		RouteSelectionLevel: logicports.LLMRouteSelectionLevelPreCheckL1,
	})
	if err != nil {
		return logicdomain.IntentResult{}, err
	}

	// Parse and clamp the resulting intent payload before returning it to the use case layer.
	// 解析并裁剪返回的意图载荷，再交还给用例层。
	intent, err := parseIntentResponse(resp.Content)
	if err != nil {
		return logicdomain.IntentResult{}, err
	}
	if len(intent.Queries) > e.maxKws {
		intent.Queries = intent.Queries[:e.maxKws]
	}
	return intent, nil
}

// parseIntentResponse executes the parseIntentResponse logic.
// parseIntentResponse 用于执行 parseIntentResponse 逻辑。
func parseIntentResponse(raw string) (logicdomain.IntentResult, error) {
	// Extract the JSON object from noisy model output before decoding fields.
	// 先从带噪声的模型输出中抽取 JSON 对象，再解码字段。
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		Queries    []string `json:"queries"`
		Keywords   []string `json:"keywords"`
		NeedMemory *bool    `json:"need_memory"`
		Reason     string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: "json decode failed", Raw: raw}
	}

	// Validate required fields and normalize keywords into a de-duplicated list.
	// 校验必填字段，并将关键词归一为去重后的列表。
	if payload.NeedMemory == nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: "missing need_memory", Raw: raw}
	}
	sourceQueries := payload.Queries
	if len(sourceQueries) == 0 {
		sourceQueries = payload.Keywords
	}
	queries := make([]string, 0, len(sourceQueries))
	seen := map[string]struct{}{}
	for _, query := range sourceQueries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		if _, ok := seen[query]; ok {
			continue
		}
		seen[query] = struct{}{}
		queries = append(queries, query)
	}
	return logicdomain.IntentResult{Queries: queries, NeedMemory: *payload.NeedMemory, Reason: strings.TrimSpace(payload.Reason)}, nil
}

// extractJSONObject extracts the target data.
// extractJSONObject 用于提取目标数据。
func extractJSONObject(raw string) (string, error) {
	// Trim markdown fences and then locate the first balanced JSON object.
	// 去除 markdown 围栏后，再定位第一个平衡的 JSON 对象。
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return "", fmt.Errorf("empty content")
	}
	cleaned = stripMarkdownFences(cleaned)
	start := strings.Index(cleaned, "{")
	if start < 0 {
		return "", fmt.Errorf("json object start not found")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(cleaned); i++ {
		ch := cleaned[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return strings.TrimSpace(cleaned[start : i+1]), nil
			}
		}
	}
	return "", fmt.Errorf("json object end not found")
}

func stripMarkdownFences(raw string) string {
	// Remove common fenced-code wrappers that some models prepend around JSON.
	// 去除模型在 JSON 外层常见的 fenced code 包装。
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		if firstNewline := strings.Index(trimmed, "\n"); firstNewline >= 0 {
			trimmed = trimmed[firstNewline+1:]
		}
		if lastFence := strings.LastIndex(trimmed, "```"); lastFence >= 0 {
			trimmed = trimmed[:lastFence]
		}
	}
	trimmed = strings.ReplaceAll(trimmed, "```json", "")
	trimmed = strings.ReplaceAll(trimmed, "```JSON", "")
	trimmed = strings.ReplaceAll(trimmed, "```", "")
	return strings.TrimSpace(trimmed)
}
