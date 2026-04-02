// precheck_intent.go stabilizes stage-one pre-check intent results before they enter unified memory retrieval.
// precheck_intent.go 用于在第一层 pre-check 意图结果进入统一记忆检索前做稳定化归一。
package usecase

import (
	"strings"
	"unicode/utf8"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

var (
	// preCheckGenericQueryFragments lists deictic phrases that usually point to the immediate conversation and should not be sent to long-term retrieval verbatim.
	// preCheckGenericQueryFragments 用于列出通常只指向即时对话的指代表达，避免它们原样进入长期记忆检索。
	preCheckGenericQueryFragments = []string{
		"这个", "这里", "这样", "这种", "这块", "这一块", "这段", "这一段",
		"这个改动", "这个地方", "这个问题", "这个方案", "这个实现", "这个逻辑", "这个流程",
		"上面", "前面", "后面", "刚才", "刚刚",
	}

	// preCheckLongTermAnchorMarkers lists words that usually imply broader history, durable rules, or concrete technical anchors, so they should keep retrieval enabled.
	// preCheckLongTermAnchorMarkers 用于列出通常意味着更广历史、稳定规则或明确技术锚点的词，命中后应保留检索能力。
	preCheckLongTermAnchorMarkers = []string{
		"之前", "以前", "历史", "长期", "记忆", "偏好", "规则", "约定", "规范", "约束",
		"架构", "决策", "兼容", "版本", "配置", "迁移", "schema",
		"grpc", "http", "tls", "sqlite", "duckdb", "lancedb", "phase",
	}
)

// normalizePreCheckIntentResult applies low-risk server-side guards so stage-one intent can benefit from prompt flexibility without letting vague queries over-trigger retrieval.
// normalizePreCheckIntentResult 用于施加低风险的服务端兜底，让第一层意图既保留提示词灵活性，又避免模糊 query 过度触发检索。
func normalizePreCheckIntentResult(intent logicdomain.IntentResult, current string, recentTurns []logicdomain.PreCheckTurnContext) logicdomain.IntentResult {
	current = textutil.NormalizeWhitespace(current)
	intent.Reason = textutil.NormalizeWhitespace(intent.Reason)
	if !intent.NeedMemory {
		intent.Queries = nil
		return intent
	}

	// First normalize and de-duplicate the returned search sentences so clearly deictic model outputs can fall back to the current request wording.
	// 先把返回的检索语句做归一和去重，让明显指代化的模型输出回退到当前请求原文。
	intent.Queries = normalizePreCheckIntentQueries(intent.Queries, current)
	if len(intent.Queries) == 0 {
		if len(recentTurns) > 0 && looksLikeImmediateContextOnlyRequest(current) {
			intent.NeedMemory = false
			return intent
		}
		if current != "" {
			intent.Queries = []string{current}
		}
		return intent
	}

	// If stage one only produced a vague echo of the current message and the request itself looks like immediate recency-only follow-up, skip retrieval altogether.
	// 如果第一层只留下了对当前消息的模糊复述，且当前请求本身看起来只是即时上下文追问，则整体跳过检索。
	if len(intent.Queries) == 1 && current != "" && intent.Queries[0] == current && len(recentTurns) > 0 && looksLikeImmediateContextOnlyRequest(current) {
		intent.NeedMemory = false
		intent.Queries = nil
	}
	return intent
}

// normalizePreCheckIntentQueries rewrites generic deictic queries into the current request and removes duplicates so the downstream retrieval surface receives stable, context-preserving inputs.
// normalizePreCheckIntentQueries 用于把泛化指代 query 改写成当前请求并去重，让下游检索面接收到稳定且保留上下文的输入。
func normalizePreCheckIntentQueries(queries []string, current string) []string {
	out := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = textutil.NormalizeWhitespace(query)
		if query == "" {
			continue
		}
		if looksLikeGenericPreCheckQuery(query) && current != "" {
			query = current
		}
		if query == "" {
			continue
		}
		if _, ok := seen[query]; ok {
			continue
		}
		seen[query] = struct{}{}
		out = append(out, query)
	}
	return out
}

// looksLikeImmediateContextOnlyRequest conservatively detects short deictic follow-up questions that are more likely explained by the recent turn window than by long-term memory.
// looksLikeImmediateContextOnlyRequest 用于保守识别短促的指代式追问，这类请求更可能由最近 turn 窗口解释，而不是依赖长期记忆。
func looksLikeImmediateContextOnlyRequest(current string) bool {
	current = strings.ToLower(textutil.NormalizeWhitespace(current))
	if current == "" {
		return false
	}
	if !containsPreCheckMarker(current, preCheckGenericQueryFragments) {
		return false
	}
	if hasExplicitPreCheckQueryAnchor(current) {
		return false
	}
	return utf8.RuneCountInString(current) <= 18
}

// looksLikeGenericPreCheckQuery identifies stage-one outputs that still read like conversation-local references and therefore should be rewritten before retrieval.
// looksLikeGenericPreCheckQuery 用于识别仍然像会话内指代的第一层输出，这类 query 在进入检索前应先被改写。
func looksLikeGenericPreCheckQuery(query string) bool {
	query = strings.ToLower(textutil.NormalizeWhitespace(query))
	if query == "" {
		return false
	}
	if !containsPreCheckMarker(query, preCheckGenericQueryFragments) {
		return false
	}
	return !hasExplicitPreCheckQueryAnchor(query)
}

// hasExplicitPreCheckQueryAnchor checks whether the query already carries stable technical or historical anchors, so it is safe to keep retrieval enabled.
// hasExplicitPreCheckQueryAnchor 用于判断 query 是否已经带有稳定的技术或历史锚点，若有则可以安全保留检索。
func hasExplicitPreCheckQueryAnchor(query string) bool {
	query = strings.ToLower(textutil.NormalizeWhitespace(query))
	if query == "" {
		return false
	}
	if containsPreCheckMarker(query, preCheckLongTermAnchorMarkers) {
		return true
	}
	strongTokens := 0
	for _, token := range textutil.Tokenize(query) {
		token = strings.TrimSpace(token)
		if token == "" || containsPreCheckMarker(token, preCheckGenericQueryFragments) {
			continue
		}
		if strings.ContainsAny(token, "0123456789abcdefghijklmnopqrstuvwxyz_-") && len(token) >= 2 {
			return true
		}
		if utf8.RuneCountInString(token) >= 2 {
			strongTokens++
			if strongTokens >= 2 {
				return true
			}
		}
	}
	return false
}

// containsPreCheckMarker reports whether one normalized text contains any normalized marker from the provided list.
// containsPreCheckMarker 用于判断一段规范化文本是否包含给定列表中的任一规范化标记。
func containsPreCheckMarker(text string, markers []string) bool {
	for _, marker := range markers {
		marker = strings.ToLower(textutil.NormalizeWhitespace(marker))
		if marker != "" && strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
