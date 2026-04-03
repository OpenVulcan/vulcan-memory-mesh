// profile_reviewer.go implements the batched profile-node reviewer used by post-action to judge new profile candidates against active user/project nodes.
// profile_reviewer.go 用于实现 post-action 的批量画像节点评审器，让新画像候选与当前活跃 user/project 节点进行对照判断。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ProfileReviewer drives prompt lookup, one batched LLM review call, and strict JSON parsing for atomic profile-node decisions.
// ProfileReviewer 用于驱动原子化画像节点的提示词读取、单次批量 LLM 评审调用和严格 JSON 解析。
type ProfileReviewer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewProfileReviewer creates a ProfileReviewer instance.
// NewProfileReviewer 用于创建 ProfileReviewer 实例。
func NewProfileReviewer(llm appports.LLMClient, prompts appports.PromptSource, model string) *ProfileReviewer {
	return &ProfileReviewer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Review compares fresh profile candidates against the active node sets and returns structured accept/invalid/supersede instructions.
// Review 用于把新画像候选与当前活跃节点集合进行对照，并返回结构化的接纳、无效和替代指令。
func (r *ProfileReviewer) Review(ctx context.Context, snapshot logicdomain.ProfileReviewTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (logicdomain.TurnProfileReviewResult, error) {
	if len(nodes) == 0 {
		return logicdomain.TurnProfileReviewResult{}, nil
	}
	if r == nil || r.llm == nil {
		return logicdomain.TurnProfileReviewResult{}, fmt.Errorf("profile reviewer llm client is nil")
	}
	prompt, err := r.prompts.GetPrompt("review_profile_nodes", r.model)
	if err != nil {
		return logicdomain.TurnProfileReviewResult{}, fmt.Errorf("load review_profile_nodes prompt: %w", err)
	}
	requestBody, userCount, projectCount, err := buildProfileReviewRequest(snapshot, nodes)
	if err != nil {
		return logicdomain.TurnProfileReviewResult{}, err
	}
	if userCount == 0 && projectCount == 0 {
		return logicdomain.TurnProfileReviewResult{}, nil
	}
	resp, err := r.llm.Generate(ctx, appports.LLMRequest{
		Model:          r.model,
		SystemPrompt:   prompt,
		UserPrompt:     requestBody,
		ResponseFormat: appports.LLMResponseFormatJSON,
	})
	if err != nil {
		return logicdomain.TurnProfileReviewResult{}, err
	}
	return parseProfileReviewResponse(resp.Content, userCount, projectCount)
}

// buildProfileReviewRequest serializes the active atomic profile nodes plus the new candidates into one stable JSON payload.
// buildProfileReviewRequest 用于把活跃的原子化画像节点和新的候选一起序列化成稳定 JSON 载荷。
func buildProfileReviewRequest(snapshot logicdomain.ProfileReviewTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (string, int, int, error) {
	type activeNodeInput struct {
		ID            uint64 `json:"id"`
		Date          string `json:"date"`
		Priority      string `json:"priority"`
		Level         string `json:"level"`
		RefreshWeight int    `json:"refresh_weight"`
		Content       string `json:"content"`
	}
	type candidateInput struct {
		CandidateIndex int    `json:"candidate_index"`
		TurnID         uint64 `json:"turn_id"`
		Date           string `json:"date"`
		Content        string `json:"content"`
	}
	type targetInput struct {
		LatestActiveDate string            `json:"latest_active_date,omitempty"`
		ActiveNodes      []activeNodeInput `json:"active_nodes"`
		NewCandidates    []candidateInput  `json:"new_candidates"`
	}
	type requestBody struct {
		User    *targetInput `json:"user,omitempty"`
		Project *targetInput `json:"project,omitempty"`
	}

	buildActiveNodes := func(records []logicdomain.ProfileActiveNodeRecord) ([]activeNodeInput, string) {
		out := make([]activeNodeInput, 0, len(records))
		latestDate := ""
		for _, node := range records {
			date := strings.TrimSpace(node.ProfileDate)
			if date == "" && !node.CreatedAt.IsZero() {
				date = node.CreatedAt.UTC().Format("2006-01-02")
			}
			if latestDate == "" || (date != "" && date > latestDate) {
				latestDate = date
			}
			out = append(out, activeNodeInput{
				ID:            node.ID,
				Date:          date,
				Priority:      profilePriorityLabel(node.Priority),
				Level:         profileLevelLabel(node.ProfileLevel),
				RefreshWeight: node.RefreshWeight,
				Content:       strings.TrimSpace(node.Content),
			})
		}
		return out, latestDate
	}

	userCandidates := make([]candidateInput, 0, len(nodes))
	projectCandidates := make([]candidateInput, 0, len(nodes))
	for _, node := range nodes {
		content := strings.TrimSpace(node.Content)
		if content == "" || !logicdomain.ValidProfileType(node.ProfileType) {
			continue
		}
		date := strings.TrimSpace(node.ProfileDate)
		if date == "" {
			date = "unknown"
		}
		input := candidateInput{
			CandidateIndex: 0,
			TurnID:         node.SourceTurnID,
			Date:           date,
			Content:        content,
		}
		switch node.ProfileType {
		case logicdomain.ProfileTypeUser:
			input.CandidateIndex = len(userCandidates)
			userCandidates = append(userCandidates, input)
		case logicdomain.ProfileTypeProject:
			input.CandidateIndex = len(projectCandidates)
			projectCandidates = append(projectCandidates, input)
		}
	}

	request := requestBody{}
	if len(userCandidates) > 0 {
		activeNodes, latestDate := buildActiveNodes(snapshot.UserNodes)
		request.User = &targetInput{
			LatestActiveDate: latestDate,
			ActiveNodes:      activeNodes,
			NewCandidates:    userCandidates,
		}
	}
	if len(projectCandidates) > 0 {
		activeNodes, latestDate := buildActiveNodes(snapshot.ProjectNodes)
		request.Project = &targetInput{
			LatestActiveDate: latestDate,
			ActiveNodes:      activeNodes,
			NewCandidates:    projectCandidates,
		}
	}

	body, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal profile review request: %w", err)
	}
	return string(body), len(userCandidates), len(projectCandidates), nil
}

// parseProfileReviewResponse validates the batched reviewer response and normalizes it into the internal review contract.
// parseProfileReviewResponse 用于校验批量评审响应，并把它归一成内部评审契约。
func parseProfileReviewResponse(raw string, userCount, projectCount int) (logicdomain.TurnProfileReviewResult, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.TurnProfileReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		User    *profileReviewSectionPayload `json:"user"`
		Project *profileReviewSectionPayload `json:"project"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.TurnProfileReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: "json decode failed", Raw: raw}
	}

	result := logicdomain.TurnProfileReviewResult{}
	if result.User, err = parseProfileReviewSection(payload.User, userCount, "user", raw); err != nil {
		return logicdomain.TurnProfileReviewResult{}, err
	}
	if result.Project, err = parseProfileReviewSection(payload.Project, projectCount, "project", raw); err != nil {
		return logicdomain.TurnProfileReviewResult{}, err
	}
	return result, nil
}

// profileReviewSectionPayload mirrors one target block in the raw LLM JSON response before internal validation.
// profileReviewSectionPayload 用于映射 LLM 原始 JSON 响应中的单个目标块，随后再进入内部校验。
type profileReviewSectionPayload struct {
	AcceptedCandidates      []profileReviewAcceptedPayload `json:"accepted_candidates"`
	InvalidCandidateIndexes []int                          `json:"invalid_candidate_indexes"`
	RetireOnlyNodeIDs       []uint64                       `json:"retire_only_node_ids"`
	Reason                  string                         `json:"reason"`
}

// profileReviewAcceptedPayload mirrors one accepted candidate item before field-level validation.
// profileReviewAcceptedPayload 用于映射一条已接纳候选的原始 JSON 项，随后再做字段级校验。
type profileReviewAcceptedPayload struct {
	CandidateIndex    int      `json:"candidate_index"`
	NormalizedContent string   `json:"normalized_content"`
	Priority          string   `json:"priority"`
	Level             string   `json:"level"`
	LevelReason       string   `json:"level_reason"`
	SupersedeNodeIDs  []uint64 `json:"supersede_node_ids"`
}

// parseProfileReviewSection validates one target block and returns nil when the corresponding input side had no candidates.
// parseProfileReviewSection 用于校验单个目标块；若对应输入侧没有候选，则返回 nil。
func parseProfileReviewSection(payload *profileReviewSectionPayload, expectedCount int, label, raw string) (*logicdomain.ProfileReviewSection, error) {
	if expectedCount == 0 {
		return nil, nil
	}
	if payload == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("missing %s block", label), Raw: raw}
	}

	// Recover from duplicate candidate indexes before field validation so one repeated LLM item does not discard the whole review block.
	// 先对重复 candidate_index 做恢复性去重，再进入字段校验，避免模型偶发重复输出一项时整块评审结果被直接丢弃。
	acceptedPayloads := canonicalizeProfileReviewAcceptedPayloads(payload.AcceptedCandidates)
	accepted := make([]logicdomain.ProfileReviewAcceptedCandidate, 0, len(acceptedPayloads))
	seenAccepted := map[int]struct{}{}
	for idx, item := range acceptedPayloads {
		if item.CandidateIndex < 0 || item.CandidateIndex >= expectedCount {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s.accepted_candidates[%d].candidate_index out of range", label, idx), Raw: raw}
		}
		if _, ok := seenAccepted[item.CandidateIndex]; ok {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s.accepted_candidates contains duplicate candidate_index %d", label, item.CandidateIndex), Raw: raw}
		}
		seenAccepted[item.CandidateIndex] = struct{}{}
		priority, err := parseProfilePriorityLabel(item.Priority)
		if err != nil {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s.accepted_candidates[%d].priority %s", label, idx, err.Error()), Raw: raw}
		}
		level, err := parseProfileLevelLabel(item.Level)
		if err != nil {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s.accepted_candidates[%d].level %s", label, idx, err.Error()), Raw: raw}
		}
		normalizedContent := strings.TrimSpace(item.NormalizedContent)
		if normalizedContent == "" {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s.accepted_candidates[%d].normalized_content is required", label, idx), Raw: raw}
		}
		accepted = append(accepted, logicdomain.ProfileReviewAcceptedCandidate{
			CandidateIndex:    item.CandidateIndex,
			NormalizedContent: normalizedContent,
			Priority:          priority,
			ProfileLevel:      level,
			LevelReason:       strings.TrimSpace(item.LevelReason),
			SupersedeNodeIDs:  normalizeUint64IDs(item.SupersedeNodeIDs),
		})
	}
	invalidIndexes, err := normalizeProfileMergeIndexes(payload.InvalidCandidateIndexes, expectedCount, label+".invalid_candidate_indexes", raw)
	if err != nil {
		return nil, err
	}
	if err := ensureProfileReviewCoverage(accepted, invalidIndexes, expectedCount, label, raw); err != nil {
		return nil, err
	}
	return &logicdomain.ProfileReviewSection{
		AcceptedCandidates:      accepted,
		InvalidCandidateIndexes: invalidIndexes,
		RetireOnlyNodeIDs:       normalizeUint64IDs(payload.RetireOnlyNodeIDs),
		Reason:                  strings.TrimSpace(payload.Reason),
	}, nil
}

// canonicalizeProfileReviewAcceptedPayloads collapses duplicate candidate_index entries into one deterministic winner so a repeated LLM item does not turn into a full review failure.
// canonicalizeProfileReviewAcceptedPayloads 用于把重复的 candidate_index 项收敛成一个确定性的胜出结果，避免模型重复输出同一候选时整次评审失败。
func canonicalizeProfileReviewAcceptedPayloads(items []profileReviewAcceptedPayload) []profileReviewAcceptedPayload {
	if len(items) == 0 {
		return nil
	}
	merged := make([]profileReviewAcceptedPayload, 0, len(items))
	indexByCandidate := make(map[int]int, len(items))
	for _, item := range items {
		item = normalizeProfileReviewAcceptedPayload(item)
		if existingIdx, ok := indexByCandidate[item.CandidateIndex]; ok {
			merged[existingIdx] = preferProfileReviewAcceptedPayload(merged[existingIdx], item)
			continue
		}
		indexByCandidate[item.CandidateIndex] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

// normalizeProfileReviewAcceptedPayload trims free-form text fields before duplicate recovery so semantically equivalent entries merge on stable content.
// normalizeProfileReviewAcceptedPayload 用于在重复恢复前裁剪自由文本字段，让语义等价的项基于稳定内容完成合并。
func normalizeProfileReviewAcceptedPayload(item profileReviewAcceptedPayload) profileReviewAcceptedPayload {
	item.NormalizedContent = strings.TrimSpace(item.NormalizedContent)
	item.Priority = strings.TrimSpace(item.Priority)
	item.Level = strings.TrimSpace(item.Level)
	item.LevelReason = strings.TrimSpace(item.LevelReason)
	item.SupersedeNodeIDs = normalizeUint64IDs(item.SupersedeNodeIDs)
	return item
}

// preferProfileReviewAcceptedPayload chooses the stronger duplicate accepted-candidate record and fills any missing fields from the weaker copy so runtime recovery stays deterministic.
// preferProfileReviewAcceptedPayload 用于在重复 accepted-candidate 记录里选择更强的一条，并用较弱副本补齐缺失字段，保证运行时恢复保持确定性。
func preferProfileReviewAcceptedPayload(primary, secondary profileReviewAcceptedPayload) profileReviewAcceptedPayload {
	primary = normalizeProfileReviewAcceptedPayload(primary)
	secondary = normalizeProfileReviewAcceptedPayload(secondary)

	preferred := primary
	fallback := secondary
	if profileReviewAcceptedPayloadStrength(secondary) > profileReviewAcceptedPayloadStrength(primary) {
		preferred = secondary
		fallback = primary
	}

	preferred.NormalizedContent = richerProfileReviewContent(preferred.NormalizedContent, fallback.NormalizedContent)
	if preferred.Priority == "" {
		preferred.Priority = fallback.Priority
	}
	if preferred.Level == "" {
		preferred.Level = fallback.Level
	}
	preferred.LevelReason = richerProfileReviewContent(preferred.LevelReason, fallback.LevelReason)
	preferred.SupersedeNodeIDs = normalizeUint64IDs(append(preferred.SupersedeNodeIDs, fallback.SupersedeNodeIDs...))
	return preferred
}

// profileReviewAcceptedPayloadStrength scores one duplicate accepted-candidate item so recovery prefers richer content and more complete lifecycle metadata.
// profileReviewAcceptedPayloadStrength 用于给重复 accepted-candidate 项打分，让恢复逻辑优先保留内容更丰富、生命周期元数据更完整的结果。
func profileReviewAcceptedPayloadStrength(item profileReviewAcceptedPayload) int {
	score := 0
	if text := strings.TrimSpace(item.NormalizedContent); text != "" {
		score += 1000 + len(text)
	}
	if strings.TrimSpace(item.Priority) != "" {
		score += 100
	}
	if strings.TrimSpace(item.Level) != "" {
		score += 100
	}
	score += len(strings.TrimSpace(item.LevelReason))
	score += len(normalizeUint64IDs(item.SupersedeNodeIDs)) * 10
	return score
}

// richerProfileReviewContent keeps the more informative non-empty string so duplicate recovery preserves the highest-density wording returned by the reviewer.
// richerProfileReviewContent 用于保留信息量更高的非空字符串，让重复恢复尽量继承 reviewer 返回的高密度表述。
func richerProfileReviewContent(primary, secondary string) string {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	switch {
	case primary == "":
		return secondary
	case secondary == "":
		return primary
	case len([]rune(secondary)) > len([]rune(primary)):
		return secondary
	default:
		return primary
	}
}

// ensureProfileReviewCoverage enforces that every candidate is classified exactly once across accepted and invalid outputs.
// ensureProfileReviewCoverage 用于强制要求每条候选都且仅能一次地分类进 accepted 或 invalid 结果。
func ensureProfileReviewCoverage(accepted []logicdomain.ProfileReviewAcceptedCandidate, invalidIndexes []int, expectedCount int, label, raw string) error {
	seen := map[int]struct{}{}
	for _, item := range accepted {
		seen[item.CandidateIndex] = struct{}{}
	}
	for _, idx := range invalidIndexes {
		if _, ok := seen[idx]; ok {
			return logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s block overlaps accepted and invalid index %d", label, idx), Raw: raw}
		}
		seen[idx] = struct{}{}
	}
	if len(seen) != expectedCount {
		return logicdomain.InvalidLLMOutputError{Scene: "review_profile_nodes", Message: fmt.Sprintf("%s block must classify all %d candidates exactly once", label, expectedCount), Raw: raw}
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

// normalizeUint64IDs removes zeros, duplicates, and unstable ordering from id lists returned by the reviewer.
// normalizeUint64IDs 用于去掉评审输出中 id 列表的零值、重复项和不稳定顺序。
func normalizeUint64IDs(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}
