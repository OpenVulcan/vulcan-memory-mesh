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
	"github.com/openvulcan/vmm/internal/adapters/outbound/openrouter"
	"github.com/openvulcan/vmm/internal/adapters/outbound/siliconflow_rerank"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// RerankerClient wraps one fixed rerank upstream configuration with in-memory API-key failover.
// RerankerClient 用于把一个固定 rerank 上游配置包装成带内存态 API Key 容灾能力的重排序客户端。
type RerankerClient struct {
	selector *selector
	provider string
	endpoint string
	model    string
	timeout  time.Duration
	// params stores provider-specific rerank hints only after the selected provider declares support for them.
	// params 仅在当前 provider 声明支持 rerank 参数时保存 provider 专属提示。
	params map[string]any
	// modelParams stores model-scoped rerank hints only for providers that can safely consume them.
	// modelParams 仅为能够安全消费模型级 rerank 参数的 provider 保存对应提示。
	modelParams map[string]map[string]any
	factory     func(string) appports.RerankerClient
	classify    func(error, time.Time) failureDecision
	clients     map[string]appports.RerankerClient
	mu          sync.Mutex
	options     Options
}

// NewRerankerClient creates one fixed-model rerank client that can rotate across multiple API keys of the same upstream configuration.
// NewRerankerClient 用于创建一个固定模型 rerank 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewRerankerClient(endpoint, model string, timeout time.Duration, apiKeys []string, options Options) (*RerankerClient, error) {
	return NewProviderRerankerClient("dashscope", endpoint, model, timeout, apiKeys, nil, nil, options)
}

// NewProviderRerankerClient creates one fixed-model rerank client for the specified provider so different upstream contracts can still reuse the same in-memory key failover wrapper.
// NewProviderRerankerClient 用于为指定 provider 创建固定模型 rerank 客户端，让不同上游协议仍可复用同一套内存态 Key 容灾包装器。
func NewProviderRerankerClient(provider, endpoint, model string, timeout time.Duration, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*RerankerClient, error) {
	options.ServiceName = "rerank"
	if len(options.Nodes) == 0 {
		options.APIKeys = append([]string(nil), apiKeys...)
	}
	selector, err := newSelector(options)
	if err != nil {
		return nil, err
	}
	hooks, err := buildRerankProviderHooks(provider, options)
	if err != nil {
		return nil, err
	}
	params, modelParams = selectSupportedRerankHints(hooks, params, modelParams)
	client := &RerankerClient{
		selector:    selector,
		provider:    hooks.provider,
		endpoint:    strings.TrimSpace(endpoint),
		model:       strings.TrimSpace(model),
		timeout:     timeout,
		params:      params,
		modelParams: modelParams,
		clients:     make(map[string]appports.RerankerClient, len(apiKeys)),
		classify:    hooks.classify,
		options:     options,
	}
	client.factory = func(apiKey string) appports.RerankerClient {
		return hooks.newClient(client.endpoint, apiKey, client.model, client.timeout, client.params, client.modelParams)
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
		return c.classify(err, now)
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

// rerankProviderHooks keeps the provider-specific adapter constructor and failure classifier aligned so fixed-model wrappers and route failover share one source of truth.
// rerankProviderHooks 用于把 provider 专属适配器构造器与故障分类器绑定在一起，确保固定模型包装器和路由容灾共享同一套事实来源。
type rerankProviderHooks struct {
	provider string
	// supportsParams tells the wrapper whether route params may be passed into the concrete provider adapter.
	// supportsParams 用于告诉包装器是否允许把 route 参数传递给具体 provider 适配器。
	supportsParams bool
	newClient      func(endpoint, apiKey, model string, timeout time.Duration, params map[string]any, modelParams map[string]map[string]any) appports.RerankerClient
	classify       func(error, time.Time) failureDecision
}

// buildRerankProviderHooks resolves one rerank provider alias into the concrete adapter constructor and the matching failover classifier.
// buildRerankProviderHooks 用于把一个 rerank provider 别名解析成具体适配器构造器，以及与之匹配的容灾分类器。
func buildRerankProviderHooks(provider string, options Options) (rerankProviderHooks, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(provider)); normalized {
	case "", "dashscope":
		return rerankProviderHooks{
			provider: "dashscope",
			newClient: func(endpoint, apiKey, model string, timeout time.Duration, _ map[string]any, _ map[string]map[string]any) appports.RerankerClient {
				return dashscope_rerank.NewClient(endpoint, apiKey, model, timeout, nil)
			},
			classify: func(err error, now time.Time) failureDecision {
				return classifyDashScopeError(err, options, now)
			},
		}, nil
	case "siliconflow":
		return rerankProviderHooks{
			provider: "siliconflow",
			newClient: func(endpoint, apiKey, model string, timeout time.Duration, _ map[string]any, _ map[string]map[string]any) appports.RerankerClient {
				return siliconflow_rerank.NewClient(endpoint, apiKey, model, timeout, nil)
			},
			classify: func(err error, now time.Time) failureDecision {
				return classifySiliconFlowError(err, options, now)
			},
		}, nil
	case "openrouter":
		return rerankProviderHooks{
			provider:       "openrouter",
			supportsParams: true,
			newClient: func(endpoint, apiKey, model string, timeout time.Duration, params map[string]any, modelParams map[string]map[string]any) appports.RerankerClient {
				return openrouter.NewRerankerClientWithParams(endpoint, apiKey, model, timeout, nil, params, modelParams)
			},
			classify: func(err error, now time.Time) failureDecision {
				return classifyOpenRouterError(err, options, now)
			},
		}, nil
	default:
		return rerankProviderHooks{}, fmt.Errorf("unsupported rerank provider: %s", provider)
	}
}

// selectSupportedRerankHints keeps provider-specific rerank params only for adapters that explicitly know how to apply them.
// selectSupportedRerankHints 用于只为明确支持参数透传的适配器保留 provider 专属 rerank 参数。
func selectSupportedRerankHints(hooks rerankProviderHooks, params map[string]any, modelParams map[string]map[string]any) (map[string]any, map[string]map[string]any) {
	if !hooks.supportsParams {
		return nil, nil
	}
	return params, modelParams
}
