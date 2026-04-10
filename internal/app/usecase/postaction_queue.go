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

// postActionQueueState tracks one session's deduplicated queue state so frequent post-action writes collapse into one worker item.
// postActionQueueState 用于跟踪单个 session 的去重队列状态，让高频 post-action 写入可以折叠成一个工作项。
type postActionQueueState struct {
	Session  logicdomain.SessionRef
	Queued   bool
	InFlight bool
	Dirty    bool
}

// startQueueWorker boots the background worker and the periodic idle scan used by the queued single-turn extraction pipeline.
// startQueueWorker 用于启动后台工作器和周期性空闲扫描，让排队式单轮提炼流水线开始工作。
func (u *PostActionUseCase) startQueueWorker() {
	if u == nil || u.queueCh != nil {
		return
	}
	u.queueCtx, u.queueCancel = context.WithCancel(context.Background())
	u.queueCh = make(chan uint64, 256)
	u.queueState = map[uint64]*postActionQueueState{}
	u.deferredQueueSet = map[uint64]struct{}{}
	u.queueWG.Add(1)
	go u.queueWorkerLoop()
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
		// Only remember deferred ids when the queue worker lifetime context exists.
		// Partially constructed test instances may provide queueCh without queueCtx, and
		// in that case the safer behavior is to skip the overflow path instead of silently retaining dead deferred work.
		// 只有在队列工作器生命周期 context 已存在时才记录延迟项。
		// 部分装配的测试实例可能只提供 queueCh 而没有 queueCtx，这时跳过溢出路径比悄悄保留永远无法刷新的延迟任务更安全。
		if u.queueCtx == nil {
			return
		}
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
	defer u.queueMu.Unlock()
	if u.deferredQueueSet == nil {
		u.deferredQueueSet = map[uint64]struct{}{}
	}
	if _, exists := u.deferredQueueSet[sessionID]; exists {
		return
	}
	u.deferredQueueSet[sessionID] = struct{}{}
	u.deferredQueueIDs = append(u.deferredQueueIDs, sessionID)
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
			u.flushDeferredQueueIDs()
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
			// Return the error instead of silently continuing; a corrupted turn that keeps returning from
			// LoadPendingSessionTurns would cause an infinite skip loop without ever advancing the window.
			// Returning the error stops the queue and surfaces the issue for operator intervention.
			// 返回错误而不是静默 continue；损坏的 turn 如果持续从 LoadPendingSessionTurns 返回，
			// 会导致无限跳过循环而永远无法推进窗口。返回错误可以停止队列并让运维介入处理。
			if u.logger != nil {
				u.logger.Error("post-action queued turn decode failed, halting queue", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", pendingTurn.ID, "source", source, "err", err)
			}
			return
		}
		if err := u.applyImmediateTurnAnalysis(workerCtx, session, persistedTurn, rawTurn); err != nil {
			if u.logger != nil {
				fields := []any{
					"session_key", session.SessionKey,
					"session_id", session.SessionID,
					"turn_id", pendingTurn.ID,
					"source", source,
				}
				fields = u.appendQueuedTurnAnalysisFailureLogFields(fields, err)
				u.logger.Error("post-action queued turn analysis failed", fields...)
			}
			return
		}
	}
}

// appendQueuedTurnAnalysisFailureLogFields enriches queued post-action first-stage failures with the configured model and, for JSON decode failures, the verbatim model output needed for operator debugging.
// appendQueuedTurnAnalysisFailureLogFields 用于为排队 post-action 第一层失败补充日志字段：默认带上当前模型；若是 JSON 解码失败，则追加排障所需的模型原始输出。
func (u *PostActionUseCase) appendQueuedTurnAnalysisFailureLogFields(fields []any, err error) []any {
	// Always surface the configured model so operators can correlate malformed outputs with one concrete route/model combination.
	// 始终输出当前配置模型，便于运维把畸形响应快速关联到具体的路由/模型组合。
	if u != nil && u.turnAnalyzer != nil {
		if model := strings.TrimSpace(u.turnAnalyzer.AnalyzeModel()); model != "" {
			fields = append(fields, "model", model)
		}
	}

	// Only dump the raw provider body for postaction_l1_main JSON decode failures, because that class of issue cannot be diagnosed from the summary error text alone.
	// 只有 postaction_l1_main 的 JSON 解码失败才追加原始 provider 响应，因为这类问题仅靠摘要错误文本无法定位实际返回体。
	var invalid logicdomain.InvalidLLMOutputError
	if errors.As(err, &invalid) && invalid.Scene == "postaction_l1_main" && invalid.Message == "json decode failed" {
		if raw := strings.TrimSpace(invalid.Raw); raw != "" {
			fields = append(fields, "llm_raw_output", invalid.Raw)
		}
	}
	return append(fields, "err", err)
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
