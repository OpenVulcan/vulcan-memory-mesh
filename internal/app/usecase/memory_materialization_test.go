// Memory use-case tests for memory materialization test.
// 记忆应用用例层检索结果物化测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestMaterializeLexicalHitsUsesEmbeddedRowsAndSkipsRetiredRows verifies lexical materialization consumes embedded rows directly while keeping the active+unexpired hot-path guard.
// TestMaterializeLexicalHitsUsesEmbeddedRowsAndSkipsRetiredRows 用于验证 lexical 物化会直接消费命中自带的行，同时继续保留 active+unexpired 热路径守卫。
func TestMaterializeLexicalHitsUsesEmbeddedRowsAndSkipsRetiredRows(t *testing.T) {
	store := &stubTurnLookupStore{memoryRowsByIDErr: errors.New("unexpected lexical backfill")}
	uc := NewMemoryUseCase(nil, store, nil, nil, nil)
	retired := logicdomain.MemoryNodeRecord{
		ID:         201,
		Status:     logicdomain.MemoryStatusSuperseded,
		SourceKind: logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel: logicdomain.MemoryScopeLevelProject,
		Abstract:   "旧阶段 A",
		Details:    "这条记忆已经被新阶段覆盖。",
		Vector:     []float32{0.1, 0.2, 0.3},
	}
	active := logicdomain.MemoryNodeRecord{
		ID:         202,
		Status:     logicdomain.MemoryStatusActive,
		SourceKind: logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel: logicdomain.MemoryScopeLevelProject,
		Abstract:   "当前阶段 B",
		Details:    "这条记忆仍处于热路径。",
		Vector:     []float32{0.4, 0.5, 0.6},
	}

	hits, err := uc.materializeLexicalHits([]logicdomain.MemoryLexicalHit{
		{MemoryID: 201, Record: retired, Score: 0.99},
		{MemoryID: 202, Record: active, Score: 0.98},
	})
	if err != nil {
		t.Fatalf("materialize lexical hits: %v", err)
	}
	if len(hits) != 1 || hits[0].MemoryRef.ID != 202 {
		t.Fatalf("expected retired lexical row to be skipped, got %+v", hits)
	}
	if len(store.memoryRowsByIDCalls) != 0 {
		t.Fatalf("expected lexical materialization to avoid row reloads, got %+v", store.memoryRowsByIDCalls)
	}
}

// TestMapSearchHitsUsesEmbeddedRowsWithoutVectorReload verifies combined vector backends can pass materialized memory rows directly into app-layer mapping without a redundant vector-id reload.
// TestMapSearchHitsUsesEmbeddedRowsWithoutVectorReload 用于验证组合向量后端可以把已物化记忆行直接传给应用层映射，而不会再次按 vector id 回表。
func TestMapSearchHitsUsesEmbeddedRowsWithoutVectorReload(t *testing.T) {
	store := &stubTurnLookupStore{}
	uc := NewMemoryUseCase(nil, store, nil, nil, nil)
	record := logicdomain.MemoryNodeRecord{
		ID:              401,
		Status:          logicdomain.MemoryStatusActive,
		SourceKind:      logicdomain.MemorySourceKindTurnExtract,
		ScopeLevel:      logicdomain.MemoryScopeLevelProject,
		Category:        logicdomain.MemoryNodeCategoryProjectContext,
		OriginSessionID: 31,
		SourceTurnID:    3101,
		VectorID:        "vec-401",
		Vector:          []float32{0.1, 0.2, 0.3},
		Abstract:        "当前阶段使用组合 PostgreSQL 检索。",
		Details:         "完整记忆行已经随向量命中返回。",
		CreatedAt:       time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC),
	}

	hits, err := uc.mapSearchHits(context.Background(), []logicdomain.MemoryHit{{
		ID:     "vec-401",
		Score:  0.91,
		Record: record,
		Metadata: map[string]string{
			"origin": "hybrid_rrf",
		},
	}})
	if err != nil {
		t.Fatalf("map search hits: %v", err)
	}
	if len(hits) != 1 || hits[0].MemoryRef.ID != 401 || hits[0].SourceRef.ID != 3101 {
		t.Fatalf("unexpected mapped hits: %+v", hits)
	}
	if hits[0].Origin != "hybrid_rrf" || hits[0].SessionID != 31 || len(hits[0].Vector) != 3 {
		t.Fatalf("expected embedded row fields to map through, got %+v", hits[0])
	}
	if len(store.memoryRowsByVectorCalls) != 0 {
		t.Fatalf("expected embedded vector hit to avoid row reloads, got %+v", store.memoryRowsByVectorCalls)
	}
}

// TestMapSearchHitsRejectsEmbeddedVectorIDMismatch verifies a combined backend cannot silently pair one vector hit with a different materialized memory row.
// TestMapSearchHitsRejectsEmbeddedVectorIDMismatch 用于验证组合后端不能把向量命中与另一个 vector id 的物化记忆行静默配对。
func TestMapSearchHitsRejectsEmbeddedVectorIDMismatch(t *testing.T) {
	store := &stubTurnLookupStore{}
	uc := NewMemoryUseCase(nil, store, nil, nil, nil)

	_, err := uc.mapSearchHits(context.Background(), []logicdomain.MemoryHit{{
		ID: "vec-actual",
		Record: logicdomain.MemoryNodeRecord{
			ID:       402,
			Status:   logicdomain.MemoryStatusActive,
			VectorID: "vec-other",
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "vector id mismatch") {
		t.Fatalf("expected vector id mismatch error, got %v", err)
	}
	if len(store.memoryRowsByVectorCalls) != 0 {
		t.Fatalf("expected mismatched embedded row to fail before reload, got %+v", store.memoryRowsByVectorCalls)
	}
}

// TestMapSearchHitsRejectsMissingOriginMetadata verifies app-layer mapping refuses vector hits whose backend did not declare a retrieval channel.
// TestMapSearchHitsRejectsMissingOriginMetadata 用于验证应用层映射会拒绝后端未声明检索通道的向量命中。
func TestMapSearchHitsRejectsMissingOriginMetadata(t *testing.T) {
	store := &stubTurnLookupStore{}
	uc := NewMemoryUseCase(nil, store, nil, nil, nil)

	_, err := uc.mapSearchHits(context.Background(), []logicdomain.MemoryHit{{
		ID:    "vec-missing-origin",
		Score: 0.88,
		Record: logicdomain.MemoryNodeRecord{
			ID:       403,
			Status:   logicdomain.MemoryStatusActive,
			VectorID: "vec-missing-origin",
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "missing origin metadata") {
		t.Fatalf("expected missing origin metadata error, got %v", err)
	}
	if len(store.memoryRowsByVectorCalls) != 0 {
		t.Fatalf("expected embedded row to fail before reload, got %+v", store.memoryRowsByVectorCalls)
	}
}

// TestNormalizeSearchOriginsPreserveMissingAttribution verifies later ranking stages do not invent a source label when upstream mapping failed to provide one.
// TestNormalizeSearchOriginsPreserveMissingAttribution 用于验证后续排序阶段不会在上游映射未提供来源时伪造来源标签。
func TestNormalizeSearchOriginsPreserveMissingAttribution(t *testing.T) {
	if got := normalizeRerankedOrigin(" "); got != "" {
		t.Fatalf("normalize reranked empty origin = %q, want empty", got)
	}
	if got := normalizeMMROrigin(" "); got != "" {
		t.Fatalf("normalize mmr empty origin = %q, want empty", got)
	}
}
