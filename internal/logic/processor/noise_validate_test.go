// noise_validate_test.go verifies read-only noise rule compilation and layered precedence.
// noise_validate_test.go 用于验证噪声规则只读编译及分层优先级。
package processor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateNoiseRulesAcceptsUserOverrideAndSystemFallback verifies the validator compiles the same effective bundles used by the gate.
// TestValidateNoiseRulesAcceptsUserOverrideAndSystemFallback 验证校验器编译与门控器相同的生效规则，并保持用户优先和系统回退。
func TestValidateNoiseRulesAcceptsUserOverrideAndSystemFallback(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeNoiseRuleFile(t, filepath.Join(systemDir, "common.json"), `{"language":"common","categories":[{"name":"system","targets":["assistant"],"patterns":["system"]}]}`)
	writeNoiseRuleFile(t, filepath.Join(systemDir, "zh-CN.json"), `{"language":"zh-CN","categories":[{"name":"language","targets":["user"],"patterns":["language"]}]}`)
	writeNoiseRuleFile(t, filepath.Join(userDir, "common.json"), `{"language":"common","categories":[{"name":"system","targets":["assistant"],"patterns":["user"]}]}`)

	if err := ValidateNoiseRules(systemDir, userDir, "zh-CN", 0); err != nil {
		t.Fatalf("validate noise rules: %v", err)
	}
}

// TestValidateNoiseRulesRejectsMalformedSelectedUserRule verifies an invalid user rule is not hidden by a valid system fallback.
// TestValidateNoiseRulesRejectsMalformedSelectedUserRule 验证无效用户规则不会被有效系统回退静默掩盖。
func TestValidateNoiseRulesRejectsMalformedSelectedUserRule(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeNoiseRuleFile(t, filepath.Join(systemDir, "common.json"), `{"language":"common","categories":[{"name":"system","targets":["assistant"],"patterns":["system"]}]}`)
	writeNoiseRuleFile(t, filepath.Join(systemDir, "zh-CN.json"), `{"language":"zh-CN","categories":[]}`)
	writeNoiseRuleFile(t, filepath.Join(userDir, "zh-CN.json"), `{"language":"zh-CN","categories":[{"name":"bad","targets":["user"],"patterns":["["]}]}`)

	err := ValidateNoiseRules(systemDir, userDir, "zh-CN", 0.88)
	if err == nil || !strings.Contains(err.Error(), "compile noise pattern") {
		t.Fatalf("expected malformed-user-rule error, got %v", err)
	}
}

// TestValidateNoiseRulesDoesNotRequireEmbeddingClient verifies the read-only validator can run without runtime AI dependencies.
// TestValidateNoiseRulesDoesNotRequireEmbeddingClient 验证只读校验器不需要运行时 AI 依赖即可执行。
func TestValidateNoiseRulesDoesNotRequireEmbeddingClient(t *testing.T) {
	systemDir := t.TempDir()
	writeNoiseRuleFile(t, filepath.Join(systemDir, "zh-CN.json"), `{"language":"zh-CN","categories":[{"name":"language","targets":["user"],"phrases":["hello"]}]}`)

	if err := ValidateNoiseRules(systemDir, "", "zh-CN", 0.88); err != nil {
		t.Fatalf("validate noise rules without embedding client: %v", err)
	}
}

// writeNoiseRuleFile creates one test-only noise rule file without connecting to a database or network service.
// writeNoiseRuleFile 创建测试用噪声规则文件，不连接数据库或网络服务。
func writeNoiseRuleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
