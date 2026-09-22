// lexical_tokenizer.go provides reusable GSE pre-tokenization helpers for native SQLite FTS5.
// lexical_tokenizer.go 用于提供原生 SQLite FTS5 可复用的 GSE 预分词辅助能力。
package textutil

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// LexicalTokenizerConfig describes whether the embedded GSE dictionary should be loaded.
// LexicalTokenizerConfig 用于描述是否加载内嵌 GSE 词典。
type LexicalTokenizerConfig struct {
	EnablePreTokenize bool
}

// LexicalTokenizer shares one synchronized segmenter so enabled backends load the dictionary once per process.
// LexicalTokenizer 共享一个带同步保护的分词器，让启用的后端在每个进程中只加载一次词典。
type LexicalTokenizer struct {
	enablePreTokenize bool
	segmenter         *gse.Segmenter
	segmenterMu       sync.Mutex
}

// disabledLexicalTokenizer shares the immutable tokenizer used when pre-tokenization is disabled.
// disabledLexicalTokenizer 共享关闭预分词时使用的不可变分词器。
var disabledLexicalTokenizer = &LexicalTokenizer{}

var (
	// enabledLexicalTokenizerOnce guarantees that the immutable embedded dictionary is loaded once per process.
	// enabledLexicalTokenizerOnce 保证每个进程只加载一次不可变的内嵌词典。
	enabledLexicalTokenizerOnce sync.Once
	// enabledLexicalTokenizer stores the process-wide tokenizer after its dictionary is loaded.
	// enabledLexicalTokenizer 保存词典加载完成后的进程级共享分词器。
	enabledLexicalTokenizer *LexicalTokenizer
	// enabledLexicalTokenizerErr stores the deterministic initialization failure for all later callers.
	// enabledLexicalTokenizerErr 保存确定性的初始化错误，供后续调用方复用。
	enabledLexicalTokenizerErr error
)

// DisabledLexicalTokenizer returns the shared tokenizer that keeps the legacy regex path.
// DisabledLexicalTokenizer 返回共享分词器，让调用方保持旧的正则分词路径。
func DisabledLexicalTokenizer() *LexicalTokenizer {
	return disabledLexicalTokenizer
}

// NewLexicalTokenizer constructs one tokenizer and eagerly loads the embedded Chinese dictionary when enabled.
// NewLexicalTokenizer 构建一个分词器，并在启用时预先加载内嵌中文词典。
func NewLexicalTokenizer(cfg LexicalTokenizerConfig) (*LexicalTokenizer, error) {
	if !cfg.EnablePreTokenize {
		return DisabledLexicalTokenizer(), nil
	}

	// Loading the fixed embedded dictionary once avoids repeating the large immutable dictionary allocation for every backend.
	// 仅加载一次固定内嵌词典，避免每个后端重复分配大型不可变词典。
	enabledLexicalTokenizerOnce.Do(func() {
		segmenter := &gse.Segmenter{SkipLog: true}
		if err := segmenter.LoadDictEmbed("zh"); err != nil {
			enabledLexicalTokenizerErr = fmt.Errorf("load embedded gse dictionary: %w", err)
			return
		}
		enabledLexicalTokenizer = &LexicalTokenizer{enablePreTokenize: true, segmenter: segmenter}
	})
	if enabledLexicalTokenizerErr != nil {
		return nil, enabledLexicalTokenizerErr
	}
	return enabledLexicalTokenizer, nil
}

// BuildSQLiteFTSIndexText turns durable text into a space-separated lexical surface for FTS5.
// BuildSQLiteFTSIndexText 把持久化文本转换为供 FTS5 使用的空格分隔词法表面。
func (t *LexicalTokenizer) BuildSQLiteFTSIndexText(raw string) string {
	normalized := NormalizeWhitespace(raw)
	if normalized == "" {
		return ""
	}
	if !t.isPretokenizeEnabled() {
		return normalized
	}

	plan := t.planTokenization(normalized, false)
	if len(plan.OrderedTokens) == 0 && len(plan.ExpandedTokens) == 0 {
		return normalized
	}

	parts := append([]string{}, plan.OrderedTokens...)
	for _, token := range plan.ExpandedTokens {
		if containsOrderedToken(plan.OrderedTokens, token) {
			continue
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, " ")
}

// BuildSQLiteFTSMatchExpression builds a quoted FTS5 MATCH expression with phrase and OR recall.
// BuildSQLiteFTSMatchExpression 构建带引号的 FTS5 MATCH 表达式，同时保留短语和 OR 召回。
func (t *LexicalTokenizer) BuildSQLiteFTSMatchExpression(raw string) string {
	normalized := NormalizeWhitespace(raw)
	if normalized == "" {
		return ""
	}
	if !t.isPretokenizeEnabled() {
		return buildLegacySQLiteFTSMatchExpression(normalized)
	}

	plan := t.planTokenization(normalized, true)
	if len(plan.OrderedTokens) == 0 && len(plan.ExpandedTokens) == 0 {
		return ""
	}

	parts := make([]string, 0, len(plan.OrderedTokens)+len(plan.ExpandedTokens)+1)
	seen := make(map[string]struct{}, len(plan.OrderedTokens)+len(plan.ExpandedTokens)+1)
	appendQuoted := func(value string) {
		value = NormalizeWhitespace(value)
		if value == "" {
			return
		}
		value = `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}

	if len(plan.OrderedTokens) > 1 {
		appendQuoted(strings.Join(plan.OrderedTokens, " "))
	}
	for _, token := range plan.OrderedTokens {
		appendQuoted(token)
	}
	for _, token := range plan.ExpandedTokens {
		appendQuoted(token)
	}
	return strings.Join(parts, " OR ")
}

// lexicalTokenPlan separates ordered GSE tokens from identifier expansions used for broader recall.
// lexicalTokenPlan 分离有序 GSE token 与用于扩大召回的标识符扩展 token。
type lexicalTokenPlan struct {
	OrderedTokens  []string
	ExpandedTokens []string
}

// planTokenization combines GSE segmentation with legacy identifier extraction for mixed-language text.
// planTokenization 将 GSE 分词与旧标识符提取结合，保证中英文混排文本可检索。
func (t *LexicalTokenizer) planTokenization(normalized string, searchMode bool) lexicalTokenPlan {
	ordered := t.segmentWithGSE(normalized, searchMode)
	expanded := make([]string, 0, len(ordered))
	seenExpanded := make(map[string]struct{}, len(ordered))

	// Preserve identifiers such as vmm-local and gpt-4.1 because GSE splits punctuation.
	// 保留 vmm-local、gpt-4.1 等标识符，因为 GSE 会拆分其中的标点。
	for _, token := range Tokenize(normalized) {
		if containsHanRune(token) {
			continue
		}
		appendExpandedToken(&expanded, seenExpanded, token)
	}

	return lexicalTokenPlan{OrderedTokens: ordered, ExpandedTokens: expanded}
}

// segmentWithGSE runs one synchronized GSE segmentation pass.
// segmentWithGSE 执行一次带同步保护的 GSE 分词。
func (t *LexicalTokenizer) segmentWithGSE(normalized string, searchMode bool) []string {
	if !t.isPretokenizeEnabled() {
		return nil
	}

	t.segmenterMu.Lock()
	defer t.segmenterMu.Unlock()

	var rawTokens []string
	if searchMode {
		rawTokens = t.segmenter.CutSearch(normalized, true)
	} else {
		rawTokens = t.segmenter.Cut(normalized, true)
	}

	tokens := make([]string, 0, len(rawTokens))
	for _, rawToken := range rawTokens {
		for _, token := range tokenPattern.FindAllString(strings.ToLower(NormalizeWhitespace(rawToken)), -1) {
			appendOrderedToken(&tokens, token)
		}
	}
	return tokens
}

// isPretokenizeEnabled reports whether this tokenizer has a loaded GSE segmenter.
// isPretokenizeEnabled 判断当前分词器是否已加载 GSE 分词器。
func (t *LexicalTokenizer) isPretokenizeEnabled() bool {
	return t != nil && t.enablePreTokenize && t.segmenter != nil
}

// appendOrderedToken appends one meaningful primary token.
// appendOrderedToken 追加一个有意义的主 token。
func appendOrderedToken(tokens *[]string, value string) {
	value = NormalizeWhitespace(value)
	if value == "" || !hasLexicalContentRune(value) {
		return
	}
	*tokens = append(*tokens, value)
}

// appendExpandedToken appends one identifier expansion exactly once.
// appendExpandedToken 仅追加一次标识符扩展 token。
func appendExpandedToken(tokens *[]string, seen map[string]struct{}, value string) {
	value = strings.ToLower(NormalizeWhitespace(value))
	if value == "" || !hasLexicalContentRune(value) {
		return
	}
	if _, ok := seen[value]; ok {
		return
	}
	seen[value] = struct{}{}
	*tokens = append(*tokens, value)
}

// containsOrderedToken reports whether a token is already present in the primary sequence.
// containsOrderedToken 判断 token 是否已经存在于主 token 序列中。
func containsOrderedToken(tokens []string, value string) bool {
	for _, token := range tokens {
		if token == value {
			return true
		}
	}
	return false
}

// containsHanRune reports whether a token contains a Han character.
// containsHanRune 判断 token 是否包含汉字。
func containsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// hasLexicalContentRune reports whether a token contains a letter, number, or Han character.
// hasLexicalContentRune 判断 token 是否包含字母、数字或汉字。
func hasLexicalContentRune(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// buildLegacySQLiteFTSMatchExpression preserves the regex-based expression for disabled pre-tokenization.
// buildLegacySQLiteFTSMatchExpression 保留关闭预分词时的正则 MATCH 表达式行为。
func buildLegacySQLiteFTSMatchExpression(normalized string) string {
	if normalized == "" {
		return ""
	}
	tokens := Tokenize(normalized)
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens)+1)
	seen := make(map[string]struct{}, len(tokens)+1)
	appendQuoted := func(value string) {
		value = NormalizeWhitespace(value)
		if value == "" {
			return
		}
		value = `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}
	appendQuoted(normalized)
	for _, token := range tokens {
		appendQuoted(token)
	}
	return strings.Join(parts, " OR ")
}
