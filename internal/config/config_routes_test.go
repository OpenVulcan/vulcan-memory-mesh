// Behavioral tests grouped by config routes test.
// 配置层模型路由行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"strings"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesSiliconFlowRerankDefaults verifies SiliconFlow routes receive provider-specific endpoint/model defaults instead of inheriting DashScope-specific values.
// TestConfigNormalizeAppliesSiliconFlowRerankDefaults 用于验证 SiliconFlow 路由会拿到 provider 专属的 endpoint/model 默认值，而不是继承 DashScope 专属默认值。
func TestConfigNormalizeAppliesSiliconFlowRerankDefaults(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: " siliconflow ",
		APIKeys:  []string{"silicon-key"},
	}}

	cfg.Normalize()

	route := cfg.Rerank.Routes[0]
	if got, want := route.Provider, "siliconflow"; got != want {
		t.Fatalf("rerank provider = %q, want %q", got, want)
	}
	if got, want := route.Endpoint, defaultSiliconFlowRerankEndpoint; got != want {
		t.Fatalf("rerank endpoint = %q, want %q", got, want)
	}
	if got, want := route.Model, defaultSiliconFlowRerankModel; got != want {
		t.Fatalf("rerank model = %q, want %q", got, want)
	}
	if got, want := route.Timeout.Duration, 8*time.Second; got != want {
		t.Fatalf("rerank timeout = %v, want %v", got, want)
	}
}

// TestConfigNormalizeAppliesOpenRouterRerankDefaults verifies OpenRouter rerank routes receive the SDK API root and documentation-aligned model defaults.
// TestConfigNormalizeAppliesOpenRouterRerankDefaults 用于验证 OpenRouter rerank 路由会拿到 SDK API 根地址和与文档一致的模型默认值。
func TestConfigNormalizeAppliesOpenRouterRerankDefaults(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: " openrouter ",
		APIKeys:  []string{"openrouter-key"},
	}}

	cfg.Normalize()

	route := cfg.Rerank.Routes[0]
	if got, want := route.Provider, "openrouter"; got != want {
		t.Fatalf("rerank provider = %q, want %q", got, want)
	}
	if got, want := route.Endpoint, defaultOpenRouterRerankEndpoint; got != want {
		t.Fatalf("rerank endpoint = %q, want %q", got, want)
	}
	if got, want := route.Model, defaultOpenRouterRerankModel; got != want {
		t.Fatalf("rerank model = %q, want %q", got, want)
	}
	if got, want := route.Timeout.Duration, 8*time.Second; got != want {
		t.Fatalf("rerank timeout = %v, want %v", got, want)
	}
}

// TestConfigNormalizeTrimsExplicitAIFields verifies Normalize trims route and embedding strings while preserving the new route-only contract.
// TestConfigNormalizeTrimsExplicitAIFields 用于验证 Normalize 会裁剪 route 与 embedding 字符串字段，同时保持新的 route-only 契约。
func TestConfigNormalizeTrimsExplicitAIFields(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{
		{
			Name:     " primary ",
			Provider: " openai ",
			Endpoint: " https://primary.example/v1 ",
			APIKeys:  []string{" key-a ", "key-b "},
			Model:    " gpt-4.1-mini ",
		},
	}
	cfg.Embedding.Provider = " openai "
	cfg.Embedding.Endpoint = " https://embed.example/v1 "
	cfg.Embedding.APIKeys = []string{" embed-a ", "embed-b "}
	cfg.Embedding.Model = " text-embedding-3-large "
	cfg.Rerank.Routes = []RerankRouteConfig{
		{
			Name:     " backup ",
			Provider: " dashscope ",
			Endpoint: " https://rerank.example/v1 ",
			APIKeys:  []string{" rerank-a "},
			Model:    " custom-rerank ",
			Timeout:  Duration{6 * time.Second},
		},
	}

	cfg.Normalize()

	if route := cfg.LLM.Routes[0]; route.Name != "primary" || route.Provider != "openai" || route.Endpoint != "https://primary.example/v1" || route.Model != "gpt-4.1-mini" {
		t.Fatalf("normalized llm route = %#v", route)
	}
	if cfg.LLM.Routes[0].Nodes[0].APIKeys[0] != "key-a" || cfg.LLM.Routes[0].Nodes[0].APIKeys[1] != "key-b" {
		t.Fatalf("normalized llm node keys = %#v", cfg.LLM.Routes[0].Nodes)
	}
	if cfg.Embedding.Provider != "openai" || cfg.Embedding.Endpoint != "https://embed.example/v1" || cfg.Embedding.Model != "text-embedding-3-large" {
		t.Fatalf("normalized embedding config = %#v", cfg.Embedding)
	}
	if route := cfg.Rerank.Routes[0]; route.Name != "backup" || route.Provider != "dashscope" || route.Endpoint != "https://rerank.example/v1" || route.Model != "custom-rerank" {
		t.Fatalf("normalized rerank route = %#v", route)
	}
}

// TestConfigNormalizeSynthesizesRouteNodesFromAPIKeys verifies one route can still collapse api_keys plus shared budgets into a single synthesized node.
// TestConfigNormalizeSynthesizesRouteNodesFromAPIKeys 用于验证单条 route 仍可把 api_keys 与共享预算折叠成一个合成节点。
func TestConfigNormalizeSynthesizesRouteNodesFromAPIKeys(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{{
		Provider: "openai",
		Endpoint: "https://primary.example/v1",
		APIKeys:  []string{"key-a", "key-b"},
		RPM:      7,
		TPM:      700,
		RPD:      70,
		Model:    "model-a",
	}}

	cfg.Normalize()

	nodes := cfg.LLM.Routes[0].Nodes
	if got, want := len(nodes), 1; got != want {
		t.Fatalf("llm route node count = %d, want %d (%#v)", got, want, nodes)
	}
	if got, want := len(nodes[0].APIKeys), 2; got != want {
		t.Fatalf("llm route node key count = %d, want %d (%#v)", got, want, nodes[0].APIKeys)
	}
	if nodes[0].RPM != 7 || nodes[0].TPM != 700 || nodes[0].RPD != 70 {
		t.Fatalf("llm route node limits = %#v", nodes[0])
	}
}

// TestConfigPrimaryModelForSelectionUsesPerSceneWeights verifies different business tiers can resolve different primary models from the same route table.
// TestConfigPrimaryModelForSelectionUsesPerSceneWeights 用于验证不同业务层级可以在同一份路由表上解析出不同的主模型。
func TestConfigPrimaryModelForSelectionUsesPerSceneWeights(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{
		{
			Provider: "openai",
			Endpoint: "https://precheck.example/v1",
			APIKeys:  []string{"precheck-key"},
			Model:    "precheck-model",
			Weights:  LLMRouteWeightConfig{PreCheckL1: intPtr(180), PostActionL2: intPtr(40), ProfileInstruction: intPtr(50)},
		},
		{
			Provider: "openai",
			Endpoint: "https://postaction.example/v1",
			APIKeys:  []string{"postaction-key"},
			Model:    "postaction-model",
			Weights:  LLMRouteWeightConfig{PreCheckL1: intPtr(60), PostActionL2: intPtr(220), ProfileInstruction: intPtr(70)},
		},
		{
			Provider: "openai",
			Endpoint: "https://profile.example/v1",
			APIKeys:  []string{"profile-key"},
			Model:    "profile-model",
			Weights:  LLMRouteWeightConfig{PreCheckL1: intPtr(50), PostActionL2: intPtr(60), ProfileInstruction: intPtr(240)},
		},
	}

	cfg.Normalize()

	if got, want := cfg.LLM.PrimaryModelForSelection("precheck_l1"), "precheck-model"; got != want {
		t.Fatalf("precheck_l1 primary model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.PrimaryModelForSelection("postaction_l2"), "postaction-model"; got != want {
		t.Fatalf("postaction_l2 primary model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.PrimaryModelForSelection("profile_instruction"), "profile-model"; got != want {
		t.Fatalf("profile_instruction primary model = %q, want %q", got, want)
	}
}

// TestLLMRouteResolvedWeightsDefaultTo100 verifies unspecified per-scene weights always fall back to 100, while explicit overrides replace only their own slots.
// TestLLMRouteResolvedWeightsDefaultTo100 用于验证未声明的分场景权重始终回落到 100，而显式覆盖只替换自己的槽位。
func TestLLMRouteResolvedWeightsDefaultTo100(t *testing.T) {
	route := LLMRouteConfig{}
	weights := route.ResolvedWeights()
	if weights.PreCheckL1 != 100 || weights.PreCheckL2 != 100 || weights.PostActionL1 != 100 || weights.PostActionL2 != 100 || weights.ProfileInstruction != 100 || weights.Reserve != 100 {
		t.Fatalf("default resolved llm route weights = %#v", weights)
	}

	route = LLMRouteConfig{
		Weights: LLMRouteWeightConfig{
			PostActionL2:       intPtr(160),
			ProfileInstruction: intPtr(175),
		},
	}
	weights = route.ResolvedWeights()
	if weights.PreCheckL1 != 100 || weights.PreCheckL2 != 100 || weights.PostActionL1 != 100 || weights.PostActionL2 != 160 || weights.ProfileInstruction != 175 || weights.Reserve != 100 {
		t.Fatalf("resolved llm route weights = %#v", weights)
	}
}

// TestConfigValidateAcceptsExplicitRoutesAndEmbeddingKeys verifies the new AI contract accepts explicit llm/rerank routes and single-provider embedding key pools together.
// TestConfigValidateAcceptsExplicitRoutesAndEmbeddingKeys 用于验证新的 AI 契约能够同时接受显式 llm/rerank routes 与单 provider embedding key 池。
func TestConfigValidateAcceptsExplicitRoutesAndEmbeddingKeys(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config with explicit routes: %v", err)
	}
}

// TestConfigValidateRequiresOpenAIDisabledReasoningProjection verifies standalone generic Chat routes fail closed unless their exact no-thinking wire dialect is declared.
// TestConfigValidateRequiresOpenAIDisabledReasoningProjection 用于验证独立通用 Chat 路由若未声明精确无思考线路方言则采用失败关闭策略。
func TestConfigValidateRequiresOpenAIDisabledReasoningProjection(t *testing.T) {
	// missing removes the only authoritative projection marker from an otherwise valid route.
	// missing 从其他字段均有效的路由中移除唯一权威投影标记。
	missing := newValidConfigForTest()
	missing.LLM.Routes[0].Params = nil
	missing.Normalize()
	err := missing.Validate()
	if err == nil || !strings.Contains(err.Error(), "must declare a disabled-reasoning projection") {
		t.Fatalf("unexpected missing no-thinking projection error: %v", err)
	}

	// modelScoped proves an exact model_params declaration is accepted by the same route validator.
	// modelScoped 证明精确 model_params 声明可被同一路由校验器接受。
	modelScoped := newValidConfigForTest()
	modelScoped.LLM.Routes[0].Params = nil
	modelScoped.LLM.Routes[0].ModelParams = map[string]map[string]any{
		"test-model": {"thinking": map[string]any{"type": "enabled"}},
	}
	modelScoped.Normalize()
	if err := modelScoped.Validate(); err != nil {
		t.Fatalf("validate model-scoped no-thinking projection: %v", err)
	}

	// visibilityOnly proves suppressing returned reasoning text is not accepted as disabling model reasoning.
	// visibilityOnly 证明仅隐藏返回的推理文本不能被视为关闭模型推理。
	visibilityOnly := newValidConfigForTest()
	visibilityOnly.LLM.Routes[0].Params = map[string]any{"include_reasoning": false}
	visibilityOnly.Normalize()
	if err := visibilityOnly.Validate(); err == nil || !strings.Contains(err.Error(), "must declare a disabled-reasoning projection") {
		t.Fatalf("unexpected visibility-only projection result: %v", err)
	}

	// ambiguousReasoning proves a route must select one exact reasoning object dialect.
	// ambiguousReasoning 证明路由必须选择一个精确的 reasoning 对象方言。
	ambiguousReasoning := newValidConfigForTest()
	ambiguousReasoning.LLM.Routes[0].Params = map[string]any{
		"reasoning": map[string]any{"effort": "none", "enabled": false},
	}
	ambiguousReasoning.Normalize()
	if err := ambiguousReasoning.Validate(); err == nil || !strings.Contains(err.Error(), "must declare a disabled-reasoning projection") {
		t.Fatalf("unexpected ambiguous reasoning projection result: %v", err)
	}

	// enabledDialect proves reasoning.enabled is a separately supported exact provider dialect.
	// enabledDialect 证明 reasoning.enabled 是独立受支持的精确 Provider 方言。
	enabledDialect := newValidConfigForTest()
	enabledDialect.LLM.Routes[0].Params = map[string]any{
		"reasoning": map[string]any{"enabled": true},
	}
	enabledDialect.Normalize()
	if err := enabledDialect.Validate(); err != nil {
		t.Fatalf("validate reasoning.enabled no-thinking projection: %v", err)
	}
}

// TestConfigValidateAcceptsSiliconFlowRerankRoute verifies rerank route validation accepts the SiliconFlow provider alongside the existing DashScope provider.
// TestConfigValidateAcceptsSiliconFlowRerankRoute 用于验证 rerank 路由校验在现有 DashScope 之外，也接受 SiliconFlow provider。
func TestConfigValidateAcceptsSiliconFlowRerankRoute(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: "siliconflow",
		Endpoint: defaultSiliconFlowRerankEndpoint,
		APIKeys:  []string{"silicon-key"},
		Model:    defaultSiliconFlowRerankModel,
		Timeout:  Duration{8 * time.Second},
	}}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate siliconflow rerank route: %v", err)
	}
}

// TestConfigValidateAcceptsGoogleAIStudioProviders verifies the native Google AI Studio provider is accepted for LLM routes and embedding, and that its LLM endpoint may be omitted.
// TestConfigValidateAcceptsGoogleAIStudioProviders 用于验证原生 Google AI Studio provider 可用于 LLM 路由与 embedding，并且其 LLM endpoint 可以省略。
func TestConfigValidateAcceptsGoogleAIStudioProviders(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{{
		Provider: "google_ai_studio",
		APIKeys:  []string{"google-llm-key"},
		Model:    "gemini-2.5-flash",
	}}
	cfg.Embedding.Provider = "google_ai_studio"
	cfg.Embedding.Endpoint = ""
	cfg.Embedding.APIKeys = []string{"google-embed-key"}
	cfg.Embedding.Model = "gemini-embedding-001"
	cfg.Embedding.Dimension = 1024

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config with google ai studio: %v", err)
	}
}

// TestConfigValidateAcceptsOpenRouterProviders verifies OpenRouter can be used for LLM routes, embedding, and rerank with SDK-default endpoints.
// TestConfigValidateAcceptsOpenRouterProviders 用于验证 OpenRouter 可在使用 SDK 默认 endpoint 的情况下同时用于 LLM 路由、embedding 与 rerank。
func TestConfigValidateAcceptsOpenRouterProviders(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{{
		Provider: "openrouter",
		APIKeys:  []string{"openrouter-llm-key"},
		Model:    "openai/gpt-4.1-mini",
	}}
	cfg.Embedding.Provider = "openrouter"
	cfg.Embedding.Endpoint = ""
	cfg.Embedding.APIKeys = []string{"openrouter-embed-key"}
	cfg.Embedding.Model = "openai/text-embedding-3-small"
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: "openrouter",
		APIKeys:  []string{"openrouter-rerank-key"},
		Params: map[string]any{
			"provider": map[string]any{
				"only":            []any{"Cohere"},
				"allow_fallbacks": false,
			},
		},
	}}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config with openrouter: %v", err)
	}
}

// TestConfigValidateRejectsInvalidEmbeddingBatchConstraints verifies embedding batching limits reject non-positive batch widths.
// TestConfigValidateRejectsInvalidEmbeddingBatchConstraints 用于验证 embedding 拆批限制会拒绝非正批宽。
func TestConfigValidateRejectsInvalidEmbeddingBatchConstraints(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Embedding.MaxBatchSize = -1
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "embedding.max_batch_size must be > 0" {
		t.Fatalf("unexpected embedding max batch size validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MaintenanceTool.VectorRebuildBatchSize = -1
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "maintenance_tool.vector_rebuild_batch_size must be > 0" {
		t.Fatalf("unexpected maintenance tool vector rebuild batch size validate error: %v", err)
	}
}

// TestConfigValidateRejectsMissingLLMRoutes verifies runtime validation no longer accepts llm top-level single-route fallbacks.
// TestConfigValidateRejectsMissingLLMRoutes 用于验证运行时校验不再接受 llm 顶层单路由回退写法。
func TestConfigValidateRejectsMissingLLMRoutes(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = nil
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "llm.routes must contain at least one route" {
		t.Fatalf("unexpected llm route validate error: %v", err)
	}
}

// TestConfigValidateRejectsNegativeLLMRouteWeight verifies malformed negative per-scene LLM weights are rejected during startup validation.
// TestConfigValidateRejectsNegativeLLMRouteWeight 用于验证格式错误的负数分场景 LLM 权重会在启动校验阶段被拒绝。
func TestConfigValidateRejectsNegativeLLMRouteWeight(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes[0].Weights.ProfileInstruction = intPtr(-1)
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "llm.routes[0].weights.profile_instruction must be >= 0" {
		t.Fatalf("unexpected llm route weight validate error: %v", err)
	}
}

// TestConfigValidateRejectsMissingEmbeddingAPIKeys verifies embedding still requires one usable multi-key pool after normalization.
// TestConfigValidateRejectsMissingEmbeddingAPIKeys 用于验证 embedding 在归一化后仍必须拥有一组可用的多 key 池。
func TestConfigValidateRejectsMissingEmbeddingAPIKeys(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Embedding.APIKeys = nil
	cfg.Embedding.Nodes = nil
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "embedding.api_keys is required" {
		t.Fatalf("unexpected embedding key validate error: %v", err)
	}
}

// TestConfigValidateRejectsRerankWithoutRoutesWhenEnabled verifies rerank cannot be enabled without explicit routes anymore.
// TestConfigValidateRejectsRerankWithoutRoutesWhenEnabled 用于验证 rerank 启用后不能再缺少显式 routes。
func TestConfigValidateRejectsRerankWithoutRoutesWhenEnabled(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = nil
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "rerank.routes must contain at least one route when rerank is enabled" {
		t.Fatalf("unexpected rerank route validate error: %v", err)
	}
}

// TestConfigValidateRejectsRoutingNodeWithoutKeys verifies each routing node still needs at least one key in the new api_keys-only contract.
// TestConfigValidateRejectsRoutingNodeWithoutKeys 用于验证在新的 api_keys-only 契约下，每个轮询节点仍必须至少携带一个 key。
func TestConfigValidateRejectsRoutingNodeWithoutKeys(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Embedding.APIKeys = nil
	cfg.Embedding.Nodes = []AIRoutingNodeConfig{{Name: "broken-node", RPM: 2}}
	cfg.Normalize()
	if err := cfg.Validate(); err == nil || err.Error() != "embedding.nodes[0].api_keys is required" {
		t.Fatalf("unexpected embedding node validate error: %v", err)
	}
}
