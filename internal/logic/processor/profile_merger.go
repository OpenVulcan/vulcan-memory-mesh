// profile_merger.go implements the batched profile-merge processor used by post-action to fold one turn's user/project profile evidence into durable profile blobs.
// profile_merger.go 用于实现 post-action 的批量画像合并处理器，把单次 turn 的 user/project 画像证据折叠进长期画像 Blob。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// ProfileMerger drives prompt lookup, one batched LLM merge call, and strict JSON parsing for user/project profile blobs.
// ProfileMerger 用于驱动 user/project 画像 Blob 的提示词读取、单次批量 LLM 合并调用和严格 JSON 解析。
type ProfileMerger struct {
	llm     logicports.LLMClient
	prompts logicports.PromptSource
	model   string
}

// NewProfileMerger creates a ProfileMerger instance.
// NewProfileMerger 用于创建 ProfileMerger 实例。
func NewProfileMerger(llm logicports.LLMClient, prompts logicports.PromptSource, model string) *ProfileMerger {
	return &ProfileMerger{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Merge folds the current turn's user/project profile candidates into the durable profile blobs with one batched JSON call.
// Merge 用于通过一次批量 JSON 调用，把当前 turn 的 user/project 画像候选合并进长期画像 Blob。
func (m *ProfileMerger) Merge(ctx context.Context, snapshot logicdomain.ProfileTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (logicdomain.TurnProfileMergeResult, error) {
	// Skip empty candidate batches early so post-action does not spend an LLM call when the analyzer produced no profile evidence.
	// 在候选为空时尽早跳过，避免分析器没有产出画像证据时仍额外消耗一次 LLM 调用。
	if len(nodes) == 0 {
		return logicdomain.TurnProfileMergeResult{}, nil
	}
	if m == nil || m.llm == nil {
		return logicdomain.TurnProfileMergeResult{}, fmt.Errorf("profile merger llm client is nil")
	}

	// Load the dedicated merge prompt and send both target kinds in one batched json request so one turn never fans out into per-node merges.
	// 加载专用合并提示词，并把两类目标一起放进一次批量 json 请求，避免单个 turn 扩散成逐节点合并。
	prompt, err := m.prompts.GetPrompt("merge_profile", m.model)
	if err != nil {
		return logicdomain.TurnProfileMergeResult{}, fmt.Errorf("load merge_profile prompt: %w", err)
	}
	requestBody, userCount, projectCount, err := buildProfileMergeRequest(snapshot, nodes)
	if err != nil {
		return logicdomain.TurnProfileMergeResult{}, err
	}
	if userCount == 0 && projectCount == 0 {
		return logicdomain.TurnProfileMergeResult{}, nil
	}
	resp, err := m.llm.Generate(ctx, logicports.LLMRequest{
		Model:          m.model,
		SystemPrompt:   prompt,
		UserPrompt:     requestBody,
		ResponseFormat: logicports.LLMResponseFormatJSON,
	})
	if err != nil {
		return logicdomain.TurnProfileMergeResult{}, err
	}
	return parseProfileMergeResponse(resp.Content, userCount, projectCount)
}

// buildProfileMergeRequest serializes the current durable profiles plus the new per-target candidate arrays into one stable JSON payload.
// buildProfileMergeRequest 用于把当前长期画像和新的分目标候选数组序列化成稳定 JSON 载荷。
func buildProfileMergeRequest(snapshot logicdomain.ProfileTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (string, int, int, error) {
	type profileMergeCandidate struct {
		Index   int    `json:"index"`
		Content string `json:"content"`
	}
	type profileMergeTargetInput struct {
		CurrentProfile string                  `json:"current_profile"`
		Candidates     []profileMergeCandidate `json:"candidates"`
	}
	type profileMergeRequest struct {
		User    *profileMergeTargetInput `json:"user,omitempty"`
		Project *profileMergeTargetInput `json:"project,omitempty"`
	}

	// Split candidates by target kind while preserving per-target stable indexes so the LLM can classify every candidate deterministically.
	// 按目标类型拆分候选，并保持每个目标内部稳定索引，让 LLM 可以确定性地标记每条候选。
	userCandidates := make([]profileMergeCandidate, 0, len(nodes))
	projectCandidates := make([]profileMergeCandidate, 0, len(nodes))
	for _, node := range nodes {
		content := strings.TrimSpace(node.Content)
		if content == "" || !logicdomain.ValidProfileType(node.ProfileType) {
			continue
		}
		switch node.ProfileType {
		case logicdomain.ProfileTypeUser:
			userCandidates = append(userCandidates, profileMergeCandidate{
				Index:   len(userCandidates),
				Content: content,
			})
		case logicdomain.ProfileTypeProject:
			projectCandidates = append(projectCandidates, profileMergeCandidate{
				Index:   len(projectCandidates),
				Content: content,
			})
		}
	}

	request := profileMergeRequest{}
	if len(userCandidates) > 0 {
		request.User = &profileMergeTargetInput{
			CurrentProfile: strings.TrimSpace(snapshot.UserProfile),
			Candidates:     userCandidates,
		}
	}
	if len(projectCandidates) > 0 {
		request.Project = &profileMergeTargetInput{
			CurrentProfile: strings.TrimSpace(snapshot.ProjectProfile),
			Candidates:     projectCandidates,
		}
	}

	body, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshal profile merge request: %w", err)
	}
	return string(body), len(userCandidates), len(projectCandidates), nil
}

// parseProfileMergeResponse validates the batched user/project merge response and normalizes it into the internal merge contract.
// parseProfileMergeResponse 用于校验批量 user/project 合并响应，并把它归一成内部合并契约。
func parseProfileMergeResponse(raw string, userCount, projectCount int) (logicdomain.TurnProfileMergeResult, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.TurnProfileMergeResult{}, logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		User    *profileMergeSectionPayload `json:"user"`
		Project *profileMergeSectionPayload `json:"project"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.TurnProfileMergeResult{}, logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: "json decode failed", Raw: raw}
	}

	// Validate each optional target block against its candidate count so every candidate is classified exactly once.
	// 按候选数量校验每个可选目标块，确保每条候选都被且只被分类一次。
	result := logicdomain.TurnProfileMergeResult{}
	if result.User, err = parseProfileMergeSection(payload.User, userCount, "user", raw); err != nil {
		return logicdomain.TurnProfileMergeResult{}, err
	}
	if result.Project, err = parseProfileMergeSection(payload.Project, projectCount, "project", raw); err != nil {
		return logicdomain.TurnProfileMergeResult{}, err
	}
	return result, nil
}

// profileMergeSectionPayload mirrors one target block in the raw LLM JSON response before internal validation.
// profileMergeSectionPayload 用于映射 LLM 原始 JSON 响应中的单个目标块，随后再进入内部校验。
type profileMergeSectionPayload struct {
	UpdatedProfile          string `json:"updated_profile"`
	MergedCandidateIndexes  []int  `json:"merged_candidate_indexes"`
	InvalidCandidateIndexes []int  `json:"invalid_candidate_indexes"`
	Reason                  string `json:"reason"`
}

// parseProfileMergeSection validates one target block and returns nil when the corresponding input side had no candidates.
// parseProfileMergeSection 用于校验单个目标块；若对应输入侧没有候选，则返回 nil。
func parseProfileMergeSection(payload *profileMergeSectionPayload, expectedCount int, label, raw string) (*logicdomain.ProfileMergeSection, error) {
	if expectedCount == 0 {
		return nil, nil
	}
	if payload == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: fmt.Sprintf("missing %s block", label), Raw: raw}
	}
	mergedIndexes, err := normalizeProfileMergeIndexes(payload.MergedCandidateIndexes, expectedCount, label+".merged_candidate_indexes", raw)
	if err != nil {
		return nil, err
	}
	invalidIndexes, err := normalizeProfileMergeIndexes(payload.InvalidCandidateIndexes, expectedCount, label+".invalid_candidate_indexes", raw)
	if err != nil {
		return nil, err
	}
	if err := ensureProfileMergeCoverage(mergedIndexes, invalidIndexes, expectedCount, label, raw); err != nil {
		return nil, err
	}
	return &logicdomain.ProfileMergeSection{
		UpdatedProfile:          strings.TrimSpace(payload.UpdatedProfile),
		MergedCandidateIndexes:  mergedIndexes,
		InvalidCandidateIndexes: invalidIndexes,
		Reason:                  strings.TrimSpace(payload.Reason),
	}, nil
}

// normalizeProfileMergeIndexes validates one index array, rejects out-of-range and duplicate indexes, and returns the sorted form.
// normalizeProfileMergeIndexes 用于校验单个索引数组，拒绝越界和重复索引，并返回排序后的结果。
func normalizeProfileMergeIndexes(values []int, expectedCount int, field, raw string) ([]int, error) {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(values))
	for _, idx := range values {
		if idx < 0 || idx >= expectedCount {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: fmt.Sprintf("%s contains out-of-range index %d", field, idx), Raw: raw}
		}
		if _, ok := seen[idx]; ok {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: fmt.Sprintf("%s contains duplicate index %d", field, idx), Raw: raw}
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	sort.Ints(out)
	return out, nil
}

// ensureProfileMergeCoverage enforces that a target block classifies every candidate exactly once across merged and invalid buckets.
// ensureProfileMergeCoverage 用于强制要求一个目标块必须把全部候选且仅一次地分类进 merged 或 invalid 桶。
func ensureProfileMergeCoverage(mergedIndexes, invalidIndexes []int, expectedCount int, label, raw string) error {
	seen := map[int]struct{}{}
	for _, idx := range mergedIndexes {
		seen[idx] = struct{}{}
	}
	for _, idx := range invalidIndexes {
		if _, ok := seen[idx]; ok {
			return logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: fmt.Sprintf("%s block overlaps merged and invalid index %d", label, idx), Raw: raw}
		}
		seen[idx] = struct{}{}
	}
	if len(seen) != expectedCount {
		return logicdomain.InvalidLLMOutputError{Scene: "merge_profile", Message: fmt.Sprintf("%s block must classify all %d candidates exactly once", label, expectedCount), Raw: raw}
	}
	return nil
}
