// runtime_ai.go implements application-layer AI dependency builders shared by the local runtime composition root and maintenance entrypoints.
// runtime_ai.go 用于实现本地运行时组合根与维护入口共用的应用层 AI 依赖构建逻辑。
package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// runtimeAIDependencies groups the generation, embedding, and rerank clients selected for one runtime so the composition root can wire downstream use cases without repeatedly rebuilding the same adapter set.
// runtimeAIDependencies 用于打包某个运行时选中的生成、embedding 和 rerank 客户端，让组合根在接线下游用例时无需重复构建同一组适配器。
type runtimeAIDependencies struct {
	LLM       appports.LLMClient
	Embedding appports.EmbeddingClient
	Reranker  appports.RerankerClient
}

// routeFailoverAwareLLMClient preserves prompt-side model routing while clearing request-level model pins in multi-route mode so processor calls can still fan out across heterogeneous llm.routes.
// routeFailoverAwareLLMClient 用于在保留提示词侧模型路由的同时，于多路由模式清空请求级模型固定值，确保处理器调用仍能在不同模型名的 llm.routes 之间扩散容灾。
type routeFailoverAwareLLMClient struct {
	upstream appports.LLMClient
}

// buildRuntimeAIDependencies materializes the full AI adapter bundle required by the local runtime before use-case assembly begins.
// buildRuntimeAIDependencies 用于在用例装配开始前，构建本地运行时所需的完整 AI 适配器集合。
func buildRuntimeAIDependencies(cfg config.Config) (runtimeAIDependencies, error) {
	llm, err := buildLLM(cfg)
	if err != nil {
		return runtimeAIDependencies{}, err
	}
	embedding, err := buildEmbedding(cfg)
	if err != nil {
		return runtimeAIDependencies{}, err
	}
	reranker, err := buildReranker(cfg)
	if err != nil {
		return runtimeAIDependencies{}, err
	}
	return runtimeAIDependencies{
		LLM:       llm,
		Embedding: embedding,
		Reranker:  reranker,
	}, nil
}

// Generate forwards one processor-originated LLM request after dropping the fixed model field that would otherwise block heterogeneous route failover.
// Generate 用于转发一条来自处理器的 LLM 请求，并在转发前移除会阻断异构路由容灾的固定模型字段。
//
// The model field is cleared because multi-route mode requires the downstream failover wrapper to select
// the actual target model based on per-scene route weights; leaving a hard-coded model pin would prevent
// the failover layer from distributing requests across different models in the route pool.
// 清空 model 字段是因为多路由模式要求下游容灾包装器根据分场景权重选择实际目标模型；
// 如果保留硬固定的 model 值，会阻止容灾层在路由池的不同模型之间分发请求。
func (c routeFailoverAwareLLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.upstream == nil {
		return appports.LLMResponse{}, fmt.Errorf("route failover aware llm client is nil")
	}
	// Clear the model field so the downstream multi-route failover wrapper can pick the correct model
	// from the route pool based on the active selection level (precheck/postaction/reserve).
	// 清空 model 字段，让下游多路由容灾包装器能根据当前选择层级从路由池中选出正确模型。
	req.Model = ""
	return c.upstream.Generate(ctx, req)
}

// selectProcessorLLMModel chooses the primary model label for one business call tier without coupling prompt selection to model-specific prompt folders.
// selectProcessorLLMModel 用于为某个业务调用层级选择主模型标识，同时避免再把提示词选择耦合到模型专属提示词目录。
func selectProcessorLLMModel(cfg config.Config, selectionLevel appports.LLMRouteSelectionLevel) string {
	return cfg.LLM.PrimaryModelForSelection(string(selectionLevel))
}

// buildLLM selects the configured generation backend used by the debug-stage post-action summary probe and future LLM-driven workflows.
// buildLLM 用于选择当前配置的生成后端，服务调试阶段的 post-action 摘要探测以及未来的 LLM 工作流。
func buildLLM(cfg config.Config) (appports.LLMClient, error) {
	cfg.Normalize()
	routes := cfg.LLM.ProviderRoutes()
	if len(routes) == 0 {
		return nil, fmt.Errorf("llm.routes must contain at least one route")
	}
	if len(routes) == 1 {
		return buildOneLLMRouteClient(routes[0], 0)
	}
	routeOptions := make([]ai_key_failover.LLMRouteOptions, 0, len(routes))
	for idx, route := range routes {
		resolvedWeights := route.ResolvedWeights()
		routeOptions = append(routeOptions, ai_key_failover.LLMRouteOptions{
			Name: buildRouteName("llm", idx, route.Name),
			SelectionWeights: ai_key_failover.LLMRouteSelectionWeights{
				PreCheckL1:   resolvedWeights.PreCheckL1,
				PreCheckL2:   resolvedWeights.PreCheckL2,
				PostActionL1: resolvedWeights.PostActionL1,
				PostActionL2: resolvedWeights.PostActionL2,
				Reserve:      resolvedWeights.Reserve,
			},
			Provider:     route.Provider,
			Endpoint:     route.Endpoint,
			Model:        route.Model,
			Organization: route.Organization,
			Project:      route.Project,
			APIKeys:      append([]string(nil), route.APIKeys...),
			Params:       route.Params,
			ModelParams:  route.ModelParams,
			Options:      buildKeyFailoverOptions(buildRouteName("llm", idx, route.Name), route.Nodes, route.KeyFailover),
		})
	}
	return ai_key_failover.NewLLMMultiRouteClient(routeOptions)
}

// adaptLLMForProcessorRoutes keeps prompt routing anchored to the primary model while freeing processor-originated requests from route-blocking model pins in multi-route mode.
// adaptLLMForProcessorRoutes 用于让提示词路由继续锚定主模型，同时在多路由模式下解除处理器请求对特定模型名的硬固定，避免阻断路由级容灾。
func adaptLLMForProcessorRoutes(cfg config.Config, client appports.LLMClient) appports.LLMClient {
	if len(cfg.LLM.ProviderRoutes()) <= 1 {
		return client
	}
	return routeFailoverAwareLLMClient{upstream: client}
}

// buildOneLLMRouteClient materializes one concrete fixed-model LLM route that will handle provider-specific key failover inside its own key pool.
// buildOneLLMRouteClient 用于实例化一条具体的固定模型 LLM 路由，让它在自己的 Key 池内部处理 provider 专属的 Key 容灾。
func buildOneLLMRouteClient(route config.LLMRouteConfig, routeIndex int) (appports.LLMClient, error) {
	return ai_key_failover.NewProviderLLMClient(
		route.Provider,
		route.Endpoint,
		route.Model,
		route.Organization,
		route.Project,
		route.APIKeys,
		route.Params,
		route.ModelParams,
		buildKeyFailoverOptions(buildRouteName("llm", routeIndex, route.Name), route.Nodes, route.KeyFailover),
	)
}

// buildEmbedding selects the configured real embedding adapter for recall and semantic filtering.
// buildEmbedding 用于为召回和语义过滤选择当前配置的真实 embedding 适配器。
func buildEmbedding(cfg config.Config) (appports.EmbeddingClient, error) {
	cfg.Normalize()
	return ai_key_failover.NewProviderEmbeddingClient(
		cfg.Embedding.Provider,
		cfg.Embedding.Endpoint,
		cfg.Embedding.Model,
		cfg.Embedding.Dimension,
		cfg.Embedding.MaxBatchSize,
		cfg.Embedding.Organization,
		cfg.Embedding.Project,
		cfg.Embedding.APIKeys,
		cfg.Embedding.Params,
		cfg.Embedding.ModelParams,
		buildKeyFailoverOptions("embedding", cfg.Embedding.RoutingNodes(), cfg.Embedding.KeyFailover),
	)
}

// buildReranker selects the optional second-stage rerank backend used to reorder first-stage vector recall hits.
// buildReranker 用于选择可选的第二阶段重排序后端，对首轮向量召回结果重新排序。
func buildReranker(cfg config.Config) (appports.RerankerClient, error) {
	cfg.Normalize()
	if !cfg.Rerank.Enabled {
		return nil, nil
	}
	routes := cfg.Rerank.ProviderRoutes()
	if len(routes) == 0 {
		return nil, fmt.Errorf("rerank.routes must contain at least one route when rerank is enabled")
	}
	if len(routes) == 1 {
		return buildOneRerankRouteClient(routes[0], 0)
	}
	routeOptions := make([]ai_key_failover.RerankRouteOptions, 0, len(routes))
	for idx, route := range routes {
		routeOptions = append(routeOptions, ai_key_failover.RerankRouteOptions{
			Name:     buildRouteName("rerank", idx, route.Name),
			Priority: route.Priority,
			Provider: route.Provider,
			Endpoint: route.Endpoint,
			Model:    route.Model,
			Timeout:  route.Timeout.Duration,
			APIKeys:  append([]string(nil), route.APIKeys...),
			Options:  buildKeyFailoverOptions(buildRouteName("rerank", idx, route.Name), route.Nodes, route.KeyFailover),
		})
	}
	return ai_key_failover.NewRerankMultiRouteClient(routeOptions)
}

// buildOneRerankRouteClient materializes one concrete fixed-model rerank route that will handle provider-specific key failover inside its own key pool.
// buildOneRerankRouteClient 用于实例化一条具体的固定模型 rerank 路由，让它在自己的 Key 池内部处理 provider 专属的 Key 容灾。
func buildOneRerankRouteClient(route config.RerankRouteConfig, routeIndex int) (appports.RerankerClient, error) {
	return ai_key_failover.NewProviderRerankerClient(
		route.Provider,
		route.Endpoint,
		route.Model,
		route.Timeout.Duration,
		route.APIKeys,
		buildKeyFailoverOptions(buildRouteName("rerank", routeIndex, route.Name), route.Nodes, route.KeyFailover),
	)
}

// buildRouteName returns one stable route label so route-level failover logs and selector error messages can point back to the configured route order.
// buildRouteName 用于返回稳定的路由标签，让路由级容灾日志和选择器错误信息都能回指到配置里的路由顺序。
func buildRouteName(serviceName string, routeIndex int, routeName string) string {
	trimmed := strings.TrimSpace(routeName)
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("%s-route-%d", serviceName, routeIndex+1)
}

// buildKeyFailoverOptions converts one normalized runtime config into the fixed-model API-key failover options consumed by outbound wrappers.
// buildKeyFailoverOptions 用于把一份已归一化的运行时配置转换为出站包装器消费的固定模型 API Key 容灾参数。
func buildKeyFailoverOptions(serviceName string, nodes []config.AIRoutingNodeConfig, cfg config.KeyFailoverConfig) ai_key_failover.Options {
	routingNodes := make([]ai_key_failover.NodeOptions, 0, len(nodes))
	totalCandidates := 0
	for idx, node := range nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("%s-node-%d", serviceName, idx+1)
		}
		apiKeys := append([]string(nil), node.APIKeys...)
		totalCandidates += len(apiKeys)
		routingNodes = append(routingNodes, ai_key_failover.NodeOptions{
			Name:    name,
			APIKeys: apiKeys,
			RPM:     node.RPM,
			TPM:     node.TPM,
			RPD:     node.RPD,
		})
	}
	return ai_key_failover.Options{
		ServiceName:        serviceName,
		Enabled:            cfg.Enabled && totalCandidates > 1,
		Policy:             strings.TrimSpace(cfg.Policy),
		Nodes:              routingNodes,
		RespectRetryAfter:  cfg.RespectRetryAfter,
		RateLimitCooldown:  cfg.RateLimitCooldown.Duration,
		QuotaCooldown:      cfg.QuotaCooldown.Duration,
		AuthCooldown:       cfg.AuthCooldown.Duration,
		ProbeAfterCooldown: cfg.ProbeAfterCooldown,
	}
}
