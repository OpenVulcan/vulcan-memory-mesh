// embedding.go implements the fixed-model embedding wrapper that rotates API keys without ever mixing embedding models.
// embedding.go 用于实现固定模型的 embedding 包装器，让系统在绝不混用 embedding 模型的前提下轮换 API Key。
package ai_key_failover

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// EmbeddingClient wraps one fixed provider/endpoint/model/dimension tuple with in-memory API-key failover.
// EmbeddingClient 用于把一个固定 provider/endpoint/model/dimension 组合包装成带内存态 API Key 容灾能力的 embedding 客户端。
type EmbeddingClient struct {
	selector     *selector
	endpoint     string
	model        string
	dimension    int
	organization string
	project      string
	params       map[string]any
	modelParams  map[string]map[string]any
	factory      func(string) appports.EmbeddingClient
	clients      map[string]appports.EmbeddingClient
	mu           sync.Mutex
	options      Options
}

// NewEmbeddingClient creates one fixed-model embedding client that can rotate across multiple API keys of the same upstream configuration.
// NewEmbeddingClient 用于创建一个固定模型 embedding 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewEmbeddingClient(endpoint, model string, dimension int, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*EmbeddingClient, error) {
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
		organization: strings.TrimSpace(organization),
		project:      strings.TrimSpace(project),
		params:       params,
		modelParams:  modelParams,
		clients:      make(map[string]appports.EmbeddingClient, len(apiKeys)),
		options:      options,
	}
	client.factory = func(apiKey string) appports.EmbeddingClient {
		return openai_native.NewEmbeddingClient(client.endpoint, apiKey, client.model, client.dimension, client.organization, client.project, client.params, client.modelParams)
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
	cost := estimateEmbeddingRequestCost(req.Texts)
	return executeWithFailover(ctx, c.selector, cost, func(ctx context.Context, apiKey string) (appports.EmbeddingResponse, error) {
		return c.clientForKey(apiKey).Embed(ctx, req)
	}, func(err error, now time.Time) failureDecision {
		return classifyOpenAIError(err, c.options, now)
	}, nil)
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
