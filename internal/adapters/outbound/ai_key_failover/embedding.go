// embedding.go implements the fixed-model embedding wrapper that rotates API keys without ever mixing embedding models.
// embedding.go 用于实现固定模型的 embedding 包装器，让系统在绝不混用 embedding 模型的前提下轮换 API Key。
package ai_key_failover

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/providerinput"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

const (
	// embeddingDropReasonInputTooLarge marks the source text as intentionally dropped after the provider confirmed that this single payload is too large to embed.
	// embeddingDropReasonInputTooLarge 用于标记某条源文本在 provider 已确认其单条载荷过大后被主动丢弃。
	embeddingDropReasonInputTooLarge = "input_too_large"
)

// EmbeddingClient wraps one fixed provider/endpoint/model/dimension tuple with in-memory API-key failover.
// EmbeddingClient 用于把一个固定 provider/endpoint/model/dimension 组合包装成带内存态 API Key 容灾能力的 embedding 客户端。
type EmbeddingClient struct {
	selector     *selector
	endpoint     string
	model        string
	dimension    int
	maxBatchSize int
	organization string
	project      string
	params       map[string]any
	modelParams  map[string]map[string]any
	factory      func(string) appports.EmbeddingClient
	classify     func(error, time.Time) failureDecision
	clients      map[string]appports.EmbeddingClient
	mu           sync.Mutex
	options      Options
}

// NewEmbeddingClient creates one fixed-model embedding client that can rotate across multiple API keys of the same upstream configuration.
// NewEmbeddingClient 用于创建一个固定模型 embedding 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewEmbeddingClient(endpoint, model string, dimension, maxBatchSize int, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*EmbeddingClient, error) {
	return NewProviderEmbeddingClient("openai", endpoint, model, dimension, maxBatchSize, organization, project, apiKeys, params, modelParams, options)
}

// NewProviderEmbeddingClient creates one provider-aware fixed-model embedding client so the same key-failover shell can serve OpenAI-compatible and Google AI Studio native backends.
// NewProviderEmbeddingClient 用于创建一个 provider 感知的固定模型 embedding 客户端，让同一套 key-failover 外壳可以同时服务 OpenAI-compatible 与 Google AI Studio 原生后端。
func NewProviderEmbeddingClient(provider, endpoint, model string, dimension, maxBatchSize int, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*EmbeddingClient, error) {
	options.ServiceName = "embedding"
	if len(options.Nodes) == 0 {
		options.APIKeys = append([]string(nil), apiKeys...)
	}
	selector, err := newSelector(options)
	if err != nil {
		return nil, err
	}
	client := &EmbeddingClient{
		selector:     selector,
		endpoint:     strings.TrimSpace(endpoint),
		model:        strings.TrimSpace(model),
		dimension:    dimension,
		maxBatchSize: maxBatchSize,
		organization: strings.TrimSpace(organization),
		project:      strings.TrimSpace(project),
		params:       params,
		modelParams:  modelParams,
		clients:      make(map[string]appports.EmbeddingClient, len(apiKeys)),
		options:      options,
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
	req.Texts = providerinput.NormalizeEmbeddingTexts(req.Texts)
	if len(req.Texts) == 0 {
		return appports.EmbeddingResponse{Vectors: [][]float32{}}, nil
	}
	return c.embedInChunks(ctx, req)
}

// embeddingVectorResult keeps one successful vector together with the original text index it belongs to.
// embeddingVectorResult 用于把一条成功生成的向量与其原始文本下标绑定起来。
type embeddingVectorResult struct {
	index  int
	vector []float32
}

// embedInChunks fans one logical embedding request out into provider-safe sub-batches so runtime callers no longer need to replicate per-model batching logic at every call site.
// embedInChunks 用于把一次逻辑 embedding 请求拆成 provider 安全的子批次，让运行时调用方无需在每个调用点重复维护模型级拆批逻辑。
func (c *EmbeddingClient) embedInChunks(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	batchSize := c.maxBatchSize
	if batchSize <= 0 {
		batchSize = len(req.Texts)
	}
	results := make([]embeddingVectorResult, 0, len(req.Texts))
	dropped := make([]appports.EmbeddingDroppedInput, 0)
	for start := 0; start < len(req.Texts); start += batchSize {
		end := start + batchSize
		if end > len(req.Texts) {
			end = len(req.Texts)
		}
		chunkResults, chunkDropped, err := c.embedChunkWithFallback(ctx, req, req.Texts[start:end], start)
		if err != nil {
			return appports.EmbeddingResponse{}, err
		}
		results = append(results, chunkResults...)
		dropped = append(dropped, chunkDropped...)
	}
	return buildEmbeddingResponse(results, dropped), nil
}

// embedChunkWithFallback keeps batching centralized inside the embedding controller and only drills down into smaller chunks when the upstream provider confirms the current payload is too large.
// embedChunkWithFallback 用于把拆批统一收敛在 embedding 控制器里，并且只在上游 provider 明确确认当前载荷过大时，才继续下钻到更小子批次。
func (c *EmbeddingClient) embedChunkWithFallback(ctx context.Context, req appports.EmbeddingRequest, texts []string, startIndex int) ([]embeddingVectorResult, []appports.EmbeddingDroppedInput, error) {
	if len(texts) == 0 {
		return []embeddingVectorResult{}, []appports.EmbeddingDroppedInput{}, nil
	}
	resp, err := c.executeEmbeddingChunk(ctx, req, texts)
	if err == nil {
		items, dropped, itemErr := offsetEmbeddingResponse(resp, len(texts), startIndex)
		if itemErr != nil {
			return nil, nil, itemErr
		}
		return items, dropped, nil
	}
	if !isEmbeddingInputTooLargeError(err) {
		return nil, nil, err
	}
	if len(texts) == 1 {
		if !req.AllowPartialInvalidTexts {
			return nil, nil, err
		}
		return nil, []appports.EmbeddingDroppedInput{{
			Index:  startIndex,
			Text:   texts[0],
			Reason: embeddingDropReasonInputTooLarge,
		}}, nil
	}

	// Split only after the provider has positively identified the current batch as too large, so ordinary callers can keep passing the full logical request.
	// 只有在 provider 已经明确认定当前批次过大后，才继续把它拆小；这样普通调用方始终可以直接提交完整逻辑请求。
	mid := len(texts) / 2
	leftVectors, leftDropped, err := c.embedChunkWithFallback(ctx, req, texts[:mid], startIndex)
	if err != nil {
		return nil, nil, err
	}
	rightVectors, rightDropped, err := c.embedChunkWithFallback(ctx, req, texts[mid:], startIndex+mid)
	if err != nil {
		return nil, nil, err
	}
	return append(leftVectors, rightVectors...), append(leftDropped, rightDropped...), nil
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

// offsetEmbeddingResponse validates one chunk response and re-binds every vector plus dropped item onto the source indexes of the outer logical request.
// offsetEmbeddingResponse 用于校验单个子批次响应，并把其中的向量与丢弃条目重新绑定回外层逻辑请求的源下标。
func offsetEmbeddingResponse(resp appports.EmbeddingResponse, inputCount, startIndex int) ([]embeddingVectorResult, []appports.EmbeddingDroppedInput, error) {
	items, err := resp.IndexedVectors(inputCount)
	if err != nil {
		return nil, nil, err
	}
	offsetItems := make([]embeddingVectorResult, 0, len(items))
	for _, item := range items {
		offsetItems = append(offsetItems, embeddingVectorResult{
			index:  startIndex + item.Index,
			vector: append([]float32(nil), item.Vector...),
		})
	}
	offsetDropped := make([]appports.EmbeddingDroppedInput, 0, len(resp.Dropped))
	for _, item := range resp.Dropped {
		offsetDropped = append(offsetDropped, appports.EmbeddingDroppedInput{
			Index:  startIndex + item.Index,
			Text:   item.Text,
			Reason: item.Reason,
		})
	}
	return offsetItems, offsetDropped, nil
}

// buildEmbeddingResponse converts indexed vector results plus dropped source texts into the public response contract returned to callers.
// buildEmbeddingResponse 用于把带索引的成功向量结果和已丢弃源文本转换成返回给调用方的公共响应契约。
func buildEmbeddingResponse(results []embeddingVectorResult, dropped []appports.EmbeddingDroppedInput) appports.EmbeddingResponse {
	resp := appports.EmbeddingResponse{
		Vectors:       make([][]float32, 0, len(results)),
		ResultIndices: make([]int, 0, len(results)),
		Dropped:       make([]appports.EmbeddingDroppedInput, 0, len(dropped)),
	}
	for _, result := range results {
		resp.ResultIndices = append(resp.ResultIndices, result.index)
		resp.Vectors = append(resp.Vectors, append([]float32(nil), result.vector...))
	}
	for _, item := range dropped {
		resp.Dropped = append(resp.Dropped, appports.EmbeddingDroppedInput{
			Index:  item.Index,
			Text:   item.Text,
			Reason: item.Reason,
		})
	}
	return resp
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
