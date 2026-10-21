// Behavioral tests grouped by config env loading test.
// 配置层环境占位符加载行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadRejectsUnknownEnvProbeField verifies unknown config fields cannot be used as environment override opt-in probes.
// TestLoadRejectsUnknownEnvProbeField 用于验证未知配置字段不能作为环境变量覆盖白名单的探针。
func TestLoadRejectsUnknownEnvProbeField(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "VMM_PRE_CHECK_TOPK")
	t.Setenv("VMM_PRE_CHECK_TOPK", "7")

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
env_probe: "${VMM_PRE_CHECK_TOPK}"
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
noise:
  enabled: true
  semantic_threshold: 0.75
pre_check:
  intent_timeout: "5s"
  top_k: 5
memory_pipeline:
  max_search_keywords: 5
  min_similarity_score: 0.75
  hard_dedupe_pool_top_k: 16
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(configPath, DefaultLocal())
	if err == nil || !strings.Contains(err.Error(), `json: unknown field "env_probe"`) {
		t.Fatalf("unexpected unknown env probe field error: %v", err)
	}
}

// TestLoadIgnoresTopLevelExtensionEnvReferences verifies YAML x-* anchor helpers do not participate in environment override opt-in unless merged into real config fields.
// TestLoadIgnoresTopLevelExtensionEnvReferences 用于验证 YAML 顶层 x-* 锚点辅助块不会参与环境变量覆盖白名单，除非其内容被合并进真实配置字段。
func TestLoadIgnoresTopLevelExtensionEnvReferences(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "VMM_PRE_CHECK_TOPK")
	t.Setenv("VMM_PRE_CHECK_TOPK", "7")

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
x-env-probe:
  top_k: "${VMM_PRE_CHECK_TOPK}"
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
		t.Fatalf("load config with top-level extension env reference: %v", err)
	}
	if got, want := cfg.PreCheck.TopK, 5; got != want {
		t.Fatalf("pre-check top_k = %d, want %d", got, want)
	}
}

// TestLoadIgnoresMismatchedFieldEnvReferences verifies one supported VMM override key cannot be enabled by mentioning it in an unrelated real config field.
// TestLoadIgnoresMismatchedFieldEnvReferences 用于验证受支持的 VMM 覆盖键不能通过出现在不相关真实配置字段中而被启用。
func TestLoadIgnoresMismatchedFieldEnvReferences(t *testing.T) {
	clearRemovedAIEnvVars(t)
	restoreEnv(t, "VMM_PRE_CHECK_TOPK")
	t.Setenv("VMM_PRE_CHECK_TOPK", "7")

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
sqlite:
  address: "${VMM_PRE_CHECK_TOPK}"
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
		t.Fatalf("load config with mismatched env reference: %v", err)
	}
	if got, want := cfg.PreCheck.TopK, 5; got != want {
		t.Fatalf("pre-check top_k = %d, want %d", got, want)
	}
}

// TestLoadPathsIgnoresUnreferencedSupportedEnvOverrides verifies literal config values stay authoritative even when matching supported env override names exist in the surrounding .env files.
// TestLoadPathsIgnoresUnreferencedSupportedEnvOverrides 用于验证即使周围 `.env` 文件存在同名支持环境变量，字面量配置值仍然保持权威，不会被静默覆盖。
func TestLoadPathsIgnoresUnreferencedSupportedEnvOverrides(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(configDir, "base.yaml")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("VMM_GRPC_PRE_CHECK_TIMEOUT=8s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseBody := `grpc:
  listen_addr: "127.0.0.1:8080"
  request_timeout:
    pre_check: "15s"
    post_action: "15s"
pre_check:
  intent_timeout: "10s"
  top_k: 5
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
	if err := os.WriteFile(basePath, []byte(baseBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{basePath, configPath}, DefaultBase())
	if err != nil {
		t.Fatalf("expected literal config to ignore .env override, got %v", err)
	}
	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, 15*time.Second; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
	}
	if got, want := cfg.PreCheck.IntentTimeout.Duration, 10*time.Second; got != want {
		t.Fatalf("intent timeout = %v, want %v", got, want)
	}
}

// TestLoadPathsExpandsExplicitEnvPlaceholdersFromDotEnv verifies .env values still participate when the config explicitly uses ${ENV} placeholders instead of literal values.
// TestLoadPathsExpandsExplicitEnvPlaceholdersFromDotEnv 用于验证当配置显式使用 ${ENV} 占位符而不是字面量值时，`.env` 里的变量仍会正常参与展开。
func TestLoadPathsExpandsExplicitEnvPlaceholdersFromDotEnv(t *testing.T) {
	clearRemovedAIEnvVars(t)
	rootDir := t.TempDir()
	configDir := filepath.Join(rootDir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(configDir, "base.yaml")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte(strings.Join([]string{
		"VMM_GRPC_PRE_CHECK_TIMEOUT=15s",
		"VMM_GRPC_POST_ACTION_TIMEOUT=15s",
		"VMM_PRE_CHECK_INTENT_TIMEOUT=10s",
	}, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseBody := `grpc:
  listen_addr: "127.0.0.1:8080"
  request_timeout:
    pre_check: "${VMM_GRPC_PRE_CHECK_TIMEOUT}"
    post_action: "${VMM_GRPC_POST_ACTION_TIMEOUT}"
pre_check:
  intent_timeout: "${VMM_PRE_CHECK_INTENT_TIMEOUT}"
  top_k: 5
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
	if err := os.WriteFile(basePath, []byte(baseBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{basePath, configPath}, DefaultBase())
	if err != nil {
		t.Fatalf("expected explicit env placeholder expansion to succeed, got %v", err)
	}
	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, 15*time.Second; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
	}
	if got, want := cfg.PreCheck.IntentTimeout.Duration, 10*time.Second; got != want {
		t.Fatalf("intent timeout = %v, want %v", got, want)
	}
}

// TestLoadPathsReportsRequiredEnvPlaceholderProblems verifies missing or empty required placeholders fail before API-key normalization can hide the root cause.
// TestLoadPathsReportsRequiredEnvPlaceholderProblems 用于验证缺失或空白的必填占位符会在 API Key 归一化掩盖根因前直接失败。
func TestLoadPathsReportsRequiredEnvPlaceholderProblems(t *testing.T) {
	for _, tc := range []struct {
		name       string
		key        string
		value      string
		setEnv     bool
		wantReason string
	}{
		{
			name:       "missing",
			key:        "VMM_TEST_MISSING_OPENROUTER_KEY",
			wantReason: "is not set",
		},
		{
			name:       "empty",
			key:        "VMM_TEST_EMPTY_OPENROUTER_KEY",
			value:      "  ",
			setEnv:     true,
			wantReason: "is empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearRemovedAIEnvVars(t)
			restoreEnv(t, tc.key)
			if tc.setEnv {
				t.Setenv(tc.key, tc.value)
			}
			rootDir := t.TempDir()
			configPath := filepath.Join(rootDir, "config.yaml")
			configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
  request_timeout:
    pre_check: "15s"
    post_action: "15s"
pre_check:
  intent_timeout: "10s"
  top_k: 5
llm:
  routes:
    - provider: "openai"
      endpoint: "https://api.openai.com/v1"
      api_keys: ["${` + tc.key + `}"]
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
			if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := LoadPaths([]string{configPath}, Config{})
			if err == nil {
				t.Fatal("expected required env placeholder to fail")
			}
			message := err.Error()
			for _, want := range []string{tc.key, "llm.routes[0].api_keys[0]", tc.wantReason} {
				if !strings.Contains(message, want) {
					t.Fatalf("error %q does not contain %q", message, want)
				}
			}
			if strings.Contains(message, "api_keys or") {
				t.Fatalf("expected env-specific error, got generic routing error: %q", message)
			}
		})
	}
}

// TestLoadPathsReportsMergedAnchorEnvPlaceholderPath verifies placeholders declared in x-* YAML anchors still report the real merged field path when they feed required runtime fields.
// TestLoadPathsReportsMergedAnchorEnvPlaceholderPath 用于验证声明在 x-* YAML 锚点中的占位符在合并到必填运行时字段后，仍会报告真实字段路径。
func TestLoadPathsReportsMergedAnchorEnvPlaceholderPath(t *testing.T) {
	clearRemovedAIEnvVars(t)
	const key = "VMM_TEST_MISSING_ANCHOR_LLM_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	configPath := filepath.Join(rootDir, "config.yaml")
	configBody := `grpc:
  listen_addr: "127.0.0.1:8080"
  request_timeout:
    pre_check: "15s"
    post_action: "15s"
pre_check:
  intent_timeout: "10s"
  top_k: 5
x-llm-defaults: &llm_defaults
  provider: "openai"
  endpoint: "https://api.openai.com/v1"
  api_keys: ["${` + key + `}"]
  model: "base-model"
llm:
  routes:
    - <<: *llm_defaults
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
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPaths([]string{configPath}, Config{})
	if err == nil {
		t.Fatal("expected merged anchor env placeholder to fail")
	}
	message := err.Error()
	for _, want := range []string{key, "llm.routes[0].api_keys[0]", "is not set"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not contain %q", message, want)
		}
	}
	if strings.Contains(message, "x-llm-defaults") {
		t.Fatalf("expected merged runtime field path, got anchor helper path: %q", message)
	}
}
