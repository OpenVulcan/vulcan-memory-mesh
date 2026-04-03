// lexical_tokenizer.go implements reusable application-side lexical tokenization helpers.
// lexical_tokenizer.go 用于实现可复用的应用层词法预分词辅助能力。
package textutil

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"
)

// LexicalTokenizerConfig describes how SQLite lexical indexing should normalize text before it reaches FTS5.
// LexicalTokenizerConfig 用于描述 SQLite lexical 索引在文本进入 FTS5 之前应如何进行归一化与预分词。
type LexicalTokenizerConfig struct {
	EnablePreTokenize bool
}

// LexicalTokenizer owns one long-lived tokenizer instance so dictionary loading is paid once at startup instead of once per query/write.
// LexicalTokenizer 用于持有一个长生命周期分词器实例，让词典加载成本只在启动时支付一次，而不是在每次查询或写入时重复支付。
type LexicalTokenizer struct {
	enablePreTokenize bool
	segmenter         *gse.Segmenter
	segmenterMu       sync.Mutex
}

var disabledLexicalTokenizer = &LexicalTokenizer{}

// DisabledLexicalTokenizer returns one shared fallback tokenizer that preserves the repository's legacy regex-based FTS behavior.
// DisabledLexicalTokenizer 用于返回一个共享的回退分词器，以保持仓库现有基于正则的 FTS 行为不变。
func DisabledLexicalTokenizer() *LexicalTokenizer {
	return disabledLexicalTokenizer
}

// NewLexicalTokenizer constructs one reusable lexical tokenizer and eagerly loads the embedded GSE dictionary when pre-tokenization is enabled.
// NewLexicalTokenizer 用于构建一个可复用的词法分词器；当启用预分词时，它会在启动阶段预加载内嵌 GSE 词典。
func NewLexicalTokenizer(cfg LexicalTokenizerConfig) (*LexicalTokenizer, error) {
	tokenizer := &LexicalTokenizer{enablePreTokenize: cfg.EnablePreTokenize}
	if !cfg.EnablePreTokenize {
		return tokenizer, nil
	}

	// Load the embedded dictionary once so sqlite lexical recall stays CGO-free and does not depend on extra runtime files.
	// 只加载一次内嵌词典，确保 sqlite lexical 召回保持 CGO-free，同时不依赖额外的运行时词典文件。
	segmenter := &gse.Segmenter{SkipLog: true}
	if err := segmenter.LoadDictEmbed("zh"); err != nil {
		return nil, fmt.Errorf("load embedded gse dictionary: %w", err)
	}
	tokenizer.segmenter = segmenter
	return tokenizer, nil
}

// BuildSQLiteFTSIndexText converts durable memory text into the lexical surface stored inside FTS5 so Chinese text can participate in BM25 scoring.
// BuildSQLiteFTSIndexText 用于把长期记忆文本转换成写入 FTS5 的词法表面形式，让中文文本也能参与 BM25 评分。
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

// BuildSQLiteFTSMatchExpression converts one caller query into a safe FTS5 MATCH expression that keeps exact token-order boosts while still allowing broader OR recall.
// BuildSQLiteFTSMatchExpression 用于把调用方查询转换成安全的 FTS5 MATCH 表达式，同时兼顾“按词序精确命中”与更宽松的 OR 召回。
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
		return buildLegacySQLiteFTSMatchExpression(normalized)
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

// lexicalTokenPlan keeps the ordered primary tokens plus optional identifier-oriented expansions so query-time phrase boosts and identifier recall can coexist.
// lexicalTokenPlan 用于同时保存有序主 token 与面向标识符的补充 token，让查询阶段的短语加权和标识符召回可以并存。
type lexicalTokenPlan struct {
	OrderedTokens  []string
	ExpandedTokens []string
}

// planTokenization derives the ordered primary tokens from GSE and then supplements ASCII-style identifiers from the legacy regex tokenizer so mixed-language inputs stay searchable.
// planTokenization 用于先从 GSE 推导有序主 token，再从旧正则分词里补充 ASCII 标识符，确保中英混合输入仍具备可搜索性。
func (t *LexicalTokenizer) planTokenization(normalized string, searchMode bool) lexicalTokenPlan {
	ordered := t.segmentWithGSE(normalized, searchMode)
	expanded := make([]string, 0, len(ordered))
	seenExpanded := make(map[string]struct{}, len(ordered))

	// Preserve non-Han identifiers like "vmm-local" or "gpt-4.1" because GSE intentionally splits punctuation while unicode61 can index them as one stable token.
	// 保留像 "vmm-local" 或 "gpt-4.1" 这类非汉字标识符，因为 GSE 会按标点拆开，而 unicode61 仍可把它们作为稳定 token 建索引。
	for _, token := range Tokenize(normalized) {
		if containsHanRune(token) {
			continue
		}
		appendExpandedToken(&expanded, seenExpanded, token)
	}

	return lexicalTokenPlan{
		OrderedTokens:  ordered,
		ExpandedTokens: expanded,
	}
}

// segmentWithGSE executes one synchronized GSE segmentation pass so the shared singleton remains concurrency-safe even if the upstream library changes its internal mutability later.
// segmentWithGSE 用于执行一次带同步保护的 GSE 分词，让共享单例即使在上游实现未来调整内部可变状态时，也能保持并发安全。
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

// isPretokenizeEnabled reports whether this tokenizer should route text through GSE instead of the legacy regex-only path.
// isPretokenizeEnabled 用于判断当前分词器是否应把文本送入 GSE，而不是继续走旧的纯正则路径。
func (t *LexicalTokenizer) isPretokenizeEnabled() bool {
	return t != nil && t.enablePreTokenize && t.segmenter != nil
}

// appendOrderedToken appends one ordered token while discarding empty noise so FTS phrase queries stay stable.
// appendOrderedToken 用于追加一个有序 token，同时丢弃空噪声项，确保 FTS 短语查询保持稳定。
func appendOrderedToken(tokens *[]string, value string) {
	value = NormalizeWhitespace(value)
	if value == "" || !hasLexicalContentRune(value) {
		return
	}
	*tokens = append(*tokens, value)
}

// appendExpandedToken appends one expansion token exactly once so identifier-specific fallback terms do not bloat the final MATCH expression.
// appendExpandedToken 用于按“只追加一次”的方式加入补充 token，避免标识符回退词把最终 MATCH 表达式无意义地膨胀。
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

// containsOrderedToken reports whether one token already exists in the ordered slice so index-time expansions can avoid replaying tokens that GSE has already produced.
// containsOrderedToken 用于判断某个 token 是否已经存在于有序切片中，以便索引阶段的补充 token 避免重复写入 GSE 已产出的内容。
func containsOrderedToken(tokens []string, value string) bool {
	for _, token := range tokens {
		if token == value {
			return true
		}
	}
	return false
}

// containsHanRune reports whether one token contains any Han rune so the legacy regex fallback can avoid re-adding whole unsplit Chinese phrases.
// containsHanRune 用于判断某个 token 是否包含汉字，以便旧正则回退路径避免再次补入未切开的整段中文短语。
func containsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// hasLexicalContentRune reports whether a token contains at least one letter, number, or Han rune so standalone punctuation does not pollute FTS rows or MATCH expressions.
// hasLexicalContentRune 用于判断一个 token 是否至少包含一个字母、数字或汉字，避免孤立标点污染 FTS 行和 MATCH 表达式。
func hasLexicalContentRune(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// buildLegacySQLiteFTSMatchExpression preserves the repository's original regex-based MATCH assembly so English-centric deployments can disable pre-tokenization without behavioral surprises.
// buildLegacySQLiteFTSMatchExpression 用于保留仓库原有的正则式 MATCH 组装逻辑，让英文主导部署关闭预分词后不会出现行为突变。
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
