// retention_delegation.go delegates RetentionStore interface methods from *Store to the internal retentionRepository and vectorRepository so the combined PostgreSQL store satisfies the retention port.
// retention_delegation.go 用于把 RetentionStore 接口方法从 *Store 委托到内部的 retentionRepository 与 vectorRepository，使组合 PostgreSQL 存储满足 retention 端口契约。
package vldb_postgres

import (
	"context"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Compile-time interface compliance assertions so future refactors cannot accidentally drop delegation methods.
// 编译期接口实现断言，防止未来重构意外丢失委托方法。
var (
	_ appports.RetentionStore                   = (*Store)(nil)
	_ appports.MemoryVectorDimensionMigrationStore = (*Store)(nil)
)

// RecycleColdMemories delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// RecycleColdMemories 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) RecycleColdMemories(ctx context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	return s.repos.retention.RecycleColdMemories(ctx, query)
}

// RecycleIdleSessions delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// RecycleIdleSessions 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) RecycleIdleSessions(ctx context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error) {
	return s.repos.retention.RecycleIdleSessions(ctx, query)
}

// PurgeExpiredTrash delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// PurgeExpiredTrash 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) PurgeExpiredTrash(ctx context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error) {
	return s.repos.retention.PurgeExpiredTrash(ctx, before, limit)
}

// EnqueueColdTurnRecycleJobs delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// EnqueueColdTurnRecycleJobs 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) EnqueueColdTurnRecycleJobs(ctx context.Context, query logicdomain.ColdTurnRecycleJobEnqueueQuery) (int, error) {
	return s.repos.retention.EnqueueColdTurnRecycleJobs(ctx, query)
}

// ClaimPendingRecycleJobs delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// ClaimPendingRecycleJobs 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) ClaimPendingRecycleJobs(ctx context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error) {
	return s.repos.retention.ClaimPendingRecycleJobs(ctx, jobType, dueBefore, claimUntil, limit)
}

// RecycleColdTurns delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// RecycleColdTurns 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) RecycleColdTurns(ctx context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error) {
	return s.repos.retention.RecycleColdTurns(ctx, query)
}

// CompleteRecycleJobs delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// CompleteRecycleJobs 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) CompleteRecycleJobs(ctx context.Context, jobIDs []uint64, completedAt time.Time) error {
	return s.repos.retention.CompleteRecycleJobs(ctx, jobIDs, completedAt)
}

// RetryRecycleJobs delegates to the internal retention repository so the combined PostgreSQL store satisfies the RetentionStore port.
// RetryRecycleJobs 委托给内部 retention 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) RetryRecycleJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	return s.repos.retention.RetryRecycleJobs(ctx, jobIDs, nextRunAt, lastError)
}

// EnqueueVectorGCJobs delegates to the internal vector repository so the combined PostgreSQL store satisfies the RetentionStore port.
// EnqueueVectorGCJobs 委托给内部 vector 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	return s.repos.vector.EnqueueVectorGCJobs(ctx, query)
}

// ClaimPendingVectorGCJobs delegates to the internal vector repository so the combined PostgreSQL store satisfies the RetentionStore port.
// ClaimPendingVectorGCJobs 委托给内部 vector 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) ClaimPendingVectorGCJobs(ctx context.Context, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.VectorGCJobRecord, error) {
	return s.repos.vector.ClaimPendingVectorGCJobs(ctx, dueBefore, claimUntil, limit)
}

// CompleteVectorGCJobs delegates to the internal vector repository so the combined PostgreSQL store satisfies the RetentionStore port.
// CompleteVectorGCJobs 委托给内部 vector 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) CompleteVectorGCJobs(ctx context.Context, jobIDs []uint64, completedAt time.Time) error {
	return s.repos.vector.CompleteVectorGCJobs(ctx, jobIDs, completedAt)
}

// RetryVectorGCJobs delegates to the internal vector repository so the combined PostgreSQL store satisfies the RetentionStore port.
// RetryVectorGCJobs 委托给内部 vector 仓库，使组合 PostgreSQL 存储满足 RetentionStore 端口。
func (s *Store) RetryVectorGCJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	return s.repos.vector.RetryVectorGCJobs(ctx, jobIDs, nextRunAt, lastError)
}

// RebuildMemoryVectorDimensions delegates to the internal vector repository so the combined PostgreSQL store satisfies the MemoryVectorDimensionMigrationStore port.
// RebuildMemoryVectorDimensions 委托给内部 vector 仓库，使组合 PostgreSQL 存储满足 MemoryVectorDimensionMigrationStore 端口。
func (s *Store) RebuildMemoryVectorDimensions(ctx context.Context, records []logicdomain.MemoryRecord) error {
	return s.repos.vector.RebuildMemoryVectorDimensions(ctx, records)
}
