// config_ai_routing.go keeps AI routing defaults, normalization, and route validation helpers used by config loading and runtime selection.
// config_ai_routing.go 用于承载配置加载与运行时选路共用的 AI 路由默认值、归一化与路由校验辅助逻辑。
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// float64Ptr returns one addressable float literal so config defaults and env overrides can reuse pointer-backed optional numeric fields without temporary variables.
// float64Ptr 用于返回一个可寻址的浮点字面量地址，让配置默认值和环境变量覆盖可以复用基于指针的可选数值字段，而无需引入临时变量。
func float64Ptr(v float64) *float64 { return &v }

// defaultKeyFailoverConfig returns the stable API-key failover defaults shared by fixed-model AI clients.
// defaultKeyFailoverConfig 用于返回固定模型 AI 客户端共享的稳定 API Key 容灾默认值。
func defaultKeyFailoverConfig() KeyFailoverConfig {
	return KeyFailoverConfig{
		Enabled:            true,
		Policy:             "ordered_failover",
		RespectRetryAfter:  true,
		RateLimitCooldown:  Duration{5 * time.Minute},
		QuotaCooldown:      Duration{10 * time.Minute},
		AuthCooldown:       Duration{12 * time.Hour},
		ProbeAfterCooldown: true,
	}
}

// RoutingNodes returns the normalized embedding routing nodes, synthesizing one default node from top-level api_keys when explicit nodes are absent.
// RoutingNodes 用于返回归一化后的 embedding 轮询节点；当未显式声明 nodes 时，会把顶层 api_keys 折叠成一个默认节点。
func (c EmbeddingConfig) RoutingNodes() []AIRoutingNodeConfig {
	return normalizeAIRoutingNodes(c.APIKeys, c.RPM, c.TPM, c.RPD, c.Nodes)
}

// ResolvedMaxBatchSize returns the effective embedding batch size after defaulting so runtime batching code can stay aligned with config normalization.
// ResolvedMaxBatchSize 用于返回补齐默认值后的 embedding 批大小，让运行时批处理逻辑与配置归一化保持一致。
func (c EmbeddingConfig) ResolvedMaxBatchSize() int {
	if c.MaxBatchSize > 0 {
		return c.MaxBatchSize
	}
	return defaultEmbeddingMaxBatchSize
}

// ProviderRoutes returns the normalized explicit LLM route list.
// ProviderRoutes 用于返回归一化后的显式 LLM 路由列表。
func (c LLMConfig) ProviderRoutes() []LLMRouteConfig {
	return normalizeLLMRouteConfigs(c.Routes)
}

// PrimaryRoute returns the default-route view used when callers do not request one explicit business selection level.
// PrimaryRoute 用于返回未显式指定业务选择层级时采用的默认路由视图。
func (c LLMConfig) PrimaryRoute() (LLMRouteConfig, bool) {
	return c.PrimaryRouteForSelection("")
}

// PrimaryRouteForSelection returns the highest-weight LLM route for one business call tier while preserving declaration order for ties.
// PrimaryRouteForSelection 用于返回某个业务调用层级下权重最高的 LLM 路由；若权重相同，则保持声明顺序。
func (c LLMConfig) PrimaryRouteForSelection(selectionLevel string) (LLMRouteConfig, bool) {
	return primaryLLMRouteForSelection(c.ProviderRoutes(), selectionLevel)
}

// PrimaryModel returns the default primary LLM model chosen from the reserve-weight route view.
// PrimaryModel 用于返回基于 reserve 权重视图选出的默认主 LLM 模型名称。
func (c LLMConfig) PrimaryModel() string {
	return c.PrimaryModelForSelection("")
}

// PrimaryModelForSelection returns the route model chosen for one business call tier after applying per-scene route weights.
// PrimaryModelForSelection 用于在应用分场景路由权重后，返回某个业务调用层级选中的路由模型。
func (c LLMConfig) PrimaryModelForSelection(selectionLevel string) string {
	route, ok := c.PrimaryRouteForSelection(selectionLevel)
	if !ok {
		return ""
	}
	return route.Model
}

// ProviderRoutes returns the normalized explicit rerank route list.
// ProviderRoutes 用于返回归一化后的显式 rerank 路由列表。
func (c RerankConfig) ProviderRoutes() []RerankRouteConfig {
	return normalizeRerankRouteConfigs(c.Routes)
}

// primaryLLMRouteForSelection returns the highest-weight LLM route for one business call tier while preserving declaration order for ties.
// primaryLLMRouteForSelection 用于返回某个业务调用层级下权重最高的 LLM 路由；若权重相同，则保持声明顺序。
func primaryLLMRouteForSelection(routes []LLMRouteConfig, selectionLevel string) (LLMRouteConfig, bool) {
	if len(routes) == 0 {
		return LLMRouteConfig{}, false
	}
	best := routes[0]
	bestWeight := best.SelectionWeight(selectionLevel)
	for _, route := range routes[1:] {
		if weight := route.SelectionWeight(selectionLevel); weight > bestWeight {
			best = route
			bestWeight = weight
		}
	}
	return best, true
}

// ResolvedWeights expands one LLM route's optional per-scene overrides into a full six-slot weight table backed by the shared default weight.
// ResolvedWeights 用于把单条 LLM 路由的可选分场景覆盖项展开成完整的六槽位权重表；未声明槽位统一回退到共享默认权重。
func (c LLMRouteConfig) ResolvedWeights() LLMRouteResolvedWeights {
	return LLMRouteResolvedWeights{
		PreCheckL1:         resolveLLMRouteWeight(c.Weights.PreCheckL1, defaultLLMRouteSelectionWeight),
		PreCheckL2:         resolveLLMRouteWeight(c.Weights.PreCheckL2, defaultLLMRouteSelectionWeight),
		PostActionL1:       resolveLLMRouteWeight(c.Weights.PostActionL1, defaultLLMRouteSelectionWeight),
		PostActionL2:       resolveLLMRouteWeight(c.Weights.PostActionL2, defaultLLMRouteSelectionWeight),
		ProfileInstruction: resolveLLMRouteWeight(c.Weights.ProfileInstruction, defaultLLMRouteSelectionWeight),
		Reserve:            resolveLLMRouteWeight(c.Weights.Reserve, defaultLLMRouteSelectionWeight),
	}
}

// SelectionWeight returns the concrete weight that one business call tier should use when ordering LLM routes.
// SelectionWeight 用于返回某个业务调用层级在排序 LLM 路由时应采用的具体权重。
func (c LLMRouteConfig) SelectionWeight(selectionLevel string) int {
	weights := c.ResolvedWeights()
	switch strings.ToLower(strings.TrimSpace(selectionLevel)) {
	case "precheck_l1":
		return weights.PreCheckL1
	case "precheck_l2":
		return weights.PreCheckL2
	case "postaction_l1":
		return weights.PostActionL1
	case "postaction_l2":
		return weights.PostActionL2
	case "profile_instruction":
		return weights.ProfileInstruction
	default:
		return weights.Reserve
	}
}

// resolveLLMRouteWeight prefers one explicit per-scene override and otherwise falls back to the shared default weight source.
// resolveLLMRouteWeight 用于优先采用显式分场景覆盖值；如果缺失，则回退到共享默认权重来源。
func resolveLLMRouteWeight(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

// normalizeKeyFailoverPolicyValue canonicalizes the in-memory API-key rotation policy so config defaults, env overrides, and runtime wiring all compare one stable token.
// normalizeKeyFailoverPolicyValue 用于规范化内存态 API Key 轮换策略，让默认值、环境变量覆盖和运行时装配始终比较同一份稳定 token。
func normalizeKeyFailoverPolicyValue(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "round_robin":
		return "round_robin"
	default:
		return "ordered_failover"
	}
}

// splitConfigAPIKeys expands one raw config string into individual API keys using comma, semicolon, or newline separators.
// splitConfigAPIKeys 用于把单个原始配置字符串按逗号、分号或换行分隔为多个独立 API Key。
func splitConfigAPIKeys(raw string) []string {
	replacer := strings.NewReplacer("\r\n", "\n", "\r", "\n", ";", "\n", ",", "\n")
	return strings.Split(replacer.Replace(raw), "\n")
}

// trimStringSlice removes surrounding whitespace and drops empty items from one string slice while preserving order.
// trimStringSlice 用于裁剪字符串切片中的首尾空白并去掉空项，同时保持原始顺序不变。
func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

// normalizeAPIKeys expands raw api_keys declarations, trims separators, and removes duplicates while preserving caller order.
// normalizeAPIKeys 用于展开原始 api_keys 声明，裁剪分隔符并在保持顺序的前提下去重。
func normalizeAPIKeys(values []string) []string {
	merged := make([]string, 0, len(values))
	for _, value := range values {
		merged = append(merged, splitConfigAPIKeys(value)...)
	}
	merged = trimStringSlice(merged)
	if len(merged) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(merged))
	normalized := make([]string, 0, len(merged))
	for _, value := range merged {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

// normalizeAIRoutingNodes canonicalizes routing-node declarations and can synthesize one default node from top-level api_keys plus shared budgets.
// normalizeAIRoutingNodes 用于规范化轮询节点声明，并可根据顶层 api_keys 与共享预算合成一个默认节点。
func normalizeAIRoutingNodes(apiKeys []string, rpm, tpm, rpd int, nodes []AIRoutingNodeConfig) []AIRoutingNodeConfig {
	if len(nodes) == 0 {
		apiKeys = normalizeAPIKeys(apiKeys)
		if len(apiKeys) == 0 {
			return nil
		}
		return []AIRoutingNodeConfig{{
			APIKeys: apiKeys,
			RPM:     rpm,
			TPM:     tpm,
			RPD:     rpd,
		}}
	}
	normalized := make([]AIRoutingNodeConfig, 0, len(nodes))
	for _, node := range nodes {
		current := AIRoutingNodeConfig{
			Name:    strings.TrimSpace(node.Name),
			APIKeys: normalizeAPIKeys(node.APIKeys),
			RPM:     node.RPM,
			TPM:     node.TPM,
			RPD:     node.RPD,
		}
		normalized = append(normalized, current)
	}
	return normalized
}

// normalizeAIRoutingNodeFields trims routing-node strings in place so later validation and runtime wiring see stable values even before full normalization runs.
// normalizeAIRoutingNodeFields 用于原地裁剪轮询节点里的字符串字段，让后续校验和运行时装配在完整归一化前也能看到稳定值。
func normalizeAIRoutingNodeFields(nodes []AIRoutingNodeConfig) []AIRoutingNodeConfig {
	if len(nodes) == 0 {
		return nil
	}
	normalized := make([]AIRoutingNodeConfig, 0, len(nodes))
	for _, node := range nodes {
		node.Name = strings.TrimSpace(node.Name)
		node.APIKeys = trimStringSlice(node.APIKeys)
		normalized = append(normalized, node)
	}
	return normalized
}

// normalizeLLMRouteConfig trims one LLM route, normalizes its API key pool, folds legacy key pools into routing nodes, and fills safe key-failover defaults for the route itself.
// normalizeLLMRouteConfig 用于裁剪单条 LLM 路由，规范化其 API Key 池，把旧版 key 池折叠成轮询节点，并为该路由补齐安全的 key-failover 默认值。
func normalizeLLMRouteConfig(route LLMRouteConfig) LLMRouteConfig {
	route.Name = strings.TrimSpace(route.Name)
	route.Provider = strings.TrimSpace(route.Provider)
	route.Endpoint = strings.TrimSpace(route.Endpoint)
	route.APIKeys = trimStringSlice(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
	route.Model = strings.TrimSpace(route.Model)
	route.Organization = strings.TrimSpace(route.Organization)
	route.Project = strings.TrimSpace(route.Project)
	route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
	route.APIKeys = normalizeAPIKeys(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodes(route.APIKeys, route.RPM, route.TPM, route.RPD, route.Nodes)
	normalizeKeyFailoverConfig(&route.KeyFailover)
	return route
}

// normalizeRerankRouteConfig trims one rerank route, normalizes its API key pool, folds legacy key pools into routing nodes, and fills safe key-failover defaults for the route itself.
// normalizeRerankRouteConfig 用于裁剪单条 rerank 路由，规范化其 API Key 池，把旧版 key 池折叠成轮询节点，并为该路由补齐安全的 key-failover 默认值。
func normalizeRerankRouteConfig(route RerankRouteConfig) RerankRouteConfig {
	route.Name = strings.TrimSpace(route.Name)
	route.Provider = normalizeRerankProviderValue(route.Provider)
	route.Endpoint = strings.TrimSpace(route.Endpoint)
	if route.Endpoint == "" {
		route.Endpoint = rerankProviderDefaultEndpoint(route.Provider)
	}
	route.APIKeys = trimStringSlice(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
	route.Model = strings.TrimSpace(route.Model)
	if route.Model == "" {
		route.Model = rerankProviderDefaultModel(route.Provider)
	}
	if route.Timeout.Duration <= 0 {
		route.Timeout = Duration{8 * time.Second}
	}
	route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
	route.APIKeys = normalizeAPIKeys(route.APIKeys)
	route.Nodes = normalizeAIRoutingNodes(route.APIKeys, route.RPM, route.TPM, route.RPD, route.Nodes)
	normalizeKeyFailoverConfig(&route.KeyFailover)
	return route
}

// normalizeLLMRouteConfigs normalizes one explicit LLM route slice in declaration order so config loading and runtime wiring share the same route view.
// normalizeLLMRouteConfigs 用于按声明顺序规范化显式 LLM 路由切片，让配置加载与运行时装配共享同一份路由视图。
func normalizeLLMRouteConfigs(routes []LLMRouteConfig) []LLMRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	normalized := make([]LLMRouteConfig, 0, len(routes))
	for _, route := range routes {
		normalized = append(normalized, normalizeLLMRouteConfig(route))
	}
	return normalized
}

// normalizeRerankRouteConfigs normalizes one explicit rerank route slice in declaration order so config loading and runtime wiring share the same route view.
// normalizeRerankRouteConfigs 用于按声明顺序规范化显式 rerank 路由切片，让配置加载与运行时装配共享同一份路由视图。
func normalizeRerankRouteConfigs(routes []RerankRouteConfig) []RerankRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	normalized := make([]RerankRouteConfig, 0, len(routes))
	for _, route := range routes {
		normalized = append(normalized, normalizeRerankRouteConfig(route))
	}
	return normalized
}

// trimLLMRouteFields trims only runtime-facing string fields inside one explicit LLM route slice, keeping the rest of Normalize responsible for full canonicalization.
// trimLLMRouteFields 用于只裁剪显式 LLM 路由切片中的运行时字符串字段，把完整规范化职责继续留给后续 Normalize 阶段。
func trimLLMRouteFields(routes []LLMRouteConfig) []LLMRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	trimmed := make([]LLMRouteConfig, 0, len(routes))
	for _, route := range routes {
		route.Name = strings.TrimSpace(route.Name)
		route.Provider = strings.TrimSpace(route.Provider)
		route.Endpoint = strings.TrimSpace(route.Endpoint)
		route.APIKeys = trimStringSlice(route.APIKeys)
		route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
		route.Model = strings.TrimSpace(route.Model)
		route.Organization = strings.TrimSpace(route.Organization)
		route.Project = strings.TrimSpace(route.Project)
		route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
		trimmed = append(trimmed, route)
	}
	return trimmed
}

// trimRerankRouteFields trims only runtime-facing string fields inside one explicit rerank route slice, keeping the rest of Normalize responsible for full canonicalization.
// trimRerankRouteFields 用于只裁剪显式 rerank 路由切片中的运行时字符串字段，把完整规范化职责继续留给后续 Normalize 阶段。
func trimRerankRouteFields(routes []RerankRouteConfig) []RerankRouteConfig {
	if len(routes) == 0 {
		return nil
	}
	trimmed := make([]RerankRouteConfig, 0, len(routes))
	for _, route := range routes {
		route.Name = strings.TrimSpace(route.Name)
		route.Provider = strings.TrimSpace(route.Provider)
		route.Endpoint = strings.TrimSpace(route.Endpoint)
		route.APIKeys = trimStringSlice(route.APIKeys)
		route.Nodes = normalizeAIRoutingNodeFields(route.Nodes)
		route.Model = strings.TrimSpace(route.Model)
		route.KeyFailover.Policy = strings.TrimSpace(route.KeyFailover.Policy)
		trimmed = append(trimmed, route)
	}
	return trimmed
}

// validateAIRoutingNodes verifies that each routing node exposes at least one key and only non-negative budget limits.
// validateAIRoutingNodes 用于校验每个轮询节点都至少暴露一个 Key，并且预算限制必须是非负数。
func validateAIRoutingNodes(serviceName string, nodes []AIRoutingNodeConfig) error {
	for idx, node := range nodes {
		label := fmt.Sprintf("%s.nodes[%d]", serviceName, idx)
		if len(node.APIKeys) == 0 {
			return fmt.Errorf("%s.api_keys is required", label)
		}
		if node.RPM < 0 {
			return fmt.Errorf("%s.rpm must be >= 0", label)
		}
		if node.TPM < 0 {
			return fmt.Errorf("%s.tpm must be >= 0", label)
		}
		if node.RPD < 0 {
			return fmt.Errorf("%s.rpd must be >= 0", label)
		}
	}
	return nil
}

// validateLLMRouteConfigs verifies each explicit LLM route remains self-contained so multi-route failover never falls back to partially inherited runtime wiring.
// validateLLMRouteConfigs 用于校验每条显式 LLM 路由都保持自包含，避免多路由容灾退化成依赖部分隐式继承的运行时装配。
func validateLLMRouteConfigs(routes []LLMRouteConfig) error {
	if len(routes) == 0 {
		return errors.New("llm.routes must contain at least one route when declared")
	}
	for idx, route := range routes {
		label := fmt.Sprintf("llm.routes[%d]", idx)
		if strings.TrimSpace(route.Provider) == "" {
			return fmt.Errorf("%s.provider is required", label)
		}
		if !isSupportedAIProvider(route.Provider) {
			return fmt.Errorf("%s.provider must be one of openai, openai_native, openai_go, google_ai_studio, or openrouter", label)
		}
		if providerRequiresEndpoint(route.Provider) && strings.TrimSpace(route.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required", label)
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("%s.model is required", label)
		}
		if len(route.Nodes) == 0 {
			return fmt.Errorf("%s.api_keys or %s.nodes is required", label, label)
		}
		if err := validateAIRoutingNodes(label, route.Nodes); err != nil {
			return err
		}
		if err := validateLLMRouteWeightConfig(label, route.Weights); err != nil {
			return err
		}
		if err := validateLLMDisabledReasoningProjection(label, route); err != nil {
			return err
		}
	}
	return nil
}

// validateLLMDisabledReasoningProjection requires every generic OpenAI-compatible route to declare the exact request field that its adapter will force to the provider's disabled value.
// validateLLMDisabledReasoningProjection 用于要求每条通用 OpenAI 兼容路由声明精确请求字段，适配器会把该字段强制改写为供应商关闭值。
//
// Parameters:
// 参数：
//   - label: stable configuration path used in startup diagnostics.
//   - label：启动诊断使用的稳定配置路径。
//   - route: normalized standalone LLM route including route-level and exact-model parameters.
//   - route：已规范化的独立 LLM 路由，包含路由级与精确模型级参数。
//
// Returns:
// 返回值：
//   - error: nil for adapters that intrinsically force no-thinking or for an OpenAI route with one registered projection key; otherwise a deterministic startup error.
//   - error：适配器内建强制无思考，或 OpenAI 路由含一个已注册投影键时为空；否则返回确定性的启动错误。
func validateLLMDisabledReasoningProjection(label string, route LLMRouteConfig) error {
	if !isOpenAIProvider(route.Provider) {
		// Managed Responses, native OpenRouter, and Google AI Studio own unconditional no-thinking
		// projections in their adapters and therefore need no user-supplied projection marker.
		// 托管 Responses、原生 OpenRouter 与 Google AI Studio 的适配器无条件持有无思考投影，
		// 因此不需要用户提供投影标记。
		return nil
	}
	modelParams := route.ModelParams[strings.TrimSpace(route.Model)]
	if hasLLMDisabledReasoningProjection(route.Params) || hasLLMDisabledReasoningProjection(modelParams) {
		return nil
	}
	return fmt.Errorf("%s must declare a disabled-reasoning projection in params or model_params using reasoning_effort, reasoning.effort, reasoning.enabled, thinking.type, or enable_thinking", label)
}

// hasLLMDisabledReasoningProjection reports whether one parameter map declares a request field that the OpenAI-compatible adapter forcibly projects to its disabled value.
// hasLLMDisabledReasoningProjection 用于判断参数映射是否声明了一个会被 OpenAI 兼容适配器强制投影为关闭值的请求字段。
//
// Parameters:
// 参数：
//   - params: route-level or exact-model provider parameter map.
//   - params：路由级或精确模型级供应商参数映射。
//
// Returns:
// 返回值：
//   - bool: true only when one normalized key belongs to the closed no-thinking projection vocabulary.
//   - bool：仅当一个规范化键属于封闭无思考投影词汇时返回 true。
func hasLLMDisabledReasoningProjection(params map[string]any) bool {
	for rawKey, rawValue := range params {
		switch strings.ToLower(strings.TrimSpace(rawKey)) {
		case "reasoning_effort", "enable_thinking":
			return true
		case "reasoning":
			if hasExactReasoningObjectDialect(rawValue) {
				return true
			}
		case "thinking":
			if providerHintObjectHasKey(rawValue, "type") {
				return true
			}
		}
	}
	return false
}

// hasExactReasoningObjectDialect reports whether one reasoning object declares exactly one supported disable control.
// hasExactReasoningObjectDialect 用于判断 reasoning 对象是否只声明了一个受支持的关闭控制字段。
//
// Parameters:
// 参数：
//   - value: configured provider hint expected to be a string-keyed object.
//   - value：预期为字符串键对象的 Provider hint 配置值。
//
// Returns:
// 返回值：
//   - bool: true only for an object containing effort or enabled, but not both.
//   - bool：仅当对象包含 effort 或 enabled 之一且不同时包含两者时返回 true。
func hasExactReasoningObjectDialect(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	hasEffort := false
	hasEnabled := false
	for rawKey := range object {
		switch strings.ToLower(strings.TrimSpace(rawKey)) {
		case "effort":
			hasEffort = true
		case "enabled":
			hasEnabled = true
		}
	}
	return hasEffort != hasEnabled
}

// providerHintObjectHasKey reports whether one string-keyed hint object contains a normalized field.
// providerHintObjectHasKey 用于判断字符串键 hint 对象是否包含一个规范化字段。
//
// Parameters:
// 参数：
//   - value: configured provider hint object.
//   - value：配置的 Provider hint 对象。
//   - expectedKey: normalized field that declares the wire dialect.
//   - expectedKey：用于声明线路方言的规范化字段。
//
// Returns:
// 返回值：
//   - bool: true only when the object contains that field after case folding and trimming.
//   - bool：仅当对象在大小写折叠与去空格后包含该字段时返回 true。
func providerHintObjectHasKey(value any, expectedKey string) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for rawKey := range object {
		if strings.EqualFold(strings.TrimSpace(rawKey), expectedKey) {
			return true
		}
	}
	return false
}

// validateLLMRouteWeightConfig rejects negative per-scene route weights so runtime ordering never has to reason about malformed slots.
// validateLLMRouteWeightConfig 用于拒绝负数的分场景路由权重，避免运行时排序阶段处理格式错误的槽位。
func validateLLMRouteWeightConfig(label string, weights LLMRouteWeightConfig) error {
	if weights.PreCheckL1 != nil && *weights.PreCheckL1 < 0 {
		return fmt.Errorf("%s.weights.precheck_l1 must be >= 0", label)
	}
	if weights.PreCheckL2 != nil && *weights.PreCheckL2 < 0 {
		return fmt.Errorf("%s.weights.precheck_l2 must be >= 0", label)
	}
	if weights.PostActionL1 != nil && *weights.PostActionL1 < 0 {
		return fmt.Errorf("%s.weights.postaction_l1 must be >= 0", label)
	}
	if weights.PostActionL2 != nil && *weights.PostActionL2 < 0 {
		return fmt.Errorf("%s.weights.postaction_l2 must be >= 0", label)
	}
	if weights.ProfileInstruction != nil && *weights.ProfileInstruction < 0 {
		return fmt.Errorf("%s.weights.profile_instruction must be >= 0", label)
	}
	if weights.Reserve != nil && *weights.Reserve < 0 {
		return fmt.Errorf("%s.weights.reserve must be >= 0", label)
	}
	return nil
}

// validateRerankRouteConfigs verifies each explicit rerank route stays self-contained so route failover can swap providers or models without guessing missing runtime fields.
// validateRerankRouteConfigs 用于校验每条显式 rerank 路由都保持自包含，确保路由容灾在切 provider 或 model 时不必猜测缺失的运行时字段。
func validateRerankRouteConfigs(routes []RerankRouteConfig) error {
	if len(routes) == 0 {
		return errors.New("rerank.routes must contain at least one route when declared")
	}
	for idx, route := range routes {
		label := fmt.Sprintf("rerank.routes[%d]", idx)
		switch normalizeRerankProviderValue(route.Provider) {
		case "dashscope", "siliconflow", "openrouter":
		default:
			return fmt.Errorf("%s.provider must be one of dashscope, siliconflow, openrouter", label)
		}
		if strings.TrimSpace(route.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required", label)
		}
		if strings.TrimSpace(route.Model) == "" {
			return fmt.Errorf("%s.model is required", label)
		}
		if route.Priority < 0 {
			return fmt.Errorf("%s.priority must be >= 0", label)
		}
		if len(route.Nodes) == 0 {
			return fmt.Errorf("%s.api_keys or %s.nodes is required", label, label)
		}
		if err := validateAIRoutingNodes(label, route.Nodes); err != nil {
			return err
		}
		if route.Timeout.Duration <= 0 {
			return fmt.Errorf("%s.timeout must be > 0", label)
		}
	}
	return nil
}

// normalizeRerankProviderValue canonicalizes rerank provider aliases so config defaults, validation, and runtime wiring all compare one stable token.
// normalizeRerankProviderValue 用于规范化 rerank provider 别名，让配置默认值、校验和运行时装配始终比较同一份稳定 token。
func normalizeRerankProviderValue(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "dashscope":
		return "dashscope"
	case "siliconflow":
		return "siliconflow"
	case "openrouter":
		return "openrouter"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

// rerankProviderDefaultEndpoint returns the provider-specific rerank endpoint used when one explicit route omits an override.
// rerankProviderDefaultEndpoint 用于返回 provider 专属的 rerank 默认地址，在显式路由未覆盖 endpoint 时补齐。
func rerankProviderDefaultEndpoint(provider string) string {
	switch normalizeRerankProviderValue(provider) {
	case "siliconflow":
		return defaultSiliconFlowRerankEndpoint
	case "openrouter":
		return defaultOpenRouterRerankEndpoint
	default:
		return defaultDashScopeRerankEndpoint
	}
}

// rerankProviderDefaultModel returns the provider-specific rerank model used when one explicit route omits an override.
// rerankProviderDefaultModel 用于返回 provider 专属的 rerank 默认模型，在显式路由未覆盖 model 时补齐。
func rerankProviderDefaultModel(provider string) string {
	switch normalizeRerankProviderValue(provider) {
	case "siliconflow":
		return defaultSiliconFlowRerankModel
	case "openrouter":
		return defaultOpenRouterRerankModel
	default:
		return defaultDashScopeRerankModel
	}
}

// normalizeKeyFailoverConfig fills safe in-memory cooldown defaults for one fixed-model API-key pool.
// normalizeKeyFailoverConfig 用于为单个固定模型的 API Key 池补齐安全的内存态冷却默认值。
func normalizeKeyFailoverConfig(cfg *KeyFailoverConfig) {
	if cfg == nil {
		return
	}
	defaults := defaultKeyFailoverConfig()
	cfg.Policy = normalizeKeyFailoverPolicyValue(cfg.Policy)
	if cfg.RateLimitCooldown.Duration <= 0 {
		cfg.RateLimitCooldown = defaults.RateLimitCooldown
	}
	if cfg.QuotaCooldown.Duration <= 0 {
		cfg.QuotaCooldown = defaults.QuotaCooldown
	}
	if cfg.AuthCooldown.Duration <= 0 {
		cfg.AuthCooldown = defaults.AuthCooldown
	}
}

// normalizePreCheckSearchScopeValue canonicalizes the pre-check search-scope enum so config defaults, env overrides, and validation all compare the same token.
// normalizePreCheckSearchScopeValue 用于规范化 pre-check 检索作用域枚举，让配置默认值、环境变量覆盖和校验始终比较同一个 token。
