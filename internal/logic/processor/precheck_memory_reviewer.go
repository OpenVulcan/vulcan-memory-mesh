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

// Review executes one structured memory-adoption review over the numbered pre-check candidates.
// Review 用于对 pre-check 召回出来的带编号候选记忆执行一次结构化采纳评审。
func (r *PreCheckMemoryReviewer) Review(ctx context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	// Load the dedicated prompt scene and send one stable JSON request body so the reviewer can choose candidate numbers deterministically.
	// 加载专用场景提示词，并发送稳定的 JSON 请求体，让评审器可以确定性地选择候选编号。
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
		UserContent   string                                `json:"user_content"`
		SearchQueries []string                              `json:"search_queries,omitempty"`
		IntentReason  string                                `json:"intent_reason,omitempty"`
		Candidates    []logicdomain.PreCheckMemoryCandidate `json:"candidates,omitempty"`
	}
	body := requestBody{
		UserContent:   strings.TrimSpace(input.UserContent),
		SearchQueries: normalizeStringValues(input.SearchQueries),
		IntentReason:  strings.TrimSpace(input.IntentReason),
		Candidates:    normalizePreCheckReviewCandidates(input.Candidates),
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal review_precheck_memory request: %w", err)
	}
	return string(rendered), nil
}

// parsePreCheckMemoryReviewResponse validates the reviewer output and rejects candidate numbers that do not belong to the current request.
// parsePreCheckMemoryReviewResponse 用于校验评审器输出，并拒绝不属于本次候选集合的候选编号。
func parsePreCheckMemoryReviewResponse(raw string, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error) {
	// Extract the first JSON object so fenced code or provider wrappers do not break structured parsing.
	// 先抽取第一个 JSON 对象，避免 fenced code 或 provider 包装破坏结构化解析。
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_precheck_memory", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		SelectedCandidateNumbers []int    `json:"selected_candidate_numbers"`
		SelectedMemoryIDs        []uint64 `json:"selected_memory_ids"`
		Reason                   string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_precheck_memory", Message: "json decode failed", Raw: raw}
	}

	// Keep only deduplicated candidate numbers that belong to the current request so one bad model response cannot select foreign rows.
	// 只保留去重后且属于当前请求的候选编号，避免错误模型响应选中外部记录。
	allowed := make(map[int]uint64, len(input.Candidates))
	memoryToNumber := make(map[uint64]int, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if candidate.CandidateNumber <= 0 {
			continue
		}
		allowed[candidate.CandidateNumber] = candidate.MemoryID
		if candidate.MemoryID > 0 {
			memoryToNumber[candidate.MemoryID] = candidate.CandidateNumber
		}
	}
	selectedNumbers := normalizeSelectedPreCheckCandidateNumbers(payload.SelectedCandidateNumbers, payload.SelectedMemoryIDs, allowed, memoryToNumber)
	selected := make([]int, 0, len(selectedNumbers))
	seen := map[int]struct{}{}
	invalidNumbers := make([]int, 0)
	for _, candidateNumber := range selectedNumbers {
		if candidateNumber <= 0 {
			continue
		}
		if _, ok := allowed[candidateNumber]; !ok {
			invalidNumbers = append(invalidNumbers, candidateNumber)
			continue
		}
		if _, ok := seen[candidateNumber]; ok {
			continue
		}
		seen[candidateNumber] = struct{}{}
		selected = append(selected, candidateNumber)
	}
	if len(selected) == 0 && len(invalidNumbers) > 0 {
		return logicdomain.PreCheckMemoryReviewResult{}, logicdomain.InvalidLLMOutputError{
			Scene:   "review_precheck_memory",
			Message: fmt.Sprintf("selected_candidate_numbers contains unknown number %d", invalidNumbers[0]),
			Raw:     raw,
		}
	}
	return logicdomain.PreCheckMemoryReviewResult{
		SelectedCandidateNumbers: selected,
		Reason:                   strings.TrimSpace(payload.Reason),
	}, nil
}

// normalizeSelectedPreCheckCandidateNumbers merges reviewer-selected candidate numbers with memory-id fallbacks so partially malformed model output can still recover the intended picks.
// normalizeSelectedPreCheckCandidateNumbers 用于把 reviewer 选择的候选编号与 memory-id 回退结果合并起来，让模型部分格式错误时仍能恢复预期选择。
func normalizeSelectedPreCheckCandidateNumbers(candidateNumbers []int, memoryIDs []uint64, allowed map[int]uint64, memoryToNumber map[uint64]int) []int {
	selected := make([]int, 0, len(candidateNumbers)+len(memoryIDs))
	seen := make(map[int]struct{}, len(candidateNumbers)+len(memoryIDs))
	appendNumber := func(number int) {
		if number <= 0 {
			return
		}
		if _, ok := seen[number]; ok {
			return
		}
		seen[number] = struct{}{}
		selected = append(selected, number)
	}

	// Keep any candidate-number choices the model emitted so valid structured output continues to drive the primary ordering.
	// 先保留模型显式输出的 candidate number，让合法的结构化编号继续作为主排序来源。
	for _, number := range candidateNumbers {
		appendNumber(number)
	}

	// Add recoverable selections from memory ids as a secondary signal, so one malformed candidate number does not erase otherwise valid intent.
	// 再补充可由 memory id 恢复出的选择，避免单个错误 candidate number 抹掉本来有效的模型意图。
	for _, memoryID := range memoryIDs {
		number, ok := memoryToNumber[memoryID]
		if !ok {
			continue
		}
		if _, ok := allowed[number]; !ok {
			continue
		}
		appendNumber(number)
	}
	return selected
}

// normalizePreCheckReviewCandidates trims empty text noise and removes duplicate candidate numbers before the payload reaches the model.
// normalizePreCheckReviewCandidates 用于在候选载荷进入模型前裁掉空文本噪声并移除重复候选编号。
func normalizePreCheckReviewCandidates(values []logicdomain.PreCheckMemoryCandidate) []logicdomain.PreCheckMemoryCandidate {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(values))
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(values))
	for _, value := range values {
		if value.CandidateNumber <= 0 || value.MemoryID == 0 {
			continue
		}
		if _, ok := seen[value.CandidateNumber]; ok {
			continue
		}
		seen[value.CandidateNumber] = struct{}{}
		value.SourceKind = strings.TrimSpace(value.SourceKind)
		value.ScopeLevel = strings.TrimSpace(value.ScopeLevel)
		value.Abstract = strings.TrimSpace(value.Abstract)
		value.Details = strings.TrimSpace(value.Details)
		value.ScoreLabel = strings.TrimSpace(value.ScoreLabel)
		value.ScoreExplanation = strings.TrimSpace(value.ScoreExplanation)
		value.Origin = strings.TrimSpace(value.Origin)
		value.OriginLabel = strings.TrimSpace(value.OriginLabel)
		value.OriginExplanation = strings.TrimSpace(value.OriginExplanation)
		value.MatchedContextValues = normalizeStringValues(value.MatchedContextValues)
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
