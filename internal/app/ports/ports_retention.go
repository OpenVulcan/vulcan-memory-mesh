// ports_retention.go keeps cold-data governance contracts used by retention and background recycle workers.
// ports_retention.go 用于承载 retention 与后台回收工作器依赖的冷数据治理契约。
package ports

import (
	"context"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// RetentionStore is the cold-data governance port used by background maintenance workers to move terminal durable memories into trash tables, compact long-idle sessions, and purge expired trash batches.
// RetentionStore 用于抽象冷数据治理端口，让后台维护工作器可以把终态长期记忆迁入回收站、压缩长期空闲 session，并清理超过保留窗口的回收批次。
type RetentionStore interface {
	RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error)
	EnqueueColdTurnRecycleJobs(ctx context.Context, query logicdomain.ColdTurnRecycleJobEnqueueQuery) (int, error)
	ClaimPendingRecycleJobs(ctx context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error)
	RecycleColdTurns(ctx context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error)
	CompleteRecycleJobs(ctx context.Context, jobIDs []uint64, completedAt time.Time) error
	RetryRecycleJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error
	RecycleIdleSessions(ctx context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error)
	PurgeExpiredTrash(ctx context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error)
	EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error
	ClaimPendingVectorGCJobs(ctx context.Context, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.VectorGCJobRecord, error)
	CompleteVectorGCJobs(ctx context.Context, jobIDs []uint64, completedAt time.Time) error
	RetryVectorGCJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error
}
