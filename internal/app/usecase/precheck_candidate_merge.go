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
	merged := existing
	if incoming.Score > merged.Score {
		merged.Score = incoming.Score
		merged.ScoreLabel = incoming.ScoreLabel
		merged.ScoreExplanation = incoming.ScoreExplanation
	}
	if strings.TrimSpace(merged.Abstract) == "" && strings.TrimSpace(incoming.Abstract) != "" {
		merged.Abstract = incoming.Abstract
	}
	if strings.TrimSpace(merged.Details) == "" && strings.TrimSpace(incoming.Details) != "" {
		merged.Details = incoming.Details
	}
	if strings.TrimSpace(merged.SourceKind) == "" && strings.TrimSpace(incoming.SourceKind) != "" {
		merged.SourceKind = incoming.SourceKind
	}
	if strings.TrimSpace(merged.ScopeLevel) == "" && strings.TrimSpace(incoming.ScopeLevel) != "" {
		merged.ScopeLevel = incoming.ScopeLevel
	}
	if merged.SourceTurnID == 0 && incoming.SourceTurnID > 0 {
		merged.SourceTurnID = incoming.SourceTurnID
	}
	if merged.Category == 0 && incoming.Category != 0 {
		merged.Category = incoming.Category
	}
	if strings.TrimSpace(merged.Origin) == "" && strings.TrimSpace(incoming.Origin) != "" {
		merged.Origin = incoming.Origin
	}
	if strings.TrimSpace(merged.OriginLabel) == "" && strings.TrimSpace(incoming.OriginLabel) != "" {
		merged.OriginLabel = incoming.OriginLabel
	}
	if strings.TrimSpace(merged.OriginExplanation) == "" && strings.TrimSpace(incoming.OriginExplanation) != "" {
		merged.OriginExplanation = incoming.OriginExplanation
	}

	// Preserve the broadest reviewer-facing context view by unioning matched values while avoiding count inflation across repeated groups.
	// 通过合并命中的 context 值来保留最广的 reviewer 视图，同时避免跨重复 group 直接累加计数导致夸大。
	merged.MatchedContextValues = appendSortedUniquePreCheckValues(merged.MatchedContextValues, incoming.MatchedContextValues...)
	if incoming.MatchedContextSupportCount > merged.MatchedContextSupportCount {
		merged.MatchedContextSupportCount = incoming.MatchedContextSupportCount
	}
	if incoming.MatchedContextRebuttalCount > merged.MatchedContextRebuttalCount {
		merged.MatchedContextRebuttalCount = incoming.MatchedContextRebuttalCount
	}
	if absFloat64(incoming.MatchedContextScoreDelta) > absFloat64(merged.MatchedContextScoreDelta) {
		merged.MatchedContextScoreDelta = incoming.MatchedContextScoreDelta
	}
	if incoming.SupportCount > merged.SupportCount {
		merged.SupportCount = incoming.SupportCount
	}
	if incoming.RebuttalCount > merged.RebuttalCount {
		merged.RebuttalCount = incoming.RebuttalCount
	}
	return merged
}

// appendSortedUniquePreCheckValues deduplicates and sorts reviewer-facing string lists so merged candidate explanations stay stable across map iteration and query-group order.
// appendSortedUniquePreCheckValues 用于对 reviewer 字符串列表去重并排序，让合并后的候选说明不受 map 遍历或 query group 顺序影响。
func appendSortedUniquePreCheckValues(base []string, extra ...string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
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
