package usecase

import (
	"context"
	"errors"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestPreCheckExecuteReportsRecallOutcome verifies every recall branch states its outcome and discloses queries only once a search ran.
// TestPreCheckExecuteReportsRecallOutcome 用于验证每个召回分支都说明其结果，且只在检索执行后披露检索词。
func TestPreCheckExecuteReportsRecallOutcome(t *testing.T) {
	hit := MemoryQueryHit{
		MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 30},
		SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
		SourceKind:     logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel:     logicdomain.MemoryScopeLevelProject,
		Abstract:       "deploy steps",
		DetailsPreview: "deploy with the release script",
		Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
		Score:          0.94,
		Origin:         "vector_search",
	}
	needMemory := logicdomain.IntentResult{Queries: []string{"deploy steps"}, NeedMemory: true}
	cases := []struct {
		name     string
		intent   *stubPreCheckIntentExtractor
		memories *stubPreCheckMemories
		reviewer *stubPreCheckReviewer
		want     PreCheckRecallOutcome
		queries  bool
	}{
		{"not needed", &stubPreCheckIntentExtractor{result: logicdomain.IntentResult{NeedMemory: false}}, &stubPreCheckMemories{}, &stubPreCheckReviewer{}, PreCheckRecallNotSearched, false},
		{"intent failed", &stubPreCheckIntentExtractor{err: errors.New("intent down")}, &stubPreCheckMemories{}, &stubPreCheckReviewer{}, PreCheckRecallFailed, false},
		{"search failed", &stubPreCheckIntentExtractor{result: needMemory}, &stubPreCheckMemories{err: errors.New("search down")}, &stubPreCheckReviewer{}, PreCheckRecallFailed, true},
		{"nothing found", &stubPreCheckIntentExtractor{result: needMemory}, &stubPreCheckMemories{}, &stubPreCheckReviewer{}, PreCheckRecallNoMatch, true},
		{"nothing adopted", &stubPreCheckIntentExtractor{result: needMemory}, &stubPreCheckMemories{result: MemoryQueryResult{Results: []MemoryQueryGroupResult{{Query: "deploy steps", Hits: []MemoryQueryHit{hit}}}}}, &stubPreCheckReviewer{}, PreCheckRecallNoMatch, true},
		{"adopted", &stubPreCheckIntentExtractor{result: needMemory}, &stubPreCheckMemories{result: MemoryQueryResult{Results: []MemoryQueryGroupResult{{Query: "deploy steps", Hits: []MemoryQueryHit{hit}}}}}, &stubPreCheckReviewer{result: logicdomain.PreCheckMemoryReviewResult{SelectedCandidateNumbers: []int{1}}}, PreCheckRecallMatched, true},
	}
	for _, tc := range cases {
		uc := NewPreCheckUseCase(tc.memories, &stubPreCheckStore{}, tc.intent, tc.reviewer, &stubPreCheckAssembler{
			text:  "assembled adopted context",
			items: []logicdomain.ContextItem{{Kind: "memory", Text: "deploy with the release script", Score: 0.94, TurnID: 18}},
		},
			PreCheckConfig{TopK: 4, MinSimilarityScore: 0.75, HistoryTurns: 4, MaxInputTokens: 200}, nil)
		result, err := uc.Execute(trace.WithTraceID(context.Background(), "trace-recall-outcome"), PreCheckCommand{
			Session:     logicdomain.SessionRef{SessionID: 41, SessionKey: "sess-1", UserID: 7, ProjectID: 9},
			UserContent: "how do we deploy?",
		})
		if err != nil {
			t.Fatalf("%s: execute pre-check: %v", tc.name, err)
		}
		if result.RecallOutcome != tc.want {
			t.Fatalf("%s: recall outcome = %d, want %d (result %+v)", tc.name, result.RecallOutcome, tc.want, result)
		}
		if got := len(result.RecallQueries) > 0; got != tc.queries {
			t.Fatalf("%s: recall queries = %#v", tc.name, result.RecallQueries)
		}
	}
}
