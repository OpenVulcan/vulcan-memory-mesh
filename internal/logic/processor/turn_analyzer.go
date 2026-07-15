// turn_analyzer.go implements the per-turn extraction processor used by post-action to turn one raw dialogue round into structured details, memory nodes, and profile nodes.
// turn_analyzer.go 用于实现 post-action 的逐轮提炼处理器，把一轮原始对话转成结构化的 details、memory 节点和 profile 节点。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// TurnAnalyzer drives prompt lookup, LLM invocation, and JSON parsing for the first-stage post-action main scene.
// TurnAnalyzer 用于驱动 post-action 第一层主场景的提示词读取、LLM 调用和 JSON 解析。
type TurnAnalyzer struct {
	llm     logicports.LLMClient
	prompts logicports.PromptSource
	model   string
}

// NewTurnAnalyzer creates a TurnAnalyzer instance.
// NewTurnAnalyzer 用于创建 TurnAnalyzer 实例。
func NewTurnAnalyzer(llm logicports.LLMClient, prompts logicports.PromptSource, model string) *TurnAnalyzer {
	return &TurnAnalyzer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// AnalyzeModel returns the configured post-action first-stage model label so callers can annotate malformed-output logs with the exact model selection.
// AnalyzeModel 用于返回当前配置的 post-action 第一层模型标识，方便调用方在畸形输出日志里标注本次使用的模型。
func (a *TurnAnalyzer) AnalyzeModel() string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.model)
}

// Analyze runs one structured LLM extraction over one reference-aware single-turn analysis input.
// Analyze 用于针对一份参考感知型单轮分析输入执行一次结构化 LLM 提炼。
func (a *TurnAnalyzer) Analyze(ctx context.Context, input logicdomain.TurnAnalysisInput) (logicdomain.TurnAnalysis, error) {
	// Load the dedicated prompt scene, render the optional TAG sections, and submit one stable JSON request body for the target turn.
	// 加载专用场景提示词、渲染可选 TAG 片段，并为目标 turn 发送一份稳定 JSON 请求体。
	if a == nil || a.llm == nil {
		return logicdomain.TurnAnalysis{}, fmt.Errorf("turn analyzer llm client is nil")
	}
	requestBody, err := renderTurnAnalysisRequest(input)
	if err != nil {
		return logicdomain.TurnAnalysis{}, fmt.Errorf("render postaction_l1_main request: %w", err)
	}
	prompt, err := a.prompts.GetPrompt("postaction_l1_main", a.model)
	if err != nil {
		return logicdomain.TurnAnalysis{}, fmt.Errorf("load postaction_l1_main prompt: %w", err)
	}
	prompt = renderTurnAnalysisSystemPrompt(prompt, input)
	resp, err := a.llm.Generate(ctx, logicports.LLMRequest{
		Model:               a.model,
		SystemPrompt:        prompt,
		UserPrompt:          requestBody,
		ResponseFormat:      logicports.LLMResponseFormatJSON,
		RouteSelectionLevel: logicports.LLMRouteSelectionLevelPostActionL1,
	})
	if err != nil {
		return logicdomain.TurnAnalysis{}, err
	}
	analysis, err := parseTurnAnalysisResponse(resp.Content)
	if err != nil {
		return logicdomain.TurnAnalysis{}, err
	}
	if analysis.TurnID != input.TargetTurn.TurnID {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{
			Scene:   "postaction_l1_main",
			Message: fmt.Sprintf("turn_id mismatch: got %d want %d", analysis.TurnID, input.TargetTurn.TurnID),
			Raw:     resp.Content,
		}
	}
	return analysis, nil
}

// parseTurnAnalysisResponse normalizes the model output into the internal turn-analysis contract and rejects structurally invalid payloads.
// parseTurnAnalysisResponse 用于把模型输出归一成内部逐轮分析契约，并拒绝结构上无效的载荷。
func parseTurnAnalysisResponse(raw string) (logicdomain.TurnAnalysis, error) {
	// Extract the first JSON object so markdown fences or provider wrappers do not break structured parsing.
	// 抽取第一个 JSON 对象，避免 markdown 围栏或 provider 包装破坏结构化解析。
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		UserInputKind string `json:"user_input_kind"`
		TurnID        uint64 `json:"turn_id"`
		Details       string `json:"details"`
		MemoryNodes   []struct {
			Category        int     `json:"category"`
			Abstract        string  `json:"abstract"`
			Details         string  `json:"details"`
			EvidenceSource  string  `json:"evidence_source"`
			Admission       string  `json:"admission"`
			AdmissionReason *string `json:"admission_reason"`
			ContextEdges    []struct {
				ContextKey   string `json:"context_key"`
				ContextValue string `json:"context_value"`
				Relation     string `json:"relation"`
			} `json:"context_edges"`
		} `json:"memory_nodes"`
		ProfileNodes []struct {
			ProfileType     int     `json:"profile_type"`
			Content         string  `json:"content"`
			EvidenceSource  string  `json:"evidence_source"`
			Admission       string  `json:"admission"`
			AdmissionReason *string `json:"admission_reason"`
		} `json:"profile_nodes"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "json decode failed", Raw: raw}
	}
	if payload.TurnID == 0 {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "turn_id is required", Raw: raw}
	}

	// Validate and de-duplicate extracted items so the persistence layer only receives canonical node candidates.
	// 校验并去重提炼项，让持久化层只接收规范化后的节点候选。
	analysis := logicdomain.TurnAnalysis{
		UserInputKind: normalizeTurnAnalysisUserInputKind(payload.UserInputKind),
		TurnID:        payload.TurnID,
		Details:       strings.TrimSpace(payload.Details),
		MemoryNodes:   make([]logicdomain.MemoryNodeCandidate, 0, len(payload.MemoryNodes)),
		ProfileNodes:  make([]logicdomain.ProfileNodeCandidate, 0, len(payload.ProfileNodes)),
	}
	if !logicdomain.ValidTurnAnalysisUserInputKind(analysis.UserInputKind) {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid user_input_kind %q", payload.UserInputKind), Raw: raw}
	}
	memorySeen := map[string]int{}
	for _, node := range payload.MemoryNodes {
		node.Abstract = strings.TrimSpace(node.Abstract)
		node.Details = strings.TrimSpace(node.Details)
		if !logicdomain.ValidMemoryNodeCategory(node.Category) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid memory category %d", node.Category), Raw: raw}
		}
		if node.Abstract == "" {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "memory node abstract is required", Raw: raw}
		}
		if node.Details == "" {
			node.Details = node.Abstract
		}
		evidenceSource := normalizeTurnAnalysisEvidenceSource(node.EvidenceSource)
		if !logicdomain.ValidTurnAnalysisEvidenceSource(evidenceSource) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid memory evidence_source %q", node.EvidenceSource), Raw: raw}
		}
		admission := normalizeTurnAnalysisAdmission(node.Admission)
		if !logicdomain.ValidTurnAnalysisAdmission(admission) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid memory admission %q", node.Admission), Raw: raw}
		}
		if node.AdmissionReason == nil {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "memory admission_reason is required", Raw: raw}
		}
		admissionReason := normalizeTurnAnalysisAdmissionReason(*node.AdmissionReason)
		if !logicdomain.ValidTurnAnalysisAdmissionReasonForAdmission(admission, admissionReason) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid memory admission_reason %q for admission %q", *node.AdmissionReason, node.Admission), Raw: raw}
		}
		contextEdges, err := normalizeMemoryContextEdgeCandidates(node.ContextEdges)
		if err != nil {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: err.Error(), Raw: raw}
		}
		key := fmt.Sprintf("%d|%s|%s", node.Category, node.Abstract, node.Details)
		if existingIdx, ok := memorySeen[key]; ok {
			analysis.MemoryNodes[existingIdx].ContextEdges = mergeMemoryContextEdgeCandidates(analysis.MemoryNodes[existingIdx].ContextEdges, contextEdges)
			continue
		}
		memorySeen[key] = len(analysis.MemoryNodes)
		analysis.MemoryNodes = append(analysis.MemoryNodes, logicdomain.MemoryNodeCandidate{
			Category:        node.Category,
			Abstract:        node.Abstract,
			Details:         node.Details,
			EvidenceSource:  evidenceSource,
			Admission:       admission,
			AdmissionReason: admissionReason,
			ContextEdges:    contextEdges,
		})
	}
	profileSeen := map[string]struct{}{}
	for _, node := range payload.ProfileNodes {
		node.Content = strings.TrimSpace(node.Content)
		if !logicdomain.ValidProfileType(node.ProfileType) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid profile_type %d", node.ProfileType), Raw: raw}
		}
		if node.Content == "" {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "profile node content is required", Raw: raw}
		}
		evidenceSource := normalizeTurnAnalysisEvidenceSource(node.EvidenceSource)
		if !logicdomain.ValidTurnAnalysisEvidenceSource(evidenceSource) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid profile evidence_source %q", node.EvidenceSource), Raw: raw}
		}
		admission := normalizeTurnAnalysisAdmission(node.Admission)
		if !logicdomain.ValidTurnAnalysisAdmission(admission) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid profile admission %q", node.Admission), Raw: raw}
		}
		if node.AdmissionReason == nil {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: "profile admission_reason is required", Raw: raw}
		}
		admissionReason := normalizeTurnAnalysisAdmissionReason(*node.AdmissionReason)
		if !logicdomain.ValidTurnAnalysisAdmissionReasonForAdmission(admission, admissionReason) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("invalid profile admission_reason %q for admission %q", *node.AdmissionReason, node.Admission), Raw: raw}
		}
		key := fmt.Sprintf("%d|%s", node.ProfileType, node.Content)
		if _, ok := profileSeen[key]; ok {
			continue
		}
		profileSeen[key] = struct{}{}
		analysis.ProfileNodes = append(analysis.ProfileNodes, logicdomain.ProfileNodeCandidate{
			ProfileType:     node.ProfileType,
			Content:         node.Content,
			EvidenceSource:  evidenceSource,
			Admission:       admission,
			AdmissionReason: admissionReason,
			Status:          logicdomain.ProfileStatusPending,
		})
	}
	return analysis, nil
}

// normalizeTurnAnalysisUserInputKind trims and lowercases the declared user-input kind so prompt outputs can be validated against one strict canonical set.
// normalizeTurnAnalysisUserInputKind 用于裁剪并小写化声明的用户输入类型，让提示词输出可按严格的规范集合校验。
func normalizeTurnAnalysisUserInputKind(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// normalizeTurnAnalysisEvidenceSource trims and lowercases the declared evidence source so missing values surface as validation errors instead of being silently defaulted.
// normalizeTurnAnalysisEvidenceSource 用于裁剪并小写化声明的证据来源，让缺失值以校验错误暴露，而不是被静默补默认值。
func normalizeTurnAnalysisEvidenceSource(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// normalizeTurnAnalysisAdmission trims and lowercases the declared admission so prompt drift cannot silently turn unknown outputs into keep decisions.
// normalizeTurnAnalysisAdmission 用于裁剪并小写化声明的准入结论，避免提示词漂移被静默解释成 keep。
func normalizeTurnAnalysisAdmission(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// normalizeTurnAnalysisAdmissionReason trims and lowercases the declared first-pass rejection reason while keeping field-presence validation at the JSON boundary.
// normalizeTurnAnalysisAdmissionReason 用于裁剪并小写化声明的首轮拒绝原因，同时把字段存在性校验保留在 JSON 边界。
func normalizeTurnAnalysisAdmissionReason(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// normalizeMemoryContextEdgeCandidates validates and de-duplicates one raw context-edge slice so persistence only sees canonical support/rebuttal evidence labels.
// normalizeMemoryContextEdgeCandidates 用于校验并去重原始 context-edge 切片，保证持久化层只接收规范化的支持/反驳情境证据标签。
func normalizeMemoryContextEdgeCandidates(rawEdges []struct {
	ContextKey   string `json:"context_key"`
	ContextValue string `json:"context_value"`
	Relation     string `json:"relation"`
}) ([]logicdomain.MemoryContextEdgeCandidate, error) {
	if len(rawEdges) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(rawEdges))
	normalized := make([]logicdomain.MemoryContextEdgeCandidate, 0, len(rawEdges))
	for _, edge := range rawEdges {
		contextKey := normalizeMemoryContextKey(edge.ContextKey)
		contextValue := normalizeMemoryContextValue(edge.ContextValue)
		if contextKey == "" {
			return nil, fmt.Errorf("invalid context edge key %q", edge.ContextKey)
		}
		if contextValue == "" {
			return nil, fmt.Errorf("invalid context edge value %q", edge.ContextValue)
		}
		relation := strings.ToLower(strings.TrimSpace(edge.Relation))
		if !logicdomain.ValidMemoryContextRelation(relation) {
			return nil, fmt.Errorf("invalid context edge relation %q", edge.Relation)
		}
		key := contextKey + "|" + contextValue + "|" + relation
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, logicdomain.MemoryContextEdgeCandidate{
			ContextKey:   contextKey,
			ContextValue: contextValue,
			Relation:     relation,
		})
	}
	return normalized, nil
}

// mergeMemoryContextEdgeCandidates appends extra contextual evidence onto one existing candidate while preserving stable de-duplication semantics.
// mergeMemoryContextEdgeCandidates 用于把额外的情境证据合并到已有候选上，同时保持稳定的去重语义。
func mergeMemoryContextEdgeCandidates(base, extra []logicdomain.MemoryContextEdgeCandidate) []logicdomain.MemoryContextEdgeCandidate {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]logicdomain.MemoryContextEdgeCandidate, 0, len(base)+len(extra))
	appendEdge := func(edge logicdomain.MemoryContextEdgeCandidate) {
		key := strings.TrimSpace(edge.ContextKey) + "|" + strings.TrimSpace(edge.ContextValue) + "|" + strings.TrimSpace(edge.Relation)
		if key == "||" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		merged = append(merged, edge)
	}
	for _, edge := range base {
		appendEdge(edge)
	}
	for _, edge := range extra {
		appendEdge(edge)
	}
	return merged
}

// normalizeMemoryContextKey converts free-form context keys into lower snake-like labels so later indexing and filtering stay stable across model wording drift.
// normalizeMemoryContextKey 用于把自由形式的情境键归一成小写、接近 snake_case 的标签，降低模型措辞波动对索引和过滤的影响。
func normalizeMemoryContextKey(raw string) string {
	return logicdomain.NormalizeMemoryContextKey(raw)
}

// normalizeMemoryContextValue converts free-form contextual values into the shared durable lexical surface so semantically equivalent labels stop forking into separate evidence rows.
// normalizeMemoryContextValue 用于把自由形式的情境值转成共享的长期词法表面，避免语义等价的标签继续分裂成多条证据行。
func normalizeMemoryContextValue(raw string) string {
	return logicdomain.NormalizeMemoryContextValue(raw)
}

// normalizeUint64Set removes zeros and duplicates from one uint64 list while keeping the first-seen order stable for deterministic persistence and tests.
// normalizeUint64Set 用于去掉 uint64 列表中的零值和重复项，并保持首次出现顺序稳定，方便持久化和测试获得确定性结果。
func normalizeUint64Set(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(values))
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
	return out
}
