// session_batch_analyzer.go implements the queued session-batch extraction processor used by post-action workers.
// session_batch_analyzer.go 用于实现 post-action 队列工作器使用的 session 批处理提炼处理器。
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// SessionBatchAnalyzer drives prompt lookup, one structured LLM call, and strict JSON parsing for queued session windows.
// SessionBatchAnalyzer 用于驱动队列 session 窗口的提示词读取、单次结构化 LLM 调用和严格 JSON 解析。
type SessionBatchAnalyzer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewSessionBatchAnalyzer creates a SessionBatchAnalyzer instance.
// NewSessionBatchAnalyzer 用于创建 SessionBatchAnalyzer 实例。
func NewSessionBatchAnalyzer(llm appports.LLMClient, prompts appports.PromptSource, model string) *SessionBatchAnalyzer {
	return &SessionBatchAnalyzer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Analyze runs one structured LLM extraction over one queued session-analysis request body.
// Analyze 用于针对一份排队的 session 分析请求体执行一次结构化 LLM 提炼。
func (a *SessionBatchAnalyzer) Analyze(ctx context.Context, requestBody string) (logicdomain.SessionBatchAnalysis, error) {
	// Load the dedicated batch scene and issue one JSON request so the worker can analyze multiple pending turns together.
	// 加载专用批处理场景，并发起一次 JSON 请求，让工作器能够把多条待处理 turn 一起分析。
	if a == nil || a.llm == nil {
		return logicdomain.SessionBatchAnalysis{}, fmt.Errorf("session batch analyzer llm client is nil")
	}
	prompt, err := a.prompts.GetPrompt("analyze_session_batch", a.model)
	if err != nil {
		return logicdomain.SessionBatchAnalysis{}, fmt.Errorf("load analyze_session_batch prompt: %w", err)
	}
	resp, err := a.llm.Generate(ctx, appports.LLMRequest{
		Model:          a.model,
		SystemPrompt:   prompt,
		UserPrompt:     strings.TrimSpace(requestBody),
		ResponseFormat: appports.LLMResponseFormatJSON,
	})
	if err != nil {
		return logicdomain.SessionBatchAnalysis{}, err
	}
	return parseSessionBatchAnalysisResponse(resp.Content)
}

// parseSessionBatchAnalysisResponse normalizes the model output into the internal queued session-analysis contract.
// parseSessionBatchAnalysisResponse 用于把模型输出归一成内部队列 session 分析契约。
func parseSessionBatchAnalysisResponse(raw string) (logicdomain.SessionBatchAnalysis, error) {
	jsonBody, err := extractJSONObject(raw)
	if err != nil {
		return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: err.Error(), Raw: raw}
	}
	var payload struct {
		TurnResults []struct {
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
		} `json:"turn_results"`
		ObsoleteMemoryTurnIDs []uint64 `json:"obsolete_memory_turn_ids"`
	}
	if err := json.Unmarshal([]byte(jsonBody), &payload); err != nil {
		return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: "json decode failed", Raw: raw}
	}

	analysis := logicdomain.SessionBatchAnalysis{
		Turns:                 make([]logicdomain.SessionBatchTurnAnalysis, 0, len(payload.TurnResults)),
		ObsoleteMemoryTurnIDs: normalizeUint64Set(payload.ObsoleteMemoryTurnIDs),
	}
	seenTurns := map[uint64]struct{}{}
	for _, item := range payload.TurnResults {
		if item.TurnID == 0 {
			return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: "turn_results[].turn_id is required", Raw: raw}
		}
		if _, ok := seenTurns[item.TurnID]; ok {
			return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("duplicate turn_id %d", item.TurnID), Raw: raw}
		}
		seenTurns[item.TurnID] = struct{}{}

		turnResult := logicdomain.SessionBatchTurnAnalysis{
			TurnID:       item.TurnID,
			Details:      strings.TrimSpace(item.Details),
			MemoryNodes:  make([]logicdomain.MemoryNodeCandidate, 0, len(item.MemoryNodes)),
			ProfileNodes: make([]logicdomain.ProfileNodeCandidate, 0, len(item.ProfileNodes)),
		}
		memorySeen := map[string]struct{}{}
		for _, node := range item.MemoryNodes {
			node.Abstract = strings.TrimSpace(node.Abstract)
			node.Details = strings.TrimSpace(node.Details)
			if !logicdomain.ValidMemoryNodeCategory(node.Category) {
				return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("invalid memory category %d for turn %d", node.Category, item.TurnID), Raw: raw}
			}
			if node.Abstract == "" {
				return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("memory node abstract is required for turn %d", item.TurnID), Raw: raw}
			}
			if node.Details == "" {
				node.Details = node.Abstract
			}
			key := strconv.Itoa(node.Category) + "|" + node.Abstract + "|" + node.Details
			if _, ok := memorySeen[key]; ok {
				continue
			}
			memorySeen[key] = struct{}{}
			turnResult.MemoryNodes = append(turnResult.MemoryNodes, logicdomain.MemoryNodeCandidate{
				Category: node.Category,
				Abstract: node.Abstract,
				Details:  node.Details,
			})
		}
		profileSeen := map[string]struct{}{}
		for _, node := range item.ProfileNodes {
			node.Content = strings.TrimSpace(node.Content)
			if !logicdomain.ValidProfileType(node.ProfileType) {
				return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("invalid profile_type %d for turn %d", node.ProfileType, item.TurnID), Raw: raw}
			}
			if node.Content == "" {
				return logicdomain.SessionBatchAnalysis{}, logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("profile node content is required for turn %d", item.TurnID), Raw: raw}
			}
			key := strconv.Itoa(node.ProfileType) + "|" + node.Content
			if _, ok := profileSeen[key]; ok {
				continue
			}
			profileSeen[key] = struct{}{}
			turnResult.ProfileNodes = append(turnResult.ProfileNodes, logicdomain.ProfileNodeCandidate{
				ProfileType: node.ProfileType,
				Content:     node.Content,
				Status:      logicdomain.ProfileStatusPending,
			})
		}
		analysis.Turns = append(analysis.Turns, turnResult)
	}
	sort.Slice(analysis.Turns, func(i, j int) bool {
		return analysis.Turns[i].TurnID < analysis.Turns[j].TurnID
	})
	return analysis, nil
}

// normalizeUint64Set removes duplicates and returns a sorted uint64 slice for stable downstream comparisons.
// normalizeUint64Set 用于去重并返回排序后的 uint64 切片，方便下游进行稳定比较。
func normalizeUint64Set(values []uint64) []uint64 {
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
