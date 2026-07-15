// retention.go implements the independent cold-data maintenance worker that recycles terminal durable memories, compacts long-idle sessions, and purges expired trash batches.
// retention.go 用于实现独立的冷数据维护工作器，负责回收终态长期记忆、压缩长期空闲 session，并清理超过保留窗口的回收站批次。
package usecase

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

const (
	// defaultRetentionRecycleBatchSize bounds one recycle pass so maintenance can chip away at cold rows without monopolizing the shared stores.
	// defaultRetentionRecycleBatchSize 用于限制单次回收批量，避免维护流程长时间独占共享存储。
	defaultRetentionRecycleBatchSize = 128

	// defaultRetentionTurnJobScanBatchSize bounds one cold-turn candidate scan so the maintenance worker can enqueue recyclable sessions incrementally instead of building one unbounded queue burst.
	// defaultRetentionTurnJobScanBatchSize 用于限制一次冷 turn 候选扫描批量，让维护工作器按增量入队可回收 session，而不是一次性制造无界任务洪峰。
	defaultRetentionTurnJobScanBatchSize = 64

	// defaultRetentionRecycleJobBatchSize bounds one claimed recycle-job batch so cold-turn execution stays incremental and does not monopolize the shared stores.
	// defaultRetentionRecycleJobBatchSize 用于限制一次领取的回收任务批量，让冷 turn 执行保持渐进，不会长时间独占共享存储。
	defaultRetentionRecycleJobBatchSize = 16

	// defaultRetentionIdleSessionBatchSize bounds one idle-session recycle pass so the maintenance worker can compact several cold sessions without monopolizing the shared stores.
	// defaultRetentionIdleSessionBatchSize 用于限制单次 idle-session 回收批量，让维护工作器能压缩多个冷 session，同时避免长时间独占共享存储。
	defaultRetentionIdleSessionBatchSize = 32

	// defaultRetentionPurgeBatchSize bounds one trash purge pass so permanent cleanup stays incremental and predictable.
	// defaultRetentionPurgeBatchSize 用于限制单次回收站清理批量，让永久删除流程保持渐进且可预测。
	defaultRetentionPurgeBatchSize = 64

	// defaultRetentionVectorGCBatchSize bounds one retry pass over pending vector-delete jobs so the maintenance worker can compensate sidecar cleanup failures without turning one loop into an unbounded sweep.
	// defaultRetentionVectorGCBatchSize 用于限制一次待重试向量删除任务的处理批量，让维护工作器补偿旁路向量清理失败时仍保持有界。
	defaultRetentionVectorGCBatchSize = 128

	// defaultRetentionVectorGCRetryDelay keeps failed sidecar vector deletes on a calm fixed retry cadence so transient vector-store outages do not create a busy retry loop.
	// defaultRetentionVectorGCRetryDelay 用于给失败的旁路向量删除设置平稳且固定的重试延迟，避免向量库瞬时故障引发忙等式重试。
	defaultRetentionVectorGCRetryDelay = 5 * time.Minute

	// defaultRetentionRecycleJobRetryDelay keeps failed cold-turn recycle jobs on a calm fixed retry cadence so transient lock contention does not create a busy retry loop.
	// defaultRetentionRecycleJobRetryDelay 用于给失败的冷 turn 回收任务设置平稳且固定的重试延迟，避免瞬时锁竞争引发忙等式重试。
	defaultRetentionRecycleJobRetryDelay = 5 * time.Minute

	// defaultRetentionRecycleJobClaimLease reserves one claimed recycle-job batch for a short bounded window so concurrent maintenance workers do not execute the same cold-turn work twice.
	// defaultRetentionRecycleJobClaimLease 用于给一批已领取的回收任务保留一个短而有界的租约窗口，避免并发维护工作器重复执行同一批冷 turn 工作。
	defaultRetentionRecycleJobClaimLease = 2 * time.Minute

	// defaultRetentionVectorGCClaimLease reserves one claimed retry batch for a short bounded window so concurrent maintenance workers do not delete the same vectors repeatedly.
	// defaultRetentionVectorGCClaimLease 用于给一批已领取的重试任务保留一个短而有界的租约窗口，避免并发维护工作器重复删除同一批向量。
	defaultRetentionVectorGCClaimLease = 2 * time.Minute
)

// RetentionConfig keeps the narrow runtime knobs needed by the cold-data maintenance worker after process-level config normalization is complete.
// RetentionConfig 用于保存冷数据维护工作器所需的最小运行时参数；此时进程级配置已完成规范化。
type RetentionConfig struct {
	Enabled                     bool
	RecycleScanInterval         time.Duration
	SessionIdleRecycleAfter     time.Duration
	TurnHotWindowSize           int
	TrashRetention              time.Duration
	ProtectPriorityFloor        string
	ProtectMemoryLevelFloor     string
	SkipProtectedSharedMemories bool
}

// RetentionUseCase owns the background maintenance loop that keeps cold durable data out of the hot relational tables without disturbing active recall traffic.
// RetentionUseCase 用于承载后台维护循环，在不干扰活跃召回流量的前提下，把冷状态长期数据从热关系表中移出。
type RetentionUseCase struct {
	store             appports.RetentionStore
	vector            appports.VectorStore
	cfg               RetentionConfig
	logger            *logx.Logger
	workerCtx         context.Context
	workerCancel      context.CancelFunc
	workerWG          sync.WaitGroup
	workerInitialized bool
}

// NewRetentionUseCase constructs the cold-data maintenance worker and starts the shared background loop whenever any retention-adjacent workload is enabled.
// NewRetentionUseCase 用于构建冷数据维护工作器，并在任一 retention 邻接工作负载启用时启动共享后台循环。
func NewRetentionUseCase(store appports.RetentionStore, vector appports.VectorStore, cfg RetentionConfig, logger *logx.Logger) *RetentionUseCase {
	u := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg:    cfg,
		logger: logger,
	}
	u.startWorker()
	return u
}

// Shutdown stops the retention worker before downstream relational or vector stores are closed.
// Shutdown 用于在下游关系存储和向量存储关闭前，先停止 retention 工作器。
func (u *RetentionUseCase) Shutdown(ctx context.Context) error {
	if u == nil || u.workerCancel == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u.workerCancel()
	done := make(chan struct{})
	go func() {
		u.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startWorker boots the shared background ticker once at least one maintenance workload is enabled and the required stores are available.
// startWorker 用于在至少存在一项维护工作且所需存储可用时启动共享后台 ticker。
func (u *RetentionUseCase) startWorker() {
	if u == nil || u.workerInitialized || u.cfg.RecycleScanInterval <= 0 || !u.maintenanceEnabled() {
		return
	}
	u.workerCtx, u.workerCancel = context.WithCancel(context.Background())
	u.workerInitialized = true
	u.workerWG.Add(1)
	go u.workerLoop()
}

// workerLoop performs one eager maintenance pass at startup and then keeps scanning on the configured cadence until shutdown.
// workerLoop 用于在启动后立即执行一次维护，并在后续按照配置周期持续扫描，直到收到关闭信号。
func (u *RetentionUseCase) workerLoop() {
	defer u.workerWG.Done()
	u.runScheduledMaintenance(u.workerCtx)

	ticker := time.NewTicker(u.cfg.RecycleScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-u.workerCtx.Done():
			return
		case <-ticker.C:
			u.runScheduledMaintenance(u.workerCtx)
		}
	}
}

// runScheduledMaintenance executes the configured retention workload and vector-GC retry work on the shared maintenance cadence.
// runScheduledMaintenance 用于在共享维护节奏上执行已启用的 retention 工作负载与向量删除重试工作。
func (u *RetentionUseCase) runScheduledMaintenance(ctx context.Context) {
	if u == nil {
		return
	}
	if u.retentionMaintenanceEnabled() {
		u.runMaintenance(ctx)
	}
	u.runVectorGCMaintenance(ctx)
}

// runMaintenance executes terminal-memory recycle, idle-session recycle, and one trash purge pass so hot-table compaction and delayed hard-delete share the same maintenance cadence.
// runMaintenance 用于执行终态记忆回收、idle-session 回收和一次回收站清理，让热表压缩与延迟硬删除共享同一维护节奏。
func (u *RetentionUseCase) runMaintenance(ctx context.Context) {
	if u == nil || u.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()

	// Recycle terminal memories first so later purge only touches already-detached trash rows.
	// 先回收终态记忆，再做 purge，这样后续硬删除只会触碰已经脱离热主表的 trash 行。
	recycleResult, recycleErr := u.store.RecycleColdMemories(ctx, logicdomain.MemoryRecycleQuery{
		Limit:                       defaultRetentionRecycleBatchSize,
		RecycledAt:                  now,
		RecycleReason:               logicdomain.RecycleReasonColdTerminalMemory,
		ProtectPriorityFloor:        retentionPriorityFloorValue(u.cfg.ProtectPriorityFloor),
		ProtectMemoryLevelFloor:     retentionMemoryLevelFloorValue(u.cfg.ProtectMemoryLevelFloor),
		SkipProtectedSharedMemories: u.cfg.SkipProtectedSharedMemories,
	})
	if recycleErr != nil {
		u.logError("retention cold memory recycle failed", recycleErr)
		// Clean obsolete vectors when the store reports an uncertain post-delete boundary while still returning the durable recycle coordinates.
		// 当存储层在删除后边界报告结果不确定、但仍返回长期回收坐标时，继续清理已过时向量。
		if logicdomain.IsOutcomeUncertain(recycleErr) {
			u.cleanupVectors(ctx, recycleResult, now)
		}
	} else {
		u.cleanupVectors(ctx, recycleResult, now)
		u.logRecycleResult(recycleResult)
	}

	// Enqueue independent cold-turn recycle work before execution so scan and execution no longer share one transaction path.
	// 先为独立冷 turn 回收入队，再单独执行已领取任务，让扫描与执行不再共用同一事务路径。
	enqueuedJobCount, enqueueErr := u.store.EnqueueColdTurnRecycleJobs(ctx, logicdomain.ColdTurnRecycleJobEnqueueQuery{
		Limit:             defaultRetentionTurnJobScanBatchSize,
		ScannedAt:         now,
		NextRunAt:         now,
		TurnHotWindowSize: max(u.cfg.TurnHotWindowSize, 0),
	})
	if enqueueErr != nil {
		u.logError("retention cold turn job enqueue failed", enqueueErr)
		// Preserve the confirmed autocommit enqueue count even when a later SQLite candidate failed, because those jobs will still be claimable in this maintenance pass.
		// 即使后续 SQLite 候选入队失败，也保留已确认的自动提交入队数量，因为这些任务仍会在本轮维护中被领取。
		if enqueuedJobCount > 0 {
			u.logRecycleJobEnqueueCount(enqueuedJobCount)
		}
	} else {
		u.logRecycleJobEnqueueCount(enqueuedJobCount)
	}
	u.runPendingColdTurnRecycleJobs(ctx, now)

	// Recycle long-idle sessions only after terminal-memory recycle has already shrunk the obvious cold rows, so the session pass can focus on active-but-expired session facts and orphaned turns.
	// 先完成终态记忆回收，再处理长期空闲 session，让后者聚焦 active 但已失效的 session 事实和无引用旧 turn。
	idleBefore := now.Add(-u.cfg.SessionIdleRecycleAfter)
	sessionResult, sessionErr := u.store.RecycleIdleSessions(ctx, logicdomain.SessionIdleRecycleQuery{
		Limit:             defaultRetentionIdleSessionBatchSize,
		RecycledAt:        now,
		IdleBefore:        idleBefore,
		TurnHotWindowSize: max(u.cfg.TurnHotWindowSize, 0),
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if sessionErr != nil {
		u.logError("retention idle session recycle failed", sessionErr)
		// Preserve vector cleanup for already-archived idle-session memories even when a late SQLite maintenance index step is uncertain.
		// 即使后置 SQLite 维护索引步骤结果不确定，也要保留已归档 idle-session memory 的向量清理。
		if logicdomain.IsOutcomeUncertain(sessionErr) {
			u.cleanupIdleSessionVectors(ctx, sessionResult, now)
		}
	} else {
		u.cleanupIdleSessionVectors(ctx, sessionResult, now)
		u.logIdleSessionRecycleResult(sessionResult)
	}

	// Purge expired trash in a second step so the soft-backup window is enforced independently from the hot-table recycle path.
	// 第二步单独清理超期回收站，以便软备份窗口能独立于热表回收路径生效。
	purgeBefore := now.Add(-u.cfg.TrashRetention)
	purgeResult, purgeErr := u.store.PurgeExpiredTrash(ctx, purgeBefore, defaultRetentionPurgeBatchSize)
	if purgeErr != nil {
		u.logError("retention trash purge failed", purgeErr)
		return
	}
	u.logPurgeResult(purgeResult)
}

// runVectorGCMaintenance retries queued sidecar vector deletes on the shared ticker even when classical cold-data recycle is disabled.
// runVectorGCMaintenance 用于在共享维护 ticker 上重试旁路向量删除，即使传统冷数据回收被关闭也照常执行。
func (u *RetentionUseCase) runVectorGCMaintenance(ctx context.Context) {
	if u == nil || !u.vectorGCMaintenanceEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u.retryPendingVectorGCJobs(ctx, time.Now().UTC())
}

// runPendingColdTurnRecycleJobs claims one bounded recycle-job batch from the persistent cold-turn queue and executes each claimed session archive independently so scan and execution remain separated.
// runPendingColdTurnRecycleJobs 用于从持久化冷 turn 队列里领取一批有界回收任务，并逐条独立执行每个 session 归档，让扫描与执行保持分离。
func (u *RetentionUseCase) runPendingColdTurnRecycleJobs(ctx context.Context, now time.Time) {
	if u == nil || u.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now = chooseRetentionTimeOrNow(now)
	claimUntil := now.Add(defaultRetentionRecycleJobClaimLease)
	jobs, err := u.store.ClaimPendingRecycleJobs(ctx, logicdomain.RecycleJobTypeColdTurn, now, claimUntil, defaultRetentionRecycleJobBatchSize)
	if err != nil {
		u.logError("retention cold turn recycle job claim failed", err)
		return
	}
	if len(jobs) == 0 {
		return
	}

	batchIDs := make([]uint64, 0, len(jobs))
	sessionIDs := make([]uint64, 0, len(jobs))
	recycledTurnCount := 0
	for _, job := range jobs {
		result, recycleErr := u.store.RecycleColdTurns(ctx, logicdomain.ColdTurnRecycleQuery{
			SessionID:         job.SessionID,
			RecycledAt:        now,
			TurnHotWindowSize: max(u.cfg.TurnHotWindowSize, 0),
			RecycleReason:     logicdomain.RecycleReasonColdTurnArchive,
		})
		if recycleErr != nil {
			u.logError("retention cold turn recycle execution failed", recycleErr)
			retryAt := now.Add(defaultRetentionRecycleJobRetryDelay)
			if retryErr := u.store.RetryRecycleJobs(ctx, []uint64{job.ID}, retryAt, recycleErr.Error()); retryErr != nil {
				u.logError("retention cold turn recycle reschedule failed", retryErr)
			}
			continue
		}
		if completeErr := u.store.CompleteRecycleJobs(ctx, []uint64{job.ID}, now); completeErr != nil {
			u.logError("retention cold turn recycle completion failed", completeErr)
			continue
		}
		if result.BatchID == 0 || result.RecycledTurnCount <= 0 {
			continue
		}
		batchIDs = append(batchIDs, result.BatchID)
		sessionIDs = append(sessionIDs, result.SessionID)
		recycledTurnCount += result.RecycledTurnCount
	}
	u.logColdTurnRecycleResult(batchIDs, sessionIDs, recycledTurnCount)
}

// cleanupVectors removes obsolete vector rows after the relational recycle transaction has succeeded; if the sidecar delete fails, the vector ids are persisted into the retry queue instead of being lost after trash purge.
// cleanupVectors 用于在关系回收事务成功后清理过时向量；若旁路删除失败，则把向量 id 持久化到重试队列，避免在回收站 purge 后永久丢失清理坐标。
func (u *RetentionUseCase) cleanupVectors(ctx context.Context, result logicdomain.MemoryRecycleResult, now time.Time) {
	vectorIDs := normalizeVectorGCIDs(result.RecycledVectorIDs)
	if u == nil || u.vector == nil || len(vectorIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := u.vector.DeleteByIDs(ctx, vectorIDs); err != nil {
		u.logError("retention vector cleanup failed", err)
		u.enqueueVectorGCJobs(ctx, retentionVectorGCBatchID(result.BatchID), vectorIDs, now)
	}
}

// cleanupIdleSessionVectors removes obsolete vector rows after one idle-session recycle pass succeeds; failures are bridged into the same persistent retry queue so stale session compaction cannot strand orphan vectors.
// cleanupIdleSessionVectors 用于在一次 idle-session 回收成功后删除过时向量；若失败则写入同一持久化重试队列，避免空闲 session 压缩留下孤儿向量。
func (u *RetentionUseCase) cleanupIdleSessionVectors(ctx context.Context, result logicdomain.SessionIdleRecycleResult, now time.Time) {
	vectorIDs := normalizeVectorGCIDs(result.RecycledVectorIDs)
	if u == nil || u.vector == nil || len(vectorIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := u.vector.DeleteByIDs(ctx, vectorIDs); err != nil {
		u.logError("retention idle-session vector cleanup failed", err)
		u.enqueueVectorGCJobs(ctx, retentionVectorGCBatchID(singleBatchIDOrZero(result.BatchIDs)), vectorIDs, now)
	}
}

// enqueueVectorGCJobs persists one batch of failed sidecar vector deletes so later maintenance passes can retry them even after the source rows have already left the hot tables.
// enqueueVectorGCJobs 用于持久化一批失败的旁路向量删除任务，让后续维护轮次即使在源行已离开热表后仍能继续重试。
func (u *RetentionUseCase) enqueueVectorGCJobs(ctx context.Context, batchID uint64, vectorIDs []string, now time.Time) {
	vectorIDs = normalizeVectorGCIDs(vectorIDs)
	if u == nil || u.store == nil || len(vectorIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	nextRunAt := chooseRetentionTimeOrNow(now).Add(defaultRetentionVectorGCRetryDelay)
	if err := u.store.EnqueueVectorGCJobs(ctx, logicdomain.VectorGCJobEnqueueQuery{
		BatchID:   batchID,
		JobType:   logicdomain.VectorGCJobTypeRetentionRecycle,
		VectorIDs: vectorIDs,
		NextRunAt: nextRunAt,
	}); err != nil {
		u.logError("retention vector cleanup enqueue failed", err)
		return
	}
}

// retryPendingVectorGCJobs leases one bounded retry batch from the persistent queue and either completes or reschedules it based on the sidecar delete outcome.
// retryPendingVectorGCJobs 用于从持久化队列里领取一批有界的重试任务，并根据旁路删除结果决定完成或再次调度。
func (u *RetentionUseCase) retryPendingVectorGCJobs(ctx context.Context, now time.Time) {
	if u == nil || u.store == nil || u.vector == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now = chooseRetentionTimeOrNow(now)
	claimUntil := now.Add(defaultRetentionVectorGCClaimLease)
	jobs, err := u.store.ClaimPendingVectorGCJobs(ctx, now, claimUntil, defaultRetentionVectorGCBatchSize)
	if err != nil {
		u.logError("retention vector gc claim failed", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	vectorIDs := make([]string, 0, len(jobs))
	jobIDs := make([]uint64, 0, len(jobs))
	invalidJobIDs := make([]uint64, 0)
	for _, job := range jobs {
		if strings.TrimSpace(job.VectorID) == "" || job.ID == 0 {
			if job.ID != 0 {
				invalidJobIDs = append(invalidJobIDs, job.ID)
			}
			continue
		}
		vectorIDs = append(vectorIDs, job.VectorID)
		jobIDs = append(jobIDs, job.ID)
	}
	// Complete invalid jobs (empty vector ID) to prevent them from staying claimed forever.
	// 完成无效的 vector-GC 任务（向量 ID 为空），防止它们永远停留在 claimed 状态。
	if len(invalidJobIDs) > 0 {
		if err := u.store.CompleteVectorGCJobs(ctx, invalidJobIDs, now); err != nil {
			u.logError("retention complete invalid vector gc jobs failed", err)
		}
	}
	vectorIDs = normalizeVectorGCIDs(vectorIDs)
	if len(jobIDs) == 0 || len(vectorIDs) == 0 {
		return
	}
	if _, err := u.vector.DeleteByIDs(ctx, vectorIDs); err != nil {
		u.logError("retention vector gc retry failed", err)
		retryAt := now.Add(defaultRetentionVectorGCRetryDelay)
		if retryErr := u.store.RetryVectorGCJobs(ctx, jobIDs, retryAt, err.Error()); retryErr != nil {
			u.logError("retention vector gc reschedule failed", retryErr)
		}
		return
	}
	if err := u.store.CompleteVectorGCJobs(ctx, jobIDs, now); err != nil {
		u.logError("retention vector gc completion failed", err)
	}
}

// logRecycleResult emits one concise operational log only when the latest recycle pass actually moved rows out of the hot tables.
// logRecycleResult 用于仅在最近一次回收确实搬走热表数据时，输出一条简洁运维日志。
func (u *RetentionUseCase) logRecycleResult(result logicdomain.MemoryRecycleResult) {
	if u == nil || u.logger == nil || result.RecycledMemoryCount == 0 {
		return
	}
	u.logger.Info(
		"retention recycled cold memories",
		"batch_id", result.BatchID,
		"memory_count", result.RecycledMemoryCount,
		"context_count", result.RecycledContextCount,
		"vector_count", len(result.RecycledVectorIDs),
	)
}

// logRecycleJobEnqueueCount emits one concise operational log only when the cold-turn scan actually persisted new recycle jobs.
// logRecycleJobEnqueueCount 用于仅在冷 turn 扫描确实持久化了新回收任务时输出一条简洁运维日志。
func (u *RetentionUseCase) logRecycleJobEnqueueCount(enqueuedJobCount int) {
	if u == nil || u.logger == nil || enqueuedJobCount <= 0 {
		return
	}
	u.logger.Info(
		"retention enqueued cold turn recycle jobs",
		"job_count", enqueuedJobCount,
	)
}

// logColdTurnRecycleResult emits one concise operational log only when one claimed cold-turn recycle batch actually archived old turns into trash.
// logColdTurnRecycleResult 用于仅在已领取的冷 turn 回收任务确实把旧 turn 迁入回收站时输出一条简洁运维日志。
func (u *RetentionUseCase) logColdTurnRecycleResult(batchIDs, sessionIDs []uint64, recycledTurnCount int) {
	if u == nil || u.logger == nil || recycledTurnCount <= 0 {
		return
	}
	u.logger.Info(
		"retention recycled cold turns",
		"batch_count", len(batchIDs),
		"session_count", len(sessionIDs),
		"turn_count", recycledTurnCount,
	)
}

// logIdleSessionRecycleResult emits one concise operational log only when the latest idle-session recycle pass actually moved stale session data out of the hot tables.
// logIdleSessionRecycleResult 用于仅在最近一次 idle-session 回收确实搬走陈旧 session 数据时，输出一条简洁运维日志。
func (u *RetentionUseCase) logIdleSessionRecycleResult(result logicdomain.SessionIdleRecycleResult) {
	if u == nil || u.logger == nil {
		return
	}
	if result.RecycledMemoryCount == 0 && result.RecycledContextCount == 0 && result.RecycledTurnCount == 0 && result.TurnDriftCount == 0 {
		return
	}
	u.logger.Info(
		"retention recycled idle sessions",
		"batch_count", len(result.BatchIDs),
		"session_count", len(result.SessionIDs),
		"memory_count", result.RecycledMemoryCount,
		"context_count", result.RecycledContextCount,
		"turn_count", result.RecycledTurnCount,
		"turn_drift_count", result.TurnDriftCount,
		"vector_count", len(result.RecycledVectorIDs),
	)
}

// logPurgeResult emits one concise operational log only when one purge pass permanently deleted trash rows.
// logPurgeResult 用于仅在某次 purge 真正永久删除了回收站数据时，输出一条简洁运维日志。
func (u *RetentionUseCase) logPurgeResult(result logicdomain.RetentionTrashPurgeResult) {
	if u == nil || u.logger == nil || result.PurgedMemoryCount == 0 && result.PurgedContextCount == 0 && result.PurgedTurnCount == 0 {
		return
	}
	u.logger.Info(
		"retention purged recycle trash",
		"batch_count", len(result.BatchIDs),
		"memory_count", result.PurgedMemoryCount,
		"context_count", result.PurgedContextCount,
		"turn_count", result.PurgedTurnCount,
	)
}

// logError centralizes nil-safe maintenance error logging so worker failures remain observable without crashing the runtime.
// logError 用于集中处理 nil-safe 维护错误日志，让工作器失败可观测但不会拖垮运行时。
func (u *RetentionUseCase) logError(message string, err error) {
	if u == nil || u.logger == nil || err == nil {
		return
	}
	u.logger.Error(message, "err", err)
}

// maintenanceEnabled reports whether the shared maintenance ticker currently has at least one real workload to execute.
// maintenanceEnabled 用于判断共享维护 ticker 当前是否至少承载了一项真实工作负载。
func (u *RetentionUseCase) maintenanceEnabled() bool {
	return u.retentionMaintenanceEnabled() || u.vectorGCMaintenanceEnabled()
}

// retentionMaintenanceEnabled reports whether the classic cold-data governance chain should run on the shared ticker.
// retentionMaintenanceEnabled 用于判断传统冷数据治理链是否应在共享 ticker 上运行。
func (u *RetentionUseCase) retentionMaintenanceEnabled() bool {
	return u != nil && u.store != nil && u.cfg.Enabled
}

// vectorGCMaintenanceEnabled reports whether the persistent vector-delete retry queue should run on the shared ticker regardless of classic recycle-policy switches.
// vectorGCMaintenanceEnabled 用于判断持久化向量删除重试队列是否应独立于经典 recycle 开关，在共享 ticker 上继续运行。
func (u *RetentionUseCase) vectorGCMaintenanceEnabled() bool {
	return u != nil && u.store != nil && u.vector != nil
}

// retentionPriorityFloorValue converts the normalized config token into the numeric durable-memory priority threshold used by storage predicates.
// retentionPriorityFloorValue 用于把规范化后的配置 token 转成存储谓词使用的长期记忆优先级阈值。
func retentionPriorityFloorValue(level string) int {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "P0":
		return logicdomain.MemoryPriorityP0
	case "P2":
		return logicdomain.MemoryPriorityP2
	case "P1", "":
		return logicdomain.MemoryPriorityP1
	default:
		return logicdomain.MemoryPriorityP1
	}
}

// retentionMemoryLevelFloorValue converts the normalized config token into the numeric durable-memory level threshold used by storage predicates.
// retentionMemoryLevelFloorValue 用于把规范化后的配置 token 转成存储谓词使用的长期记忆层级阈值。
func retentionMemoryLevelFloorValue(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session":
		return logicdomain.MemoryLevelSession
	case "phase":
		return logicdomain.MemoryLevelPhase
	case "persistent":
		return logicdomain.MemoryLevelPersistent
	case "stable", "":
		return logicdomain.MemoryLevelStable
	default:
		return logicdomain.MemoryLevelStable
	}
}

// chooseRetentionTimeOrNow keeps retention helpers on one non-zero UTC clock value even when callers intentionally pass zero time in tests or future refactors.
// chooseRetentionTimeOrNow 用于让 retention 辅助逻辑总能拿到一个非零 UTC 时间值，避免测试或后续重构传入零时间时出现异常调度。
func chooseRetentionTimeOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

// retentionVectorGCBatchID keeps retry-queue batch references explicit while still allowing zero when one recycle result aggregated multiple batch sources.
// retentionVectorGCBatchID 用于显式保留重试队列里的批次引用；若结果聚合了多个批次，则允许退回零值批次锚点。
func retentionVectorGCBatchID(batchID uint64) uint64 {
	return batchID
}

// singleBatchIDOrZero returns the concrete batch id only when one aggregate result really contains exactly one batch anchor; multi-batch aggregates intentionally fall back to zero so retry rows do not claim the wrong source batch.
// singleBatchIDOrZero 用于仅在聚合结果确实只包含一个批次锚点时返回该批次 id；多批次聚合会有意退回零值，避免重试行错误归属到某个批次。
func singleBatchIDOrZero(values []uint64) uint64 {
	if len(values) != 1 {
		return 0
	}
	return values[0]
}

// EnsureRetentionStore reports one explicit wiring error when the configured relational store does not expose the retention maintenance port.
// EnsureRetentionStore 用于在关系存储未暴露 retention 维护端口时返回显式装配错误。
func EnsureRetentionStore(store appports.RelationalStore) (appports.RetentionStore, error) {
	if store == nil {
		return nil, fmt.Errorf("relational store is nil")
	}
	retentionStore, ok := store.(appports.RetentionStore)
	if !ok {
		return nil, fmt.Errorf("relational store does not support retention maintenance")
	}
	return retentionStore, nil
}
