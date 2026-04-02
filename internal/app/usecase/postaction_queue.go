// postaction_queue.go implements the queued post-action downstream pipeline that batches turn extraction after the cleaned turns are durably stored.
// postaction_queue.go 用于实现排队式 post-action 后续流水线：先稳定落库，再批量执行 turn 提炼。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postActionQueueState tracks one session's deduplicated queue state so frequent post-action writes collapse into one worker item.
// postActionQueueState 用于跟踪单个 session 的去重队列状态，让高频 post-action 写入可以折叠成一个工作项。
type postActionQueueState struct {
	Session  logicdomain.SessionRef
	Queued   bool
	InFlight bool
	Dirty    bool
	Force    bool
}

// postActionProfileNodeRef points back to one profile node inside one turn-local analysis result.
// postActionProfileNodeRef 用于回指某个 turn 局部分析结果中的一条画像节点。
type postActionProfileNodeRef struct {
	TurnIndex int
	NodeIndex int
}

// startQueueWorker boots the background worker and the periodic idle scan used by the queued session-analysis pipeline.
// startQueueWorker 用于启动后台工作器和周期性空闲扫描，让排队式 session 分析流水线开始工作。
func (u *PostActionUseCase) startQueueWorker() {
	if u == nil || u.queueCh != nil {
		return
	}
	u.queueCtx, u.queueCancel = context.WithCancel(context.Background())
	u.queueCh = make(chan uint64, 256)
	u.queueState = map[uint64]*postActionQueueState{}
	u.queueWG.Add(1)
	go u.queueWorkerLoop()
}

// Shutdown drains the post-action queue worker before relational and vector dependencies are closed.
// Shutdown 用于在关系存储和向量存储关闭前，先把 post-action 队列工作器停掉。
func (u *PostActionUseCase) Shutdown(ctx context.Context) error {
	if u == nil || u.queueCancel == nil {
		return nil
	}
	u.queueCancel()
	done := make(chan struct{})
	go func() {
		u.queueWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// enqueueSessionAnalysis deduplicates session work so one hot session only occupies one queued slot while new turns keep refreshing its scope state.
// enqueueSessionAnalysis 用于对 session 工作项去重，确保高频 session 最多只占用一个排队槽位，同时持续刷新其最新范围状态。
func (u *PostActionUseCase) enqueueSessionAnalysis(session logicdomain.SessionRef, force bool) {
	if u == nil || u.queueCh == nil || session.SessionID == 0 {
		return
	}

	// Merge repeated enqueue attempts into one state row so the worker can always pick up the latest scope snapshot.
	// 把重复入队合并到同一条状态记录里，让工作器始终拿到最新的范围快照。
	var queueID uint64
	u.queueMu.Lock()
	state, ok := u.queueState[session.SessionID]
	if !ok {
		state = &postActionQueueState{}
		u.queueState[session.SessionID] = state
	}
	state.Session = session
	if force {
		state.Force = true
	}
	switch {
	case state.InFlight:
		state.Dirty = true
	case state.Queued:
		// Keep the existing queued slot and just refresh the latest session snapshot.
		// 保留已有排队槽位，仅刷新最新 session 快照。
	default:
		state.Queued = true
		queueID = session.SessionID
	}
	u.queueMu.Unlock()

	if queueID != 0 {
		u.pushQueueID(queueID)
	}
}

// pushQueueID sends one deduplicated session id into the worker channel and falls back to a goroutine when the buffer is temporarily full.
// pushQueueID 用于把去重后的 session id 推入工作器通道；若缓冲区暂时已满，则回退到 goroutine 发送。
func (u *PostActionUseCase) pushQueueID(sessionID uint64) {
	if u == nil || u.queueCh == nil || sessionID == 0 {
		return
	}
	select {
	case u.queueCh <- sessionID:
	default:
		go func() {
			select {
			case u.queueCh <- sessionID:
			case <-u.queueCtx.Done():
			}
		}()
	}
}

// queueWorkerLoop prioritizes explicit queue items while also scanning every 30 seconds for stale pending sessions that crossed the idle threshold.
// queueWorkerLoop 用于优先处理显式入队内容，同时每 30 秒扫描一次超过空闲阈值的待处理 session。
func (u *PostActionUseCase) queueWorkerLoop() {
	defer u.queueWG.Done()
	ticker := time.NewTicker(u.analysisCfg.QueueScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-u.queueCtx.Done():
			return
		case sessionID := <-u.queueCh:
			u.handleQueuedSession(sessionID)
		case <-ticker.C:
			if u.queueMaintenanceBackoffActive(time.Now()) {
				continue
			}
			u.convergeExpiredProfiles()
			u.scanIdlePendingSessions()
		}
	}
}

// handleQueuedSession snapshots the latest queue state for one session, runs one batch attempt, and reschedules if new turns arrived mid-flight.
// handleQueuedSession 用于获取某个 session 的最新队列状态、执行一次批处理尝试，并在处理中间又有新 turn 到来时自动重排。
func (u *PostActionUseCase) handleQueuedSession(sessionID uint64) {
	if u == nil || sessionID == 0 {
		return
	}

	// Mark the current queue slot as in-flight before running the heavier analysis path.
	// 在执行较重的分析路径前，先把当前队列槽位标记成处理中。
	var (
		session logicdomain.SessionRef
		force   bool
	)
	u.queueMu.Lock()
	state := u.queueState[sessionID]
	if state == nil {
		u.queueMu.Unlock()
		return
	}
	state.Queued = false
	state.InFlight = true
	session = state.Session
	force = state.Force
	state.Force = false
	u.queueMu.Unlock()

	u.processQueuedSession(session, force, "queue")

	// If new turns arrived while the worker was busy, immediately requeue the same session so it does not wait for the next external trigger.
	// 如果工作器繁忙期间又有新 turn 到达，则立刻把同一 session 重新排队，避免它只能等待下一次外部触发。
	var requeue bool
	u.queueMu.Lock()
	state = u.queueState[sessionID]
	if state != nil {
		state.InFlight = false
		if state.Dirty {
			state.Dirty = false
			if !state.Queued {
				state.Queued = true
				requeue = true
			}
		} else if !state.Force {
			delete(u.queueState, sessionID)
		}
	}
	u.queueMu.Unlock()

	if requeue {
		u.pushQueueID(sessionID)
	}
}

// scanIdlePendingSessions periodically promotes stale sessions into forced legacy batch attempts when that compatibility path is still enabled.
// scanIdlePendingSessions 用于在兼容批处理路径仍启用时，周期性地把空闲过久的 session 提升为强制分析。
func (u *PostActionUseCase) scanIdlePendingSessions() {
	if u == nil || u.store == nil || u.batchAnalyzer == nil || u.analysisCfg.IdleTimeout <= 0 {
		return
	}
	sessions, err := u.store.ListIdlePendingSessions(u.queueCtx, u.analysisCfg.IdleTimeout, 128)
	if err != nil {
		u.markQueueMaintenanceBackoff("idle session scan", err)
		if u.logger != nil {
			u.logger.Error("post-action idle session scan failed", "err", err)
		}
		return
	}
	for _, session := range sessions {
		u.enqueueSessionAnalysis(session, true)
	}
}

// queueMaintenanceBackoffActive reports whether the periodic maintenance ticker should temporarily stand down after a fatal storage-side deadlock symptom.
// queueMaintenanceBackoffActive 用于判断周期性维护 ticker 是否应在存储侧出现致命死锁症状后暂时退避。
func (u *PostActionUseCase) queueMaintenanceBackoffActive(now time.Time) bool {
	if u == nil {
		return false
	}
	u.maintenanceMu.Lock()
	defer u.maintenanceMu.Unlock()
	return now.Before(u.maintenanceBackoffUntil)
}

// markQueueMaintenanceBackoff pauses low-priority maintenance scans for one short window after the shared DuckDB connection starts reporting poisoned-state errors.
// markQueueMaintenanceBackoff 用于在共享 DuckDB 连接开始报“污染态”错误后，暂时暂停低优先级维护扫描，避免持续撞击坏连接。
func (u *PostActionUseCase) markQueueMaintenanceBackoff(operation string, err error) {
	if u == nil || err == nil || !shouldPauseQueueMaintenance(err) {
		return
	}

	backoff := u.analysisCfg.QueueScanInterval * 2
	if backoff < time.Minute {
		backoff = time.Minute
	}
	now := time.Now()
	until := now.Add(backoff)

	u.maintenanceMu.Lock()
	extended := until.After(u.maintenanceBackoffUntil)
	if extended {
		u.maintenanceBackoffUntil = until
	}
	u.maintenanceMu.Unlock()

	if extended && u.logger != nil {
		u.logger.Warn(
			"post-action queue maintenance paused",
			"operation", operation,
			"backoff_until", until.Format(time.RFC3339Nano),
			"err", err,
		)
	}
}

// shouldPauseQueueMaintenance classifies the gateway failures that indicate the shared DuckDB connection is already poisoned and periodic scans should stand down briefly.
// shouldPauseQueueMaintenance 用于识别这类网关失败：它表明共享 DuckDB 连接已经进入污染态，周期性扫描应暂时退避。
func shouldPauseQueueMaintenance(err error) bool {
	if err == nil {
		return false
	}
	if logicdomain.IsOutcomeUncertain(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	markers := []string{
		"resource deadlock would occur",
		"failed to commit",
		"prepare failed",
		"waiting for the shared connection",
		"stream terminated by rst_stream",
		"transactioncontext error",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// processQueuedSession loads the legacy batch window, checks thresholds, runs the compatibility analyzer, merges profiles once, persists vectors, and writes results back into DuckDB.
// processQueuedSession 用于加载旧的批处理窗口、检查阈值、执行兼容分析器、统一合并画像、持久化向量，并把结果回写到 DuckDB。
func (u *PostActionUseCase) processQueuedSession(session logicdomain.SessionRef, force bool, source string) {
	if u == nil || u.store == nil || u.batchAnalyzer == nil || session.SessionID == 0 {
		return
	}
	workerCtx := u.queueCtx
	if workerCtx == nil {
		workerCtx = context.Background()
	}

	// Load the pending turns first because they define both the threshold checks and the current batch window.
	// 先加载待处理 turn，因为它们同时决定阈值判断和当前批处理窗口。
	pendingTurns, err := u.store.LoadPendingSessionTurns(workerCtx, session)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action pending turns load failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}
	if len(pendingTurns) == 0 {
		return
	}
	pendingBudget := sumSessionTurnBudgets(pendingTurns)
	idleGap := time.Duration(0)
	if !session.UpdatedAt.IsZero() {
		idleGap = time.Since(session.UpdatedAt)
	}
	if !force && pendingBudget < u.analysisCfg.TokenThreshold && len(pendingTurns) < u.analysisCfg.TurnThreshold {
		if u.logger != nil {
			u.logger.Info(
				"post-action queue skipped by thresholds",
				"session_key", session.SessionKey,
				"session_id", session.SessionID,
				"source", source,
				"pending_turn_count", len(pendingTurns),
				"pending_token_budget", pendingBudget,
				"idle_gap_ms", idleGap.Milliseconds(),
			)
		}
		return
	}

	// Select the pending raw turns first, then backfill reference history with already refined summaries under the total input-token cap.
	// 先选择待处理原始 turn，再在总输入 token 上限内回填已经提炼过的历史精要作为参考。
	selectedPending, selectedPendingBudget := selectPendingTurnsByBudget(pendingTurns, u.analysisCfg.MaxInputTokens)
	if len(selectedPending) == 0 {
		return
	}
	remainingBudget := u.analysisCfg.MaxInputTokens - selectedPendingBudget
	if remainingBudget < 0 {
		remainingBudget = 0
	}
	historyTurns, err := u.store.LoadRecentSessionHistory(workerCtx, session, u.analysisCfg.HistoryTurns)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action history turns load failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}
	selectedHistory := trimHistoryTurnsByBudget(historyTurns, remainingBudget)
	activeMemoryNodes, err := u.store.LoadActiveSessionMemoryNodes(workerCtx, session)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action active memory load failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}

	requestBody, err := buildSessionBatchAnalysisRequest(selectedHistory, selectedPending, activeMemoryNodes)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action batch request build failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}
	analysis, err := u.batchAnalyzer.Analyze(workerCtx, requestBody)
	if err != nil {
		if u.logger != nil {
			u.logger.Error(
				"post-action session batch analysis failed",
				"session_key", session.SessionKey,
				"session_id", session.SessionID,
				"source", source,
				"pending_turn_count", len(selectedPending),
				"history_turn_count", len(selectedHistory),
				"err", err,
			)
		}
		return
	}
	if err := validateSessionBatchAnalysis(selectedPending, activeMemoryNodes, analysis); err != nil {
		if u.logger != nil {
			u.logger.Error("post-action session batch analysis rejected", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}

	// Review the fresh atomic profile candidates against current active nodes, then let the backend rebuild the final user/project profile text from data.
	// 先把新的原子化画像候选与当前活跃节点做评审，再由后端基于数据重建最终 user/project 画像文本。
	if err := u.reviewSessionBatchProfiles(workerCtx, session, selectedPending, &analysis); err != nil && u.logger != nil {
		u.logger.Error("post-action session profile review failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		for turnIdx := range analysis.Turns {
			analysis.Turns[turnIdx].ProfileNodes = nil
		}
		analysis.RetiredProfileNodeIDs = nil
		analysis.UserProfileMerged = false
		analysis.MergedUserProfile = ""
		analysis.ProjectProfileMerged = false
		analysis.MergedProjectProfile = ""
	}

	vectorIDs, err := u.persistSessionBatchVectors(workerCtx, session, selectedPending, &analysis)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action session vector persistence failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}
	applyResult, err := u.store.ApplySessionBatchAnalysis(workerCtx, session, selectedPending, analysis)
	if err != nil {
		if len(vectorIDs) > 0 && u.vector != nil {
			if _, rollbackErr := u.vector.DeleteByIDs(workerCtx, vectorIDs); rollbackErr != nil && u.logger != nil {
				u.logger.Error("post-action session vector rollback failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", rollbackErr)
			}
		}
		if u.logger != nil {
			u.logger.Error("post-action session batch persistence failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
		return
	}
	if len(applyResult.ObsoleteVectorIDs) > 0 && u.vector != nil {
		if _, err := u.vector.DeleteByIDs(workerCtx, applyResult.ObsoleteVectorIDs); err != nil && u.logger != nil {
			u.logger.Error("post-action obsolete vector delete failed", "session_key", session.SessionKey, "session_id", session.SessionID, "err", err)
		}
	}

	analysisJSON, _ := json.Marshal(analysis)
	if u.logger != nil {
		u.logger.Info(
			"post-action session batch analysis result",
			"session_key", session.SessionKey,
			"session_id", session.SessionID,
			"source", source,
			"force", force,
			"history_turn_count", len(selectedHistory),
			"pending_turn_count", len(selectedPending),
			"pending_token_budget", selectedPendingBudget,
			"obsolete_turn_count", len(analysis.ObsoleteMemoryTurnIDs),
			"vector_count", len(vectorIDs),
			"analysis", string(analysisJSON),
		)
	}
}

// sumSessionTurnBudgets sums dehydrated budgets across one turn slice so threshold checks can operate on the same persisted token heuristic.
// sumSessionTurnBudgets 用于累计一组 turn 的脱水预算，让阈值判断继续沿用同一套持久化 token 口径。
func sumSessionTurnBudgets(turns []logicdomain.SessionTurnRecord) int {
	total := 0
	for _, turn := range turns {
		total += turn.DehydratedBudget
	}
	return total
}

// selectPendingTurnsByBudget keeps the oldest pending turns first and stops once adding another turn would exceed the configured input budget.
// selectPendingTurnsByBudget 用于优先保留最早的待处理 turn，并在再加入一条就会超出输入预算时停止。
func selectPendingTurnsByBudget(turns []logicdomain.SessionTurnRecord, maxInputTokens int) ([]logicdomain.SessionTurnRecord, int) {
	if len(turns) == 0 {
		return nil, 0
	}
	if maxInputTokens <= 0 {
		return append([]logicdomain.SessionTurnRecord(nil), turns...), sumSessionTurnBudgets(turns)
	}
	selected := make([]logicdomain.SessionTurnRecord, 0, len(turns))
	total := 0
	for _, turn := range turns {
		budget := turn.DehydratedBudget
		if budget <= 0 {
			budget = estimatePostActionTextBudget(turn.DehydratedContent)
		}
		if len(selected) > 0 && total+budget > maxInputTokens {
			break
		}
		selected = append(selected, turn)
		total += budget
	}
	if len(selected) == 0 {
		firstBudget := turns[0].DehydratedBudget
		if firstBudget <= 0 {
			firstBudget = estimatePostActionTextBudget(turns[0].DehydratedContent)
		}
		return []logicdomain.SessionTurnRecord{turns[0]}, firstBudget
	}
	return selected, total
}

// trimHistoryTurnsByBudget keeps the most recent refined summaries within both the count cap and the remaining token budget.
// trimHistoryTurnsByBudget 用于在数量上限和剩余 token 预算内，保留最近的已提炼历史摘要。
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

// buildSessionBatchAnalysisRequest serializes history summaries, pending raw turns, and active memory anchors into one stable JSON body for the batch scene.
// buildSessionBatchAnalysisRequest 用于把历史摘要、待处理原始 turn 和活跃记忆锚点序列化成稳定 JSON 请求体，供批处理场景使用。
func buildSessionBatchAnalysisRequest(historyTurns, pendingTurns []logicdomain.SessionTurnRecord, activeMemoryNodes []logicdomain.SessionMemoryNodeRecord) (string, error) {
	type historyTurnInput struct {
		HistoryIndex  int    `json:"history_index"`
		TurnID        uint64 `json:"turn_id"`
		Details       string `json:"details"`
		DetailsBudget int    `json:"details_budget"`
	}
	type pendingTurnInput struct {
		PendingIndex     int             `json:"pending_index"`
		TurnID           uint64          `json:"turn_id"`
		DehydratedBudget int             `json:"dehydrated_budget"`
		RawTurn          json.RawMessage `json:"raw_turn"`
	}
	type activeMemoryNodeInput struct {
		MemoryNodeID uint64 `json:"memory_node_id"`
		TurnID       uint64 `json:"turn_id"`
		VectorID     string `json:"vector_id"`
		Category     int    `json:"category"`
		Abstract     string `json:"abstract"`
		Details      string `json:"details"`
	}
	type requestBody struct {
		HistoryTurns      []historyTurnInput      `json:"history_turns"`
		PendingTurns      []pendingTurnInput      `json:"pending_turns"`
		ActiveMemoryNodes []activeMemoryNodeInput `json:"active_memory_nodes"`
	}

	body := requestBody{
		HistoryTurns:      make([]historyTurnInput, 0, len(historyTurns)),
		PendingTurns:      make([]pendingTurnInput, 0, len(pendingTurns)),
		ActiveMemoryNodes: make([]activeMemoryNodeInput, 0, len(activeMemoryNodes)),
	}
	for idx, turn := range historyTurns {
		body.HistoryTurns = append(body.HistoryTurns, historyTurnInput{
			HistoryIndex:  idx + 1,
			TurnID:        turn.ID,
			Details:       strings.TrimSpace(turn.Details),
			DetailsBudget: turn.DetailsBudget,
		})
	}
	for idx, turn := range pendingTurns {
		rawTurn := json.RawMessage(strings.TrimSpace(turn.DehydratedContent))
		if !json.Valid(rawTurn) {
			return "", fmt.Errorf("pending turn %d dehydrated_content is not valid json", turn.ID)
		}
		body.PendingTurns = append(body.PendingTurns, pendingTurnInput{
			PendingIndex:     idx + 1,
			TurnID:           turn.ID,
			DehydratedBudget: turn.DehydratedBudget,
			RawTurn:          rawTurn,
		})
	}
	for _, node := range activeMemoryNodes {
		body.ActiveMemoryNodes = append(body.ActiveMemoryNodes, activeMemoryNodeInput{
			MemoryNodeID: node.ID,
			TurnID:       node.TurnID,
			VectorID:     strings.TrimSpace(node.VectorID),
			Category:     node.Category,
			Abstract:     strings.TrimSpace(node.Abstract),
			Details:      strings.TrimSpace(node.Details),
		})
	}
	rendered, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal session batch analysis request: %w", err)
	}
	return string(rendered), nil
}

// validateSessionBatchAnalysis enforces one-to-one turn coverage and rejects obsolete turn ids that were never exposed as active memory anchors.
// validateSessionBatchAnalysis 用于强制一对一 turn 覆盖，并拒绝任何未在活跃记忆锚点中暴露过的 obsolete turn id。
func validateSessionBatchAnalysis(pendingTurns []logicdomain.SessionTurnRecord, activeMemoryNodes []logicdomain.SessionMemoryNodeRecord, analysis logicdomain.SessionBatchAnalysis) error {
	expected := map[uint64]struct{}{}
	for _, turn := range pendingTurns {
		expected[turn.ID] = struct{}{}
	}
	if len(expected) != len(analysis.Turns) {
		return logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("turn_results count mismatch: got %d want %d", len(analysis.Turns), len(expected))}
	}
	for _, turn := range analysis.Turns {
		if _, ok := expected[turn.TurnID]; !ok {
			return logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("unexpected turn_id %d in turn_results", turn.TurnID)}
		}
	}
	activeTurnIDs := map[uint64]struct{}{}
	for _, node := range activeMemoryNodes {
		activeTurnIDs[node.TurnID] = struct{}{}
	}
	for _, turnID := range analysis.ObsoleteMemoryTurnIDs {
		if _, ok := activeTurnIDs[turnID]; !ok {
			return logicdomain.InvalidLLMOutputError{Scene: "analyze_session_batch", Message: fmt.Sprintf("obsolete_memory_turn_ids contains unknown turn_id %d", turnID)}
		}
	}
	return nil
}

// persistSessionBatchVectors embeds every newly extracted memory abstract in the batch, writes them to LanceDB, and backfills vector ids onto the analysis payload.
// persistSessionBatchVectors 用于对批次中新提取的全部记忆摘要生成向量、写入 LanceDB，并把 vector_id 回填到分析结果。
func (u *PostActionUseCase) persistSessionBatchVectors(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.SessionTurnRecord, analysis *logicdomain.SessionBatchAnalysis) ([]string, error) {
	if analysis == nil {
		return nil, nil
	}
	type memoryNodeRef struct {
		TurnIndex int
		NodeIndex int
	}
	turnByID := map[uint64]logicdomain.SessionTurnRecord{}
	for _, turn := range turns {
		turnByID[turn.ID] = turn
	}
	texts := make([]string, 0)
	refs := make([]memoryNodeRef, 0)
	for turnIdx := range analysis.Turns {
		for nodeIdx, node := range analysis.Turns[turnIdx].MemoryNodes {
			text := strings.TrimSpace(node.Abstract)
			if text == "" {
				return nil, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(nodeIdx) + "].abstract", Message: "is required for vector persistence"}
			}
			texts = append(texts, text)
			refs = append(refs, memoryNodeRef{TurnIndex: turnIdx, NodeIndex: nodeIdx})
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if u.embedding == nil {
		return nil, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return nil, fmt.Errorf("vector store is nil")
	}
	vectors, err := embedPostActionTexts(ctx, u.embedding, texts)
	if err != nil {
		return nil, err
	}
	if len(vectors) != len(refs) {
		return nil, fmt.Errorf("embedding result count mismatch: got %d want %d", len(vectors), len(refs))
	}

	insertedIDs := make([]string, 0, len(refs))
	for idx, ref := range refs {
		vectorID, err := generatePostActionUUID()
		if err != nil {
			if len(insertedIDs) > 0 {
				_, _ = u.vector.DeleteByIDs(ctx, insertedIDs)
			}
			return nil, err
		}
		turnResult := &analysis.Turns[ref.TurnIndex]
		node := &turnResult.MemoryNodes[ref.NodeIndex]
		node.VectorID = vectorID
		turn := turnByID[turnResult.TurnID]
		record := logicdomain.MemoryRecord{
			ID:     vectorID,
			Text:   strings.TrimSpace(node.Abstract),
			Vector: vectors[idx],
			Filter: buildPostActionMemoryFilter(session),
			Metadata: map[string]string{
				"turn_id":  strconv.FormatUint(turnResult.TurnID, 10),
				"category": strconv.Itoa(node.Category),
				"details":  strings.TrimSpace(node.Details),
			},
			CreatedAt: chooseSessionTurnCreatedAt(turn),
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

// chooseSessionTurnCreatedAt reuses the persisted turn timestamp so new vector rows stay anchored to the same approximate origin time as DuckDB rows.
// chooseSessionTurnCreatedAt 用于复用已持久化 turn 的时间戳，让新向量行与 DuckDB 行共享接近的产生时间。
func chooseSessionTurnCreatedAt(turn logicdomain.SessionTurnRecord) time.Time {
	if !turn.CreatedAt.IsZero() {
		return turn.CreatedAt
	}
	return time.Now().UTC()
}
