// rerank_multi_route.go implements ordered provider/model route failover for rerank calls while preserving the existing per-route API-key failover contract.
// rerank_multi_route.go 用于实现 rerank 调用的有序 provider/model 路由容灾，同时保留现有的“每条路由内部先做 API Key 容灾”契约。
package ai_key_failover

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// RerankRouteOptions describes one concrete rerank route, including its provider/model identity and the fixed-model key failover options owned by that route alone.
// RerankRouteOptions 用于描述一条具体的 rerank 路由，包括它自己的 provider/model 身份，以及仅属于该路由的固定模型 Key 容灾参数。
type RerankRouteOptions struct {
	Name     string
	Priority int
	Provider string
	Endpoint string
	Model    string
	Timeout  time.Duration
	APIKeys  []string
	Options  Options
}

// rerankMultiRouteEntry stores one compiled rerank route so the outer wrapper can iterate providers/models without rebuilding fixed-model key pools on every request.
// rerankMultiRouteEntry 用于保存一条编译完成的 rerank 路由，让外层包装器在遍历 provider/model 时无需为每次请求重复构建固定模型 Key 池。
type rerankMultiRouteEntry struct {
	name     string
	priority int
	client   appports.RerankerClient
	classify func(error, time.Time) failureDecision
}

// RerankMultiRouteClient executes ordered multi-route failover for rerank requests and delegates each route's internal retries to the existing fixed-model key failover client.
// RerankMultiRouteClient 用于执行 rerank 请求的有序多路由容灾，并把每条路由内部的重试继续委托给现有的固定模型 Key 容灾客户端。
type RerankMultiRouteClient struct {
	routes []rerankMultiRouteEntry
}

// NewRerankMultiRouteClient builds one ordered rerank multi-route client from explicit route declarations.
// NewRerankMultiRouteClient 用于根据显式路由声明构建一个有序的 rerank 多路由客户端。
func NewRerankMultiRouteClient(routes []RerankRouteOptions) (*RerankMultiRouteClient, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("rerank multi-route client requires at least one route")
	}
	built := make([]rerankMultiRouteEntry, 0, len(routes))
	for idx, route := range routes {
		name := buildMultiRouteName("rerank", idx, route.Name)
		client, classifier, err := buildRerankMultiRouteEntry(route)
		if err != nil {
			return nil, fmt.Errorf("build rerank route %q: %w", name, err)
		}
		built = append(built, rerankMultiRouteEntry{
			name:     name,
			priority: route.Priority,
			client:   client,
			classify: classifier,
		})
	}
	sort.SliceStable(built, func(i, j int) bool {
		return built[i].priority > built[j].priority
	})
	return &RerankMultiRouteClient{routes: built}, nil
}

// Rerank runs one request against the configured rerank routes in order and only continues when the current route reports an exhaustion or switch-worthy upstream fault.
// Rerank 用于按配置顺序对 rerank 路由执行一次请求，并且只有当前路由报告耗尽或值得切换的上游故障时，才继续尝试后续路由。
func (c *RerankMultiRouteClient) Rerank(ctx context.Context, query string, docs []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	if c == nil || len(c.routes) == 0 {
		return nil, fmt.Errorf("rerank multi-route client is nil")
	}
	var lastSwitchableErr error
	for _, route := range c.routes {
		results, callErr := route.client.Rerank(ctx, query, docs, topN)
		if callErr == nil {
			return results, nil
		}
		if shouldAbortRouteFailover(ctx, callErr) {
			return nil, callErr
		}
		if !shouldSwitchRoute(callErr, route.classify) {
			return nil, callErr
		}
		lastSwitchableErr = callErr
	}
	if lastSwitchableErr != nil {
		return nil, lastSwitchableErr
	}
	return nil, fmt.Errorf("no rerank routes available for current request")
}

// buildRerankMultiRouteEntry compiles one rerank route into the existing fixed-model key failover client and returns the provider-specific error classifier used for route switching.
// buildRerankMultiRouteEntry 用于把单条 rerank 路由编译成现有固定模型 Key 容灾客户端，并返回路由切换所需的 provider 专属错误分类器。
func buildRerankMultiRouteEntry(route RerankRouteOptions) (appports.RerankerClient, func(error, time.Time) failureDecision, error) {
	switch strings.ToLower(strings.TrimSpace(route.Provider)) {
	case "dashscope":
		client, err := NewRerankerClient(route.Endpoint, route.Model, route.Timeout, route.APIKeys, route.Options)
		if err != nil {
			return nil, nil, err
		}
		return client, func(err error, now time.Time) failureDecision {
			return classifyDashScopeError(err, route.Options, now)
		}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported rerank provider: %s", route.Provider)
	}
}
