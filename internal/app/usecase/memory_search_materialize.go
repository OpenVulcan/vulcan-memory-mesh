// Memory hit materialization from durable relational rows.
// 应用用例层从长期关系行物化记忆检索结果。
package usecase

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"sort"
	"strings"
	"time"
)

// mapSearchHits enriches vector hits with relational unified-memory rows so the search response can return durable memory refs instead of bare turn anchors.
// mapSearchHits 用于用关系层统一记忆行补全向量命中，从而让搜索响应返回长期 memory ref，而不是裸 turn 锚点。
func (u *MemoryUseCase) mapSearchHits(ctx context.Context, hits []logicdomain.MemoryHit) ([]MemoryQueryHit, error) {
	byVectorID, err := u.materializeVectorHitRows(ctx, hits)
	if err != nil {
		return nil, err
	}
	if len(byVectorID) == 0 {
		return []MemoryQueryHit{}, nil
	}
	mapped := make([]MemoryQueryHit, 0, len(hits))
	for _, hit := range hits {
		row, ok := byVectorID[strings.TrimSpace(hit.ID)]
		if !ok {
			continue
		}
		// Require source attribution before response mapping so later rerank or MMR stages cannot hide a backend contract violation.
		// 在响应映射前强制校验来源归属，避免后续 rerank 或 MMR 阶段掩盖后端契约违例。
		origin, err := requiredRawMemoryHitOrigin(hit)
		if err != nil {
			return nil, err
		}
		mappedHit := MemoryQueryHit{
			MemoryRef: logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeMemory,
				ID:   row.ID,
			},
			SourceKind:               row.SourceKind,
			ScopeLevel:               row.ScopeLevel,
			Priority:                 row.Priority,
			MemoryLevel:              row.MemoryLevel,
			RefreshWeight:            row.RefreshWeight,
			SessionID:                chooseSearchSessionID(hit, row),
			Abstract:                 strings.TrimSpace(row.Abstract),
			DetailsPreview:           strings.TrimSpace(row.Details),
			Category:                 row.Category,
			Score:                    hit.Score,
			Origin:                   origin,
			Vector:                   append([]float32(nil), row.Vector...),
			CreatedAt:                row.CreatedAt,
			LastRecalledAt:           row.LastRecalledAt,
			LastAdoptedAt:            row.LastAdoptedAt,
			LastReinforcedAt:         row.LastReinforcedAt,
			SupportCount:             row.SupportCount,
			RebuttalCount:            row.RebuttalCount,
			ReinforcementCount:       row.ReinforcementCount,
			CrossSessionAdoptedCount: row.CrossSessionAdoptedCount,
			DecayDisabled:            row.DecayDisabled,
		}
		if row.SourceTurnID > 0 {
			mappedHit.SourceRef = logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeTurn,
				ID:   row.SourceTurnID,
			}
		}
		mapped = append(mapped, mappedHit)
	}
	sort.SliceStable(mapped, func(i, j int) bool {
		if mapped[i].Score == mapped[j].Score {
			return mapped[i].MemoryRef.ID < mapped[j].MemoryRef.ID
		}
		return mapped[i].Score > mapped[j].Score
	})
	return mapped, nil
}

// materializeVectorHitRows returns durable memory rows for vector hits, preferring rows embedded by combined backends and only querying by vector id for detached backends that cannot materialize memory rows themselves.
// materializeVectorHitRows 用于返回向量命中的长期记忆行；优先使用组合后端随命中携带的行，仅在分离后端无法自行物化记忆行时才按 vector id 回表。
func (u *MemoryUseCase) materializeVectorHitRows(ctx context.Context, hits []logicdomain.MemoryHit) (map[string]logicdomain.MemoryNodeRecord, error) {
	byVectorID := make(map[string]logicdomain.MemoryNodeRecord, len(hits))
	if len(hits) == 0 {
		return byVectorID, nil
	}
	now := time.Now().UTC()
	for idx, hit := range hits {
		if hit.Record.ID == 0 {
			continue
		}
		vectorID := strings.TrimSpace(hit.ID)
		if err := validateMaterializedVectorHit(idx, vectorID, hit.Record); err != nil {
			return nil, err
		}
		// Keep the app-layer lifecycle guard for embedded combined-store rows so stale test fixtures or future callers cannot bypass the active+unexpired recall contract.
		// 对组合库随命中携带的行继续保留应用层生命周期守卫，避免过期测试夹具或未来调用方绕过 active+unexpired 召回契约。
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(hit.Record, now) {
			continue
		}
		byVectorID[vectorID] = hit.Record
	}

	vectorIDs := collectMissingVectorHitRowIDs(hits, byVectorID)
	if len(vectorIDs) == 0 {
		return byVectorID, nil
	}
	rows, err := u.memories.LoadMemoryNodesByVectorIDs(ctx, vectorIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		vectorID := strings.TrimSpace(row.VectorID)
		if vectorID == "" {
			return nil, fmt.Errorf("loaded memory row %d is missing vector id", row.ID)
		}
		byVectorID[vectorID] = row
	}
	return byVectorID, nil
}

// validateMaterializedVectorHit enforces the combined-backend contract that a hit carrying a durable row must point at exactly the same vector id as that row.
// validateMaterializedVectorHit 用于校验组合后端契约：携带长期记忆行的命中必须指向与该行完全一致的 vector id。
func validateMaterializedVectorHit(idx int, vectorID string, row logicdomain.MemoryNodeRecord) error {
	if strings.TrimSpace(vectorID) == "" {
		return fmt.Errorf("vector hit at rank %d carries materialized row %d but is missing vector id", idx+1, row.ID)
	}
	rowVectorID := strings.TrimSpace(row.VectorID)
	if rowVectorID == "" {
		return fmt.Errorf("vector hit %q carries materialized row %d without vector id", vectorID, row.ID)
	}
	if rowVectorID != vectorID {
		return fmt.Errorf("vector hit %q materialized row %d vector id mismatch: got %q", vectorID, row.ID, rowVectorID)
	}
	return nil
}

// collectMissingVectorHitRowIDs returns the detached vector ids that still need relational backfill after embedded rows have been consumed.
// collectMissingVectorHitRowIDs 用于返回消费完随命中携带的行之后，仍然需要关系层补全的分离向量 id。
func collectMissingVectorHitRowIDs(hits []logicdomain.MemoryHit, byVectorID map[string]logicdomain.MemoryNodeRecord) []string {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hits))
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		vectorID := strings.TrimSpace(hit.ID)
		if vectorID == "" {
			continue
		}
		if _, ok := byVectorID[vectorID]; ok {
			continue
		}
		if _, ok := seen[vectorID]; ok {
			continue
		}
		seen[vectorID] = struct{}{}
		ids = append(ids, vectorID)
	}
	return ids
}
