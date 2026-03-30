// postaction.go implements the main post-action persistence workflow on top of resolved session scope metadata.
// postaction.go 用于基于已解析 session 范围元数据实现主 post-action 持久化工作流。
package usecase

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// PostActionTimelineItem carries one validated timeline message that sits between the first user prompt and final assistant reply.
// PostActionTimelineItem 用于承载一条已经校验过的 timeline 中间消息，它位于首轮用户提问和最终助手回答之间。
type PostActionTimelineItem struct {
	Type    string
	Content string
}

// PostActionCommand carries the resolved session scope plus the new text-only post-action payload.
// PostActionCommand 用于承载已解析的 session 范围以及新的纯文本 post-action 载荷。
type PostActionCommand struct {
	Session             logicdomain.SessionRef
	UserContent         string
	AssistantContent    string
	Timeline            []PostActionTimelineItem
	RawUserContent      string
	RawAssistantContent string
	RawTimeline         []PostActionTimelineItem
}

// PostActionResult returns the asynchronous acknowledgement data back to the transport layer.
// PostActionResult 用于把异步确认结果返回给传输层。
type PostActionResult struct {
	Accepted bool
	TraceID  string
}

// PostActionExecutor is the interface consumed by the gRPC adapter to run the post-action workflow.
// PostActionExecutor 用于让 gRPC 适配层执行 post-action 工作流。
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

// PostActionTurnSummarizer is the tiny port used by post-action to run one debug-stage single-turn LLM summary when the session window reaches analysis thresholds.
// PostActionTurnSummarizer 用于给 post-action 在 session 窗口命中分析阈值后执行一次调试阶段的单轮 LLM 摘要。
type PostActionTurnSummarizer interface {
	Summarize(ctx context.Context, transcript string) (string, error)
}

// PostActionAnalysisConfig carries the session-level thresholds that decide when post-action should fire one debug-stage LLM summary.
// PostActionAnalysisConfig 用于承载 session 级阈值，并决定 post-action 何时触发一次调试阶段的 LLM 摘要。
type PostActionAnalysisConfig struct {
	TurnThreshold  int
	TokenThreshold int
	IdleTimeout    time.Duration
}

// PostActionUseCase stores one cleaned turn record and applies the noise gate only to simple single-round flows.
// PostActionUseCase 用于存储一条清洗后的 turn 记录，并只在简单单轮流程上执行噪声门。
type PostActionUseCase struct {
	noiseGate   appports.NoiseTurnFilter
	store       appports.RelationalStore
	summarizer  PostActionTurnSummarizer
	analysisCfg PostActionAnalysisConfig
	logger      *logx.Logger
}

// NewPostActionUseCase creates a PostActionUseCase instance.
// NewPostActionUseCase 用于创建 PostActionUseCase 实例。
func NewPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, summarizer PostActionTurnSummarizer, analysisCfg PostActionAnalysisConfig, logger *logx.Logger) *PostActionUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	if analysisCfg.TurnThreshold < 0 {
		analysisCfg.TurnThreshold = 0
	}
	if analysisCfg.TokenThreshold < 0 {
		analysisCfg.TokenThreshold = 0
	}
	if analysisCfg.IdleTimeout < 0 {
		analysisCfg.IdleTimeout = 0
	}
	return &PostActionUseCase{
		noiseGate:   noiseGate,
		store:       store,
		summarizer:  summarizer,
		analysisCfg: analysisCfg,
		logger:      logger,
	}
}

// Execute persists one cleaned turn into the resolved session after optional noise screening.
// Execute 用于在可选噪声筛查之后，把清洗后的单条 turn 持久化到已解析的 session 中。
func (u *PostActionUseCase) Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error) {
	// Validate the resolved session scope and the new text-only payload before touching storage.
	// 在访问存储前先校验已解析的 session 范围和新的纯文本载荷。
	if err := validatePostAction(cmd); err != nil {
		return PostActionResult{}, err
	}
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

	// Persist one canonical turn record so DuckDB can keep the cleaned conversation as one dehydrated analysis unit.
	// 持久化一条标准 turn 记录，让 DuckDB 可以把清洗后的对话保存为一个脱水分析单元。
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
	if err := u.store.AppendTurnRecord(ctx, cmd.Session, turn); err != nil {
		return PostActionResult{}, err
	}

	// Trigger one debug-stage single-turn LLM summary only after the cleaned turn has been durably written.
	// 只有在清洗后的 turn 已经稳定落库之后，才触发一次调试阶段的单轮 LLM 摘要。
	u.runDebugSessionAnalysis(ctx, traceID, cmd, turn)
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

// runDebugSessionAnalysis checks the configured thresholds and, when any of them fires, sends the current raw turn through the existing single-turn summary prompt for log-only inspection.
// runDebugSessionAnalysis 用于检查配置化阈值；一旦任一命中，就把当前原始 turn 送入现有的单轮摘要提示词，并且仅输出日志方便观察。
func (u *PostActionUseCase) runDebugSessionAnalysis(ctx context.Context, traceID string, cmd PostActionCommand, persistedTurn logicdomain.TurnRecord) {
	if u == nil || u.summarizer == nil {
		return
	}

	// Reuse the same turn-payload budget estimation logic as relational persistence so token-threshold decisions stay aligned with the stored summarize_budget counter.
	// 复用与关系持久化一致的 turn 载荷预算估算方式，让 token 阈值判断与落库后的 summarize_budget 计数保持一致。
	_, currentBudget, err := buildPostActionTurnPayload(persistedTurn)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action analysis budget build failed", "trace_id", traceID, "session_key", cmd.Session.SessionKey, "err", err)
		}
		return
	}
	trigger := u.resolveSessionAnalysisTrigger(cmd.Session, currentBudget)
	if len(trigger.Reasons) == 0 {
		return
	}

	// Feed the original dialogue content to the existing summarize_entry prompt so we can inspect whether the current single-turn extraction format is already usable.
	// 把原始对话内容送入现有 summarize_entry 提示词，先观察当前单轮提炼格式是否已经足够可用。
	transcript, err := buildPostActionAnalysisTranscript(rawTurnFromCommand(cmd))
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action analysis transcript build failed", "trace_id", traceID, "session_key", cmd.Session.SessionKey, "err", err)
		}
		return
	}
	summary, err := u.summarizer.Summarize(ctx, transcript)
	if err != nil {
		if u.logger != nil {
			u.logger.Error(
				"post-action llm analysis failed",
				"trace_id", traceID,
				"session_key", cmd.Session.SessionKey,
				"session_id", cmd.Session.SessionID,
				"trigger_reasons", strings.Join(trigger.Reasons, ","),
				"next_turn_count", trigger.NextTurnCount,
				"next_summarize_budget", trigger.NextSummarizeBudget,
				"idle_gap_ms", trigger.IdleGap.Milliseconds(),
				"err", err,
			)
		}
		return
	}
	if u.logger != nil {
		u.logger.Info(
			"post-action llm analysis result",
			"trace_id", traceID,
			"session_key", cmd.Session.SessionKey,
			"session_id", cmd.Session.SessionID,
			"prompt_scene", "summarize_entry",
			"trigger_reasons", strings.Join(trigger.Reasons, ","),
			"next_turn_count", trigger.NextTurnCount,
			"next_summarize_budget", trigger.NextSummarizeBudget,
			"idle_gap_ms", trigger.IdleGap.Milliseconds(),
			"analysis", summary,
		)
	}
}

// postActionAnalysisTrigger describes why one session window should run the debug-stage single-turn summary after the latest turn has landed.
// postActionAnalysisTrigger 用于描述最新 turn 落库后，为什么当前 session 窗口应该执行一次调试阶段的单轮摘要。
type postActionAnalysisTrigger struct {
	Reasons             []string
	NextTurnCount       int
	NextSummarizeBudget int
	IdleGap             time.Duration
}

// resolveSessionAnalysisTrigger evaluates the configured turn/token/idle thresholds against the current session window and the newest persisted turn budget.
// resolveSessionAnalysisTrigger 用于结合当前 session 窗口状态和最新 turn 的预算，评估 turn/token/idle 三类阈值是否命中。
func (u *PostActionUseCase) resolveSessionAnalysisTrigger(session logicdomain.SessionRef, currentTurnBudget int) postActionAnalysisTrigger {
	trigger := postActionAnalysisTrigger{
		Reasons:             []string{},
		NextTurnCount:       session.TurnCount + 1,
		NextSummarizeBudget: session.SummarizeBudget + currentTurnBudget,
	}
	if u == nil {
		return trigger
	}
	if u.analysisCfg.TurnThreshold > 0 && trigger.NextTurnCount >= u.analysisCfg.TurnThreshold {
		trigger.Reasons = append(trigger.Reasons, "turn_threshold")
	}
	if u.analysisCfg.TokenThreshold > 0 && trigger.NextSummarizeBudget >= u.analysisCfg.TokenThreshold {
		trigger.Reasons = append(trigger.Reasons, "token_threshold")
	}
	if u.analysisCfg.IdleTimeout > 0 && session.TurnCount > 0 && !session.UpdatedAt.IsZero() {
		trigger.IdleGap = time.Since(session.UpdatedAt)
		if trigger.IdleGap >= u.analysisCfg.IdleTimeout {
			trigger.Reasons = append(trigger.Reasons, "idle_timeout")
		}
	}
	return trigger
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

// buildPostActionAnalysisTranscript pretty-prints the current raw turn into a stable JSON transcript so the existing single-turn prompt can inspect the full dialogue structure.
// buildPostActionAnalysisTranscript 用于把当前原始 turn 以稳定 JSON 形式美化输出，让现有单轮提示词可以看到完整对话结构。
func buildPostActionAnalysisTranscript(turn logicdomain.TurnRecord) (string, error) {
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
	body, err := json.MarshalIndent(turnPayload{
		User:      strings.TrimSpace(turn.UserContent),
		Timeline:  timeline,
		Assistant: strings.TrimSpace(turn.AssistantContent),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
