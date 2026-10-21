package usecase

import (
	"errors"
	"math"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestApplySearchReviewThresholdPartitionsHits verifies the default and caller thresholds split hits by review score and count the dropped ones.
// TestApplySearchReviewThresholdPartitionsHits 用于验证默认阈值与调用方阈值按审核评分划分命中并统计被剔除者。
func TestApplySearchReviewThresholdPartitionsHits(t *testing.T) {
	newResult := func() MemoryQueryResult {
		return MemoryQueryResult{Results: []MemoryQueryGroupResult{{
			Hits: []MemoryQueryHit{{Score: 0.9, Origin: "vector"}, {Score: 0.6, Origin: "vector"}, {Score: 0.4, Origin: "vector"}},
		}}}
	}
	memory := &MemoryUseCase{}
	memory.ConfigureSearchReviewThreshold(0)

	result := newResult()
	memory.applySearchReviewThreshold(&result, nil)
	if got := len(result.Results[0].Hits); got != 1 || result.Results[0].BelowThresholdCount != 2 {
		t.Fatalf("default threshold kept %d hits and dropped %d", got, result.Results[0].BelowThresholdCount)
	}
	if result.MinScore != defaultPreCheckSimilarity {
		t.Fatalf("applied threshold = %v, want the pre-check default", result.MinScore)
	}

	lowered := 0.5
	result = newResult()
	memory.applySearchReviewThreshold(&result, &lowered)
	if got := len(result.Results[0].Hits); got != 2 || result.Results[0].BelowThresholdCount != 1 || result.MinScore != lowered {
		t.Fatalf("caller threshold kept %d hits, dropped %d, applied %v", got, result.Results[0].BelowThresholdCount, result.MinScore)
	}
}

// TestValidateMemorySearchMinScoreRejectsOutOfRange verifies an out-of-range threshold is rejected instead of clamped.
// TestValidateMemorySearchMinScoreRejectsOutOfRange 用于验证超出范围的阈值被拒绝而不是被钳制。
func TestValidateMemorySearchMinScoreRejectsOutOfRange(t *testing.T) {
	for _, value := range []float64{-0.1, 1.1, math.NaN()} {
		err := validateMemorySearchMinScore(&value)
		var validation logicdomain.ValidationError
		if !errors.As(err, &validation) || validation.Field != "min_score" {
			t.Fatalf("min_score %v: got %v", value, err)
		}
	}
	for _, value := range []float64{0, 0.5, 1} {
		if err := validateMemorySearchMinScore(&value); err != nil {
			t.Fatalf("min_score %v rejected: %v", value, err)
		}
	}
	if err := validateMemorySearchMinScore(nil); err != nil {
		t.Fatalf("omitted min_score rejected: %v", err)
	}
}
