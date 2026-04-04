// precheck_candidate_merge.go keeps pre-check candidate merging stable when the same memory is recalled by multiple query groups.
// precheck_candidate_merge.go 用于在同一条记忆被多个 query group 召回时，保持 pre-check 候选合并稳定且信息不丢失。
package usecase

import (
	"sort"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// mergePreCheckCandidate combines repeated hits for the same memory so reviewer-facing evidence is preserved across multiple query groups instead of being silently overwritten by the first equal-scored hit.
// mergePreCheckCandidate 用于合并同一 memory 的重复命中，避免多个 query group 的 reviewer 证据在分数相等时被第一条记录静默覆盖。
func mergePreCheckCandidate(existing, incoming logicdomain.PreCheckMemoryCandidate) logicdomain.PreCheckMemoryCandidate {
	primary := existing
	secondary := incoming
	if shouldPreferIncomingPreCheckCandidate(existing, incoming) {
		primary = incoming
		secondary = existing
	}
	merged := primary
	merged.Abstract = chooseRicherPreCheckText(primary.Abstract, secondary.Abstract)
	merged.Details = chooseRicherPreCheckText(primary.Details, secondary.Details)
	if strings.TrimSpace(merged.SourceKind) == "" && strings.TrimSpace(secondary.SourceKind) != "" {
		merged.SourceKind = secondary.SourceKind
	}
	if strings.TrimSpace(merged.ScopeLevel) == "" && strings.TrimSpace(secondary.ScopeLevel) != "" {
		merged.ScopeLevel = secondary.ScopeLevel
	}
	if merged.SourceTurnID == 0 && secondary.SourceTurnID > 0 {
		merged.SourceTurnID = secondary.SourceTurnID
	}
	if merged.Category == 0 && secondary.Category != 0 {
		merged.Category = secondary.Category
	}
	if strings.TrimSpace(merged.ScoreLabel) == "" && strings.TrimSpace(secondary.ScoreLabel) != "" {
		merged.ScoreLabel = secondary.ScoreLabel
	}
	if strings.TrimSpace(merged.ScoreExplanation) == "" && strings.TrimSpace(secondary.ScoreExplanation) != "" {
		merged.ScoreExplanation = secondary.ScoreExplanation
	}
	if strings.TrimSpace(merged.Origin) == "" && strings.TrimSpace(secondary.Origin) != "" {
		merged.Origin = secondary.Origin
	}
	if strings.TrimSpace(merged.OriginLabel) == "" && strings.TrimSpace(secondary.OriginLabel) != "" {
		merged.OriginLabel = secondary.OriginLabel
	}
	if strings.TrimSpace(merged.OriginExplanation) == "" && strings.TrimSpace(secondary.OriginExplanation) != "" {
		merged.OriginExplanation = secondary.OriginExplanation
	}

	// Preserve the broadest reviewer-facing context view by unioning matched values while avoiding count inflation across repeated groups.
	// 通过合并命中的 context 值来保留最广的 reviewer 视图，同时避免跨重复 group 直接累加计数导致夸大。
	merged.MatchedContextValues = appendSortedUniquePreCheckValues(existing.MatchedContextValues, incoming.MatchedContextValues...)
	merged.MatchedContextSupportCount = maxPreCheckCount(existing.MatchedContextSupportCount, incoming.MatchedContextSupportCount)
	merged.MatchedContextRebuttalCount = maxPreCheckCount(existing.MatchedContextRebuttalCount, incoming.MatchedContextRebuttalCount)
	merged.MatchedContextScoreDelta = chooseStrongerSignedDelta(existing.MatchedContextScoreDelta, incoming.MatchedContextScoreDelta)
	merged.SupportCount = maxPreCheckCount(existing.SupportCount, incoming.SupportCount)
	merged.RebuttalCount = maxPreCheckCount(existing.RebuttalCount, incoming.RebuttalCount)
	return merged
}

// shouldPreferIncomingPreCheckCandidate chooses which repeated hit should act as the representative explanation source when the same memory appears in multiple query groups.
// shouldPreferIncomingPreCheckCandidate 用于在同一 memory 出现在多个 query group 时，决定哪条命中应充当说明字段的代表来源。
func shouldPreferIncomingPreCheckCandidate(existing, incoming logicdomain.PreCheckMemoryCandidate) bool {
	if incoming.Score != existing.Score {
		return incoming.Score > existing.Score
	}
	if absFloat64(incoming.MatchedContextScoreDelta) != absFloat64(existing.MatchedContextScoreDelta) {
		return absFloat64(incoming.MatchedContextScoreDelta) > absFloat64(existing.MatchedContextScoreDelta)
	}
	if len(incoming.MatchedContextValues) != len(existing.MatchedContextValues) {
		return len(incoming.MatchedContextValues) > len(existing.MatchedContextValues)
	}
	if scorePreCheckOriginRichness(incoming.Origin) != scorePreCheckOriginRichness(existing.Origin) {
		return scorePreCheckOriginRichness(incoming.Origin) > scorePreCheckOriginRichness(existing.Origin)
	}
	if len(strings.TrimSpace(incoming.Details)) != len(strings.TrimSpace(existing.Details)) {
		return len(strings.TrimSpace(incoming.Details)) > len(strings.TrimSpace(existing.Details))
	}
	if len(strings.TrimSpace(incoming.Abstract)) != len(strings.TrimSpace(existing.Abstract)) {
		return len(strings.TrimSpace(incoming.Abstract)) > len(strings.TrimSpace(existing.Abstract))
	}
	return false
}

// scorePreCheckOriginRichness ranks retrieval-origin codes by how much reviewer-facing explanation value they carry when repeated hits tie on score.
// scorePreCheckOriginRichness 用于按 reviewer 说明价值给检索来源代码排序，在重复命中分数相等时选择信息更丰富的来源。
func scorePreCheckOriginRichness(origin string) int {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return 0
	}
	score := 1
	if strings.Contains(origin, "hybrid_rrf") {
		score += 4
	} else if strings.Contains(origin, "lexical") || strings.Contains(origin, "vector") {
		score += 2
	}
	if strings.Contains(origin, "rerank") {
		score += 2
	}
	if strings.HasSuffix(origin, "_mmr") {
		score++
	}
	return score
}

// chooseRicherPreCheckText keeps the more informative non-empty candidate text so repeated hits can retain stronger explanations without losing fuller reviewer-facing wording.
// chooseRicherPreCheckText 用于在重复命中时保留信息量更高的非空候选文本，让更强说明不会牺牲 reviewer 看到的完整表述。
func chooseRicherPreCheckText(primary, secondary string) string {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	switch {
	case primary == "":
		return secondary
	case secondary == "":
		return primary
	case len(secondary) > len(primary):
		return secondary
	default:
		return primary
	}
}

// appendSortedUniquePreCheckValues deduplicates and sorts reviewer-facing string lists on top of the shared context-evidence canonical surface so pre-check explanations do not reintroduce formatting drift that query-time retrieval already collapsed.
// appendSortedUniquePreCheckValues 用于基于共享的 context evidence 规范表面对 reviewer 字符串列表做去重与排序，避免 pre-check 说明重新引入查询期已折叠的格式漂移。
func appendSortedUniquePreCheckValues(base []string, extra ...string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	appendValue := func(value string) {
		canonical := logicdomain.NormalizeMemoryContextEvidenceLabel(value)
		if canonical == "" {
			return
		}
		if _, ok := seen[canonical]; ok {
			return
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	for _, value := range base {
		appendValue(value)
	}
	for _, value := range extra {
		appendValue(value)
	}
	sort.Strings(out)
	return out
}

// absFloat64 keeps candidate merge heuristics readable when choosing the strongest matched-context delta explanation.
// absFloat64 用于在选择最强 matched-context delta 说明时保持合并启发式可读。
func absFloat64(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// chooseStrongerSignedDelta keeps the signed delta whose absolute value is larger so merged candidates retain the strongest contextual boost or penalty seen across repeated hits.
// chooseStrongerSignedDelta 用于保留绝对值更大的带符号 delta，让合并后的候选继续体现重复命中里最强的情境增益或惩罚。
func chooseStrongerSignedDelta(left, right float64) float64 {
	if absFloat64(right) > absFloat64(left) {
		return right
	}
	return left
}

// maxPreCheckCount keeps candidate merge code readable when taking the stronger reviewer-facing count across repeated hits.
// maxPreCheckCount 用于在重复命中里选择更强 reviewer 计数时保持合并代码可读。
func maxPreCheckCount(left, right int) int {
	if right > left {
		return right
	}
	return left
}
