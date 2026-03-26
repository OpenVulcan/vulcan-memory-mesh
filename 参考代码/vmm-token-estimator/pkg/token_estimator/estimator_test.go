package token_estimator

import (
	"math"
	"sync"
	"testing"
)

func TestEstimate_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		text string
		want int
	}{
		{
			name: "overseas empty",
			cfg:  OverseasModelConfig,
			text: "",
			want: 0,
		},
		{
			name: "overseas english words",
			cfg:  OverseasModelConfig,
			text: "hello world",
			want: 2,
		},
		{
			name: "overseas cjk only",
			cfg:  OverseasModelConfig,
			text: "你好世界",
			want: 9,
		},
		{
			name: "overseas mixed text",
			cfg:  OverseasModelConfig,
			text: "你好，world 2026!",
			want: 10,
		},
		{
			name: "overseas camel case",
			cfg:  OverseasModelConfig,
			text: "tokenEstimatorHTTPServer",
			want: 7,
		},
		{
			name: "overseas dense json",
			cfg:  OverseasModelConfig,
			text: "{\"name\":\"张三\",\"age\":30,\"langs\":[\"Go\",\"Python\"],\"active\":true}",
			want: 35,
		},
		{
			name: "domestic empty",
			cfg:  DomesticModelConfig,
			text: "",
			want: 0,
		},
		{
			name: "domestic english words",
			cfg:  DomesticModelConfig,
			text: "hello world",
			want: 2,
		},
		{
			name: "domestic cjk only",
			cfg:  DomesticModelConfig,
			text: "你好世界",
			want: 6,
		},
		{
			name: "domestic mixed text",
			cfg:  DomesticModelConfig,
			text: "你好，world 2026!",
			want: 8,
		},
		{
			name: "domestic camel case",
			cfg:  DomesticModelConfig,
			text: "tokenEstimatorHTTPServer",
			want: 6,
		},
		{
			name: "domestic dense json",
			cfg:  DomesticModelConfig,
			text: "{\"name\":\"张三\",\"age\":30,\"langs\":[\"Go\",\"Python\"],\"active\":true}",
			want: 34,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			estimator := NewEstimator(tt.cfg)
			got := estimator.Estimate(tt.text)
			if got != tt.want {
				t.Fatalf("Estimate(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestEstimateDetailed_Breakdown(t *testing.T) {
	t.Parallel()

	estimator := NewEstimator(OverseasModelConfig)
	report := estimator.EstimateDetailed("你好 GoLang 123!\nПривет")

	wantBuckets := BucketStats{
		Runes:                21,
		CJKRunes:             2,
		LatinWords:           1,
		LatinLetters:         6,
		LatinCaseTransitions: 1,
		NumberRunes:          3,
		SymbolRunes:          1,
		WhitespaceRunes:      3,
		OtherLetterRunes:     6,
	}
	if report.Buckets != wantBuckets {
		t.Fatalf("bucket mismatch:\n got  %+v\n want %+v", report.Buckets, wantBuckets)
	}

	if report.EstimatedTokens != 17 {
		t.Fatalf("EstimatedTokens = %d, want 17", report.EstimatedTokens)
	}

	if math.Abs(report.RawEstimate-15.39) > 0.0001 {
		t.Fatalf("RawEstimate = %.6f, want 15.39", report.RawEstimate)
	}

	if math.Abs(report.Contributions.CJK-3.9) > 0.0001 {
		t.Fatalf("CJK contribution = %.6f, want 3.9", report.Contributions.CJK)
	}
	if math.Abs(report.Contributions.Latin-1.0) > 0.0001 {
		t.Fatalf("Latin contribution = %.6f, want 1.0", report.Contributions.Latin)
	}
	if math.Abs(report.Contributions.Numbers-1.65) > 0.0001 {
		t.Fatalf("Numbers contribution = %.6f, want 1.65", report.Contributions.Numbers)
	}
	if math.Abs(report.Contributions.Symbols-0.8) > 0.0001 {
		t.Fatalf("Symbols contribution = %.6f, want 0.8", report.Contributions.Symbols)
	}
	if math.Abs(report.Contributions.Whitespace-0.24) > 0.0001 {
		t.Fatalf("Whitespace contribution = %.6f, want 0.24", report.Contributions.Whitespace)
	}
	if math.Abs(report.Contributions.OtherLetter-7.8) > 0.0001 {
		t.Fatalf("OtherLetter contribution = %.6f, want 7.8", report.Contributions.OtherLetter)
	}
}

func TestEstimatorBiasProfiles(t *testing.T) {
	t.Parallel()

	overseas := NewEstimator(OverseasModelConfig)
	domestic := NewEstimator(DomesticModelConfig)

	chineseHeavy := "这是一个主要由中文组成的长段落，用来模拟国产模型与海外模型之间的中文成本差异。"
	englishHeavy := "This paragraph is intentionally dominated by ordinary English words so the CJK bias should not amplify too much."
	codeHeavy := "{\"service\":\"billing\",\"ok\":true,\"retries\":3,\"payload\":{\"items\":[1,2,3],\"lang\":\"Go\"}}"

	if overseas.Estimate(chineseHeavy) <= domestic.Estimate(chineseHeavy) {
		t.Fatalf("expected overseas estimate to be larger on Chinese-heavy text")
	}

	if overseas.Estimate(englishHeavy) < 1 || domestic.Estimate(englishHeavy) < 1 {
		t.Fatalf("english-heavy text should still produce a positive estimate")
	}

	if overseas.Estimate(codeHeavy) <= overseas.Estimate("hello world") {
		t.Fatalf("code-heavy text should cost more than a tiny English phrase")
	}
}

func TestEstimatorConcurrentUse(t *testing.T) {
	t.Parallel()

	estimator := NewEstimator(OverseasModelConfig)
	text := "用户说：请把 JSON 和 Go code 混在一起。func main() { fmt.Println(\"hi\") }"
	want := estimator.Estimate(text)

	const goroutines = 64
	const iterations = 500

	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if got := estimator.Estimate(text); got != want {
					errCh <- "Estimate returned inconsistent result"
					return
				}
				report := estimator.EstimateDetailed(text)
				if report.EstimatedTokens != want {
					errCh <- "EstimateDetailed returned inconsistent result"
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for msg := range errCh {
		t.Fatal(msg)
	}
}

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	estimator := NewEstimator(Config{
		CJKWeight:               -1,
		LatinWordWeight:         -2,
		LatinShortWordThreshold: -3,
		LatinCharsPerExtraToken: 0,
		LatinCaseBoundaryWeight: -4,
		NumberWeight:            -5,
		SymbolWeight:            -6,
		WhitespaceWeight:        -7,
		OtherLetterWeight:       -8,
		SafeMargin:              0.5,
		MinimumTokens:           -9,
	})

	cfg := estimator.Config()
	if cfg.CJKWeight != 0 || cfg.LatinWordWeight != 0 || cfg.LatinShortWordThreshold != 0 {
		t.Fatalf("expected negative inputs to be clamped, got %+v", cfg)
	}
	if cfg.LatinCharsPerExtraToken != 4.0 {
		t.Fatalf("LatinCharsPerExtraToken = %.2f, want 4.0", cfg.LatinCharsPerExtraToken)
	}
	if cfg.SafeMargin != 1.0 {
		t.Fatalf("SafeMargin = %.2f, want 1.0", cfg.SafeMargin)
	}
	if cfg.MinimumTokens != 0 {
		t.Fatalf("MinimumTokens = %d, want 0", cfg.MinimumTokens)
	}
}

func BenchmarkEstimatorEstimate(b *testing.B) {
	texts := map[string]string{
		"mixed_cn_en":  "用户说：请把 JSON 和 Go code 混在一起。func main() { fmt.Println(\"hi\") }",
		"dense_json":   "{\"name\":\"张三\",\"age\":30,\"langs\":[\"Go\",\"Python\"],\"active\":true}",
		"english_long": "This is a long English paragraph about token estimation, concurrency safety, context windows, and prompt budgeting across multiple providers and models.",
		"cjk_long":     "这是一个用于基准测试的中文段落，包含较多汉字、标点以及少量空格，用来验证中文按字估算时的吞吐量与稳定性。",
	}

	configs := map[string]Config{
		"overseas": OverseasModelConfig,
		"domestic": DomesticModelConfig,
	}

	for cfgName, cfg := range configs {
		estimator := NewEstimator(cfg)
		for textName, text := range texts {
			b.Run(cfgName+"/"+textName, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_ = estimator.Estimate(text)
				}
			})
		}
	}
}
