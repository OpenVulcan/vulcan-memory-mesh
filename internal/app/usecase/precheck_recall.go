// Pre-check candidate recall, thresholding and review.
// 应用用例层 pre-check 候选召回、阈值筛选与评审。
package usecase

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"sort"
	"strings"
	"time"
)

// searchMemoryCandidates reuses the unified memory-search RPC logic, then keeps only de-duplicated hits above the configured similarity floor and numbers them for stage two.
// searchMemoryCandidates 用于复用统一记忆查询逻辑，然后只保留高于配置相似度下限且去重后的命中结果，并为第二层评审分配候选编号。
func (u *PreCheckUseCase) searchMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.memories == nil {
		return []logicdomain.PreCheckMemoryCandidate{}, nil
	}
	queries := buildPreCheckMemoryQueries(intent, cmd.UserContent)
	recallMode := NormalizePreCheckRecallMode(cmd.RecallMode)
	u.logPreCheckStage("pre-check memory query prepared", trace.IDFromContext(ctx), cmd.Session, []any{
		"top_k", u.config.TopK,
		"search_scope", u.config.SearchScope,
		"normalized_query_count", len(queries),
		"recall_mode", int32(recallMode),
		"raw_recall_mode", int32(cmd.RecallMode),
		"last_compacted_turn_id", cmd.Session.LastCompactedTurnID,
	}, map[string]any{
		"current_user_input": cmd.UserContent,
		"intent_queries":     queries,
		"search_scope":       u.config.SearchScope,
		"queries":            queries,
	})
	boundaryFilter := buildPreCheckSessionBoundaryFilter(cmd.Session, recallMode)
	result, err := u.memories.Search(ctx, MemoryQueryCommand{
		UserID:              cmd.Session.UserID,
		ProjectID:           cmd.Session.ProjectID,
		Queries:             queries,
		TopK:                u.config.TopK,
		ScopeOverride:       u.config.SearchScope,
		BoundarySessionID:   boundaryFilter.BoundarySessionID,
		BoundaryMaxTurnID:   boundaryFilter.BoundaryMaxTurnID,
		ExcludeBoundaryTurn: boundaryFilter.ExcludeBoundaryTurn,
	})
	if err != nil {
		return nil, err
	}
	logPreviewPIICache := newPreCheckLogPreviewPIICache(u.piiScrubber)
	rawGroups, rawHitCount := buildPreCheckRawRecallLogs(result.Results)
	rawGroups = scrubPreCheckRawRecallGroupLogsPII(logPreviewPIICache, rawGroups)
	merged := make(map[uint64]logicdomain.PreCheckMemoryCandidate)
	mergedCandidateIDs := make(map[uint64]struct{})
	firstSeenOrder := make(map[uint64]int)
	nextSeenOrder := 0
	belowThresholdCount := 0
	var bestRawHit *preCheckThresholdHitLogPayload
	var bestBelowThresholdHit *preCheckThresholdHitLogPayload
	for _, group := range result.Results {
		for hitIdx, hit := range group.Hits {
			if hit.MemoryRef.Type != logicdomain.MemoryRefTypeMemory || hit.MemoryRef.ID == 0 {
				continue
			}
			candidateScore := normalizePreCheckReviewScore(hit.Score, hit.Origin, hitIdx+1, len(group.Hits))
			logHit := scrubPreCheckThresholdHitLogPII(logPreviewPIICache, summarizePreCheckThresholdHitForLog(hit, candidateScore))
			bestRawHit = choosePreferredPreCheckThresholdHit(bestRawHit, logHit)
			if candidateScore < u.config.MinSimilarityScore {
				belowThresholdCount++
				bestBelowThresholdHit = choosePreferredPreCheckThresholdHit(bestBelowThresholdHit, logHit)
				continue
			}
			createdTimestamp := int64(0)
			createdDateTime := ""
			createdDate := ""
			if !hit.CreatedAt.IsZero() {
				createdTimestamp = hit.CreatedAt.UTC().UnixMilli()
				createdDateTime = logicdomain.FormatDisplayDateTime(hit.CreatedAt)
				createdDate = logicdomain.FormatDisplayDate(hit.CreatedAt)
			}
			candidate := logicdomain.PreCheckMemoryCandidate{
				MemoryID:                    hit.MemoryRef.ID,
				SourceTurnID:                hit.SourceRef.ID,
				CreatedTimestamp:            createdTimestamp,
				CreatedDateTime:             createdDateTime,
				CreatedDate:                 createdDate,
				SourceKind:                  logicdomain.MemorySourceKindLabel(hit.SourceKind),
				ScopeLevel:                  logicdomain.MemoryScopeLevelLabel(hit.ScopeLevel),
				Category:                    hit.Category,
				Abstract:                    strings.TrimSpace(hit.Abstract),
				Details:                     strings.TrimSpace(hit.DetailsPreview),
				Score:                       candidateScore,
				Origin:                      strings.TrimSpace(hit.Origin),
				SupportCount:                hit.SupportCount,
				RebuttalCount:               hit.RebuttalCount,
				MatchedContextValues:        append([]string(nil), hit.MatchedContextValues...),
				MatchedContextSupportCount:  hit.MatchedContextSupportCount,
				MatchedContextRebuttalCount: hit.MatchedContextRebuttalCount,
				MatchedContextScoreDelta:    hit.MatchedContextScoreDelta,
			}
			if candidate.Abstract == "" && candidate.Details == "" {
				continue
			}
			if candidate.Details == "" {
				candidate.Details = candidate.Abstract
			}
			if _, ok := firstSeenOrder[candidate.MemoryID]; !ok {
				firstSeenOrder[candidate.MemoryID] = nextSeenOrder
				nextSeenOrder++
			}
			if existing, ok := merged[candidate.MemoryID]; ok {
				merged[candidate.MemoryID] = mergePreCheckCandidate(existing, candidate)
				mergedCandidateIDs[candidate.MemoryID] = struct{}{}
				continue
			}
			merged[candidate.MemoryID] = candidate
		}
	}

	// Redact the deduplicated candidate set once, after repeated query-group hits have been merged, so PII masking cost scales with reviewer candidates instead of raw recall hits.
	// 在重复 query group 命中合并之后，仅对去重后的候选集合执行一次脱敏，让 PII 掩码成本随 reviewer 候选数增长，而不是随原始召回命中数增长。
	mergedCandidates := make([]logicdomain.PreCheckMemoryCandidate, 0, len(merged))
	for _, candidate := range merged {
		mergedCandidates = append(mergedCandidates, candidate)
	}
	mergedCandidates = scrubPreCheckMemoryCandidatesPII(u.piiScrubber, mergedCandidates)

	u.logPreCheckStage("pre-check raw memory hits returned", trace.IDFromContext(ctx), cmd.Session, []any{
		"group_count", len(result.Results),
		"raw_hit_count", rawHitCount,
		"search_scope", u.config.SearchScope,
		"similarity_threshold", u.config.MinSimilarityScore,
	}, map[string]any{
		"current_user_input": cmd.UserContent,
		"intent_queries":     queries,
		"search_scope":       u.config.SearchScope,
		"raw_groups":         rawGroups,
		"best_raw_hit":       bestRawHit,
	})
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(mergedCandidates))
	for _, candidate := range mergedCandidates {
		// Re-apply the text guard after redaction because a runtime scrubber may remove an entire sensitive fragment.
		// 脱敏后重新执行文本守卫，因为运行时脱敏器可能会移除整段敏感片段。
		if candidate.Abstract == "" && candidate.Details == "" {
			continue
		}
		if candidate.Details == "" {
			candidate.Details = candidate.Abstract
		}
		// Context labels are merged before redaction only for repeated-memory hits, so normalize those merged sets after masking to collapse values that become equivalent once secrets are hidden while preserving untouched single-hit labels verbatim.
		// 只有重复 memory 命中会先合并 context 标签再脱敏，因此仅对这些已合并集合在掩码后重新归一去重，折叠隐藏密文后变得等价的值，同时保留未合并单命中标签的原始写法。
		if _, wasMerged := mergedCandidateIDs[candidate.MemoryID]; wasMerged && len(candidate.MatchedContextValues) > 1 {
			candidate.MatchedContextValues = appendSortedUniquePreCheckValues(candidate.MatchedContextValues)
		}
		candidate = normalizePreCheckCandidateDerivedFields(candidate, u.config.MinSimilarityScore)
		out = append(out, candidate)
	}
	deduplicatedCandidateCount := len(out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		leftOrder, leftOK := firstSeenOrder[out[i].MemoryID]
		rightOrder, rightOK := firstSeenOrder[out[j].MemoryID]
		if leftOK && rightOK && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	if len(out) > u.config.ReviewCandidateLimit {
		out = out[:u.config.ReviewCandidateLimit]
	}
	for idx := range out {
		out[idx].CandidateNumber = idx + 1
	}
	u.logPreCheckStage("pre-check memory candidate filtering applied", trace.IDFromContext(ctx), cmd.Session, []any{
		"group_count", len(result.Results),
		"raw_hit_count", rawHitCount,
		"search_scope", u.config.SearchScope,
		"below_threshold_count", belowThresholdCount,
		"deduplicated_candidate_count", deduplicatedCandidateCount,
		"review_candidate_count", len(out),
		"similarity_threshold", u.config.MinSimilarityScore,
	}, map[string]any{
		"current_user_input":       cmd.UserContent,
		"intent_queries":           queries,
		"search_scope":             u.config.SearchScope,
		"best_raw_hit":             bestRawHit,
		"best_below_threshold_hit": bestBelowThresholdHit,
	})
	return out, nil
}

// normalizePreCheckCandidateDerivedFields recomputes reviewer-facing derived explanations from the final merged candidate values so score/origin labels never lag behind later merge decisions.
// normalizePreCheckCandidateDerivedFields 用于基于最终合并后的候选值重新计算 reviewer 可见的派生说明，避免 score/origin 标签滞后于后续 merge 决策。
func normalizePreCheckCandidateDerivedFields(candidate logicdomain.PreCheckMemoryCandidate, threshold float64) logicdomain.PreCheckMemoryCandidate {
	scoreExplanation := describePreCheckCandidateScore(candidate.Score, threshold, candidate.MatchedContextScoreDelta)
	originExplanation := describePreCheckMemoryOrigin(candidate.Origin)
	candidate.ScoreLabel = scoreExplanation.Label
	candidate.ScoreExplanation = scoreExplanation.Explanation
	candidate.OriginLabel = originExplanation.Label
	candidate.OriginExplanation = originExplanation.Explanation
	return candidate
}

// reviewMemoryCandidates lets the second-stage reviewer choose candidate numbers and restores the selected candidate order for final injection.
// reviewMemoryCandidates 用于让第二层评审器选择候选编号，并恢复最终注入使用的候选顺序。
func (u *PreCheckUseCase) reviewMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult, candidates []logicdomain.PreCheckMemoryCandidate) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.reviewer == nil {
		return nil, fmt.Errorf("pre-check memory reviewer is nil")
	}
	searchQueries := normalizePreCheckMemoryQueries(intent.Queries, cmd.UserContent)
	review, err := u.reviewer.Review(ctx, logicdomain.PreCheckMemoryReviewInput{
		CurrentTimestamp: time.Now().UTC().UnixMilli(),
		UserContent:      cmd.UserContent,
		SearchQueries:    searchQueries,
		IntentReason:     intent.Reason,
		Candidates:       append([]logicdomain.PreCheckMemoryCandidate(nil), candidates...),
	})
	if err != nil {
		return nil, err
	}
	candidatesByNumber := make(map[int]logicdomain.PreCheckMemoryCandidate, len(candidates))
	for _, candidate := range candidates {
		candidatesByNumber[candidate.CandidateNumber] = candidate
	}
	selected := make([]logicdomain.PreCheckMemoryCandidate, 0, len(review.SelectedCandidateNumbers))
	seenNumbers := make(map[int]struct{}, len(review.SelectedCandidateNumbers))
	for _, number := range review.SelectedCandidateNumbers {
		// Re-enforce reviewer boundary uniqueness here so alternate reviewer implementations cannot duplicate lifecycle write-back or final injection by repeating a candidate number.
		// 在 reviewer 边界重新保证编号唯一性，避免替代 reviewer 实现通过重复候选编号造成生命周期写回或最终注入重复。
		if _, ok := seenNumbers[number]; ok {
			continue
		}
		if candidate, ok := candidatesByNumber[number]; ok {
			seenNumbers[number] = struct{}{}
			selected = append(selected, candidate)
		}
	}
	return selected, nil
}

// deduplicateSelectedCandidatesByTurnID removes repeated adopted candidates that point to the same source turn so final injection does not surface multiple summaries for one underlying dialogue.
// deduplicateSelectedCandidatesByTurnID 用于去掉指向同一来源 turn 的重复已采纳候选，避免最终注入把同一段底层对话拆成多条摘要重复返回。
func deduplicateSelectedCandidatesByTurnID(candidates []logicdomain.PreCheckMemoryCandidate) []logicdomain.PreCheckMemoryCandidate {
	if len(candidates) <= 1 {
		return append([]logicdomain.PreCheckMemoryCandidate(nil), candidates...)
	}
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(candidates))
	turnIndex := make(map[uint64]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.SourceTurnID == 0 {
			out = append(out, candidate)
			continue
		}
		if idx, ok := turnIndex[candidate.SourceTurnID]; ok {
			if shouldPreferIncomingPreCheckCandidate(out[idx], candidate) {
				out[idx] = candidate
			}
			continue
		}
		turnIndex[candidate.SourceTurnID] = len(out)
		out = append(out, candidate)
	}
	return out
}

// writeMemoryAdoption persists lifecycle updates only for the final adopted memory ids selected by the second-stage reviewer.
// writeMemoryAdoption 用于只为第二层评审器最终采纳的记忆 id 持久化生命周期更新。
func (u *PreCheckUseCase) writeMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, candidates []logicdomain.PreCheckMemoryCandidate) error {
	if u.store == nil || len(candidates) == 0 {
		return nil
	}
	memoryIDs := make([]uint64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.MemoryID > 0 {
			memoryIDs = append(memoryIDs, candidate.MemoryID)
		}
	}
	// Stop before the store port when no durable memory id survived filtering, so lifecycle adoption remains a no-op instead of delegating an empty mutation.
	// 当过滤后没有任何长期 memory id 时，在进入存储端口前停止，让生命周期采纳保持无操作，而不是下放一个空写入。
	if len(memoryIDs) == 0 {
		return nil
	}
	updatedRecords, err := u.store.ApplyMemoryAdoption(ctx, session, memoryIDs, time.Now().UTC())
	if err != nil {
		return err
	}
	if u.lifecycle == nil || len(updatedRecords) == 0 {
		return nil
	}
	for _, record := range updatedRecords {
		if err := u.lifecycle.Upsert(ctx, record); err != nil {
			return fmt.Errorf("sync adopted memory vector %s: %w", record.ID, err)
		}
	}
	return nil
}

// buildPreCheckMemoryQueries converts the stage-one retrieval hints into the simplified query list already consumed by the unified search surface.
// buildPreCheckMemoryQueries 用于把第一层检索提示转换成统一检索接口已消费的简化 query 列表。
func buildPreCheckMemoryQueries(intent logicdomain.IntentResult, userContent string) []string {
	return normalizePreCheckMemoryQueries(intent.Queries, userContent)
}

// normalizePreCheckMemoryQueries trims and de-duplicates stage-one queries so pre-check only searches and explains the distinct retrieval prompts that actually matter.
// normalizePreCheckMemoryQueries 用于裁剪并去重第一层 query，让 pre-check 只检索并解释真正有意义的去重检索语句。
func normalizePreCheckMemoryQueries(queries []string, userContent string) []string {
	normalized := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = textutil.NormalizeWhitespace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(textutil.NormalizeWhitespace(query))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, query)
	}
	if len(normalized) > 0 {
		return normalized
	}
	userContent = textutil.NormalizeWhitespace(userContent)
	if userContent == "" {
		return nil
	}
	return []string{userContent}
}
