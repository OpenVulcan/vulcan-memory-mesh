package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type IntentExtractor struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
	maxKws  int
}

func NewIntentExtractor(llm appports.LLMClient, prompts appports.PromptSource, model string, maxKeywords int) *IntentExtractor {
	if maxKeywords <= 0 {
		maxKeywords = 5
	}
	if maxKeywords > 10 {
		maxKeywords = 10
	}
	return &IntentExtractor{llm: llm, prompts: prompts, model: strings.TrimSpace(model), maxKws: maxKeywords}
}

func (e *IntentExtractor) Extract(ctx context.Context, history []logicdomain.HistorySnippet, current string) (logicdomain.IntentResult, error) {
	if e.llm == nil {
		return logicdomain.IntentResult{}, fmt.Errorf("llm client is nil")
	}
	prompt, err := e.prompts.GetPrompt("extract_intent", e.model)
	if err != nil {
		return logicdomain.IntentResult{}, fmt.Errorf("load extract_intent prompt: %w", err)
	}
	resp, err := e.llm.Generate(ctx, appports.LLMRequest{Model: e.model, SystemPrompt: prompt, UserPrompt: renderIntentUserPrompt(history, current, e.maxKws), ResponseFormat: appports.LLMResponseFormatJSON})
	if err != nil {
		return logicdomain.IntentResult{}, err
	}
	intent, err := parseIntentResponse(resp.Content)
	if err != nil {
		return logicdomain.IntentResult{}, err
	}
	if len(intent.Keywords) > e.maxKws {
		intent.Keywords = intent.Keywords[:e.maxKws]
	}
	return intent, nil
}

func parseIntentResponse(raw string) (logicdomain.IntentResult, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		Keywords   []string `json:"keywords"`
		NeedMemory *bool    `json:"need_memory"`
		Reason     string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: "json decode failed", Raw: raw}
	}
	if payload.NeedMemory == nil {
		return logicdomain.IntentResult{}, logicdomain.InvalidLLMOutputError{Scene: "extract_intent", Message: "missing need_memory", Raw: raw}
	}
	keywords := make([]string, 0, len(payload.Keywords))
	seen := map[string]struct{}{}
	for _, kw := range payload.Keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if _, ok := seen[kw]; ok {
			continue
		}
		seen[kw] = struct{}{}
		keywords = append(keywords, kw)
	}
	return logicdomain.IntentResult{Keywords: keywords, NeedMemory: *payload.NeedMemory, Reason: strings.TrimSpace(payload.Reason)}, nil
}

func extractJSONObject(raw string) (string, error) {
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
