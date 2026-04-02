// precheck_memory_reviewer_test.go verifies the parser-level contract of the second-stage pre-check memory reviewer.
// precheck_memory_reviewer_test.go 用于验证 pre-check 第二层记忆评审器的解析器级契约。
package processor

import (
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestParsePreCheckMemoryReviewResponseAcceptsKnownIDs verifies the reviewer parser keeps only valid deduplicated ids from the current candidate set.
// TestParsePreCheckMemoryReviewResponseAcceptsKnownIDs 用于验证评审器解析器只保留当前候选集合中合法且去重后的 id。
func TestParsePreCheckMemoryReviewResponseAcceptsKnownIDs(t *testing.T) {
	result, err := parsePreCheckMemoryReviewResponse("```json\n{\"selected_memory_ids\":[12,15,12],\"reason\":\"useful\"}\n```", logicdomain.PreCheckMemoryReviewInput{
		RecentSessionMemories: []logicdomain.PreCheckMemoryCandidate{{MemoryID: 12}},
		RetrievedMemories:     []logicdomain.PreCheckMemoryCandidate{{MemoryID: 15}},
	})
	if err != nil {
		t.Fatalf("parse review response: %v", err)
	}
	if len(result.SelectedMemoryIDs) != 2 || result.SelectedMemoryIDs[0] != 12 || result.SelectedMemoryIDs[1] != 15 {
		t.Fatalf("unexpected selected ids: %#v", result.SelectedMemoryIDs)
	}
	if result.Reason != "useful" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

// TestParsePreCheckMemoryReviewResponseRejectsUnknownIDs verifies the reviewer parser rejects ids that were not present in the current request.
// TestParsePreCheckMemoryReviewResponseRejectsUnknownIDs 用于验证评审器解析器会拒绝本次请求中不存在的 id。
func TestParsePreCheckMemoryReviewResponseRejectsUnknownIDs(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{"selected_memory_ids":[99]}`, logicdomain.PreCheckMemoryReviewInput{
		RetrievedMemories: []logicdomain.PreCheckMemoryCandidate{{MemoryID: 15}},
	})
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}
