// Memory MMR diversity selection and provider reranking.
// 应用用例层记忆 MMR 多样性选择与提供方重排。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"math"
	"strconv"
	"strings"
)

// applyMMRSearchHits diversifies the already-ranked candidate pool so highly similar memories do not monopolize the final top-k.
// applyMMRSearchHits 用于对已经排好序的候选池做多样性重排，避免高度相似的记忆垄断最终 top-k。
func (u *MemoryUseCase) applyMMRSearchHits(topK int, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.mmrEnabled || len(hits) <= 1 {
		return trimSearchHits(hits, topK)
	}
	limit := topK
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	// Consume only vectors already carried by mapped hits because vector and lexical materialization now own the row payload contract.
	// 只消费映射命中自身携带的向量，因为向量与 lexical 物化阶段已经负责长期行载荷契约。
	working := append([]MemoryQueryHit(nil), hits...)
	if len(working) <= 1 {
		return trimSearchHits(working, limit)
	}

	selected := make([]MemoryQueryHit, 0, limit)
	used := make(map[uint64]struct{}, limit)
	for len(selected) < limit {
		bestIdx := -1
		bestScore := math.Inf(-1)
		for idx, hit := range working {
			if _, ok := used[hit.MemoryRef.ID]; ok {
				continue
			}
			score := hit.Score
			if len(selected) > 0 {
				score = u.mmrLambda*hit.Score - (1-u.mmrLambda)*maxMMRSimilarity(hit, selected)
			}
			if score > bestScore {
				bestScore = score
				bestIdx = idx
				continue
			}
			if score == bestScore {
				current := working[bestIdx]
				if hit.Score > current.Score || (hit.Score == current.Score && hit.MemoryRef.ID < current.MemoryRef.ID) {
					bestIdx = idx
				}
			}
		}
		if bestIdx < 0 {
			break
		}
		chosen := working[bestIdx]
		chosen.Origin = normalizeMMROrigin(chosen.Origin)
		selected = append(selected, chosen)
		used[chosen.MemoryRef.ID] = struct{}{}
	}
	return selected
}

// maxMMRSimilarity returns the largest cosine similarity between one candidate and the already selected set.
// maxMMRSimilarity 用于返回某个候选与已选择集合之间的最大余弦相似度。
func maxMMRSimilarity(candidate MemoryQueryHit, selected []MemoryQueryHit) float64 {
	if len(candidate.Vector) == 0 || len(selected) == 0 {
		return 0
	}
	best := 0.0
	for _, chosen := range selected {
		score := cosineSimilarityFloat32(candidate.Vector, chosen.Vector)
		if score > best {
			best = score
		}
	}
	return best
}

// cosineSimilarityFloat32 computes a safe cosine similarity for two candidate vectors so MMR can compare near-duplicate memories from different retrieval channels.
// cosineSimilarityFloat32 用于为两条候选向量计算安全的余弦相似度，让 MMR 可以比较不同检索通道中的近重复记忆。
func cosineSimilarityFloat32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	dot := 0.0
	normA := 0.0
	normB := 0.0
	for idx := range a {
		av := float64(a[idx])
		bv := float64(b[idx])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA <= 0 || normB <= 0 {
		return 0
	}
	return dot / math.Sqrt(normA*normB)
}

// rerankSearchHits reorders the first-stage vector hits with the configured rerank backend and degrades to the original ordering on provider failures.
// rerankSearchHits 用于使用已配置的 rerank 后端重排首轮向量命中，并在 provider 失败时降级回原始顺序。
func (u *MemoryUseCase) rerankSearchHits(ctx context.Context, query string, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || u.reranker == nil || len(hits) <= 1 {
		return hits
	}
	limit := u.rerankTopN
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	primary := append([]MemoryQueryHit(nil), hits[:limit]...)
	documents := buildRerankDocuments(primary)
	// Rerank only has ordering value when at least two provider-facing documents survive text normalization, so keep the current recall order instead of spending an external call on a single comparable item.
	// 只有至少两个 provider 可见文档通过文本归一化后，rerank 才有排序价值；否则保留当前召回顺序，避免为单个可比较项消耗外部调用。
	if len(documents) <= 1 {
		return hits
	}
	results, err := u.reranker.Rerank(ctx, strings.TrimSpace(query), documents, len(documents))
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory search rerank degraded to rerank-disabled fallback", append(memoryQueryLogFields(u.logger, query), "candidate_count", len(documents), "primary_candidate_count", len(primary), "fallback_mode", "rerank_disabled", "err", err)...)
		}
		return hits
	}
	if len(results) == 0 {
		return hits
	}

	// Rewrite the provider-ranked subset with rerank scores first, then append any untouched tail candidates so the caller still receives a stable result count.
	// 先按 provider 重排并写回被 rerank 的子集分数，再追加未触及的尾部候选，保证调用方仍拿到稳定数量的结果。
	byID := make(map[uint64]MemoryQueryHit, len(primary))
	for _, hit := range primary {
		byID[hit.MemoryRef.ID] = hit
	}
	ordered := make([]MemoryQueryHit, 0, len(hits))
	seen := make(map[uint64]struct{}, len(results))
	for _, result := range results {
		memoryID, err := strconv.ParseUint(strings.TrimSpace(result.ID), 10, 64)
		if err != nil {
			continue
		}
		hit, ok := byID[memoryID]
		if !ok {
			continue
		}
		hit.Score = result.Score
		hit.Origin = normalizeRerankedOrigin(hit.Origin)
		ordered = append(ordered, hit)
		seen[memoryID] = struct{}{}
	}
	for _, hit := range primary {
		if _, ok := seen[hit.MemoryRef.ID]; ok {
			continue
		}
		ordered = append(ordered, hit)
	}
	if limit < len(hits) {
		ordered = append(ordered, hits[limit:]...)
	}
	u.logMemorySearchStage(ctx, "memory search rerank stage completed", query, []any{
		"rerank_input_count", len(documents),
		"rerank_primary_candidate_count", len(primary),
		"rerank_output_count", len(ordered),
	}, map[string]any{
		"top_input_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(primary)),
		"top_output_candidate": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(ordered)),
	})
	return ordered
}

// normalizeRerankedOrigin upgrades the hit origin label once a later rerank model has re-evaluated the candidate order without inventing missing source attribution.
// normalizeRerankedOrigin 用于在后续 rerank 模型重新评估候选顺序后升级命中来源标签，同时不会伪造缺失的来源归属。
func normalizeRerankedOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	switch origin {
	case "hybrid_rrf":
		return "hybrid_rrf_rerank"
	case "lexical_search":
		return "lexical_rerank"
	case "vector_search":
		return "vector_rerank"
	default:
		return origin
	}
}

// normalizeMMROrigin upgrades the hit origin label once the diversity pass has re-ordered the candidate list without masking missing source attribution.
// normalizeMMROrigin 用于在多样性重排改写候选顺序后升级命中来源标签，同时不会掩盖缺失的来源归属。
func normalizeMMROrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	switch origin {
	case "hybrid_rrf_rerank":
		return "hybrid_rrf_rerank_mmr"
	case "hybrid_rrf":
		return "hybrid_rrf_mmr"
	case "lexical_rerank":
		return "lexical_rerank_mmr"
	case "lexical_search":
		return "lexical_mmr"
	case "vector_rerank":
		return "vector_rerank_mmr"
	case "vector_search":
		return "vector_mmr"
	default:
		if origin == "" {
			return ""
		}
		if strings.HasSuffix(origin, "_mmr") {
			return origin
		}
		return origin + "_mmr"
	}
}

// buildRerankDocuments converts one mapped-hit slice into the compact text fragments expected by the rerank provider.
// buildRerankDocuments 用于把补全后的命中切片转换成 rerank provider 期望的紧凑文本片段。
func buildRerankDocuments(hits []MemoryQueryHit) []appports.RerankerDocument {
	if len(hits) == 0 {
		return nil
	}
	docs := make([]appports.RerankerDocument, 0, len(hits))
	for _, hit := range hits {
		text := buildMemorySearchCandidateText(hit)
		if hit.MemoryRef.ID == 0 || strings.TrimSpace(text) == "" {
			continue
		}
		docs = append(docs, appports.RerankerDocument{
			ID:   strconv.FormatUint(hit.MemoryRef.ID, 10),
			Text: text,
		})
	}
	return docs
}

// buildMemorySearchCandidateText merges abstract and details into one rerank document while avoiding empty duplicated text.
// buildMemorySearchCandidateText 用于把 abstract 和 details 合并成一条 rerank 文档，同时避免空文本和重复文本。
func buildMemorySearchCandidateText(hit MemoryQueryHit) string {
	abstract := strings.TrimSpace(hit.Abstract)
	details := strings.TrimSpace(hit.DetailsPreview)
	switch {
	case abstract == "" && details == "":
		return ""
	case details == "" || details == abstract:
		return abstract
	case abstract == "":
		return details
	default:
		return abstract + "\n" + details
	}
}
