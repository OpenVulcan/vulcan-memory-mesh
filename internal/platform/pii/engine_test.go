// engine_test.go implements integration and concurrency tests for the PII engine.
// engine_test.go 用于实现 PII 引擎的集成与并发测试。
package pii

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/platform/logx"
)

const zhCNStreetAddressTemplatePattern = `((?:[A-Za-z0-9]{2,20}|[\p{Han}]{2,10})(?:路|街|巷|弄|胡同|大道|道))((?:(?:\s*[0-9A-Za-z甲乙丙丁一二三四五六七八九十-]{1,8}\s*(?:号|栋|幢|楼|座)){1,3}(?:\s*[0-9A-Za-z甲乙丙丁一二三四五六七八九十-]{1,8}\s*(?:单元|室|户)){1,2}(?:\s*\d{1,4}(?:室|户)?)?|(?:\s*[0-9A-Za-z甲乙丙丁一二三四五六七八九十-]{1,8}\s*(?:号|栋|幢|楼|座)){1,4}(?:\s*[0-9A-Za-z甲乙丙丁一二三四五六七八九十-]{1,8}\s*(?:单元|室|户)){0,2}(?:\s*\d{1,3}\b)?))`
const contextSecretNamesPattern = `(?:access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|auth[_-]?token|private[_-]?token|api[_-]?key|x[_-]?api[_-]?key|access[_-]?key|secret[_-]?access[_-]?key|private[_-]?key|client[_-]?secret|signing[_-]?secret|webhook[_-]?secret|secret(?:[_-]?key)?|token|password|passwd|pwd)`

// TestEngineScrubMasksKnownZhCNPII verifies the TestEngineScrubMasksKnownZhCNPII behavior.
// TestEngineScrubMasksKnownZhCNPII 用于验证 TestEngineScrubMasksKnownZhCNPII 行为。
func TestEngineScrubMasksKnownZhCNPII(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Bearer_Token", "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})", "replacement": "Bearer [TOKEN_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("你好，我的电话是 13800138000，头信息是 Bearer abcdefghijklmnop", "zh-CN")
	want := "你好，我的电话是 [MOBILE_MASKED]，头信息是 Bearer [TOKEN_MASKED]"
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestEngineLanguageRulesOverrideCommonByName verifies that one language bundle can replace a common rule by name.
// TestEngineLanguageRulesOverrideCommonByName 用于验证语言规则可以按名称覆盖公共规则。
func TestEngineLanguageRulesOverrideCommonByName(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Bearer_Token", "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})", "replacement": "Bearer [COMMON_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Bearer_Token", "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})", "replacement": "Bearer [ZH_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("Bearer abcdefghijklmnop", "zh-CN")
	if got != "Bearer [ZH_MASKED]" {
		t.Fatalf("scrubbed text = %q", got)
	}
}

// TestEngineFallsBackToDefaultLanguage verifies the TestEngineFallsBackToDefaultLanguage behavior.
// TestEngineFallsBackToDefaultLanguage 用于验证 TestEngineFallsBackToDefaultLanguage 行为。
func TestEngineFallsBackToDefaultLanguage(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[SK_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
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
	writeRuleFile(t, filepath.Join(systemRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[SYSTEM_SK_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[SYSTEM_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(userRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[USER_SK_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(userRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[USER_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, userRulesDir, "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("电话 13800138000", "zh-CN")
	if got != "电话 [USER_MASKED]" {
		t.Fatalf("scrubbed text = %q", got)
	}
	got = engine.Scrub("密钥 sk-abcdefghijklmnopqrstuvwxyz123456", "zh-CN")
	if got != "密钥 [USER_SK_MASKED]" {
		t.Fatalf("common override scrubbed text = %q", got)
	}
}

// TestEngineUserLanguageBundleReplacesSystemLanguageBundle verifies that a user language file replaces the system language file instead of merging with it.
// TestEngineUserLanguageBundleReplacesSystemLanguageBundle 用于验证用户语言文件会替换系统语言文件，而不是与其合并。
func TestEngineUserLanguageBundleReplacesSystemLanguageBundle(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	userRulesDir := filepath.Join(root, "user", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "\\b1[3-9]\\d{9}\\b", "replacement": "[SYSTEM_MOBILE_MASKED]" },
	    { "name": "CN_ID_PRO", "pattern": "\\b(\\d{17})([0-9Xx])\\b", "replacement": "[ID_VERIFIED]", "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(userRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "\\b1[3-9]\\d{9}\\b", "replacement": "[USER_MOBILE_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, userRulesDir, "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("身份证号 11010519491231002X，电话 13800138000", "zh-CN")
	want := "身份证号 11010519491231002X，电话 [USER_MOBILE_MASKED]"
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestEngineUserCommonBundleReplacesSystemCommonBundle verifies that a user common file replaces the system common file instead of merging with it.
// TestEngineUserCommonBundleReplacesSystemCommonBundle 用于验证用户公共规则文件会替换系统公共规则文件，而不是与其合并。
func TestEngineUserCommonBundleReplacesSystemCommonBundle(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	userRulesDir := filepath.Join(root, "user", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[SYSTEM_SK_MASKED]" },
	    { "name": "Bearer_Token", "pattern": "Bearer\\s+([A-Za-z0-9._+=-]{10,})", "replacement": "Bearer [SYSTEM_TOKEN_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.0.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "\\b1[3-9]\\d{9}\\b", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(userRulesDir, "common.json"), `{
	  "language": "common",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "OpenAI_Key", "pattern": "sk-[A-Za-z0-9]{20,}", "replacement": "[USER_SK_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, userRulesDir, "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("密钥 sk-abcdefghijklmnopqrstuvwxyz123456，头信息 Bearer abcdefghijklmnop", "zh-CN")
	want := "密钥 [USER_SK_MASKED]，头信息 Bearer abcdefghijklmnop"
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestEngineCommonBareSecretRuleMasksPasswordAssignments verifies that bare password-like assignments are masked consistently with the quoted secret rules.
// TestEngineCommonBareSecretRuleMasksPasswordAssignments 用于验证裸值 password 类赋值会与带引号 secret 规则保持一致地被脱敏。
func TestEngineCommonBareSecretRuleMasksPasswordAssignments(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")

	commonBundle, err := json.Marshal(ruleFile{
		Language: "common",
		Version:  "2.0.0",
		Rules: []ruleEntry{
			{
				Name:        "CONTEXT_SECRET_BARE",
				Pattern:     `(?i)(["']?(?:` + contextSecretNamesPattern + `)["']?)(\s*[:=]\s*)([A-Za-z0-9._~+/=-]{12,})`,
				Replacement: `$1$2[SECRET_VALUE_MASKED]`,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeRuleFile(t, filepath.Join(systemRulesDir, "common.json"), string(commonBundle))

	langBundle, err := json.Marshal(ruleFile{
		Language: "en-US",
		Version:  "2.0.0",
		Rules:    []ruleEntry{},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeRuleFile(t, filepath.Join(systemRulesDir, "en-US.json"), string(langBundle))

	engine, err := NewEngineWithLogger(systemRulesDir, "", "en-US", silentLogger())
	if err != nil {
		t.Fatal(err)
	}

	text := "password = supersecretvalue123; passwd = anotherSecret456; pwd=thirdSecret789"
	want := "password = [SECRET_VALUE_MASKED]; passwd = [SECRET_VALUE_MASKED]; pwd=[SECRET_VALUE_MASKED]"
	got := engine.Scrub(text, "en-US")
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestEngineNorthAmericanPhoneRulesMaskInternationalAndParenthesizedFormats verifies that en-US and en-CA phone rules both cover +1 and parenthesized area-code formats.
// TestEngineNorthAmericanPhoneRulesMaskInternationalAndParenthesizedFormats 用于验证 en-US 和 en-CA 电话规则都能覆盖 +1 与括号区号格式。
func TestEngineNorthAmericanPhoneRulesMaskInternationalAndParenthesizedFormats(t *testing.T) {
	cases := []struct {
		name        string
		language    string
		ruleName    string
		replacement string
		want        string
	}{
		{
			name:        "en-US",
			language:    "en-US",
			ruleName:    "US_PHONE_NUMBER",
			replacement: "[US_PHONE_MASKED]",
			want:        "Call me at [US_PHONE_MASKED] or [US_PHONE_MASKED].",
		},
		{
			name:        "en-CA",
			language:    "en-CA",
			ruleName:    "CA_PHONE_NUMBER",
			replacement: "[CA_PHONE_MASKED]",
			want:        "Call me at [CA_PHONE_MASKED] or [CA_PHONE_MASKED].",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			systemRulesDir := filepath.Join(root, "system", "pii_rules")

			langBundle, err := json.Marshal(ruleFile{
				Language: tc.language,
				Version:  "2.0.0",
				Rules: []ruleEntry{
					{
						Name:        tc.ruleName,
						Pattern:     `(?:\+1[ -]?)?(?:\([2-9]\d{2}\)|[2-9]\d{2})[ -]?[2-9]\d{2}[ -]?\d{4}\b`,
						Replacement: tc.replacement,
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			writeRuleFile(t, filepath.Join(systemRulesDir, tc.language+".json"), string(langBundle))

			engine, err := NewEngineWithLogger(systemRulesDir, "", tc.language, silentLogger())
			if err != nil {
				t.Fatal(err)
			}

			got := engine.Scrub("Call me at +1 212-555-1234 or (212) 555-1234.", tc.language)
			if got != tc.want {
				t.Fatalf("scrubbed text = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEngineConditionKeepsFalsePositive verifies the TestEngineConditionKeepsFalsePositive behavior.
// TestEngineConditionKeepsFalsePositive 用于验证 TestEngineConditionKeepsFalsePositive 行为。
func TestEngineConditionKeepsFalsePositive(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "CN_ID_PRO", "pattern": "\\b(\\d{17})([0-9Xx])\\b", "replacement": "[ID_VERIFIED]", "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	invalid := "身份证号 110105194912310021"
	got := engine.Scrub(invalid, "zh-CN")
	if got != invalid {
		t.Fatalf("scrubbed text = %q, want original text kept", got)
	}
}

// TestEngineConditionMasksVerifiedID verifies the TestEngineConditionMasksVerifiedID behavior.
// TestEngineConditionMasksVerifiedID 用于验证 TestEngineConditionMasksVerifiedID 行为。
func TestEngineConditionMasksVerifiedID(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "CN_ID_PRO", "pattern": "\\b(\\d{17})([0-9Xx])\\b", "replacement": "[ID_VERIFIED]", "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("身份证号 11010519491231002X", "zh-CN")
	if got != "身份证号 [ID_VERIFIED]" {
		t.Fatalf("scrubbed text = %q", got)
	}
}

// TestEngineMobileRuleDoesNotMaskInsideLongDigitStrings verifies the TestEngineMobileRuleDoesNotMaskInsideLongDigitStrings behavior.
// TestEngineMobileRuleDoesNotMaskInsideLongDigitStrings 用于验证 TestEngineMobileRuleDoesNotMaskInsideLongDigitStrings 行为。
func TestEngineMobileRuleDoesNotMaskInsideLongDigitStrings(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "\\b1[3-9]\\d{9}\\b", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("身份证 110105194912310021", "zh-CN")
	if got != "身份证 110105194912310021" {
		t.Fatalf("scrubbed text = %q, want original long digit string", got)
	}
}

// TestEngineLogsSafeHashOnly verifies the TestEngineLogsSafeHashOnly behavior.
// TestEngineLogsSafeHashOnly 用于验证 TestEngineLogsSafeHashOnly 行为。
func TestEngineLogsSafeHashOnly(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)

	var logBuf bytes.Buffer
	logger := logx.New(&logBuf, logx.Config{Level: "info", Format: "text"})
	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", logger)
	if err != nil {
		t.Fatal(err)
	}
	original := "我的手机号是 13800138000"
	_ = engine.Scrub(original, "zh-CN")

	logText := logBuf.String()
	if !strings.Contains(logText, "[PII-EVAL]") {
		t.Fatalf("expected pii evaluator log, got %q", logText)
	}
	if strings.Contains(logText, "13800138000") {
		t.Fatalf("log leaked original match text: %q", logText)
	}
	if !strings.Contains(logText, "match_hash") {
		t.Fatalf("expected match hash in log, got %q", logText)
	}
}

// TestEngineDebugModePrintsVerboseTrace verifies that debug mode prints raw match and capture groups only when explicitly enabled.
// TestEngineDebugModePrintsVerboseTrace 用于验证调试模式仅在显式开启时才打印原始匹配和捕获组轨迹。
func TestEngineDebugModePrintsVerboseTrace(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "CN_ID_PRO", "pattern": "\\b(\\d{17})([0-9Xx])\\b", "replacement": "[ID_VERIFIED]", "condition": "(weight_sum($1, [7,9,10,5,8,4,2,1,6,3,7,9,10,5,8,4,2]) % 11) == cn_check($2)" }
	  ]
	}`)

	var debugBuf bytes.Buffer
	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	engine.DebugMode = true
	engine.debugOutput = &debugBuf

	got := engine.Scrub("身份证号 11010519491231002X", "zh-CN")
	if got != "身份证号 [ID_VERIFIED]" {
		t.Fatalf("scrubbed text = %q", got)
	}

	trace := debugBuf.String()
	if !strings.Contains(trace, "=== Trace: CN_ID_PRO ===") {
		t.Fatalf("missing trace header: %q", trace)
	}
	if !strings.Contains(trace, "Regex Matched: 11010519491231002X") {
		t.Fatalf("missing full match in debug trace: %q", trace)
	}
	if !strings.Contains(trace, "$0: 11010519491231002X") {
		t.Fatalf("missing $0 in debug trace: %q", trace)
	}
	if !strings.Contains(trace, "$1: 11010519491231002") || !strings.Contains(trace, "$2: X") {
		t.Fatalf("missing capture groups in debug trace: %q", trace)
	}
	if !strings.Contains(trace, "VM Evaluated: TRUE") {
		t.Fatalf("missing vm result in debug trace: %q", trace)
	}
}

// TestNewEngineWithRulesRunsAdHocRules verifies that in-memory rule injection works without loading JSON bundles.
// TestNewEngineWithRulesRunsAdHocRules 用于验证内存规则注入可以在不加载 JSON 规则包的情况下直接运行。
func TestNewEngineWithRulesRunsAdHocRules(t *testing.T) {
	engine, err := NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{
			Name:        "Ad-hoc Rule",
			Pattern:     "\\b\\d{4}-\\d{4}\\b",
			Replacement: "[SECRET]",
			Condition:   "len($0) == 9",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("我的代号是 8888-9999", "adhoc")
	if got != "我的代号是 [SECRET]" {
		t.Fatalf("scrubbed text = %q", got)
	}
}

// TestNewEngineWithRulesSupportsCaptureGroupReplacement verifies that ad-hoc rules can preserve one captured prefix during replacement.
// TestNewEngineWithRulesSupportsCaptureGroupReplacement 用于验证 Ad-hoc 规则可以在替换时保留指定捕获前缀。
func TestNewEngineWithRulesSupportsCaptureGroupReplacement(t *testing.T) {
	engine, err := NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{
			Name:        "Street Prefix",
			Pattern:     `([\p{Han}]+大街)(.*)`,
			Replacement: `$1[MASKED]`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := engine.Scrub("北京市海淀区中关村大街100号8栋2单元101室", "adhoc")
	want := "北京市海淀区中关村大街[MASKED]"
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestNewEngineWithRulesMasksZhCNAddressSuffixOnly verifies that address rules can keep the street prefix while masking the doorplate suffix.
// TestNewEngineWithRulesMasksZhCNAddressSuffixOnly 用于验证中文地址规则可以保留街道前缀，仅掩码门牌后缀。
func TestNewEngineWithRulesMasksZhCNAddressSuffixOnly(t *testing.T) {
	engine, err := NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{
			Name:        "CN Street Address",
			Pattern:     zhCNStreetAddressTemplatePattern,
			Replacement: `$1[SPECIFIC_ADDRESS_MASKED]`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "long han prefix and unit room suffix",
			text: "北京市海淀区中关村大街100号8栋2单元101室",
			want: "北京市海淀区中关村大街[SPECIFIC_ADDRESS_MASKED]",
		},
		{
			name: "bare room number after building",
			text: "上海市浦东新区世纪大道100号8栋 101。",
			want: "上海市浦东新区世纪大道[SPECIFIC_ADDRESS_MASKED]。",
		},
		{
			name: "four digit tail remains outside the mask",
			text: "上海市浦东新区世纪大道100号 2024 年活动",
			want: "上海市浦东新区世纪大道[SPECIFIC_ADDRESS_MASKED] 2024 年活动",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.Scrub(tc.text, "adhoc")
			if got != tc.want {
				t.Fatalf("scrubbed text = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewEngineWithRulesSupportsContextPreservingSecretTemplates verifies that replacement templates can preserve non-sensitive context for secrets.
// TestNewEngineWithRulesSupportsContextPreservingSecretTemplates 用于验证 replacement 模板可以在脱敏密钥时保留非敏感上下文。
func TestNewEngineWithRulesSupportsContextPreservingSecretTemplates(t *testing.T) {
	engine, err := NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{
			Name:        "Bearer Token",
			Pattern:     `(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]{20,}`,
			Replacement: `$1[BEARER_TOKEN_MASKED]`,
		},
		{
			Name:        "URL Basic Auth",
			Pattern:     `([A-Za-z][A-Za-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`,
			Replacement: `$1[BASIC_AUTH_MASKED]@`,
		},
		{
			Name:        "Context Secret Double Quoted",
			Pattern:     `(?i)(["']?` + contextSecretNamesPattern + `["']?)(\s*[:=]\s*)(")((?:\$\{[^}\r\n"]+\}[^\r\n"]+|[^$\r\n"][^\r\n"]{3,}|\$[^\{\r\n"][^\r\n"]{2,}))(")`,
			Replacement: `$1$2$3[SECRET_VALUE_MASKED]$5`,
		},
		{
			Name:        "Context Secret Single Quoted",
			Pattern:     `(?i)(["']?` + contextSecretNamesPattern + `["']?)(\s*[:=]\s*)(')((?:\$\{[^}\r\n']+\}[^\r\n']+|[^$\r\n'][^\r\n']{3,}|\$[^\{\r\n'][^\r\n']{2,}))(')`,
			Replacement: `$1$2$3[SECRET_VALUE_MASKED]$5`,
		},
		{
			Name:        "Context Secret Bare",
			Pattern:     `(?i)(["']?` + contextSecretNamesPattern + `["']?)(\s*[:=]\s*)([A-Za-z0-9._~+/=-]{12,})`,
			Replacement: `$1$2[SECRET_VALUE_MASKED]`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	text := "Authorization: Bearer abcdefghijklmnopqrstuvwx; https://alice:s3cr3t@example.com/path; token = \"supersecretvalue\"; api_key='anotherSecret123'; access_token = abcdefghijklmnop; secret_access_key = \"awsSecretLikeValue123\"; x-api-key='gatewayKeyValue456'; session_token = sessiontokenvalue789; token = \"${OPENAI_API_KEY}\"; password='${DB_PASSWORD}'; private_key=\"$abc12345\"; client_id = \"public-client-id\""
	got := engine.Scrub(text, "adhoc")
	want := "Authorization: Bearer [BEARER_TOKEN_MASKED]; https://[BASIC_AUTH_MASKED]@example.com/path; token = \"[SECRET_VALUE_MASKED]\"; api_key='[SECRET_VALUE_MASKED]'; access_token = [SECRET_VALUE_MASKED]; secret_access_key = \"[SECRET_VALUE_MASKED]\"; x-api-key='[SECRET_VALUE_MASKED]'; session_token = [SECRET_VALUE_MASKED]; token = \"${OPENAI_API_KEY}\"; password='${DB_PASSWORD}'; private_key=\"[SECRET_VALUE_MASKED]\"; client_id = \"public-client-id\""
	if got != want {
		t.Fatalf("scrubbed text = %q, want %q", got, want)
	}
}

// TestNewEngineWithRulesReturnsCompileErrors verifies that invalid ad-hoc regex or DSL input fails fast.
// TestNewEngineWithRulesReturnsCompileErrors 用于验证非法的 Ad-hoc 正则或 DSL 会立刻失败。
func TestNewEngineWithRulesReturnsCompileErrors(t *testing.T) {
	_, err := NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{Name: "Broken Rule", Pattern: "[a-", Replacement: "[X]"},
	})
	if err == nil || !strings.Contains(err.Error(), "compile pii rule") {
		t.Fatalf("expected regex compile error, got %v", err)
	}

	_, err = NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{Name: "Broken Rule", Pattern: "\\d+", Replacement: "[X]", Condition: "unknown_func($0)"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown function") {
		t.Fatalf("expected condition compile error, got %v", err)
	}

	_, err = NewEngineWithRules("adhoc", silentLogger(), []RuleDefinition{
		{Name: "Broken Rule", Pattern: "(\\d+)", Replacement: "$2[X]"},
	})
	if err == nil || !strings.Contains(err.Error(), "compile replacement") {
		t.Fatalf("expected replacement compile error, got %v", err)
	}
}

// TestEngineDebugModeOffKeepsVerboseTraceSilent verifies that production mode never emits raw-match traces.
// TestEngineDebugModeOffKeepsVerboseTraceSilent 用于验证生产模式绝不会输出包含原始匹配的调试轨迹。
func TestEngineDebugModeOffKeepsVerboseTraceSilent(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "\\b1[3-9]\\d{9}\\b", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)

	var debugBuf bytes.Buffer
	engine, err := NewEngineWithLogger(systemRulesDir, "", "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	engine.debugOutput = &debugBuf

	got := engine.Scrub("电话 13800138000", "zh-CN")
	if got != "电话 [MOBILE_MASKED]" {
		t.Fatalf("scrubbed text = %q", got)
	}
	if debugBuf.Len() != 0 {
		t.Fatalf("expected no debug output when DebugMode is false, got %q", debugBuf.String())
	}
}

// TestEngineConcurrentScrubAndReloadDoesNotDeadlock verifies the TestEngineConcurrentScrubAndReloadDoesNotDeadlock behavior.
// TestEngineConcurrentScrubAndReloadDoesNotDeadlock 用于验证 TestEngineConcurrentScrubAndReloadDoesNotDeadlock 行为。
func TestEngineConcurrentScrubAndReloadDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	systemRulesDir := filepath.Join(root, "system", "pii_rules")
	userRulesDir := filepath.Join(root, "user", "pii_rules")
	writeRuleFile(t, filepath.Join(systemRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[MOBILE_MASKED]" }
	  ]
	}`)
	writeRuleFile(t, filepath.Join(userRulesDir, "zh-CN.json"), `{
	  "language": "zh-CN",
	  "version": "1.1.1",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[USER_MASKED]" }
	  ]
	}`)

	engine, err := NewEngineWithLogger(systemRulesDir, userRulesDir, "zh-CN", silentLogger())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					_ = engine.Scrub("电话 13800138000", "zh-CN")
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := engine.LoadDirs(systemRulesDir, userRulesDir); err != nil {
					t.Errorf("reload rules: %v", err)
					return
				}
			}
		}()
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent scrub and reload timed out, possible deadlock")
	}
}

// TestLoadRuleFileRejectsDuplicateRuleNames verifies that one bundle cannot define the same rule twice.
// TestLoadRuleFileRejectsDuplicateRuleNames 用于验证同一规则包内不允许重复定义同名规则。
func TestLoadRuleFileRejectsDuplicateRuleNames(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pii_rules", "zh-CN.json")
	writeRuleFile(t, path, `{
	  "language": "zh-CN",
	  "version": "1.1.0",
	  "rules": [
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[A]" },
	    { "name": "Mobile", "pattern": "1[3-9]\\d{9}", "replacement": "[B]" }
	  ]
	}`)

	_, err := loadRuleFile(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate rule name") {
		t.Fatalf("expected duplicate rule name error, got %v", err)
	}
}

// writeRuleFile writes one test JSON rule file to disk.
// writeRuleFile 用于把一份测试 JSON 规则文件写入磁盘。
func writeRuleFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// silentLogger creates one logger suitable for tests that do not care about log output.
// silentLogger 用于创建一个适合无需关心日志输出的测试日志器。
func silentLogger() *logx.Logger {
	return logx.New(&bytes.Buffer{}, logx.Config{Level: "error", Format: "text"})
}
