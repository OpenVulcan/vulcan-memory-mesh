// lexical_tokenizer_test.go verifies application-side lexical tokenization stays stable for Chinese BM25 retrieval and English fallback identifiers.
// lexical_tokenizer_test.go 用于验证应用层词法预分词在中文 BM25 检索和英文标识符回退场景下保持稳定。
package textutil

import "testing"

// TestLexicalTokenizerBuildSQLiteFTSMatchExpressionPretokenizesChinese verifies Chinese queries are expanded into a tokenized phrase plus OR terms so unicode61-backed FTS can score them correctly.
// TestLexicalTokenizerBuildSQLiteFTSMatchExpressionPretokenizesChinese 用于验证中文查询会被扩展成“分词短语 + OR 词项”，让基于 unicode61 的 FTS 能正确评分。
func TestLexicalTokenizerBuildSQLiteFTSMatchExpressionPretokenizesChinese(t *testing.T) {
	tokenizer, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("NewLexicalTokenizer returned error: %v", err)
	}

	got := tokenizer.BuildSQLiteFTSMatchExpression("文本排序模型")
	want := `"文本 排序 模型" OR "文本" OR "排序" OR "模型"`
	if got != want {
		t.Fatalf("match expression = %q, want %q", got, want)
	}
}

// TestLexicalTokenizerBuildSQLiteFTSIndexTextKeepsIdentifierFallback verifies mixed-language content keeps Chinese word boundaries while still appending stable ASCII identifiers such as hyphenated names.
// TestLexicalTokenizerBuildSQLiteFTSIndexTextKeepsIdentifierFallback 用于验证中英混合内容既能保留中文词边界，也会补充像连字符名称这样的稳定 ASCII 标识符。
func TestLexicalTokenizerBuildSQLiteFTSIndexTextKeepsIdentifierFallback(t *testing.T) {
	tokenizer, err := NewLexicalTokenizer(LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		t.Fatalf("NewLexicalTokenizer returned error: %v", err)
	}

	got := tokenizer.BuildSQLiteFTSIndexText("vmm-local FTS5 中文分词")
	want := "vmm local fts5 中文 分词 vmm-local"
	if got != want {
		t.Fatalf("index text = %q, want %q", got, want)
	}
}

// TestLexicalTokenizerBuildSQLiteFTSMatchExpressionFallsBackWhenDisabled verifies English-centric deployments can disable pre-tokenization and keep the repository's original regex-only MATCH assembly.
// TestLexicalTokenizerBuildSQLiteFTSMatchExpressionFallsBackWhenDisabled 用于验证英文主导部署关闭预分词后，仍会保留仓库原有的纯正则 MATCH 组装逻辑。
func TestLexicalTokenizerBuildSQLiteFTSMatchExpressionFallsBackWhenDisabled(t *testing.T) {
	tokenizer := DisabledLexicalTokenizer()
	got := tokenizer.BuildSQLiteFTSMatchExpression("文本排序模型")
	if got != `"文本排序模型"` {
		t.Fatalf("fallback match expression = %q", got)
	}
}
