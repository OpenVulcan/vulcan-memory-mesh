// precheck_memory_reviewer_test.go verifies the parser-level contract of the second-stage pre-check memory reviewer.
// precheck_memory_reviewer_test.go 用于验证 pre-check 第二层记忆评审器的解析器级契约。
package processor

import (
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers verifies the reviewer parser keeps only valid deduplicated candidate numbers from the current candidate set.
// TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers 用于验证评审器解析器只保留当前候选集合中合法且去重后的候选编号。
func TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers(t *testing.T) {
	result, err := parsePreCheckMemoryReviewResponse("```json\n{\"selected_candidate_numbers\":[1,2,1],\"reason\":\"useful\"}\n```", logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{CandidateNumber: 1, MemoryID: 12},
			{CandidateNumber: 2, MemoryID: 15},
		},
	})
	if err != nil {
		t.Fatalf("parse review response: %v", err)
	}
	if len(result.SelectedCandidateNumbers) != 2 || result.SelectedCandidateNumbers[0] != 1 || result.SelectedCandidateNumbers[1] != 2 {
		t.Fatalf("unexpected selected numbers: %#v", result.SelectedCandidateNumbers)
	}
	if result.Reason != "useful" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

// TestParsePreCheckMemoryReviewResponseRejectsUnknownNumbers verifies the reviewer parser rejects candidate numbers that were not present in the current request.
// TestParsePreCheckMemoryReviewResponseRejectsUnknownNumbers 用于验证评审器解析器会拒绝本次请求中不存在的候选编号。
func TestParsePreCheckMemoryReviewResponseRejectsUnknownNumbers(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{"selected_candidate_numbers":[99]}`, logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{{CandidateNumber: 1, MemoryID: 15}},
	})
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}
