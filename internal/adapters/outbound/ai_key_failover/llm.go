// llm.go implements the fixed-model LLM wrapper that rotates API keys without changing provider, endpoint, or model.
// llm.go 用于实现固定模型的 LLM 包装器，让系统在不切换 provider、endpoint 或 model 的前提下轮换 API Key。
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

// LLMClient wraps one fixed provider/endpoint/model tuple with in-memory API-key failover.
// LLMClient 用于把一个固定 provider/endpoint/model 组合包装成带内存态 API Key 容灾能力的 LLM 客户端。
type LLMClient struct {
	selector     *selector
	endpoint     string
	model        string
	organization string
	project      string
	params       map[string]any
	modelParams  map[string]map[string]any
	factory      func(string) appports.LLMClient
	clients      map[string]appports.LLMClient
	mu           sync.Mutex
	options      Options
}

// NewLLMClient creates one fixed-model LLM client that can rotate across multiple API keys of the same upstream configuration.
// NewLLMClient 用于创建一个固定模型 LLM 客户端，让它能在同一上游配置的多个 API Key 之间轮换。
func NewLLMClient(endpoint, model, organization, project string, apiKeys []string, params map[string]any, modelParams map[string]map[string]any, options Options) (*LLMClient, error) {
	options.ServiceName = "llm"
	options.APIKeys = append([]string(nil), apiKeys...)
	selector, err := newSelector(options)
	if err != nil {
		return nil, err
	}
	client := &LLMClient{
		selector:     selector,
		endpoint:     strings.TrimSpace(endpoint),
		model:        strings.TrimSpace(model),
		organization: strings.TrimSpace(organization),
		project:      strings.TrimSpace(project),
		params:       params,
		modelParams:  modelParams,
		clients:      make(map[string]appports.LLMClient, len(apiKeys)),
		options:      options,
	}
	client.factory = func(apiKey string) appports.LLMClient {
		return openai_native.NewLLMClient(client.endpoint, apiKey, client.model, client.organization, client.project, client.params, client.modelParams)
	}
	return client, nil
}

// Generate executes one fixed-model LLM request and only rotates API keys on classified key-level failures.
// Generate 用于执行一次固定模型 LLM 请求，并且只在识别出的 Key 级故障下轮换 API Key。
func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c == nil || c.selector == nil {
		return appports.LLMResponse{}, fmt.Errorf("llm key failover client is nil")
	}
	if model := strings.TrimSpace(req.Model); model != "" && model != c.model {
		return appports.LLMResponse{}, fmt.Errorf("llm key failover requires fixed model %q, got %q", c.model, model)
	}
	req.Model = c.model
	return executeWithFailover(ctx, c.selector, func(ctx context.Context, apiKey string) (appports.LLMResponse, error) {
		return c.clientForKey(apiKey).Generate(ctx, req)
	}, func(err error, now time.Time) failureDecision {
		return classifyOpenAIError(err, c.options, now)
	})
}

// clientForKey returns the cached concrete LLM adapter for one API key or builds it on first use.
// clientForKey 用于返回某个 API Key 对应的缓存 LLM 适配器；若首次使用则即时构建。
func (c *LLMClient) clientForKey(apiKey string) appports.LLMClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[apiKey]; ok {
		return client
	}
	client := c.factory(apiKey)
	c.clients[apiKey] = client
	return client
}
