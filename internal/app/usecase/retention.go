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

	// defaultRetentionIdleSessionBatchSize bounds one idle-session recycle pass so the maintenance worker can compact several cold sessions without monopolizing the shared stores.
	// defaultRetentionIdleSessionBatchSize 用于限制单次 idle-session 回收批量，让维护工作器能压缩多个冷 session，同时避免长时间独占共享存储。
	defaultRetentionIdleSessionBatchSize = 32

	// defaultRetentionPurgeBatchSize bounds one trash purge pass so permanent cleanup stays incremental and predictable.
	// defaultRetentionPurgeBatchSize 用于限制单次回收站清理批量，让永久删除流程保持渐进且可预测。
	defaultRetentionPurgeBatchSize = 64
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
	store        appports.RetentionStore
	vector       appports.VectorStore
	cfg          RetentionConfig
	logger       *logx.Logger
	workerCtx    context.Context
	workerCancel context.CancelFunc
	workerWG     sync.WaitGroup
}

// NewRetentionUseCase constructs the cold-data maintenance worker and starts the background loop only when the runtime configuration explicitly enables it.
// NewRetentionUseCase 用于构建冷数据维护工作器，并仅在运行时配置显式启用时启动后台循环。
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

// startWorker boots the background ticker only when retention governance is enabled and the required stores are available.
// startWorker 用于仅在 retention 治理已启用且所需存储可用时启动后台 ticker。
func (u *RetentionUseCase) startWorker() {
	if u == nil || u.store == nil || !u.cfg.Enabled || u.cfg.RecycleScanInterval <= 0 || u.workerCancel != nil {
		return
	}
	u.workerCtx, u.workerCancel = context.WithCancel(context.Background())
	u.workerWG.Add(1)
	go u.workerLoop()
}

// workerLoop performs one eager maintenance pass at startup and then keeps scanning on the configured cadence until shutdown.
// workerLoop 用于在启动后立即执行一次维护，并在后续按照配置周期持续扫描，直到收到关闭信号。
func (u *RetentionUseCase) workerLoop() {
	defer u.workerWG.Done()
	u.runMaintenance(u.workerCtx)

	ticker := time.NewTicker(u.cfg.RecycleScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-u.workerCtx.Done():
			return
		case <-ticker.C:
			u.runMaintenance(u.workerCtx)
		}
	}
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
	} else {
		u.cleanupVectors(ctx, recycleResult)
		u.logRecycleResult(recycleResult)
	}

	// Recycle long-idle sessions only after terminal-memory recycle has already shrunk the obvious cold rows, so the session pass can focus on active-but-expired session facts and orphaned turns.
	// 先完成终态记忆回收，再处理长期空闲 session，让后者聚焦 active 但已失效的 session 事实和无引用旧 turn。
	idleBefore := now.Add(-u.cfg.SessionIdleRecycleAfter)
	sessionResult, sessionErr := u.store.RecycleIdleSessions(ctx, logicdomain.SessionIdleRecycleQuery{
		Limit:             defaultRetentionIdleSessionBatchSize,
		RecycledAt:        now,
		IdleBefore:        idleBefore,
		TurnHotWindowSize: normalizeRetentionTurnHotWindowSize(u.cfg.TurnHotWindowSize),
		RecycleReason:     logicdomain.RecycleReasonIdleSessionCompact,
	})
	if sessionErr != nil {
		u.logError("retention idle session recycle failed", sessionErr)
	} else {
		u.cleanupIdleSessionVectors(ctx, sessionResult)
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

// cleanupVectors removes obsolete vector rows after the relational recycle transaction has succeeded, while tolerating combined-store no-op implementations.
// cleanupVectors 用于在关系回收事务成功后清理过时向量，同时兼容组合库存储下的 no-op 删除实现。
func (u *RetentionUseCase) cleanupVectors(ctx context.Context, result logicdomain.MemoryRecycleResult) {
	if u == nil || u.vector == nil || len(result.RecycledVectorIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := u.vector.DeleteByIDs(ctx, result.RecycledVectorIDs); err != nil {
		u.logError("retention vector cleanup failed", err)
	}
}

// cleanupIdleSessionVectors removes obsolete vector rows after one idle-session recycle pass succeeds, reusing the same best-effort vector deletion bridge as terminal-memory recycle.
// cleanupIdleSessionVectors 用于在一次 idle-session 回收成功后删除过时向量，复用与终态记忆回收相同的 best-effort 向量清理桥接逻辑。
func (u *RetentionUseCase) cleanupIdleSessionVectors(ctx context.Context, result logicdomain.SessionIdleRecycleResult) {
	if u == nil || u.vector == nil || len(result.RecycledVectorIDs) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := u.vector.DeleteByIDs(ctx, result.RecycledVectorIDs); err != nil {
		u.logError("retention idle-session vector cleanup failed", err)
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

// logIdleSessionRecycleResult emits one concise operational log only when the latest idle-session recycle pass actually moved stale session data out of the hot tables.
// logIdleSessionRecycleResult 用于仅在最近一次 idle-session 回收确实搬走陈旧 session 数据时，输出一条简洁运维日志。
func (u *RetentionUseCase) logIdleSessionRecycleResult(result logicdomain.SessionIdleRecycleResult) {
	if u == nil || u.logger == nil {
		return
	}
	if result.RecycledMemoryCount == 0 && result.RecycledContextCount == 0 && result.RecycledTurnCount == 0 {
		return
	}
	u.logger.Info(
		"retention recycled idle sessions",
		"batch_count", len(result.BatchIDs),
		"session_count", len(result.SessionIDs),
		"memory_count", result.RecycledMemoryCount,
		"context_count", result.RecycledContextCount,
		"turn_count", result.RecycledTurnCount,
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

// normalizeRetentionTurnHotWindowSize keeps the runtime hot-window size non-negative even when composition code passes an invalid value.
// normalizeRetentionTurnHotWindowSize 用于在组合根传入非法值时，仍把运行时 turn 热窗口保持为非负数。
func normalizeRetentionTurnHotWindowSize(size int) int {
	if size > 0 {
		return size
	}
	return 0
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
