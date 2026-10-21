// Review-threshold filtering for caller-facing memory search.
// 面向调用方记忆检索的审核阈值过滤。
package usecase

import (
	"math"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ConfigureSearchReviewThreshold sets the default threshold for searches that request review filtering; it is the pre-check threshold so both judge relevance alike.
// ConfigureSearchReviewThreshold 用于设置请求审核过滤之检索的默认阈值；它与 pre-check 阈值一致，使二者按同一标准判断相关度。
func (u *MemoryUseCase) ConfigureSearchReviewThreshold(minScore float64) {
	if u == nil {
		return
	}
	u.searchReviewThreshold = clampPreCheckThreshold(minScore)
}

// validateMemorySearchMinScore rejects a caller threshold outside the reviewer-facing unit scale instead of clamping it.
// validateMemorySearchMinScore 用于拒绝超出审核单位尺度的调用方阈值，而不是钳制它。
func validateMemorySearchMinScore(minScore *float64) error {
	if minScore == nil {
		return nil
	}
	if math.IsNaN(*minScore) || *minScore < 0 || *minScore > 1 {
		return logicdomain.ValidationError{Field: "min_score", Message: "must be a number in [0, 1]"}
	}
	return nil
}

// applySearchReviewThreshold scores each hit exactly as pre-check reviews its candidates and keeps only those at or above the threshold, counting the rest.
// applySearchReviewThreshold 用于按 pre-check 审核候选的同一方式为每个命中评分，只保留不低于阈值者并统计其余命中。
func (u *MemoryUseCase) applySearchReviewThreshold(result *MemoryQueryResult, minScore *float64) {
	threshold := u.searchReviewThreshold
	if minScore != nil {
		threshold = *minScore
	}
	for idx := range result.Results {
		group := &result.Results[idx]
		kept := make([]MemoryQueryHit, 0, len(group.Hits))
		for rank, hit := range group.Hits {
			if normalizePreCheckReviewScore(hit.Score, hit.Origin, rank+1, len(group.Hits)) < threshold {
				group.BelowThresholdCount++
				continue
			}
			kept = append(kept, hit)
		}
		group.Hits = kept
	}
	result.MinScore = threshold
}
