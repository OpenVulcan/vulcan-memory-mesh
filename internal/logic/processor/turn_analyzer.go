// turn_analyzer.go implements the per-turn extraction processor used by post-action to turn one raw dialogue round into structured details, memory nodes, and profile nodes.
// turn_analyzer.go 用于实现 post-action 的逐轮提炼处理器，把一轮原始对话转成结构化的 details、memory 节点和 profile 节点。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TurnAnalyzer drives prompt lookup, LLM invocation, and JSON parsing for the turn-analysis scene.
// TurnAnalyzer 用于驱动逐轮分析场景的提示词读取、LLM 调用和 JSON 解析。
type TurnAnalyzer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewTurnAnalyzer creates a TurnAnalyzer instance.
// NewTurnAnalyzer 用于创建 TurnAnalyzer 实例。
func NewTurnAnalyzer(llm appports.LLMClient, prompts appports.PromptSource, model string) *TurnAnalyzer {
	return &TurnAnalyzer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
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
		return logicdomain.TurnAnalysis{}, fmt.Errorf("render analyze_turn request: %w", err)
	}
	prompt, err := a.prompts.GetPrompt("analyze_turn", a.model)
	if err != nil {
		return logicdomain.TurnAnalysis{}, fmt.Errorf("load analyze_turn prompt: %w", err)
	}
	prompt = renderTurnAnalysisSystemPrompt(prompt, input)
	resp, err := a.llm.Generate(ctx, appports.LLMRequest{
		Model:          a.model,
		SystemPrompt:   prompt,
		UserPrompt:     requestBody,
		ResponseFormat: appports.LLMResponseFormatJSON,
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
			Scene:   "analyze_turn",
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
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		TurnID      uint64 `json:"turn_id"`
		Details     string `json:"details"`
		MemoryNodes []struct {
			Category int    `json:"category"`
			Abstract string `json:"abstract"`
			Details  string `json:"details"`
		} `json:"memory_nodes"`
		ProfileNodes []struct {
			ProfileType int    `json:"profile_type"`
			Content     string `json:"content"`
		} `json:"profile_nodes"`
		SupersededMemoryIDs []uint64 `json:"superseded_memory_ids"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: "json decode failed", Raw: raw}
	}
	if payload.TurnID == 0 {
		return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: "turn_id is required", Raw: raw}
	}

	// Validate and de-duplicate extracted items so the persistence layer only receives canonical node candidates.
	// 校验并去重提炼项，让持久化层只接收规范化后的节点候选。
	analysis := logicdomain.TurnAnalysis{
		TurnID:              payload.TurnID,
		Details:             strings.TrimSpace(payload.Details),
		MemoryNodes:         make([]logicdomain.MemoryNodeCandidate, 0, len(payload.MemoryNodes)),
		ProfileNodes:        make([]logicdomain.ProfileNodeCandidate, 0, len(payload.ProfileNodes)),
		SupersededMemoryIDs: normalizeUint64Set(payload.SupersededMemoryIDs),
	}
	memorySeen := map[string]struct{}{}
	for _, node := range payload.MemoryNodes {
		node.Abstract = strings.TrimSpace(node.Abstract)
		node.Details = strings.TrimSpace(node.Details)
		if !logicdomain.ValidMemoryNodeCategory(node.Category) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: fmt.Sprintf("invalid memory category %d", node.Category), Raw: raw}
		}
		if node.Abstract == "" {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: "memory node abstract is required", Raw: raw}
		}
		if node.Details == "" {
			node.Details = node.Abstract
		}
		key := fmt.Sprintf("%d|%s|%s", node.Category, node.Abstract, node.Details)
		if _, ok := memorySeen[key]; ok {
			continue
		}
		memorySeen[key] = struct{}{}
		analysis.MemoryNodes = append(analysis.MemoryNodes, logicdomain.MemoryNodeCandidate{
			Category: node.Category,
			Abstract: node.Abstract,
			Details:  node.Details,
		})
	}
	profileSeen := map[string]struct{}{}
	for _, node := range payload.ProfileNodes {
		node.Content = strings.TrimSpace(node.Content)
		if !logicdomain.ValidProfileType(node.ProfileType) {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: fmt.Sprintf("invalid profile_type %d", node.ProfileType), Raw: raw}
		}
		if node.Content == "" {
			return logicdomain.TurnAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: "profile node content is required", Raw: raw}
		}
		key := fmt.Sprintf("%d|%s", node.ProfileType, node.Content)
		if _, ok := profileSeen[key]; ok {
			continue
		}
		profileSeen[key] = struct{}{}
		analysis.ProfileNodes = append(analysis.ProfileNodes, logicdomain.ProfileNodeCandidate{
			ProfileType: node.ProfileType,
			Content:     node.Content,
			Status:      logicdomain.ProfileStatusPending,
		})
	}
	return analysis, nil
}
