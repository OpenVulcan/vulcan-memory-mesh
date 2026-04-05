// recycle_jobs.go implements the SQLite-backed recycle-job queue used by the independent cold-turn scan/claim/execute pipeline.
// recycle_jobs.go 用于实现 SQLite 的回收任务队列，服务独立冷 turn 扫描/领取/执行链路。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// sqliteRecycleJobRow stores one SQLite recycle-job row before it is converted into the shared retention domain shape.
// sqliteRecycleJobRow 用于保存一条 SQLite 回收任务行，在转换为共享 retention 领域模型前使用。
type sqliteRecycleJobRow struct {
	ID               uint64 `json:"id"`
	SessionID        uint64 `json:"session_id"`
	ProjectID        uint64 `json:"project_id"`
	JobType          string `json:"job_type"`
	AttemptCount     int    `json:"attempt_count"`
	NextRunTimestamp int64  `json:"next_run_timestamp"`
	ClaimedTimestamp int64  `json:"claimed_timestamp"`
	LastError        string `json:"last_error"`
	CreatedTimestamp int64  `json:"created_timestamp"`
	UpdatedTimestamp int64  `json:"updated_timestamp"`
}

// toDomain converts one SQLite recycle-job row into the shared retention recycle-job record.
// toDomain 用于把一条 SQLite 回收任务行转换成共享 retention 回收任务记录。
func (r sqliteRecycleJobRow) toDomain() logicdomain.RecycleJobRecord {
	return logicdomain.RecycleJobRecord{
		ID:           r.ID,
		SessionID:    r.SessionID,
		ProjectID:    r.ProjectID,
		JobType:      strings.TrimSpace(r.JobType),
		AttemptCount: r.AttemptCount,
		NextRunAt:    sqliteTimestampToTime(r.NextRunTimestamp),
		ClaimedAt:    sqliteTimestampToTime(r.ClaimedTimestamp),
		LastError:    strings.TrimSpace(r.LastError),
		CreatedAt:    sqliteTimestampToTime(r.CreatedTimestamp),
		UpdatedAt:    sqliteTimestampToTime(r.UpdatedTimestamp),
	}
}

// EnqueueColdTurnRecycleJobs scans for sessions that currently expose recyclable old turns and persists bounded recycle jobs so scan and execution no longer share one write script.
// EnqueueColdTurnRecycleJobs 用于扫描当前存在可回收旧 turn 的 session，并持久化一批有界的回收任务，让扫描与执行不再共用同一段写脚本。
func (s *Store) EnqueueColdTurnRecycleJobs(ctx context.Context, query logicdomain.ColdTurnRecycleJobEnqueueQuery) (int, error) {
	if s == nil || s.client == nil {
		return 0, fmt.Errorf("sqlite store is not initialized")
	}
	limit := normalizeSQLiteRetentionBatchLimit(query.Limit, 64)
	scannedAtMs := normalizeSQLiteRecycleTime(query.ScannedAt).UnixMilli()
	nextRunMs := normalizeSQLiteRecycleTime(query.NextRunAt).UnixMilli()
	turnHotWindowSize := normalizeSQLiteTurnHotWindowSize(query.TurnHotWindowSize)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	availabilityClause, availabilityArgs := buildSQLiteColdTurnJobSessionAvailabilityClause(turnHotWindowSize)
	sessionRows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE NOT EXISTS (
    SELECT 1
    FROM vmm_turn_records tr
    WHERE tr.session_id = vmm_sessions.id
      AND tr.extracted_status = ?
  )
  AND `+availabilityClause+`
  AND NOT EXISTS (
    SELECT 1
    FROM vmm_recycle_jobs jobs
    WHERE jobs.session_id = vmm_sessions.id
      AND jobs.job_type = ?
  )
ORDER BY updated_timestamp ASC, id ASC
LIMIT ?
`, append([]any{logicdomain.TurnExtractedStatusPending}, append(availabilityArgs, logicdomain.RecycleJobTypeColdTurn, limit)...)...)
	if err != nil {
		return 0, fmt.Errorf("query sqlite cold-turn recycle job candidates: %w", err)
	}
	if len(sessionRows) == 0 {
		return 0, nil
	}

	nextID, err := s.nextNumericID(ctx, "vmm_recycle_jobs")
	if err != nil {
		return 0, fmt.Errorf("allocate sqlite recycle job id: %w", err)
	}
	var builder strings.Builder
	builder.WriteString("BEGIN IMMEDIATE;\n")
	for _, session := range sessionRows {
		builder.WriteString(fmt.Sprintf(`
INSERT OR IGNORE INTO vmm_recycle_jobs (
  id, session_id, project_id, job_type, attempt_count, next_run_timestamp,
  claimed_timestamp, last_error, created_timestamp, updated_timestamp
)
VALUES (%d, %d, %d, %s, 0, %d, 0, '', %d, %d);
`, nextID, session.ID, session.ProjectID, sqlStringLiteral(logicdomain.RecycleJobTypeColdTurn), nextRunMs, scannedAtMs, scannedAtMs))
		nextID++
	}
	builder.WriteString("COMMIT;")
	if err := s.exec(ctx, builder.String()); err != nil {
		return 0, fmt.Errorf("enqueue sqlite cold-turn recycle jobs: %w", err)
	}
	return len(sessionRows), nil
}

// ClaimPendingRecycleJobs leases one bounded SQLite recycle-job batch by moving due jobs to the lease horizon, so crashed workers eventually release the claim automatically.
// ClaimPendingRecycleJobs 用于通过把到期任务推进到租约边界来领取一批有界的 SQLite 回收任务，从而在工作器崩溃后自动释放领取状态。
func (s *Store) ClaimPendingRecycleJobs(ctx context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return nil, fmt.Errorf("recycle job type is required")
	}
	limit = normalizeSQLiteRetentionBatchLimit(limit, 32)
	dueBeforeMs := normalizeSQLiteRecycleTime(dueBefore).UnixMilli()
	claimAtMs := time.Now().UTC().UnixMilli()
	claimUntilMs := normalizeSQLiteRecycleTime(claimUntil).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[sqliteRecycleJobRow](s, ctx, `
SELECT id, session_id, project_id, job_type, attempt_count,
       next_run_timestamp, claimed_timestamp, last_error, created_timestamp, updated_timestamp
FROM vmm_recycle_jobs
WHERE job_type = ?
  AND next_run_timestamp > 0
  AND next_run_timestamp <= ?
ORDER BY next_run_timestamp ASC, id ASC
LIMIT ?
`, jobType, dueBeforeMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query sqlite pending recycle jobs: %w", err)
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
UPDATE vmm_recycle_jobs
SET claimed_timestamp = %d,
    next_run_timestamp = %d,
    updated_timestamp = %d
WHERE id IN (%s);
COMMIT;
`, claimAtMs, claimUntilMs, claimAtMs, jobIDList)
	if err := s.exec(ctx, script); err != nil {
		return nil, fmt.Errorf("claim sqlite recycle jobs: %w", err)
	}
	claimed := make([]logicdomain.RecycleJobRecord, 0, len(rows))
	for _, row := range rows {
		row.ClaimedTimestamp = claimAtMs
		row.NextRunTimestamp = claimUntilMs
		row.UpdatedTimestamp = claimAtMs
		claimed = append(claimed, row.toDomain())
	}
	return claimed, nil
}

// RecycleColdTurns executes one claimed cold-turn recycle job by moving unreferenced turns outside the hot window into the turn-trash table.
// RecycleColdTurns 用于执行一条已领取的冷 turn 回收任务，把热窗口外且无引用的旧 turn 迁入 turn 回收站表。
func (s *Store) RecycleColdTurns(ctx context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error) {
	if s == nil || s.client == nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	if query.SessionID == 0 {
		return logicdomain.ColdTurnRecycleResult{}, nil
	}
	recycledAtMillis := normalizeSQLiteRecycleTime(query.RecycledAt).UnixMilli()
	turnHotWindowSize := normalizeSQLiteTurnHotWindowSize(query.TurnHotWindowSize)
	reason := strings.TrimSpace(query.RecycleReason)
	if reason == "" {
		reason = logicdomain.RecycleReasonColdTurnArchive
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	sessionRows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE id = ?
LIMIT 1
`, query.SessionID)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("query sqlite cold-turn recycle session %d: %w", query.SessionID, err)
	}
	if len(sessionRows) == 0 {
		return logicdomain.ColdTurnRecycleResult{}, nil
	}
	session := sessionRows[0]

	pendingCount, err := s.countRows(ctx, `
SELECT COUNT(*) AS count
FROM vmm_turn_records
WHERE session_id = ?
  AND extracted_status = ?
`, query.SessionID, logicdomain.TurnExtractedStatusPending)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("count sqlite pending turns for session %d: %w", query.SessionID, err)
	}
	if pendingCount > 0 {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	}

	turnRows, err := queryRows[turnRecordRow](s, ctx, fmt.Sprintf(`
WITH recent_turns AS (
  SELECT id
  FROM vmm_turn_records
  WHERE session_id = ?
  ORDER BY id DESC
  LIMIT %d
)
SELECT tr.id, tr.session_id, tr.project_id,
       tr.dehydrated_content,
       tr.dehydrated_budget, tr.extracted_status, tr.details, tr.details_budget,
       tr.created_timestamp, tr.updated_timestamp
FROM vmm_turn_records tr
WHERE tr.session_id = ?
  AND tr.extracted_status <> ?
  AND tr.id NOT IN (SELECT id FROM recent_turns)
  AND NOT EXISTS (
    SELECT 1
    FROM vmm_memory_nodes mn
    WHERE mn.source_turn_id = tr.id
  )
  AND NOT EXISTS (
    SELECT 1
    FROM vmm_profile_nodes pn
    WHERE pn.turn_id = tr.id
  )
ORDER BY tr.id ASC
`, turnHotWindowSize), query.SessionID, query.SessionID, logicdomain.TurnExtractedStatusPending)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("query sqlite cold turns for session %d: %w", query.SessionID, err)
	}
	if len(turnRows) == 0 {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	}

	batchID, err := s.nextNumericID(ctx, "vmm_recycle_batches")
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("allocate sqlite cold-turn recycle batch id for session %d: %w", query.SessionID, err)
	}
	turnIDs := make([]uint64, 0, len(turnRows))
	for _, row := range turnRows {
		turnIDs = append(turnIDs, row.ID)
	}
	normalizedTurnIDs := normalizeUint64List(turnIDs)
	if len(normalizedTurnIDs) == 0 {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	}
	turnIDList := sqlUint64List(normalizedTurnIDs)

	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (%d, %s, %d, %d, %s, %d, 0, %d, %d);

INSERT INTO vmm_turn_records_trash (
  batch_id, recycled_at, recycle_reason,
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
)
SELECT %d, %d, %s,
       id, session_id, project_id, dehydrated_content, dehydrated_budget,
       extracted_status, details, details_budget, created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s);

DELETE FROM vmm_turn_records
WHERE id IN (%s);
COMMIT;
`, batchID, sqlStringLiteral(logicdomain.RecycleTypeColdTurn), query.SessionID, session.ProjectID, sqlStringLiteral(reason), recycledAtMillis, recycledAtMillis, recycledAtMillis,
		batchID, recycledAtMillis, sqlStringLiteral(reason), turnIDList, turnIDList)
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("recycle sqlite cold turns for session %d: %w", query.SessionID, err)
	}
	return logicdomain.ColdTurnRecycleResult{
		BatchID:           batchID,
		SessionID:         query.SessionID,
		ProjectID:         session.ProjectID,
		RecycledTurnCount: len(normalizedTurnIDs),
	}, nil
}

// CompleteRecycleJobs removes one SQLite recycle-job batch immediately after scan-separated cold-turn execution completes, keeping the queue transient instead of retaining completed rows forever.
// CompleteRecycleJobs 用于在 scan 分离后的冷 turn 执行完成后立即删除一批 SQLite 回收任务，让队列表保持瞬时语义，而不是永久保留已完成行。
func (s *Store) CompleteRecycleJobs(ctx context.Context, jobIDs []uint64, _ time.Time) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	jobIDs = normalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	script := fmt.Sprintf(`
BEGIN IMMEDIATE;
DELETE FROM vmm_recycle_jobs
WHERE id IN (%s);
COMMIT;
`, sqlUint64List(jobIDs))
	if err := s.exec(ctx, script); err != nil {
		return fmt.Errorf("complete sqlite recycle jobs: %w", err)
	}
	return nil
}

// RetryRecycleJobs reschedules one SQLite recycle-job batch after execution still fails, incrementing attempts while releasing the current lease.
// RetryRecycleJobs 用于在执行仍然失败时重新调度一批 SQLite 回收任务，同时递增尝试次数并释放当前租约。
func (s *Store) RetryRecycleJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
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
UPDATE vmm_recycle_jobs
SET attempt_count = attempt_count + 1,
    next_run_timestamp = %d,
    claimed_timestamp = 0,
    last_error = %s,
    updated_timestamp = %d
WHERE id IN (%s);
COMMIT;
`, nextRunMs, sqlStringLiteral(lastError), nowMs, sqlUint64List(jobIDs))
	if err := s.exec(ctx, script); err != nil {
		return fmt.Errorf("retry sqlite recycle jobs: %w", err)
	}
	return nil
}

// buildSQLiteColdTurnJobSessionAvailabilityClause renders the candidate predicate used by the independent cold-turn scan so only sessions with real recyclable turns enter the queue.
// buildSQLiteColdTurnJobSessionAvailabilityClause 用于渲染独立冷 turn 扫描使用的候选谓词，确保只有确实存在可回收旧 turn 的 session 才会入队。
func buildSQLiteColdTurnJobSessionAvailabilityClause(turnHotWindowSize int) (string, []any) {
	return fmt.Sprintf(`EXISTS (
  WITH recent_turns AS (
    SELECT id
    FROM vmm_turn_records
    WHERE session_id = vmm_sessions.id
    ORDER BY id DESC
    LIMIT %d
  )
  SELECT 1
  FROM vmm_turn_records tr
  WHERE tr.session_id = vmm_sessions.id
    AND tr.extracted_status <> ?
    AND tr.id NOT IN (SELECT id FROM recent_turns)
    AND NOT EXISTS (SELECT 1 FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id)
    AND NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)
)`, turnHotWindowSize), []any{logicdomain.TurnExtractedStatusPending}
}
