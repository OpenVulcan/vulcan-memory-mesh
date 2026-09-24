// validate_test.go verifies read-only PII rule validation and layered precedence.
// validate_test.go 用于验证 PII 规则只读校验及分层优先级。
package pii

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRuleDirsAcceptsUserOverrideAndSystemFallback verifies the effective PII layers compile with user precedence and system fallback.
// TestValidateRuleDirsAcceptsUserOverrideAndSystemFallback 验证用户规则优先且缺失用户语言包时回退系统规则。
func TestValidateRuleDirsAcceptsUserOverrideAndSystemFallback(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writePIIRuleFile(t, filepath.Join(systemDir, "common.json"), `{"language":"common","rules":[{"name":"token","pattern":"token","replacement":"[MASKED]"}]}`)
	writePIIRuleFile(t, filepath.Join(systemDir, "zh-CN.json"), `{"language":"zh-CN","rules":[{"name":"phone","pattern":"phone","replacement":"[PHONE]"}]}`)
	writePIIRuleFile(t, filepath.Join(userDir, "common.json"), `{"language":"common","rules":[{"name":"token","pattern":"token","replacement":"[USER_MASKED]"}]}`)

	if err := ValidateRuleDirs(systemDir, userDir, "zh-CN"); err != nil {
		t.Fatalf("validate pii rules: %v", err)
	}
}

// TestValidateRuleDirsRejectsMissingDefaultLanguage verifies a configured language must exist after layered merging.
// TestValidateRuleDirsRejectsMissingDefaultLanguage 验证分层合并后必须存在配置指定的默认语言。
func TestValidateRuleDirsRejectsMissingDefaultLanguage(t *testing.T) {
	systemDir := t.TempDir()
	writePIIRuleFile(t, filepath.Join(systemDir, "common.json"), `{"language":"common","rules":[]}`)
	writePIIRuleFile(t, filepath.Join(systemDir, "en-US.json"), `{"language":"en-US","rules":[]}`)

	err := ValidateRuleDirs(systemDir, "", "zh-CN")
	if err == nil || !strings.Contains(err.Error(), "default pii language") {
		t.Fatalf("expected missing-language error, got %v", err)
	}
}

// TestValidateRuleDirsRejectsMalformedSelectedUserRule verifies an invalid user override is reported instead of silently falling back.
// TestValidateRuleDirsRejectsMalformedSelectedUserRule 验证无效用户覆盖会报错，而不会静默回退到系统规则。
func TestValidateRuleDirsRejectsMalformedSelectedUserRule(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writePIIRuleFile(t, filepath.Join(systemDir, "zh-CN.json"), `{"language":"zh-CN","rules":[]}`)
	writePIIRuleFile(t, filepath.Join(userDir, "zh-CN.json"), `{"language":"zh-CN","rules":[{"name":"bad","pattern":"[","replacement":"x"}]}`)

	err := ValidateRuleDirs(systemDir, userDir, "zh-CN")
	if err == nil || !strings.Contains(err.Error(), "compile pii rule") {
		t.Fatalf("expected malformed-user-rule error, got %v", err)
	}
}

// writePIIRuleFile creates one test-only PII rule file without involving runtime configuration or external state.
// writePIIRuleFile 创建测试用 PII 规则文件，不触碰运行时配置或外部状态。
func writePIIRuleFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
