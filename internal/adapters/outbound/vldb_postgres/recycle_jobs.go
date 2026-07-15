// recycle_jobs.go implements the PostgreSQL-backed recycle-job queue used by the independent cold-turn scan/claim/execute pipeline.
// recycle_jobs.go 用于实现 PostgreSQL 的回收任务队列，服务独立冷 turn 扫描/领取/执行链路。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postgresRecycleJobRow stores one PostgreSQL recycle-job row before it is converted into the shared retention domain model.
// postgresRecycleJobRow 用于保存一条 PostgreSQL 回收任务行，在转换为共享 retention 领域模型前使用。
type postgresRecycleJobRow struct {
	ID           uint64
	SessionID    uint64
	ProjectID    uint64
	JobType      string
	AttemptCount int
	NextRunAt    time.Time
	ClaimedAt    *time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// toDomain converts one PostgreSQL recycle-job row into the shared retention recycle-job record.
// toDomain 用于把一条 PostgreSQL 回收任务行转换成共享 retention 回收任务记录。
func (r postgresRecycleJobRow) toDomain() logicdomain.RecycleJobRecord {
	record := logicdomain.RecycleJobRecord{
		ID:           r.ID,
		SessionID:    r.SessionID,
		ProjectID:    r.ProjectID,
		JobType:      strings.TrimSpace(r.JobType),
		AttemptCount: r.AttemptCount,
		NextRunAt:    r.NextRunAt.UTC(),
		LastError:    strings.TrimSpace(r.LastError),
		CreatedAt:    r.CreatedAt.UTC(),
		UpdatedAt:    r.UpdatedAt.UTC(),
	}
	if r.ClaimedAt != nil {
		record.ClaimedAt = r.ClaimedAt.UTC()
	}
	return record
}

// EnqueueColdTurnRecycleJobs scans for sessions that currently expose recyclable old turns and persists bounded recycle jobs so scan and execution no longer share the same transaction.
// EnqueueColdTurnRecycleJobs 用于扫描当前存在可回收旧 turn 的 session，并持久化一批有界的回收任务，让扫描与执行不再共用同一事务。
func (r *retentionRepository) EnqueueColdTurnRecycleJobs(ctx context.Context, query logicdomain.ColdTurnRecycleJobEnqueueQuery) (int, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	limit := normalizeRetentionBatchLimit(query.Limit, 64)
	scannedAt := chooseNonZeroTime(query.ScannedAt, time.Now().UTC())
	nextRunAt := chooseNonZeroTime(query.NextRunAt, scannedAt)
	turnHotWindowSize := normalizeRetentionTurnHotWindowSize(query.TurnHotWindowSize)

	args := &sqlArgsBuilder{}
	jobTypePlaceholder := args.Add(logicdomain.RecycleJobTypeColdTurn)
	nextRunAtPlaceholder := args.Add(nextRunAt)
	scannedAtPlaceholder := args.Add(scannedAt)
	whereClauses := []string{
		fmt.Sprintf(`NOT EXISTS (
  SELECT 1
  FROM %s tr
  WHERE tr.session_id = s.id
    AND tr.extracted_status = %s
)`, r.turnsTable(), args.Add(logicdomain.TurnExtractedStatusPending)),
		buildPostgresColdTurnJobSessionAvailabilityClause(args, r, turnHotWindowSize),
		fmt.Sprintf(`NOT EXISTS (
  SELECT 1
  FROM %s jobs
  WHERE jobs.session_id = s.id
    AND jobs.job_type = %s
)`, r.recycleJobsTable(), jobTypePlaceholder),
	}
	limitPlaceholder := args.Add(limit)
	sqlText := fmt.Sprintf(`
INSERT INTO %s (
  session_id, project_id, job_type, attempt_count, next_run_at, claimed_at, last_error, created_at, updated_at
)
SELECT s.id, s.project_id, %s, 0, %s, NULL, '', %s, %s
FROM %s AS s
WHERE %s
ORDER BY s.updated_at ASC, s.id ASC
LIMIT %s
ON CONFLICT (session_id, job_type) DO NOTHING
`, r.recycleJobsTable(), jobTypePlaceholder, nextRunAtPlaceholder, scannedAtPlaceholder, scannedAtPlaceholder, r.sessionsTable(), strings.Join(whereClauses, "\n  AND "), limitPlaceholder)

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tag, err := r.shared.pool.Exec(callCtx, strings.TrimSpace(sqlText), args.Args()...)
	if err != nil {
		return 0, fmt.Errorf("enqueue postgres cold-turn recycle jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClaimPendingRecycleJobs leases one bounded PostgreSQL recycle-job batch with SKIP LOCKED so concurrent maintenance workers cannot execute the same cold-turn work twice.
// ClaimPendingRecycleJobs 用于通过 SKIP LOCKED 领取一批有界的 PostgreSQL 回收任务，避免并发维护工作器重复执行同一批冷 turn 工作。
func (r *retentionRepository) ClaimPendingRecycleJobs(ctx context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return nil, fmt.Errorf("recycle job type is required")
	}
	limit = normalizeRetentionBatchLimit(limit, 32)
	dueBefore = chooseNonZeroTime(dueBefore, time.Now().UTC())
	claimAt := time.Now().UTC()
	claimUntil = chooseNonZeroTime(claimUntil, claimAt)

	sqlText := buildPostgresClaimPendingRecycleJobsSQL(r.recycleJobsTable())

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres recycle-job claim tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	rows, err := tx.Query(callCtx, strings.TrimSpace(sqlText), jobType, dueBefore, limit, claimAt, claimUntil)
	if err != nil {
		return nil, fmt.Errorf("claim postgres recycle jobs: %w", err)
	}
	defer rows.Close()

	claimed := make([]logicdomain.RecycleJobRecord, 0)
	for rows.Next() {
		var row postgresRecycleJobRow
		if err := rows.Scan(
			&row.ID,
			&row.SessionID,
			&row.ProjectID,
			&row.JobType,
			&row.AttemptCount,
			&row.NextRunAt,
			&row.ClaimedAt,
			&row.LastError,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres recycle job: %w", err)
		}
		claimed = append(claimed, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres recycle jobs: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return nil, postgresClaimCommitError("claim recycle jobs", "commit postgres recycle-job claim tx", len(claimed), err)
	}
	return claimed, nil
}

// RecycleColdTurns executes one claimed cold-turn recycle job by moving unreferenced turns outside the hot window into the turn-trash table under one transaction.
// RecycleColdTurns 用于执行一条已领取的冷 turn 回收任务，并在一个事务里把热窗口外且无引用的旧 turn 迁入 turn 回收站表。
func (r *retentionRepository) RecycleColdTurns(ctx context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("postgres store is not initialized")
	}
	if query.SessionID == 0 {
		return logicdomain.ColdTurnRecycleResult{}, nil
	}
	recycledAt := chooseNonZeroTime(query.RecycledAt, time.Now().UTC())
	turnHotWindowSize := normalizeRetentionTurnHotWindowSize(query.TurnHotWindowSize)
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonColdTurnArchive
	}

	callCtx, cancel := r.retentionMaintenanceWriteContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("begin postgres cold-turn recycle tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	sessionSQL := fmt.Sprintf(`
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_at, last_extract_completed_at, last_compacted_at,
       created_at, updated_at
FROM %s
WHERE id = $1
FOR UPDATE
`, r.sessionsTable())
	var session sessionScanRow
	if err := tx.QueryRow(callCtx, strings.TrimSpace(sessionSQL), int64(query.SessionID)).Scan(
		&session.ID,
		&session.SessionKey,
		&session.UserID,
		&session.TeamID,
		&session.SpaceID,
		&session.ProjectID,
		&session.TurnCount,
		&session.LastSummarizedID,
		&session.LastCompactedTurnID,
		&session.SummarizeContent,
		&session.SummarizeBudget,
		&session.LastExtractObservedAt,
		&session.LastExtractCompletedAt,
		&session.LastCompactedAt,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.ColdTurnRecycleResult{}, nil
		}
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("lock postgres cold-turn recycle session %d: %w", query.SessionID, err)
	}

	pendingCheckSQL := fmt.Sprintf(`
SELECT 1
FROM %s
WHERE session_id = $1
  AND extracted_status = $2
LIMIT 1
`, r.turnsTable())
	var pendingMarker int
	if err := tx.QueryRow(callCtx, strings.TrimSpace(pendingCheckSQL), int64(query.SessionID), logicdomain.TurnExtractedStatusPending).Scan(&pendingMarker); err == nil {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("check postgres pending turns for session %d: %w", query.SessionID, err)
	}

	args := &sqlArgsBuilder{}
	recentTurnsCTE := fmt.Sprintf(`
WITH recent_turns AS (
  SELECT id
  FROM %s
  WHERE session_id = %s
  ORDER BY id DESC
  LIMIT %d
)
`, r.turnsTable(), args.Add(int64(query.SessionID)), turnHotWindowSize)
	turnWhereClauses := []string{
		"tr.session_id = " + args.Add(int64(query.SessionID)),
		"tr.extracted_status <> " + args.Add(logicdomain.TurnExtractedStatusPending),
		"tr.id NOT IN (SELECT id FROM recent_turns)",
		fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)`, r.memoryNodesTable()),
		fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM %s pn WHERE pn.turn_id = tr.id)`, (&workspaceRepository{shared: r.shared}).profileNodesTable()),
	}
	turnSQL := fmt.Sprintf(`
%s
SELECT tr.id
FROM %s AS tr
WHERE %s
ORDER BY tr.id ASC
FOR UPDATE
`, strings.TrimSpace(recentTurnsCTE), r.turnsTable(), strings.Join(turnWhereClauses, " AND "))
	rows, err := tx.Query(callCtx, strings.TrimSpace(turnSQL), args.Args()...)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("query postgres cold turns for session %d: %w", query.SessionID, err)
	}
	defer rows.Close()

	turnIDs := make([]uint64, 0)
	for rows.Next() {
		var turnID uint64
		if err := rows.Scan(&turnID); err != nil {
			return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("scan postgres cold turn id for session %d: %w", query.SessionID, err)
		}
		turnIDs = append(turnIDs, turnID)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("iterate postgres cold turns for session %d: %w", query.SessionID, err)
	}
	normalizedTurnIDs := storageutil.NormalizeUint64List(turnIDs)
	if len(normalizedTurnIDs) == 0 {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	}

	insertBatchSQL := fmt.Sprintf(`
INSERT INTO %s (recycle_type, session_id, project_id, reason, recycled_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5, $5)
RETURNING id
`, r.recycleBatchesTable())
	var batchID uint64
	if err := tx.QueryRow(callCtx, strings.TrimSpace(insertBatchSQL), logicdomain.RecycleTypeColdTurn, int64(query.SessionID), int64(session.ProjectID), reason, recycledAt).Scan(&batchID); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("insert postgres cold-turn recycle batch for session %d: %w", query.SessionID, err)
	}

	turnIDArgs := toInt64List(normalizedTurnIDs)
	insertTurnTrashSQL := fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason,
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_at, updated_at
)
SELECT $1, $2, $3,
       id, session_id, project_id, dehydrated_content, dehydrated_budget,
       extracted_status, details, details_budget, created_at, updated_at
FROM %s
WHERE id = ANY($4)
`, r.turnsTrashTable(), r.turnsTable())
	turnTag, err := tx.Exec(callCtx, strings.TrimSpace(insertTurnTrashSQL), batchID, recycledAt, reason, turnIDArgs)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("copy postgres cold turns into trash for session %d: %w", query.SessionID, err)
	}
	expectedTurnRowsAffected := int64(len(normalizedTurnIDs))
	if err := requirePostgresColdTurnRowsAffected("copy postgres cold turns into trash", query.SessionID, turnTag.RowsAffected(), expectedTurnRowsAffected); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, err
	}
	deleteTurnSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, r.turnsTable())
	deleteTurnTag, err := tx.Exec(callCtx, deleteTurnSQL, turnIDArgs)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("delete postgres cold turns for session %d: %w", query.SessionID, err)
	}
	if err := requirePostgresColdTurnRowsAffected("delete postgres cold turns", query.SessionID, deleteTurnTag.RowsAffected(), expectedTurnRowsAffected); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, err
	}

	result := logicdomain.ColdTurnRecycleResult{
		BatchID:           batchID,
		SessionID:         query.SessionID,
		ProjectID:         session.ProjectID,
		RecycledTurnCount: int(turnTag.RowsAffected()),
	}
	if err := tx.Commit(callCtx); err != nil {
		return result, postgresColdTurnRecycleCommitOutcomeUncertainError(query.SessionID, err)
	}
	return result, nil
}

// postgresColdTurnRecycleCommitOutcomeUncertainError marks cold-turn archive commits whose trash copy and hot-table delete may already be durable.
// postgresColdTurnRecycleCommitOutcomeUncertainError 用于标记冷 turn 归档提交失败，此时 trash 复制和热表删除可能已经持久化。
func postgresColdTurnRecycleCommitOutcomeUncertainError(sessionID uint64, err error) error {
	return postgresCommitOutcomeUncertainError("recycle cold turns", fmt.Sprintf("commit postgres cold-turn recycle tx for session %d", sessionID), err)
}

// requirePostgresColdTurnRowsAffected rejects cold-turn recycle drift before the transaction commits and the claimed job is completed.
// requirePostgresColdTurnRowsAffected 用于在事务提交和已领取任务完成前拒绝 cold-turn 回收行数漂移。
func requirePostgresColdTurnRowsAffected(action string, sessionID uint64, rowsAffected, expectedRows int64) error {
	return postgresRowsAffectedDriftError(fmt.Sprintf("%s for session %d", action, sessionID), rowsAffected, expectedRows)
}

// CompleteRecycleJobs removes one PostgreSQL recycle-job batch immediately after scan-separated cold-turn execution completes, keeping the queue transient instead of turning it into a second long-lived ledger.
// CompleteRecycleJobs 用于在 scan 分离后的冷 turn 执行完成后立即删除一批 PostgreSQL 回收任务，让队列保持瞬时补偿语义，而不是演化成第二套长期台账。
func (r *retentionRepository) CompleteRecycleJobs(ctx context.Context, jobIDs []uint64, _ time.Time) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	jobIDs = storageutil.NormalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	sqlText := fmt.Sprintf(`
DELETE FROM %s
WHERE id = ANY($1)
`, r.recycleJobsTable())

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres recycle-job completion tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	tag, err := tx.Exec(callCtx, strings.TrimSpace(sqlText), toInt64List(jobIDs))
	if err != nil {
		return fmt.Errorf("complete postgres recycle jobs: %w", err)
	}
	if err := requirePostgresRecycleJobRowsAffected("complete postgres recycle jobs", tag.RowsAffected(), len(jobIDs)); err != nil {
		return err
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresCommitOutcomeUncertainError("complete recycle jobs", "commit postgres recycle-job completion tx", err)
	}
	return nil
}

// RetryRecycleJobs reschedules one PostgreSQL recycle-job batch after execution still fails, incrementing attempts while releasing the current lease.
// RetryRecycleJobs 用于在执行仍然失败时重新调度一批 PostgreSQL 回收任务，同时递增尝试次数并释放当前租约。
func (r *retentionRepository) RetryRecycleJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
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
`, r.recycleJobsTable())

	callCtx, cancel := r.retentionQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres recycle-job retry tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	tag, err := tx.Exec(callCtx, strings.TrimSpace(sqlText), toInt64List(jobIDs), nextRunAt, lastError, now)
	if err != nil {
		return fmt.Errorf("retry postgres recycle jobs: %w", err)
	}
	if err := requirePostgresRecycleJobRowsAffected("retry postgres recycle jobs", tag.RowsAffected(), len(jobIDs)); err != nil {
		return err
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresCommitOutcomeUncertainError("retry recycle jobs", "commit postgres recycle-job retry tx", err)
	}
	return nil
}

// requirePostgresRecycleJobRowsAffected rejects queue state drift when a terminal recycle-job update did not touch every normalized job id.
// requirePostgresRecycleJobRowsAffected 用于在回收任务终态更新未命中全部归一化 job id 时拒绝队列状态漂移。
func requirePostgresRecycleJobRowsAffected(messagePrefix string, rowsAffected int64, expectedRows int) error {
	return postgresRowsAffectedDriftError(messagePrefix, rowsAffected, int64(expectedRows))
}

// buildPostgresColdTurnJobSessionAvailabilityClause renders the candidate predicate used by the independent cold-turn scan so only sessions with real recyclable turns enter the queue.
// buildPostgresColdTurnJobSessionAvailabilityClause 用于渲染独立冷 turn 扫描使用的候选谓词，确保只有确实存在可回收旧 turn 的 session 才会入队。
func buildPostgresColdTurnJobSessionAvailabilityClause(args *sqlArgsBuilder, r *retentionRepository, turnHotWindowSize int) string {
	if args == nil || r == nil {
		return "FALSE"
	}
	pendingStatusPlaceholder := args.Add(logicdomain.TurnExtractedStatusPending)
	return fmt.Sprintf(`EXISTS (
  WITH recent_turns AS (
    SELECT id
    FROM %s
    WHERE session_id = s.id
    ORDER BY id DESC
    LIMIT %d
  )
  SELECT 1
  FROM %s AS tr
  WHERE tr.session_id = s.id
    AND tr.extracted_status <> %s
    AND tr.id NOT IN (SELECT id FROM recent_turns)
    AND NOT EXISTS (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)
    AND NOT EXISTS (SELECT 1 FROM %s pn WHERE pn.turn_id = tr.id)
)`, r.turnsTable(), turnHotWindowSize, r.turnsTable(), pendingStatusPlaceholder, r.memoryNodesTable(), (&workspaceRepository{shared: r.shared}).profileNodesTable())
}

// buildPostgresClaimPendingRecycleJobsSQL renders the SKIP LOCKED claim statement used by the recycle-job queue so tests can assert multi-worker no-duplicate semantics without needing a live PostgreSQL instance.
// buildPostgresClaimPendingRecycleJobsSQL 用于渲染回收任务队列的 SKIP LOCKED 领取语句，让测试无需真实 PostgreSQL 也能断言多工作器不重复消费语义。
func buildPostgresClaimPendingRecycleJobsSQL(recycleJobsTable string) string {
	return fmt.Sprintf(`
WITH claimed AS (
  SELECT id
  FROM %s
  WHERE job_type = $1
    AND next_run_at <= $2
  ORDER BY next_run_at ASC, id ASC
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
UPDATE %s AS jobs
SET claimed_at = $4,
    next_run_at = $5,
    updated_at = $4
FROM claimed
WHERE jobs.id = claimed.id
RETURNING jobs.id, jobs.session_id, jobs.project_id, jobs.job_type, jobs.attempt_count,
          jobs.next_run_at, jobs.claimed_at, jobs.last_error, jobs.created_at, jobs.updated_at
`, recycleJobsTable, recycleJobsTable)
}
