// rerank.go implements the fixed-model rerank wrapper that rotates API keys without changing provider, endpoint, or rerank model.
// rerank.go 用于实现固定模型的 rerank 包装器，让系统在不切换 provider、endpoint 或 rerank 模型的前提下轮换 API Key。
package ai_key_failover

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/dashscope_rerank"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// RerankerClient wraps one fixed rerank upstream configuration with in-memory API-key failover.
// RerankerClient 用于把一个固定 rerank 上游配置包装成带内存态 API Key 容灾能力的重排序客户端。
type RerankerClient struct {
	selector *selector
	endpoint string
	model    string
	timeout  time.Duration
	factory  func(string) appports.RerankerClient
	clients  map[string]appports.RerankerClient
	mu       sync.Mutex
	options  Options
}

// NewRerankerClient creates one fixed-model rerank client that can rotate across multiple API keys of the same upstream configuration.
// NewRerankerClient 用于创建一个固定模型 rerank 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewRerankerClient(endpoint, model string, timeout time.Duration, apiKeys []string, options Options) (*RerankerClient, error) {
	options.ServiceName = "rerank"
	if len(options.Nodes) == 0 {
		options.APIKeys = append([]string(nil), apiKeys...)
	}
	selector, err := newSelector(options)
	if err != nil {
		return nil, err
	}
	client := &RerankerClient{
		selector: selector,
		endpoint: strings.TrimSpace(endpoint),
		model:    strings.TrimSpace(model),
		timeout:  timeout,
		clients:  make(map[string]appports.RerankerClient, len(apiKeys)),
		options:  options,
	}
	client.factory = func(apiKey string) appports.RerankerClient {
		return dashscope_rerank.NewClient(client.endpoint, apiKey, client.model, client.timeout, nil)
	}
	return client, nil
}

// Rerank executes one fixed-model rerank request and only rotates API keys on classified key-level failures.
// Rerank 用于执行一次固定模型 rerank 请求，并且只在识别出的 Key 级故障下轮换 API Key。
func (c *RerankerClient) Rerank(ctx context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	if c == nil || c.selector == nil {
		return nil, fmt.Errorf("rerank key failover client is nil")
	}
	cost := estimateRerankRequestCost(query, docs)
	return executeWithFailover(ctx, c.selector, cost, func(ctx context.Context, apiKey string) ([]appports.RerankerResult, error) {
		return c.clientForKey(apiKey).Rerank(ctx, query, docs, topN)
	}, func(err error, now time.Time) failureDecision {
		return classifyDashScopeError(err, c.options, now)
	}, nil)
}

// clientForKey returns the cached concrete rerank adapter for one API key or builds it on first use.
// clientForKey 用于返回某个 API Key 对应的缓存 rerank 适配器；若首次使用则即时构建。
func (c *RerankerClient) clientForKey(apiKey string) appports.RerankerClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[apiKey]; ok {
		return client
	}
	client := c.factory(apiKey)
	c.clients[apiKey] = client
	return client
}
