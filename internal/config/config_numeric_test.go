// config_numeric_test.go verifies numeric configuration through the real layered loader rather than isolated validation.
// config_numeric_test.go 通过实际分层加载器验证数值配置，而非仅调用独立校验。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadRejectsExplicitInvalidNumbers preserves missing defaults but rejects explicit zero, negative, and out-of-range values.
// TestLoadRejectsExplicitInvalidNumbers 保留缺省默认值，并拒绝显式零值、负数和越界值。
func TestLoadRejectsExplicitInvalidNumbers(t *testing.T) {
	for _, test := range []struct {
		body  string
		field string
	}{
		{"grpc:\n  max_receive_message_bytes: -1\n", "grpc.max_receive_message_bytes"},
		{"grpc:\n  max_receive_message_bytes: 0\n", "grpc.max_receive_message_bytes"},
		{"pre_check:\n  top_k: -2\n", "top_k"},
		{"memory_pipeline:\n  max_search_keywords: 999\n", "max_search_keywords"},
		{"memory_pipeline:\n  mmr_lambda: 2\n", "mmr_lambda"},
		{"memory_pipeline:\n  weibull_min_multiplier: -1\n", "weibull_min_multiplier"},
		{"grpc:\n  keepalive:\n    max_connection_age: -1s\n", "grpc.keepalive"},
		{"embedding:\n  dimension: 0\n", "embedding.dimension"},
		{"embedding:\n  key_failover:\n    auth_cooldown: -1s\n", "key_failover"},
		{"embedding:\n  key_failover:\n    policy: misspelled\n", "key_failover.policy"},
	} {
		t.Run(test.field+test.body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "override.yaml")
			if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path, newValidConfigForTest()); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s validation error, got %v", test.field, err)
			}
		})
	}
}

// TestLoadPreservesDefaultsAndValidZero confirms omission inherits defaults and documented zero-valued weights remain zero.
// TestLoadPreservesDefaultsAndValidZero 确认缺省字段继承默认值，契约允许为零的权重保持为零。
func TestLoadPreservesDefaultsAndValidZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(path, []byte("memory_pipeline:\n  weibull_min_multiplier: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, newValidConfigForTest())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPC.MaxReceiveMessageBytes != DefaultBase().GRPC.MaxReceiveMessageBytes || cfg.MemoryPipeline.WeibullMinMultiplier != 0 {
		t.Fatalf("defaults or valid zero changed: grpc=%+v pipeline=%+v", cfg.GRPC, cfg.MemoryPipeline)
	}
}

// TestLoadRejectsInvalidReferencedEnvNumber exercises the file expansion and environment override path.
// TestLoadRejectsInvalidReferencedEnvNumber 覆盖文件展开与环境变量覆盖链路中的非法数值。
func TestLoadRejectsInvalidReferencedEnvNumber(t *testing.T) {
	t.Setenv("VMM_PRE_CHECK_TOPK", "-2")
	path := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(path, []byte("pre_check:\n  top_k: ${VMM_PRE_CHECK_TOPK}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, newValidConfigForTest()); err == nil || !strings.Contains(err.Error(), "top_k") {
		t.Fatalf("invalid environment value was not rejected: %v", err)
	}
}
