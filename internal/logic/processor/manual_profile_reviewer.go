// manual_profile_reviewer.go implements the manual profile-instruction reviewer used by gRPC profile editing flows.
// manual_profile_reviewer.go 用于实现 gRPC 手工画像编辑流程使用的画像指令评审器。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// ManualProfileReviewer drives prompt lookup, one explicit instruction review call, and strict JSON parsing for profile-node mutations.
// ManualProfileReviewer 用于驱动提示词读取、单次显式画像指令评审调用，以及面向画像节点变更的严格 JSON 解析。
type ManualProfileReviewer struct {
	llm     logicports.LLMClient
	prompts logicports.PromptSource
	model   string
}

// NewManualProfileReviewer creates a ManualProfileReviewer instance.
// NewManualProfileReviewer 用于创建 ManualProfileReviewer 实例。
func NewManualProfileReviewer(llm logicports.LLMClient, prompts logicports.PromptSource, model string) *ManualProfileReviewer {
	return &ManualProfileReviewer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Review compares one explicit manual instruction against the current active nodes of one target and returns structured node-mutation instructions.
// Review 用于把一条显式手工画像指令与单个目标当前 active 节点做对照，并返回结构化的节点变更指令。
func (r *ManualProfileReviewer) Review(ctx context.Context, target logicdomain.ProfileTargetRef, activeNodes []logicdomain.ProfileNodeRecord, instruction string, floorPriority, floorLevel int) (logicdomain.ManualProfileInstructionReview, error) {
	if r == nil || r.llm == nil {
		return logicdomain.ManualProfileInstructionReview{}, fmt.Errorf("manual profile reviewer llm client is nil")
	}
	if strings.TrimSpace(instruction) == "" {
		return logicdomain.ManualProfileInstructionReview{}, logicdomain.ValidationError{Field: "instruction", Message: "is required"}
	}
	prompt, err := r.prompts.GetPrompt("profile_instruction_main", r.model)
	if err != nil {
		return logicdomain.ManualProfileInstructionReview{}, fmt.Errorf("load profile_instruction_main prompt: %w", err)
	}
	requestBody, err := buildManualProfileReviewRequest(target, activeNodes, instruction, floorPriority, floorLevel)
	if err != nil {
		return logicdomain.ManualProfileInstructionReview{}, err
	}
	resp, err := r.llm.Generate(ctx, logicports.LLMRequest{
		Model:               r.model,
		SystemPrompt:        prompt,
		UserPrompt:          requestBody,
		ResponseFormat:      logicports.LLMResponseFormatJSON,
		RouteSelectionLevel: logicports.LLMRouteSelectionLevelProfileInstruction,
	})
	if err != nil {
		return logicdomain.ManualProfileInstructionReview{}, err
	}
	return parseManualProfileReviewResponse(resp.Content, activeNodes)
}

// buildManualProfileReviewRequest serializes one target, its active nodes, the incoming instruction, and the enforced authority floor into one stable JSON payload.
// buildManualProfileReviewRequest 用于把单个目标、其 active 节点、输入指令以及必须遵守的权限地板序列化成稳定 JSON 载荷。
func buildManualProfileReviewRequest(target logicdomain.ProfileTargetRef, activeNodes []logicdomain.ProfileNodeRecord, instruction string, floorPriority, floorLevel int) (string, error) {
	type activeNodeInput struct {
		ID            uint64 `json:"id"`
		DateTime      string `json:"datetime,omitempty"`
		Priority      string `json:"priority"`
		Level         string `json:"level"`
		RefreshWeight int    `json:"refresh_weight"`
		SourceKind    string `json:"source_kind"`
		SourceID      uint64 `json:"source_id"`
		Content       string `json:"content"`
	}
	type authorityFloorInput struct {
		Priority string `json:"priority"`
		Level    string `json:"level"`
		Reason   string `json:"reason"`
	}
	type requestBody struct {
		Target         string              `json:"target"`
		BindID         uint64              `json:"bind_id"`
		Instruction    string              `json:"instruction"`
		AuthorityFloor authorityFloorInput `json:"authority_floor"`
		ActiveNodes    []activeNodeInput   `json:"active_nodes"`
	}

	out := requestBody{
		Target:      profileTargetLabel(target.ProfileType),
		BindID:      target.BindID,
		Instruction: strings.TrimSpace(instruction),
		AuthorityFloor: authorityFloorInput{
			Priority: profilePriorityLabel(floorPriority),
			Level:    profileLevelLabel(floorLevel),
			Reason:   manualAuthorityFloorReason(target.ProfileType, floorPriority, floorLevel),
		},
		ActiveNodes: make([]activeNodeInput, 0, len(activeNodes)),
	}
	for _, node := range activeNodes {
		dateTime := ""
		if !node.ProfileDateAnchorAt.IsZero() {
			dateTime = logicdomain.FormatDisplayDateTime(node.ProfileDateAnchorAt)
		} else if !node.CreatedAt.IsZero() {
			dateTime = logicdomain.FormatDisplayDateTime(node.CreatedAt)
		}
		out.ActiveNodes = append(out.ActiveNodes, activeNodeInput{
			ID:            node.ID,
			DateTime:      dateTime,
			Priority:      profilePriorityLabel(node.Priority),
			Level:         profileLevelLabel(node.ProfileLevel),
			RefreshWeight: node.RefreshWeight,
			SourceKind:    profileSourceKindLabel(node.SourceKind),
			SourceID:      node.SourceID,
			Content:       strings.TrimSpace(node.Content),
		})
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal manual profile review request: %w", err)
	}
	return string(body), nil
}

// parseManualProfileReviewResponse validates the manual instruction review JSON and ensures every referenced node id came from the provided active set exactly once.
// parseManualProfileReviewResponse 用于校验手工画像指令评审 JSON，并确保所有引用到的节点 ID 都来自提供的 active 集合且不会重复退役。
func parseManualProfileReviewResponse(raw string, activeNodes []logicdomain.ProfileNodeRecord) (logicdomain.ManualProfileInstructionReview, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		AcceptedNodes []struct {
			NormalizedContent string `json:"normalized_content"`
			Priority          string `json:"priority"`
			Level             string `json:"level"`
			LevelReason       string `json:"level_reason"`
			SupersedeNodes    []struct {
				NodeID uint64 `json:"node_id"`
				Reason string `json:"reason"`
			} `json:"supersede_nodes"`
		} `json:"accepted_nodes"`
		RetiredNodes []struct {
			NodeID uint64 `json:"node_id"`
			Reason string `json:"reason"`
		} `json:"retired_nodes"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: "json decode failed", Raw: raw}
	}

	activeIDs := make(map[uint64]struct{}, len(activeNodes))
	for _, node := range activeNodes {
		if node.ID == 0 {
			continue
		}
		activeIDs[node.ID] = struct{}{}
	}
	retiredSeen := map[uint64]struct{}{}
	accepted := make([]logicdomain.ManualProfileAcceptedNode, 0, len(payload.AcceptedNodes))
	for idx, item := range payload.AcceptedNodes {
		content := strings.TrimSpace(item.NormalizedContent)
		if content == "" {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("accepted_nodes[%d].normalized_content is required", idx), Raw: raw}
		}
		priority, err := parseProfilePriorityLabel(item.Priority)
		if err != nil {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("accepted_nodes[%d].priority %s", idx, err.Error()), Raw: raw}
		}
		level, err := parseProfileLevelLabel(item.Level)
		if err != nil {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("accepted_nodes[%d].level %s", idx, err.Error()), Raw: raw}
		}
		supersede := make([]logicdomain.ProfileRetireDecision, 0, len(item.SupersedeNodes))
		for supIdx, decision := range item.SupersedeNodes {
			if decision.NodeID == 0 {
				return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("accepted_nodes[%d].supersede_nodes[%d].node_id is required", idx, supIdx), Raw: raw}
			}
			if _, ok := activeIDs[decision.NodeID]; !ok {
				return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("accepted_nodes[%d].supersede_nodes[%d].node_id %d was not present in active nodes", idx, supIdx, decision.NodeID), Raw: raw}
			}
			if _, exists := retiredSeen[decision.NodeID]; exists {
				return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("node_id %d is retired multiple times", decision.NodeID), Raw: raw}
			}
			retiredSeen[decision.NodeID] = struct{}{}
			supersede = append(supersede, logicdomain.ProfileRetireDecision{
				NodeID: decision.NodeID,
				Reason: strings.TrimSpace(decision.Reason),
			})
		}
		accepted = append(accepted, logicdomain.ManualProfileAcceptedNode{
			NormalizedContent: content,
			Priority:          priority,
			ProfileLevel:      level,
			LevelReason:       strings.TrimSpace(item.LevelReason),
			SupersedeNodes:    supersede,
		})
	}

	retired := make([]logicdomain.ProfileRetireDecision, 0, len(payload.RetiredNodes))
	for idx, decision := range payload.RetiredNodes {
		if decision.NodeID == 0 {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("retired_nodes[%d].node_id is required", idx), Raw: raw}
		}
		if _, ok := activeIDs[decision.NodeID]; !ok {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("retired_nodes[%d].node_id %d was not present in active nodes", idx, decision.NodeID), Raw: raw}
		}
		if _, exists := retiredSeen[decision.NodeID]; exists {
			return logicdomain.ManualProfileInstructionReview{}, logicdomain.InvalidLLMOutputError{Scene: "profile_instruction_main", Message: fmt.Sprintf("node_id %d is retired multiple times", decision.NodeID), Raw: raw}
		}
		retiredSeen[decision.NodeID] = struct{}{}
		retired = append(retired, logicdomain.ProfileRetireDecision{
			NodeID: decision.NodeID,
			Reason: strings.TrimSpace(decision.Reason),
		})
	}
	return logicdomain.ManualProfileInstructionReview{
		AcceptedNodes: accepted,
		RetiredNodes:  retired,
		Reason:        strings.TrimSpace(payload.Reason),
	}, nil
}

// profileTargetLabel renders the compact target label used by the manual instruction request body.
// profileTargetLabel 用于渲染手工画像指令请求体里的紧凑目标标签。
func profileTargetLabel(profileType int) string {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return "USER"
	case logicdomain.ProfileTypeProject:
		return "PROJECT"
	case logicdomain.ProfileTypeTeam:
		return "TEAM"
	case logicdomain.ProfileTypeSpace:
		return "SPACE"
	default:
		return "UNKNOWN"
	}
}

// profileSourceKindLabel renders the compact source label used when showing active-node provenance to the manual reviewer.
// profileSourceKindLabel 用于渲染展示给手工评审器的紧凑来源标签，说明 active 节点的溯源。
func profileSourceKindLabel(sourceKind int) string {
	switch sourceKind {
	case logicdomain.ProfileSourceKindManualInstruction:
		return "manual_instruction"
	case logicdomain.ProfileSourceKindSystemSeed:
		return "system_seed"
	case logicdomain.ProfileSourceKindRetainedAfterUserDelete:
		return "retained_after_user_delete"
	default:
		return "turn_extract"
	}
}

// manualAuthorityFloorReason explains why one target has the enforced floor carried in the request body.
// manualAuthorityFloorReason 用于解释某个目标为什么必须遵守请求体里携带的权限地板。
func manualAuthorityFloorReason(profileType, floorPriority, floorLevel int) string {
	if profileType == logicdomain.ProfileTypeTeam || profileType == logicdomain.ProfileTypeSpace {
		return "Team and space manual instructions are highest-authority scope rules and must not be downgraded below the strongest organizational baseline."
	}
	return fmt.Sprintf("Manual profile instructions are explicit user assertions and must stay at or above [%s][%s].", profilePriorityLabel(floorPriority), profileLevelLabel(floorLevel))
}
