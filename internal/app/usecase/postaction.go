// postaction.go implements the main post-action persistence workflow on top of resolved session scope metadata.
// postaction.go 用于基于已解析 session 范围元数据实现主 post-action 持久化工作流。
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
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

// PostActionResult returns the post-action acknowledgement data back to the transport layer.
// PostActionResult 用于把 post-action 的确认结果返回给传输层。
type PostActionResult struct {
	Accepted bool
	TraceID  string
}

// PostActionExecutor is the interface consumed by the gRPC adapter to run the post-action workflow.
// PostActionExecutor 用于让 gRPC 适配层执行 post-action 工作流。
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

// PostActionTurnAnalyzer is the tiny port used by post-action background workers to analyze one persisted turn after it has been durably queued.
// PostActionTurnAnalyzer 用于让 post-action 后台工作器在 turn 稳定落库并入队后，再对这一轮执行提炼分析。
type PostActionTurnAnalyzer interface {
	Analyze(ctx context.Context, input logicdomain.TurnAnalysisInput) (logicdomain.TurnAnalysis, error)
}

// PostActionProfileReviewer is the tiny port used by post-action to review fresh profile candidates against the current active user/project profile nodes.
// PostActionProfileReviewer 用于让 post-action 把新的画像候选与当前活跃的 user/project 画像节点进行评审。
type PostActionProfileReviewer interface {
	Review(ctx context.Context, snapshot logicdomain.ProfileReviewTargetsSnapshot, nodes []logicdomain.ProfileNodeCandidate) (logicdomain.TurnProfileReviewResult, error)
}

// PostActionAnalysisConfig carries the queue and history knobs used by the async single-turn extraction pipeline,
// while keeping a few legacy threshold fields for backward-compatible configuration parsing.
// PostActionAnalysisConfig 用于承载异步单轮提炼流水线的队列与历史窗口参数，
// 并保留少量旧阈值字段以兼容既有配置解析。
type PostActionAnalysisConfig struct {
	TurnThreshold     int
	TokenThreshold    int
	IdleTimeout       time.Duration
	HistoryTurns      int
	MaxInputTokens    int
	QueueScanInterval time.Duration
}

// PostActionUseCase stores one cleaned turn, queues asynchronous single-turn extraction work,
// and coordinates the maintenance tasks that keep long-lived profile state in sync.
// PostActionUseCase 用于存储一条清洗后的 turn、把后续单轮提炼排入异步工作器，
// 并协调长期画像状态保持同步所需的维护任务。
type PostActionUseCase struct {
	noiseGate               appports.NoiseTurnFilter
	store                   appports.RelationalStore
	embedding               appports.EmbeddingClient
	vector                  appports.VectorStore
	turnAnalyzer            PostActionTurnAnalyzer
	profiles                PostActionProfileReviewer
	analysisCfg             PostActionAnalysisConfig
	logger                  *logx.Logger
	queueCtx                context.Context
	queueCancel             context.CancelFunc
	queueWG                 sync.WaitGroup
	queueMu                 sync.Mutex
	maintenanceMu           sync.Mutex
	maintenanceBackoffUntil time.Time
	queueCh                 chan uint64
	queueState              map[uint64]*postActionQueueState
}

// NewPostActionUseCase creates a PostActionUseCase instance for the runtime asynchronous single-turn path.
// NewPostActionUseCase 用于为运行时异步单轮提炼路径创建 PostActionUseCase 实例。
func NewPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, embedding appports.EmbeddingClient, vector appports.VectorStore, turnAnalyzer PostActionTurnAnalyzer, profiles PostActionProfileReviewer, analysisCfg PostActionAnalysisConfig, logger *logx.Logger) *PostActionUseCase {
	return newPostActionUseCase(noiseGate, store, embedding, vector, turnAnalyzer, profiles, analysisCfg, logger, true)
}

// newPostActionUseCase builds the post-action use case and optionally starts the async queue worker for runtime paths.
// newPostActionUseCase 用于构建 post-action 用例，并按需启动运行时异步队列工作器。
func newPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, embedding appports.EmbeddingClient, vector appports.VectorStore, turnAnalyzer PostActionTurnAnalyzer, profiles PostActionProfileReviewer, analysisCfg PostActionAnalysisConfig, logger *logx.Logger, startWorker bool) *PostActionUseCase {
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
	if analysisCfg.HistoryTurns < 0 {
		analysisCfg.HistoryTurns = 0
	}
	if analysisCfg.MaxInputTokens < 0 {
		analysisCfg.MaxInputTokens = 0
	}
	if analysisCfg.QueueScanInterval <= 0 {
		analysisCfg.QueueScanInterval = 30 * time.Second
	}
	uc := &PostActionUseCase{
		noiseGate:    noiseGate,
		store:        store,
		embedding:    embedding,
		vector:       vector,
		turnAnalyzer: turnAnalyzer,
		profiles:     profiles,
		analysisCfg:  analysisCfg,
		logger:       logger,
	}
	if startWorker && store != nil {
		uc.startQueueWorker()
	}
	return uc
}

// Execute persists one cleaned turn into the resolved session, enqueues background extraction, and returns immediately without blocking on LLM work.
// Execute 用于把清洗后的单条 turn 持久化到已解析的 session 中、把后台提炼工作入队，并在不等待 LLM 完成的情况下立即返回。
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

// normalizeTurnProfileNodes resets freshly extracted profile nodes to pending so a later merge step can deterministically flip only the chosen indexes.
// normalizeTurnProfileNodes 用于把新提取出的画像节点统一重置为 pending，便于后续合并步骤只修改被选中的索引。
func normalizeTurnProfileNodes(analysis *logicdomain.TurnAnalysis) {
	if analysis == nil {
		return
	}
	for idx := range analysis.ProfileNodes {
		analysis.ProfileNodes[idx].Status = logicdomain.ProfileStatusPending
	}
}

// applyImmediateTurnAnalysis assembles the reference-aware single-turn request, runs the LLM, reviews fresh profile candidates, persists vectors, and writes the final result back onto the new turn.
// applyImmediateTurnAnalysis 用于组装参考感知的单轮请求、执行 LLM、评审新的画像候选、持久化向量，并把最终结果回写到新 turn 上。
func (u *PostActionUseCase) applyImmediateTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord) error {
	if u == nil {
		return nil
	}
	input, analysisCutoff, err := u.buildTurnAnalysisInput(ctx, session, turn, rawTurn)
	if err != nil {
		return err
	}
	analysis, err := u.turnAnalyzer.Analyze(ctx, input)
	if err != nil {
		return err
	}
	if err := validateTurnAnalysis(input, analysis); err != nil {
		return err
	}

	// Normalize fresh profile nodes before the review stage so a temporary reviewer failure can safely drop them instead of persisting invalid lifecycle fields.
	// 在画像评审前先规范化新节点状态，保证评审器临时失败时可以安全丢弃这些节点，而不会把非法生命周期字段写进库里。
	normalizeTurnProfileNodes(&analysis)
	if err := u.reviewTurnProfiles(ctx, session, turn, &analysis); err != nil {
		if u.logger != nil {
			u.logger.Error("post-action turn profile review failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", err)
		}
		analysis.ProfileNodes = nil
		analysis.UserProfileMerged = false
		analysis.MergedUserProfile = ""
		analysis.ProjectProfileMerged = false
		analysis.MergedProjectProfile = ""
	}

	vectorIDs, err := u.persistMemoryNodeVectors(ctx, session, turn, &analysis)
	if err != nil {
		return err
	}
	applyResult, err := u.store.ApplyTurnAnalysis(ctx, session, turn, analysis)
	if err != nil {
		if len(vectorIDs) > 0 && u.vector != nil {
			if _, rollbackErr := u.vector.DeleteByIDs(ctx, vectorIDs); rollbackErr != nil && u.logger != nil {
				u.logger.Error("post-action turn vector rollback failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", rollbackErr)
			}
		}
		return err
	}
	if err := u.store.AdvanceSessionExtractWindow(ctx, session.SessionID, analysisCutoff, analysisCutoff); err != nil {
		return err
	}
	if len(applyResult.SupersededVectorIDs) > 0 && u.vector != nil {
		if _, deleteErr := u.vector.DeleteByIDs(ctx, applyResult.SupersededVectorIDs); deleteErr != nil && u.logger != nil {
			u.logger.Error("post-action superseded vector cleanup failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", deleteErr)
		}
	}

	if u.logger != nil {
		analysisJSON, _ := json.Marshal(analysis)
		u.logger.Info(
			"post-action turn analysis result",
			"session_key", session.SessionKey,
			"session_id", session.SessionID,
			"turn_id", turn.ID,
			"reference_turn_count", len(input.ReferenceTurns),
			"active_memory_count", len(input.ActiveMemoryNodes),
			"recent_direct_write_count", len(input.RecentGRPCMemoryWrites),
			"vector_count", len(vectorIDs),
			"analysis", string(analysisJSON),
		)
	}
	return nil
}

// buildTurnAnalysisInput loads refined reference turns and active memory anchors, then builds the structured request consumed by the reference-aware single-turn analyzer.
// buildTurnAnalysisInput 用于加载已提炼的参考 turn 和活跃记忆锚点，并构建参考感知型单轮分析器需要的结构化请求。
func (u *PostActionUseCase) buildTurnAnalysisInput(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord) (logicdomain.TurnAnalysisInput, time.Time, error) {
	if u == nil || u.store == nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, fmt.Errorf("post-action relational store is nil")
	}
	analysisCutoff := time.Now().UTC()

	// Serialize the raw turn into the compact JSON payload used by the analyzer and reuse the same token-budget heuristic to trim reference history.
	// 先把原始 turn 序列化成分析器使用的紧凑 JSON 载荷，并复用同一套 token 预算规则裁剪参考历史。
	targetBody, targetBudget, err := buildPostActionTurnPayload(rawTurn)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, err
	}
	historyTurns, err := u.store.LoadRecentSessionHistory(ctx, session, u.analysisCfg.HistoryTurns)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, fmt.Errorf("load recent session history: %w", err)
	}
	selectedHistory := historyTurns
	if u.analysisCfg.MaxInputTokens > 0 {
		remainingBudget := u.analysisCfg.MaxInputTokens - targetBudget
		if remainingBudget < 0 {
			remainingBudget = 0
		}
		selectedHistory = trimHistoryTurnsByBudget(historyTurns, remainingBudget)
	}
	activeMemoryNodes, err := u.store.LoadActiveSessionMemoryNodes(ctx, session)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, fmt.Errorf("load active session memory nodes: %w", err)
	}
	recentDirectWrites, err := u.store.LoadRecentDirectMemoryWrites(ctx, session, session.LastExtractObservedAt, analysisCutoff)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, fmt.Errorf("load recent direct memory writes: %w", err)
	}

	// Convert storage rows into the narrower analyzer input model so the prompt only sees the fields relevant to single-turn context, de-duplication, and supersede decisions.
	// 把存储行转换成更窄的分析器输入模型，让提示词只看到单轮上下文、去重和覆盖判断真正需要的字段。
	input := logicdomain.TurnAnalysisInput{
		ReferenceTurns:         make([]logicdomain.TurnAnalysisReferenceTurn, 0, len(selectedHistory)),
		TargetTurn:             logicdomain.TurnAnalysisTargetTurn{TurnID: turn.ID, RawTurn: targetBody},
		ActiveMemoryNodes:      make([]logicdomain.TurnAnalysisActiveMemoryNode, 0, len(activeMemoryNodes)),
		RecentGRPCMemoryWrites: make([]logicdomain.TurnAnalysisDirectWrite, 0, len(recentDirectWrites)),
	}
	for _, historyTurn := range selectedHistory {
		input.ReferenceTurns = append(input.ReferenceTurns, logicdomain.TurnAnalysisReferenceTurn{
			TurnID:  historyTurn.ID,
			Details: strings.TrimSpace(historyTurn.Details),
		})
	}
	for _, node := range activeMemoryNodes {
		input.ActiveMemoryNodes = append(input.ActiveMemoryNodes, logicdomain.TurnAnalysisActiveMemoryNode{
			MemoryID:      node.ID,
			SourceTurnID:  node.TurnID,
			Category:      node.Category,
			Abstract:      strings.TrimSpace(node.Abstract),
			Details:       strings.TrimSpace(node.Details),
			SourceKind:    logicdomain.MemorySourceKindLabel(node.SourceKind),
			ScopeLevel:    logicdomain.MemoryScopeLevelLabel(node.ScopeLevel),
			SupportCount:  node.SupportCount,
			RebuttalCount: node.RebuttalCount,
		})
	}
	for _, item := range recentDirectWrites {
		input.RecentGRPCMemoryWrites = append(input.RecentGRPCMemoryWrites, logicdomain.TurnAnalysisDirectWrite{
			MemoryID:         item.MemoryID,
			ScopeLevel:       item.ScopeLevel,
			Abstract:         strings.TrimSpace(item.Abstract),
			Details:          strings.TrimSpace(item.Details),
			CreatedTimestamp: item.CreatedTimestamp,
		})
	}
	return input, analysisCutoff, nil
}

// validateTurnAnalysis rejects supersede references that do not belong to the active-memory anchors exposed to the single-turn analyzer.
// validateTurnAnalysis 用于拒绝任何没有出现在单轮分析器输入活跃记忆锚点中的 supersede 引用。
func validateTurnAnalysis(input logicdomain.TurnAnalysisInput, analysis logicdomain.TurnAnalysis) error {
	if input.TargetTurn.TurnID == 0 {
		return logicdomain.ValidationError{Field: "target_turn.turn_id", Message: "is required"}
	}
	if analysis.TurnID != input.TargetTurn.TurnID {
		return logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: fmt.Sprintf("unexpected turn_id %d", analysis.TurnID)}
	}
	activeMemoryIDs := make(map[uint64]struct{}, len(input.ActiveMemoryNodes))
	for _, node := range input.ActiveMemoryNodes {
		activeMemoryIDs[node.MemoryID] = struct{}{}
	}
	for _, memoryID := range analysis.SupersededMemoryIDs {
		if _, ok := activeMemoryIDs[memoryID]; !ok {
			return logicdomain.InvalidLLMOutputError{Scene: "analyze_turn", Message: fmt.Sprintf("superseded_memory_ids contains unknown memory_id %d", memoryID)}
		}
	}
	return nil
}

// persistMemoryNodeVectors embeds the extracted memory-node abstracts, writes them to LanceDB, and attaches the resulting vector ids back onto the analysis payload.
// persistMemoryNodeVectors 用于对提炼出的记忆节点摘要生成向量、写入 LanceDB，并把得到的 vector_id 回填到分析结果中。
func (u *PostActionUseCase) persistMemoryNodeVectors(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis *logicdomain.TurnAnalysis) ([]string, error) {
	if analysis == nil || len(analysis.MemoryNodes) == 0 {
		return nil, nil
	}
	if u.embedding == nil {
		return nil, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return nil, fmt.Errorf("vector store is nil")
	}

	// Embed the memory-node abstracts in batches so provider batch limits do not break post-action extraction on larger outputs.
	// 按批次对记忆节点摘要做向量化，避免较大的提炼结果触发 provider 的 batch 限制。
	texts := make([]string, 0, len(analysis.MemoryNodes))
	for idx, node := range analysis.MemoryNodes {
		text := strings.TrimSpace(node.Abstract)
		if text == "" {
			return nil, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].abstract", Message: "is required for vector persistence"}
		}
		texts = append(texts, text)
	}
	vectors, err := embedPostActionTexts(ctx, u.embedding, texts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(analysis.MemoryNodes) {
		return nil, fmt.Errorf("embedding result count mismatch: got %d want %d", len(vectors), len(analysis.MemoryNodes))
	}

	// Upsert LanceDB rows first so SQLite only flips extracted_status after the corresponding vectors already exist.
	// 先 upsert LanceDB 行，确保 SQLite 只有在对应向量已存在时才会把 extracted_status 置为完成。
	insertedIDs := make([]string, 0, len(analysis.MemoryNodes))
	for idx := range analysis.MemoryNodes {
		vectorID, err := generatePostActionUUID()
		if err != nil {
			if len(insertedIDs) > 0 {
				_, _ = u.vector.DeleteByIDs(ctx, insertedIDs)
			}
			return nil, err
		}
		analysis.MemoryNodes[idx].VectorID = vectorID
		analysis.MemoryNodes[idx].Vector = append([]float32(nil), vectors[idx]...)
		record := logicdomain.MemoryRecord{
			ID:     vectorID,
			Text:   strings.TrimSpace(analysis.MemoryNodes[idx].Abstract),
			Vector: vectors[idx],
			Filter: buildPostActionMemoryFilter(session),
			Metadata: map[string]string{
				"turn_id":  strconv.FormatUint(turn.ID, 10),
				"category": strconv.Itoa(analysis.MemoryNodes[idx].Category),
				"details":  strings.TrimSpace(analysis.MemoryNodes[idx].Details),
			},
			CreatedAt: choosePostActionCreatedAt(turn),
		}
		if err := u.vector.Upsert(ctx, record); err != nil {
			if len(insertedIDs) > 0 {
				_, _ = u.vector.DeleteByIDs(ctx, insertedIDs)
			}
			return nil, err
		}
		insertedIDs = append(insertedIDs, vectorID)
	}
	return insertedIDs, nil
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

// embedPostActionTexts runs embedding requests in provider-safe batches and returns vectors in the same order as the input texts.
// embedPostActionTexts 用于按 provider 安全批次执行 embedding，并按输入文本顺序返回向量。
func embedPostActionTexts(ctx context.Context, client appports.EmbeddingClient, texts []string) ([][]float32, error) {
	if client == nil {
		return nil, fmt.Errorf("embedding client is nil")
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += 10 {
		end := start + 10
		if end > len(texts) {
			end = len(texts)
		}
		resp, err := client.Embed(ctx, appports.EmbeddingRequest{Texts: texts[start:end]})
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, resp.Vectors...)
	}
	return vectors, nil
}

// buildPostActionMemoryFilter derives the flattened hierarchy scope stored on vector rows for post-action memory nodes.
// buildPostActionMemoryFilter 用于推导 post-action 记忆节点写入向量行时携带的扁平层级范围。
func buildPostActionMemoryFilter(session logicdomain.SessionRef) logicdomain.SearchFilter {
	return logicdomain.SearchFilter{
		UserID:    session.UserID,
		TeamID:    session.TeamID,
		SpaceID:   session.SpaceID,
		ProjectID: session.ProjectID,
		SessionID: session.SessionID,
	}
}

// choosePostActionCreatedAt prefers the persisted turn timestamp so vector rows and SQLite memory nodes share the same approximate origin time.
// choosePostActionCreatedAt 用于优先复用已落库 turn 的时间戳，让向量行和 SQLite 记忆节点共享接近的产生时间。
func choosePostActionCreatedAt(turn logicdomain.PersistedTurnRecord) time.Time {
	if !turn.CreatedAt.IsZero() {
		return turn.CreatedAt
	}
	return time.Now().UTC()
}

// generatePostActionUUID creates one random UUID string for the LanceDB row id and SQLite memory-node vector_id link.
// generatePostActionUUID 用于生成随机 UUID 字符串，同时作为 LanceDB 行 id 和 SQLite 记忆节点的 vector_id 关联键。
func generatePostActionUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate post-action uuid: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
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
