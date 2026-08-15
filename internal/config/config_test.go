// config_test.go verifies the simplified runtime config contract where prompt selection is explicit, LLM/rerank use explicit routes, and embedding stays single-provider with multi-key failover.
// config_test.go 用于验证收敛后的运行时配置契约：提示词选择显式配置，LLM/rerank 只使用显式 routes，embedding 保持单 provider 且支持多 key 容灾。
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestConfigNormalizeAppliesCurrentDefaults verifies Normalize still fills the current gRPC, storage, rerank, and prompt-bundle defaults expected by the local runtime.
// TestConfigNormalizeAppliesCurrentDefaults 用于验证 Normalize 仍会补齐当前本地运行时所需的 gRPC、存储、rerank 与提示词包默认值。
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
	cfg.SQLite.TokenizerMode = ""
	cfg.LanceDB.Address = ""
	cfg.Controller.Endpoint = ""
	cfg.Controller.ProcessMode = ""
	cfg.Controller.MinimumUptime = Duration{}
	cfg.Controller.IdleTimeout = Duration{}
	cfg.Controller.LeaseTTL = Duration{}
	cfg.Controller.ConnectTimeout = Duration{}
	cfg.Controller.StartupTimeout = Duration{}
	cfg.Controller.StartupRetryInterval = Duration{}
	cfg.Controller.LeaseRenewInterval = Duration{}
	cfg.Controller.RequestTimeout = Duration{}
	cfg.Controller.SpaceID = ""
	cfg.Controller.SpaceLabel = ""
	cfg.MemoryPipeline.MaxSearchKeywords = 0
	cfg.MemoryPipeline.MinSimilarityScore = nil
	cfg.MemoryPipeline.ReplaceMinSimilarityScore = nil
	cfg.MemoryPipeline.HardDedupeCosineThreshold = nil
	cfg.MemoryPipeline.HardDedupePoolTopK = 0
	cfg.Prompts.PromptLanguage = ""
	cfg.MaintenanceTool.Postgres.ReadTimeout = Duration{}
	cfg.MaintenanceTool.Postgres.WriteTimeout = Duration{}
	cfg.MaintenanceTool.VectorRebuildBatchSize = 0
	cfg.Embedding.MaxBatchSize = 0
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
	if cfg.SQLite.Address != "" {
		t.Fatalf("sqlite address = %q", cfg.SQLite.Address)
	}
	if cfg.SQLite.TokenizerMode != "jieba" {
		t.Fatalf("sqlite tokenizer mode = %q", cfg.SQLite.TokenizerMode)
	}
	if cfg.LanceDB.Address != "" {
		t.Fatalf("lancedb address = %q", cfg.LanceDB.Address)
	}
	if got, want := cfg.Controller.Endpoint, "http://127.0.0.1:19801"; got != want {
		t.Fatalf("controller endpoint = %q, want %q", got, want)
	}
	if got, want := cfg.Controller.ProcessMode, "managed"; got != want {
		t.Fatalf("controller process mode = %q, want %q", got, want)
	}
	if got, want := cfg.Controller.LeaseTTL.Duration, 2*time.Minute; got != want {
		t.Fatalf("controller lease ttl = %v, want %v", got, want)
	}
	if got, want := cfg.Controller.LeaseRenewInterval.Duration, 30*time.Second; got != want {
		t.Fatalf("controller lease renew interval = %v, want %v", got, want)
	}
	if got, want := cfg.Controller.RequestTimeout.Duration, 30*time.Second; got != want {
		t.Fatalf("controller request timeout = %v, want %v", got, want)
	}
	if got, want := cfg.Controller.SpaceID, "vmm-local-default"; got != want {
		t.Fatalf("controller space id = %q, want %q", got, want)
	}
	if got, want := cfg.Prompts.PromptLanguage, defaultPromptBundle; got != want {
		t.Fatalf("prompt language = %q, want %q", got, want)
	}
	if cfg.Logging.LLMOutputEnabled {
		t.Fatal("expected llm output logging to stay disabled by default")
	}
	if cfg.MemoryPipeline.MinSimilarityScore == nil || *cfg.MemoryPipeline.MinSimilarityScore != 0.82 {
		t.Fatalf("min similarity score = %#v", cfg.MemoryPipeline.MinSimilarityScore)
	}
	if cfg.MemoryPipeline.ReplaceMinSimilarityScore == nil || *cfg.MemoryPipeline.ReplaceMinSimilarityScore != defaultMemoryReplaceMinSimilarityScore {
		t.Fatalf("replace min similarity score = %#v", cfg.MemoryPipeline.ReplaceMinSimilarityScore)
	}
	if cfg.MemoryPipeline.HardDedupeCosineThreshold == nil || *cfg.MemoryPipeline.HardDedupeCosineThreshold != defaultMemoryHardDedupeCosineThreshold {
		t.Fatalf("hard dedupe cosine threshold = %#v", cfg.MemoryPipeline.HardDedupeCosineThreshold)
	}
	if cfg.MemoryPipeline.HardDedupePoolTopK != defaultMemoryHardDedupePoolTopK {
		t.Fatalf("hard dedupe pool top k = %d", cfg.MemoryPipeline.HardDedupePoolTopK)
	}
	if cfg.Rerank.TopN != 8 {
		t.Fatalf("rerank top_n = %d", cfg.Rerank.TopN)
	}
	if got, want := cfg.MaintenanceTool.Postgres.ReadTimeout.Duration, 30*time.Second; got != want {
		t.Fatalf("maintenance tool postgres read timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.Postgres.WriteTimeout.Duration, 10*time.Minute; got != want {
		t.Fatalf("maintenance tool postgres write timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.VectorRebuildBatchSize, defaultMaintenanceToolVectorRebuildBatchSize; got != want {
		t.Fatalf("maintenance tool vector rebuild batch size = %d, want %d", got, want)
	}
	if got, want := cfg.Embedding.MaxBatchSize, defaultEmbeddingMaxBatchSize; got != want {
		t.Fatalf("embedding max batch size = %d, want %d", got, want)
	}
	if got, want := cfg.Rerank.Routes[0].Endpoint, defaultDashScopeRerankEndpoint; got != want {
		t.Fatalf("rerank default endpoint = %q, want %q", got, want)
	}
	if got, want := cfg.Rerank.Routes[0].Model, defaultDashScopeRerankModel; got != want {
		t.Fatalf("rerank default model = %q, want %q", got, want)
	}
	if got, want := cfg.Rerank.Routes[0].Timeout.Duration, 8*time.Second; got != want {
		t.Fatalf("rerank default timeout = %v, want %v", got, want)
	}
}

// TestConfigValidateAcceptsControllerMode verifies one loopback controller can own both split-store backends without activating PostgreSQL validation.
// TestConfigValidateAcceptsControllerMode 用于验证 loopback controller 可以统一持有两种 split 后端，且不会触发 PostgreSQL 校验。
func TestConfigValidateAcceptsControllerMode(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Postgres.DSN = ""
	cfg.Normalize()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate controller mode: %v", err)
	}
	if !cfg.UsesController() {
		t.Fatal("expected controller mode helper to be enabled")
	}
}

// TestConfigValidateRejectsRemoteControllerEndpoint verifies the unauthenticated first release cannot expose its database control plane on a remote host.
// TestConfigValidateRejectsRemoteControllerEndpoint 用于验证首版无认证控制面不能通过远程主机暴露数据库能力。
func TestConfigValidateRejectsRemoteControllerEndpoint(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Controller.Endpoint = "http://192.168.31.10:19801"
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "controller.endpoint must use a loopback host when storage.mode=controller" {
		t.Fatalf("expected loopback controller endpoint error, got %v", err)
	}
}

// TestConfigValidateRejectsControllerLeaseRenewalAtTTL verifies renewal must happen before the registered lease expires.
// TestConfigValidateRejectsControllerLeaseRenewalAtTTL 用于验证续租必须早于已注册租约到期。
func TestConfigValidateRejectsControllerLeaseRenewalAtTTL(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Storage.Mode = "controller"
	cfg.Controller.LeaseTTL = Duration{30 * time.Second}
	cfg.Controller.LeaseRenewInterval = Duration{30 * time.Second}
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "controller.lease_renew_interval must be less than controller.lease_ttl" {
		t.Fatalf("expected controller lease interval error, got %v", err)
	}
}

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

// TestDefaultBaseKeepsAIKeysEmpty verifies baked-in defaults never treat YAML environment placeholders as real API keys.
// TestDefaultBaseKeepsAIKeysEmpty 用于验证内建默认值不会把 YAML 环境变量占位符当作真实 API Key。
func TestDefaultBaseKeepsAIKeysEmpty(t *testing.T) {
	cfg := DefaultBase()
	if got := cfg.LLM.Routes[0].APIKeys; len(got) != 0 {
		t.Fatalf("default llm api keys = %#v, want empty", got)
	}
	if got := cfg.Embedding.APIKeys; len(got) != 0 {
		t.Fatalf("default embedding api keys = %#v, want empty", got)
	}
	if got := cfg.Rerank.Routes[0].APIKeys; len(got) != 0 {
		t.Fatalf("default rerank api keys = %#v, want empty", got)
	}
}

// TestConfigNormalizeAppliesGRPCKeepaliveDefaults verifies Normalize restores the server-side keepalive defaults and clamps negative connection-age values back to the disabled baseline.
// TestConfigNormalizeAppliesGRPCKeepaliveDefaults 用于验证 Normalize 会恢复服务端 keepalive 默认值，并把负数连接年龄配置钳制回“关闭该限制”的基线。
func TestConfigNormalizeAppliesGRPCKeepaliveDefaults(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.Keepalive.Time = Duration{}
	cfg.GRPC.Keepalive.Timeout = Duration{}
	cfg.GRPC.Keepalive.MinPingInterval = Duration{}
	cfg.GRPC.Keepalive.MaxConnectionIdle = Duration{-1 * time.Second}
	cfg.GRPC.Keepalive.MaxConnectionAge = Duration{-1 * time.Second}
	cfg.GRPC.Keepalive.MaxConnectionAgeGrace = Duration{-1 * time.Second}

	cfg.Normalize()

	if !cfg.GRPC.Keepalive.Enabled {
		t.Fatal("expected grpc keepalive to stay enabled by default")
	}
	if got, want := cfg.GRPC.Keepalive.Time.Duration, 30*time.Second; got != want {
		t.Fatalf("grpc keepalive time = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.Timeout.Duration, 10*time.Second; got != want {
		t.Fatalf("grpc keepalive timeout = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.MinPingInterval.Duration, 20*time.Second; got != want {
		t.Fatalf("grpc keepalive min ping interval = %v, want %v", got, want)
	}
	if got := cfg.GRPC.Keepalive.MaxConnectionIdle.Duration; got != 0 {
		t.Fatalf("grpc keepalive max connection idle = %v, want 0", got)
	}
	if got := cfg.GRPC.Keepalive.MaxConnectionAge.Duration; got != 0 {
		t.Fatalf("grpc keepalive max connection age = %v, want 0", got)
	}
	if got := cfg.GRPC.Keepalive.MaxConnectionAgeGrace.Duration; got != 0 {
		t.Fatalf("grpc keepalive max connection age grace = %v, want 0", got)
	}
	if !cfg.GRPC.Keepalive.PermitWithoutStream {
		t.Fatal("expected grpc keepalive permit_without_stream to stay enabled by default")
	}
}

// TestConfigNormalizeCanonicalizesPromptLanguageAliases verifies built-in prompt-language aliases normalize into the canonical bundle directory names while preserving custom bundle names.
// TestConfigNormalizeCanonicalizesPromptLanguageAliases 用于验证内建提示词语言别名会归一成规范目录名，同时保留自定义提示词包名称。
func TestConfigNormalizeCanonicalizesPromptLanguageAliases(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Prompts.PromptLanguage = " zh-CN "

	cfg.Normalize()

	if got, want := cfg.Prompts.PromptLanguage, defaultChinesePromptBundle; got != want {
		t.Fatalf("normalized chinese prompt language = %q, want %q", got, want)
	}

	cfg = newValidConfigForTest()
	cfg.Prompts.PromptLanguage = " custom-bundle "

	cfg.Normalize()

	if got, want := cfg.Prompts.PromptLanguage, "custom-bundle"; got != want {
		t.Fatalf("normalized custom prompt language = %q, want %q", got, want)
	}
}

// TestConfigNormalizePreservesExplicitZeroHardDedupeThreshold verifies callers can explicitly disable vector hard-dedupe without Normalize silently restoring the default threshold.
// TestConfigNormalizePreservesExplicitZeroHardDedupeThreshold 用于验证调用方可以显式关闭向量硬排重，而不会在 Normalize 后被悄悄恢复成默认阈值。
func TestConfigNormalizePreservesExplicitZeroHardDedupeThreshold(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.HardDedupeCosineThreshold = float64Ptr(0)

	cfg.Normalize()

	if cfg.MemoryPipeline.HardDedupeCosineThreshold == nil || *cfg.MemoryPipeline.HardDedupeCosineThreshold != 0 {
		t.Fatalf("hard dedupe cosine threshold = %#v", cfg.MemoryPipeline.HardDedupeCosineThreshold)
	}
}

// TestConfigNormalizeCanonicalizesMemoryReplaceScope verifies memory replacement scope follows the same default and canonical spelling that runtime wiring expects.
// TestConfigNormalizeCanonicalizesMemoryReplaceScope 用于验证记忆更替作用域会归一到运行时装配期望的默认值和规范拼写。
func TestConfigNormalizeCanonicalizesMemoryReplaceScope(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryReplaceScope = ""

	cfg.Normalize()

	if got, want := cfg.MemoryReplaceScope, "project"; got != want {
		t.Fatalf("default memory replace scope = %q, want %q", got, want)
	}

	cfg = newValidConfigForTest()
	cfg.MemoryReplaceScope = " TEAM "

	cfg.Normalize()

	if got, want := cfg.MemoryReplaceScope, "team"; got != want {
		t.Fatalf("normalized memory replace scope = %q, want %q", got, want)
	}
}

// TestConfigValidateRejectsUnsupportedMemoryReplaceScope verifies startup validation rejects replacement scopes that the post-action and direct-write runtime filters cannot interpret.
// TestConfigValidateRejectsUnsupportedMemoryReplaceScope 用于验证启动校验会拒绝 post-action 与主动写入运行时过滤器无法解释的记忆更替作用域。
func TestConfigValidateRejectsUnsupportedMemoryReplaceScope(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryReplaceScope = "workspace"
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "memory_replace_scope must be one of session, team, space, or project" {
		t.Fatalf("unexpected memory replace scope validate error: %v", err)
	}
}

// TestConfigValidateRejectsNonPositiveHardDedupePoolTopK verifies startup validation rejects disabled or malformed hard-dedupe pool sizes because the reviewer-front recall split depends on a positive expansion window.
// TestConfigValidateRejectsNonPositiveHardDedupePoolTopK 用于验证启动校验会拒绝禁用或格式错误的硬排重候选池大小，因为 reviewer 前召回拆分依赖一个正数扩展窗口。
func TestConfigValidateRejectsNonPositiveHardDedupePoolTopK(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.MemoryPipeline.HardDedupePoolTopK = -1
	cfg.MemoryPipeline.hardDedupePoolTopKSet = true
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.hard_dedupe_pool_top_k must be > 0" {
		t.Fatalf("unexpected hard dedupe pool top k validate error: %v", err)
	}
}

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

	cfg, err := LoadPaths([]string{basePath, overridePath}, Config{})
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

// TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced verifies explicitly referenced typed environment overrides report parse failures instead of silently falling back to existing values.
// TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced 用于验证显式引用的类型化环境变量覆盖会报告解析失败，而不是静默回退到已有值。
func TestApplyEnvOverridesReportsInvalidTypedValuesWhenReferenced(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "int", key: "VMM_PRE_CHECK_TOPK", value: "not-int"},
		{name: "float", key: "VMM_PRE_CHECK_SIMILARITY_THRESHOLD", value: "not-float"},
		{name: "optional-float", key: "VMM_MEMORY_MIN_SIMILARITY_SCORE", value: "not-float"},
		{name: "bool", key: "VMM_NOISE_ENABLED", value: "not-bool"},
		{name: "duration", key: "VMM_GRPC_PRE_CHECK_TIMEOUT", value: "not-duration"},
		{name: "dedupe-pool-int", key: "VMM_MEMORY_HARD_DEDUPE_POOL_TOP_K", value: "not-int"},
		{name: "postaction-timeout", key: "VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT", value: "not-duration"},
		{name: "postaction-failure-threshold", key: "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD", value: "not-int"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreEnv(t, tc.key)
			t.Setenv(tc.key, tc.value)

			cfg := newValidConfigForTest()
			failures := applyEnvOverrides(&cfg, map[string]struct{}{
				tc.key: {},
			})
			if !slices.Contains(failures, tc.key) {
				t.Fatalf("parse failures = %#v, want %q", failures, tc.key)
			}
		})
	}
}

// TestPostActionReliabilityConfigRejectsUnsafeValues verifies explicit invalid values fail closed instead of being normalized back to defaults.
// TestPostActionReliabilityConfigRejectsUnsafeValues 用于验证显式无效值会封闭失败，而不是被归一化回默认值。
func TestPostActionReliabilityConfigRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "analysis timeout below minimum",
			mutate: func(cfg *Config) {
				cfg.PostAction.SessionAnalysisTimeout = Duration{Duration: 119 * time.Second}
			},
			wantErr: "post_action.session_analysis_timeout must be >= 2m",
		},
		{
			name: "zero failure threshold",
			mutate: func(cfg *Config) {
				cfg.PostAction.FailurePassThreshold = 0
			},
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
		{
			name: "negative failure threshold",
			mutate: func(cfg *Config) {
				cfg.PostAction.FailurePassThreshold = -1
			},
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newValidConfigForTest()
			cfg.Normalize()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues verifies typed environment overrides cannot be normalized back to defaults after parsing.
// TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues 用于验证类型化环境变量覆盖在解析后不会被归一化回默认值。
func TestPostActionReliabilityEnvOverridesPreserveExplicitUnsafeValues(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{
			name:    "zero analysis timeout",
			key:     "VMM_POST_ACTION_SESSION_ANALYSIS_TIMEOUT",
			value:   "0s",
			wantErr: "post_action.session_analysis_timeout must be >= 2m",
		},
		{
			name:    "zero failure threshold",
			key:     "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD",
			value:   "0",
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
		{
			name:    "negative failure threshold",
			key:     "VMM_POST_ACTION_FAILURE_PASS_THRESHOLD",
			value:   "-1",
			wantErr: "post_action.failure_pass_threshold must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			cfg := newValidConfigForTest()
			if failures := applyEnvOverrides(&cfg, map[string]struct{}{tc.key: {}}); len(failures) != 0 {
				t.Fatalf("applyEnvOverrides() failures = %#v", failures)
			}
			cfg.Normalize()
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys verifies the path-gating table stays in lockstep with every supported process-level VMM override.
// TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys 用于验证路径门控表与所有受支持的进程级 VMM 覆盖保持同步。
func TestSupportedEnvOverrideValuePathsCoverApplyEnvOverridesKeys(t *testing.T) {
	body, err := os.ReadFile("config_validate.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	supported := map[string]struct{}{}
	for _, match := range regexp.MustCompile(`set(?:String|StringSlice|Int|Int64|Float|OptionalFloat|Bool|Duration)\("([A-Z0-9_]+)"`).FindAllStringSubmatch(source, -1) {
		supported[match[1]] = struct{}{}
	}
	for _, match := range regexp.MustCompile(`envOverrideAllowed\(referencedEnvKeys, "([A-Z0-9_]+)"`).FindAllStringSubmatch(source, -1) {
		supported[match[1]] = struct{}{}
	}
	mapped := map[string]struct{}{}
	for key := range supportedEnvOverrideValuePaths {
		mapped[key] = struct{}{}
	}

	missing := []string{}
	for key := range supported {
		if _, ok := mapped[key]; !ok {
			missing = append(missing, key)
		}
	}
	extra := []string{}
	for key := range mapped {
		if _, ok := supported[key]; !ok {
			extra = append(extra, key)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("env override path table mismatch: missing=%v extra=%v", missing, extra)
	}
}

// TestSupportedEnvOverrideValuePathsExistInConfigSchema verifies every path-gating entry points at a real JSON-tagged Config field.
// TestSupportedEnvOverrideValuePathsExistInConfigSchema 用于验证每个路径门控项都指向真实存在的 Config JSON 标签字段。
func TestSupportedEnvOverrideValuePathsExistInConfigSchema(t *testing.T) {
	rootType := reflect.TypeOf(Config{})
	invalid := []string{}
	for key, paths := range supportedEnvOverrideValuePaths {
		for _, path := range paths {
			if configSchemaPathExists(rootType, path) {
				continue
			}
			invalid = append(invalid, key+"="+path)
		}
	}
	slices.Sort(invalid)
	if len(invalid) > 0 {
		t.Fatalf("env override path table contains unknown config paths: %v", invalid)
	}
}

// configSchemaPathExists walks one dot-separated JSON-tag path through the Config struct schema.
// configSchemaPathExists 用于沿着 Config 结构体 schema 检查一个点分 JSON 标签路径是否存在。
func configSchemaPathExists(rootType reflect.Type, path string) bool {
	current := rootType
	for _, segment := range strings.Split(path, ".") {
		if strings.TrimSpace(segment) == "" {
			return false
		}
		for current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct {
			return false
		}
		field, ok := jsonTaggedFieldByName(current, segment)
		if !ok {
			return false
		}
		current = field.Type
	}
	return true
}

// jsonTaggedFieldByName returns the struct field whose JSON tag owns one config path segment.
// jsonTaggedFieldByName 用于返回 JSON 标签归属于某个配置路径片段的结构体字段。
func jsonTaggedFieldByName(structType reflect.Type, segment string) (reflect.StructField, bool) {
	for idx := 0; idx < structType.NumField(); idx++ {
		field := structType.Field(idx)
		tagName := strings.Split(field.Tag.Get("json"), ",")[0]
		if tagName == "" || tagName == "-" {
			continue
		}
		if tagName == segment {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

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

// TestValidateRemovedAIEnvOverridesOnlyWhenReferenced verifies removed AI env overrides are only rejected after the config explicitly opts into the corresponding placeholder.
// TestValidateRemovedAIEnvOverridesOnlyWhenReferenced 用于验证已移除的 AI 环境变量只会在配置显式引用对应占位符后才被拒绝。
func TestValidateRemovedAIEnvOverridesOnlyWhenReferenced(t *testing.T) {
	restoreEnv(t, "VMM_LLM_API_KEYS")
	t.Setenv("VMM_LLM_API_KEYS", "legacy-key")
	if err := validateRemovedAIEnvOverrides(nil); err != nil {
		t.Fatalf("expected unreferenced removed env override to be ignored, got %v", err)
	}
	err := validateRemovedAIEnvOverrides(map[string]struct{}{"VMM_LLM_API_KEYS": {}})
	if err == nil || !strings.Contains(err.Error(), "VMM_LLM_API_KEYS has been removed") {
		t.Fatalf("unexpected removed env error after explicit reference: %v", err)
	}
}

// TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced verifies supported embedding env overrides still inject multi-key pools and shared node budgets after the config explicitly opts into that placeholder.
// TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced 用于验证配置显式引用对应占位符后，embedding 环境变量覆盖仍会注入多 key 池与共享节点预算。
func TestApplyEnvOverridesSetsEmbeddingKeyPoolsWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_EMBED_API_KEYS", "embed-a,embed-b")
	t.Setenv("VMM_EMBED_RPM", "7")
	t.Setenv("VMM_EMBED_TPM", "700")
	t.Setenv("VMM_EMBED_RPD", "70")
	t.Setenv("VMM_EMBED_MAX_BATCH_SIZE", "12")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_EMBED_API_KEYS":       {},
		"VMM_EMBED_RPM":            {},
		"VMM_EMBED_TPM":            {},
		"VMM_EMBED_RPD":            {},
		"VMM_EMBED_MAX_BATCH_SIZE": {},
	})
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
	if got, want := cfg.Embedding.MaxBatchSize, 12; got != want {
		t.Fatalf("embedding max batch size = %d, want %d", got, want)
	}
}

// TestApplyEnvOverridesSkipsUnreferencedValues verifies plain YAML values no longer get silently replaced by supported env overrides when the config did not explicitly reference those placeholders.
// TestApplyEnvOverridesSkipsUnreferencedValues 用于验证当配置未显式引用对应占位符时，字面量 YAML 值不会再被支持的环境变量覆盖静默替换。
func TestApplyEnvOverridesSkipsUnreferencedValues(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.RequestTimeout.PreCheck = Duration{15 * time.Second}
	t.Setenv("VMM_GRPC_PRE_CHECK_TIMEOUT", "8s")

	_ = applyEnvOverrides(&cfg, nil)

	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, 15*time.Second; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
	}
}

// TestApplyEnvOverridesSetsPromptLanguageWhenReferenced verifies prompt-language env overrides only take effect after the config explicitly opts into that placeholder.
// TestApplyEnvOverridesSetsPromptLanguageWhenReferenced 用于验证只有在配置显式引用对应占位符后，提示词语言环境变量覆盖才会生效。
func TestApplyEnvOverridesSetsPromptLanguageWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_PROMPTS_PROMPT_LANGUAGE", "zh-CN")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_PROMPTS_PROMPT_LANGUAGE": {},
	})
	cfg.Normalize()

	if got, want := cfg.Prompts.PromptLanguage, defaultChinesePromptBundle; got != want {
		t.Fatalf("prompt language = %q, want %q", got, want)
	}
}

// TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced verifies the dedicated LLM output log switch only changes when the config explicitly opts into the environment override.
// TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced 用于验证只有在配置显式引用对应环境变量时，专用 LLM 输出日志开关才会被环境覆盖。
func TestApplyEnvOverridesSetsLLMOutputLoggingWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_LOG_LLM_OUTPUT_ENABLED", "true")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_LOG_LLM_OUTPUT_ENABLED": {},
	})
	cfg.Normalize()

	if !cfg.Logging.LLMOutputEnabled {
		t.Fatal("expected llm output logging to be enabled by env override")
	}
}

// TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced verifies maintenance-tool timeout env overrides stay isolated from the regular postgres runtime node after the config explicitly references those placeholders.
// TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced 用于验证配置显式引用对应占位符后，维护工具超时环境变量覆盖会写入独立 maintenance_tool 节点，而不会污染常规 postgres 运行时节点。
func TestApplyEnvOverridesSetsMaintenanceToolPostgresTimeoutsWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	t.Setenv("VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT", "45s")
	t.Setenv("VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT", "12m")
	t.Setenv("VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE", "24")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_MAINTENANCE_TOOL_POSTGRES_READ_TIMEOUT":     {},
		"VMM_MAINTENANCE_TOOL_POSTGRES_WRITE_TIMEOUT":    {},
		"VMM_MAINTENANCE_TOOL_VECTOR_REBUILD_BATCH_SIZE": {},
	})
	cfg.Normalize()

	if got, want := cfg.MaintenanceTool.Postgres.ReadTimeout.Duration, 45*time.Second; got != want {
		t.Fatalf("maintenance tool postgres read timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.Postgres.WriteTimeout.Duration, 12*time.Minute; got != want {
		t.Fatalf("maintenance tool postgres write timeout = %v, want %v", got, want)
	}
	if got, want := cfg.MaintenanceTool.VectorRebuildBatchSize, 24; got != want {
		t.Fatalf("maintenance tool vector rebuild batch size = %d, want %d", got, want)
	}
	if got, want := cfg.Postgres.QueryTimeout.Duration, 5*time.Second; got != want {
		t.Fatalf("postgres query timeout = %v, want %v", got, want)
	}
}

// TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced verifies explicit env placeholders can tune the new gRPC keepalive block without mutating unrelated transport defaults.
// TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced 用于验证在显式引用环境变量占位符后，可以调整新的 gRPC keepalive 配置块，同时不污染无关的传输层默认值。
func TestApplyEnvOverridesSetsGRPCKeepaliveWhenReferenced(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.GRPC.Keepalive.Enabled = false
	originalPreCheckTimeout := cfg.GRPC.RequestTimeout.PreCheck.Duration
	t.Setenv("VMM_GRPC_KEEPALIVE_ENABLED", "true")
	t.Setenv("VMM_GRPC_KEEPALIVE_TIME", "45s")
	t.Setenv("VMM_GRPC_KEEPALIVE_TIMEOUT", "12s")
	t.Setenv("VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL", "30s")
	t.Setenv("VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM", "false")

	_ = applyEnvOverrides(&cfg, map[string]struct{}{
		"VMM_GRPC_KEEPALIVE_ENABLED":               {},
		"VMM_GRPC_KEEPALIVE_TIME":                  {},
		"VMM_GRPC_KEEPALIVE_TIMEOUT":               {},
		"VMM_GRPC_KEEPALIVE_MIN_PING_INTERVAL":     {},
		"VMM_GRPC_KEEPALIVE_PERMIT_WITHOUT_STREAM": {},
	})
	cfg.Normalize()

	if !cfg.GRPC.Keepalive.Enabled {
		t.Fatal("expected grpc keepalive to be enabled by env override")
	}
	if got, want := cfg.GRPC.Keepalive.Time.Duration, 45*time.Second; got != want {
		t.Fatalf("grpc keepalive time = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.Timeout.Duration, 12*time.Second; got != want {
		t.Fatalf("grpc keepalive timeout = %v, want %v", got, want)
	}
	if got, want := cfg.GRPC.Keepalive.MinPingInterval.Duration, 30*time.Second; got != want {
		t.Fatalf("grpc keepalive min ping interval = %v, want %v", got, want)
	}
	if cfg.GRPC.Keepalive.PermitWithoutStream {
		t.Fatal("expected grpc keepalive permit_without_stream to be disabled by env override")
	}
	if got, want := cfg.GRPC.RequestTimeout.PreCheck.Duration, originalPreCheckTimeout; got != want {
		t.Fatalf("pre-check timeout = %v, want %v", got, want)
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

	cfg, err := LoadPaths([]string{basePath, configPath}, Config{})
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

	cfg, err := LoadPaths([]string{basePath, configPath}, Config{})
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
		Params:   map[string]any{"reasoning_effort": "none"},
	}}
	cfg.Embedding.Endpoint = "https://api.openai.com/v1"
	cfg.Embedding.APIKeys = []string{"test-key"}
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []RerankRouteConfig{{
		Provider: "dashscope",
		Endpoint: defaultDashScopeRerankEndpoint,
		APIKeys:  []string{"rerank-key"},
		Model:    defaultDashScopeRerankModel,
		Timeout:  Duration{8 * time.Second},
	}}
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"
	return cfg
}

// intPtr returns one stable pointer to the provided integer so config tests can express explicit zero/positive/negative route weights.
// intPtr 用于返回指向目标整数的稳定指针，让配置测试可以表达显式的零值、正值或负值路由权重。
func intPtr(value int) *int {
	return &value
}
