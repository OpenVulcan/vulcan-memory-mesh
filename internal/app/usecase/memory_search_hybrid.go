// Memory hybrid recall and reciprocal rank fusion.
// 应用用例层记忆混合召回与倒数排名融合。
package usecase

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hybridizeSearchHits optionally augments vector hits with lexical recall and fuses both channels through RRF.
// hybridizeSearchHits 用于按需使用 lexical 召回增强向量命中，并通过 RRF 融合两个通道。
func (u *MemoryUseCase) hybridizeSearchHits(ctx context.Context, item MemoryQueryItem, filter logicdomain.SearchFilter, poolK int, vectorHits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.hybridEnabled || u.memories == nil {
		return trimSearchHits(vectorHits, poolK)
	}
	query := buildMemoryLexicalQuery(item)
	if strings.TrimSpace(query) == "" {
		return trimSearchHits(vectorHits, poolK)
	}
	lexicalHits, err := u.memories.SearchLexicalMemory(ctx, query, poolK, filter)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory lexical search degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
		}
		return trimSearchHits(vectorHits, poolK)
	}
	if len(lexicalHits) == 0 {
		u.logMemorySearchStage(ctx, "memory search hybrid stage completed", buildMemorySearchText(item), []any{
			"vector_candidate_count", len(vectorHits),
			"lexical_hit_count", 0,
			"lexical_materialized_count", 0,
			"hybrid_candidate_count", len(trimSearchHits(vectorHits, poolK)),
		}, map[string]any{
			"query":                 item.Query,
			"top_vector_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(vectorHits)),
			"top_lexical_candidate": nil,
			"top_hybrid_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(trimSearchHits(vectorHits, poolK))),
		})
		return trimSearchHits(vectorHits, poolK)
	}
	materialized, err := u.materializeLexicalHits(lexicalHits)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory lexical materialization degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
		}
		return trimSearchHits(vectorHits, poolK)
	}
	fused := fuseSearchHitsByRRF(vectorHits, materialized, poolK, u.rrfK)
	u.logMemorySearchStage(ctx, "memory search hybrid stage completed", buildMemorySearchText(item), []any{
		"vector_candidate_count", len(vectorHits),
		"lexical_hit_count", len(lexicalHits),
		"lexical_materialized_count", len(materialized),
		"hybrid_candidate_count", len(fused),
	}, map[string]any{
		"query":                 item.Query,
		"top_vector_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(vectorHits)),
		"top_lexical_candidate": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(materialized)),
		"top_hybrid_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(fused)),
	})
	return fused
}

// buildMemoryLexicalQuery keeps lexical recall focused on the normalized query text so the simplified AI-facing contract does not need a second background field.
// buildMemoryLexicalQuery 用于让 lexical 召回聚焦规范化后的 query 文本，避免简化后的 AI 接口再引入第二个 background 字段。
func buildMemoryLexicalQuery(item MemoryQueryItem) string {
	return strings.TrimSpace(item.Query)
}

// materializeLexicalHits converts lexical hits that already carry durable rows into the same public hit shape used by vector recall.
// materializeLexicalHits 用于把已经携带长期行的 lexical 命中转换成与向量召回一致的公开命中结构。
func (u *MemoryUseCase) materializeLexicalHits(hits []logicdomain.MemoryLexicalHit) ([]MemoryQueryHit, error) {
	if len(hits) == 0 {
		return []MemoryQueryHit{}, nil
	}
	now := time.Now().UTC()
	mapped := make([]MemoryQueryHit, 0, len(hits))
	total := len(hits)
	for idx, hit := range hits {
		row := hit.Record
		if hit.MemoryID == 0 {
			return nil, fmt.Errorf("lexical hit at rank %d is missing memory id", idx+1)
		}
		if row.ID == 0 {
			return nil, fmt.Errorf("lexical hit %d is missing materialized memory row", hit.MemoryID)
		}
		if row.ID != hit.MemoryID {
			return nil, fmt.Errorf("lexical hit %d materialized row id mismatch: got %d", hit.MemoryID, row.ID)
		}
		// Keep the app-layer hot-path guard after store-level lexical filtering so concurrent retirements cannot leak into fusion.
		// 在存储层 lexical 过滤之后继续保留应用层热路径守卫，避免并发退役的记忆泄漏进融合阶段。
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, now) {
			continue
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
			SessionID:                row.OriginSessionID,
			Abstract:                 strings.TrimSpace(row.Abstract),
			DetailsPreview:           strings.TrimSpace(row.Details),
			Category:                 row.Category,
			Score:                    normalizedRankScore(idx+1, total),
			Origin:                   "lexical_search",
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
	return mapped, nil
}

// fuseSearchHitsByRRF merges vector and lexical hits by reciprocal-rank fusion while preserving one caller-friendly score in the 0..1 range.
// fuseSearchHitsByRRF 用于通过 reciprocal-rank fusion 融合向量和 lexical 命中，并保留一个 0..1 区间内的调用方友好分数。
func fuseSearchHitsByRRF(vectorHits, lexicalHits []MemoryQueryHit, topK, rrfK int) []MemoryQueryHit {
	type fusedCandidate struct {
		Hit      MemoryQueryHit
		RRFScore float64
		Channels int
	}
	if rrfK <= 0 {
		rrfK = 60
	}
	candidates := make(map[uint64]*fusedCandidate, len(vectorHits)+len(lexicalHits))
	merge := func(hits []MemoryQueryHit, channel string) {
		total := len(hits)
		for idx, hit := range hits {
			if hit.MemoryRef.ID == 0 {
				continue
			}
			candidate, ok := candidates[hit.MemoryRef.ID]
			if !ok {
				copyHit := hit
				candidate = &fusedCandidate{Hit: copyHit}
				candidates[hit.MemoryRef.ID] = candidate
			}
			candidate.RRFScore += 1.0 / float64(rrfK+idx+1)
			candidate.Channels++
			if hit.Score > candidate.Hit.Score {
				candidate.Hit.Score = hit.Score
			}
			switch {
			case candidate.Hit.Origin == "":
				candidate.Hit.Origin = channel
			case candidate.Hit.Origin != channel:
				candidate.Hit.Origin = "hybrid_rrf"
			}
			if strings.TrimSpace(candidate.Hit.Abstract) == "" {
				candidate.Hit.Abstract = hit.Abstract
			}
			if strings.TrimSpace(candidate.Hit.DetailsPreview) == "" {
				candidate.Hit.DetailsPreview = hit.DetailsPreview
			}
			if candidate.Hit.SourceRef.Empty() && !hit.SourceRef.Empty() {
				candidate.Hit.SourceRef = hit.SourceRef
			}
			if candidate.Hit.SessionID == 0 {
				candidate.Hit.SessionID = hit.SessionID
			}
			if total > 0 {
				rankScore := normalizedRankScore(idx+1, total)
				if rankScore > candidate.Hit.Score {
					candidate.Hit.Score = rankScore
				}
			}
		}
	}
	merge(vectorHits, "vector_search")
	merge(lexicalHits, "lexical_search")

	fused := make([]fusedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		fused = append(fused, *candidate)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].RRFScore == fused[j].RRFScore {
			if fused[i].Hit.Score == fused[j].Hit.Score {
				return fused[i].Hit.MemoryRef.ID < fused[j].Hit.MemoryRef.ID
			}
			return fused[i].Hit.Score > fused[j].Hit.Score
		}
		return fused[i].RRFScore > fused[j].RRFScore
	})
	if topK > 0 && len(fused) > topK {
		fused = fused[:topK]
	}
	out := make([]MemoryQueryHit, 0, len(fused))
	for idx, candidate := range fused {
		hit := candidate.Hit
		rankScore := normalizedRankScore(idx+1, len(fused))
		if rankScore > hit.Score {
			hit.Score = rankScore
		}
		if candidate.Channels > 1 && hit.Score < 1 {
			hit.Score = math.Min(1, hit.Score+0.03)
		}
		out = append(out, hit)
	}
	return out
}

// normalizedRankScore converts one 1-based rank into a stable 0..1 score so lexical-only or fused hits can still pass the existing pre-check threshold gate.
// normalizedRankScore 用于把 1-based 排名转换成稳定的 0..1 分数，让 lexical-only 或融合命中仍能通过现有 pre-check 阈值门槛。
func normalizedRankScore(rank, total int) float64 {
	if rank <= 0 || total <= 0 {
		return 0
	}
	score := 1 - (float64(rank-1) / float64(total*2))
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// trimSearchHits applies the final top-k cap while preserving the already established hit order.
// trimSearchHits 用于在保持既有命中顺序的前提下施加最终 top-k 截断。
func trimSearchHits(hits []MemoryQueryHit, topK int) []MemoryQueryHit {
	if topK <= 0 || len(hits) <= topK {
		return hits
	}
	return append([]MemoryQueryHit(nil), hits[:topK]...)
}

// clampMemoryQueryHitScores enforces the shared 0..1 caller-facing score contract after each optional retrieval post-processing stage, so arbitrary provider scores or contextual boosts cannot leak unstable score ranges to downstream ranking and RPC responses.
// clampMemoryQueryHitScores 用于在每个可选检索后处理阶段之后强制执行统一的 0..1 对外分数契约，避免 provider 自定义分数或情境加减分把不稳定范围泄露到后续排序和 RPC 响应中。
func clampMemoryQueryHitScores(hits []MemoryQueryHit) []MemoryQueryHit {
	for idx := range hits {
		hits[idx].Score = clampUnitScore(hits[idx].Score)
	}
	return hits
}

// normalizeCombinedHybridSQLHits rewrites SQL-level raw RRF scores into the stable 0..1 score contract expected by downstream thresholding, while preserving the original fused score for diagnostics.
// normalizeCombinedHybridSQLHits 用于把 SQL 层原始 RRF 分数改写成下游阈值链路期望的稳定 0..1 分数契约，同时保留原始融合分供诊断使用。
func normalizeCombinedHybridSQLHits(hits []logicdomain.MemoryHit) []logicdomain.MemoryHit {
	if len(hits) == 0 {
		return []logicdomain.MemoryHit{}
	}
	normalized := append([]logicdomain.MemoryHit(nil), hits...)
	total := len(normalized)
	for idx := range normalized {
		rawScore := normalized[idx].Score
		if normalized[idx].Metadata == nil {
			normalized[idx].Metadata = make(map[string]string, 1)
		}
		normalized[idx].Metadata["raw_score"] = strconv.FormatFloat(rawScore, 'f', -1, 64)

		// Convert the SQL-fused rank order into the same stable reviewer-facing score semantics used by the app-side fusion path,
		// so combined PostgreSQL search does not leak tiny reciprocal-rank values into logs, thresholds, or final responses.
		// 把 SQL 融合后的排序顺序转换成与应用层融合路径一致的稳定 reviewer 分数语义，
		// 避免 PostgreSQL 组合检索把极小的 reciprocal-rank 原始值泄露到日志、阈值判断或最终响应中。
		normalized[idx].Score = normalizedRankScore(idx+1, total)
	}
	return normalized
}

// requiredRawMemoryHitOrigin extracts the retrieval-channel label that every vector-search adapter must stamp before app-layer mapping.
// requiredRawMemoryHitOrigin 用于提取每个向量检索适配器在应用层映射前必须写入的检索通道标签。
func requiredRawMemoryHitOrigin(hit logicdomain.MemoryHit) (string, error) {
	if origin := strings.TrimSpace(hit.Metadata["origin"]); origin != "" {
		return origin, nil
	}
	return "", fmt.Errorf("memory hit %q is missing origin metadata", strings.TrimSpace(hit.ID))
}
