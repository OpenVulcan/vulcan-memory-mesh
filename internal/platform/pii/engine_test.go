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
	rulesDir := filepath.Join(root, "pii_rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(rulesDir, "zh-CN.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(rulesDir, "zh-CN")
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
	rulesDir := filepath.Join(root, "pii_rules")
	if err := os.MkdirAll(rulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[SK_MASKED]" }
	  ]
	}`
	if err := os.WriteFile(filepath.Join(rulesDir, "zh-CN.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := NewEngine(rulesDir, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("my key is sk-abcdefghijklmnopqrstuvwxyz123456", "en-US")
	if got != "my key is [SK_MASKED]" {
		t.Fatalf("fallback scrubbed text = %q", got)
	}
}
