// precheck_prompt_hints.go extracts deterministic context hints for the first-stage pre-check prompt.
// precheck_prompt_hints.go 用于为 pre-check 第一层提示词提取确定性的情境 hints。
package processor

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

var (
	// preCheckWeakHintTexts filters out purely deictic or filler fragments so prompt hints emphasize durable anchors instead of conversational glue.
	// preCheckWeakHintTexts 用于过滤纯指代或填充片段，让提示词 hints 强调稳定锚点而不是对话胶水。
	preCheckWeakHintTexts = map[string]struct{}{
		"这个": {}, "这个改动": {}, "这个地方": {}, "这个问题": {}, "这个方案": {}, "这个实现": {}, "这个逻辑": {}, "这个流程": {},
		"这里": {}, "这样": {}, "这种": {}, "这一块": {}, "这块": {}, "这段": {}, "这一段": {}, "上面": {}, "前面": {}, "后面": {},
		"刚才": {}, "刚刚": {}, "当前": {}, "为什么": {}, "怎么": {}, "如何": {}, "是否": {}, "会不会": {},
	}
)

// extractPreCheckContextHints converts one raw text into a short, de-duplicated hint list so the first-stage prompt can preserve concrete anchors without relying on another model call.
// extractPreCheckContextHints 用于把一段原始文本转换成简短且去重的 hint 列表，让第一层提示词无需额外模型调用也能保留具体锚点。
func extractPreCheckContextHints(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	normalized := textutil.NormalizeWhitespace(text)
	if raw := strings.TrimSpace(text); raw != "" && json.Valid([]byte(raw)) {
		var payload struct {
			User      any `json:"user"`
			Assistant any `json:"assistant"`
			Details   any `json:"details"`
			Content   any `json:"content"`
			Timeline  []struct {
				Content any `json:"content"`
			} `json:"timeline"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			parts := make([]string, 0, 2+len(payload.Timeline))
			if text := textutil.ExtractTextFromAny(payload.User); text != "" {
				parts = append(parts, text)
			}
			for _, item := range payload.Timeline {
				if text := textutil.ExtractTextFromAny(item.Content); text != "" {
					parts = append(parts, text)
				}
			}
			if text := textutil.ExtractTextFromAny(payload.Assistant); text != "" {
				parts = append(parts, text)
			}
			if text := textutil.ExtractTextFromAny(payload.Details); text != "" {
				parts = append(parts, text)
			}
			if text := textutil.ExtractTextFromAny(payload.Content); text != "" {
				parts = append(parts, text)
			}
			normalized = textutil.NormalizeWhitespace(strings.Join(parts, " "))
		}
	}
	if normalized == "" {
		return nil
	}

	// Keep one short list that first preserves full clauses, then backfills with stronger tokens when the sentence itself is too broad.
	// 先保留完整短语，再在句子过宽时补充更强的 token，确保 hints 既能保上下文又不至于失真。
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendHint := func(value string) {
		value = truncatePreCheckContextHint(textutil.NormalizeWhitespace(value), 48)
		if value == "" || isWeakPreCheckContextHint(value) || len(out) >= limit {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	appendHint(normalized)
	splitter := strings.NewReplacer(
		"，", "\n", ",", "\n",
		"。", "\n", "；", "\n", ";", "\n",
		"：", "\n", ":", "\n", "、", "\n",
		"（", "\n", "）", "\n", "(", "\n", ")", "\n",
		"？", "\n", "?", "\n", "！", "\n", "!", "\n",
	)
	for _, segment := range strings.Split(splitter.Replace(normalized), "\n") {
		appendHint(segment)
	}
	for _, token := range textutil.Tokenize(normalized) {
		if len(out) >= limit {
			break
		}
		token = textutil.NormalizeWhitespace(token)
		if token == "" || isWeakPreCheckContextHint(token) {
			continue
		}
		if !strings.ContainsAny(token, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_-") && utf8.RuneCountInString(token) > 20 {
			continue
		}
		appendHint(token)
	}
	return out
}

// extractRecentTurnContextHints pulls a compact cross-turn hint list from the recent mixed window so the first-stage prompt can reason about recency anchors without reading every long span equally.
// extractRecentTurnContextHints 用于从最近混合 turn 窗口中提取紧凑 hints，让第一层提示词在不平均放大每段长文本的情况下仍能感知最近上下文锚点。
func extractRecentTurnContextHints(turns []logicdomain.PreCheckTurnContext, limit int) []string {
	if limit <= 0 || len(turns) == 0 {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendHint := func(value string) {
		value = textutil.NormalizeWhitespace(value)
		if value == "" || len(out) >= limit {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	// Walk from newest to oldest so the hints emphasize the freshest turns that are most likely to explain short deictic requests.
	// 按从新到旧的顺序提取，让 hints 优先强调最可能解释短指代请求的最新 turn。
	for idx := len(turns) - 1; idx >= 0 && len(out) < limit; idx-- {
		for _, hint := range extractPreCheckContextHints(turns[idx].Content, 2) {
			appendHint(hint)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// isWeakPreCheckContextHint rejects fragments that are too deictic or too short to help the prompt preserve retrieval anchors.
// isWeakPreCheckContextHint 用于剔除过于指代化或过短的片段，避免这些内容污染检索锚点。
func isWeakPreCheckContextHint(value string) bool {
	value = strings.ToLower(textutil.NormalizeWhitespace(value))
	if value == "" {
		return true
	}
	if _, ok := preCheckWeakHintTexts[value]; ok {
		return true
	}
	return utf8.RuneCountInString(value) <= 1
}

// truncatePreCheckContextHint bounds one hint length so prompt-side context remains readable and stable even when raw turn content is long.
// truncatePreCheckContextHint 用于限制单条 hint 的长度，避免原始 turn 很长时把提示词上下文撑得不可读或不稳定。
func truncatePreCheckContextHint(value string, limit int) string {
	value = textutil.NormalizeWhitespace(value)
	if value == "" || limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit]))
}
