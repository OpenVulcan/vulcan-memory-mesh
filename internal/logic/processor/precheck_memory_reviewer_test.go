// precheck_memory_reviewer_test.go verifies the parser-level contract of the second-stage pre-check memory reviewer.
// precheck_memory_reviewer_test.go 用于验证 pre-check 第二层记忆评审器的解析器级契约。
package processor

import (
	"encoding/json"
	"strings"
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

// TestRenderPreCheckMemoryReviewRequestPreservesMatchedContextEvidence verifies the second-stage reviewer request keeps the matched context summary that explains why each candidate fit the current query.
// TestRenderPreCheckMemoryReviewRequestPreservesMatchedContextEvidence 用于验证第二层 reviewer 请求会保留用于解释候选为何命中当前 query 的 matched context 摘要。
func TestRenderPreCheckMemoryReviewRequestPreservesMatchedContextEvidence(t *testing.T) {
	rendered, err := renderPreCheckMemoryReviewRequest(logicdomain.PreCheckMemoryReviewInput{
		UserContent:   "在 local oss 的 phase4 下应该继续用哪个方案？",
		SearchQueries: []string{"local oss 下的 phase4 当前方案"},
		IntentReason:  "needs environment-specific architecture memory",
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{
				CandidateNumber:            1,
				MemoryID:                   15,
				Abstract:                   "phase4 新方案",
				Details:                    "适用于 local oss 当前环境。",
				Score:                      0.96,
				ScoreLabel:                 "Very Strong Match",
				ScoreExplanation:           "当前最终分数为 0.960，候选阈值为 0.800。 当前 query 命中的 context evidence 额外带来了 +0.075 的正向增益。",
				Origin:                     "hybrid_rrf_rerank_mmr",
				OriginLabel:                "Hybrid RRF + Rerank + MMR",
				OriginExplanation:          "候选先经过 hybrid RRF 融合，再被 rerank 模型重排，并额外经过 MMR 多样性控制。",
				MatchedContextValues:       []string{"deployment_mode=local oss", "deployment_mode=local oss", "task_stage=phase4"},
				MatchedContextSupportCount: 3,
				MatchedContextScoreDelta:   0.075,
			},
		},
	})
	if err != nil {
		t.Fatalf("render review request: %v", err)
	}
	var payload struct {
		Candidates []logicdomain.PreCheckMemoryCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("unmarshal rendered review request: %v", err)
	}
	if strings.Contains(rendered, "\"CandidateNumber\"") || !strings.Contains(rendered, "\"candidate_number\"") {
		t.Fatalf("expected snake_case candidate keys in rendered payload, got %s", rendered)
	}
	if len(payload.Candidates) != 1 {
		t.Fatalf("unexpected candidates: %#v", payload.Candidates)
	}
	if len(payload.Candidates[0].MatchedContextValues) != 2 {
		t.Fatalf("expected duplicate matched context values to be deduplicated, got %#v", payload.Candidates[0].MatchedContextValues)
	}
	if payload.Candidates[0].MatchedContextValues[0] != "deployment_mode=local oss" || payload.Candidates[0].MatchedContextValues[1] != "task_stage=phase4" {
		t.Fatalf("unexpected matched context values: %#v", payload.Candidates[0].MatchedContextValues)
	}
	if payload.Candidates[0].OriginLabel != "Hybrid RRF + Rerank + MMR" || payload.Candidates[0].OriginExplanation == "" {
		t.Fatalf("expected origin explanation fields to survive rendering, got %#v", payload.Candidates[0])
	}
	if payload.Candidates[0].ScoreLabel != "Very Strong Match" || payload.Candidates[0].ScoreExplanation == "" {
		t.Fatalf("expected score explanation fields to survive rendering, got %#v", payload.Candidates[0])
	}
}
