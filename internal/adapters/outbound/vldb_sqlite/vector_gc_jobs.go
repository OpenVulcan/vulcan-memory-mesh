// vector_gc_jobs.go implements SQLite-backed vector-GC retry queue helpers used by retention maintenance when sidecar vector deletes fail after relational recycle already committed.
// vector_gc_jobs.go 用于实现 SQLite 的向量 GC 重试队列辅助逻辑，服务于 retention 在关系回收已提交后补偿旁路向量删除失败。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// sqliteVectorGCJobRow stores one SQLite vector-GC job row before it is converted into the shared retention domain shape.
// sqliteVectorGCJobRow 用于保存一条 SQLite 向量 GC 任务行，在转换为共享 retention 领域模型前使用。
type sqliteVectorGCJobRow struct {
	ID                 uint64 `json:"id"`
	BatchID            uint64 `json:"batch_id"`
	VectorID           string `json:"vector_id"`
	JobType            string `json:"job_type"`
	AttemptCount       int    `json:"attempt_count"`
	NextRunTimestamp   int64  `json:"next_run_timestamp"`
	ClaimedTimestamp   int64  `json:"claimed_timestamp"`
	CompletedTimestamp int64  `json:"completed_timestamp"`
	LastError          string `json:"last_error"`
	CreatedTimestamp   int64  `json:"created_timestamp"`
	UpdatedTimestamp   int64  `json:"updated_timestamp"`
}

// toDomain converts one SQLite retry row into the shared retention vector-GC job record so the usecase can stay storage-agnostic.
// toDomain 用于把一条 SQLite 重试队列行转换为共享 retention 向量 GC 任务记录，让用例层保持与存储无关。
func (r sqliteVectorGCJobRow) toDomain() logicdomain.VectorGCJobRecord {
	return logicdomain.VectorGCJobRecord{
		ID:           r.ID,
		BatchID:      r.BatchID,
		VectorID:     strings.TrimSpace(r.VectorID),
		JobType:      strings.TrimSpace(r.JobType),
		AttemptCount: r.AttemptCount,
		NextRunAt:    sqliteTimestampToTime(r.NextRunTimestamp),
		ClaimedAt:    sqliteTimestampToTime(r.ClaimedTimestamp),
		CompletedAt:  sqliteTimestampToTime(r.CompletedTimestamp),
		LastError:    strings.TrimSpace(r.LastError),
		CreatedAt:    sqliteTimestampToTime(r.CreatedTimestamp),
		UpdatedAt:    sqliteTimestampToTime(r.UpdatedTimestamp),
	}
}

// EnqueueVectorGCJobs persists failed sidecar vector deletes into the SQLite retry queue so later maintenance passes can compensate transient vector-store failures.
// EnqueueVectorGCJobs 用于把失败的旁路向量删除持久化到 SQLite 重试队列，让后续维护轮次可以补偿瞬时向量库故障。
func (s *Store) EnqueueVectorGCJobs(ctx context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	vectorIDs := normalizeStringList(query.VectorIDs)
	if len(vectorIDs) == 0 {
		return nil
	}
	jobType := strings.TrimSpace(query.JobType)
	if jobType == "" {
		jobType = logicdomain.VectorGCJobTypeRetentionRecycle
	}
	nowMs := time.Now().UTC().UnixMilli()
	nextRunMs := normalizeSQLiteRecycleTime(query.NextRunAt).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_vector_gc_jobs")
	if err != nil {
		return fmt.Errorf("allocate sqlite vector gc job id: %w", err)
	}
	script := strings.Builder{}
	script.WriteString("BEGIN IMMEDIATE;\n")
	for _, vectorID := range vectorIDs {
		script.WriteString(fmt.Sprintf(`
INSERT OR IGNORE INTO vmm_vector_gc_jobs (
  id, batch_id, vector_id, job_type, attempt_count, next_run_timestamp,
  claimed_timestamp, completed_timestamp, last_error, created_timestamp, updated_timestamp
)
VALUES (%d, %d, %s, %s, 0, %d, 0, 0, '', %d, %d);
`, nextID, query.BatchID, sqlStringLiteral(vectorID), sqlStringLiteral(jobType), nextRunMs, nowMs, nowMs))
		nextID++
	}
	script.WriteString("COMMIT;")
	if err := s.exec(ctx, script.String()); err != nil {
		return fmt.Errorf("enqueue sqlite vector gc jobs: %w", err)
	}
	return nil
}

// ClaimPendingVectorGCJobs leases one bounded SQLite retry batch by moving due jobs to the next-run lease horizon, so crashed workers eventually release the claim automatically.
// ClaimPendingVectorGCJobs 用于通过把到期任务推进到下一次运行租约窗口来领取一批有界的 SQLite 重试任务，从而在工作器崩溃后自动释放领取状态。
func (s *Store) ClaimPendingVectorGCJobs(ctx context.Context, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.VectorGCJobRecord, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	limit = normalizeSQLiteRetentionBatchLimit(limit, 128)
	dueBeforeMs := normalizeSQLiteRecycleTime(dueBefore).UnixMilli()
	claimAtMs := time.Now().UTC().UnixMilli()
	claimUntilMs := normalizeSQLiteRecycleTime(claimUntil).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[sqliteVectorGCJobRow](s, ctx, `
SELECT id, batch_id, vector_id, job_type, attempt_count,
       next_run_timestamp, claimed_timestamp, completed_timestamp,
       last_error, created_timestamp, updated_timestamp
FROM vmm_vector_gc_jobs
WHERE completed_timestamp = 0
  AND next_run_timestamp > 0
  AND next_run_timestamp <= ?
ORDER BY next_run_timestamp ASC, id ASC
LIMIT ?
`, dueBeforeMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query sqlite pending vector gc jobs: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	jobIDs := make([]uint64, 0, len(rows))
	for _, row := range rows {
		jobIDs = append(jobIDs, row.ID)
	}
	jobIDList := sqlUint64List(jobIDs)
	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
UPDATE vmm_vector_gc_jobs
SET claimed_timestamp = %d,
    next_run_timestamp = %d,
    updated_timestamp = %d
WHERE id IN (%s)
  AND completed_timestamp = 0;
COMMIT;
`, claimAtMs, claimUntilMs, claimAtMs, jobIDList)
	if err := s.exec(ctx, script); err != nil {
		return nil, fmt.Errorf("claim sqlite vector gc jobs: %w", err)
	}
	claimed := make([]logicdomain.VectorGCJobRecord, 0, len(rows))
	for _, row := range rows {
		row.ClaimedTimestamp = claimAtMs
		row.NextRunTimestamp = claimUntilMs
		row.UpdatedTimestamp = claimAtMs
		claimed = append(claimed, row.toDomain())
	}
	return claimed, nil
}

// CompleteVectorGCJobs marks one SQLite retry batch as finished after the sidecar delete succeeds, keeping the completed rows for auditability and unique-idempotency.
// CompleteVectorGCJobs 用于在旁路删除成功后把一批 SQLite 重试任务标记为已完成，同时保留完成行以便审计和唯一幂等。
func (s *Store) CompleteVectorGCJobs(ctx context.Context, jobIDs []uint64, completedAt time.Time) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	jobIDs = normalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	completedMs := normalizeSQLiteRecycleTime(completedAt).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
UPDATE vmm_vector_gc_jobs
SET completed_timestamp = %d,
    claimed_timestamp = 0,
    last_error = '',
    updated_timestamp = %d
WHERE id IN (%s);
COMMIT;
`, completedMs, completedMs, sqlUint64List(jobIDs))
	if err := s.exec(ctx, script); err != nil {
		return fmt.Errorf("complete sqlite vector gc jobs: %w", err)
	}
	return nil
}

// RetryVectorGCJobs reschedules one SQLite retry batch after the sidecar delete still fails, incrementing attempts while releasing the current lease.
// RetryVectorGCJobs 用于在旁路删除仍然失败时重新调度一批 SQLite 重试任务，同时递增尝试计数并释放当前租约。
func (s *Store) RetryVectorGCJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	jobIDs = normalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	nextRunMs := normalizeSQLiteRecycleTime(nextRunAt).UnixMilli()
	lastError = strings.TrimSpace(lastError)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
UPDATE vmm_vector_gc_jobs
SET attempt_count = attempt_count + 1,
    next_run_timestamp = %d,
    claimed_timestamp = 0,
    last_error = %s,
    updated_timestamp = %d
WHERE id IN (%s)
  AND completed_timestamp = 0;
COMMIT;
`, nextRunMs, sqlStringLiteral(lastError), nowMs, sqlUint64List(jobIDs))
	if err := s.exec(ctx, script); err != nil {
		return fmt.Errorf("retry sqlite vector gc jobs: %w", err)
	}
	return nil
}

// sqliteTimestampToTime converts one millisecond timestamp used by the SQLite store into UTC time while treating zero as the absent-value marker.
// sqliteTimestampToTime 用于把 SQLite store 使用的毫秒时间戳转换成 UTC 时间，并把零值视作缺省标记。
func sqliteTimestampToTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
