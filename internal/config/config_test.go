// Behavioral tests grouped by config test.
// 配置默认值与共享夹具行为测试，沿用同包夹具验证对应业务链路。
package config

import (
	"os"
	"testing"
	"time"
)

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
	cfg.Normalize()

	if err := cfg.Validate(); err == nil || err.Error() != "memory_pipeline.hard_dedupe_pool_top_k must be > 0" {
		t.Fatalf("unexpected hard dedupe pool top k validate error: %v", err)
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
