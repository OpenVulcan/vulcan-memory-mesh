// engine_test.go implements the language-aware PII scrubbing engine tests.
// engine_test.go 用于实现多语言 PII 脱敏引擎测试。
package pii

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEngineScrubMasksKnownZhCNPII verifies the TestEngineScrubMasksKnownZhCNPII behavior.
// TestEngineScrubMasksKnownZhCNPII 用于验证 TestEngineScrubMasksKnownZhCNPII 行为。
func TestEngineScrubMasksKnownZhCNPII(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	if err := os.MkdirAll(systemRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[MOBILE_MASKED]" },
	    { "name": "Bearer_Token", "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})", "replacement": "Bearer [TOKEN_MASKED]" }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(systemRulesDir, "zh-CN.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(systemRulesDir, "", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("你好，我的电话是 13800138000，头信息是 Bearer abcdefghijklmnop", "zh-CN")
	want := "你好，我的电话是 [MOBILE_MASKED]，头信息是 Bearer [TOKEN_MASKED]"
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestEngineFallsBackToDefaultLanguage verifies the TestEngineFallsBackToDefaultLanguage behavior.
// TestEngineFallsBackToDefaultLanguage 用于验证 TestEngineFallsBackToDefaultLanguage 行为。
func TestEngineFallsBackToDefaultLanguage(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	if err := os.MkdirAll(systemRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[SK_MASKED]" }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(systemRulesDir, "zh-CN.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(systemRulesDir, "", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("my key is sk-abcdefghijklmnopqrstuvwxyz123456", "en-US")
	if got != "my key is [SK_MASKED]" {
		t.Fatalf("fallback scrubbed text = %q", got)
	}
}

// TestEngineUserRulesOverrideSystemRules verifies the TestEngineUserRulesOverrideSystemRules behavior.
// TestEngineUserRulesOverrideSystemRules 用于验证 TestEngineUserRulesOverrideSystemRules 行为。
func TestEngineUserRulesOverrideSystemRules(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	userRulesDir := filepath.Join(root, "user", "pii_rules")
	if err := os.MkdirAll(systemRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	systemBody := `{
	  "language": "zh-CN",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[SYSTEM_MASKED]" }
	  ]
	}`
	userBody := `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[USER_MASKED]" }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(systemRulesDir, "zh-CN.json"), []byte(systemBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userRulesDir, "zh-CN.json"), []byte(userBody), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(systemRulesDir, userRulesDir, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("电话 13800138000", "zh-CN")
	if got != "电话 [USER_MASKED]" {
		t.Fatalf("scrubbed text = %q", got)
	}
}
