// postaction_candidate_reviewer.go implements the unified post-action reviewer that jointly decides memory dedupe admission and profile acceptance in one LLM call.
// postaction_candidate_reviewer.go 用于实现统一的 post-action reviewer，在一次 LLM 调用里同时判断记忆去重准入和画像接纳结果。
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

// PostActionCandidateReviewer drives prompt lookup, one joint LLM review call, and strict JSON parsing for post-action memory/profile candidate decisions.
// PostActionCandidateReviewer 用于驱动 post-action 记忆/画像联合评审的提示词读取、单次 LLM 调用和严格 JSON 解析。
type PostActionCandidateReviewer struct {
	llm     logicports.LLMClient
	prompts logicports.PromptSource
	model   string
}

// NewPostActionCandidateReviewer creates a PostActionCandidateReviewer instance.
// NewPostActionCandidateReviewer 用于创建 PostActionCandidateReviewer 实例。
func NewPostActionCandidateReviewer(llm logicports.LLMClient, prompts logicports.PromptSource, model string) *PostActionCandidateReviewer {
	return &PostActionCandidateReviewer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Review sends one stable JSON payload that lets the model jointly classify new memory and profile candidates for the current turn.
// Review 用于发送一份稳定 JSON 载荷，让模型对当前轮的新记忆与画像候选执行一次联合分类评审。
func (r *PostActionCandidateReviewer) Review(ctx context.Context, input logicdomain.PostActionCandidateReviewInput) (logicdomain.PostActionCandidateReviewResult, error) {
	if r == nil || r.llm == nil {
		return logicdomain.PostActionCandidateReviewResult{}, fmt.Errorf("post-action candidate reviewer llm client is nil")
	}
	requestBody, memoryCount, userCount, projectCount, err := renderPostActionCandidateReviewRequest(input)
	if err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, fmt.Errorf("render review_postaction_candidates request: %w", err)
	}
	if memoryCount == 0 && userCount == 0 && projectCount == 0 {
		return logicdomain.PostActionCandidateReviewResult{}, nil
	}
	prompt, err := r.prompts.GetPrompt("review_postaction_candidates", r.model)
	if err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, fmt.Errorf("load review_postaction_candidates prompt: %w", err)
	}
	resp, err := r.llm.Generate(ctx, logicports.LLMRequest{
		Model:               r.model,
		SystemPrompt:        prompt,
		UserPrompt:          requestBody,
		ResponseFormat:      logicports.LLMResponseFormatJSON,
		RouteSelectionLevel: logicports.LLMRouteSelectionLevelPostActionL2,
	})
	if err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, err
	}
	return parsePostActionCandidateReviewResponse(resp.Content, memoryCount, userCount, projectCount)
}

// renderPostActionCandidateReviewRequest serializes the current turn context, memory dedupe evidence, and profile review snapshot into one stable JSON body.
// renderPostActionCandidateReviewRequest 用于把当前轮上下文、记忆去重证据和画像评审快照序列化成一份稳定 JSON 请求体。
func renderPostActionCandidateReviewRequest(input logicdomain.PostActionCandidateReviewInput) (string, int, int, int, error) {
	type memoryMatchInput struct {
		MemoryID     uint64  `json:"memory_id"`
		SourceTurnID uint64  `json:"source_turn_id,omitempty"`
		ScopeLevel   string  `json:"scope_level,omitempty"`
		Category     int     `json:"category"`
		Score        float64 `json:"score"`
		Origin       string  `json:"origin,omitempty"`
		Abstract     string  `json:"abstract"`
		Details      string  `json:"details"`
	}
	type memoryCandidateInput struct {
		CandidateIndex  int                `json:"candidate_index"`
		Category        int                `json:"category"`
		Abstract        string             `json:"abstract"`
		Details         string             `json:"details"`
		EvidenceSource  string             `json:"evidence_source,omitempty"`
		AdmissionReason string             `json:"admission_reason,omitempty"`
		SimilarMemories []memoryMatchInput `json:"similar_memories,omitempty"`
	}
	type activeNodeInput struct {
		ID            uint64 `json:"id"`
		Date          string `json:"date"`
		Priority      string `json:"priority"`
		Level         string `json:"level"`
		RefreshWeight int    `json:"refresh_weight"`
		Content       string `json:"content"`
	}
	type profileCandidateInput struct {
		CandidateIndex  int    `json:"candidate_index"`
		TurnID          uint64 `json:"turn_id"`
		Date            string `json:"date"`
		Content         string `json:"content"`
		EvidenceSource  string `json:"evidence_source,omitempty"`
		AdmissionReason string `json:"admission_reason,omitempty"`
	}
	type targetInput struct {
		LatestActiveDate string                  `json:"latest_active_date,omitempty"`
		ActiveNodes      []activeNodeInput       `json:"active_nodes,omitempty"`
		NewCandidates    []profileCandidateInput `json:"new_candidates,omitempty"`
	}
	type requestBody struct {
		UserInputKind    string                 `json:"user_input_kind,omitempty"`
		UserContent      string                 `json:"user_content,omitempty"`
		AssistantContent string                 `json:"assistant_content,omitempty"`
		Memory           []memoryCandidateInput `json:"memory,omitempty"`
		User             *targetInput           `json:"user,omitempty"`
		Project          *targetInput           `json:"project,omitempty"`
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

	body := requestBody{
		UserInputKind:    strings.TrimSpace(input.UserInputKind),
		UserContent:      strings.TrimSpace(input.UserContent),
		AssistantContent: strings.TrimSpace(input.AssistantContent),
	}
	for _, candidate := range input.MemoryCandidates {
		if candidate.CandidateIndex < 0 {
			continue
		}
		abstract := strings.TrimSpace(candidate.Abstract)
		details := strings.TrimSpace(candidate.Details)
		if abstract == "" && details == "" {
			continue
		}
		if details == "" {
			details = abstract
		}
		item := memoryCandidateInput{
			CandidateIndex:  candidate.CandidateIndex,
			Category:        candidate.Category,
			Abstract:        abstract,
			Details:         details,
			EvidenceSource:  strings.TrimSpace(candidate.EvidenceSource),
			AdmissionReason: strings.TrimSpace(candidate.AdmissionReason),
			SimilarMemories: make([]memoryMatchInput, 0, len(candidate.SimilarMemories)),
		}
		for _, similar := range candidate.SimilarMemories {
			similarAbstract := strings.TrimSpace(similar.Abstract)
			similarDetails := strings.TrimSpace(similar.Details)
			if similarAbstract == "" && similarDetails == "" {
				continue
			}
			if similarDetails == "" {
				similarDetails = similarAbstract
			}
			item.SimilarMemories = append(item.SimilarMemories, memoryMatchInput{
				MemoryID:     similar.MemoryID,
				SourceTurnID: similar.SourceTurnID,
				ScopeLevel:   strings.TrimSpace(similar.ScopeLevel),
				Category:     similar.Category,
				Score:        similar.Score,
				Origin:       strings.TrimSpace(similar.Origin),
				Abstract:     similarAbstract,
				Details:      similarDetails,
			})
		}
		body.Memory = append(body.Memory, item)
	}

	userCandidates := make([]profileCandidateInput, 0, len(input.ProfileCandidates))
	projectCandidates := make([]profileCandidateInput, 0, len(input.ProfileCandidates))
	for _, candidate := range input.ProfileCandidates {
		content := strings.TrimSpace(candidate.Content)
		if content == "" || !logicdomain.ValidProfileType(candidate.ProfileType) {
			continue
		}
		item := profileCandidateInput{
			CandidateIndex:  0,
			TurnID:          candidate.SourceTurnID,
			Date:            strings.TrimSpace(candidate.ProfileDate),
			Content:         content,
			EvidenceSource:  strings.TrimSpace(candidate.EvidenceSource),
			AdmissionReason: strings.TrimSpace(candidate.AdmissionReason),
		}
		if item.Date == "" {
			item.Date = "unknown"
		}
		switch candidate.ProfileType {
		case logicdomain.ProfileTypeUser:
			item.CandidateIndex = len(userCandidates)
			userCandidates = append(userCandidates, item)
		case logicdomain.ProfileTypeProject:
			item.CandidateIndex = len(projectCandidates)
			projectCandidates = append(projectCandidates, item)
		}
	}
	if len(userCandidates) > 0 {
		activeNodes, latestDate := buildActiveNodes(input.ProfileTargets.UserNodes)
		body.User = &targetInput{
			LatestActiveDate: latestDate,
			ActiveNodes:      activeNodes,
			NewCandidates:    userCandidates,
		}
	}
	if len(projectCandidates) > 0 {
		activeNodes, latestDate := buildActiveNodes(input.ProfileTargets.ProjectNodes)
		body.Project = &targetInput{
			LatestActiveDate: latestDate,
			ActiveNodes:      activeNodes,
			NewCandidates:    projectCandidates,
		}
	}

	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("marshal review_postaction_candidates request: %w", err)
	}
	return string(rendered), len(body.Memory), len(userCandidates), len(projectCandidates), nil
}

// parsePostActionCandidateReviewResponse validates the joint reviewer output and normalizes it into the internal result contract.
// parsePostActionCandidateReviewResponse 用于校验联合 reviewer 输出，并把它归一成内部结果契约。
func parsePostActionCandidateReviewResponse(raw string, memoryCount, userCount, projectCount int) (logicdomain.PostActionCandidateReviewResult, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		Memory *struct {
			AcceptedCandidates []struct {
				CandidateIndex     int      `json:"candidate_index"`
				SupersedeMemoryIDs []uint64 `json:"supersede_memory_ids"`
			} `json:"accepted_candidates"`
			DroppedCandidates []struct {
				CandidateIndex int    `json:"candidate_index"`
				DedupeMemoryID uint64 `json:"dedupe_memory_id"`
			} `json:"dropped_candidates"`
			AcceptedCandidateIndexes []int  `json:"accepted_candidate_indexes"`
			DroppedCandidateIndexes  []int  `json:"dropped_candidate_indexes"`
			Reason                   string `json:"reason"`
		} `json:"memory"`
		User    *profileReviewSectionPayload `json:"user"`
		Project *profileReviewSectionPayload `json:"project"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: "json decode failed", Raw: raw}
	}

	result := logicdomain.PostActionCandidateReviewResult{}
	if result.Memory, err = parsePostActionMemoryReviewSection(payload.Memory, memoryCount, raw); err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, err
	}
	if result.User, err = parseProfileReviewSection(payload.User, userCount, "user", "review_postaction_candidates", raw); err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, err
	}
	if result.Project, err = parseProfileReviewSection(payload.Project, projectCount, "project", "review_postaction_candidates", raw); err != nil {
		return logicdomain.PostActionCandidateReviewResult{}, err
	}
	return result, nil
}

// parsePostActionMemoryReviewSection validates one memory review block and requires every memory candidate to be classified exactly once.
// parsePostActionMemoryReviewSection 用于校验单个记忆评审块，并要求每条记忆候选都且仅被分类一次。
func parsePostActionMemoryReviewSection(payload *struct {
	AcceptedCandidates []struct {
		CandidateIndex     int      `json:"candidate_index"`
		SupersedeMemoryIDs []uint64 `json:"supersede_memory_ids"`
	} `json:"accepted_candidates"`
	DroppedCandidates []struct {
		CandidateIndex int    `json:"candidate_index"`
		DedupeMemoryID uint64 `json:"dedupe_memory_id"`
	} `json:"dropped_candidates"`
	AcceptedCandidateIndexes []int  `json:"accepted_candidate_indexes"`
	DroppedCandidateIndexes  []int  `json:"dropped_candidate_indexes"`
	Reason                   string `json:"reason"`
}, expectedCount int, raw string) (*logicdomain.PostActionMemoryReviewSection, error) {
	if expectedCount == 0 {
		return nil, nil
	}
	if payload == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: "missing memory block", Raw: raw}
	}
	acceptedCandidates, accepted, err := normalizePostActionAcceptedMemoryCandidates(payload.AcceptedCandidates, payload.AcceptedCandidateIndexes, expectedCount, raw)
	if err != nil {
		return nil, err
	}
	droppedCandidates, dropped, err := normalizePostActionDroppedMemoryCandidates(payload.DroppedCandidates, payload.DroppedCandidateIndexes, expectedCount, raw)
	if err != nil {
		return nil, err
	}
	if err := ensurePostActionMemoryReviewCoverage(accepted, dropped, expectedCount, raw); err != nil {
		return nil, err
	}
	return &logicdomain.PostActionMemoryReviewSection{
		AcceptedCandidates:       acceptedCandidates,
		DroppedCandidates:        droppedCandidates,
		AcceptedCandidateIndexes: accepted,
		DroppedCandidateIndexes:  dropped,
		Reason:                   strings.TrimSpace(payload.Reason),
	}, nil
}

// normalizePostActionAcceptedMemoryCandidates validates the accepted memory payload, supports both the new structured form and the historical index-only fallback, and returns one normalized section plus its derived index list.
// normalizePostActionAcceptedMemoryCandidates 用于校验被接纳的记忆载荷，同时兼容新的结构化格式和历史索引格式，并返回归一化结果及其派生索引列表。
func normalizePostActionAcceptedMemoryCandidates(items []struct {
	CandidateIndex     int      `json:"candidate_index"`
	SupersedeMemoryIDs []uint64 `json:"supersede_memory_ids"`
}, legacyIndexes []int, expectedCount int, raw string) ([]logicdomain.PostActionAcceptedMemoryCandidate, []int, error) {
	if len(items) == 0 {
		indexes, err := normalizePostActionCandidateIndexes(legacyIndexes, expectedCount, "memory.accepted_candidate_indexes", raw)
		if err != nil {
			return nil, nil, err
		}
		accepted := make([]logicdomain.PostActionAcceptedMemoryCandidate, 0, len(indexes))
		for _, idx := range indexes {
			accepted = append(accepted, logicdomain.PostActionAcceptedMemoryCandidate{CandidateIndex: idx})
		}
		return accepted, indexes, nil
	}
	seen := make(map[int]struct{}, len(items))
	accepted := make([]logicdomain.PostActionAcceptedMemoryCandidate, 0, len(items))
	for _, item := range items {
		if item.CandidateIndex < 0 || item.CandidateIndex >= expectedCount {
			return nil, nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory.accepted_candidates index %d is out of range", item.CandidateIndex), Raw: raw}
		}
		if _, ok := seen[item.CandidateIndex]; ok {
			return nil, nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory.accepted_candidates repeats index %d", item.CandidateIndex), Raw: raw}
		}
		seen[item.CandidateIndex] = struct{}{}
		accepted = append(accepted, logicdomain.PostActionAcceptedMemoryCandidate{
			CandidateIndex:     item.CandidateIndex,
			SupersedeMemoryIDs: normalizeUint64IDs(item.SupersedeMemoryIDs),
		})
	}
	sort.Slice(accepted, func(i, j int) bool {
		return accepted[i].CandidateIndex < accepted[j].CandidateIndex
	})
	indexes := make([]int, 0, len(accepted))
	for _, item := range accepted {
		indexes = append(indexes, item.CandidateIndex)
	}
	return accepted, indexes, nil
}

// normalizePostActionDroppedMemoryCandidates validates the dropped memory payload, supports both the new structured form and the historical index-only fallback, and returns one normalized section plus its derived index list.
// normalizePostActionDroppedMemoryCandidates 用于校验被丢弃的记忆载荷，同时兼容新的结构化格式和历史索引格式，并返回归一化结果及其派生索引列表。
func normalizePostActionDroppedMemoryCandidates(items []struct {
	CandidateIndex int    `json:"candidate_index"`
	DedupeMemoryID uint64 `json:"dedupe_memory_id"`
}, legacyIndexes []int, expectedCount int, raw string) ([]logicdomain.PostActionDroppedMemoryCandidate, []int, error) {
	if len(items) == 0 {
		indexes, err := normalizePostActionCandidateIndexes(legacyIndexes, expectedCount, "memory.dropped_candidate_indexes", raw)
		if err != nil {
			return nil, nil, err
		}
		dropped := make([]logicdomain.PostActionDroppedMemoryCandidate, 0, len(indexes))
		for _, idx := range indexes {
			dropped = append(dropped, logicdomain.PostActionDroppedMemoryCandidate{CandidateIndex: idx})
		}
		return dropped, indexes, nil
	}
	seen := make(map[int]struct{}, len(items))
	dropped := make([]logicdomain.PostActionDroppedMemoryCandidate, 0, len(items))
	for _, item := range items {
		if item.CandidateIndex < 0 || item.CandidateIndex >= expectedCount {
			return nil, nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory.dropped_candidates index %d is out of range", item.CandidateIndex), Raw: raw}
		}
		if _, ok := seen[item.CandidateIndex]; ok {
			return nil, nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory.dropped_candidates repeats index %d", item.CandidateIndex), Raw: raw}
		}
		seen[item.CandidateIndex] = struct{}{}
		dropped = append(dropped, logicdomain.PostActionDroppedMemoryCandidate{
			CandidateIndex: item.CandidateIndex,
			DedupeMemoryID: item.DedupeMemoryID,
		})
	}
	sort.Slice(dropped, func(i, j int) bool {
		return dropped[i].CandidateIndex < dropped[j].CandidateIndex
	})
	indexes := make([]int, 0, len(dropped))
	for _, item := range dropped {
		indexes = append(indexes, item.CandidateIndex)
	}
	return dropped, indexes, nil
}

// normalizePostActionCandidateIndexes trims duplicates and validates that all candidate indexes belong to the current request range.
// normalizePostActionCandidateIndexes 用于去重候选索引，并校验所有索引都属于当前请求范围。
func normalizePostActionCandidateIndexes(indexes []int, expectedCount int, label, raw string) ([]int, error) {
	if len(indexes) == 0 {
		return nil, nil
	}
	seen := make(map[int]struct{}, len(indexes))
	out := make([]int, 0, len(indexes))
	for _, idx := range indexes {
		if idx < 0 || idx >= expectedCount {
			return nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("%s index %d is out of range", label, idx), Raw: raw}
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	sort.Ints(out)
	return out, nil
}

// ensurePostActionMemoryReviewCoverage enforces that every memory candidate appears exactly once across accepted and dropped outputs.
// ensurePostActionMemoryReviewCoverage 用于强制每条记忆候选必须且只能一次地出现在 accepted 或 dropped 输出中。
func ensurePostActionMemoryReviewCoverage(accepted, dropped []int, expectedCount int, raw string) error {
	seen := make(map[int]struct{}, expectedCount)
	for _, idx := range accepted {
		seen[idx] = struct{}{}
	}
	for _, idx := range dropped {
		if _, ok := seen[idx]; ok {
			return logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory block overlaps accepted and dropped index %d", idx), Raw: raw}
		}
		seen[idx] = struct{}{}
	}
	if len(seen) != expectedCount {
		return logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory block must classify all %d candidates exactly once", expectedCount), Raw: raw}
	}
	return nil
}
