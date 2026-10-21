// Behavioral tests grouped by precheck context test.
// 应用用例层 pre-check 上下文与采纳行为测试，沿用同包夹具验证对应业务链路。
package usecase

import (
	"context"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"testing"
	"time"
)

// TestBuildPreCheckMemoryTextDeduplicatesFormattingVariants verifies that abstract/details pairs which differ only by whitespace or casing are collapsed into one injected line instead of being repeated as two lines in the final context.
// TestBuildPreCheckMemoryTextDeduplicatesFormattingVariants 用于验证当 abstract/details 只存在空白或大小写差异时，最终注入文本会折叠成一行，而不是重复两次。
func TestBuildPreCheckMemoryTextDeduplicatesFormattingVariants(t *testing.T) {
	text := buildPreCheckMemoryText(logicdomain.PreCheckMemoryCandidate{
		Abstract: "SQLite schema 13 compatibility",
		Details:  " sqlite   schema 13\ncompatibility ",
	})
	if text != "SQLite schema 13 compatibility" {
		t.Fatalf("expected duplicate formatting variants to collapse to abstract, got %q", text)
	}
}

// TestPreCheckExecuteDeduplicatesEquivalentFinalInjectionText verifies pre-check removes equivalent duplicate injected memory text only at the final assembly stage, while still preserving adoption write-back for every reviewer-selected memory id.
// TestPreCheckExecuteDeduplicatesEquivalentFinalInjectionText 用于验证 pre-check 只会在最终组装阶段去掉等价的重复注入文本，同时仍会为 reviewer 选中的全部 memory id 保留 adoption 写回。
func TestPreCheckExecuteDeduplicatesEquivalentFinalInjectionText(t *testing.T) {
	store := &stubPreCheckStore{}
	reviewer := &stubPreCheckReviewer{
		result: logicdomain.PreCheckMemoryReviewResult{
			SelectedCandidateNumbers: []int{1, 2},
		},
	}
	assembler := &stubPreCheckAssembler{
		text: "assembled",
		items: []logicdomain.ContextItem{
			{Kind: "memory", Title: "混合召回记忆", Text: "assembled"},
		},
	}
	uc := NewPreCheckUseCase(
		&stubPreCheckMemories{
			result: MemoryQueryResult{
				Results: []MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "SQLite 兼容性",
						Hits: []MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 301},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 18},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       "SQLite schema 13 compatibility",
								DetailsPreview: "",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.93,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 302},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 19},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								Abstract:       " sqlite   schema 13 compatibility ",
								DetailsPreview: "",
								Category:       logicdomain.MemoryNodeCategoryArchitectureDecision,
								Score:          0.92,
								Origin:         "hybrid_rrf_rerank_mmr",
							},
						},
					},
				},
			},
		},
		store,
		&stubPreCheckIntentExtractor{
			result: logicdomain.IntentResult{
				Queries:    []string{"SQLite 兼容性"},
				NeedMemory: true,
				Reason:     "needs compatibility memory",
			},
		},
		reviewer,
		assembler,
		PreCheckConfig{TopK: 4, MinSimilarityScore: 0.8, ReviewCandidateLimit: 4},
		nil,
	)

	result, err := uc.Execute(context.Background(), PreCheckCommand{
		Session: logicdomain.SessionRef{
			SessionID:  41,
			SessionKey: "sess-1",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		UserContent: "我们现在 SQLite schema 13 兼容性怎么处理？",
	})
	if err != nil {
		t.Fatalf("execute pre-check: %v", err)
	}
	if !result.ShouldInject {
		t.Fatalf("expected pre-check to keep assembled injection result, got %#v", result)
	}
	if len(store.adoptedIDs) != 2 || store.adoptedIDs[0] != 301 || store.adoptedIDs[1] != 302 {
		t.Fatalf("expected adoption to preserve all reviewer-selected memory ids, got %#v", store.adoptedIDs)
	}
	if len(assembler.hits) != 1 {
		t.Fatalf("expected final assembly to receive one deduplicated hit, got %#v", assembler.hits)
	}
	if assembler.hits[0].ID != "301" || assembler.hits[0].Text != "SQLite schema 13 compatibility" {
		t.Fatalf("expected first selected memory text to survive dedupe, got %#v", assembler.hits[0])
	}
}

// TestPreCheckWriteMemoryAdoptionSyncsReturnedRecordsToVectorLifecycle verifies relational adoption changes are mirrored into the vector sidecar.
// TestPreCheckWriteMemoryAdoptionSyncsReturnedRecordsToVectorLifecycle 用于验证关系侧采纳变更会同步镜像到向量 sidecar。
func TestPreCheckWriteMemoryAdoptionSyncsReturnedRecordsToVectorLifecycle(t *testing.T) {
	expiresAt := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	store := &stubPreCheckStore{
		adoptedRecords: []logicdomain.MemoryRecord{
			{
				ID:        "memory-301",
				Vector:    []float32{0.1, 0.2},
				Text:      "SQLite uses local lifecycle filtering.",
				Filter:    logicdomain.SearchFilter{TeamID: 1, SpaceID: 2, ProjectID: 3, UserID: 4, SessionID: 41},
				Status:    logicdomain.MemoryStatusActive,
				ExpiresAt: expiresAt,
			},
		},
	}
	lifecycle := &stubPreCheckLifecycleVectorStore{}
	uc := &PreCheckUseCase{store: store}
	uc.ConfigureMemoryAdoptionVectorSync(lifecycle)

	err := uc.writeMemoryAdoption(context.Background(), logicdomain.SessionRef{SessionID: 41}, []logicdomain.PreCheckMemoryCandidate{
		{MemoryID: 301},
	})
	if err != nil {
		t.Fatalf("writeMemoryAdoption returned error: %v", err)
	}
	if len(store.adoptedIDs) != 1 || store.adoptedIDs[0] != 301 {
		t.Fatalf("unexpected adopted ids: %#v", store.adoptedIDs)
	}
	if len(lifecycle.records) != 1 {
		t.Fatalf("expected one vector lifecycle upsert, got %#v", lifecycle.records)
	}
	if lifecycle.records[0].ID != "memory-301" || !lifecycle.records[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected vector lifecycle record: %#v", lifecycle.records[0])
	}
}

// TestPreCheckWriteMemoryAdoptionSkipsStoreWithoutValidMemoryIDs verifies invalid in-memory candidates cannot delegate an empty adoption mutation to the relational store.
// TestPreCheckWriteMemoryAdoptionSkipsStoreWithoutValidMemoryIDs 用于验证无效内存候选不会把空采纳写入下放到关系存储。
func TestPreCheckWriteMemoryAdoptionSkipsStoreWithoutValidMemoryIDs(t *testing.T) {
	store := &stubPreCheckStore{}
	uc := &PreCheckUseCase{store: store}

	err := uc.writeMemoryAdoption(context.Background(), logicdomain.SessionRef{}, []logicdomain.PreCheckMemoryCandidate{
		{MemoryID: 0},
	})
	if err != nil {
		t.Fatalf("writeMemoryAdoption returned error: %v", err)
	}
	if store.adoptionCalls != 0 {
		t.Fatalf("expected empty memory-id adoption to skip store, got %d calls", store.adoptionCalls)
	}
}
