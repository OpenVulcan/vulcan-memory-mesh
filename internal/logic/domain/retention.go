// retention.go declares the cold-data retention models shared by maintenance workers and storage adapters.
// retention.go 用于声明冷数据回收工作器与存储适配器共享的 retention 模型。
package domain

import "time"

const (
	// RecycleTypeColdMemory marks one recycle batch that moved terminal durable memories into the trash tables.
	// RecycleTypeColdMemory 用于标记一类回收批次：把终态长期记忆迁入回收站表。
	RecycleTypeColdMemory = "cold_memory_recycle"

	// RecycleReasonColdTerminalMemory keeps the stable batch reason text used by the first retention worker phase.
	// RecycleReasonColdTerminalMemory 用于保存第一阶段 retention 工作器使用的稳定回收原因文本。
	RecycleReasonColdTerminalMemory = "terminal durable memory recycled by retention maintenance"
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

// MemoryTrashPurgeResult stores how many trash rows were permanently removed after the configured retention window elapsed.
// MemoryTrashPurgeResult 用于保存超过保留窗口后被永久删除的回收站行数量。
type MemoryTrashPurgeResult struct {
	BatchIDs           []uint64
	PurgedMemoryCount  int
	PurgedContextCount int
}
