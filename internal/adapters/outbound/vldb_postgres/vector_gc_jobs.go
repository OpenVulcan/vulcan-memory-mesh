// vector_gc_jobs.go implements PostgreSQL-backed vector-GC retry queue helpers used when retention must compensate failed sidecar vector deletes without reopening the relational recycle transaction.
// vector_gc_jobs.go 用于实现 PostgreSQL 的向量 GC 重试队列辅助逻辑，服务于 retention 在不重新打开关系回收事务的前提下补偿失败的旁路向量删除。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postgresVectorGCJobRow stores one PostgreSQL vector-GC retry row before it is converted into the shared retention domain model.
// postgresVectorGCJobRow 用于保存一条 PostgreSQL 向量 GC 重试行，在转换为共享 retention 领域模型前使用。
type postgresVectorGCJobRow struct {
	ID           uint64
	BatchID      uint64
	VectorID     string
	JobType      string
	AttemptCount int
	NextRunAt    time.Time
	ClaimedAt    *time.Time
	CompletedAt  *time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// toDomain converts one PostgreSQL retry row into the shared vector-GC job record used by the retention worker.
// toDomain 用于把一条 PostgreSQL 重试行转换成 retention 工作器使用的共享向量 GC 任务记录。
func (r postgresVectorGCJobRow) toDomain() logicdomain.VectorGCJobRecord {
	job := logicdomain.VectorGCJobRecord{
		ID:           r.ID,
		BatchID:      r.BatchID,
		VectorID:     strings.TrimSpace(r.VectorID),
		JobType:      strings.TrimSpace(r.JobType),
		AttemptCount: r.AttemptCount,
		NextRunAt:    r.NextRunAt.UTC(),
		LastError:    strings.TrimSpace(r.LastError),
		CreatedAt:    r.CreatedAt.UTC(),
		UpdatedAt:    r.UpdatedAt.UTC(),
	}
	if r.ClaimedAt != nil {
		job.ClaimedAt = r.ClaimedAt.UTC()
	}
	if r.CompletedAt != nil {
		job.CompletedAt = r.CompletedAt.UTC()
	}
	return job
}

// EnqueueVectorGCJobs persists failed sidecar vector deletes into the PostgreSQL retry queue so later maintenance passes can compensate transient vector-store failures.
// EnqueueVectorGCJobs 用于把失败的旁路向量删除持久化到 PostgreSQL 重试队列，让后续维护轮次可以补偿瞬时向量库故障。
func (r *vectorRepository) EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	vectorIDs := storageutil.NormalizeStringList(query.VectorIDs)
	if len(vectorIDs) == 0 {
		return nil
	}
	jobType := strings.TrimSpace(query.JobType)
	if jobType == "" {
		jobType = logicdomain.VectorGCJobTypeRetentionRecycle
	}
	now := time.Now().UTC()
	nextRunAt := chooseNonZeroTime(query.NextRunAt, now)

	args := &sqlArgsBuilder{}
	valueClauses := make([]string, 0, len(vectorIDs))
	for _, vectorID := range vectorIDs {
		valueClauses = append(valueClauses, fmt.Sprintf("(%s, %s, %s, 0, %s, NULL, NULL, '', %s, %s)",
			args.Add(int64(query.BatchID)),
			args.Add(vectorID),
			args.Add(jobType),
			args.Add(nextRunAt),
			args.Add(now),
			args.Add(now),
		))
	}
	sqlText := fmt.Sprintf(`
INSERT INTO %s (
  batch_id, vector_id, job_type, attempt_count, next_run_at,
  claimed_at, completed_at, last_error, created_at, updated_at
)
VALUES %s
ON CONFLICT (vector_id, job_type, batch_id) DO NOTHING
`, r.vectorGCJobsTable(), strings.Join(valueClauses, ",\n"))

	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	if _, err := r.shared.pool.Exec(callCtx, strings.TrimSpace(sqlText), args.Args()...); err != nil {
		return fmt.Errorf("enqueue postgres vector gc jobs: %w", err)
	}
	return nil
}

// ClaimPendingVectorGCJobs leases one bounded PostgreSQL retry batch with SKIP LOCKED so concurrent maintenance workers cannot consume the same vector-delete work twice.
// ClaimPendingVectorGCJobs 用于通过 SKIP LOCKED 领取一批有界的 PostgreSQL 重试任务，避免并发维护工作器重复消费同一批向量删除工作。
func (r *vectorRepository) ClaimPendingVectorGCJobs(ctx context.Context, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.VectorGCJobRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	limit = normalizeRetentionBatchLimit(limit, 128)
	dueBefore = chooseNonZeroTime(dueBefore, time.Now().UTC())
	claimAt := time.Now().UTC()
	claimUntil = chooseNonZeroTime(claimUntil, claimAt)

	sqlText := fmt.Sprintf(`
WITH claimed AS (
  SELECT id
  FROM %s
  WHERE completed_at IS NULL
    AND next_run_at <= $1
  ORDER BY next_run_at ASC, id ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE %s AS jobs
SET claimed_at = $3,
    next_run_at = $4,
    updated_at = $3
FROM claimed
WHERE jobs.id = claimed.id
RETURNING jobs.id, jobs.batch_id, jobs.vector_id, jobs.job_type, jobs.attempt_count,
          jobs.next_run_at, jobs.claimed_at, jobs.completed_at, jobs.last_error,
          jobs.created_at, jobs.updated_at
`, r.vectorGCJobsTable(), r.vectorGCJobsTable())

	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres vector gc claim tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	rows, err := tx.Query(callCtx, strings.TrimSpace(sqlText), dueBefore, limit, claimAt, claimUntil)
	if err != nil {
		return nil, fmt.Errorf("claim postgres vector gc jobs: %w", err)
	}
	defer rows.Close()

	claimed := make([]logicdomain.VectorGCJobRecord, 0)
	for rows.Next() {
		var row postgresVectorGCJobRow
		if err := rows.Scan(
			&row.ID,
			&row.BatchID,
			&row.VectorID,
			&row.JobType,
			&row.AttemptCount,
			&row.NextRunAt,
			&row.ClaimedAt,
			&row.CompletedAt,
			&row.LastError,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres vector gc job: %w", err)
		}
		claimed = append(claimed, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres vector gc jobs: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return nil, postgresClaimCommitError("claim vector gc jobs", "commit postgres vector gc claim tx", len(claimed), err)
	}
	return claimed, nil
}

// CompleteVectorGCJobs removes one PostgreSQL retry batch immediately after the sidecar delete succeeds, keeping the queue strictly transient instead of retaining completed bookkeeping rows forever.
// CompleteVectorGCJobs 用于在旁路删除成功后立即删除一批 PostgreSQL 重试任务，让该队列严格保持瞬时补偿语义，而不是永久保留已完成台账。
func (r *vectorRepository) CompleteVectorGCJobs(ctx context.Context, jobIDs []uint64, _ time.Time) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	jobIDs = storageutil.NormalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	sqlText := buildPostgresDeleteCompletedVectorGCJobsSQL(r.vectorGCJobsTable())

	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres vector gc completion tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	tag, err := tx.Exec(callCtx, strings.TrimSpace(sqlText), toInt64List(jobIDs))
	if err != nil {
		return fmt.Errorf("complete postgres vector gc jobs: %w", err)
	}
	if err := requirePostgresVectorGCJobRowsAffected("complete postgres vector gc jobs", tag.RowsAffected(), len(jobIDs)); err != nil {
		return err
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresCommitOutcomeUncertainError("complete vector gc jobs", "commit postgres vector gc completion tx", err)
	}
	return nil
}

// buildPostgresDeleteCompletedVectorGCJobsSQL returns the single-table delete used once one claimed retry batch has successfully removed its vectors, so completed queue rows cannot accumulate indefinitely.
// buildPostgresDeleteCompletedVectorGCJobsSQL 用于返回成功删除向量后清理队列行的单表删除 SQL，避免已完成的队列行无限累积。
func buildPostgresDeleteCompletedVectorGCJobsSQL(table string) string {
	return fmt.Sprintf(`
DELETE FROM %s
WHERE id = ANY($1)
`, table)
}

// RetryVectorGCJobs reschedules one PostgreSQL retry batch after the sidecar delete still fails, incrementing attempts while releasing the current lease.
// RetryVectorGCJobs 用于在旁路删除仍然失败时重新调度一批 PostgreSQL 重试任务，同时递增尝试次数并释放当前租约。
func (r *vectorRepository) RetryVectorGCJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	jobIDs = storageutil.NormalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	nextRunAt = chooseNonZeroTime(nextRunAt, now)
	lastError = strings.TrimSpace(lastError)

	sqlText := fmt.Sprintf(`
UPDATE %s
SET attempt_count = attempt_count + 1,
    next_run_at = $2,
    claimed_at = NULL,
    last_error = $3,
    updated_at = $4
WHERE id = ANY($1)
  AND completed_at IS NULL
`, r.vectorGCJobsTable())

	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres vector gc retry tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	tag, err := tx.Exec(callCtx, strings.TrimSpace(sqlText), toInt64List(jobIDs), nextRunAt, lastError, now)
	if err != nil {
		return fmt.Errorf("retry postgres vector gc jobs: %w", err)
	}
	if err := requirePostgresVectorGCJobRowsAffected("retry postgres vector gc jobs", tag.RowsAffected(), len(jobIDs)); err != nil {
		return err
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresCommitOutcomeUncertainError("retry vector gc jobs", "commit postgres vector gc retry tx", err)
	}
	return nil
}

// requirePostgresVectorGCJobRowsAffected rejects queue state drift when a terminal vector-GC update did not touch every normalized job id.
// requirePostgresVectorGCJobRowsAffected 用于在 vector-GC 终态更新未命中全部归一化 job id 时拒绝队列状态漂移。
func requirePostgresVectorGCJobRowsAffected(messagePrefix string, rowsAffected int64, expectedRows int) error {
	return postgresRowsAffectedDriftError(messagePrefix, rowsAffected, int64(expectedRows))
}
