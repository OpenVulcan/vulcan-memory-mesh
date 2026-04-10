// main.go implements the standalone PII rule tester CLI.
// main.go 用于实现独立的 PII 规则测试器命令行入口。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/pii"
)

// main executes the CLI bootstrap and dispatches to either file mode or ad-hoc mode.
// main 用于完成命令行启动，并分发到文件模式或 Ad-hoc 模式。
func main() {
	text := flag.String("text", "", "raw text to test against the PII rules")
	lang := flag.String("lang", "zh-CN", "language bundle to test")
	configArg := flag.String("config", "", "user config root (~/.vmm by default); explicit config files must use .yaml or .yml")
	pattern := flag.String("pattern", "", "single ad-hoc regexp pattern; enabling this switches the tester into ad-hoc mode")
	condition := flag.String("condition", "", "optional ad-hoc VM condition expression used with -pattern")
	replace := flag.String("replace", "[TEST_MASKED]", "replacement text used in ad-hoc mode")
	flag.Parse()

	if strings.TrimSpace(*text) == "" {
		fmt.Fprintln(os.Stderr, "missing required -text argument")
		flag.Usage()
		os.Exit(2)
	}

	var err error
	if strings.TrimSpace(*pattern) != "" {
		err = runAdHocMode(*text, *pattern, *condition, *replace)
	} else {
		err = runFileMode(*text, *lang, *configArg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", err)
		os.Exit(1)
	}
}

// runAdHocMode compiles one in-memory rule and evaluates it immediately without loading any JSON bundles.
// runAdHocMode 用于编译一条内存规则并立刻执行，而不加载任何 JSON 规则包。
func runAdHocMode(text, pattern, condition, replacement string) error {
	fmt.Println("[VMM PII TESTER] Ad-hoc Mode Triggered.")
	fmt.Printf("[-] Compiling Pattern: %s\n", pattern)
	if strings.TrimSpace(condition) == "" {
		fmt.Println("[-] Compiling Condition: <none>")
	} else {
		fmt.Printf("[-] Compiling Condition: %s\n", condition)
	}
	fmt.Println()
	fmt.Printf("=== ORIGINAL TEXT ===\n%s\n\n", text)

	engineLogger := logx.New(os.Stderr, logx.Config{Level: "error", Format: "text"})
	engine, err := pii.NewEngineWithRules("adhoc", engineLogger, []pii.RuleDefinition{
		{
			Name:        "Ad-hoc Rule",
			Pattern:     pattern,
			Replacement: replacement,
			Condition:   condition,
		},
	})
	if err != nil {
		return fmt.Errorf("compilation failed: %w", err)
	}
	engine.DebugMode = true

	result := engine.Scrub(text, "adhoc")
	fmt.Printf("=== FINAL RESULT ===\n%s\n", result)
	return nil
}

// runFileMode loads the fixed rule directories and evaluates the provided text using the selected language bundle.
// runFileMode 用于加载固定规则目录，并使用选中的语言规则包执行文本验证。
func runFileMode(text, lang, configArg string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working dir: %w", err)
	}

	layout, err := config.ResolvePromptLayout(exePath, wd, configArg, "local")
	if err != nil {
		return fmt.Errorf("resolve config layout: %w", err)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.DefaultLocal())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	engineLogger := logx.New(os.Stderr, logx.Config{Level: "error", Format: "text"})
	engine, err := pii.NewEngineWithLogger(layout.SystemPIIRulesDir(), layout.UserPIIRulesDir(), cfg.PII.DefaultLanguage, engineLogger)
	if err != nil {
		return fmt.Errorf("init pii engine: %w", err)
	}
	engine.DebugMode = true

	fmt.Printf("[VMM PII TESTER] Engine initialized. Loading [%s, %s] rules...\n", "common", lang)
	fmt.Printf("[VMM PII TESTER] System rules: %s\n", layout.SystemPIIRulesDir())
	fmt.Printf("[VMM PII TESTER] User rules: %s\n\n", layout.UserPIIRulesDir())
	fmt.Printf("=== ORIGINAL TEXT ===\n%s\n\n", text)

	result := engine.Scrub(text, lang)
	fmt.Printf("=== FINAL RESULT ===\n%s\n", result)
	return nil
}
