// request_cost.go implements request-cost estimation helpers used by per-key budget prechecks before outbound AI calls are sent.
// request_cost.go 用于实现请求成本估算助手，让每个 Key 的预算预检查能在真正发出 AI 请求前先判断额度是否足够。
package ai_key_failover

import (
	"math"
	"strconv"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

// defaultRequestTokenEstimator is the shared conservative estimator used to approximate TPM consumption before one upstream request is sent.
// defaultRequestTokenEstimator 用于保存共享的保守 token 估算器，让系统在真正发送上游请求前近似计算 TPM 消耗。
var defaultRequestTokenEstimator = textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())

// requestCost stores the in-memory RPM/TPM/RPD reservation envelope consumed by one outbound attempt.
// requestCost 用于保存单次出站尝试在内存态 RPM/TPM/RPD 预算里占用的保留额度。
type requestCost struct {
	Requests int
	Tokens   int
}

// normalizeRequestCost canonicalizes one request-cost envelope so routing always reserves at least one request while keeping token counts non-negative.
// normalizeRequestCost 用于规范化一份请求成本，保证路由至少预留一次请求，并且 token 计数不会出现负值。
func normalizeRequestCost(cost requestCost) requestCost {
	if cost.Requests <= 0 {
		cost.Requests = 1
	}
	if cost.Tokens < 0 {
		cost.Tokens = 0
	}
	return cost
}

// estimateLLMRequestCost approximates prompt and completion usage so per-key TPM guards can switch routes before the provider reports a hard limit.
// estimateLLMRequestCost 用于近似估算提示词与输出成本，让每个 Key 的 TPM 守卫能在提供方报硬限之前就提前切换路线。
func estimateLLMRequestCost(systemPrompt, userPrompt string, hints map[string]any) requestCost {
	promptTokens := estimateTextTokens(systemPrompt, userPrompt)
	completionTokens := maxHintInt(hints, "max_completion_tokens", "max_tokens")
	if completionTokens > 0 {
		if n := hintInt(hints, "n"); n > 1 {
			completionTokens *= n
		}
	}
	return normalizeRequestCost(requestCost{
		Requests: 1,
		Tokens:   promptTokens + completionTokens,
	})
}

// estimateEmbeddingRequestCost approximates the total embedding input load for one batch request so per-key TPM guards can pre-skip exhausted routes.
// estimateEmbeddingRequestCost 用于近似估算单次批量 embedding 请求的输入负载，让每个 Key 的 TPM 守卫可以提前跳过已耗尽的路线。
func estimateEmbeddingRequestCost(texts []string) requestCost {
	return normalizeRequestCost(requestCost{
		Requests: 1,
		Tokens:   estimateTextTokens(texts...),
	})
}

// estimateRerankRequestCost approximates the rerank prompt load from the query plus candidate documents so per-key TPM accounting remains conservative.
// estimateRerankRequestCost 用于从 query 与候选文档近似估算 rerank 负载，保证每个 Key 的 TPM 记账保持保守。
func estimateRerankRequestCost(query string, docs []appports.RerankerDocument) requestCost {
	segments := make([]string, 0, len(docs)+1)
	segments = append(segments, query)
	for _, doc := range docs {
		segments = append(segments, doc.Text)
	}
	return normalizeRequestCost(requestCost{
		Requests: 1,
		Tokens:   estimateTextTokens(segments...),
	})
}

// llmActualUsageCost reconciles the optimistic pre-reservation with provider-reported LLM usage, or falls back to 1.3x input tokens when usage is absent.
// llmActualUsageCost 用于把乐观预留与提供方返回的 LLM 实际用量对齐；若 usage 缺失，则回退到输入 token 的 1.3 倍。
func llmActualUsageCost(resp appports.LLMResponse, inputTokens int) requestCost {
	totalTokens := resp.Usage.TotalTokens
	if totalTokens <= 0 {
		totalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	}
	if totalTokens <= 0 {
		totalTokens = estimateMissingLLMUsageTokens(inputTokens)
	}
	return normalizeRequestCost(requestCost{
		Requests: 1,
		Tokens:   totalTokens,
	})
}

// estimateMissingLLMUsageTokens applies a conservative 1.3x multiplier to input tokens when one upstream omits usage accounting.
// estimateMissingLLMUsageTokens 用于在上游未返回 usage 统计时，对输入 token 应用保守的 1.3 倍系数。
func estimateMissingLLMUsageTokens(inputTokens int) int {
	if inputTokens <= 0 {
		return 0
	}
	return int(math.Ceil(float64(inputTokens) * 1.3))
}

// estimateTextTokens sums conservative token estimates for a list of text segments while skipping empty strings.
// estimateTextTokens 用于累加多段文本的保守 token 估算值，并自动跳过空字符串。
func estimateTextTokens(texts ...string) int {
	total := 0
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		total += defaultRequestTokenEstimator.Estimate(text)
	}
	return total
}

// maxHintInt returns the first positive integer hint among the provided keys so route estimation can reuse provider hint conventions.
// maxHintInt 用于返回给定键集合里的首个正整数 hint，让路线估算可以复用 provider hint 约定。
func maxHintInt(hints map[string]any, keys ...string) int {
	for _, key := range keys {
		if value := hintInt(hints, key); value > 0 {
			return value
		}
	}
	return 0
}

// hintInt parses one integer-like hint key from a flat provider-hint map without panicking on unexpected JSON-decoded numeric shapes.
// hintInt 用于从扁平 provider-hint 表中解析类整数键，并兼容 JSON 解码后常见的数值形态而不 panic。
func hintInt(hints map[string]any, key string) int {
	if len(hints) == 0 {
		return 0
	}
	value, ok := hints[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
}
