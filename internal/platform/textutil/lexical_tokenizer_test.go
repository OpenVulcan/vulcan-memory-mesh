// lexical_tokenizer_test.go verifies the native FTS lexical surface and query escaping contract.
// lexical_tokenizer_test.go 用于验证原生 FTS 词法表面与查询转义契约。
package textutil

import (
	"strings"
	"sync"
	"testing"
)

// TestLexicalTokenizerBuildsStableMixedLanguageTokens verifies Chinese, identifiers, and versions share one index surface.
// TestLexicalTokenizerBuildsStableMixedLanguageTokens 验证中文、标识符与版本号共享稳定索引表面。
func TestLexicalTokenizerBuildsStableMixedLanguageTokens(t *testing.T) {
	tokenizer, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("load embedded tokenizer: %v", err)
	}
	indexed := tokenizer.BuildSQLiteFTSIndexText("请检查 vmm-local 的 gpt-4.1 配置")
	for _, token := range []string{"请", "检查", "vmm-local", "gpt-4.1", "配置"} {
		if !strings.Contains(indexed, token) {
			t.Fatalf("indexed text %q does not contain %q", indexed, token)
		}
	}
}

// TestLexicalTokenizerConcurrentConstructionSharesDictionary verifies concurrent constructors share one loaded GSE instance.
// TestLexicalTokenizerConcurrentConstructionSharesDictionary 验证并发构造函数共享同一个已加载的 GSE 实例。
func TestLexicalTokenizerConcurrentConstructionSharesDictionary(t *testing.T) {
	const callers = 32
	instances := make(chan *LexicalTokenizer, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			instance, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
			instances <- instance
			errs <- err
		}()
	}
	wg.Wait()
	close(instances)
	close(errs)
	var first *LexicalTokenizer
	for instance := range instances {
		if instance == nil {
			t.Fatal("concurrent constructor returned nil tokenizer")
		}
		if first == nil {
			first = instance
			continue
		}
		if instance != first {
			t.Fatalf("concurrent constructors returned distinct tokenizers: %p and %p", first, instance)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent constructor failed: %v", err)
		}
	}
	disabled, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: false})
	if err != nil {
		t.Fatalf("disabled tokenizer construction failed: %v", err)
	}
	if disabled != DisabledLexicalTokenizer() {
		t.Fatal("disabled tokenizer should remain the lightweight shared path")
	}
}

// TestLexicalTokenizerSharedInstancesProduceIdenticalTokens verifies cached instances preserve exact lexical output.
// TestLexicalTokenizerSharedInstancesProduceIdenticalTokens 验证缓存实例保持完全一致的词法输出。
func TestLexicalTokenizerSharedInstancesProduceIdenticalTokens(t *testing.T) {
	first, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("load first tokenizer: %v", err)
	}
	second, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("load second tokenizer: %v", err)
	}
	input := "共享中文词典 gpt-4.1 vmm-local"
	if got, want := second.BuildSQLiteFTSIndexText(input), first.BuildSQLiteFTSIndexText(input); got != want {
		t.Fatalf("shared tokenizer output changed: got %q want %q", got, want)
	}
	if got, want := second.BuildSQLiteFTSMatchExpression(input), first.BuildSQLiteFTSMatchExpression(input); got != want {
		t.Fatalf("shared tokenizer query output changed: got %q want %q", got, want)
	}
}

// BenchmarkLexicalTokenizerCachedConstruction records the warm constructor cost after the one-time dictionary load.
// BenchmarkLexicalTokenizerCachedConstruction 记录一次性词典加载后的热构造成本。
func BenchmarkLexicalTokenizerCachedConstruction(b *testing.B) {
	if _, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true}); err != nil {
		b.Fatalf("load embedded tokenizer: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true}); err != nil {
			b.Fatalf("construct cached tokenizer: %v", err)
		}
	}
}

// TestLexicalTokenizerEscapesFTSOperators verifies user punctuation cannot become FTS syntax.
// TestLexicalTokenizerEscapesFTSOperators 验证用户标点不会变成 FTS 操作符。
func TestLexicalTokenizerEscapesFTSOperators(t *testing.T) {
	tokenizer, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("load embedded tokenizer: %v", err)
	}
	query := tokenizer.BuildSQLiteFTSMatchExpression(`标题 OR "括号" (NEAR:3) *`)
	if query == "" {
		t.Fatal("operator-containing query should preserve lexical terms")
	}
	if strings.Contains(query, " OR ") {
		parts := strings.Split(query, " OR ")
		for _, part := range parts {
			if !strings.HasPrefix(part, `"`) || !strings.HasSuffix(part, `"`) {
				t.Fatalf("unquoted FTS term %q in expression %q", part, query)
			}
		}
	}
	if got := tokenizer.BuildSQLiteFTSMatchExpression("!!!"); got != "" {
		t.Fatalf("punctuation-only query produced %q", got)
	}
}

// TestLexicalTokenizerConcurrentCalls verifies the explicit synchronization around the shared GSE segmenter.
// TestLexicalTokenizerConcurrentCalls 验证共享 GSE 分词器上的显式同步保护。
func TestLexicalTokenizerConcurrentCalls(t *testing.T) {
	tokenizer, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("load embedded tokenizer: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := tokenizer.BuildSQLiteFTSIndexText("中文检索 gpt-4.1"); got == "" {
				t.Errorf("concurrent tokenization returned empty result")
			}
		}()
	}
	wg.Wait()
}
