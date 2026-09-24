// validate.go exposes read-only validation for the PII rule layers used by runtime startup.
// validate.go 用于提供运行时启动所使用 PII 规则层的只读校验能力。
package pii

import (
	"fmt"
	"strings"
)

// ValidateRuleDirs loads, merges, and compiles the effective PII rule layers without creating an engine or changing process state.
// ValidateRuleDirs 在不创建引擎且不改变进程状态的前提下加载、合并并编译生效的 PII 规则层。
func ValidateRuleDirs(systemDir, userDir, defaultLang string) error {
	// Use the exact loader shared by NewEngineWithLogger so user rules keep precedence while missing user bundles fall back to system rules.
	// 使用与 NewEngineWithLogger 相同的加载器，保持用户规则优先以及用户规则缺失时回退系统规则的行为。
	loaded, err := loadRuleDirs(systemDir, userDir)
	if err != nil {
		return err
	}

	if len(loaded.languages) == 0 {
		return fmt.Errorf("no pii rules loaded from system=%s user=%s", strings.TrimSpace(systemDir), strings.TrimSpace(userDir))
	}

	language := normalizeLanguage(defaultLang)
	if language == "" {
		return nil
	}
	if _, ok := loaded.languages[language]; !ok {
		return fmt.Errorf("default pii language %q not found", language)
	}
	return nil
}
