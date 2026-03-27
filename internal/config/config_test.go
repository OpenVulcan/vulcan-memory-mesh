// config_test.go implements configuration and prompt loading.
// config_test.go 用于实现配置与提示词加载。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp verifies the TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp behavior.
// TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp 用于验证 TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp 行为。
func TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.PreCheck.SimilarityThreshold = 0.82
	cfg.GRPC.MaxReceiveMessageBytes = 0
	cfg.Normalize()
	if cfg.MemoryPipeline.MaxSearchKeywords != 5 {
		t.Fatalf("max search keywords = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
	if cfg.GRPC.MaxReceiveMessageBytes != 1<<20 {
		t.Fatalf("max receive message bytes = %d", cfg.GRPC.MaxReceiveMessageBytes)
	}
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0.82 {
		t.Fatalf("min similarity = %v", cfg.MemoryPipeline.MinSimilarityScore)
	}

	cfg.MemoryPipeline.MinSimilarityScore = float64Ptr(0)
	cfg.Normalize()
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0 {
		t.Fatalf("explicit zero min similarity = %v", cfg.MemoryPipeline.MinSimilarityScore)
	}

	cfg.MemoryPipeline.MaxSearchKeywords = 99
	cfg.Normalize()
	if cfg.MemoryPipeline.MaxSearchKeywords != 10 {
		t.Fatalf("max search keywords after clamp = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
}

// TestConfigNormalizeDefaultsPostActionInputMode verifies the TestConfigNormalizeDefaultsPostActionInputMode behavior.
// TestConfigNormalizeDefaultsPostActionInputMode 用于验证 TestConfigNormalizeDefaultsPostActionInputMode 行为。
func TestConfigNormalizeDefaultsPostActionInputMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.InputMode = ""
	cfg.Noise.DefaultLanguage = ""
	cfg.Noise.SemanticThreshold = 0
	cfg.Vector.Provider = ""
	cfg.Relational.Provider = ""
	cfg.DockDB.Address = ""
	cfg.LanceDB.Address = ""
	cfg.Normalize()
	if cfg.PostAction.InputMode != "compat" {
		t.Fatalf("post action input mode = %q", cfg.PostAction.InputMode)
	}
	if cfg.Noise.DefaultLanguage != cfg.PII.DefaultLanguage {
		t.Fatalf("noise default language = %q", cfg.Noise.DefaultLanguage)
	}
	if cfg.Noise.SemanticThreshold != 0.88 {
		t.Fatalf("noise semantic threshold = %v", cfg.Noise.SemanticThreshold)
	}
	if cfg.Vector.Provider != "lancedb" {
		t.Fatalf("vector provider = %q", cfg.Vector.Provider)
	}
	if cfg.Relational.Provider != "dockdb" {
		t.Fatalf("relational provider = %q", cfg.Relational.Provider)
	}
	if cfg.DockDB.Address != "127.0.0.1:50052" {
		t.Fatalf("dockdb address = %q", cfg.DockDB.Address)
	}
	if cfg.LanceDB.Address != "127.0.0.1:50051" {
		t.Fatalf("lancedb address = %q", cfg.LanceDB.Address)
	}
	if cfg.GRPC.RequestTimeout.Chat.Duration != 5*time.Second {
		t.Fatalf("grpc chat timeout = %v", cfg.GRPC.RequestTimeout.Chat.Duration)
	}
	if cfg.GRPC.RequestTimeout.PreCheck.Duration != 8*time.Second {
		t.Fatalf("grpc precheck timeout = %v", cfg.GRPC.RequestTimeout.PreCheck.Duration)
	}
	if cfg.GRPC.RequestTimeout.PostAction.Duration != 8*time.Second {
		t.Fatalf("grpc post action timeout = %v", cfg.GRPC.RequestTimeout.PostAction.Duration)
	}
	if cfg.GRPC.RequestTimeout.SeedMemory.Duration != 15*time.Second {
		t.Fatalf("grpc seed memory timeout = %v", cfg.GRPC.RequestTimeout.SeedMemory.Duration)
	}
	if cfg.PreCheck.IntentTimeout.Duration != 5*time.Second {
		t.Fatalf("precheck intent timeout = %v", cfg.PreCheck.IntentTimeout.Duration)
	}
}

// TestConfigValidateRejectsUnknownPostActionMode verifies the TestConfigValidateRejectsUnknownPostActionMode behavior.
// TestConfigValidateRejectsUnknownPostActionMode 用于验证 TestConfigValidateRejectsUnknownPostActionMode 行为。
func TestConfigValidateRejectsUnknownPostActionMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.InputMode = "broken"
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.input_mode must be either strict or compat" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsNoiseThresholdOutsideRange verifies the TestConfigValidateRejectsNoiseThresholdOutsideRange behavior.
// TestConfigValidateRejectsNoiseThresholdOutsideRange 用于验证 TestConfigValidateRejectsNoiseThresholdOutsideRange 行为。
func TestConfigValidateRejectsNoiseThresholdOutsideRange(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Noise.SemanticThreshold = 1.5
	if err := cfg.Validate(); err == nil || err.Error() != "noise.semantic_threshold must be in [0,1]" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsPreCheckMethodTimeoutNotGreaterThanIntentTimeout verifies the request budget leaves room for outer orchestration beyond intent extraction.
// TestConfigValidateRejectsPreCheckMethodTimeoutNotGreaterThanIntentTimeout 用于验证 pre-check 方法级预算必须大于内部意图提取预算。
func TestConfigValidateRejectsPreCheckMethodTimeoutNotGreaterThanIntentTimeout(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.RequestTimeout.PreCheck = Duration{5 * time.Second}
	cfg.PreCheck.IntentTimeout = Duration{5 * time.Second}
	if err := cfg.Validate(); err == nil || err.Error() != "grpc.request_timeout.pre_check must be greater than pre_check.intent_timeout" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsMemoryVectorProvider verifies vector storage no longer allows the removed in-memory fallback.
// TestConfigValidateRejectsMemoryVectorProvider 用于验证向量存储已经不再允许被移除的内存回退实现。
func TestConfigValidateRejectsMemoryVectorProvider(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Vector.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "vector.provider must be lancedb" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsMemoryRelationalProvider verifies relational storage now only accepts the DockDB backend.
// TestConfigValidateRejectsMemoryRelationalProvider 用于验证关系存储现在只接受 DockDB 后端。
func TestConfigValidateRejectsMemoryRelationalProvider(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Relational.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "relational.provider must be dockdb" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestLoadExpandsEnvPlaceholdersFromDotEnv verifies the TestLoadExpandsEnvPlaceholdersFromDotEnv behavior.
// TestLoadExpandsEnvPlaceholdersFromDotEnv 用于验证 TestLoadExpandsEnvPlaceholdersFromDotEnv 行为。
func TestLoadExpandsEnvPlaceholdersFromDotEnv(t *testing.T) {
	const key = "TEST_CONFIG_API_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=from-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	configBody := `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"8s","post_action":"8s","seed_memory":"15s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"test-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"dockdb"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75},
		"admin":{"seed_enabled":true}
	}`
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "from-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadIgnoresMissingDotEnvAndUsesProcessEnv verifies the TestLoadIgnoresMissingDotEnvAndUsesProcessEnv behavior.
// TestLoadIgnoresMissingDotEnvAndUsesProcessEnv 用于验证 TestLoadIgnoresMissingDotEnvAndUsesProcessEnv 行为。
func TestLoadIgnoresMissingDotEnvAndUsesProcessEnv(t *testing.T) {
	const key = "TEST_CONFIG_ENV_ONLY"
	t.Setenv(key, "from-process-env")

	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configBody := `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"8s","post_action":"8s","seed_memory":"15s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"test-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"dockdb"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75},
		"admin":{"seed_enabled":true}
	}`
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.APIKey != "from-process-env" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadPathsMergesSystemAndOverrideConfigsWithOverridePriority verifies the TestLoadPathsMergesSystemAndOverrideConfigsWithOverridePriority behavior.
// TestLoadPathsMergesSystemAndOverrideConfigsWithOverridePriority 用于验证 TestLoadPathsMergesSystemAndOverrideConfigsWithOverridePriority 行为。
func TestLoadPathsMergesSystemAndOverrideConfigsWithOverridePriority(t *testing.T) {
	const key = "TEST_LOAD_PATHS_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	systemConfigDir := filepath.Join(rootDir, "configs")
	userConfigDir := filepath.Join(rootDir, "user")
	if err := os.MkdirAll(systemConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=system-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userConfigDir, ".env"), []byte(key+"=user-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	systemConfig := filepath.Join(systemConfigDir, "local.json")
	userConfig := filepath.Join(userConfigDir, "local.json")
	if err := os.WriteFile(systemConfig, []byte(`{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"8s","post_action":"8s","seed_memory":"15s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"system-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"dockdb"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75},
		"admin":{"seed_enabled":false}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfig, []byte(`{
		"llm":{"model":"user-model"},
		"admin":{"seed_enabled":true}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{systemConfig, userConfig}, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "user-model" {
		t.Fatalf("llm model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "user-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
	if !cfg.Admin.SeedEnabled {
		t.Fatal("expected admin.seed_enabled override to be true")
	}
}

// TestLoadPathsUsesExplicitConfigDirectoryDotEnvAsOverride verifies the TestLoadPathsUsesExplicitConfigDirectoryDotEnvAsOverride behavior.
// TestLoadPathsUsesExplicitConfigDirectoryDotEnvAsOverride 用于验证 TestLoadPathsUsesExplicitConfigDirectoryDotEnvAsOverride 行为。
func TestLoadPathsUsesExplicitConfigDirectoryDotEnvAsOverride(t *testing.T) {
	const key = "TEST_LOAD_PATHS_EXPLICIT_FILE_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	systemConfigDir := filepath.Join(rootDir, "configs")
	overrideDir := filepath.Join(rootDir, "bundle")
	if err := os.MkdirAll(systemConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=system-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, ".env"), []byte(key+"=bundle-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	systemConfig := filepath.Join(systemConfigDir, "local.json")
	overrideConfig := filepath.Join(overrideDir, "local.json")
	if err := os.WriteFile(systemConfig, []byte(`{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"8s","post_action":"8s","seed_memory":"15s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"system-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"dockdb"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75},
		"admin":{"seed_enabled":false}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideConfig, []byte(`{
		"llm":{"model":"bundle-model"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{systemConfig, overrideConfig}, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "bundle-model" {
		t.Fatalf("llm model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "bundle-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadExpandsModelSpecificProviderParams verifies provider parameter maps can be loaded from config placeholders.
// TestLoadExpandsModelSpecificProviderParams 用于验证 provider 参数映射可以从配置占位符中正确加载。
func TestLoadExpandsModelSpecificProviderParams(t *testing.T) {
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
	configBody := `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"8s","post_action":"8s","seed_memory":"15s"},"shutdown_timeout":"10s"},
		"llm":{
			"provider":"openai",
			"endpoint":"https://api.openai.com/v1",
			"api_key":"${` + apiKey + `}",
			"model":"${` + modelKey + `}",
			"params":{"reasoning_effort":"low"},
			"model_params":{"${` + modelKey + `}":{"enable_thinking":false}}
		},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + apiKey + `}","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"dockdb"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75},
		"admin":{"seed_enabled":true}
	}`
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Params["reasoning_effort"] != "low" {
		t.Fatalf("llm.params.reasoning_effort = %#v", cfg.LLM.Params["reasoning_effort"])
	}
	modelParams, ok := cfg.LLM.ModelParams["qwen3.5-flash"]
	if !ok {
		t.Fatalf("expected model params for qwen3.5-flash, got %#v", cfg.LLM.ModelParams)
	}
	if enabled, ok := modelParams["enable_thinking"].(bool); !ok || enabled {
		t.Fatalf("llm.model_params.enable_thinking = %#v", modelParams["enable_thinking"])
	}
}

// restoreEnv executes the restoreEnv logic.
// restoreEnv 用于执行 restoreEnv 逻辑。
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

// newValidConfigForTest returns one minimal fully valid real-model config so validation-focused tests fail on the field under test only.
// newValidConfigForTest 用于返回一份最小且完整的真实模型配置，让聚焦校验测试只因为目标字段而失败。
func newValidConfigForTest() Config {
	cfg := DefaultLocal()
	cfg.GRPC.ListenAddr = "127.0.0.1:8080"
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.Embedding.Dimension = 1024
	return cfg
}
