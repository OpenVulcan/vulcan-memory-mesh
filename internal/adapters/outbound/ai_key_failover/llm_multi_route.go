// llm_multi_route.go implements ordered provider/model route failover for LLM calls while preserving the existing per-route API-key failover contract.
// llm_multi_route.go 用于实现 LLM 调用的有序 provider/model 路由容灾，同时保留现有的“每条路由内部先做 API Key 容灾”契约。
package ai_key_failover

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// LLMRouteOptions describes one concrete LLM route, including its provider/model identity and the fixed-model key failover options owned by that route alone.
// LLMRouteOptions 用于描述一条具体的 LLM 路由，包括它自己的 provider/model 身份，以及仅属于该路由的固定模型 Key 容灾参数。
type LLMRouteOptions struct {
	Name             string
	SelectionWeights LLMRouteSelectionWeights
	Provider         string
	Endpoint         string
	Model            string
	Organization     string
	Project          string
	APIKeys          []string
	Params           map[string]any
	ModelParams      map[string]map[string]any
	Options          Options
}

// LLMRouteSelectionWeights keeps the fully materialized per-scene weights used to order one shared LLM route across different business chains.
// LLMRouteSelectionWeights 用于保存一条共享 LLM 路由在不同业务链路下排序时使用的完整分场景权重。
type LLMRouteSelectionWeights struct {
	PreCheckL1   int
	PreCheckL2   int
	PostActionL1 int
	PostActionL2 int
	Reserve      int
}

// WeightFor returns the concrete route weight for the requested business call tier and falls back to the reserve slot for unknown callers.
// WeightFor 用于返回目标业务调用层级对应的具体路由权重；若调用方未知，则回退到 reserve 槽位。
func (w LLMRouteSelectionWeights) WeightFor(level appports.LLMRouteSelectionLevel) int {
	switch level {
	case appports.LLMRouteSelectionLevelPreCheckL1:
		return w.PreCheckL1
	case appports.LLMRouteSelectionLevelPreCheckL2:
		return w.PreCheckL2
	case appports.LLMRouteSelectionLevelPostActionL1:
		return w.PostActionL1
	case appports.LLMRouteSelectionLevelPostActionL2:
		return w.PostActionL2
	default:
		return w.Reserve
	}
}

// llmMultiRouteEntry stores one compiled LLM route so the outer wrapper can iterate providers/models without rebuilding fixed-model key pools on every request.
// llmMultiRouteEntry 用于保存一条编译完成的 LLM 路由，让外层包装器在遍历 provider/model 时无需为每次请求重复构建固定模型 Key 池。
type llmMultiRouteEntry struct {
	name             string
	selectionWeights LLMRouteSelectionWeights
	model            string
	client           appports.LLMClient
	classify         func(error, time.Time) failureDecision
}

// LLMMultiRouteClient executes ordered multi-route failover for LLM requests and delegates each route's internal retries to the existing fixed-model key failover client.
// LLMMultiRouteClient 用于执行 LLM 请求的有序多路由容灾，并把每条路由内部的重试继续委托给现有的固定模型 Key 容灾客户端。
type LLMMultiRouteClient struct {
	routes []llmMultiRouteEntry
}

// NewLLMMultiRouteClient builds one ordered LLM multi-route client from explicit route declarations.
// NewLLMMultiRouteClient 用于根据显式路由声明构建一个有序的 LLM 多路由客户端。
func NewLLMMultiRouteClient(routes []LLMRouteOptions) (*LLMMultiRouteClient, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("llm multi-route client requires at least one route")
	}
	built := make([]llmMultiRouteEntry, 0, len(routes))
	for idx, route := range routes {
		name := buildMultiRouteName("llm", idx, route.Name)
		client, classifier, err := buildLLMMultiRouteEntry(route)
		if err != nil {
			return nil, fmt.Errorf("build llm route %q: %w", name, err)
		}
		built = append(built, llmMultiRouteEntry{
			name:             name,
			selectionWeights: route.SelectionWeights,
			model:            strings.TrimSpace(route.Model),
			client:           client,
			classify:         classifier,
		})
	}
	return &LLMMultiRouteClient{routes: built}, nil
}

// Generate runs one request against the first matching LLM route and only continues to later routes when the current route reports an exhaustion or switch-worthy upstream fault.
// Generate 用于把一次请求发往第一条匹配的 LLM 路由，且只有当前路由报告耗尽或值得切换的上游故障时，才继续尝试后续路由。
func (c *LLMMultiRouteClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	var zero appports.LLMResponse
	if c == nil || len(c.routes) == 0 {
		return zero, fmt.Errorf("llm multi-route client is nil")
	}
	candidateIndexes, err := c.candidateRouteIndexes(strings.TrimSpace(req.Model), req.RouteSelectionLevel)
	if err != nil {
		return zero, err
	}
	var lastSwitchableErr error
	for _, routeIndex := range candidateIndexes {
		route := c.routes[routeIndex]
		routeReq := req
		if strings.TrimSpace(routeReq.Model) == "" {
			routeReq.Model = route.model
		}
		response, callErr := route.client.Generate(ctx, routeReq)
		if callErr == nil {
			return response, nil
		}
		if shouldAbortRouteFailover(ctx, callErr) {
			return zero, callErr
		}
		if !shouldSwitchRoute(callErr, route.classify) {
			return zero, callErr
		}
		lastSwitchableErr = callErr
	}
	if lastSwitchableErr != nil {
		return zero, lastSwitchableErr
	}
	return zero, fmt.Errorf("no llm routes available for current request")
}

// candidateRouteIndexes filters routes by exact model match when needed and then reorders the remaining candidates by the requested business-call weight.
// candidateRouteIndexes 用于在必要时先按精确模型匹配过滤路由，再按目标业务调用层级对应的权重重排剩余候选。
func (c *LLMMultiRouteClient) candidateRouteIndexes(requestedModel string, selectionLevel appports.LLMRouteSelectionLevel) ([]int, error) {
	if len(c.routes) == 0 {
		return nil, fmt.Errorf("llm multi-route client has no routes")
	}
	indexes := make([]int, 0, len(c.routes))
	for idx, route := range c.routes {
		if requestedModel == "" || route.model == requestedModel {
			indexes = append(indexes, idx)
		}
	}
	if len(indexes) == 0 && requestedModel != "" {
		return nil, fmt.Errorf("no llm route configured for model %q", requestedModel)
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left := c.routes[indexes[i]].selectionWeights.WeightFor(selectionLevel)
		right := c.routes[indexes[j]].selectionWeights.WeightFor(selectionLevel)
		return left > right
	})
	return indexes, nil
}

// buildLLMMultiRouteEntry compiles one LLM route into the existing fixed-model key failover client and returns the provider-specific error classifier used for route switching.
// buildLLMMultiRouteEntry 用于把单条 LLM 路由编译成现有固定模型 Key 容灾客户端，并返回路由切换所需的 provider 专属错误分类器。
func buildLLMMultiRouteEntry(route LLMRouteOptions) (appports.LLMClient, func(error, time.Time) failureDecision, error) {
	client, err := NewProviderLLMClient(
		route.Provider,
		route.Endpoint,
		route.Model,
		route.Organization,
		route.Project,
		route.APIKeys,
		route.Params,
		route.ModelParams,
		route.Options,
	)
	if err != nil {
		return nil, nil, err
	}
	_, classifier, err := newLLMProviderFactory(route.Provider, route.Endpoint, route.Model, route.Organization, route.Project, route.Params, route.ModelParams, route.Options)
	if err != nil {
		return nil, nil, err
	}
	return client, classifier, nil
}
