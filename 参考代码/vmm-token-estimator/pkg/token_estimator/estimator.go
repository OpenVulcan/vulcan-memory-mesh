package token_estimator

import (
	"math"
	"unicode"
)

// Config defines all tunable coefficients used by the estimator.
//
// Design notes:
//   - CJK is counted per rune because Chinese/Japanese/Korean ideographs often
//     map much closer to character-level token consumption.
//   - Latin text is estimated per contiguous word-like run instead of per letter,
//     then adjusted for unusually long words and camelCase/PascalCase boundaries.
//   - Numbers, symbols and whitespaces are kept independent so code, JSON and
//     prompt templates can be tuned without changing the core algorithm.
//   - SafeMargin is applied at the very end and should usually stay above 1.0
//     to favor slight overestimation over risky underestimation.
type Config struct {
	// Per-rune weight for Han/Hiragana/Katakana/Hangul text.
	CJKWeight float64

	// Base weight for one contiguous Latin word run, for example "hello".
	LatinWordWeight float64
	// Letters covered by the base Latin word cost before extra cost is applied.
	LatinShortWordThreshold int
	// Average number of extra Latin letters that add one more estimated token.
	LatinCharsPerExtraToken float64
	// Extra cost for lower->upper transitions inside one Latin run, useful for
	// identifiers such as tokenEstimator or HTTPServer.
	LatinCaseBoundaryWeight float64

	// Per-digit weight.
	NumberWeight float64
	// Per symbol or punctuation rune weight.
	SymbolWeight float64
	// Per whitespace rune weight, including spaces and newlines.
	WhitespaceWeight float64
	// Fallback per-rune weight for non-Latin, non-CJK letters such as Cyrillic.
	OtherLetterWeight float64

	// Final multiplicative safety factor. For interceptors, values around
	// 1.05 ~ 1.10 are usually a good default.
	SafeMargin float64
	// Minimum tokens returned for any non-empty input.
	MinimumTokens int
}

// DefaultBalancedConfig is a conservative general-purpose baseline.
var DefaultBalancedConfig = Config{
	CJKWeight:               1.60,
	LatinWordWeight:         0.90,
	LatinShortWordThreshold: 6,
	LatinCharsPerExtraToken: 4.0,
	LatinCaseBoundaryWeight: 0.12,
	NumberWeight:            0.55,
	SymbolWeight:            0.78,
	WhitespaceWeight:        0.08,
	OtherLetterWeight:       1.20,
	SafeMargin:              1.08,
	MinimumTokens:           1,
}

// OverseasModelConfig simulates models where CJK text is comparatively
// expensive while English words remain relatively cheap.
var OverseasModelConfig = Config{
	CJKWeight:               1.95,
	LatinWordWeight:         0.88,
	LatinShortWordThreshold: 6,
	LatinCharsPerExtraToken: 4.0,
	LatinCaseBoundaryWeight: 0.12,
	NumberWeight:            0.55,
	SymbolWeight:            0.80,
	WhitespaceWeight:        0.08,
	OtherLetterWeight:       1.30,
	SafeMargin:              1.08,
	MinimumTokens:           1,
}

// DomesticModelConfig simulates models where CJK text is comparatively cheap
// and English keeps a more regular cost profile.
var DomesticModelConfig = Config{
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

// Estimator is immutable after construction and therefore safe for concurrent
// use by many goroutines.
type Estimator struct {
	cfg Config
}

// BucketStats captures the classified text shape.
type BucketStats struct {
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

// ContributionStats stores the raw cost contribution of each bucket.
type ContributionStats struct {
	CJK         float64
	Latin       float64
	Numbers     float64
	Symbols     float64
	Whitespace  float64
	OtherLetter float64
}

// Total returns the accumulated raw estimate before SafeMargin is applied.
func (c ContributionStats) Total() float64 {
	return c.CJK + c.Latin + c.Numbers + c.Symbols + c.Whitespace + c.OtherLetter
}

// Report is a detailed estimation result useful for calibration and debugging.
type Report struct {
	RawEstimate      float64
	SafeMargin       float64
	EstimatedTokens  int
	Buckets          BucketStats
	Contributions    ContributionStats
	NormalizedConfig Config
}

// NewEstimator normalizes the provided configuration and returns an immutable
// estimator instance.
func NewEstimator(cfg Config) *Estimator {
	return &Estimator{cfg: normalizeConfig(cfg)}
}

// Config returns a copy of the normalized configuration in use.
func (e *Estimator) Config() Config {
	return e.cfg
}

// Estimate returns a conservative token estimate in O(N) time over UTF-8 runes.
func (e *Estimator) Estimate(text string) int {
	return e.EstimateDetailed(text).EstimatedTokens
}

// EstimateDetailed returns the estimate together with bucket counts and per-
// bucket contributions, which makes coefficient tuning much easier.
func (e *Estimator) EstimateDetailed(text string) Report {
	cfg := e.cfg
	if text == "" {
		return Report{
			RawEstimate:      0,
			SafeMargin:       cfg.SafeMargin,
			EstimatedTokens:  0,
			Buckets:          BucketStats{},
			Contributions:    ContributionStats{},
			NormalizedConfig: cfg,
		}
	}

	var stats BucketStats
	var contrib ContributionStats
	var latin latinAccumulator

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
		latin = latinAccumulator{}
	}

	for _, r := range text {
		stats.Runes++

		switch {
		case isCJK(r):
			flushLatin()
			stats.CJKRunes++
			contrib.CJK += cfg.CJKWeight
		case isLatinLetter(r):
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
		return Report{
			RawEstimate:      0,
			SafeMargin:       cfg.SafeMargin,
			EstimatedTokens:  0,
			Buckets:          stats,
			Contributions:    contrib,
			NormalizedConfig: cfg,
		}
	}

	estimated := int(math.Ceil(raw * cfg.SafeMargin))
	if estimated < cfg.MinimumTokens {
		estimated = cfg.MinimumTokens
	}

	return Report{
		RawEstimate:      raw,
		SafeMargin:       cfg.SafeMargin,
		EstimatedTokens:  estimated,
		Buckets:          stats,
		Contributions:    contrib,
		NormalizedConfig: cfg,
	}
}

type latinAccumulator struct {
	letters         int
	caseTransitions int
	last            rune
}

func (a *latinAccumulator) push(r rune) {
	if a.letters > 0 && unicode.IsLower(a.last) && unicode.IsUpper(r) {
		a.caseTransitions++
	}
	a.letters++
	a.last = r
}

func normalizeConfig(cfg Config) Config {
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

func isCJK(r rune) bool {
	return unicode.In(r,
		unicode.Han,
		unicode.Hiragana,
		unicode.Katakana,
		unicode.Hangul,
	)
}

func isLatinLetter(r rune) bool {
	return unicode.IsLetter(r) && unicode.In(r, unicode.Latin)
}
