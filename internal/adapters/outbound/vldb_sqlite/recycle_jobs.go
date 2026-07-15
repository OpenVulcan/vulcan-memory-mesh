// recycle_jobs.go implements the SQLite-backed recycle-job queue used by the independent cold-turn scan/claim/execute pipeline.
// recycle_jobs.go 用于实现 SQLite 的回收任务队列，服务独立冷 turn 扫描/领取/执行链路。
package vldb_sqlite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
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
	if !s.hasSQLiteStore() {
		return 0, fmt.Errorf("sqlite store is not initialized")
	}
	limit := storageutil.PositiveOrDefault(query.Limit, 64)
	scannedAtMs := normalizeSQLiteRecycleTime(query.ScannedAt).UnixMilli()
	nextRunMs := normalizeSQLiteRecycleTime(query.NextRunAt).UnixMilli()
	turnHotWindowSize := max(query.TurnHotWindowSize, 0)

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
	statements := make([]sqliteWriteStatement, 0, len(sessionRows))
	for _, session := range sessionRows {
		statements = append(statements, parameterizedColdTurnRecycleJobInsertStatement(nextID, session.ID, session.ProjectID, nextRunMs, scannedAtMs))
		nextID++
	}
	// Count each confirmed autocommit enqueue so callers do not lose already-durable queue rows when a later candidate drifts.
	// 统计每条已确认的自动提交入队结果，避免后续候选漂移时调用方丢失已经持久化的队列行数量。
	enqueuedCount := 0
	for idx, statement := range statements {
		insertResult, err := s.execResult(ctx, statement.SQL, statement.Params...)
		if err != nil {
			err := fmt.Errorf("enqueue sqlite cold-turn recycle job %d: %w", idx+1, err)
			return enqueuedCount, sqliteQueuePartialWriteError(enqueuedCount > 0, "enqueue cold-turn recycle jobs", err)
		}
		// Report enqueue success only when the candidate session produced or refreshed exactly one durable recycle-job row.
		// 只有候选 session 精确生成或刷新一条持久化 recycle-job 行时，才报告入队成功。
		if insertResult.RowsChanged != 1 {
			err := fmt.Errorf("enqueue sqlite cold-turn recycle job %d affected %d rows, want 1", idx+1, insertResult.RowsChanged)
			return enqueuedCount, sqlitePartialMutationError(enqueuedCount > 0 || insertResult.RowsChanged > 0, "enqueue cold-turn recycle jobs", err)
		}
		enqueuedCount++
	}
	return enqueuedCount, nil
}

// parameterizedColdTurnRecycleJobInsertStatement returns one bounded cold-turn recycle-job UPSERT with the stable job type bound as a typed param.
// parameterizedColdTurnRecycleJobInsertStatement 用于返回一条有界冷 turn 回收任务 UPSERT，并把稳定 job type 作为强类型参数绑定。
func parameterizedColdTurnRecycleJobInsertStatement(jobID, sessionID, projectID uint64, nextRunMs, scannedAtMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_recycle_jobs (
  id, session_id, project_id, job_type, attempt_count, next_run_timestamp,
  claimed_timestamp, last_error, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, 0, ?, 0, ?, ?, ?)
ON CONFLICT(session_id, job_type) DO UPDATE SET
  updated_timestamp = excluded.updated_timestamp;
`,
		Params: []any{jobID, sessionID, projectID, logicdomain.RecycleJobTypeColdTurn, nextRunMs, "", scannedAtMs, scannedAtMs},
	}
}

// ClaimPendingRecycleJobs leases one bounded SQLite recycle-job batch by moving due jobs to the lease horizon, so crashed workers eventually release the claim automatically.
// ClaimPendingRecycleJobs 用于通过把到期任务推进到租约边界来领取一批有界的 SQLite 回收任务，从而在工作器崩溃后自动释放领取状态。
func (s *Store) ClaimPendingRecycleJobs(ctx context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error) {
	if !s.hasSQLiteStore() {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return nil, fmt.Errorf("recycle job type is required")
	}
	limit = storageutil.PositiveOrDefault(limit, 32)
	dueBeforeMs := normalizeSQLiteRecycleTime(dueBefore).UnixMilli()
	claimAtMs := time.Now().UTC().UnixMilli()
	claimUntilMs := normalizeSQLiteRecycleTime(claimUntil).UnixMilli()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[sqliteRecycleJobRow](s, ctx, `
UPDATE vmm_recycle_jobs
SET claimed_timestamp = ?,
    next_run_timestamp = ?,
    updated_timestamp = ?
WHERE id IN (
  SELECT id
  FROM vmm_recycle_jobs
  WHERE job_type = ?
    AND next_run_timestamp > 0
    AND next_run_timestamp <= ?
  ORDER BY next_run_timestamp ASC, id ASC
  LIMIT ?
)
RETURNING id, session_id, project_id, job_type, attempt_count,
		  next_run_timestamp, claimed_timestamp, last_error, created_timestamp, updated_timestamp;
`, claimAtMs, claimUntilMs, claimAtMs, jobType, dueBeforeMs, limit)
	if err != nil {
		return nil, sqliteQueueWriteError("claim recycle jobs", fmt.Errorf("claim sqlite pending recycle jobs: %w", err))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ID < rows[j].ID
	})
	claimed := make([]logicdomain.RecycleJobRecord, 0, len(rows))
	for _, row := range rows {
		claimed = append(claimed, row.toDomain())
	}
	return claimed, nil
}

// RecycleColdTurns executes one claimed cold-turn recycle job by moving unreferenced turns outside the hot window into the turn-trash table.
// RecycleColdTurns 用于执行一条已领取的冷 turn 回收任务，把热窗口外且无引用的旧 turn 迁入 turn 回收站表。
func (s *Store) RecycleColdTurns(ctx context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error) {
	if !s.hasSQLiteStore() {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	if query.SessionID == 0 {
		return logicdomain.ColdTurnRecycleResult{}, nil
	}
	recycledAtMillis := normalizeSQLiteRecycleTime(query.RecycledAt).UnixMilli()
	turnHotWindowSize := max(query.TurnHotWindowSize, 0)
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

	turnRows, err := queryRows[turnRecordRow](s, ctx, `
WITH recent_turns AS (
  SELECT id
  FROM vmm_turn_records
  WHERE session_id = ?
  ORDER BY id DESC
  LIMIT ?
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
`, query.SessionID, turnHotWindowSize, query.SessionID, logicdomain.TurnExtractedStatusPending)
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
	normalizedTurnIDs := storageutil.NormalizeUint64List(turnIDs)
	if len(normalizedTurnIDs) == 0 {
		return logicdomain.ColdTurnRecycleResult{SessionID: query.SessionID, ProjectID: session.ProjectID}, nil
	}
	turnIDPlaceholders := sqlitePlaceholders(len(normalizedTurnIDs))
	turnIDParams := sqliteUint64Params(normalizedTurnIDs)

	batchStatement := sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?);
`,
		Params: []any{batchID, logicdomain.RecycleTypeColdTurn, query.SessionID, session.ProjectID, reason, recycledAtMillis, recycledAtMillis, recycledAtMillis},
	}
	turnTrashStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
INSERT INTO vmm_turn_records_trash (
  batch_id, recycled_at, recycle_reason,
  id, session_id, project_id, dehydrated_content, dehydrated_budget,
  extracted_status, details, details_budget, created_timestamp, updated_timestamp
)
SELECT ?, ?, ?,
       id, session_id, project_id, dehydrated_content, dehydrated_budget,
       extracted_status, details, details_budget, created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s);
`, turnIDPlaceholders),
		Params: append([]any{batchID, recycledAtMillis, reason}, turnIDParams...),
	}
	turnDeleteStatement := sqliteWriteStatement{
		SQL: fmt.Sprintf(`
DELETE FROM vmm_turn_records
WHERE id IN (%s);
`, turnIDPlaceholders),
		Params: turnIDParams,
	}
	// Verify the batch metadata row with the shared single-insert classifier so over-reported inserts are not treated as clean retryable failures.
	// 使用统一的单行插入分类器验收批次元数据，避免把超量插入报告误判为可干净重试的普通失败。
	if err := s.execInsertOneRow(ctx, fmt.Sprintf("insert sqlite cold-turn recycle batch for session %d", query.SessionID), batchStatement.SQL, batchStatement.Params...); err != nil {
		return logicdomain.ColdTurnRecycleResult{}, fmt.Errorf("insert sqlite cold-turn recycle batch for session %d: %w", query.SessionID, err)
	}
	expectedTurnRowsChanged := int64(len(normalizedTurnIDs))
	turnTrashResult, err := s.execResult(ctx, turnTrashStatement.SQL, turnTrashStatement.Params...)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, sqlitePartialMutationError(true, "recycle cold turns", fmt.Errorf("copy sqlite cold turns to trash for session %d: %w", query.SessionID, err))
	}
	// Keep the claimed job retryable unless every selected cold turn is copied into trash before hot-table deletion.
	// 除非所有已选 cold turn 都先复制进回收站，否则保持已领取任务可重试，不进入热表删除。
	if turnTrashResult.RowsChanged != expectedTurnRowsChanged {
		return logicdomain.ColdTurnRecycleResult{}, logicdomain.OutcomeUncertainError{
			Operation: "recycle cold turns",
			Message:   fmt.Sprintf("copy sqlite cold turns to trash for session %d affected %d rows, want %d", query.SessionID, turnTrashResult.RowsChanged, expectedTurnRowsChanged),
		}
	}
	turnDeleteResult, err := s.execResult(ctx, turnDeleteStatement.SQL, turnDeleteStatement.Params...)
	if err != nil {
		return logicdomain.ColdTurnRecycleResult{}, sqlitePartialMutationError(true, "recycle cold turns", fmt.Errorf("delete recycled sqlite cold turns for session %d: %w", query.SessionID, err))
	}
	// Complete the recycle job only when the hot-table delete confirms every selected cold turn left the active table.
	// 只有热表删除确认所有已选 cold turn 都离开 active 表后，才允许后续完成回收任务。
	if turnDeleteResult.RowsChanged != expectedTurnRowsChanged {
		return logicdomain.ColdTurnRecycleResult{}, logicdomain.OutcomeUncertainError{
			Operation: "recycle cold turns",
			Message:   fmt.Sprintf("delete recycled sqlite cold turns for session %d affected %d rows, want %d", query.SessionID, turnDeleteResult.RowsChanged, expectedTurnRowsChanged),
		}
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
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	jobIDs = storageutil.NormalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	jobIDParams := make([]any, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		jobIDParams = append(jobIDParams, jobID)
	}
	deleteResult, err := s.execResult(ctx, fmt.Sprintf(`
DELETE FROM vmm_recycle_jobs
WHERE id IN (%s);
`, sqlitePlaceholders(len(jobIDs))), jobIDParams...)
	if err != nil {
		return sqliteQueueWriteError("complete recycle jobs", fmt.Errorf("complete sqlite recycle jobs: %w", err))
	}
	expectedRowsChanged := int64(len(jobIDs))
	if err := sqliteAutocommitRowsChangedDriftError("complete recycle jobs", "complete sqlite recycle jobs", deleteResult.RowsChanged, expectedRowsChanged); err != nil {
		return err
	}
	return nil
}

// RetryRecycleJobs reschedules one SQLite recycle-job batch after execution still fails, incrementing attempts while releasing the current lease.
// RetryRecycleJobs 用于在执行仍然失败时重新调度一批 SQLite 回收任务，同时递增尝试次数并释放当前租约。
func (s *Store) RetryRecycleJobs(ctx context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	jobIDs = storageutil.NormalizeUint64List(jobIDs)
	if len(jobIDs) == 0 {
		return nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	nextRunMs := normalizeSQLiteRecycleTime(nextRunAt).UnixMilli()
	lastError = strings.TrimSpace(lastError)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	params := make([]any, 0, 3+len(jobIDs))
	params = append(params, nextRunMs, lastError, nowMs)
	for _, jobID := range jobIDs {
		params = append(params, jobID)
	}
	updateResult, err := s.execResult(ctx, fmt.Sprintf(`
UPDATE vmm_recycle_jobs
SET attempt_count = attempt_count + 1,
    next_run_timestamp = ?,
    claimed_timestamp = 0,
    last_error = ?,
    updated_timestamp = ?
WHERE id IN (%s);
`, sqlitePlaceholders(len(jobIDs))), params...)
	if err != nil {
		return sqliteQueueWriteError("retry recycle jobs", fmt.Errorf("retry sqlite recycle jobs: %w", err))
	}
	expectedRowsChanged := int64(len(jobIDs))
	if err := sqliteAutocommitRowsChangedDriftError("retry recycle jobs", "retry sqlite recycle jobs", updateResult.RowsChanged, expectedRowsChanged); err != nil {
		return err
	}
	return nil
}

// sqliteQueueWriteError preserves SQLite commit-boundary uncertainty for queue mutations while keeping ordinary execution failures retryable.
// sqliteQueueWriteError 用于在队列写入中保留 SQLite 提交边界不确定语义，同时让普通执行失败保持可重试错误。
func sqliteQueueWriteError(operation string, err error) error {
	return sqliteQueuePartialWriteError(false, operation, err)
}

// sqliteQueuePartialWriteError preserves commit-boundary uncertainty before falling back to confirmed partial-mutation classification for queue writes.
// sqliteQueuePartialWriteError 用于在队列写入中优先保留提交边界不确定语义，再按已确认局部写入进行分类。
func sqliteQueuePartialWriteError(mutated bool, operation string, err error) error {
	return sqliteWriteCommitOrPartialMutationError(mutated, operation, err)
}

// buildSQLiteColdTurnJobSessionAvailabilityClause renders the candidate predicate used by the independent cold-turn scan so only sessions with real recyclable turns enter the queue.
// buildSQLiteColdTurnJobSessionAvailabilityClause 用于渲染独立冷 turn 扫描使用的候选谓词，确保只有确实存在可回收旧 turn 的 session 才会入队。
func buildSQLiteColdTurnJobSessionAvailabilityClause(turnHotWindowSize int) (string, []any) {
	return `EXISTS (
  WITH recent_turns AS (
    SELECT id
    FROM vmm_turn_records
    WHERE session_id = vmm_sessions.id
    ORDER BY id DESC
    LIMIT ?
  )
  SELECT 1
  FROM vmm_turn_records tr
  WHERE tr.session_id = vmm_sessions.id
    AND tr.extracted_status <> ?
    AND tr.id NOT IN (SELECT id FROM recent_turns)
    AND NOT EXISTS (SELECT 1 FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id)
    AND NOT EXISTS (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)
)`, []any{turnHotWindowSize, logicdomain.TurnExtractedStatusPending}
}
