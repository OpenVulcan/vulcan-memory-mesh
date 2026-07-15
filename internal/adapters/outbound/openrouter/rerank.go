// rerank.go implements the OpenRouter SDK-backed rerank adapter used by second-stage memory recall.
// rerank.go 用于实现基于 OpenRouter SDK 的 rerank 出站适配器，服务二阶段记忆召回。
package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenRouterTeam/go-sdk/models/operations"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerhint"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerinput"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

const (
	// defaultRerankTimeout keeps rerank bounded separately from chat and embedding because recall should degrade quickly on upstream stalls.
	// defaultRerankTimeout 用于单独限制 rerank 耗时，因为召回链路在上游卡顿时应快速降级。
	defaultRerankTimeout = 8 * time.Second
)

// RerankerClient adapts the internal rerank port onto OpenRouter's typed rerank SDK endpoint.
// RerankerClient 用于把内部 rerank 端口适配到 OpenRouter 的强类型 rerank SDK 端点。
type RerankerClient struct {
	client *Client
	model  string
	// params stores route-level OpenRouter rerank hints copied from configuration.
	// params 用于保存从配置复制而来的路由级 OpenRouter rerank 参数。
	params map[string]any
	// modelParams stores model-scoped OpenRouter rerank hints copied from configuration.
	// modelParams 用于保存从配置复制而来的模型级 OpenRouter rerank 参数。
	modelParams map[string]map[string]any
}

// NewRerankerClient creates one fixed-model OpenRouter rerank client with a provider-root endpoint and bounded timeout.
// NewRerankerClient 用于创建一个固定模型的 OpenRouter rerank 客户端，并配置 provider 根地址与有界超时。
func NewRerankerClient(endpoint, apiKey, model string, timeout time.Duration, httpClient *http.Client) *RerankerClient {
	return NewRerankerClientWithParams(endpoint, apiKey, model, timeout, httpClient, nil, nil)
}

// NewRerankerClientWithParams creates one fixed-model OpenRouter rerank client with optional OpenRouter provider-routing hints.
// NewRerankerClientWithParams 用于创建一个固定模型的 OpenRouter rerank 客户端，并携带可选的 OpenRouter provider 选路参数。
func NewRerankerClientWithParams(endpoint, apiKey, model string, timeout time.Duration, httpClient *http.Client, params map[string]any, modelParams map[string]map[string]any) *RerankerClient {
	if timeout <= 0 {
		timeout = defaultRerankTimeout
	}
	return &RerankerClient{
		client:      NewClient(endpoint, apiKey, timeout, httpClient),
		model:       strings.TrimSpace(model),
		params:      providerhint.CloneMap(params),
		modelParams: providerhint.CloneNestedMap(modelParams),
	}
}

// Rerank sends one query and candidate document list to OpenRouter and maps result indexes back to internal document ids.
// Rerank 用于把单个 query 与候选文档列表发送到 OpenRouter，并将结果索引映射回内部文档 id。
func (c *RerankerClient) Rerank(ctx context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	if c == nil || c.client == nil || c.client.sdkClient == nil {
		return nil, fmt.Errorf("openrouter rerank client is nil")
	}
	if strings.TrimSpace(c.client.apiKey) == "" {
		return nil, fmt.Errorf("openrouter rerank api key is required")
	}
	if strings.TrimSpace(c.model) == "" {
		return nil, fmt.Errorf("openrouter rerank model is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("openrouter rerank query is required")
	}
	normalizedDocs := providerinput.NormalizeRerankerDocuments(docs)
	if len(normalizedDocs) == 0 {
		return []appports.RerankerResult{}, nil
	}
	if topN <= 0 || topN > len(normalizedDocs) {
		topN = len(normalizedDocs)
	}
	params := operations.CreateRerankRequest{
		Documents: rerankDocumentTexts(normalizedDocs),
		Model:     c.model,
		Query:     query,
		TopN:      int64Pointer(int64(topN)),
	}
	applyRerankHints(&params, providerhint.Merge(c.params, c.modelParams[strings.TrimSpace(c.model)], nil))
	resp, err := c.client.sdkClient.Rerank.Rerank(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.CreateRerankResponseBody == nil {
		return nil, fmt.Errorf("openrouter rerank empty response")
	}
	return mapRerankResults(resp.CreateRerankResponseBody.Results, normalizedDocs)
}

// rerankDocumentTexts extracts the provider-facing text payload while keeping ids available for response remapping.
// rerankDocumentTexts 用于提取 provider 侧需要的文本载荷，同时保留 id 供响应映射使用。
func rerankDocumentTexts(docs []appports.RerankerDocument) []string {
	texts := make([]string, 0, len(docs))
	for _, doc := range docs {
		texts = append(texts, doc.Text)
	}
	return texts
}

// applyRerankHints maps OpenRouter-supported rerank hints into the generated SDK request while ignoring unsupported keys.
// applyRerankHints 用于把 OpenRouter 支持的 rerank 参数映射到生成式 SDK 请求，并忽略不支持的键。
func applyRerankHints(params *operations.CreateRerankRequest, hints map[string]any) {
	if params == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "provider":
			if value, ok := providerPreferencesHint(rawValue); ok {
				params.Provider = optionalValue(value)
			}
		}
	}
}

// mapRerankResults maps OpenRouter result indexes back onto the normalized input document ids and scores.
// mapRerankResults 用于把 OpenRouter 返回的结果索引映射回规范化输入文档的 id 与分数。
func mapRerankResults(items []operations.Result, docs []appports.RerankerDocument) ([]appports.RerankerResult, error) {
	results := make([]appports.RerankerResult, 0, len(items))
	for _, item := range items {
		if item.Index < 0 || item.Index >= int64(len(docs)) {
			return nil, fmt.Errorf("openrouter rerank returned out-of-range index %d", item.Index)
		}
		doc := docs[int(item.Index)]
		results = append(results, appports.RerankerResult{
			ID:    doc.ID,
			Score: item.RelevanceScore,
		})
	}
	return results, nil
}
