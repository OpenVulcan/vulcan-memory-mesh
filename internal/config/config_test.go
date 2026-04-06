// config_test.go verifies the simplified AI config contract where LLM/rerank use explicit routes and embedding stays single-provider with multi-key failover.
// config_test.go 用于验证收敛后的 AI 配置契约：LLM/rerank 只使用显式 routes，embedding 保持单 provider 且支持多 key 容灾。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesCurrentDefaults verifies Normalize still fills the current gRPC, storage, rerank, and route defaults expected by the local runtime.
// TestConfigNormalizeAppliesCurrentDefaults 用于验证 Normalize 仍会补齐当前本地运行时所需的 gRPC、存储、rerank 与路由默认值。
func TestConfigNormalizeAppliesCurrentDefaults(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.MaxReceiveMessageBytes = 0
	cfg.GRPC.RequestTimeout.Workspace = Duration{}
	cfg.GRPC.RequestTimeout.PreCheck = Duration{}
	cfg.GRPC.RequestTimeout.PostAction = Duration{}
	cfg.PreCheck.IntentTimeout = Duration{}
	cfg.PostAction.InputMode = ""
	cfg.PostAction.SessionAnalysisTurnThreshold = 0
	cfg.PostAction.SessionAnalysisTokenThreshold = 0
	cfg.PostAction.SessionAnalysisIdleTimeout = Duration{}
	cfg.PostAction.SessionAnalysisHistoryTurns = 0
	cfg.PostAction.SessionAnalysisMaxInputTokens = 0
	cfg.Vector.Provider = ""
	cfg.Relational.Provider = ""
	cfg.SQLite.Address = ""
	cfg.LanceDB.Address = ""
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.Rerank.TopN = 0
	cfg.Rerank.Routes[0].Timeout = Duration{}
	cfg.Rerank.Routes[0].Endpoint = ""
	cfg.Rerank.Routes[0].Model = ""
	cfg.PreCheck.SimilarityThreshold = 0.82

	cfg.Normalize()

	if cfg.GRPC.MaxReceiveMessageBytes != 1<<20 {
		t.Fatalf("max receive message bytes = %d", cfg.GRPC.MaxReceiveMessageBytes)
	}
	if cfg.GRPC.RequestTimeout.Workspace.Duration != 15*time.Second {
		t.Fatalf("workspace timeout = %v", cfg.GRPC.RequestTimeout.Workspace.Duration)
	}
	if cfg.GRPC.RequestTimeout.PreCheck.Duration != 8*time.Second {
		t.Fatalf("pre-check timeout = %v", cfg.GRPC.RequestTimeout.PreCheck.Duration)
	}
	if cfg.GRPC.RequestTimeout.PostAction.Duration != 8*time.Second {
		t.Fatalf("post-action timeout = %v", cfg.GRPC.RequestTimeout.PostAction.Duration)
	}
	if cfg.PreCheck.IntentTimeout.Duration != 5*time.Second {
		t.Fatalf("pre-check intent timeout = %v", cfg.PreCheck.IntentTimeout.Duration)
	}
	if cfg.Vector.Provider != "lancedb" {
		t.Fatalf("vector provider = %q", cfg.Vector.Provider)
	}
	if cfg.Relational.Provider != "sqlite" {
		t.Fatalf("relational provider = %q", cfg.Relational.Provider)
	}
	if cfg.SQLite.Address != "127.0.0.1:19501" {
		t.Fatalf("sqlite address = %q", cfg.SQLite.Address)
	}
	if cfg.LanceDB.Address != "127.0.0.1:19301" {
		t.Fatalf("lancedb address = %q", cfg.LanceDB.Address)
	}
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0.82 {
		t.Fatalf("min similarity score = %#v", cfg.MemoryPipeline.MinSimilarityScore)
	}
	if cfg.Rerank.TopN != 8 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if got, want := cfg.Rerank.Routes[0].Endpoint, "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"; got != want {
		t.Fatalf("rerank default endpoint = %q, want %q", got, want)
	}
	if got, want := cfg.Rerank.Routes[0].Model, "qwen3-vl-rerank"; got != want {
		t.Fatalf("rerank default model = %q, want %q", got, want)
	}
	if got, want := cfg.Rerank.Routes[0].Timeout.Duration, 8*time.Second; got != want {
		t.Fatalf("rerank default timeout = %v, want %v", got, want)
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

// TestConfigPrimaryModelUsesHighestPriorityRoute verifies helper accessors expose the highest-priority route model for prompt assembly and diagnostics.
// TestConfigPrimaryModelUsesHighestPriorityRoute 用于验证辅助访问器会暴露最高优先级路由的模型名称，供提示词装配与诊断逻辑使用。
func TestConfigPrimaryModelUsesHighestPriorityRoute(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.LLM.Routes = []LLMRouteConfig{
		{Provider: "openai", Endpoint: "https://low.example/v1", APIKeys: []string{"low-key"}, Model: "low", Priority: 10},
		{Provider: "openai", Endpoint: "https://high.example/v1", APIKeys: []string{"high-key"}, Model: "high", Priority: 100},
	}

	cfg.Normalize()

	if got, want := cfg.LLM.PrimaryModel(), "high"; got != want {
		t.Fatalf("primary model = %q, want %q", got, want)
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

// TestLoadPathsRejectsRemovedTopLevelLLMFields verifies loading fails fast when one config layer still uses removed top-level llm runtime fields.
// TestLoadPathsRejectsRemovedTopLevelLLMFields 用于验证当配置层仍使用已移除的顶层 llm 运行时字段时，加载流程会快速失败。
func TestLoadPathsRejectsRemovedTopLevelLLMFields(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"llm":{"provider":"openai"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPaths([]string{configPath}, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), "llm.provider has been removed") {
		t.Fatalf("unexpected llm removed-field error: %v", err)
	}
}

// TestLoadPathsRejectsRemovedTopLevelRerankFields verifies loading fails fast when one config layer still uses removed top-level rerank runtime fields.
// TestLoadPathsRejectsRemovedTopLevelRerankFields 用于验证当配置层仍使用已移除的顶层 rerank 运行时字段时，加载流程会快速失败。
func TestLoadPathsRejectsRemovedTopLevelRerankFields(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"rerank":{"endpoint":"https://rerank.example/v1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPaths([]string{configPath}, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), "rerank.endpoint has been removed") {
		t.Fatalf("unexpected rerank removed-field error: %v", err)
	}
}

// TestLoadPathsRejectsRemovedSingularAPIKeyFields verifies removed singular api_key fields are rejected for embedding, routes, and nodes.
// TestLoadPathsRejectsRemovedSingularAPIKeyFields 用于验证 embedding、routes 与 nodes 中已移除的单值 api_key 字段都会被拒绝。
func TestLoadPathsRejectsRemovedSingularAPIKeyFields(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "embedding",
			body: `{"embedding":{"api_key":"legacy-key"}}`,
			want: "embedding.api_key has been removed",
		},
		{
			name: "llm-route",
			body: `{"llm":{"routes":[{"provider":"openai","endpoint":"https://example.com/v1","api_key":"legacy-key","model":"model-a"}]}}`,
			want: "llm.routes[0].api_key has been removed",
		},
		{
			name: "rerank-node",
			body: `{"rerank":{"enabled":true,"routes":[{"provider":"dashscope","endpoint":"https://rerank.example/v1","model":"rerank-a","timeout":"5s","nodes":[{"api_key":"legacy-key"}]}]}}`,
			want: "rerank.routes[0].nodes[0].api_key has been removed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(rootDir, tc.name+".json")
			if err := os.WriteFile(configPath, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadPaths([]string{configPath}, DefaultLocal())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected removed api_key error: %v", err)
			}
		})
	}
}

// TestLoadExpandsModelSpecificProviderParams verifies model-scoped provider parameter maps still support environment-expanded keys under route mode.
// TestLoadExpandsModelSpecificProviderParams 用于验证在 route 模式下，模型粒度的 provider 参数映射仍支持带环境变量展开的键名。
func TestLoadExpandsModelSpecificProviderParams(t *testing.T) {
	clearRemovedAIEnvVars(t)
	const modelKey = "TEST_MODEL_NAME"
	const apiKey = "TEST_MODEL_PARAMS_API_KEY"
	restoreEnv(t, modelKey)
	restoreEnv(t, apiKey)
	t.Setenv(modelKey, "qwen3.5-flash")
	t.Setenv(apiKey, "test-key")

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
  request_timeout:
    workspace: "15s"
    pre_check: "8s"
    post_action: "8s"
  shutdown_timeout: "10s"
sqlite:
  address: "127.0.0.1:19501"
  timeout: "5s"
lancedb:
  address: "127.0.0.1:19301"
  timeout: "5s"
  table_name: "vmm_memory_vectors"
  vector_column: "vector"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys:
        - "${` + apiKey + `}"
      model: "${` + modelKey + `}"
      params:
        reasoning_effort: "low"
      model_params:
        "${` + modelKey + `}":
          enable_thinking: false
embedding:
  provider: "openai"
  endpoint: "https://api.openai.com/v1"
  api_keys:
    - "${` + apiKey + `}"
  model: "text-embedding-3-large"
  dimension: 1024
vector:
  provider: "lancedb"
relational:
  provider: "sqlite"
pre_check:
  intent_timeout: "5s"
  top_k: 5
memory_pipeline:
  max_search_keywords: 5
  min_similarity_score: 0.75
`
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	route, ok := cfg.LLM.PrimaryRoute()
	if !ok {
		t.Fatal("expected primary llm route")
	}
	if route.Params["reasoning_effort"] != "low" {
		t.Fatalf("llm route params.reasoning_effort = %#v", route.Params["reasoning_effort"])
	}
	modelParams, ok := route.ModelParams["qwen3.5-flash"]
	if !ok || modelParams["enable_thinking"] != false {
		t.Fatalf("expected model params for qwen3.5-flash, got %#v", route.ModelParams)
	}
}

// TestLoadPathsMergesPromptRoutesFromLayeredYAML verifies base/config YAML layers keep prompt-route map merging semantics when user overrides add extra model prefixes.
// TestLoadPathsMergesPromptRoutesFromLayeredYAML 用于验证 base/config YAML 分层加载仍会保留提示词路由 map 的合并语义，让高优先级覆盖可追加新的模型前缀。
func TestLoadPathsMergesPromptRoutesFromLayeredYAML(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	basePath := filepath.Join(rootDir, "base.yaml")
	overridePath := filepath.Join(rootDir, "config.yaml")

	baseBody := `grpc:
  listen_addr: "127.0.0.1:8080"
prompts:
  routes:
    "qwen*": "qwen-base"
    "*": "default"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["base-key"]
      model: "base-model"
embedding:
  provider: "openai"
  endpoint: "https://api.openai.com/v1"
  api_keys: ["embed-key"]
  model: "text-embedding-3-large"
  dimension: 1024
vector:
  provider: "lancedb"
relational:
  provider: "sqlite"
pre_check:
  intent_timeout: "5s"
  top_k: 5
memory_pipeline:
  max_search_keywords: 5
  min_similarity_score: 0.75
`
	overrideBody := `prompts:
  routes:
    "gpt-4.1*": "gpt-4.1-folder"
`
	if err := os.WriteFile(basePath, []byte(baseBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte(overrideBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{basePath, overridePath}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Prompts.Routes["qwen*"], "qwen-base"; got != want {
		t.Fatalf("base prompt route = %q, want %q", got, want)
	}
	if got, want := cfg.Prompts.Routes["gpt-4.1*"], "gpt-4.1-folder"; got != want {
		t.Fatalf("override prompt route = %q, want %q", got, want)
	}
	if got, want := cfg.Prompts.Routes["*"], "default"; got != want {
		t.Fatalf("wildcard prompt route = %q, want %q", got, want)
	}
}

// TestLoadPathsRejectsPromptRoutesWithEmptyEntries verifies prompt-route entries that collapse to empty keys or folders after trimming or env expansion still fail fast instead of being ignored.
// TestLoadPathsRejectsPromptRoutesWithEmptyEntries 用于验证提示词路由项在裁剪或环境变量展开后若退化为空键或空目录，加载流程仍会快速失败，而不是被静默忽略。
func TestLoadPathsRejectsPromptRoutesWithEmptyEntries(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "TEST_EMPTY_PROMPT_ROUTE_KEY")
	restoreEnv(t, "TEST_EMPTY_PROMPT_ROUTE_FOLDER")
	t.Setenv("TEST_EMPTY_PROMPT_ROUTE_KEY", "   ")
	t.Setenv("TEST_EMPTY_PROMPT_ROUTE_FOLDER", "   ")

	rootDir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "empty-key",
			body: `prompts:
  routes:
    "${TEST_EMPTY_PROMPT_ROUTE_KEY}": "custom-folder"
`,
			want: "prompts.routes contains empty model key",
		},
		{
			name: "empty-folder",
			body: `prompts:
  routes:
    "qwen*": "${TEST_EMPTY_PROMPT_ROUTE_FOLDER}"
`,
			want: `prompts.routes["qwen*"] must not be empty`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(rootDir, tc.name+".yaml")
			if err := os.WriteFile(configPath, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadPaths([]string{configPath}, DefaultLocal())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected prompt-route validation error: %v", err)
			}
		})
	}
}

// TestValidateRemovedAIEnvOverrides verifies removed AI env overrides now fail fast instead of silently reintroducing deleted single-route semantics.
// TestValidateRemovedAIEnvOverrides 用于验证已移除的 AI 环境变量现在会快速失败，而不是静默重新引入被删除的单路由语义。
func TestValidateRemovedAIEnvOverrides(t *testing.T) {
	restoreEnv(t, "VMM_LLM_API_KEYS")
	t.Setenv("VMM_LLM_API_KEYS", "legacy-key")
	err := validateRemovedAIEnvOverrides()
	if err == nil || !strings.Contains(err.Error(), "VMM_LLM_API_KEYS has been removed") {
		t.Fatalf("unexpected removed env error: %v", err)
	}
}

// TestApplyEnvOverridesSetsEmbeddingKeyPools verifies supported embedding env overrides still inject multi-key pools and shared node budgets.
// TestApplyEnvOverridesSetsEmbeddingKeyPools 用于验证仍受支持的 embedding 环境变量覆盖会注入多 key 池与共享节点预算。
func TestApplyEnvOverridesSetsEmbeddingKeyPools(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_EMBED_API_KEYS", "embed-a,embed-b")
	t.Setenv("VMM_EMBED_RPM", "7")
	t.Setenv("VMM_EMBED_TPM", "700")
	t.Setenv("VMM_EMBED_RPD", "70")

	applyEnvOverrides(&cfg)
	cfg.Normalize()

	if got, want := len(cfg.Embedding.APIKeys), 2; got != want {
		t.Fatalf("embedding api key pool size = %d, want %d (%#v)", got, want, cfg.Embedding.APIKeys)
	}
	nodes := cfg.Embedding.RoutingNodes()
	if got, want := len(nodes), 1; got != want {
		t.Fatalf("embedding node count = %d, want %d (%#v)", got, want, nodes)
	}
	if nodes[0].RPM != 7 || nodes[0].TPM != 700 || nodes[0].RPD != 70 {
		t.Fatalf("embedding node limits = %#v", nodes[0])
	}
}

// restoreEnv clears one environment variable for the test duration and then restores the prior value.
// restoreEnv 用于在测试期间清空某个环境变量，并在结束后恢复原值。
func restoreEnv(t *testing.T, key string) {
	t.Helper()
	value, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !existed {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, value)
	})
}

// clearRemovedAIEnvVars clears removed AI env overrides so load-path tests only exercise the config file being asserted.
// clearRemovedAIEnvVars 用于清理已移除的 AI 环境变量覆盖，确保分层加载测试只验证目标配置文件本身。
func clearRemovedAIEnvVars(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"VMM_LLM_PROVIDER",
		"VMM_LLM_ENDPOINT",
		"VMM_LLM_API_KEY",
		"VMM_LLM_API_KEYS",
		"VMM_LLM_RPM",
		"VMM_LLM_TPM",
		"VMM_LLM_RPD",
		"VMM_LLM_MODEL",
		"VMM_LLM_ORGANIZATION",
		"VMM_LLM_PROJECT",
		"VMM_LLM_KEY_FAILOVER_ENABLED",
		"VMM_LLM_KEY_FAILOVER_POLICY",
		"VMM_LLM_KEY_FAILOVER_RESPECT_RETRY_AFTER",
		"VMM_LLM_KEY_FAILOVER_RATE_LIMIT_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_QUOTA_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_AUTH_COOLDOWN",
		"VMM_LLM_KEY_FAILOVER_PROBE_AFTER_COOLDOWN",
		"VMM_RERANK_PROVIDER",
		"VMM_RERANK_ENDPOINT",
		"VMM_RERANK_API_KEY",
		"VMM_RERANK_API_KEYS",
		"VMM_RERANK_RPM",
		"VMM_RERANK_TPM",
		"VMM_RERANK_RPD",
		"VMM_RERANK_MODEL",
		"VMM_RERANK_TIMEOUT",
		"VMM_RERANK_KEY_FAILOVER_ENABLED",
		"VMM_RERANK_KEY_FAILOVER_POLICY",
		"VMM_RERANK_KEY_FAILOVER_RESPECT_RETRY_AFTER",
		"VMM_RERANK_KEY_FAILOVER_RATE_LIMIT_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_QUOTA_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_AUTH_COOLDOWN",
		"VMM_RERANK_KEY_FAILOVER_PROBE_AFTER_COOLDOWN",
		"VMM_EMBED_API_KEY",
	} {
		restoreEnv(t, key)
	}
}

// newValidConfigForTest returns one minimal fully valid config so focused validation tests fail only on the target field.
// newValidConfigForTest 用于返回一份最小且完整的有效配置，让聚焦校验测试只在目标字段上失败。
func newValidConfigForTest() Config {
	cfg := DefaultLocal()
	cfg.GRPC.ListenAddr = "127.0.0.1:8080"
	cfg.LLM.Routes = []LLMRouteConfig{{
		Provider: "openai",
		Endpoint: "https://api.openai.com/v1",
		APIKeys:  []string{"test-key"},
		Model:    "test-model",
	}}
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKeys = []string{"test-key"}
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: "dashscope",
		Endpoint: "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
		APIKeys:  []string{"rerank-key"},
		Model:    "qwen3-vl-rerank",
		Timeout:  Duration{8 * time.Second},
	}}
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"
	return cfg
}
