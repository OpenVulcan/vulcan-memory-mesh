// retention.go declares the cold-data retention models shared by maintenance workers and storage adapters.
// retention.go 用于声明冷数据回收工作器与存储适配器共享的 retention 模型。
package domain

import "time"

const (
	// RecycleTypeColdMemory marks one recycle batch that moved terminal durable memories into the trash tables.
	// RecycleTypeColdMemory 用于标记一类回收批次：把终态长期记忆迁入回收站表。
	RecycleTypeColdMemory = "cold_memory_recycle"

	// RecycleTypeSessionIdle marks one recycle batch that compacted one long-idle session by removing stale session memories and archiving orphaned cold turns.
	// RecycleTypeSessionIdle 用于标记一类回收批次：对长期空闲 session 做压缩，移出陈旧 session 记忆并归档无引用旧 turn。
	RecycleTypeSessionIdle = "session_idle_recycle"

	// RecycleReasonColdTerminalMemory keeps the stable batch reason text used by the first retention worker phase.
	// RecycleReasonColdTerminalMemory 用于保存第一阶段 retention 工作器使用的稳定回收原因文本。
	RecycleReasonColdTerminalMemory = "terminal durable memory recycled by retention maintenance"

	// RecycleReasonIdleSessionCompact keeps the stable batch reason text used when one idle session is compacted by retention maintenance.
	// RecycleReasonIdleSessionCompact 用于保存 retention 维护器压缩长期空闲 session 时使用的稳定回收原因文本。
	RecycleReasonIdleSessionCompact = "idle session compacted by retention maintenance"

	// VectorGCJobTypeRetentionRecycle keeps the stable vector-GC job type used when retention must retry a failed sidecar vector delete after relational recycle already committed.
	// VectorGCJobTypeRetentionRecycle 用于保存 retention 在关系回收已提交后重试失败向量删除时使用的稳定向量 GC 任务类型。
	VectorGCJobTypeRetentionRecycle = "retention_recycle_vector_delete"
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
	RecycledVectorIDs    []string
}

// RetentionTrashPurgeResult stores how many trash rows were permanently removed after the configured retention window elapsed.
// RetentionTrashPurgeResult 用于保存超过保留窗口后被永久删除的回收站行数量。
type RetentionTrashPurgeResult struct {
	BatchIDs           []uint64
	PurgedMemoryCount  int
	PurgedContextCount int
	PurgedTurnCount    int
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
