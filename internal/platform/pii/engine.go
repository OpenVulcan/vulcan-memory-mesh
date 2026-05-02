// engine.go implements the language-aware PII scrubbing engine.
// engine.go 用于实现支持多语言切换的 PII 脱敏引擎。
package pii

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/openvulcan/vmm/internal/platform/logx"
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
	Condition   string `json:"condition,omitempty"`
}

// RuleDefinition describes one in-memory rule used by tools such as the standalone tester.
// RuleDefinition 用于描述一条由独立工具直接在内存中构造的规则。
type RuleDefinition struct {
	Name        string
	Pattern     string
	Replacement string
	Condition   string
}

// compiledRule stores one precompiled regex replacement rule used during scrub operations.
// compiledRule 用于保存一条在 Scrub 期间直接复用的预编译正则替换规则。
type compiledRule struct {
	name         string
	replacement  string
	regex        *regexp.Regexp
	program      program
	condition    string
	hasCondition bool
	hasTemplate  bool
}

// languageRules groups all compiled rules and excludes for one language tag.
// languageRules 用于按语言标签收集全部编译后规则和排除字段。
type languageRules struct {
	language string
	version  string
	rules    []compiledRule
	excludes map[string]struct{}
}

// ruleDirLoadResult carries both the concrete merged language rules and source-layer metadata that is lost after common rules are expanded.
// ruleDirLoadResult 用于同时承载合并后的具体语言规则，以及 common 规则展开后会丢失的来源层元信息。
type ruleDirLoadResult struct {
	// languages contains the final concrete language rules used by runtime scrubbing.
	// languages 保存运行时脱敏实际使用的最终具体语言规则。
	languages map[string]languageRules

	// hasCommon reports whether either system or user layer provided a common rule bundle before merging.
	// hasCommon 表示系统层或用户层在合并前是否提供过 common 规则包。
	hasCommon bool
}

const commonLanguage = "common"

// Engine keeps precompiled language rules and serves concurrent scrub requests safely.
// Engine 用于保存预编译好的多语言规则，并为并发 Scrub 请求提供线程安全访问。
type Engine struct {
	mu             sync.RWMutex
	defaultLang    string
	languages      map[string]languageRules
	hasCommonRules bool
	logger         *logx.Logger
	vm             evaluator
	DebugMode      bool
	debugOutput    io.Writer
}

// NewEngine loads system pii_rules first and then applies the optional user override directory using the default logger.
// NewEngine 用于先加载系统 pii_rules，再使用默认日志器叠加可选的用户覆盖目录。
func NewEngine(systemDir, userDir, defaultLang string) (*Engine, error) {
	return NewEngineWithLogger(systemDir, userDir, defaultLang, nil)
}

// NewEngineWithLogger loads system pii_rules first and then applies the optional user override directory with an explicit logger.
// NewEngineWithLogger 用于先加载系统 pii_rules，再使用显式日志器叠加可选的用户覆盖目录。
func NewEngineWithLogger(systemDir, userDir, defaultLang string, logger *logx.Logger) (*Engine, error) {
	if logger == nil {
		logger = logx.Default()
	}
	engine := &Engine{
		defaultLang: normalizeLanguage(defaultLang),
		languages:   map[string]languageRules{},
		logger:      logger,
	}
	if err := engine.LoadDirs(systemDir, userDir); err != nil {
		return nil, err
	}
	// Warn when common.json is missing so operators know that universal PII rules (emails, credit cards, API keys) are not active.
	// 当 common.json 缺失时发出警告，让运维知道通用 PII 规则（邮箱、信用卡、API Key）未生效。
	if !engine.hasCommonRuleBundle() {
		logger.Warn("pii engine common.json not found, universal PII rules will be missing", "system_dir", systemDir, "user_dir", userDir)
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

// NewEngineWithRules creates one engine from in-memory rule definitions without loading any JSON files.
// NewEngineWithRules 用于从内存规则定义直接创建引擎，而不读取任何 JSON 文件。
func NewEngineWithRules(language string, logger *logx.Logger, rules []RuleDefinition) (*Engine, error) {
	if logger == nil {
		logger = logx.Default()
	}
	lang := normalizeLanguage(language)
	if lang == "" {
		lang = "adhoc"
	}
	compiled, err := compileRuleEntries(ruleEntriesFromDefinitions(rules), lang, "in-memory rules")
	if err != nil {
		return nil, err
	}
	engine := &Engine{
		defaultLang: lang,
		languages: map[string]languageRules{
			lang: {
				language: lang,
				version:  "adhoc",
				rules:    compiled,
				excludes: map[string]struct{}{},
			},
		},
		logger: logger,
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
	e.languages = loaded.languages
	e.hasCommonRules = loaded.hasCommon
	if e.defaultLang == "" {
		for lang := range loaded.languages {
			e.defaultLang = lang
			break
		}
	}
	return nil
}

// hasCommonRuleBundle reports whether the last directory load saw a common rule bundle before language merging removed the common key.
// hasCommonRuleBundle 用于报告最近一次目录加载是否在语言合并前看到了 common 规则包。
func (e *Engine) hasCommonRuleBundle() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.hasCommonRules
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
		matches := rule.regex.FindAllStringSubmatchIndex(scrubbed, -1)
		if len(matches) == 0 {
			continue
		}
		var builder strings.Builder
		builder.Grow(len(scrubbed))
		lastIndex := 0
		for _, match := range matches {
			if len(match) < 2 || match[0] < lastIndex || match[1] > len(scrubbed) {
				continue
			}
			builder.WriteString(scrubbed[lastIndex:match[0]])
			matchText := scrubbed[match[0]:match[1]]
			result := true
			reason := reasonEvalOK
			if rule.hasCondition {
				result, reason = e.vm.Run(rule.program, scrubbed, match)
			}
			if result {
				if rule.hasTemplate {
					expandReplacement(&builder, rule.replacement, scrubbed, match)
				} else {
					builder.WriteString(rule.replacement)
				}
			} else {
				builder.WriteString(matchText)
			}
			e.debugTrace(rule, scrubbed, match, result, reason)
			e.logEvaluation(rule.name, result, reason, matchText)
			lastIndex = match[1]
		}
		builder.WriteString(scrubbed[lastIndex:])
		scrubbed = builder.String()
	}
	return scrubbed
}

// debugTrace prints one verbose rule-evaluation report to stdout when the engine runs in debug mode.
// debugTrace 用于在引擎处于调试模式时把单条规则求值报告打印到标准输出。
func (e *Engine) debugTrace(rule compiledRule, text string, match []int, result bool, reason reasonCode) {
	if e == nil || !e.DebugMode {
		return
	}
	out := e.debugOutput
	if out == nil {
		out = os.Stdout
	}
	fullMatch, ok := readGroup(text, match, 0)
	if !ok {
		fullMatch = ""
	}
	fmt.Fprintf(out, "=== Trace: %s ===\n", rule.name)
	fmt.Fprintf(out, "[-] Regex Matched: %s\n", fullMatch)
	fmt.Fprintf(out, "[-] Capture Groups:\n")
	fmt.Fprintf(out, "    $0: %s\n", fullMatch)
	groupCount := len(match)/2 - 1
	for groupIndex := 1; groupIndex <= groupCount; groupIndex++ {
		groupValue, ok := readGroup(text, match, groupIndex)
		if !ok {
			fmt.Fprintf(out, "    $%d: <unavailable>\n", groupIndex)
			continue
		}
		fmt.Fprintf(out, "    $%d: %s\n", groupIndex, groupValue)
	}
	switch {
	case !rule.hasCondition:
		fmt.Fprintln(out, "[-] VM Evaluated: TRUE (No condition)")
	case result:
		fmt.Fprintf(out, "[-] VM Evaluated: TRUE (%s)\n", rule.condition)
	default:
		fmt.Fprintf(out, "[-] VM Evaluated: FALSE (%s)\n", rule.condition)
	}
	if result {
		replacement := rule.replacement
		if rule.hasTemplate {
			var expanded strings.Builder
			expanded.Grow(len(rule.replacement))
			expandReplacement(&expanded, rule.replacement, text, match)
			replacement = expanded.String()
		}
		fmt.Fprintf(out, "[-] Action: Replaced with %s\n\n", replacement)
		return
	}
	fmt.Fprintf(out, "[-] Action: Kept original match (%s)\n\n", reason)
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
	compiled, err := compileRuleEntries(file.Rules, lang, path)
	if err != nil {
		return languageRules{}, err
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

// ruleEntriesFromDefinitions converts exported in-memory rule definitions into internal rule entries.
// ruleEntriesFromDefinitions 用于把导出的内存规则定义转换为内部规则条目。
func ruleEntriesFromDefinitions(rules []RuleDefinition) []ruleEntry {
	entries := make([]ruleEntry, 0, len(rules))
	for _, rule := range rules {
		entries = append(entries, ruleEntry{
			Name:        rule.Name,
			Pattern:     rule.Pattern,
			Replacement: rule.Replacement,
			Condition:   rule.Condition,
		})
	}
	return entries
}

// compileRuleEntries compiles one list of rule entries and rejects invalid names, regexes, and DSL.
// compileRuleEntries 用于编译一组规则条目，并拒绝非法名称、正则和 DSL。
func compileRuleEntries(entries []ruleEntry, language, source string) ([]compiledRule, error) {
	_ = language
	compiled := make([]compiledRule, 0, len(entries))
	seenNames := make(map[string]struct{}, len(entries))
	for _, item := range entries {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, fmt.Errorf("pii rule source %s contains unnamed rule", source)
		}
		if _, exists := seenNames[name]; exists {
			return nil, fmt.Errorf("pii rule source %s contains duplicate rule name %q", source, name)
		}
		seenNames[name] = struct{}{}
		regex, err := regexp.Compile(item.Pattern)
		if err != nil {
			return nil, fmt.Errorf("compile pii rule %s from %s: %w", name, source, err)
		}
		if err := validateReplacementTemplate(item.Replacement, regex.NumSubexp()); err != nil {
			return nil, fmt.Errorf("compile replacement for pii rule %s from %s: %w", name, source, err)
		}
		condition := strings.TrimSpace(item.Condition)
		prog, err := compileCondition(condition, regex.NumSubexp())
		if err != nil {
			return nil, fmt.Errorf("compile condition for pii rule %s from %s: %w", name, source, err)
		}
		compiled = append(compiled, compiledRule{
			name:         name,
			replacement:  item.Replacement,
			regex:        regex,
			program:      prog,
			condition:    condition,
			hasCondition: condition != "",
			hasTemplate:  strings.Contains(item.Replacement, "$"),
		})
	}
	return compiled, nil
}

// loadRuleDirs selects one common bundle and one language bundle per language, then overlays language rules on top of common rules.
// loadRuleDirs 用于为每种语言分别选择一个公共规则包和一个语言规则包，再把语言规则按名称覆盖到公共规则之上。
func loadRuleDirs(systemDir, userDir string) (ruleDirLoadResult, error) {
	systemDir = strings.TrimSpace(systemDir)
	if systemDir == "" {
		return ruleDirLoadResult{}, fmt.Errorf("system pii rules dir is required")
	}

	systemRules, err := loadRuleDir(systemDir, true)
	if err != nil {
		return ruleDirLoadResult{}, err
	}
	userRules, err := loadRuleDir(userDir, false)
	if err != nil {
		return ruleDirLoadResult{}, err
	}
	return ruleDirLoadResult{
		languages: selectRuleLayers(systemRules, userRules),
		hasCommon: pickBundle(systemRules[commonLanguage], userRules[commonLanguage]).language != "",
	}, nil
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

// selectRuleLayers picks one common bundle source and one language bundle source independently, then applies language overrides by rule name.
// selectRuleLayers 用于分别选择公共规则包来源和语言规则包来源，再按规则名应用语言层对公共层的覆盖。
func selectRuleLayers(systemRules, userRules map[string]languageRules) map[string]languageRules {
	final := map[string]languageRules{}
	langs := collectLanguages(systemRules, userRules)
	for _, lang := range langs {
		merged := languageRules{
			language: lang,
			excludes: map[string]struct{}{},
		}
		applyLanguageOverride(&merged, pickBundle(systemRules[commonLanguage], userRules[commonLanguage]))
		applyLanguageOverride(&merged, pickBundle(systemRules[lang], userRules[lang]))
		final[lang] = merged
	}
	return final
}

// pickBundle prefers the user bundle when it exists, otherwise it falls back to the system bundle.
// pickBundle 用于在用户规则包存在时优先选择用户规则包，否则回退到系统规则包。
func pickBundle(systemBundle, userBundle languageRules) languageRules {
	if userBundle.language != "" {
		return userBundle
	}
	return systemBundle
}

// collectLanguages builds the final list of concrete languages, excluding the reserved common bundle.
// collectLanguages 用于构建最终的具体语言列表，并排除保留的 common 规则包。
func collectLanguages(layers ...map[string]languageRules) []string {
	seen := map[string]struct{}{}
	langs := make([]string, 0)
	for _, layer := range layers {
		for lang := range layer {
			if lang == "" || lang == commonLanguage {
				continue
			}
			if _, exists := seen[lang]; exists {
				continue
			}
			seen[lang] = struct{}{}
			langs = append(langs, lang)
		}
	}
	sort.Strings(langs)
	return langs
}

// applyLanguageOverride applies one bundle on top of the current language bundle using rule-name replacement.
// applyLanguageOverride 用于把一个规则包按“规则名替换、否则追加”的方式覆盖到当前语言包上。
func applyLanguageOverride(dst *languageRules, src languageRules) {
	if dst == nil || src.language == "" {
		return
	}
	if dst.excludes == nil {
		dst.excludes = map[string]struct{}{}
	}
	if strings.TrimSpace(src.version) != "" {
		dst.version = strings.TrimSpace(src.version)
	}
	for key := range src.excludes {
		dst.excludes[key] = struct{}{}
	}
	if len(src.rules) == 0 {
		return
	}
	indexByName := make(map[string]int, len(dst.rules))
	for i, rule := range dst.rules {
		indexByName[rule.name] = i
	}
	for _, rule := range src.rules {
		if idx, exists := indexByName[rule.name]; exists {
			dst.rules[idx] = rule
			continue
		}
		indexByName[rule.name] = len(dst.rules)
		dst.rules = append(dst.rules, rule)
	}
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

// expandReplacement writes one replacement template into the builder by expanding $n capture references directly from the current match offsets.
// expandReplacement 用于直接根据当前匹配偏移把 replacement 模板中的 $n 捕获组展开到 Builder 中。
func expandReplacement(builder *strings.Builder, replacement string, text string, match []int) {
	literalStart := 0
	for i := 0; i < len(replacement); i++ {
		if replacement[i] != '$' || i+1 >= len(replacement) || replacement[i+1] < '0' || replacement[i+1] > '9' {
			continue
		}
		if literalStart < i {
			builder.WriteString(replacement[literalStart:i])
		}
		groupIndex := 0
		j := i + 1
		for j < len(replacement) && replacement[j] >= '0' && replacement[j] <= '9' {
			groupIndex = groupIndex*10 + int(replacement[j]-'0')
			j++
		}
		if groupValue, ok := readGroup(text, match, groupIndex); ok {
			builder.WriteString(groupValue)
		}
		i = j - 1
		literalStart = j
	}
	if literalStart < len(replacement) {
		builder.WriteString(replacement[literalStart:])
	}
}

// validateReplacementTemplate verifies that every $n reference in a replacement string points to an available capture group.
// validateReplacementTemplate 用于校验 replacement 中的每个 $n 引用都指向存在的捕获组。
func validateReplacementTemplate(replacement string, numSubexp int) error {
	for i := 0; i < len(replacement); i++ {
		if replacement[i] != '$' || i+1 >= len(replacement) || replacement[i+1] < '0' || replacement[i+1] > '9' {
			continue
		}
		groupIndex := 0
		j := i + 1
		for j < len(replacement) && replacement[j] >= '0' && replacement[j] <= '9' {
			groupIndex = groupIndex*10 + int(replacement[j]-'0')
			j++
		}
		if groupIndex > numSubexp {
			return fmt.Errorf("replacement references capture group $%d but regex only defines %d groups", groupIndex, numSubexp)
		}
		i = j - 1
	}
	return nil
}

// logEvaluation prints one privacy-safe evaluator line only in explicit debug mode so runtime production logs stay quiet while the standalone tester still shows per-match diagnostics.
// logEvaluation 用于仅在显式调试模式下输出隐私安全的求值信息，确保生产运行时日志保持安静，同时独立调试器仍可看到逐命中诊断信息。
func (e *Engine) logEvaluation(rule string, result bool, reason reasonCode, matchText string) {
	if e == nil || !e.DebugMode {
		return
	}
	hash := sha256.Sum256([]byte(matchText))
	hashText := hex.EncodeToString(hash[:])
	if len(hashText) > 12 {
		hashText = hashText[:12]
	}
	out := e.debugOutput
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "[PII-EVAL] rule=%s result=%t match_len=%d match_hash=%s reason=%s\n", rule, result, len(matchText), hashText, string(reason))
}
