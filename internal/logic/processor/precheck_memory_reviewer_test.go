// precheck_memory_reviewer_test.go verifies the parser-level contract of the second-stage pre-check memory reviewer.
// precheck_memory_reviewer_test.go 用于验证 pre-check 第二层记忆评审器的解析器级契约。
package processor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TestPreCheckMemoryReviewerSendsExactStructuredOutput verifies the second-stage pre-check carries its decoder-derived schema.
// TestPreCheckMemoryReviewerSendsExactStructuredOutput 用于验证第二阶段预检查携带由解码结构生成的 Schema。
func TestPreCheckMemoryReviewerSendsExactStructuredOutput(t *testing.T) {
	llm := &stubProfileMergerLLM{response: logicports.LLMResponse{Content: `{"selected_candidate_numbers":[]}`}}
	prompts := &stubProfilePromptSource{prompt: "return json only"}
	reviewer := NewPreCheckMemoryReviewer(llm, prompts, "test-model")

	if _, err := reviewer.Review(context.Background(), logicdomain.PreCheckMemoryReviewInput{}); err != nil {
		t.Fatalf("review pre-check memory: %v", err)
	}
	assertStructuredOutputRequest(t, llm.request, "vmm_precheck_memory_review")
}

// TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers verifies the reviewer parser keeps only valid deduplicated candidate numbers from the current candidate set.
// TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers 用于验证评审器解析器只保留当前候选集合中合法且去重后的候选编号。
func TestParsePreCheckMemoryReviewResponseAcceptsKnownNumbers(t *testing.T) {
	result, err := parsePreCheckMemoryReviewResponse("```json\n{\"selected_candidate_numbers\":[1,2,1]}\n```", logicdomain.PreCheckMemoryReviewInput{
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
}

// TestParsePreCheckMemoryReviewResponseAcceptsNumberOnlyOutput verifies the current pre-check L2 contract only needs selected candidate numbers.
// TestParsePreCheckMemoryReviewResponseAcceptsNumberOnlyOutput 用于验证当前 pre-check L2 契约只需要返回已选候选编号。
func TestParsePreCheckMemoryReviewResponseAcceptsNumberOnlyOutput(t *testing.T) {
	result, err := parsePreCheckMemoryReviewResponse(`{"selected_candidate_numbers":[2,1]}`, logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{CandidateNumber: 1, MemoryID: 12},
			{CandidateNumber: 2, MemoryID: 15},
		},
	})
	if err != nil {
		t.Fatalf("parse compact review response: %v", err)
	}
	if len(result.SelectedCandidateNumbers) != 2 || result.SelectedCandidateNumbers[0] != 2 || result.SelectedCandidateNumbers[1] != 1 {
		t.Fatalf("unexpected selected numbers: %#v", result.SelectedCandidateNumbers)
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

// TestParsePreCheckMemoryReviewResponseRejectsMixedUnknownNumbers verifies partially malformed selections fail instead of being silently repaired.
// TestParsePreCheckMemoryReviewResponseRejectsMixedUnknownNumbers 用于验证部分畸形的选择会直接失败，而不是被静默修复。
func TestParsePreCheckMemoryReviewResponseRejectsMixedUnknownNumbers(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{"selected_candidate_numbers":[99,2]}`, logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{CandidateNumber: 1, MemoryID: 12},
			{CandidateNumber: 2, MemoryID: 15},
		},
	})
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}

// TestParsePreCheckMemoryReviewResponseRejectsLegacyMemoryIDOutput verifies the parser no longer accepts the removed memory-id fallback field.
// TestParsePreCheckMemoryReviewResponseRejectsLegacyMemoryIDOutput 用于验证解析器不再接受已经移除的 memory-id 回退字段。
func TestParsePreCheckMemoryReviewResponseRejectsLegacyMemoryIDOutput(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{"selected_candidate_numbers":[2],"selected_memory_ids":[12]}`, logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{CandidateNumber: 1, MemoryID: 12},
			{CandidateNumber: 2, MemoryID: 15},
		},
	})
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}

// TestParsePreCheckMemoryReviewResponseRejectsLegacyReasonOutput verifies the parser rejects old rationale-only compatibility fields after the prompt contract was narrowed.
// TestParsePreCheckMemoryReviewResponseRejectsLegacyReasonOutput 用于验证 prompt 契约收窄后，解析器会拒绝旧的理由兼容字段。
func TestParsePreCheckMemoryReviewResponseRejectsLegacyReasonOutput(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{"selected_candidate_numbers":[2],"reason":"useful"}`, logicdomain.PreCheckMemoryReviewInput{
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{CandidateNumber: 1, MemoryID: 12},
			{CandidateNumber: 2, MemoryID: 15},
		},
	})
	if err == nil {
		t.Fatal("expected invalid output error")
	}
}

// TestParsePreCheckMemoryReviewResponseRejectsMissingSelectedNumbers verifies selected_candidate_numbers is required even when the JSON object is syntactically valid.
// TestParsePreCheckMemoryReviewResponseRejectsMissingSelectedNumbers 用于验证即使 JSON 对象语法合法，也必须显式提供 selected_candidate_numbers。
func TestParsePreCheckMemoryReviewResponseRejectsMissingSelectedNumbers(t *testing.T) {
	_, err := parsePreCheckMemoryReviewResponse(`{}`, logicdomain.PreCheckMemoryReviewInput{
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
		CurrentTimestamp: 1775000008000,
		UserContent:      "在 local oss 的 phase4 下应该继续用哪个方案？",
		SearchQueries:    []string{"local oss 下的 phase4 当前方案"},
		IntentReason:     "needs environment-specific architecture memory",
		Candidates: []logicdomain.PreCheckMemoryCandidate{
			{
				CandidateNumber:            1,
				MemoryID:                   15,
				CreatedTimestamp:           1775000001000,
				Abstract:                   "phase4 新方案",
				Details:                    "适用于 local oss 当前环境。",
				Score:                      0.96,
				ScoreLabel:                 "Very Strong Match",
				ScoreExplanation:           "当前最终分数为 0.960，候选阈值为 0.800。 当前 query 命中的 context evidence 额外带来了 +0.075 的正向增益。",
				Origin:                     "hybrid_rrf_rerank_mmr",
				OriginLabel:                "Hybrid RRF + Rerank + MMR",
				OriginExplanation:          "候选先经过 hybrid RRF 融合，再被 rerank 模型重排，并额外经过 MMR 多样性控制。",
				MatchedContextValues:       []string{"deployment_mode=local oss", "deployment mode = LOCAL_OSS", "task-stage= phase4"},
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
	assertCompactJSONPrompt(t, rendered)
	for _, fragment := range []string{`"current_datetime":"2026-04-01 07:33:28"`, `"created_datetime":"2026-04-01 07:33:21"`} {
		if !strings.Contains(rendered, fragment) {
			t.Fatalf("expected rendered request to expose %s, got %s", fragment, rendered)
		}
	}
	for _, removed := range []string{`"current_date"`, `"created_date"`} {
		if strings.Contains(rendered, removed) {
			t.Fatalf("expected rendered request to stop exposing %s, got %s", removed, rendered)
		}
	}
	if strings.Contains(rendered, `"current_timestamp"`) || strings.Contains(rendered, `"created_timestamp"`) {
		t.Fatalf("expected rendered request to stop exposing raw timestamps, got %s", rendered)
	}
	if len(payload.Candidates) != 1 {
		t.Fatalf("unexpected candidates: %#v", payload.Candidates)
	}
	if payload.Candidates[0].CreatedDateTime != "2026-04-01 07:33:21" {
		t.Fatalf("expected candidate created datetime to be derived from timestamp, got %#v", payload.Candidates[0])
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
