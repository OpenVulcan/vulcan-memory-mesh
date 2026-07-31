// retention.go declares the cold-data retention models shared by maintenance workers and storage adapters.
// retention.go 用于声明冷数据回收工作器与存储适配器共享的 retention 模型。
package domain

import "time"

const (
	// RecycleTypeColdMemory marks one recycle batch that moved terminal durable memories into the trash tables.
	// RecycleTypeColdMemory 用于标记一类回收批次：把终态长期记忆迁入回收站表。
	RecycleTypeColdMemory = "cold_memory_recycle"

	// RecycleTypeColdTurn marks one recycle batch that moved old unreferenced turns into the turn-trash table.
	// RecycleTypeColdTurn 用于标记一类回收批次：把超出热窗口且无引用的旧 turn 迁入 turn 回收站表。
	RecycleTypeColdTurn = "cold_turn_recycle"

	// RecycleTypeSessionIdle marks one recycle batch that compacted one long-idle session by removing stale session memories and archiving orphaned cold turns.
	// RecycleTypeSessionIdle 用于标记一类回收批次：对长期空闲 session 做压缩，移出陈旧 session 记忆并归档无引用旧 turn。
	RecycleTypeSessionIdle = "session_idle_recycle"

	// RecycleReasonColdTerminalMemory keeps the stable batch reason text used by the first retention worker phase.
	// RecycleReasonColdTerminalMemory 用于保存第一阶段 retention 工作器使用的稳定回收原因文本。
	RecycleReasonColdTerminalMemory = "terminal durable memory recycled by retention maintenance"

	// RecycleReasonColdTurnArchive keeps the stable batch reason text used when retention archives old turns outside the hot window.
	// RecycleReasonColdTurnArchive 用于保存 retention 归档热窗口外旧 turn 时使用的稳定回收原因文本。
	RecycleReasonColdTurnArchive = "cold turns archived by retention maintenance"

	// RecycleReasonIdleSessionCompact keeps the stable batch reason text used when one idle session is compacted by retention maintenance.
	// RecycleReasonIdleSessionCompact 用于保存 retention 维护器压缩长期空闲 session 时使用的稳定回收原因文本。
	RecycleReasonIdleSessionCompact = "idle session compacted by retention maintenance"

	// RecycleJobTypeColdTurn keeps the stable recycle-job type used by the independent cold-turn scan/claim/execute pipeline.
	// RecycleJobTypeColdTurn 用于保存独立冷 turn 扫描/领取/执行链路使用的稳定回收任务类型。
	RecycleJobTypeColdTurn = "cold_turn_recycle_job"

	// VectorGCJobTypeRetentionRecycle keeps the stable vector-GC job type used when retention must retry a failed sidecar vector delete after relational recycle already committed.
	// VectorGCJobTypeRetentionRecycle 用于保存 retention 在关系回收已提交后重试失败向量删除时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeRetentionRecycle = "retention_recycle_vector_delete"

	// VectorGCJobTypeTurnAnalysisRollback keeps the stable vector-GC job type used when post-action must compensate a failed rollback after vector persistence succeeded but relational analysis apply failed.
	// VectorGCJobTypeTurnAnalysisRollback 用于保存 post-action 在向量已落库但关系分析写入失败后，补偿失败回滚时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeTurnAnalysisRollback = "turn_analysis_vector_rollback"

	// VectorGCJobTypeTurnAnalysisSupersedeCleanup keeps the stable vector-GC job type used when post-action committed relational supersede changes but the obsolete sidecar vectors could not be deleted immediately.
	// VectorGCJobTypeTurnAnalysisSupersedeCleanup 用于保存 post-action 已提交关系 supersede 变更、但旧旁路向量无法立即删除时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeTurnAnalysisSupersedeCleanup = "turn_analysis_superseded_vector_delete"

	// VectorGCJobTypeDirectWriteRollback keeps the stable vector-GC job type used when one direct memory write must compensate a failed rollback after vector persistence succeeded but relational persistence failed.
	// VectorGCJobTypeDirectWriteRollback 用于保存主动写记忆在向量已落库但关系持久化失败后，补偿失败回滚时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeDirectWriteRollback = "direct_write_vector_rollback"

	// VectorGCJobTypeDirectWriteSupersedeCleanup keeps the stable vector-GC job type used when one direct memory write committed supersede changes but could not delete the obsolete vectors immediately.
	// VectorGCJobTypeDirectWriteSupersedeCleanup 用于保存主动写记忆已提交 supersede 变更、但无法立即删除旧向量时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeDirectWriteSupersedeCleanup = "direct_write_superseded_vector_delete"

	// VectorGCJobTypeManualMemoryDelete keeps the stable vector-GC job type used when a manual memory delete committed relational removal but could not delete sidecar vectors immediately.
	// VectorGCJobTypeManualMemoryDelete 用于保存手工删除记忆已提交关系侧移除、但无法立即删除旁路向量时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeManualMemoryDelete = "manual_memory_delete_vector_delete"

	// VectorGCJobTypeManagementPurge keeps the stable vector-GC job type used after a restorable management batch is permanently purged.
	// VectorGCJobTypeManagementPurge 用于保存可恢复管理批次被永久清理后使用的稳定向量 GC 任务类型。
	VectorGCJobTypeManagementPurge = "management_purge_vector_delete"
)

// MemoryRecycleQuery describes one cold-memory recycle pass, including batch size, timestamps, and protection knobs.
// MemoryRecycleQuery 用于描述一次冷状态记忆回收扫描，包括批量大小、时间戳与保护参数。
type MemoryRecycleQuery struct {
	Limit                       int
	RecycledAt                  time.Time
	RecycleReason               string
	ProtectPriorityFloor        int
	ProtectMemoryLevelFloor     int
	SkipProtectedSharedMemories bool
}

// MemoryRecycleResult stores the recycled-memory counts and obsolete vector identifiers produced by one recycle batch.
// MemoryRecycleResult 用于保存一次回收批次产出的记忆计数，以及后续需要清理的旧向量标识。
type MemoryRecycleResult struct {
	BatchID              uint64
	RecycledMemoryCount  int
	RecycledContextCount int
	RecycledVectorIDs    []string
}

// SessionIdleRecycleQuery describes one idle-session recycle scan, including the idle cutoff and the turn hot-window size that must stay in the hot table.
// SessionIdleRecycleQuery 用于描述一次 idle-session 回收扫描，包括空闲截止时间，以及必须继续留在热表中的 turn 热窗口大小。
type SessionIdleRecycleQuery struct {
	Limit             int
	RecycledAt        time.Time
	IdleBefore        time.Time
	TurnHotWindowSize int
	RecycleReason     string
}

// SessionIdleRecycleResult stores the aggregate batch/session identifiers plus the recycled stale-memory, contextual-edge, turn, and vector counts produced by one idle-session recycle pass.
// SessionIdleRecycleResult 用于保存一次 idle-session 回收扫描产出的批次/会话标识，以及被移出的陈旧记忆、情境边、turn 和向量统计。
type SessionIdleRecycleResult struct {
	BatchIDs             []uint64
	SessionIDs           []uint64
	RecycledMemoryCount  int
	RecycledContextCount int
	RecycledTurnCount    int
	// TurnDriftCount records selected idle-session turns whose trash copy or hot-table delete was not fully confirmed so maintenance logs can expose retryable archive drift.
	// TurnDriftCount 记录已选中但 trash 复制或热表删除未完全确认的 idle-session turn 数量，让维护日志可以暴露可重试归档漂移。
	TurnDriftCount    int
	RecycledVectorIDs []string
}

// ColdTurnRecycleJobEnqueueQuery describes one scan pass that should enqueue bounded cold-turn recycle jobs for sessions that currently expose recyclable turns.
// ColdTurnRecycleJobEnqueueQuery 用于描述一次扫描任务，让系统为当前存在可回收旧 turn 的 session 入队一批有界的冷 turn 回收任务。
type ColdTurnRecycleJobEnqueueQuery struct {
	Limit             int
	ScannedAt         time.Time
	NextRunAt         time.Time
	TurnHotWindowSize int
}

// ColdTurnRecycleQuery describes one execution pass for a claimed cold-turn recycle job.
// ColdTurnRecycleQuery 用于描述一条已领取冷 turn 回收任务的执行参数。
type ColdTurnRecycleQuery struct {
	SessionID         uint64
	RecycledAt        time.Time
	TurnHotWindowSize int
	RecycleReason     string
}

// ColdTurnRecycleResult stores the concrete recycle batch and old-turn count produced when one claimed cold-turn job is executed.
// ColdTurnRecycleResult 用于保存执行一条已领取冷 turn 回收任务后产生的具体回收批次与旧 turn 数量。
type ColdTurnRecycleResult struct {
	BatchID           uint64
	SessionID         uint64
	ProjectID         uint64
	RecycledTurnCount int
}

// RetentionTrashPurgeResult stores how many trash rows were permanently removed after the configured retention window elapsed.
// RetentionTrashPurgeResult 用于保存超过保留窗口后被永久删除的回收站行数量。
type RetentionTrashPurgeResult struct {
	BatchIDs           []uint64
	PurgedMemoryCount  int
	PurgedContextCount int
	PurgedTurnCount    int
}

// RecycleJobRecord stores one leased retention recycle job selected from the persistent cold-turn queue.
// RecycleJobRecord 用于保存一条从持久化冷 turn 队列里领取出来的 retention 回收任务。
type RecycleJobRecord struct {
	ID           uint64
	SessionID    uint64
	ProjectID    uint64
	JobType      string
	AttemptCount int
	NextRunAt    time.Time
	ClaimedAt    time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// VectorGCJobEnqueueQuery describes one batch of vector ids that should be retried asynchronously after a best-effort delete failed.
// VectorGCJobEnqueueQuery 用于描述一批在 best-effort 删除失败后需要异步重试的向量 id。
type VectorGCJobEnqueueQuery struct {
	BatchID   uint64
	JobType   string
	VectorIDs []string
	NextRunAt time.Time
}

// VectorGCJobRecord stores one leased vector-GC retry task selected from the persistent retry queue.
// VectorGCJobRecord 用于保存一条从持久化重试队列中领取出来的向量 GC 重试任务。
type VectorGCJobRecord struct {
	ID           uint64
	BatchID      uint64
	VectorID     string
	JobType      string
	AttemptCount int
	NextRunAt    time.Time
	ClaimedAt    time.Time
	CompletedAt  time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
