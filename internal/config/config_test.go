// config_test.go verifies normalization, validation, and layered loading against the current gRPC-only runtime contract.
// config_test.go 用于围绕当前仅 gRPC 运行时契约，验证配置归一化、校验和分层加载行为。
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesCurrentDefaults verifies the active runtime fills gRPC, provider, and storage defaults expected by the latest local build.
// TestConfigNormalizeAppliesCurrentDefaults 用于验证当前运行时会补齐最新本地构建所需的 gRPC、provider 和存储默认值。
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
	cfg.DuckDB.Address = ""
	cfg.SQLite.Address = ""
	cfg.LanceDB.Address = ""
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.MemoryPipeline.LexicalTopK = 0
	cfg.MemoryPipeline.RRFK = 0
	cfg.MemoryPipeline.MMRLambda = 0
	cfg.Rerank.Provider = ""
	cfg.Rerank.Endpoint = ""
	cfg.Rerank.Model = ""
	cfg.Rerank.TopN = 0
	cfg.Rerank.Timeout = Duration{}
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
	if cfg.PostAction.InputMode != "compat" {
		t.Fatalf("post action input mode = %q", cfg.PostAction.InputMode)
	}
	if cfg.PostAction.SessionAnalysisTurnThreshold != 2 {
		t.Fatalf("post action session analysis turn threshold = %d", cfg.PostAction.SessionAnalysisTurnThreshold)
	}
	if cfg.PostAction.SessionAnalysisTokenThreshold != 12000 {
		t.Fatalf("post action session analysis token threshold = %d", cfg.PostAction.SessionAnalysisTokenThreshold)
	}
	if cfg.PostAction.SessionAnalysisIdleTimeout.Duration != 15*time.Minute {
		t.Fatalf("post action session analysis idle timeout = %v", cfg.PostAction.SessionAnalysisIdleTimeout.Duration)
	}
	if cfg.PostAction.SessionAnalysisHistoryTurns != 3 {
		t.Fatalf("post action session analysis history turns = %d", cfg.PostAction.SessionAnalysisHistoryTurns)
	}
	if cfg.PostAction.SessionAnalysisMaxInputTokens != 6000 {
		t.Fatalf("post action session analysis max input tokens = %d", cfg.PostAction.SessionAnalysisMaxInputTokens)
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
	if cfg.MemoryPipeline.MaxSearchKeywords != 5 {
		t.Fatalf("max search keywords = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0.82 {
		t.Fatalf("min similarity score = %#v", cfg.MemoryPipeline.MinSimilarityScore)
	}
	if !cfg.MemoryPipeline.HybridEnabled {
		t.Fatal("expected hybrid retrieval to stay enabled by default")
	}
	if cfg.MemoryPipeline.LexicalTopK != 8 {
		t.Fatalf("lexical top_k = %d", cfg.MemoryPipeline.LexicalTopK)
	}
	if cfg.MemoryPipeline.RRFK != 60 {
		t.Fatalf("rrf_k = %d", cfg.MemoryPipeline.RRFK)
	}
	if !cfg.MemoryPipeline.MMREnabled {
		t.Fatal("expected mmr to stay enabled by default")
	}
	if cfg.MemoryPipeline.MMRLambda != 0.75 {
		t.Fatalf("mmr_lambda = %v", cfg.MemoryPipeline.MMRLambda)
	}
	if cfg.Rerank.Provider != "dashscope" {
		t.Fatalf("rerank provider = %q", cfg.Rerank.Provider)
	}
	if cfg.Rerank.Endpoint == "" {
		t.Fatal("expected rerank endpoint default")
	}
	if cfg.Rerank.Model != "qwen3-vl-rerank" {
		t.Fatalf("rerank model = %q", cfg.Rerank.Model)
	}
	if cfg.Rerank.TopN != 8 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if cfg.Rerank.Timeout.Duration != 8*time.Second {
		t.Fatalf("rerank timeout = %v", cfg.Rerank.Timeout.Duration)
	}
}

// TestConfigNormalizeClampsSearchKeywordFanOut verifies the recall keyword fan-out remains capped even when callers provide an excessive value.
// TestConfigNormalizeClampsSearchKeywordFanOut 用于验证即使调用方提供过大的值，召回关键词扇出仍会被钳制在上限内。
func TestConfigNormalizeClampsSearchKeywordFanOut(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.MaxSearchKeywords = 99
	cfg.Normalize()
	if cfg.MemoryPipeline.MaxSearchKeywords != 10 {
		t.Fatalf("max search keywords after clamp = %d", cfg.MemoryPipeline.MaxSearchKeywords)
	}
}

// TestConfigValidateRejectsInvalidHybridRetrievalKnobs verifies the new lexical recall and RRF parameters stay strictly positive once configured.
// TestConfigValidateRejectsInvalidHybridRetrievalKnobs 用于验证新增的 lexical 召回和 RRF 参数一旦配置后必须保持严格正数。
func TestConfigValidateRejectsInvalidHybridRetrievalKnobs(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.LexicalTopK = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.lexical_top_k must be > 0" {
		t.Fatalf("unexpected lexical_top_k validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.RRFK = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.rrf_k must be > 0" {
		t.Fatalf("unexpected rrf_k validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryPipeline.MMRLambda = 0
	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.mmr_lambda must be in (0,1]" {
		t.Fatalf("unexpected mmr_lambda validate error: %v", err)
	}
}

// TestConfigValidateRejectsUnknownPostActionMode verifies the new string-only post-action contract still rejects unsupported validation modes.
// TestConfigValidateRejectsUnknownPostActionMode 用于验证新的纯字符串 post-action 契约仍会拒绝不支持的校验模式。
func TestConfigValidateRejectsUnknownPostActionMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.InputMode = "broken"
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.input_mode must be either strict or compat" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsInvalidPostActionAnalysisThresholds verifies the future session-analysis trigger thresholds must stay positive.
// TestConfigValidateRejectsInvalidPostActionAnalysisThresholds 用于验证未来 session 分析触发阈值必须保持正数。
func TestConfigValidateRejectsInvalidPostActionAnalysisThresholds(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.PostAction.SessionAnalysisTurnThreshold = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_turn_threshold must be > 0" {
		t.Fatalf("unexpected turn-threshold validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisTokenThreshold = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_token_threshold must be > 0" {
		t.Fatalf("unexpected token-threshold validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisIdleTimeout = Duration{}
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_idle_timeout must be > 0" {
		t.Fatalf("unexpected idle-timeout validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisHistoryTurns = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_history_turns must be > 0" {
		t.Fatalf("unexpected history-turns validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.PostAction.SessionAnalysisMaxInputTokens = 0
	if err := cfg.Validate(); err == nil || err.Error() != "post_action.session_analysis_max_input_tokens must be > 0" {
		t.Fatalf("unexpected max-input-tokens validate error: %v", err)
	}
}

// TestConfigValidateRejectsPreCheckTimeoutBudget verifies the outer pre-check RPC budget must stay larger than the internal intent step timeout.
// TestConfigValidateRejectsPreCheckTimeoutBudget 用于验证外层 pre-check RPC 预算必须大于内部意图步骤超时。
func TestConfigValidateRejectsPreCheckTimeoutBudget(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.RequestTimeout.PreCheck = Duration{5 * time.Second}
	cfg.PreCheck.IntentTimeout = Duration{5 * time.Second}
	if err := cfg.Validate(); err == nil || err.Error() != "grpc.request_timeout.pre_check must be greater than pre_check.intent_timeout" {
		t.Fatalf("unexpected validate error: %v", err)
	}
}

// TestConfigValidateRejectsRemovedProviders verifies the runtime no longer accepts the removed in-memory fallbacks.
// TestConfigValidateRejectsRemovedProviders 用于验证运行时已经不再接受被移除的内存回退 provider。
func TestConfigValidateRejectsRemovedProviders(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Vector.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "vector.provider must be lancedb" {
		t.Fatalf("unexpected vector validate error: %v", err)
	}

	cfg = newValidConfigForTest()
	cfg.Relational.Provider = "memory"
	if err := cfg.Validate(); err == nil || err.Error() != "relational.provider must be sqlite or duckdb" {
		t.Fatalf("unexpected relational validate error: %v", err)
	}
}

// TestLoadExpandsEnvPlaceholdersFromDotEnv verifies the layered loader expands placeholders from the nearest resolved .env file.
// TestLoadExpandsEnvPlaceholdersFromDotEnv 用于验证分层加载器会从最近解析到的 .env 文件里展开占位符。
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
	configPath := filepath.Join(configDir, "local.json")
	if err := os.WriteFile(configPath, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
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

// TestLoadPathsMergesSystemAndOverrideConfigs verifies later config files override earlier ones while their colocated .env files also override earlier values.
// TestLoadPathsMergesSystemAndOverrideConfigs 用于验证后面的配置文件会覆盖前面的配置，同时其同目录的 .env 也会覆盖更早的值。
func TestLoadPathsMergesSystemAndOverrideConfigs(t *testing.T) {
	const key = "TEST_LOAD_PATHS_KEY"
	restoreEnv(t, key)

	rootDir := t.TempDir()
	systemConfigDir := filepath.Join(rootDir, "configs")
	overrideDir := filepath.Join(rootDir, "user")
	if err := os.MkdirAll(systemConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".env"), []byte(key+"=system-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, ".env"), []byte(key+"=user-dotenv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	systemConfig := filepath.Join(systemConfigDir, "local.json")
	overrideConfig := filepath.Join(overrideDir, "local.json")
	if err := os.WriteFile(systemConfig, []byte(currentTestConfigBody("${"+key+"}")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideConfig, []byte(`{"llm":{"model":"user-model"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPaths([]string{systemConfig, overrideConfig}, DefaultLocal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Model != "user-model" {
		t.Fatalf("llm model = %q", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "user-dotenv" {
		t.Fatalf("llm api key = %q", cfg.LLM.APIKey)
	}
}

// TestLoadExpandsModelSpecificProviderParams verifies provider parameter maps still support environment-expanded model keys.
// TestLoadExpandsModelSpecificProviderParams 用于验证 provider 参数映射仍支持带环境变量展开的模型键名。
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
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"workspace":"15s","pre_check":"8s","post_action":"8s"},"shutdown_timeout":"10s"},
		"duckdb":{"address":"127.0.0.1:19401","timeout":"5s"},
		"sqlite":{"address":"127.0.0.1:19501","timeout":"5s"},
		"lancedb":{"address":"127.0.0.1:19301","timeout":"5s","table_name":"vmm_memory_vectors","vector_column":"vector"},
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
		"relational":{"provider":"sqlite"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75}
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

// TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds verifies process-level overrides can tune the future session-analysis trigger thresholds.
// TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds 用于验证进程级环境变量可以覆盖未来 session 分析触发阈值。
func TestApplyEnvOverridesSetsPostActionSessionAnalysisThresholds(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_TURN_THRESHOLD", "33")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_TOKEN_THRESHOLD", "24000")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_IDLE_TIMEOUT", "25m")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_HISTORY_TURNS", "5")
	t.Setenv("VMM_POST_ACTION_SESSION_ANALYSIS_MAX_INPUT_TOKENS", "7200")

	applyEnvOverrides(&cfg)

	if cfg.PostAction.SessionAnalysisTurnThreshold != 33 {
		t.Fatalf("post action session analysis turn threshold = %d", cfg.PostAction.SessionAnalysisTurnThreshold)
	}
	if cfg.PostAction.SessionAnalysisTokenThreshold != 24000 {
		t.Fatalf("post action session analysis token threshold = %d", cfg.PostAction.SessionAnalysisTokenThreshold)
	}
	if cfg.PostAction.SessionAnalysisIdleTimeout.Duration != 25*time.Minute {
		t.Fatalf("post action session analysis idle timeout = %v", cfg.PostAction.SessionAnalysisIdleTimeout.Duration)
	}
	if cfg.PostAction.SessionAnalysisHistoryTurns != 5 {
		t.Fatalf("post action session analysis history turns = %d", cfg.PostAction.SessionAnalysisHistoryTurns)
	}
	if cfg.PostAction.SessionAnalysisMaxInputTokens != 7200 {
		t.Fatalf("post action session analysis max input tokens = %d", cfg.PostAction.SessionAnalysisMaxInputTokens)
	}
}

// TestApplyEnvOverridesSetsRerankSettings verifies process-level overrides can enable DashScope rerank without editing the base config file.
// TestApplyEnvOverridesSetsRerankSettings 用于验证进程级环境变量可以在不修改基础配置文件的前提下启用 DashScope rerank。
func TestApplyEnvOverridesSetsRerankSettings(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_RERANK_ENABLED", "true")
	t.Setenv("VMM_RERANK_PROVIDER", "dashscope")
	t.Setenv("VMM_RERANK_ENDPOINT", "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank")
	t.Setenv("VMM_RERANK_API_KEY", "dashscope-key")
	t.Setenv("VMM_RERANK_MODEL", "qwen3-vl-rerank")
	t.Setenv("VMM_RERANK_TOP_N", "6")
	t.Setenv("VMM_RERANK_TIMEOUT", "9s")

	applyEnvOverrides(&cfg)

	if !cfg.Rerank.Enabled {
		t.Fatal("expected rerank to be enabled")
	}
	if cfg.Rerank.Provider != "dashscope" {
		t.Fatalf("rerank provider = %q", cfg.Rerank.Provider)
	}
	if cfg.Rerank.APIKey != "dashscope-key" {
		t.Fatalf("rerank api key = %q", cfg.Rerank.APIKey)
	}
	if cfg.Rerank.Model != "qwen3-vl-rerank" {
		t.Fatalf("rerank model = %q", cfg.Rerank.Model)
	}
	if cfg.Rerank.TopN != 6 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if cfg.Rerank.Timeout.Duration != 9*time.Second {
		t.Fatalf("rerank timeout = %v", cfg.Rerank.Timeout.Duration)
	}
}

// TestApplyEnvOverridesSetsHybridRetrievalSettings verifies process-level overrides can tune lexical recall and RRF without editing the base config file.
// TestApplyEnvOverridesSetsHybridRetrievalSettings 用于验证进程级环境变量可以在不改基础配置文件的前提下调整 lexical 召回和 RRF 参数。
func TestApplyEnvOverridesSetsHybridRetrievalSettings(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_MEMORY_HYBRID_ENABLED", "false")
	t.Setenv("VMM_MEMORY_LEXICAL_TOP_K", "11")
	t.Setenv("VMM_MEMORY_RRF_K", "77")
	t.Setenv("VMM_MEMORY_MMR_ENABLED", "false")
	t.Setenv("VMM_MEMORY_MMR_LAMBDA", "0.66")

	applyEnvOverrides(&cfg)

	if cfg.MemoryPipeline.HybridEnabled {
		t.Fatal("expected hybrid retrieval to be disabled by env override")
	}
	if cfg.MemoryPipeline.LexicalTopK != 11 {
		t.Fatalf("memory pipeline lexical top_k = %d", cfg.MemoryPipeline.LexicalTopK)
	}
	if cfg.MemoryPipeline.RRFK != 77 {
		t.Fatalf("memory pipeline rrf_k = %d", cfg.MemoryPipeline.RRFK)
	}
	if cfg.MemoryPipeline.MMREnabled {
		t.Fatal("expected mmr to be disabled by env override")
	}
	if cfg.MemoryPipeline.MMRLambda != 0.66 {
		t.Fatalf("memory pipeline mmr_lambda = %v", cfg.MemoryPipeline.MMRLambda)
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

// currentTestConfigBody returns one minimal latest-format config body used by placeholder and layered-load tests.
// currentTestConfigBody 用于返回占位符与分层加载测试使用的最小最新格式配置体。
func currentTestConfigBody(apiKeyExpr string) string {
	return `{
		"grpc":{"listen_addr":"127.0.0.1:8080","request_timeout":{"workspace":"15s","pre_check":"8s","post_action":"8s"},"shutdown_timeout":"10s"},
		"duckdb":{"address":"127.0.0.1:19401","timeout":"5s"},
		"sqlite":{"address":"127.0.0.1:19501","timeout":"5s"},
		"lancedb":{"address":"127.0.0.1:19301","timeout":"5s","table_name":"vmm_memory_vectors","vector_column":"vector"},
		"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"` + apiKeyExpr + `","model":"test-model"},
		"embedding":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"` + apiKeyExpr + `","model":"text-embedding-3-large","dimension":1024},
		"vector":{"provider":"lancedb"},
		"relational":{"provider":"sqlite"},
		"pre_check":{"intent_timeout":"5s","top_k":5},
		"memory_pipeline":{"max_search_keywords":5,"min_similarity_score":0.75}
	}`
}

// newValidConfigForTest returns one minimal fully valid config so focused validation tests fail only on the target field.
// newValidConfigForTest 用于返回一份最小且完整的有效配置，让聚焦校验测试只在目标字段上失败。
func newValidConfigForTest() Config {
	cfg := DefaultLocal()
	cfg.GRPC.ListenAddr = "127.0.0.1:8080"
	cfg.LLM.Endpoint = "https://api.openai.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.Embedding.Dimension = 1024
	cfg.DuckDB.Address = "127.0.0.1:19401"
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"
	return cfg
}
