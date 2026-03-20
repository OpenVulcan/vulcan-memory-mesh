// engine.go implements the language-aware PII scrubbing engine.
// engine.go 用于实现支持多语言切换的 PII 脱敏引擎。
package pii

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ruleFile represents one JSON rule bundle loaded from configs/pii_rules.
// ruleFile 用于表示从 configs/pii_rules 加载的一份 JSON 规则包。
type ruleFile struct {
	Language string      `json:"language"`
	Version  string      `json:"version"`
	Rules    []ruleEntry `json:"rules"`
	Excludes []string    `json:"excludes"`
}

// ruleEntry describes one regex replacement rule before it is compiled.
// ruleEntry 用于描述一条在编译前的正则替换规则。
type ruleEntry struct {
	Name        string `json:"name"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
}

// compiledRule stores one precompiled regex replacement rule used during scrub operations.
// compiledRule 用于保存一条在 Scrub 期间直接复用的预编译正则替换规则。
type compiledRule struct {
	name        string
	pattern     string
	replacement string
	regex       *regexp.Regexp
}

// languageRules groups all compiled rules and excludes for one language tag.
// languageRules 用于按语言标签收集全部编译后规则和排除字段。
type languageRules struct {
	language string
	version  string
	rules    []compiledRule
	excludes map[string]struct{}
}

// Engine keeps precompiled language rules and serves concurrent scrub requests safely.
// Engine 用于保存预编译好的多语言规则，并为并发 Scrub 请求提供线程安全访问。
type Engine struct {
	mu          sync.RWMutex
	defaultLang string
	languages   map[string]languageRules
}

// NewEngine loads system pii_rules first and then applies the optional user override directory.
// NewEngine 用于先加载系统 pii_rules，再叠加可选的用户覆盖目录。
func NewEngine(systemDir, userDir, defaultLang string) (*Engine, error) {
	engine := &Engine{
		defaultLang: normalizeLanguage(defaultLang),
		languages:   map[string]languageRules{},
	}
	if err := engine.LoadDirs(systemDir, userDir); err != nil {
		return nil, err
	}
	if engine.defaultLang == "" {
		for lang := range engine.languages {
			engine.defaultLang = lang
			break
		}
	}
	if engine.defaultLang == "" {
		return nil, fmt.Errorf("no pii rules loaded from system=%s user=%s", systemDir, userDir)
	}
	if _, ok := engine.languages[engine.defaultLang]; !ok {
		return nil, fmt.Errorf("default pii language %q not found", engine.defaultLang)
	}
	return engine, nil
}

// LoadDirs rescans the fixed system/user pii_rules directories and swaps the rule map atomically.
// LoadDirs 用于重新扫描固定的系统/用户 pii_rules 目录，并原子替换规则映射。
func (e *Engine) LoadDirs(systemDir, userDir string) error {
	loaded, err := loadRuleDirs(systemDir, userDir)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.languages = loaded
	if e.defaultLang == "" {
		for lang := range loaded {
			e.defaultLang = lang
			break
		}
	}
	return nil
}

// Scrub masks PII in one text according to the requested language, falling back when necessary.
// Scrub 用于按请求语言对文本中的 PII 做脱敏，并在必要时降级到默认语言。
func (e *Engine) Scrub(text string, lang string) string {
	langRules := e.lookup(lang)
	if text == "" || len(langRules.rules) == 0 {
		return text
	}
	scrubbed := text
	for _, rule := range langRules.rules {
		scrubbed = rule.regex.ReplaceAllString(scrubbed, rule.replacement)
	}
	return scrubbed
}

// lookup finds the requested language rule set or falls back to the default language.
// lookup 用于查找目标语言规则集，并在找不到时回退到默认语言。
func (e *Engine) lookup(lang string) languageRules {
	e.mu.RLock()
	defer e.mu.RUnlock()
	normalized := normalizeLanguage(lang)
	if rules, ok := e.languages[normalized]; ok {
		return rules
	}
	if rules, ok := e.languages[e.defaultLang]; ok {
		return rules
	}
	return languageRules{}
}

// loadRuleFile parses one JSON file and precompiles every regex before it enters the runtime map.
// loadRuleFile 用于解析单个 JSON 文件，并在进入运行时映射前预编译全部正则。
func loadRuleFile(path string) (languageRules, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return languageRules{}, fmt.Errorf("read pii rule file %s: %w", path, err)
	}
	var file ruleFile
	if err := json.Unmarshal(body, &file); err != nil {
		return languageRules{}, fmt.Errorf("parse pii rule file %s: %w", path, err)
	}
	lang := normalizeLanguage(file.Language)
	if lang == "" {
		return languageRules{}, fmt.Errorf("pii rule file %s missing language", path)
	}
	compiled := make([]compiledRule, 0, len(file.Rules))
	for _, item := range file.Rules {
		if strings.TrimSpace(item.Name) == "" {
			return languageRules{}, fmt.Errorf("pii rule file %s contains unnamed rule", path)
		}
		regex, err := regexp.Compile(item.Pattern)
		if err != nil {
			return languageRules{}, fmt.Errorf("compile pii rule %s from %s: %w", item.Name, path, err)
		}
		compiled = append(compiled, compiledRule{
			name:        item.Name,
			pattern:     item.Pattern,
			replacement: item.Replacement,
			regex:       regex,
		})
	}
	excludes := map[string]struct{}{}
	for _, item := range file.Excludes {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		excludes[item] = struct{}{}
	}
	return languageRules{
		language: lang,
		version:  strings.TrimSpace(file.Version),
		rules:    compiled,
		excludes: excludes,
	}, nil
}

// loadRuleDirs loads the built-in pii_rules directory first and then applies user overrides by language.
// loadRuleDirs 用于先加载内置 pii_rules 目录，再按语言标签叠加用户覆盖规则。
func loadRuleDirs(systemDir, userDir string) (map[string]languageRules, error) {
	systemDir = strings.TrimSpace(systemDir)
	if systemDir == "" {
		return nil, fmt.Errorf("system pii rules dir is required")
	}

	systemRules, err := loadRuleDir(systemDir, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userDir) == "" {
		return systemRules, nil
	}

	userRules, err := loadRuleDir(userDir, false)
	if err != nil {
		return nil, err
	}
	for lang, rules := range userRules {
		systemRules[lang] = rules
	}
	return systemRules, nil
}

// loadRuleDir loads and compiles one rule directory, optionally requiring at least one JSON file.
// loadRuleDir 用于加载并编译单个规则目录，并可选要求目录中至少存在一个 JSON 文件。
func loadRuleDir(dir string, required bool) (map[string]languageRules, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		if required {
			return nil, fmt.Errorf("pii rules dir is required")
		}
		return map[string]languageRules{}, nil
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("scan pii rules in %s: %w", dir, err)
	}
	if len(matches) == 0 {
		if required {
			return nil, fmt.Errorf("no pii rule files found in %s", dir)
		}
		return map[string]languageRules{}, nil
	}

	loaded := make(map[string]languageRules, len(matches))
	for _, path := range matches {
		rules, err := loadRuleFile(path)
		if err != nil {
			return nil, err
		}
		loaded[rules.language] = rules
	}
	return loaded, nil
}

// normalizeLanguage normalizes Accept-Language style inputs into one map key.
// normalizeLanguage 用于把 Accept-Language 风格输入归一成一个映射键。
func normalizeLanguage(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ""
	}
	first := strings.Split(lang, ",")[0]
	first = strings.TrimSpace(first)
	first = strings.Split(first, ";")[0]
	return first
}
