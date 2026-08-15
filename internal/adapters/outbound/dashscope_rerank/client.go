// client.go implements the DashScope text-rerank outbound adapter used by the memory recall pipeline.
// client.go 用于实现记忆召回流水线使用的 DashScope text-rerank 出站适配器。
package dashscope_rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerinput"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

const (
	// defaultEndpoint keeps the DashScope rerank URL stable when config omits an explicit override.
	// defaultEndpoint 用于在配置未显式覆盖时，给 DashScope rerank 提供稳定默认地址。
	defaultEndpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"

	// defaultTimeout keeps one rerank call bounded so retrieval can degrade instead of hanging indefinitely.
	// defaultTimeout 用于限制单次 rerank 调用时长，保证检索流程在外部服务抖动时可以降级而不是无限挂起。
	defaultTimeout = 8 * time.Second
)

// Client adapts the internal rerank port onto DashScope's text-rerank HTTP API.
// Client 用于把内部 rerank 端口适配到 DashScope 的 text-rerank HTTP 接口。
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	timeout    time.Duration
	httpClient *http.Client
}

// APIError preserves the DashScope HTTP status, response headers, and trimmed body so upper layers can decide whether to retry, rotate keys, or degrade rerank safely.
// APIError 用于保留 DashScope 的 HTTP 状态码、响应头和裁剪后的响应体，便于上层决定是否重试、轮换 Key 或安全降级 rerank。
type APIError struct {
	StatusCode int
	Headers    http.Header
	Body       string
}

// Error renders the structured DashScope API failure back into one stable diagnostic string for logs and fallback handling.
// Error 用于把结构化的 DashScope API 失败渲染回稳定的诊断字符串，方便日志和降级处理复用。
func (e *APIError) Error() string {
	if e == nil {
		return "dashscope rerank api error <nil>"
	}
	if body := strings.TrimSpace(e.Body); body != "" {
		return fmt.Sprintf("dashscope rerank status %d: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("dashscope rerank status %d", e.StatusCode)
}

// NewClient creates a DashScope rerank client with one bounded shared transport, an explicit operation budget, and normalized endpoint settings.
// NewClient 用于创建使用有界共享 Transport、显式操作预算和规范化 endpoint 设置的 DashScope rerank 客户端。
func NewClient(endpoint, apiKey, model string, timeout time.Duration, httpClient *http.Client) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = httpclient.SharedDefault()
	}
	trimmedEndpoint := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmedEndpoint == "" {
		trimmedEndpoint = defaultEndpoint
	}
	return &Client{
		endpoint:   trimmedEndpoint,
		apiKey:     strings.TrimSpace(apiKey),
		model:      strings.TrimSpace(model),
		timeout:    timeout,
		httpClient: httpClient,
	}
}

// Rerank sends one query plus candidate documents to DashScope and returns provider scores keyed by document id.
// Rerank 用于把单个 query 和候选文档发送到 DashScope，并按文档 id 返回提供方分数。
func (c *Client) Rerank(ctx context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	if c == nil {
		return nil, fmt.Errorf("dashscope rerank client is nil")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("dashscope rerank api key is required")
	}
	if strings.TrimSpace(c.model) == "" {
		return nil, fmt.Errorf("dashscope rerank model is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("dashscope rerank query is required")
	}
	normalizedDocs := providerinput.NormalizeRerankerDocuments(docs)
	if len(normalizedDocs) == 0 {
		return []appports.RerankerResult{}, nil
	}
	if topN <= 0 || topN > len(normalizedDocs) {
		topN = len(normalizedDocs)
	}

	// Build the provider payload in one stable shape so tests can assert the exact request contract.
	// 使用稳定的请求体结构组装 provider 载荷，便于测试精确断言请求契约。
	body, err := json.Marshal(buildRequestPayload(c.model, query, normalizedDocs, topN))
	if err != nil {
		return nil, fmt.Errorf("marshal dashscope rerank request: %w", err)
	}
	// Apply the route-owned rerank budget through a child context so the process-wide HTTP client remains connection-only and the earlier parent deadline still wins.
	// 通过子 Context 应用路由持有的 rerank 预算，让进程级 HTTP Client 只负责连接，并确保更早的父截止时间仍优先生效。
	requestCtx, cancelRequest := context.WithTimeout(ctx, c.timeout)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build dashscope rerank request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call dashscope rerank: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read dashscope rerank response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Headers:    resp.Header.Clone(),
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	results, err := parseResponsePayload(respBody, normalizedDocs)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// requestPayload models the outbound JSON body accepted by DashScope text-rerank.
// requestPayload 用于描述 DashScope text-rerank 接受的出站 JSON 请求体。
type requestPayload struct {
	Model      string                `json:"model"`
	Input      requestInput          `json:"input"`
	Parameters requestParametersBody `json:"parameters"`
}

// requestInput models the query and candidate document list sent to DashScope.
// requestInput 用于描述发送给 DashScope 的 query 和候选文档列表。
type requestInput struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// requestParametersBody models the optional provider parameters used during rerank.
// requestParametersBody 用于描述 rerank 过程中的可选 provider 参数。
type requestParametersBody struct {
	ReturnDocuments bool `json:"return_documents"`
	TopN            int  `json:"top_n"`
}

// responsePayload models the subset of DashScope response fields needed by the recall pipeline.
// responsePayload 用于描述召回流水线需要使用的 DashScope 响应字段子集。
type responsePayload struct {
	Output struct {
		Results []responseResult `json:"results"`
	} `json:"output"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// responseResult models one reranked document result returned by DashScope.
// responseResult 用于描述 DashScope 返回的一条重排序结果。
type responseResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// buildRequestPayload renders the stable provider request body from the internal rerank contract.
// buildRequestPayload 用于把内部 rerank 契约渲染成稳定的 provider 请求体。
func buildRequestPayload(model, query string, docs []appports.RerankerDocument, topN int) requestPayload {
	texts := make([]string, 0, len(docs))
	for _, doc := range docs {
		texts = append(texts, doc.Text)
	}
	return requestPayload{
		Model: model,
		Input: requestInput{
			Query:     query,
			Documents: texts,
		},
		Parameters: requestParametersBody{
			ReturnDocuments: true,
			TopN:            topN,
		},
	}
}

// parseResponsePayload maps provider result indexes back onto the internal document ids and scores.
// parseResponsePayload 用于把 provider 返回的结果索引映射回内部文档 id 和分数。
func parseResponsePayload(body []byte, docs []appports.RerankerDocument) ([]appports.RerankerResult, error) {
	var payload responsePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode dashscope rerank response: %w", err)
	}
	results := make([]appports.RerankerResult, 0, len(payload.Output.Results))
	for _, item := range payload.Output.Results {
		if item.Index < 0 || item.Index >= len(docs) {
			return nil, fmt.Errorf("dashscope rerank returned out-of-range index %d", item.Index)
		}
		results = append(results, appports.RerankerResult{
			ID:    docs[item.Index].ID,
			Score: item.RelevanceScore,
		})
	}
	return results, nil
}
