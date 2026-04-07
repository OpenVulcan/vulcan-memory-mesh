// embedding.go implements the fixed-model embedding wrapper that rotates API keys without ever mixing embedding models.
// embedding.go 用于实现固定模型的 embedding 包装器，让系统在绝不混用 embedding 模型的前提下轮换 API Key。
package ai_key_failover

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	// embeddingRetryTruncationAttempts keeps the single-text fallback bounded so one pathological payload cannot loop forever after repeated upstream length rejections.
	// embeddingRetryTruncationAttempts 用于限制单条文本的回退截断次数，避免异常载荷在上游持续报长度错误时无限重试。
	embeddingRetryTruncationAttempts = 6

	// embeddingRetryMinimumTokenBudget keeps the truncation fallback from collapsing into an unusably tiny payload while it progressively shrinks one overlong text.
	// embeddingRetryMinimumTokenBudget 用于限制截断回退的最小 token 预算，避免逐步收缩时把文本压成几乎不可用的极小片段。
	embeddingRetryMinimumTokenBudget = 32

	// embeddingRetryTruncationMarker marks provider-triggered fallback clipping so stored logs and downstream diagnostics can tell this text was shortened only for one retry.
	// embeddingRetryTruncationMarker 用于标记由 provider 长度错误触发的回退裁剪，方便日志和后续诊断区分这类“仅为重试而缩短”的文本。
	embeddingRetryTruncationMarker = "\n...[truncated for embedding retry]...\n"
)

var (
	// embeddingRetryConfiguredBudgetSteps keeps the configured per-text token budget as the first retry target, then progressively shrinks it when the provider still rejects the payload.
	// embeddingRetryConfiguredBudgetSteps 用于在存在显式单条 token 预算时，先以该预算作为首轮重试目标；若 provider 仍拒绝，则继续渐进收缩。
	embeddingRetryConfiguredBudgetSteps = [...]float64{1.0, 0.85, 0.7, 0.55, 0.4, 0.3}

	// embeddingRetryEstimatedBudgetSteps derives one fallback retry budget from the local estimator only after the provider has already confirmed the original payload is too long.
	// embeddingRetryEstimatedBudgetSteps 用于在 provider 已经确认原始载荷过长后，再基于本地估算器推导一组回退预算。
	embeddingRetryEstimatedBudgetSteps = [...]float64{0.75, 0.6, 0.45, 0.33, 0.24, 0.16}

	// embeddingRetryRuneClipSteps keeps the emergency rune-level shrinking ratios used when token-budget clipping still cannot force the payload to become shorter.
	// embeddingRetryRuneClipSteps 用于保存 token 预算裁剪仍无法让文本变短时，最后启用的 rune 级紧急收缩比例。
	embeddingRetryRuneClipSteps = [...]float64{0.75, 0.6, 0.45, 0.33, 0.24, 0.16}

	// embeddingRetryTokenEstimator reuses the shared conservative estimator only for fallback clipping, never as the authoritative tokenizer that decides whether a request may be sent.
	// embeddingRetryTokenEstimator 仅在回退裁剪阶段复用共享保守估算器，而不会把它当成发送前的权威 tokenizer。
	embeddingRetryTokenEstimator = textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
)

// EmbeddingClient wraps one fixed provider/endpoint/model/dimension tuple with in-memory API-key failover.
// EmbeddingClient 用于把一个固定 provider/endpoint/model/dimension 组合包装成带内存态 API Key 容灾能力的 embedding 客户端。
type EmbeddingClient struct {
	selector              *selector
	endpoint              string
	model                 string
	dimension             int
	maxBatchSize          int
	maxInputTokensPerText int
	organization          string
	project               string
	params                map[string]any
	modelParams           map[string]map[string]any
	factory               func(string) appports.EmbeddingClient
	classify              func(error, time.Time) failureDecision
	clients               map[string]appports.EmbeddingClient
	mu                    sync.Mutex
	options               Options
}

// NewEmbeddingClient creates one fixed-model embedding client that can rotate across multiple API keys of the same upstream configuration.
// NewEmbeddingClient 用于创建一个固定模型 embedding 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewEmbeddingClient(endpoint, model string, dimension, maxBatchSize, maxInputTokensPerText int, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*EmbeddingClient, error) {
	return NewProviderEmbeddingClient("openai", endpoint, model, dimension, maxBatchSize, maxInputTokensPerText, organization, project, apiKeys, params, modelParams, options)
}

// NewProviderEmbeddingClient creates one provider-aware fixed-model embedding client so the same key-failover shell can serve OpenAI-compatible and Google AI Studio native backends.
// NewProviderEmbeddingClient 用于创建一个 provider 感知的固定模型 embedding 客户端，让同一套 key-failover 外壳可以同时服务 OpenAI-compatible 与 Google AI Studio 原生后端。
func NewProviderEmbeddingClient(provider, endpoint, model string, dimension, maxBatchSize, maxInputTokensPerText int, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*EmbeddingClient, error) {
	options.ServiceName = "embedding"
	if len(options.Nodes) == 0 {
		options.APIKeys = append([]string(nil), apiKeys...)
	}
	selector, err := newSelector(options)
	if err != nil {
		return nil, err
	}
	client := &EmbeddingClient{
		selector:              selector,
		endpoint:              strings.TrimSpace(endpoint),
		model:                 strings.TrimSpace(model),
		dimension:             dimension,
		maxBatchSize:          maxBatchSize,
		maxInputTokensPerText: maxInputTokensPerText,
		organization:          strings.TrimSpace(organization),
		project:               strings.TrimSpace(project),
		params:                params,
		modelParams:           modelParams,
		clients:               make(map[string]appports.EmbeddingClient, len(apiKeys)),
		options:               options,
	}
	client.factory, client.classify, err = newEmbeddingProviderFactory(provider, client.endpoint, client.model, client.dimension, client.organization, client.project, client.params, client.modelParams, client.options)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Embed executes one fixed-model embedding request and only rotates API keys on classified key-level failures.
// Embed 用于执行一次固定模型 embedding 请求，并且只在识别出的 Key 级故障下轮换 API Key。
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	if c == nil || c.selector == nil {
		return appports.EmbeddingResponse{}, fmt.Errorf("embedding key failover client is nil")
	}
	if model := strings.TrimSpace(req.Model); model != "" && model != c.model {
		return appports.EmbeddingResponse{}, fmt.Errorf("embedding key failover requires fixed model %q, got %q", c.model, model)
	}
	if req.Dimension > 0 && req.Dimension != c.dimension {
		return appports.EmbeddingResponse{}, fmt.Errorf("embedding key failover requires fixed dimension %d, got %d", c.dimension, req.Dimension)
	}
	req.Model = c.model
	req.Dimension = c.dimension
	req.Texts = normalizeEmbeddingTexts(req.Texts)
	if len(req.Texts) == 0 {
		return appports.EmbeddingResponse{Vectors: [][]float32{}}, nil
	}
	return c.embedInChunks(ctx, req)
}

// embedInChunks fans one logical embedding request out into provider-safe sub-batches so runtime callers no longer need to replicate per-model batching logic at every call site.
// embedInChunks 用于把一次逻辑 embedding 请求拆成 provider 安全的子批次，让运行时调用方无需在每个调用点重复维护模型级拆批逻辑。
func (c *EmbeddingClient) embedInChunks(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	batchSize := c.maxBatchSize
	if batchSize <= 0 {
		batchSize = len(req.Texts)
	}
	vectors := make([][]float32, 0, len(req.Texts))
	for start := 0; start < len(req.Texts); start += batchSize {
		end := start + batchSize
		if end > len(req.Texts) {
			end = len(req.Texts)
		}
		chunkVectors, err := c.embedChunkWithFallback(ctx, req, req.Texts[start:end])
		if err != nil {
			return appports.EmbeddingResponse{}, err
		}
		vectors = append(vectors, chunkVectors...)
	}
	return appports.EmbeddingResponse{Vectors: vectors}, nil
}

// embedChunkWithFallback keeps batching centralized inside the embedding controller and only drills down into smaller chunks when the upstream provider confirms the current payload is too large.
// embedChunkWithFallback 用于把拆批统一收敛在 embedding 控制器里，并且只在上游 provider 明确确认当前载荷过大时，才继续下钻到更小子批次。
func (c *EmbeddingClient) embedChunkWithFallback(ctx context.Context, req appports.EmbeddingRequest, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	resp, err := c.executeEmbeddingChunk(ctx, req, texts)
	if err == nil {
		if err := validateEmbeddingVectorCount(resp, len(texts)); err != nil {
			return nil, err
		}
		return resp.Vectors, nil
	}
	if !isEmbeddingInputTooLargeError(err) {
		return nil, err
	}
	if len(texts) == 1 {
		vector, retryErr := c.retryOversizedSingleText(ctx, req, texts[0], err)
		if retryErr != nil {
			return nil, retryErr
		}
		return [][]float32{vector}, nil
	}

	// Split only after the provider has positively identified the current batch as too large, so ordinary callers can keep passing the full logical request.
	// 只有在 provider 已经明确认定当前批次过大后，才继续把它拆小；这样普通调用方始终可以直接提交完整逻辑请求。
	mid := len(texts) / 2
	leftVectors, err := c.embedChunkWithFallback(ctx, req, texts[:mid])
	if err != nil {
		return nil, err
	}
	rightVectors, err := c.embedChunkWithFallback(ctx, req, texts[mid:])
	if err != nil {
		return nil, err
	}
	return append(leftVectors, rightVectors...), nil
}

// executeEmbeddingChunk sends one concrete chunk through the fixed-model key-failover loop without letting callers replicate selector, budget, or response-count handling.
// executeEmbeddingChunk 用于把一个具体子批次送入固定模型 Key 容灾循环，避免调用方重复处理 selector、预算预留和结果计数校验。
func (c *EmbeddingClient) executeEmbeddingChunk(ctx context.Context, req appports.EmbeddingRequest, texts []string) (appports.EmbeddingResponse, error) {
	chunkReq := req
	chunkReq.Texts = append([]string(nil), texts...)
	cost := estimateEmbeddingRequestCost(chunkReq.Texts)
	return executeWithFailover(ctx, c.selector, cost, func(ctx context.Context, apiKey string) (appports.EmbeddingResponse, error) {
		return c.clientForKey(apiKey).Embed(ctx, chunkReq)
	}, func(err error, now time.Time) failureDecision {
		if c.classify == nil {
			return failureDecision{Class: errorClassUnknown}
		}
		return c.classify(err, now)
	}, nil)
}

// retryOversizedSingleText progressively truncates one provider-rejected text and retries it in place so one abnormal payload no longer aborts the entire logical embedding batch.
// retryOversizedSingleText 用于对被 provider 拒绝的超长单条文本做渐进裁剪并原位重试，避免单个异常载荷拖垮整批逻辑 embedding 请求。
func (c *EmbeddingClient) retryOversizedSingleText(ctx context.Context, req appports.EmbeddingRequest, text string, cause error) ([]float32, error) {
	current := strings.TrimSpace(text)
	if current == "" {
		return nil, cause
	}
	lastErr := cause
	seen := map[string]struct{}{current: {}}
	for attempt := 0; attempt < embeddingRetryTruncationAttempts; attempt++ {
		truncated, changed := c.truncateEmbeddingTextForRetry(current, attempt)
		if !changed {
			break
		}
		if _, exists := seen[truncated]; exists {
			break
		}
		seen[truncated] = struct{}{}

		resp, err := c.executeEmbeddingChunk(ctx, req, []string{truncated})
		if err == nil {
			if err := validateEmbeddingVectorCount(resp, 1); err != nil {
				return nil, err
			}
			return append([]float32(nil), resp.Vectors[0]...), nil
		}
		lastErr = err
		if !isEmbeddingInputTooLargeError(err) {
			return nil, err
		}
		current = truncated
	}
	return nil, fmt.Errorf("embedding text still exceeds upstream length limit after truncation retry: %w", lastErr)
}

// truncateEmbeddingTextForRetry derives one shorter retry payload from either the configured token target or a provider-confirmed fallback estimate, while guaranteeing the result is shorter than the original text.
// truncateEmbeddingTextForRetry 用于基于显式 token 目标或 provider 已确认的回退估算，生成更短的重试载荷，并保证结果一定短于原始文本。
func (c *EmbeddingClient) truncateEmbeddingTextForRetry(text string, attempt int) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	tokenBudget := c.retryTruncationTokenBudget(text, attempt)
	clipped := strings.TrimSpace(textutil.EnforceTokenBudget(text, textutil.TokenBudgetConfig{
		MaxTokens: tokenBudget,
		HeadRunes: 320,
		TailRunes: 120,
		Marker:    embeddingRetryTruncationMarker,
		Estimator: textutil.DomesticTokenEstimatorConfig(),
	}))
	if clipped != "" && clipped != text {
		return clipped, true
	}
	return clipEmbeddingTextByRunes(text, retryRuneClipRatio(attempt))
}

// retryTruncationTokenBudget chooses the next retry budget. When callers configured one explicit per-text token target, retries honor it first; otherwise the budget is derived only after the provider has already rejected the full text.
// retryTruncationTokenBudget 用于选择下一轮重试预算：若调用方配置了显式单条 token 目标，则优先尊重该目标；否则只在 provider 已拒绝完整文本后才回退到估算预算。
func (c *EmbeddingClient) retryTruncationTokenBudget(text string, attempt int) int {
	if c != nil && c.maxInputTokensPerText > 0 {
		return scaleRetryTokenBudget(c.maxInputTokensPerText, embeddingRetryConfiguredBudgetSteps, attempt)
	}
	base := 0
	if embeddingRetryTokenEstimator != nil {
		base = embeddingRetryTokenEstimator.Estimate(text)
	}
	if base <= 0 {
		base = 128
	}
	return scaleRetryTokenBudget(base, embeddingRetryEstimatedBudgetSteps, attempt)
}

// scaleRetryTokenBudget applies one bounded shrink step to the provided base budget so successive retries become materially smaller without collapsing to zero.
// scaleRetryTokenBudget 用于对给定基础预算应用有界收缩步长，让连续重试能明显变小，同时避免预算塌缩为零。
func scaleRetryTokenBudget(base int, steps [embeddingRetryTruncationAttempts]float64, attempt int) int {
	if base <= 0 {
		base = embeddingRetryMinimumTokenBudget
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(steps) {
		attempt = len(steps) - 1
	}
	budget := int(math.Ceil(float64(base) * steps[attempt]))
	if budget < embeddingRetryMinimumTokenBudget {
		return embeddingRetryMinimumTokenBudget
	}
	return budget
}

// retryRuneClipRatio returns the emergency rune-level shrink ratio for the requested attempt index.
// retryRuneClipRatio 用于返回指定尝试序号对应的 rune 级紧急收缩比例。
func retryRuneClipRatio(attempt int) float64 {
	if attempt < 0 {
		return embeddingRetryRuneClipSteps[0]
	}
	if attempt >= len(embeddingRetryRuneClipSteps) {
		return embeddingRetryRuneClipSteps[len(embeddingRetryRuneClipSteps)-1]
	}
	return embeddingRetryRuneClipSteps[attempt]
}

// clipEmbeddingTextByRunes forces one shorter payload even when token-budget clipping could not make progress, which can happen for dense short texts where rune count is the last practical fallback.
// clipEmbeddingTextByRunes 用于在 token 预算裁剪无法继续缩短文本时，强制生成更短载荷；这类情况常见于 rune 数不多但 token 密度很高的文本。
func clipEmbeddingTextByRunes(text string, ratio float64) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	runes := []rune(text)
	if len(runes) <= 1 {
		return "", false
	}
	if ratio <= 0 || ratio >= 1 {
		ratio = embeddingRetryRuneClipSteps[0]
	}
	maxRunes := int(math.Floor(float64(len(runes)) * ratio))
	if maxRunes >= len(runes) {
		maxRunes = len(runes) - 1
	}
	if maxRunes < 1 {
		maxRunes = 1
	}
	markerRunes := []rune(embeddingRetryTruncationMarker)
	if maxRunes <= len(markerRunes)+2 {
		clipped := strings.TrimSpace(string(runes[:maxRunes]))
		return clipped, clipped != "" && clipped != text
	}
	remaining := maxRunes - len(markerRunes)
	head := int(math.Ceil(float64(remaining) * 0.7))
	tail := remaining - head
	if head < 1 {
		head = 1
	}
	if tail < 1 {
		tail = 1
		if head > 1 {
			head--
		}
	}
	clipped := strings.TrimSpace(string(runes[:head]) + embeddingRetryTruncationMarker + string(runes[len(runes)-tail:]))
	return clipped, clipped != "" && clipped != text
}

// validateEmbeddingVectorCount keeps the wrapper strict about one-input-one-vector ordering even when one request had to be recursively split or retried.
// validateEmbeddingVectorCount 用于在请求被递归拆分或重试时，仍然严格保证“一条输入对应一条向量”的顺序契约。
func validateEmbeddingVectorCount(resp appports.EmbeddingResponse, want int) error {
	if len(resp.Vectors) != want {
		return fmt.Errorf("embedding result count mismatch: got %d want %d", len(resp.Vectors), want)
	}
	return nil
}

// normalizeEmbeddingTexts trims and drops blank inputs once so chunking, token checks, and provider adapters all observe the same effective request payload.
// normalizeEmbeddingTexts 用于统一裁剪并剔除空白输入，让拆批、token 校验和 provider 适配器看到一致的实际请求载荷。
func normalizeEmbeddingTexts(texts []string) []string {
	normalized := make([]string, 0, len(texts))
	for _, text := range texts {
		if text = strings.TrimSpace(text); text != "" {
			normalized = append(normalized, text)
		}
	}
	return normalized
}

// clientForKey returns the cached concrete embedding adapter for one API key or builds it on first use.
// clientForKey 用于返回某个 API Key 对应的缓存 embedding 适配器；若首次使用则即时构建。
func (c *EmbeddingClient) clientForKey(apiKey string) appports.EmbeddingClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[apiKey]; ok {
		return client
	}
	client := c.factory(apiKey)
	c.clients[apiKey] = client
	return client
}
