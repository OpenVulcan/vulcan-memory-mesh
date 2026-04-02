// precheck_origin_test.go verifies the reviewer-facing retrieval-origin explanations exposed by pre-check candidates.
// precheck_origin_test.go 用于验证 pre-check 候选对 reviewer 暴露的检索来源解释。
package usecase

import "testing"

// TestDescribePreCheckMemoryOriginExplainsHybridRerankMMR verifies that the most layered retrieval path still renders a stable human-readable label and explanation.
// TestDescribePreCheckMemoryOriginExplainsHybridRerankMMR 用于验证最复杂的检索路径仍能渲染成稳定的人类可读标签和说明。
func TestDescribePreCheckMemoryOriginExplainsHybridRerankMMR(t *testing.T) {
	got := describePreCheckMemoryOrigin("hybrid_rrf_rerank_mmr")
	if got.Label != "Hybrid RRF + Rerank + MMR" {
		t.Fatalf("unexpected label: %#v", got)
	}
	if got.Explanation == "" {
		t.Fatalf("expected non-empty explanation, got %#v", got)
	}
}

// TestDescribePreCheckMemoryOriginExplainsVectorOnly verifies that a plain vector-only recall path keeps a direct explanation instead of falling back to a generic unknown string.
// TestDescribePreCheckMemoryOriginExplainsVectorOnly 用于验证纯向量召回路径会得到直接说明，而不是退回泛化的 unknown 文本。
func TestDescribePreCheckMemoryOriginExplainsVectorOnly(t *testing.T) {
	got := describePreCheckMemoryOrigin("vector_search")
	if got.Label != "Vector Recall" {
		t.Fatalf("unexpected label: %#v", got)
	}
	if got.Explanation != "候选仅通过向量语义召回命中，未经过 lexical 融合或 rerank 重排。" {
		t.Fatalf("unexpected explanation: %#v", got)
	}
}
