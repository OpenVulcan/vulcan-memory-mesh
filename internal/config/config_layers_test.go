// Behavioral tests grouped by config layers test.
// 配置层文件合并行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLoadShippedDeepSeekTestConfig verifies the checked-in base/config pair keeps base generic while config.yaml supplies the DeepSeek test override and local deployment settings.
// TestLoadShippedDeepSeekTestConfig 用于验证仓库内置 base/config 组合会保持 base 通用，并由 config.yaml 提供 DeepSeek 测试覆盖与本地部署设置。
func TestLoadShippedDeepSeekTestConfig(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "test-deepseek-key")
	t.Setenv("BAILIAN_API_KEY", "test-bailian-key")
	t.Setenv("BAILIAN_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1")
	t.Setenv("BAILIAN_RERANK_URL", defaultDashScopeRerankEndpoint)
	t.Setenv("VMM_POSTGRES_DSN", "postgres://postgres:postgres@127.0.0.1:5432/vmm?sslmode=disable")

	configDir := filepath.Join("..", "..", "configs")
	cfg, err := LoadPaths([]string{
		filepath.Join(configDir, "base.yaml"),
		filepath.Join(configDir, "config.yaml"),
	}, Config{})
	if err != nil {
		t.Fatalf("load shipped deepseek base config: %v", err)
	}
	if got, want := cfg.GRPC.ListenAddr, "127.0.0.1:17625"; got != want {
		t.Fatalf("grpc listen addr = %q, want %q", got, want)
	}
	if got, want := cfg.Storage.Mode, "combined"; got != want {
		t.Fatalf("storage mode = %q, want %q", got, want)
	}
	if got, want := cfg.Prompts.PromptLanguage, "default_cn"; got != want {
		t.Fatalf("prompt language = %q, want %q", got, want)
	}
	if got, want := cfg.MaintenanceTool.VectorRebuildBatchSize, 32; got != want {
		t.Fatalf("vector rebuild batch size = %d, want %d", got, want)
	}
	if got, want := len(cfg.LLM.Routes), 2; got != want {
		t.Fatalf("llm route count = %d, want %d", got, want)
	}
	for idx, route := range cfg.LLM.Routes {
		if got, want := route.Provider, "openai"; got != want {
			t.Fatalf("llm route %d provider = %q, want %q", idx, got, want)
		}
		if got, want := route.Endpoint, "https://api.deepseek.com/v1"; got != want {
			t.Fatalf("llm route %d endpoint = %q, want %q", idx, got, want)
		}
		if got, want := route.APIKeys, []string{"test-deepseek-key"}; !slices.Equal(got, want) {
			t.Fatalf("llm route %d api keys = %#v, want %#v", idx, got, want)
		}
	}
	if got, want := cfg.LLM.Routes[0].Model, "deepseek-v4-pro"; got != want {
		t.Fatalf("primary deepseek model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.Routes[1].Model, "deepseek-v4-flash"; got != want {
		t.Fatalf("deepseek flash model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.PrimaryModelForSelection("postaction_l2"), "deepseek-v4-pro"; got != want {
		t.Fatalf("postaction l2 model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.PrimaryModelForSelection("precheck_l2"), "deepseek-v4-flash"; got != want {
		t.Fatalf("precheck l2 model = %q, want %q", got, want)
	}
	if got, want := cfg.LLM.PrimaryModel(), "deepseek-v4-flash"; got != want {
		t.Fatalf("reserve model = %q, want %q", got, want)
	}
	providerPrefs, ok := cfg.LLM.Routes[0].Params["provider"].(map[string]any)
	if !ok {
		t.Fatalf("deepseek provider preferences = %#v", cfg.LLM.Routes[0].Params["provider"])
	}
	allowFallbacks, ok := providerPrefs["allow_fallbacks"].(bool)
	if !ok || !allowFallbacks {
		t.Fatalf("deepseek provider allow_fallbacks = %#v", providerPrefs["allow_fallbacks"])
	}
	if got, want := cfg.Embedding.Model, "text-embedding-v4"; got != want {
		t.Fatalf("embedding model = %q, want %q", got, want)
	}
	if got, want := cfg.Embedding.APIKeys, []string{"test-bailian-key"}; !slices.Equal(got, want) {
		t.Fatalf("embedding api keys = %#v, want %#v", got, want)
	}
	if !cfg.Rerank.Enabled {
		t.Fatal("expected rerank to stay enabled")
	}
	if got, want := len(cfg.Rerank.Routes), 3; got != want {
		t.Fatalf("rerank route count = %d, want %d", got, want)
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

// TestLoadPathsRejectsRemovedRouteCompatibilityFields verifies removed compatibility fields are rejected for embedding, llm routes, and nested route nodes.
// TestLoadPathsRejectsRemovedRouteCompatibilityFields 用于验证 embedding、llm route 与嵌套路由节点中的已移除兼容字段都会被拒绝。
func TestLoadPathsRejectsRemovedRouteCompatibilityFields(t *testing.T) {
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
			name: "llm-route-priority",
			body: `{"llm":{"routes":[{"provider":"openai","endpoint":"https://example.com/v1","api_keys":["key-a"],"model":"model-a","priority":100}]}}`,
			want: "llm.routes[0].priority has been removed",
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
				t.Fatalf("unexpected removed route compatibility error: %v", err)
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

// TestLoadPathsOverridesPromptLanguageFromLayeredYAML verifies higher-priority config layers may replace the selected prompt bundle explicitly.
// TestLoadPathsOverridesPromptLanguageFromLayeredYAML 用于验证高优先级配置层可以显式覆盖选中的提示词包。
func TestLoadPathsOverridesPromptLanguageFromLayeredYAML(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	basePath := filepath.Join(rootDir, "base.yaml")
	overridePath := filepath.Join(rootDir, "config.yaml")

	baseBody := `grpc:
  listen_addr: "127.0.0.1:8080"
prompts:
  prompt_language: "default_en"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["base-key"]
      model: "base-model"
      params:
        reasoning_effort: "none"
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
post_action:
  input_mode: "strict"
  session_analysis_turn_threshold: 2
  session_analysis_token_threshold: 12000
  session_analysis_idle_timeout: "15m"
  session_analysis_history_turns: 3
  session_analysis_max_input_tokens: 6000
  max_queue_workers: 4
`
	overrideBody := `prompts:
  prompt_language: "default_cn"
`
	if err := os.WriteFile(basePath, []byte(baseBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte(overrideBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{basePath, overridePath}, DefaultBase())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Prompts.PromptLanguage, defaultChinesePromptBundle; got != want {
		t.Fatalf("prompt language = %q, want %q", got, want)
	}
}

// TestLoadRejectsNonPositiveHardDedupePoolTopKFromYAML verifies the standard load chain preserves explicit YAML values for hard_dedupe_pool_top_k so invalid numbers fail validation instead of silently falling back to defaults.
// TestLoadRejectsNonPositiveHardDedupePoolTopKFromYAML 用于验证标准加载链路会保留 YAML 中显式提供的 hard_dedupe_pool_top_k；因此非法数值应直接触发校验错误，而不是静默回退默认值。
func TestLoadRejectsNonPositiveHardDedupePoolTopKFromYAML(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["test-key"]
      model: "test-model"
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
  hard_dedupe_pool_top_k: -1
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), "memory_pipeline.hard_dedupe_pool_top_k must be > 0") {
		t.Fatalf("unexpected hard dedupe pool top k load error: %v", err)
	}
}

// TestLoadRejectsNonPositiveHardDedupePoolTopKFromEnvOverride verifies environment overrides can still trigger hard_dedupe_pool_top_k validation errors when the config explicitly opts into that env key.
// TestLoadRejectsNonPositiveHardDedupePoolTopKFromEnvOverride 用于验证当配置显式引用该环境变量时，环境变量覆盖仍会正确触发 hard_dedupe_pool_top_k 的校验错误。
func TestLoadRejectsNonPositiveHardDedupePoolTopKFromEnvOverride(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K")
	t.Setenv("VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K", "0")

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["test-key"]
      model: "test-model"
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
  hard_dedupe_pool_top_k: ${VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K}
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), "memory_pipeline.hard_dedupe_pool_top_k must be > 0") {
		t.Fatalf("unexpected hard dedupe pool env-override error: %v", err)
	}
}

// TestLoadAppliesMemoryReplaceScopeFromEnvOverride verifies the standard load chain applies the explicit memory replacement scope environment override before normalization and validation.
// TestLoadAppliesMemoryReplaceScopeFromEnvOverride 用于验证标准加载链路会在归一化和校验前应用显式记忆更替作用域环境变量覆盖。
func TestLoadAppliesMemoryReplaceScopeFromEnvOverride(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "VMM_MEMORY_REPLACE_SCOPE")
	t.Setenv("VMM_MEMORY_REPLACE_SCOPE", " TEAM ")

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["test-key"]
      model: "test-model"
      params: {reasoning_effort: none}
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
memory_replace_scope: "${VMM_MEMORY_REPLACE_SCOPE}"
memory_pipeline:
  max_search_keywords: 5
  min_similarity_score: 0.75
  hard_dedupe_pool_top_k: 16
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatalf("load memory replace scope env override: %v", err)
	}
	if got, want := cfg.MemoryReplaceScope, "team"; got != want {
		t.Fatalf("memory replace scope env override = %q, want %q", got, want)
	}
}

// TestLoadRejectsUnknownMemoryPipelineField verifies the memory-pipeline custom unmarshaller preserves strict unknown-field rejection.
// TestLoadRejectsUnknownMemoryPipelineField 用于验证 memory_pipeline 的自定义反序列化仍然保持未知字段拒绝。
func TestLoadRejectsUnknownMemoryPipelineField(t *testing.T) {
	clearRemovedAIEnvVars(t)

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["test-key"]
      model: "test-model"
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
  hard_dedupe_pool_top_k: 16
  unknown_weight: 1
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), `json: unknown field "unknown_weight"`) {
		t.Fatalf("unexpected unknown memory pipeline field error: %v", err)
	}
}

// TestLoadPathsRejectsRemovedPromptRoutes verifies the removed legacy prompt-route map now fails fast instead of being silently ignored.
// TestLoadPathsRejectsRemovedPromptRoutes 用于验证已移除的旧版提示词路由映射现在会快速失败，而不是被静默忽略。
func TestLoadPathsRejectsRemovedPromptRoutes(t *testing.T) {
	clearRemovedAIEnvVars(t)

	rootDir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "legacy-route-map",
			body: `prompts:
  routes:
    "qwen*": "custom-folder"
`,
			want: "prompts.routes has been removed",
		},
		{
			name: "legacy-wildcard-route",
			body: `prompts:
  routes:
    "*": "default"
`,
			want: "prompts.routes has been removed",
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
