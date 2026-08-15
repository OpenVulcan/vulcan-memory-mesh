// postaction_queue.go implements the queued post-action downstream pipeline that drains persisted pending turns one by one after durable writes complete.
// postaction_queue.go 用于实现排队式 post-action 后续流水线：在稳定落库之后，按顺序逐条消化待提炼 turn。
package usecase

import (
	"context"
	"errors"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// postActionDeferredQueueCapacity bounds the in-memory overflow backlog independently from the worker channel.
	// postActionDeferredQueueCapacity 用于独立限制工作通道之外的内存溢出积压数量。
	postActionDeferredQueueCapacity = 256
)

// postActionQueueState tracks one session's deduplicated queue state so frequent post-action writes collapse into one worker item.
// postActionQueueState 用于跟踪单个 session 的去重队列状态，让高频 post-action 写入可以折叠成一个工作项。
type postActionQueueState struct {
	Session  logicdomain.SessionRef
	Queued   bool
	InFlight bool
	Dirty    bool
}

// startQueueWorker boots the background worker pool and a single maintenance ticker used by the queued single-turn extraction pipeline.
// startQueueWorker 用于启动后台工作器池和单一维护 ticker，让排队式单轮提炼流水线开始工作。
func (u *PostActionUseCase) startQueueWorker() {
	if u == nil || u.queueCh != nil {
		return
	}
	u.queueCtx, u.queueCancel = context.WithCancel(context.Background())
	u.queueCh = make(chan uint64, 256)
	u.queueState = map[uint64]*postActionQueueState{}
	u.deferredQueueSet = map[uint64]struct{}{}
	u.sessionLocked = map[uint64]bool{}

	n := u.queueWorkers
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		u.queueWG.Add(1)
		go u.queueWorkerLoop()
	}
	// Start exactly one maintenance goroutine so convergeExpiredProfiles, scanIdlePendingSessions, and flushDeferredQueueIDs run once per interval instead of N times.
	// 只启动一个维护 goroutine，确保画像过期收敛、idle session 补扫和 deferred flush 每个周期只执行一次，而不是 N 倍重复。
	u.queueWG.Add(1)
	go u.maintenanceLoop()
	// Run one bounded recovery scan immediately so persisted pending turns do not wait for the first ticker interval after startup.
	// 启动后立即执行一次有界恢复扫描，避免持久化的 pending turn 必须等待第一个 ticker 周期。
	u.scanIdlePendingSessions()
}

// Shutdown drains the post-action queue worker before relational and vector dependencies are closed.
// Shutdown 用于在关系存储和向量存储关闭前，先把 post-action 队列工作器停掉。
func (u *PostActionUseCase) Shutdown(ctx context.Context) error {
	if u == nil || u.queueCancel == nil {
		return nil
	}

	// Normalize nil shutdown contexts so direct tests and partial integrations can still
	// wait for the queue worker to stop without panicking on ctx.Done().
	// 这里先把 nil 的 shutdown context 归一成 Background，
	// 这样直接测试或部分集成调用在等待队列工作器退出时就不会因为 ctx.Done() 而 panic。
	if ctx == nil {
		ctx = context.Background()
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
func (u *PostActionUseCase) enqueueSessionAnalysis(session logicdomain.SessionRef) {
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

// pushQueueID sends one deduplicated session id into the worker channel and records overflow in a bounded deferred queue instead of spawning blocked goroutines.
// pushQueueID 用于把去重后的 session id 推入工作器通道；若缓冲区暂时已满，则把溢出项记录到有界延迟队列，而不是启动阻塞 goroutine。
func (u *PostActionUseCase) pushQueueID(sessionID uint64) {
	if u == nil || u.queueCh == nil || sessionID == 0 {
		return
	}
	select {
	case u.queueCh <- sessionID:
	default:
		u.queueDeferredSessionID(sessionID)
	}
}

// queueDeferredSessionID keeps one overflowed queue id in a deduplicated in-memory backlog so sustained bursts do not create one blocked goroutine per missed send.
// queueDeferredSessionID 用于把溢出的 queue id 保存在去重的内存 backlog 中，避免持续突发流量为每次失败发送都创建一个阻塞 goroutine。
func (u *PostActionUseCase) queueDeferredSessionID(sessionID uint64) {
	if u == nil || sessionID == 0 {
		return
	}
	u.queueMu.Lock()
	if u.deferredQueueSet == nil {
		u.deferredQueueSet = map[uint64]struct{}{}
	}
	if _, exists := u.deferredQueueSet[sessionID]; exists {
		u.queueMu.Unlock()
		return
	}
	if len(u.deferredQueueIDs) >= postActionDeferredQueueCapacity {
		// Release the volatile queued flag when both bounded buffers are full; the durable idle scan remains the authoritative recovery path.
		// 当两个有界缓冲区都已满时释放易失 queued 标记；持久化 idle 扫描仍是权威恢复路径。
		if state := u.queueState[sessionID]; state != nil {
			state.Queued = false
			if state.InFlight {
				state.Dirty = true
			} else {
				delete(u.queueState, sessionID)
			}
		}
		u.queueMu.Unlock()
		if u.logger != nil {
			u.logger.Warn("post-action deferred queue capacity reached", "session_id", sessionID, "capacity", postActionDeferredQueueCapacity)
		}
		return
	}
	u.deferredQueueSet[sessionID] = struct{}{}
	u.deferredQueueIDs = append(u.deferredQueueIDs, sessionID)
	u.queueMu.Unlock()
}

// flushDeferredQueueIDs opportunistically moves overflowed session ids back into the worker channel in FIFO order without ever blocking the queue loop.
// flushDeferredQueueIDs 用于机会性地按 FIFO 顺序把溢出的 session id 重新推回工作通道，同时绝不阻塞队列循环。
func (u *PostActionUseCase) flushDeferredQueueIDs() {
	if u == nil || u.queueCh == nil {
		return
	}
	u.queueMu.Lock()
	defer u.queueMu.Unlock()
	if len(u.deferredQueueIDs) == 0 {
		return
	}
	remaining := u.deferredQueueIDs[:0]
	blocked := false
	for _, sessionID := range u.deferredQueueIDs {
		if sessionID == 0 {
			continue
		}
		if blocked {
			remaining = append(remaining, sessionID)
			continue
		}
		select {
		case u.queueCh <- sessionID:
			delete(u.deferredQueueSet, sessionID)
		default:
			blocked = true
			remaining = append(remaining, sessionID)
		}
	}
	if len(remaining) == 0 {
		u.deferredQueueIDs = nil
		return
	}
	u.deferredQueueIDs = remaining
}

// queueWorkerLoop processes work items from the shared channel.
// queueWorkerLoop 用于从共享通道中消费工作项。
func (u *PostActionUseCase) queueWorkerLoop() {
	defer u.queueWG.Done()

	for {
		select {
		case <-u.queueCtx.Done():
			return
		case sessionID := <-u.queueCh:
			if !u.tryLockSession(sessionID) {
				u.pushQueueID(sessionID)
				continue
			}
			u.handleQueuedSession(sessionID)
			u.unlockSession(sessionID)
			u.flushDeferredQueueIDs()
		}
	}
}

// maintenanceLoop runs a single periodic ticker that performs low-priority maintenance tasks once per interval regardless of worker count.
// maintenanceLoop 用于运行单一的周期性维护 ticker，确保低优先级维护任务每个周期只执行一次，与 worker 数量无关。
func (u *PostActionUseCase) maintenanceLoop() {
	defer u.queueWG.Done()
	ticker := time.NewTicker(u.analysisCfg.QueueScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-u.queueCtx.Done():
			return
		case <-ticker.C:
			if u.queueMaintenanceBackoffActive(time.Now()) {
				continue
			}
			u.convergeExpiredProfiles()
			u.scanIdlePendingSessions()
			u.flushDeferredQueueIDs()
		}
	}
}

// tryLockSession attempts to acquire an exclusive lock for one session so no two workers process it concurrently.
// tryLockSession 用于尝试获取某个 session 的排他锁，确保不会有两个 worker 同时处理同一 session。
func (u *PostActionUseCase) tryLockSession(sessionID uint64) bool {
	u.queueMu.Lock()
	defer u.queueMu.Unlock()
	if u.sessionLocked[sessionID] {
		return false
	}
	u.sessionLocked[sessionID] = true
	return true
}

// unlockSession releases the exclusive lock for one session after its queued work completes.
// unlockSession 用于在某个 session 的队列工作完成后释放排他锁。
func (u *PostActionUseCase) unlockSession(sessionID uint64) {
	u.queueMu.Lock()
	defer u.queueMu.Unlock()
	delete(u.sessionLocked, sessionID)
}

// handleQueuedSession snapshots the latest queue state for one session, runs one asynchronous extraction attempt, and reschedules if new turns arrived mid-flight.
// handleQueuedSession 用于获取某个 session 的最新队列状态、执行一次异步提炼尝试，并在处理中间又有新 turn 到来时自动重排。
func (u *PostActionUseCase) handleQueuedSession(sessionID uint64) {
	if u == nil || sessionID == 0 {
		return
	}

	// Mark the current queue slot as in-flight before running the heavier analysis path.
	// 在执行较重的分析路径前，先把当前队列槽位标记成处理中。
	var (
		session logicdomain.SessionRef
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
	u.queueMu.Unlock()

	u.processQueuedTurns(session, "queue")

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
		} else {
			delete(u.queueState, sessionID)
		}
	}
	u.queueMu.Unlock()

	if requeue {
		u.pushQueueID(sessionID)
	}
}

// scanIdlePendingSessions periodically promotes stale sessions with pending turns into queue items so async extraction can recover after crashes or temporary failures.
// scanIdlePendingSessions 用于周期性地把仍有待处理 turn 且已空闲过久的 session 提升为队列任务，方便异步提炼在崩溃或临时失败后恢复。
func (u *PostActionUseCase) scanIdlePendingSessions() {
	if u == nil || u.store == nil || u.analysisCfg.IdleTimeout <= 0 {
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
		u.enqueueSessionAnalysis(session)
	}
}

// processQueuedTurns loads pending turns for one session and applies the single-turn analyzer asynchronously in durable order.
// processQueuedTurns 用于加载某个 session 的待处理 turn，并按持久化顺序异步执行逐轮单轮分析。
func (u *PostActionUseCase) processQueuedTurns(session logicdomain.SessionRef, source string) {
	if u == nil || u.store == nil || u.turnAnalyzer == nil || session.SessionID == 0 {
		return
	}
	workerCtx := u.queueCtx
	if workerCtx == nil {
		workerCtx = context.Background()
	}

	// Load the currently pending turns first so one queue slot can drain as much finished work as possible before yielding.
	// 先加载当前所有待处理 turn，让一次队列处理尽量多地消化已落库但尚未提炼的工作。
	pendingTurns, err := u.store.LoadPendingSessionTurns(workerCtx, session)
	if err != nil {
		if u.logger != nil {
			u.logger.Error("post-action pending turns load failed", "session_key", session.SessionKey, "session_id", session.SessionID, "source", source, "err", err)
		}
		return
	}
	for _, pendingTurn := range pendingTurns {
		persistedTurn := logicdomain.PersistedTurnRecord{
			ID:               pendingTurn.ID,
			SessionID:        pendingTurn.SessionID,
			ProjectID:        pendingTurn.ProjectID,
			DehydratedBudget: pendingTurn.DehydratedBudget,
			CreatedAt:        pendingTurn.CreatedAt,
			UpdatedAt:        pendingTurn.UpdatedAt,
		}
		rawTurn, err := turnRecordFromStoredTurn(pendingTurn)
		if err != nil {
			// Mark the corrupted turn as done so it stops being returned by LoadPendingSessionTurns.
			// This prevents an infinite skip loop while allowing subsequent healthy turns to be processed.
			// 将损坏的 turn 标记为已处理，让它不再被 LoadPendingSessionTurns 返回。
			// 这样既能防止无限跳过循环，又能让后续健康的 turn 继续被处理。
			if u.logger != nil {
				u.logger.Error("post-action queued turn decode failed, marking as corrupted", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", pendingTurn.ID, "source", source, "err", err)
			}
			if markErr := u.store.MarkTurnAsCorrupted(workerCtx, session, pendingTurn.ID); markErr != nil {
				if u.logger != nil {
					u.logger.Error("post-action corrupted turn mark failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", pendingTurn.ID, "err", markErr)
				}
				return
			}
			continue
		}
		// Bound each complete background analysis chain independently so slow models get the configured PostAction budget without inheriting the intake timeout.
		// 独立限制每条完整后台分析链，让慢模型获得配置的 PostAction 预算且不继承接入超时。
		analysisCtx, cancelAnalysis := context.WithTimeout(workerCtx, u.analysisCfg.AnalysisTimeout)
		analysisErr := u.applyImmediateTurnAnalysis(analysisCtx, session, persistedTurn, rawTurn)
		cancelAnalysis()
		if analysisErr != nil {
			if u.logger != nil {
				fields := []any{
					"session_key", session.SessionKey,
					"session_id", session.SessionID,
					"turn_id", pendingTurn.ID,
					"source", source,
				}
				fields = u.appendQueuedTurnAnalysisFailureLogFields(fields, analysisErr)
				u.logger.Error("post-action queued turn analysis failed", fields...)
			}
			failureResult, recordErr := u.store.RecordTurnAnalysisFailure(
				workerCtx,
				session,
				pendingTurn.ID,
				logicdomain.TurnAnalysisFailureStagePostAction,
				analysisErr.Error(),
				u.analysisCfg.FailurePassThreshold,
				logicdomain.IsOutcomeUncertain(analysisErr),
			)
			if recordErr != nil {
				if u.logger != nil {
					u.logger.Error("post-action queued turn failure state write failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", pendingTurn.ID, "source", source, "err", recordErr)
				}
				return
			}
			if u.logger != nil {
				u.logger.Warn(
					"post-action queued turn failure state recorded",
					"session_key", session.SessionKey,
					"session_id", session.SessionID,
					"turn_id", pendingTurn.ID,
					"attempt_count", failureResult.AttemptCount,
					"failure_pass_threshold", u.analysisCfg.FailurePassThreshold,
					"failure_status", failureResult.Status,
				)
			}
			if failureResult.Passed {
				continue
			}
			return
		}
	}
}

// appendQueuedTurnAnalysisFailureLogFields enriches queued post-action failures with the model and raw output that belong to the failing LLM scene.
// appendQueuedTurnAnalysisFailureLogFields 用于为排队 post-action 失败补充归属于失败 LLM 场景的模型标识和原始输出。
func (u *PostActionUseCase) appendQueuedTurnAnalysisFailureLogFields(fields []any, err error) []any {
	execution, executionFound := queuedLLMExecutionForError(err)
	if executionFound {
		fields = appendLLMExecutionLogFields(fields, execution)
	}
	var invalid logicdomain.InvalidLLMOutputError
	if errors.As(err, &invalid) {
		scene := strings.TrimSpace(invalid.Scene)
		if !executionFound && scene != "" {
			fields = append(fields, "llm_scene", scene)
		}
		if model := u.queuedLLMFailureModel(scene); !executionFound && model != "" {
			fields = append(fields, "configured_model", model)
		}
		// Invalid LLM output can fail either at JSON decoding or at stricter structural validation; both cases need the raw provider body for actionable operator debugging.
		// LLM 畸形输出既可能失败在 JSON 解码，也可能失败在更严格的结构校验；两类问题都需要原始 provider 响应用于运维排障。
		if raw := strings.TrimSpace(invalid.Raw); raw != "" {
			fields = append(fields, "llm_raw_output", invalid.Raw)
		}
		return append(fields, "err", err)
	}

	// Preserve the historical analyzer model field for non-structured queue failures so storage or embedding failures still retain their original pipeline context.
	// 对非结构化队列失败保留历史 analyzer 模型字段，让存储或 embedding 失败仍带有原本的流水线上下文。
	if model := u.queuedTurnAnalyzerModel(); !executionFound && model != "" {
		fields = append(fields, "configured_model", model)
	}
	return append(fields, "err", err)
}

// queuedLLMExecutionForError resolves the most relevant physical LLM execution retained by a structured-output or later pipeline failure.
// queuedLLMExecutionForError 用于解析结构化输出失败或后续流水线失败所保留的最相关物理 LLM 执行事实。
//
// Parameters:
// 参数：
//   - err: queued PostAction analysis error that may retain direct invalid-output metadata or completed pipeline executions.
//   - err：可能保留无效输出直接元数据或已完成流水线执行事实的排队 PostAction 分析错误。
//
// Returns:
// 返回值：
//   - logicdomain.LLMExecutionMetadata: direct failing call, or the latest completed call before a downstream failure.
//   - logicdomain.LLMExecutionMetadata：直接失败调用，或下游失败前最近完成的调用。
//   - bool: true only when physical execution facts were retained.
//   - bool：仅在保留了物理执行事实时为 true。
func queuedLLMExecutionForError(err error) (logicdomain.LLMExecutionMetadata, bool) {
	var invalid logicdomain.InvalidLLMOutputError
	if errors.As(err, &invalid) && invalid.Execution != nil {
		return *invalid.Execution, true
	}
	var contextErr logicdomain.LLMExecutionContextError
	if errors.As(err, &contextErr) && len(contextErr.Executions) > 0 {
		return contextErr.Executions[len(contextErr.Executions)-1], true
	}
	return logicdomain.LLMExecutionMetadata{}, false
}

// appendLLMExecutionLogFields appends identity and complete token accounting without promoting configured intent into an observed response model.
// appendLLMExecutionLogFields 用于追加身份与完整 token 统计，同时不把配置意图冒充为已观测响应模型。
//
// Parameters:
// 参数：
//   - fields: existing structured queue log fields.
//   - fields：已有的结构化队列日志字段。
//   - execution: retained physical LLM call metadata.
//   - execution：保留的物理 LLM 调用元数据。
//
// Returns:
// 返回值：
//   - []any: extended structured field list.
//   - []any：扩展后的结构化字段列表。
func appendLLMExecutionLogFields(fields []any, execution logicdomain.LLMExecutionMetadata) []any {
	return append(fields,
		"llm_scene", strings.TrimSpace(execution.Purpose),
		"configured_model", strings.TrimSpace(execution.ConfiguredModel),
		"response_model", strings.TrimSpace(execution.ResponseModel),
		"request_id", strings.TrimSpace(execution.RequestID),
		"prompt_tokens", execution.Usage.PromptTokens,
		"completion_tokens", execution.Usage.CompletionTokens,
		"total_tokens", execution.Usage.TotalTokens,
		"cached_input_tokens", execution.Usage.CachedInputTokens,
		"reasoning_tokens", execution.Usage.ReasoningTokens,
	)
}

// queuedLLMFailureModel resolves the configured model for the exact LLM scene that produced one InvalidLLMOutputError.
// queuedLLMFailureModel 用于根据产生 InvalidLLMOutputError 的具体 LLM 场景解析对应的配置模型。
func (u *PostActionUseCase) queuedLLMFailureModel(scene string) string {
	switch scene {
	case "postaction_l1_main":
		return u.queuedTurnAnalyzerModel()
	case "postaction_l2_main":
		return u.queuedCandidateReviewerModel()
	default:
		return ""
	}
}

// queuedTurnAnalyzerModel returns the first-stage analyzer model label without exposing nil checks to the logging caller.
// queuedTurnAnalyzerModel 用于返回第一层 analyzer 模型标识，并把 nil 检查封装在日志调用方之外。
func (u *PostActionUseCase) queuedTurnAnalyzerModel() string {
	if u == nil || u.turnAnalyzer == nil {
		return ""
	}
	return strings.TrimSpace(u.turnAnalyzer.AnalyzeModel())
}

// queuedCandidateReviewerModel returns the second-stage reviewer model label without guessing when no reviewer is configured.
// queuedCandidateReviewerModel 用于返回第二层 reviewer 模型标识；当 reviewer 未配置时不会猜测模型。
func (u *PostActionUseCase) queuedCandidateReviewerModel() string {
	if u == nil || u.candidateReviewer == nil {
		return ""
	}
	return strings.TrimSpace(u.candidateReviewer.ReviewModel())
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

// markQueueMaintenanceBackoff pauses low-priority maintenance scans for one short window after the shared SQLite connection starts reporting poisoned-state errors.
// markQueueMaintenanceBackoff 用于在共享 SQLite 连接开始报“污染态”错误后，暂时暂停低优先级维护扫描，避免持续撞击坏连接。
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

// shouldPauseQueueMaintenance classifies the gateway failures that indicate the shared SQLite connection is already poisoned and periodic scans should stand down briefly.
// shouldPauseQueueMaintenance 用于识别这类网关失败：它表明共享 SQLite 连接已经进入污染态，周期性扫描应暂时退避。
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
