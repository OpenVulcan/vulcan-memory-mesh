// profile_review_shared.go keeps the shared profile-review label and response parsing helpers used by the
// unified post-action reviewer and the manual profile-instruction reviewer.
// profile_review_shared.go 用于保存统一 post-action reviewer 与手工画像指令 reviewer 共享的画像标签与响应解析辅助逻辑。
package processor

import (
	"fmt"
	"sort"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// profileReviewSectionPayload mirrors one target block in the raw LLM JSON response before internal validation.
// profileReviewSectionPayload 用于映射 LLM 原始 JSON 响应中的单个目标块，随后再进入内部校验。
type profileReviewSectionPayload struct {
	AcceptedCandidates      []profileReviewAcceptedPayload `json:"accepted_candidates,omitempty"`
	InvalidCandidateIndexes []int                          `json:"invalid_candidate_indexes,omitempty"`
	RetireOnlyNodeIDs       []uint64                       `json:"retire_only_node_ids,omitempty"`
	Reason                  string                         `json:"reason,omitempty"`
}

// profileReviewAcceptedPayload mirrors one accepted candidate item before field-level validation.
// profileReviewAcceptedPayload 用于映射一条已接纳候选的原始 JSON 项，随后再做字段级校验。
type profileReviewAcceptedPayload struct {
	CandidateIndex    int      `json:"candidate_index"`
	NormalizedContent string   `json:"normalized_content"`
	Priority          string   `json:"priority"`
	Level             string   `json:"level"`
	LevelReason       string   `json:"level_reason,omitempty"`
	SupersedeNodeIDs  []uint64 `json:"supersede_node_ids,omitempty"`
}

// parseProfileReviewSection validates one target block and returns nil when the corresponding input side had no candidates.
// parseProfileReviewSection 用于校验单个目标块；若对应输入侧没有候选，则返回 nil。
func parseProfileReviewSection(payload *profileReviewSectionPayload, expectedCount int, label, scene, raw string) (*logicdomain.ProfileReviewSection, error) {
	if expectedCount == 0 {
		return nil, nil
	}
	if payload == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("missing %s block", label), Raw: raw}
	}

	// Validate accepted candidates in raw response order so duplicate candidate indexes surface as output errors instead of being merged.
	// 按原始响应顺序校验接纳候选，让重复 candidate_index 作为输出错误暴露，而不是被合并掩盖。
	accepted := make([]logicdomain.ProfileReviewAcceptedCandidate, 0, len(payload.AcceptedCandidates))
	seenAccepted := map[int]struct{}{}
	for idx, item := range payload.AcceptedCandidates {
		item.NormalizedContent = strings.TrimSpace(item.NormalizedContent)
		item.Priority = strings.TrimSpace(item.Priority)
		item.Level = strings.TrimSpace(item.Level)
		item.LevelReason = strings.TrimSpace(item.LevelReason)
		if item.CandidateIndex < 0 || item.CandidateIndex >= expectedCount {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s.accepted_candidates[%d].candidate_index out of range", label, idx), Raw: raw}
		}
		if _, ok := seenAccepted[item.CandidateIndex]; ok {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s.accepted_candidates contains duplicate candidate_index %d", label, item.CandidateIndex), Raw: raw}
		}
		seenAccepted[item.CandidateIndex] = struct{}{}
		priority, err := parseProfilePriorityLabel(item.Priority)
		if err != nil {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s.accepted_candidates[%d].priority %s", label, idx, err.Error()), Raw: raw}
		}
		level, err := parseProfileLevelLabel(item.Level)
		if err != nil {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s.accepted_candidates[%d].level %s", label, idx, err.Error()), Raw: raw}
		}
		normalizedContent := strings.TrimSpace(item.NormalizedContent)
		if normalizedContent == "" {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s.accepted_candidates[%d].normalized_content is required", label, idx), Raw: raw}
		}
		supersedeNodeIDs, err := normalizeReviewerIDList(item.SupersedeNodeIDs, fmt.Sprintf("%s.accepted_candidates[%d].supersede_node_ids", label, idx), scene, raw)
		if err != nil {
			return nil, err
		}
		accepted = append(accepted, logicdomain.ProfileReviewAcceptedCandidate{
			CandidateIndex:    item.CandidateIndex,
			NormalizedContent: normalizedContent,
			Priority:          priority,
			ProfileLevel:      level,
			LevelReason:       strings.TrimSpace(item.LevelReason),
			SupersedeNodeIDs:  supersedeNodeIDs,
		})
	}
	invalidIndexes, err := normalizeProfileReviewIndexes(payload.InvalidCandidateIndexes, expectedCount, label+".invalid_candidate_indexes", scene, raw)
	if err != nil {
		return nil, err
	}
	if err := ensureProfileReviewCoverage(accepted, invalidIndexes, expectedCount, label, scene, raw); err != nil {
		return nil, err
	}
	retireOnlyNodeIDs, err := normalizeReviewerIDList(payload.RetireOnlyNodeIDs, label+".retire_only_node_ids", scene, raw)
	if err != nil {
		return nil, err
	}
	return &logicdomain.ProfileReviewSection{
		AcceptedCandidates:      accepted,
		InvalidCandidateIndexes: invalidIndexes,
		RetireOnlyNodeIDs:       retireOnlyNodeIDs,
		Reason:                  strings.TrimSpace(payload.Reason),
	}, nil
}

// normalizeProfileReviewIndexes validates one reviewer index array, rejects out-of-range and duplicate indexes, and returns the sorted form for stable downstream comparisons.
// normalizeProfileReviewIndexes 用于校验 reviewer 返回的一组索引，拒绝越界和重复项，并返回排序后的稳定结果，便于后续比较和落库。
func normalizeProfileReviewIndexes(values []int, expectedCount int, field, scene, raw string) ([]int, error) {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, idx := range values {
		if idx < 0 || idx >= expectedCount {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s contains out-of-range index %d", field, idx), Raw: raw}
		}
		if _, ok := seen[idx]; ok {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s contains duplicate index %d", field, idx), Raw: raw}
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	sort.Ints(out)
	return out, nil
}

// ensureProfileReviewCoverage enforces that every candidate is classified exactly once across accepted and invalid outputs.
// ensureProfileReviewCoverage 用于强制要求每条候选都且仅能一次地分类进 accepted 或 invalid 结果。
func ensureProfileReviewCoverage(accepted []logicdomain.ProfileReviewAcceptedCandidate, invalidIndexes []int, expectedCount int, label, scene, raw string) error {
	seen := map[int]struct{}{}
	for _, item := range accepted {
		seen[item.CandidateIndex] = struct{}{}
	}
	for _, idx := range invalidIndexes {
		if _, ok := seen[idx]; ok {
			return logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s block overlaps accepted and invalid index %d", label, idx), Raw: raw}
		}
		seen[idx] = struct{}{}
	}
	if len(seen) != expectedCount {
		return logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s block must classify all %d candidates exactly once", label, expectedCount), Raw: raw}
	}
	return nil
}

// profilePriorityLabel renders the compact P-label used in reviewer requests and later profile rendering.
// profilePriorityLabel 用于渲染评审请求和后续画像展示共享的紧凑 P 标签。
func profilePriorityLabel(priority int) string {
	switch priority {
	case logicdomain.ProfilePriorityP0:
		return "P0"
	case logicdomain.ProfilePriorityP1:
		return "P1"
	default:
		return "P2"
	}
}

// parseProfilePriorityLabel converts one reviewer priority label back into the internal numeric enum.
// parseProfilePriorityLabel 用于把评审输出中的优先级标签还原成内部数字枚举。
func parseProfilePriorityLabel(label string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "P0":
		return logicdomain.ProfilePriorityP0, nil
	case "P1":
		return logicdomain.ProfilePriorityP1, nil
	case "P2":
		return logicdomain.ProfilePriorityP2, nil
	default:
		return 0, fmt.Errorf("must be one of P0/P1/P2")
	}
}

// profileLevelLabel renders the compact L-label used in reviewer requests and later profile rendering.
// profileLevelLabel 用于渲染评审请求和后续画像展示共享的紧凑 L 标签。
func profileLevelLabel(level int) string {
	switch level {
	case logicdomain.ProfileLevelTransient:
		return "L0"
	case logicdomain.ProfileLevelSituational:
		return "L1"
	case logicdomain.ProfileLevelStable:
		return "L2"
	default:
		return "L3"
	}
}

// parseProfileLevelLabel converts one reviewer level label back into the internal numeric enum.
// parseProfileLevelLabel 用于把评审输出中的等级标签还原成内部数字枚举。
func parseProfileLevelLabel(label string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "L0":
		return logicdomain.ProfileLevelTransient, nil
	case "L1":
		return logicdomain.ProfileLevelSituational, nil
	case "L2":
		return logicdomain.ProfileLevelStable, nil
	case "L3":
		return logicdomain.ProfileLevelPersistent, nil
	default:
		return 0, fmt.Errorf("must be one of L0/L1/L2/L3")
	}
}

// normalizeReviewerIDList validates one reviewer-returned id list, rejecting zero and duplicate ids before sorting it for deterministic downstream use.
// normalizeReviewerIDList 用于校验 reviewer 返回的 id 列表，在排序供下游稳定使用前拒绝零值和重复 id。
func normalizeReviewerIDList(values []uint64, field, scene, raw string) ([]uint64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s contains zero id", field), Raw: raw}
		}
		if _, ok := seen[value]; ok {
			return nil, logicdomain.InvalidLLMOutputError{Scene: scene, Message: fmt.Sprintf("%s contains duplicate id %d", field, value), Raw: raw}
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out, nil
}
