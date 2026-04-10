// postaction_intake.go contains the post-action intake path, turn reconstruction helpers, and input-budget utilities used before queued analysis begins.
// postaction_intake.go 用于承载 post-action 在进入异步分析前的入站接收、turn 重建以及输入预算辅助逻辑。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// Execute persists one cleaned turn into the resolved session, enqueues background extraction, and returns immediately without blocking on LLM work.
// Execute 用于把清洗后的单条 turn 持久化到已解析的 session 中、把后台提炼工作入队，并在不等待 LLM 完成的情况下立即返回。
func (u *PostActionUseCase) Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error) {
	if u == nil {
		return PostActionResult{}, fmt.Errorf("post-action use case is nil")
	}
	if u.store == nil {
		return PostActionResult{}, fmt.Errorf("post-action relational store is nil")
	}

	// Validate the resolved session scope and the new text-only payload before touching storage.
	// 在访问存储前先校验已解析的 session 范围和新的纯文本载荷。
	if err := validatePostAction(cmd); err != nil {
		return PostActionResult{}, err
	}
	cmd = scrubPostActionCommandPII(u.piiScrubber, cmd)
	traceID := trace.IDFromContext(ctx)

	// Skip the noise gate for timeline-driven flows because the middle messages already indicate one interrupted or branching conversation.
	// 对带 timeline 的流程直接跳过噪声门，因为中间消息已经表明它不是简单的单轮问答。
	if u.noiseGate != nil && len(cmd.Timeline) == 0 {
		turns := []logicdomain.NormalizedTurn{{
			TurnIndex:      1,
			UserMessage:    strings.TrimSpace(cmd.UserContent),
			AssistantReply: strings.TrimSpace(cmd.AssistantContent),
			CreatedAt:      time.Now().UTC(),
		}}
		kept := u.noiseGate.FilterPersistableTurns(ctx, turns)
		if len(kept) == 0 {
			if u.logger != nil {
				u.logger.Info("post-action dropped by noise gate", "trace_id", traceID, "session_key", cmd.Session.SessionKey)
			}
			return PostActionResult{Accepted: true, TraceID: traceID}, nil
		}
	}

	// Require the single-turn analyzer before touching durable storage so accepted requests will not get stuck permanently without any worker implementation.
	// 在访问持久化存储前要求存在单轮分析器，避免请求被接受后却因没有任何工作器实现而永久悬空。
	if u.turnAnalyzer == nil {
		return PostActionResult{}, fmt.Errorf("post-action analyzer is nil")
	}

	// Persist one canonical turn record first so later retries and pre-check windows can always rely on durable turn history even before extraction finishes.
	// 先持久化一条标准 turn 记录，让后续重试和 pre-check 窗口即使在提炼尚未完成时，也始终能够依赖稳定的 turn 历史。
	timeline := make([]logicdomain.TurnTimelineItem, 0, len(cmd.Timeline))
	for _, item := range cmd.Timeline {
		timeline = append(timeline, logicdomain.TurnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	turn := logicdomain.TurnRecord{
		UserContent:      strings.TrimSpace(cmd.UserContent),
		Timeline:         timeline,
		AssistantContent: strings.TrimSpace(cmd.AssistantContent),
		CreatedAt:        time.Now().UTC(),
	}
	persistedTurn, err := u.store.AppendTurnRecord(ctx, cmd.Session, turn)
	if err != nil {
		return PostActionResult{}, err
	}

	// Queue the owning session for asynchronous extraction so the transport layer does not block on model latency.
	// 把所属 session 排入异步提炼队列，避免传输层被模型延迟卡住。
	if u.logger != nil {
		u.logger.Info("post-action turn queued", "trace_id", traceID, "session_key", cmd.Session.SessionKey, "session_id", cmd.Session.SessionID, "turn_id", persistedTurn.ID)
	}
	u.enqueueSessionAnalysis(cmd.Session)
	return PostActionResult{Accepted: true, TraceID: traceID}, nil
}

// validatePostAction checks the resolved identifiers and required turn fields for the new turn-level persistence flow.
// validatePostAction 用于校验新 turn 级持久化流程所需的已解析标识和必填字段。
func validatePostAction(cmd PostActionCommand) error {
	if cmd.Session.SessionID == 0 || strings.TrimSpace(cmd.Session.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if cmd.Session.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if cmd.Session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if strings.TrimSpace(cmd.UserContent) == "" {
		return logicdomain.ValidationError{Field: "user_content", Message: "is required"}
	}
	if strings.TrimSpace(cmd.AssistantContent) == "" {
		return logicdomain.ValidationError{Field: "assistant_content", Message: "is required"}
	}
	for idx, item := range cmd.Timeline {
		role := strings.TrimSpace(strings.ToLower(item.Type))
		if role != "user" && role != "assistant" {
			return logicdomain.ValidationError{Field: "timeline[" + strconv.Itoa(idx) + "].type", Message: "must be user or assistant"}
		}
		if strings.TrimSpace(item.Content) == "" {
			return logicdomain.ValidationError{Field: "timeline[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
	}
	return nil
}

// postActionAnalysisState captures the evolving session counters after the latest turn is appended so debug logs can explain the current window shape.
// postActionAnalysisState 用于记录最新 turn 追加后的 session 计数变化，方便调试日志解释当前窗口形态。
type postActionAnalysisState struct {
	NextTurnCount       int
	NextSummarizeBudget int
	IdleGap             time.Duration
}

// captureSessionAnalysisState derives the next turn count, next summarize budget, and idle gap from the current session state plus the newest persisted turn budget.
// captureSessionAnalysisState 用于根据当前 session 状态和最新 turn 的预算，推导下一次 turn 数、下一次 summarize_budget 以及空闲间隔。
func captureSessionAnalysisState(session logicdomain.SessionRef, currentTurnBudget int) postActionAnalysisState {
	state := postActionAnalysisState{
		NextTurnCount:       session.TurnCount + 1,
		NextSummarizeBudget: session.SummarizeBudget + currentTurnBudget,
	}
	if session.TurnCount > 0 && !session.UpdatedAt.IsZero() {
		state.IdleGap = time.Since(session.UpdatedAt)
	}
	return state
}

// rawTurnFromCommand rebuilds the current turn from the raw payload when available, while falling back to the cleaned payload so tests and older callers remain valid.
// rawTurnFromCommand 用于优先根据原始载荷重建当前 turn；如果没有原始字段，则回退到清洗后字段，保证测试和旧调用方仍可工作。
func rawTurnFromCommand(cmd PostActionCommand) logicdomain.TurnRecord {
	rawTimeline := cmd.RawTimeline
	if len(rawTimeline) == 0 {
		rawTimeline = cmd.Timeline
	}
	timeline := make([]logicdomain.TurnTimelineItem, 0, len(rawTimeline))
	for _, item := range rawTimeline {
		timeline = append(timeline, logicdomain.TurnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	rawUser := strings.TrimSpace(cmd.RawUserContent)
	if rawUser == "" {
		rawUser = strings.TrimSpace(cmd.UserContent)
	}
	rawAssistant := strings.TrimSpace(cmd.RawAssistantContent)
	if rawAssistant == "" {
		rawAssistant = strings.TrimSpace(cmd.AssistantContent)
	}
	return logicdomain.TurnRecord{
		UserContent:      rawUser,
		Timeline:         timeline,
		AssistantContent: rawAssistant,
	}
}

// turnRecordFromStoredTurn rebuilds one raw turn from the dehydrated row so background workers can rerun the same single-turn analyzer input after async queueing.
// turnRecordFromStoredTurn 用于从脱水 turn 行重建原始轮次，让后台工作器在异步入队后仍能重建同样的单轮分析输入。
func turnRecordFromStoredTurn(turn logicdomain.SessionTurnRecord) (logicdomain.TurnRecord, error) {
	userContent, timeline, assistantContent := parseDehydratedTurnContent(turn.DehydratedContent)
	if strings.TrimSpace(userContent) == "" && strings.TrimSpace(assistantContent) == "" && len(timeline) == 0 {
		return logicdomain.TurnRecord{}, fmt.Errorf("stored turn %d dehydrated_content could not be decoded", turn.ID)
	}
	rawTimeline := make([]logicdomain.TurnTimelineItem, 0, len(timeline))
	for _, item := range timeline {
		rawTimeline = append(rawTimeline, logicdomain.TurnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	return logicdomain.TurnRecord{
		UserContent:      strings.TrimSpace(userContent),
		Timeline:         rawTimeline,
		AssistantContent: strings.TrimSpace(assistantContent),
		CreatedAt:        turn.CreatedAt,
	}, nil
}

// buildPostActionTurnPayload serializes one turn into the same compact JSON structure used for current persistence-side budget estimation.
// buildPostActionTurnPayload 用于把一个 turn 序列化成当前持久化侧同款的紧凑 JSON 结构，并估算对应 token 预算。
func buildPostActionTurnPayload(turn logicdomain.TurnRecord) (string, int, error) {
	type turnTimelineItem struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	type turnPayload struct {
		User      string             `json:"user"`
		Timeline  []turnTimelineItem `json:"timeline"`
		Assistant string             `json:"assistant"`
	}

	timeline := make([]turnTimelineItem, 0, len(turn.Timeline))
	for _, item := range turn.Timeline {
		timeline = append(timeline, turnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	payload := turnPayload{
		User:      strings.TrimSpace(turn.UserContent),
		Timeline:  timeline,
		Assistant: strings.TrimSpace(turn.AssistantContent),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return string(body), estimator.Estimate(string(body)), nil
}

// estimatePostActionTextBudget applies the local token estimator to one extracted text field so turn details use the same budget heuristic as stored turn payloads.
// estimatePostActionTextBudget 用于对提炼文本应用本地 token 估算器，让 turn details 与落库 turn 载荷共享同一预算口径。
func estimatePostActionTextBudget(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return estimator.Estimate(text)
}

// trimHistoryTurnsByBudget keeps the most recent refined summaries inside the remaining analyzer budget so the target raw turn always has room.
// trimHistoryTurnsByBudget 用于在剩余分析预算内保留最近的已提炼历史精要，确保目标原始 turn 始终有足够空间。
func trimHistoryTurnsByBudget(turns []logicdomain.SessionTurnRecord, remainingBudget int) []logicdomain.SessionTurnRecord {
	if len(turns) == 0 || remainingBudget <= 0 {
		return nil
	}
	selected := make([]logicdomain.SessionTurnRecord, 0, len(turns))
	total := 0
	for i := len(turns) - 1; i >= 0; i-- {
		budget := turns[i].DetailsBudget
		if budget <= 0 {
			budget = estimatePostActionTextBudget(turns[i].Details)
		}
		if len(selected) > 0 && total+budget > remainingBudget {
			break
		}
		selected = append(selected, turns[i])
		total += budget
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}
