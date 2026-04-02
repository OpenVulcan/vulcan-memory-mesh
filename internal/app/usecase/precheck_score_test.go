// precheck_score_test.go verifies the reviewer-facing score explanations exposed by pre-check candidates.
// precheck_score_test.go 用于验证 pre-check 候选对 reviewer 暴露的分数解释。
package usecase

import "testing"

// TestDescribePreCheckCandidateScoreWithPositiveContextDelta verifies that a strong final score with positive matched-context evidence produces the expected high-confidence label and explanation.
// TestDescribePreCheckCandidateScoreWithPositiveContextDelta 用于验证高分且带正向情境增益的候选会生成预期的高置信标签和说明。
func TestDescribePreCheckCandidateScoreWithPositiveContextDelta(t *testing.T) {
	got := describePreCheckCandidateScore(0.96, 0.80, 0.075)
	if got.Label != "Very Strong Match" {
		t.Fatalf("unexpected label: %#v", got)
	}
	if got.Explanation == "" {
		t.Fatalf("expected non-empty explanation, got %#v", got)
	}
}

// TestDescribePreCheckCandidateScoreWithoutContextDelta verifies that a near-threshold score without matched context evidence falls back to the ranking-only explanation path.
// TestDescribePreCheckCandidateScoreWithoutContextDelta 用于验证接近阈值且没有情境增减分的候选会回退到纯排序解释路径。
func TestDescribePreCheckCandidateScoreWithoutContextDelta(t *testing.T) {
	got := describePreCheckCandidateScore(0.83, 0.80, 0)
	if got.Label != "Usable Match" {
		t.Fatalf("unexpected label: %#v", got)
	}
	if got.Explanation != "当前最终分数为 0.830，候选阈值为 0.800。 当前分数主要由召回、融合和重排链本身决定。" {
		t.Fatalf("unexpected explanation: %#v", got)
	}
}
