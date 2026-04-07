// embedding_test.go verifies the embedding response index contract used by strict and best-effort callers.
// embedding_test.go 用于验证严格调用方与 best-effort 调用方共享的 embedding 响应索引契约。
package ports

import "testing"

// TestEmbeddingResponseIndexedVectorsAllowsAllDroppedWithoutResultIndices verifies a best-effort response may legally report zero vectors plus only dropped items when every source text was rejected.
// TestEmbeddingResponseIndexedVectorsAllowsAllDroppedWithoutResultIndices 用于验证当全部源文本都被拒绝时，best-effort 响应可以合法返回“零向量 + 仅 dropped 条目”，而不必额外伪造 ResultIndices。
func TestEmbeddingResponseIndexedVectorsAllowsAllDroppedWithoutResultIndices(t *testing.T) {
	resp := EmbeddingResponse{
		Dropped: []EmbeddingDroppedInput{
			{Index: 0, Text: "too long one", Reason: "input_too_large"},
			{Index: 1, Text: "too long two", Reason: "input_too_large"},
		},
	}

	items, err := resp.IndexedVectors(2)
	if err != nil {
		t.Fatalf("IndexedVectors returned error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("indexed vectors = %+v, want none", items)
	}
}

// TestEmbeddingResponseIndexedVectorsRejectsIncompleteAllDroppedCoverage verifies the zero-vector best-effort shortcut still requires dropped items to cover every input, so malformed adapters cannot silently hide undisclosed results.
// TestEmbeddingResponseIndexedVectorsRejectsIncompleteAllDroppedCoverage 用于验证零向量 best-effort 快捷路径仍要求 dropped 条目完整覆盖全部输入，避免异常适配器把未声明结果静默吞掉。
func TestEmbeddingResponseIndexedVectorsRejectsIncompleteAllDroppedCoverage(t *testing.T) {
	resp := EmbeddingResponse{
		Dropped: []EmbeddingDroppedInput{
			{Index: 1, Text: "too long two", Reason: "input_too_large"},
		},
	}

	items, err := resp.IndexedVectors(3)
	if err == nil {
		t.Fatalf("IndexedVectors error = nil, items = %+v, want coverage error", items)
	}
}
