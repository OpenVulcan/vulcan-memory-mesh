// precheck_score.go centralizes reviewer-facing explanations for the final candidate score used by pre-check second-stage review.
// precheck_score.go 用于集中管理 pre-check 第二层评审里候选最终分数的 reviewer 解释。
package usecase

import (
	"fmt"
	"strings"
)

// preCheckScoreExplanation stores one compact reviewer-facing score label and explanation derived from the final pre-check candidate score.
// preCheckScoreExplanation 用于保存从 pre-check 候选最终分数推导出的紧凑 reviewer 标签和解释文本。
type preCheckScoreExplanation struct {
	Label       string
	Explanation string
}

// describePreCheckCandidateScore converts the final ranking score into one stable reviewer-facing label plus explanation, while also summarizing whether the current matched context evidence boosted or demoted the candidate.
// describePreCheckCandidateScore 用于把最终排序分转换成稳定的 reviewer 标签和说明，并补充当前命中情境证据是加分还是减分。
func describePreCheckCandidateScore(score, threshold, matchedContextDelta float64) preCheckScoreExplanation {
	label := "Borderline Match"
	switch {
	case score >= 0.92:
		label = "Very Strong Match"
	case score >= 0.85:
		label = "Strong Match"
	case score >= clampPreCheckThreshold(threshold):
		label = "Usable Match"
	}

	explanation := fmt.Sprintf("当前最终分数为 %.3f，候选阈值为 %.3f。", score, clampPreCheckThreshold(threshold))
	switch {
	case matchedContextDelta > 0:
		explanation += fmt.Sprintf(" 当前 query 命中的 context evidence 额外带来了 %+0.3f 的正向增益。", matchedContextDelta)
	case matchedContextDelta < 0:
		explanation += fmt.Sprintf(" 当前 query 命中的 context evidence 带来了 %0.3f 的负向调整。", matchedContextDelta)
	default:
		explanation += " 当前分数主要由召回、融合和重排链本身决定。"
	}
	return preCheckScoreExplanation{
		Label:       label,
		Explanation: explanation,
	}
}

// normalizePreCheckReviewScore converts one unified-search hit score into the reviewer-facing score semantics expected by pre-check thresholding.
// normalizePreCheckReviewScore 用于把 unified-search 命中的分数转换成 pre-check 阈值所期待的 reviewer 分数语义。
func normalizePreCheckReviewScore(score float64, origin string, rank, total int) float64 {
	score = clampUnitScore(score)
	origin = strings.TrimSpace(origin)

	// Rerank providers often return model-specific relevance scores that do not share the same threshold semantics as vector similarity.
	// When pre-check builds numbered candidates, keep the reranked order but floor the reviewer-facing score by stable rank normalization
	// so top reranked memories are not incorrectly discarded or mislabeled as weak matches.
	// rerank provider 往往返回模型自定义相关性分数，它与向量相似度阈值并不共用同一语义。
	// 因此 pre-check 在构造编号候选时，需要保留 rerank 顺序，但同时用稳定的排名归一分作为下限，
	// 避免顶部 rerank 记忆被错误过滤，或被误标成弱匹配。
	if strings.Contains(origin, "rerank") {
		if rankScore := normalizedRankScore(rank, total); rankScore > score {
			return rankScore
		}
	}
	return score
}

// clampPreCheckThreshold keeps the reviewer-facing threshold explanation stable even if config normalization has not been applied at the call site.
// clampPreCheckThreshold 用于在调用点尚未做配置归一时，仍保证 reviewer 看到稳定的阈值说明。
func clampPreCheckThreshold(threshold float64) float64 {
	if threshold <= 0 || threshold > 1 {
		return defaultPreCheckSimilarity
	}
	return threshold
}
