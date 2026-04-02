// precheck_memory_reviewer.go implements the second-stage pre-check reviewer that selects truly useful memory rows for injection.
// precheck_memory_reviewer.go 用于实现 pre-check 的第二层评审器，负责从候选记忆里选出真正值得注入的条目。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// PreCheckMemoryReviewer drives prompt lookup, LLM invocation, and JSON parsing for the second-stage pre-check memory adoption scene.
// PreCheckMemoryReviewer 用于驱动 pre-check 第二层记忆采纳场景的提示词读取、LLM 调用和 JSON 解析。
type PreCheckMemoryReviewer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewPreCheckMemoryReviewer creates a PreCheckMemoryReviewer instance.
// NewPreCheckMemoryReviewer 用于创建 PreCheckMemoryReviewer 实例。
func NewPreCheckMemoryReviewer(llm appports.LLMClient, prompts appports.PromptSource, model string) *PreCheckMemoryReviewer {
	return &PreCheckMemoryReviewer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Review executes one structured memory-adoption review over the recalled pre-check candidates.
// Review 用于对 pre-check 召回出来的候选记忆执行一次结构化采纳评审。
func (r *PreCheckMemoryReviewer) Review(ctx context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	// Load the dedicated prompt scene and send one stable JSON request body so the reviewer can choose memory ids deterministically.
	// 加载专用场景提示词，并发送稳定的 JSON 请求体，让评审器可以确定性地选择 memory id。
	if r == nil || r.llm == nil {
		return logicdomain.PreCheckMemoryReviewResult{}, fmt.Errorf("pre-check memory reviewer llm client is nil")
	}
	requestBody, err := renderPreCheckMemoryReviewRequest(input)
	if err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, fmt.Errorf("render review_precheck_memory request: %w", err)
	}
	prompt, err := r.prompts.GetPrompt("review_precheck_memory", r.model)
	if err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, fmt.Errorf("load review_precheck_memory prompt: %w", err)
	}
	resp, err := r.llm.Generate(ctx, appports.LLMRequest{
		Model:          r.model,
		SystemPrompt:   prompt,
		UserPrompt:     requestBody,
		ResponseFormat: appports.LLMResponseFormatJSON,
	})
	if err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, err
	}
	return parsePreCheckMemoryReviewResponse(resp.Content, input)
}

// renderPreCheckMemoryReviewRequest serializes the second-stage review input into one stable JSON payload.
// renderPreCheckMemoryReviewRequest 用于把第二层评审输入序列化成稳定的 JSON 请求体。
func renderPreCheckMemoryReviewRequest(input logicdomain.PreCheckMemoryReviewInput) (string, error) {
	type requestBody struct {
		UserContent           string                                `json:"user_content"`
		IntentKeywords        []string                              `json:"intent_keywords"`
		IntentReason          string                                `json:"intent_reason,omitempty"`
		RecentSessionMemories []logicdomain.PreCheckMemoryCandidate `json:"recent_session_memories,omitempty"`
		RetrievedMemories     []logicdomain.PreCheckMemoryCandidate `json:"retrieved_memories,omitempty"`
	}
	body := requestBody{
		UserContent:           strings.TrimSpace(input.UserContent),
		IntentKeywords:        normalizeStringValues(input.IntentKeywords),
		IntentReason:          strings.TrimSpace(input.IntentReason),
		RecentSessionMemories: normalizePreCheckReviewCandidates(input.RecentSessionMemories),
		RetrievedMemories:     normalizePreCheckReviewCandidates(input.RetrievedMemories),
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal review_precheck_memory request: %w", err)
	}
	return string(rendered), nil
}

// parsePreCheckMemoryReviewResponse validates the reviewer output and rejects ids that do not belong to the candidate set of this request.
// parsePreCheckMemoryReviewResponse 用于校验评审器输出，并拒绝不属于本次候选集合的 id。
func parsePreCheckMemoryReviewResponse(raw string, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	// Extract the first JSON object so fenced code or provider wrappers do not break structured parsing.
	// 先抽取第一个 JSON 对象，避免 fenced code 或 provider 包装破坏结构化解析。
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_precheck_memory", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		SelectedMemoryIDs []uint64 `json:"selected_memory_ids"`
		Reason            string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_precheck_memory", Message: "json decode failed", Raw: raw}
	}

	// Keep only deduplicated ids that belong to the current candidate set so one bad model response cannot select foreign rows.
	// 只保留去重后且属于当前候选集合的 id，避免错误模型响应选中外部记录。
	allowed := make(map[uint64]struct{}, len(input.RecentSessionMemories)+len(input.RetrievedMemories))
	for _, candidate := range input.RecentSessionMemories {
		if candidate.MemoryID > 0 {
			allowed[candidate.MemoryID] = struct{}{}
		}
	}
	for _, candidate := range input.RetrievedMemories {
		if candidate.MemoryID > 0 {
			allowed[candidate.MemoryID] = struct{}{}
		}
	}
	selected := make([]uint64, 0, len(payload.SelectedMemoryIDs))
	seen := map[uint64]struct{}{}
	for _, memoryID := range payload.SelectedMemoryIDs {
		if memoryID == 0 {
			continue
		}
		if _, ok := allowed[memoryID]; !ok {
			return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{
				Scene:   "review_precheck_memory",
				Message: fmt.Sprintf("selected_memory_ids contains unknown id %d", memoryID),
				Raw:     raw,
			}
		}
		if _, ok := seen[memoryID]; ok {
			continue
		}
		seen[memoryID] = struct{}{}
		selected = append(selected, memoryID)
	}
	return logicdomain.PreCheckMemoryReviewResult{
		SelectedMemoryIDs: selected,
		Reason:            strings.TrimSpace(payload.Reason),
	}, nil
}

// normalizePreCheckReviewCandidates trims empty text noise and removes duplicate candidate ids before the payload reaches the model.
// normalizePreCheckReviewCandidates 用于在候选载荷进入模型前裁掉空文本噪声并移除重复 id。
func normalizePreCheckReviewCandidates(values []logicdomain.PreCheckMemoryCandidate) []logicdomain.PreCheckMemoryCandidate {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(values))
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(values))
	for _, value := range values {
		if value.MemoryID == 0 {
			continue
		}
		if _, ok := seen[value.MemoryID]; ok {
			continue
		}
		seen[value.MemoryID] = struct{}{}
		value.SourceKind = strings.TrimSpace(value.SourceKind)
		value.ScopeLevel = strings.TrimSpace(value.ScopeLevel)
		value.Abstract = strings.TrimSpace(value.Abstract)
		value.Details = strings.TrimSpace(value.Details)
		value.Origin = strings.TrimSpace(value.Origin)
		if value.Abstract == "" && value.Details == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

// normalizeStringValues trims one string list and removes duplicates while preserving order.
// normalizeStringValues 用于在保持顺序的同时裁剪字符串列表并移除重复项。
func normalizeStringValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
