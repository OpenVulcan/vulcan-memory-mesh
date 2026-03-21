// config_test.go implements configuration and prompt loading.
// config_test.go 用于实现配置与提示词加载。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp verifies the TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp behavior.
// TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp 用于验证 TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp 行为。
func TestConfigNormalizeAppliesMemoryPipelineDefaultsAndClamp(t *testing.T) {
	cfg := DefaultLocal()
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.PreCheck.SimilarityThreshold = 0.82
	cfg.HTTP.MaxRequestBodyBytes = 0
	cfg.Normalize()
	if cfg.MemoryPipeline.MaxSearchKeywords != 5 {
		t.Fatalf("max search keywords = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
	if cfg.HTTP.MaxRequestBodyBytes != 1<<20 {
		t.Fatalf("max request body bytes = %d", cfg.HTTP.MaxRequestBodyBytes)
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

// TestConfigValidateRequiresTLSFiles verifies the TestConfigValidateRequiresTLSFiles behavior.
// TestConfigValidateRequiresTLSFiles 用于验证 TestConfigValidateRequiresTLSFiles 行为。
func TestConfigValidateRequiresTLSFiles(t *testing.T) {
	cfg := DefaultLocal()
	cfg.HTTP.TLS.Enabled = true
	cfg.HTTP.TLS.CertFile = ""
	cfg.HTTP.TLS.KeyFile = "server.key"
	if err := cfg.Validate(); err == nil || err.Error() != "http.tls.cert_file is required when tls is enabled" {
		t.Fatalf("unexpected validate error: %v", err)
	}

	cfg.HTTP.TLS.CertFile = "server.crt"
	cfg.HTTP.TLS.KeyFile = ""
	if err := cfg.Validate(); err == nil || err.Error() != "http.tls.key_file is required when tls is enabled" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigNormalizeDefaultsPostActionInputMode verifies the TestConfigNormalizeDefaultsPostActionInputMode behavior.
// TestConfigNormalizeDefaultsPostActionInputMode 用于验证 TestConfigNormalizeDefaultsPostActionInputMode 行为。
func TestConfigNormalizeDefaultsPostActionInputMode(t *testing.T) {
	cfg := DefaultLocal()
	cfg.PostAction.InputMode = ""
	cfg.Noise.DefaultLanguage = ""
	cfg.Noise.SemanticThreshold = 0
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
}

// TestConfigValidateRejectsUnknownPostActionMode verifies the TestConfigValidateRejectsUnknownPostActionMode behavior.
// TestConfigValidateRejectsUnknownPostActionMode 用于验证 TestConfigValidateRejectsUnknownPostActionMode 行为。
func TestConfigValidateRejectsUnknownPostActionMode(t *testing.T) {
	cfg := DefaultLocal()
	cfg.PostAction.InputMode = "broken"
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.input_mode must be either strict or compat" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsNoiseThresholdOutsideRange verifies the TestConfigValidateRejectsNoiseThresholdOutsideRange behavior.
// TestConfigValidateRejectsNoiseThresholdOutsideRange 用于验证 TestConfigValidateRejectsNoiseThresholdOutsideRange 行为。
func TestConfigValidateRejectsNoiseThresholdOutsideRange(t *testing.T) {
	cfg := DefaultLocal()
	cfg.Noise.SemanticThreshold = 1.5
	if err := cfg.Validate(); err == nil || err.Error() != "noise.semantic_threshold must be in [0,1]" {
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
		"http":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"3s","post_action":"3s","seed_memory":"3s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"gpt-4.1-mini"},
		"embedding":{"provider":"mock","model":"mock-embedding-v1","dimension":64},
		"vector":{"provider":"memory"},
		"relational":{"provider":"memory"},
		"precheck":{"intent_timeout":"2s","top_k":5},
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
		"http":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"3s","post_action":"3s","seed_memory":"3s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${` + key + `}","model":"gpt-4.1-mini"},
		"embedding":{"provider":"mock","model":"mock-embedding-v1","dimension":64},
		"vector":{"provider":"memory"},
		"relational":{"provider":"memory"},
		"precheck":{"intent_timeout":"2s","top_k":5},
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
		"http":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"3s","post_action":"3s","seed_memory":"3s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"system-model"},
		"embedding":{"provider":"mock","model":"mock-embedding-v1","dimension":64},
		"vector":{"provider":"memory"},
		"relational":{"provider":"memory"},
		"precheck":{"intent_timeout":"2s","top_k":5},
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
		"http":{"listen_addr":"127.0.0.1:8080","request_timeout":{"pre_check":"3s","post_action":"3s","seed_memory":"3s"},"shutdown_timeout":"10s"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"${`+key+`}","model":"system-model"},
		"embedding":{"provider":"mock","model":"mock-embedding-v1","dimension":64},
		"vector":{"provider":"memory"},
		"relational":{"provider":"memory"},
		"precheck":{"intent_timeout":"2s","top_k":5},
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
