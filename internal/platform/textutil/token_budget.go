// token_budget.go implements heuristic token budgeting helpers used to clip oversized post-action payloads before storage.
// token_budget.go 用于实现启发式 token 预算助手，在 post-action 载荷入库前裁剪超大文本。
package textutil

import (
	"math"
	"unicode"
	"unicode/utf8"
)

// TokenEstimatorConfig defines the coefficients used by the lightweight token estimator.
// TokenEstimatorConfig 用于定义轻量 token 估算器使用的系数。
type TokenEstimatorConfig struct {
	CJKWeight               float64
	LatinWordWeight         float64
	LatinShortWordThreshold int
	LatinCharsPerExtraToken float64
	LatinCaseBoundaryWeight float64
	NumberWeight            float64
	SymbolWeight            float64
	WhitespaceWeight        float64
	OtherLetterWeight       float64
	SafeMargin              float64
	MinimumTokens           int
}

// TokenBudgetConfig describes the clipping budget applied after machine-text cleaning.
// TokenBudgetConfig 用于描述机器文本清洗之后执行的截断预算。
type TokenBudgetConfig struct {
	MaxTokens int
	HeadRunes int
	TailRunes int
	Marker    string
	Estimator TokenEstimatorConfig
}

// TokenEstimator is immutable after construction and can be shared across goroutines.
// TokenEstimator 用于在构造后保持不可变，因此可以安全地被多个 goroutine 共享。
type TokenEstimator struct {
	cfg TokenEstimatorConfig
}

// tokenBucketStats stores the classified text-shape counts used by the estimator.
// tokenBucketStats 用于保存估算器使用的文本形态分类统计。
type tokenBucketStats struct {
	Runes                int
	CJKRunes             int
	LatinWords           int
	LatinLetters         int
	LatinCaseTransitions int
	NumberRunes          int
	SymbolRunes          int
	WhitespaceRunes      int
	OtherLetterRunes     int
}

// tokenContributionStats stores the raw contribution of each bucket before the safety margin is applied.
// tokenContributionStats 用于保存各个分类桶在安全系数应用前的原始贡献值。
type tokenContributionStats struct {
	CJK         float64
	Latin       float64
	Numbers     float64
	Symbols     float64
	Whitespace  float64
	OtherLetter float64
}

// Total sums the raw bucket contributions into one pre-margin estimate.
// Total 用于把各分类桶原始贡献值汇总成安全系数应用前的估算结果。
func (c tokenContributionStats) Total() float64 {
	return c.CJK + c.Latin + c.Numbers + c.Symbols + c.Whitespace + c.OtherLetter
}

// TokenEstimateReport exposes the detailed estimation result so tests can calibrate the heuristic.
// TokenEstimateReport 用于暴露详细估算结果，方便测试和调参验证启发式效果。
type TokenEstimateReport struct {
	RawEstimate     float64
	SafeMargin      float64
	EstimatedTokens int
	Buckets         tokenBucketStats
	Contributions   tokenContributionStats
	Config          TokenEstimatorConfig
}

// DomesticTokenEstimatorConfig returns the default estimator profile used by the post-action storage sanitizer.
// DomesticTokenEstimatorConfig 用于返回 post-action 存储清洗器默认使用的估算器配置。
func DomesticTokenEstimatorConfig() TokenEstimatorConfig {
	return TokenEstimatorConfig{
		CJKWeight:               1.18,
		LatinWordWeight:         0.88,
		LatinShortWordThreshold: 5,
		LatinCharsPerExtraToken: 4.5,
		LatinCaseBoundaryWeight: 0.10,
		NumberWeight:            0.50,
		SymbolWeight:            0.82,
		WhitespaceWeight:        0.08,
		OtherLetterWeight:       1.10,
		SafeMargin:              1.06,
		MinimumTokens:           1,
	}
}

// DefaultTokenBudgetConfig returns the conservative post-action clipping budget used before persistence.
// DefaultTokenBudgetConfig 用于返回持久化前采用的保守 post-action 裁剪预算。
func DefaultTokenBudgetConfig() TokenBudgetConfig {
	return TokenBudgetConfig{
		MaxTokens: 1200,
		HeadRunes: 1200,
		TailRunes: 400,
		Marker:    "\n...[truncated by token budget]...\n",
		Estimator: DomesticTokenEstimatorConfig(),
	}
}

// NewTokenEstimator constructs one immutable estimator with normalized coefficients.
// NewTokenEstimator 用于使用规范化后的系数构造不可变估算器。
func NewTokenEstimator(cfg TokenEstimatorConfig) *TokenEstimator {
	return &TokenEstimator{cfg: normalizeTokenEstimatorConfig(cfg)}
}

// Estimate returns the conservative token estimate for one text payload.
// Estimate 用于返回单条文本载荷的保守 token 估算值。
func (e *TokenEstimator) Estimate(text string) int {
	return e.EstimateDetailed(text).EstimatedTokens
}

// EstimateDetailed returns the detailed token estimate that explains why one text exceeds the budget.
// EstimateDetailed 用于返回详细 token 估算结果，以解释某段文本为何超出预算。
func (e *TokenEstimator) EstimateDetailed(text string) TokenEstimateReport {
	cfg := e.cfg
	if text == "" {
		return TokenEstimateReport{SafeMargin: cfg.SafeMargin, Config: cfg}
	}

	var stats tokenBucketStats
	var contrib tokenContributionStats
	var latin tokenLatinAccumulator
	flushLatin := func() {
		if latin.letters == 0 {
			return
		}
		stats.LatinWords++
		stats.LatinLetters += latin.letters
		stats.LatinCaseTransitions += latin.caseTransitions
		wordCost := cfg.LatinWordWeight
		if latin.letters > cfg.LatinShortWordThreshold {
			wordCost += float64(latin.letters-cfg.LatinShortWordThreshold) / cfg.LatinCharsPerExtraToken
		}
		if latin.caseTransitions > 0 {
			wordCost += float64(latin.caseTransitions) * cfg.LatinCaseBoundaryWeight
		}
		contrib.Latin += wordCost
		latin = tokenLatinAccumulator{}
	}

	// Count each rune bucket in one pass so token clipping stays cheap even when run for every post-action request.
	// 单次遍历完成各类 rune 计数，让 token 裁剪即使在每次 post-action 都执行时也保持轻量。
	for _, r := range text {
		stats.Runes++
		switch {
		case isTokenEstimatorCJK(r):
			flushLatin()
			stats.CJKRunes++
			contrib.CJK += cfg.CJKWeight
		case isTokenEstimatorLatinLetter(r):
			latin.push(r)
		case unicode.IsDigit(r):
			flushLatin()
			stats.NumberRunes++
			contrib.Numbers += cfg.NumberWeight
		case unicode.IsSpace(r):
			flushLatin()
			stats.WhitespaceRunes++
			contrib.Whitespace += cfg.WhitespaceWeight
		case unicode.IsLetter(r):
			flushLatin()
			stats.OtherLetterRunes++
			contrib.OtherLetter += cfg.OtherLetterWeight
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			flushLatin()
			stats.SymbolRunes++
			contrib.Symbols += cfg.SymbolWeight
		default:
			flushLatin()
			stats.SymbolRunes++
			contrib.Symbols += cfg.SymbolWeight
		}
	}
	flushLatin()

	raw := contrib.Total()
	if raw == 0 {
		return TokenEstimateReport{SafeMargin: cfg.SafeMargin, Buckets: stats, Contributions: contrib, Config: cfg}
	}

	estimated := int(math.Ceil(raw * cfg.SafeMargin))
	if estimated < cfg.MinimumTokens {
		estimated = cfg.MinimumTokens
	}
	return TokenEstimateReport{
		RawEstimate:     raw,
		SafeMargin:      cfg.SafeMargin,
		EstimatedTokens: estimated,
		Buckets:         stats,
		Contributions:   contrib,
		Config:          cfg,
	}
}

// EnforceTokenBudget clips oversized text by preserving the head and tail while inserting one marker in the middle.
// EnforceTokenBudget 用于在文本超出预算时保留头尾并在中间插入截断标记。
func EnforceTokenBudget(text string, cfg TokenBudgetConfig) string {
	cfg = normalizeTokenBudgetConfig(cfg)
	if text == "" || cfg.MaxTokens <= 0 {
		return text
	}
	estimator := NewTokenEstimator(cfg.Estimator)
	if estimator.Estimate(text) <= cfg.MaxTokens {
		return text
	}

	runes := []rune(text)
	if len(runes) <= cfg.HeadRunes+cfg.TailRunes {
		return text
	}

	headRunes := minTokenBudgetInt(cfg.HeadRunes, len(runes))
	tailRunes := minTokenBudgetInt(cfg.TailRunes, len(runes)-headRunes)
	clipped := string(runes[:headRunes]) + cfg.Marker + string(runes[len(runes)-tailRunes:])
	if estimator.Estimate(clipped) <= cfg.MaxTokens {
		return clipped
	}

	// Shrink progressively when the initial head/tail budget still exceeds the target because the input is extremely dense.
	// 当初始头尾预算在极高密度文本下仍超标时，继续渐进收缩，避免把超大文本原样写入存储。
	for headRunes > 80 || tailRunes > 40 {
		if headRunes > 80 {
			headRunes = maxTokenBudgetInt(80, int(float64(headRunes)*0.8))
		}
		if tailRunes > 40 {
			tailRunes = maxTokenBudgetInt(40, int(float64(tailRunes)*0.8))
		}
		clipped = string(runes[:headRunes]) + cfg.Marker + string(runes[len(runes)-tailRunes:])
		if estimator.Estimate(clipped) <= cfg.MaxTokens {
			return clipped
		}
	}
	return clipped
}

// tokenLatinAccumulator tracks one contiguous Latin word run while the estimator scans the string.
// tokenLatinAccumulator 用于在估算器扫描字符串时跟踪连续 Latin 单词段。
type tokenLatinAccumulator struct {
	letters         int
	caseTransitions int
	last            rune
}

// push appends one Latin rune into the current word run and records lower->upper transitions.
// push 用于把一个 Latin rune 追加到当前单词段，并记录 lower->upper 的大小写跃迁。
func (a *tokenLatinAccumulator) push(r rune) {
	if a.letters > 0 && unicode.IsLower(a.last) && unicode.IsUpper(r) {
		a.caseTransitions++
	}
	a.letters++
	a.last = r
}

// normalizeTokenEstimatorConfig ensures the estimator never runs with negative or zero divisors.
// normalizeTokenEstimatorConfig 用于确保估算器不会以负数或零除数配置运行。
func normalizeTokenEstimatorConfig(cfg TokenEstimatorConfig) TokenEstimatorConfig {
	if cfg.CJKWeight < 0 {
		cfg.CJKWeight = 0
	}
	if cfg.LatinWordWeight < 0 {
		cfg.LatinWordWeight = 0
	}
	if cfg.LatinShortWordThreshold < 0 {
		cfg.LatinShortWordThreshold = 0
	}
	if cfg.LatinCharsPerExtraToken <= 0 {
		cfg.LatinCharsPerExtraToken = 4.0
	}
	if cfg.LatinCaseBoundaryWeight < 0 {
		cfg.LatinCaseBoundaryWeight = 0
	}
	if cfg.NumberWeight < 0 {
		cfg.NumberWeight = 0
	}
	if cfg.SymbolWeight < 0 {
		cfg.SymbolWeight = 0
	}
	if cfg.WhitespaceWeight < 0 {
		cfg.WhitespaceWeight = 0
	}
	if cfg.OtherLetterWeight < 0 {
		cfg.OtherLetterWeight = 0
	}
	if cfg.SafeMargin < 1.0 {
		cfg.SafeMargin = 1.0
	}
	if cfg.MinimumTokens < 0 {
		cfg.MinimumTokens = 0
	}
	return cfg
}

// normalizeTokenBudgetConfig applies fallback defaults so post-action clipping always has a usable budget.
// normalizeTokenBudgetConfig 用于应用兜底默认值，确保 post-action 裁剪总能拿到可用预算。
func normalizeTokenBudgetConfig(cfg TokenBudgetConfig) TokenBudgetConfig {
	defaults := DefaultTokenBudgetConfig()
	estimatorEmpty := cfg.Estimator == (TokenEstimatorConfig{})
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaults.MaxTokens
	}
	if cfg.HeadRunes <= 0 {
		cfg.HeadRunes = defaults.HeadRunes
	}
	if cfg.TailRunes <= 0 {
		cfg.TailRunes = defaults.TailRunes
	}
	if cfg.Marker == "" {
		cfg.Marker = defaults.Marker
	}
	if estimatorEmpty {
		cfg.Estimator = defaults.Estimator
	} else {
		cfg.Estimator = normalizeTokenEstimatorConfig(cfg.Estimator)
	}
	return cfg
}

// isTokenEstimatorCJK checks whether one rune belongs to the CJK families counted at rune granularity.
// isTokenEstimatorCJK 用于判断一个 rune 是否属于按字符粒度计数的 CJK 字符族。
func isTokenEstimatorCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// isTokenEstimatorLatinLetter checks whether one rune is a Latin letter counted as part of a word run.
// isTokenEstimatorLatinLetter 用于判断一个 rune 是否是按单词段统计的 Latin 字母。
func isTokenEstimatorLatinLetter(r rune) bool {
	return unicode.IsLetter(r) && unicode.In(r, unicode.Latin)
}

// minTokenBudgetInt returns the smaller integer used by token clipping math.
// minTokenBudgetInt 用于在 token 裁剪计算中返回较小整数。
func minTokenBudgetInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxTokenBudgetInt returns the larger integer used by token clipping math.
// maxTokenBudgetInt 用于在 token 裁剪计算中返回较大整数。
func maxTokenBudgetInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// runeCount returns the rune length of one string while keeping call sites explicit and readable.
// runeCount 用于返回字符串的 rune 长度，保持调用点语义明确易读。
func runeCount(s string) int {
	return utf8.RuneCountInString(s)
}
