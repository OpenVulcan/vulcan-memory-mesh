// SQLite turn storage and extraction progress used by session workflows.
// SQLite 适配层会话工作流使用的轮次存储与提炼进度。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// AppendTurnRecord persists one cleaned turn into the resolved session, increments the session turn counter, and returns the new turn identifier.
// AppendTurnRecord 用于把一条清洗后的 turn 写入已解析的 session、递增会话 turn 计数，并返回新的 turn 标识。
func (s *Store) AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	if strings.TrimSpace(turn.UserContent) == "" && strings.TrimSpace(turn.AssistantContent) == "" && len(turn.Timeline) == 0 {
		return logicdomain.PersistedTurnRecord{}, nil
	}
	if session.SessionID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}

	// Serialize turn writes under the process write lock because the real SQLite FFI cannot bind typed params across a multi-statement transaction script.
	// 在进程写锁下串行化 turn 写入，因为真实 SQLite FFI 无法在多语句事务脚本中绑定强类型参数。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_turn_records")
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("allocate turn record id: %w", err)
	}
	dehydratedContent, dehydratedBudget, err := storageutil.DehydrateTurn(turn)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()
	createdMs := nowMs
	if !turn.CreatedAt.IsZero() {
		createdMs = turn.CreatedAt.UTC().UnixMilli()
	}
	currentSession, err := s.loadSessionByID(ctx, session.SessionID)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	if currentSession.ProjectID != session.ProjectID {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ConflictError{
			Resource: "session",
			Message:  fmt.Sprintf("session %d belongs to project %d, not project %d", session.SessionID, currentSession.ProjectID, session.ProjectID),
		}
	}
	nextTurnCount := currentSession.TurnCount + 1
	nextSummarizeBudget := currentSession.SummarizeBudget + dehydratedBudget

	// Write and verify the durable turn row first because any later failure must be reported as an uncertain append outcome.
	// 先写入并验收持久化 turn 行，因为后续任何失败都必须以追加结果不确定的方式上报。
	turnInsertStatement := parameterizedTurnInsertStatement(nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs)
	if err := s.execInsertOneRow(ctx, "insert appended turn record", turnInsertStatement.SQL, turnInsertStatement.Params...); err != nil {
		insertRecovered := false
		// Re-read only the allocated primary key after a commit-unknown insert; a mismatched row cannot prove this append was persisted.
		// 仅在插入提交结果不明后回读本次分配的主键；不匹配的行无法证明本次追加已经落库。
		if sqliteInsertCommitUnknownError(err) {
			recovered, found, reconcileErr := s.loadTurnRecordByID(ctx, nextID)
			if reconcileErr != nil {
				return logicdomain.PersistedTurnRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "append turn record insert",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if found && sameAppendedTurnRecord(recovered, nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs) {
				insertRecovered = true
			}
		}
		if !insertRecovered {
			return logicdomain.PersistedTurnRecord{}, fmt.Errorf("append turn record insert: %w", err)
		}
	}

	// Update the owning session counter only after the insert is confirmed, then convert counter drift into outcome-uncertain instead of pretending the append failed cleanly.
	// 仅在确认插入成功后更新所属 session 计数，并把计数漂移转换为结果不确定，避免伪装成可安全重试的干净失败。
	sessionUpdateStatement := parameterizedSessionTurnUpdateStatement(session.SessionID, nextTurnCount, nextSummarizeBudget, nowMs)
	if err := s.execExpectRowsChanged(ctx, "update appended turn session counters", 1, sessionUpdateStatement.SQL, sessionUpdateStatement.Params...); err != nil {
		sessionUpdateRecovered := false
		// Re-read the absolute counter target only after a commit-unknown session update; row-count drift still means the append is uncertain.
		// 仅在 session 更新提交结果不明后回读绝对计数目标；行数漂移仍表示追加结果不确定。
		if isSQLiteOutcomeUncertainError(err) {
			recoveredSession, reconcileErr := s.loadSessionByID(ctx, session.SessionID)
			if reconcileErr != nil {
				return logicdomain.PersistedTurnRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "append turn record",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			sessionUpdateRecovered = sameAppendedSessionCounters(recoveredSession, session.SessionID, session.ProjectID, nextTurnCount, nextSummarizeBudget, nowMs)
		}
		if !sessionUpdateRecovered {
			return logicdomain.PersistedTurnRecord{}, sqlitePartialMutationError(true, "append turn record", err)
		}
	}
	return logicdomain.PersistedTurnRecord{
		ID:               nextID,
		SessionID:        session.SessionID,
		ProjectID:        session.ProjectID,
		DehydratedBudget: dehydratedBudget,
		CreatedAt:        unixMilliToTime(createdMs),
		UpdatedAt:        unixMilliToTime(nowMs),
	}, nil
}

// LoadPendingSessionTurns returns the oldest not-yet-extracted turn rows for one session so queued post-action workers can drain pending work in durable order.
// LoadPendingSessionTurns 用于返回某个 session 中尚未提取的最早 turn 行，让排队的 post-action 工作器按持久化顺序消化待处理工作。
func (s *Store) LoadPendingSessionTurns(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ? AND extracted_status = ?
ORDER BY id ASC
`, session.SessionID, logicdomain.TurnExtractedStatusPending)
	if err != nil {
		return nil, fmt.Errorf("query pending session turns: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	return turns, nil
}

// turnAnalysisFailureRow mirrors one durable per-turn analysis retry record returned by SQLite.
// turnAnalysisFailureRow 用于映射 SQLite 返回的一条逐 turn 持久化分析重试记录。
type turnAnalysisFailureRow struct {
	// TurnID identifies the durable turn whose analysis failed.
	// TurnID 用于标识发生分析失败的持久化 turn。
	TurnID uint64 `json:"turn_id"`

	// SessionID identifies the owning durable session.
	// SessionID 用于标识该失败记录所属的持久化 session。
	SessionID uint64 `json:"session_id"`

	// Stage identifies the analysis stage that owns this retry counter.
	// Stage 用于标识拥有该重试计数器的分析阶段。
	Stage string `json:"stage"`

	// AttemptCount stores the consecutive failed execution count.
	// AttemptCount 用于保存连续失败执行次数。
	AttemptCount int `json:"attempt_count"`

	// TerminalStatus stores retrying, passed, or passed_uncertain.
	// TerminalStatus 用于保存 retrying、passed 或 passed_uncertain 状态。
	TerminalStatus string `json:"terminal_status"`
}

// RecordTurnAnalysisFailure atomically increments one durable failure counter and lets schema triggers move terminal attempts to Pass.
// RecordTurnAnalysisFailure 用于原子递增一条持久化失败计数，并由 schema 触发器把终止尝试迁移为 Pass。
func (s *Store) RecordTurnAnalysisFailure(
	ctx context.Context,
	session logicdomain.SessionRef,
	turnID uint64,
	stage string,
	errorMessage string,
	failurePassThreshold int,
	forcePass bool,
) (logicdomain.TurnAnalysisFailureResult, error) {
	if session.SessionID == 0 {
		return logicdomain.TurnAnalysisFailureResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if turnID == 0 {
		return logicdomain.TurnAnalysisFailureResult{}, logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return logicdomain.TurnAnalysisFailureResult{}, logicdomain.ValidationError{Field: "stage", Message: "must not be empty"}
	}
	if failurePassThreshold <= 0 {
		return logicdomain.TurnAnalysisFailureResult{}, logicdomain.ValidationError{Field: "failure_pass_threshold", Message: "must be > 0"}
	}
	errorMessage = storageutil.TruncateUTF8Bytes(errorMessage, 4000)
	nowMs := time.Now().UTC().UnixMilli()

	// Serialize the counter UPSERT with all other SQLite turn state transitions so an accepted retry cannot race a successful analysis write-back.
	// 将计数 UPSERT 与其他 SQLite turn 状态迁移串行化，避免已接受的重试与成功分析回写发生竞态。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	insertStatus := logicdomain.TurnAnalysisFailureStatusRetrying
	if forcePass {
		insertStatus = logicdomain.TurnAnalysisFailureStatusPassedUncertain
	} else if failurePassThreshold == 1 {
		insertStatus = logicdomain.TurnAnalysisFailureStatusPassed
	}
	statement := `
INSERT INTO vmm_turn_analysis_failures (
  turn_id, session_id, stage, attempt_count, terminal_status, last_error, last_attempt_timestamp
)
SELECT id, session_id, ?, 1, ?, ?, ?
FROM vmm_turn_records
WHERE id = ? AND session_id = ? AND extracted_status = ?
ON CONFLICT(turn_id, stage) DO UPDATE SET
  attempt_count = vmm_turn_analysis_failures.attempt_count + 1,
  terminal_status = CASE
    WHEN ? THEN ?
    WHEN vmm_turn_analysis_failures.attempt_count + 1 >= ? THEN ?
    ELSE ?
  END,
  last_error = excluded.last_error,
  last_attempt_timestamp = excluded.last_attempt_timestamp
WHERE vmm_turn_analysis_failures.terminal_status = ?;
`
	if _, err := s.execResult(
		ctx,
		statement,
		stage,
		insertStatus,
		errorMessage,
		nowMs,
		turnID,
		session.SessionID,
		logicdomain.TurnExtractedStatusPending,
		forcePass,
		logicdomain.TurnAnalysisFailureStatusPassedUncertain,
		failurePassThreshold,
		logicdomain.TurnAnalysisFailureStatusPassed,
		logicdomain.TurnAnalysisFailureStatusRetrying,
		logicdomain.TurnAnalysisFailureStatusRetrying,
	); err != nil {
		return logicdomain.TurnAnalysisFailureResult{}, sqliteWriteCommitBoundaryError("record turn analysis failure", err)
	}
	rows, err := queryRows[turnAnalysisFailureRow](s, ctx, `
SELECT turn_id, session_id, stage, attempt_count, terminal_status
FROM vmm_turn_analysis_failures
WHERE turn_id = ? AND session_id = ? AND stage = ?
`, turnID, session.SessionID, stage)
	if err != nil {
		return logicdomain.TurnAnalysisFailureResult{}, fmt.Errorf("load recorded turn analysis failure: %w", err)
	}
	if len(rows) != 1 {
		return logicdomain.TurnAnalysisFailureResult{}, fmt.Errorf("record turn analysis failure did not resolve one pending turn")
	}
	status := strings.TrimSpace(rows[0].TerminalStatus)
	return logicdomain.TurnAnalysisFailureResult{
		AttemptCount: rows[0].AttemptCount,
		Status:       status,
		Passed:       status == logicdomain.TurnAnalysisFailureStatusPassed || status == logicdomain.TurnAnalysisFailureStatusPassedUncertain,
	}, nil
}

// parameterizedMarkTurnAsCorruptedStatement builds the guarded pending-to-done update used when a queued turn payload is unreadable.
// parameterizedMarkTurnAsCorruptedStatement 用于构造排队 turn 载荷不可读时使用的受保护 pending 到 done 更新语句。
func parameterizedMarkTurnAsCorruptedStatement(sessionID, turnID uint64, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_turn_records
SET extracted_status = ?, updated_timestamp = ?
WHERE id = ? AND session_id = ? AND extracted_status = ?;
`,
		Params: []any{logicdomain.TurnExtractedStatusDone, updatedMs, turnID, sessionID, logicdomain.TurnExtractedStatusPending},
	}
}

// MarkTurnAsCorrupted marks one pending turn whose dehydrated payload cannot be decoded as done so it stops being returned by LoadPendingSessionTurns.
// MarkTurnAsCorrupted 用于将无法解码的损坏 pending turn 标记为已处理，让它不再被 LoadPendingSessionTurns 返回。
func (s *Store) MarkTurnAsCorrupted(ctx context.Context, session logicdomain.SessionRef, turnID uint64) error {
	if session.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if turnID == 0 {
		return logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	nowMs := time.Now().UTC().UnixMilli()

	// Serialize corrupted-turn draining with normal turn analysis because both paths transition the same pending turn state machine.
	// 将损坏 turn 消化与正常 turn 分析串行化，因为两条路径都会推进同一个 pending turn 状态机。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Capture the exact pending row before the guarded update so commit-unknown reconciliation can prove this narrow state transition instead of guessing from status alone.
	// 在受保护更新前捕获精确 pending 行，让提交未知对账可以证明这次窄状态迁移，而不是只凭状态字段猜测。
	beforeTurn, beforeFound, err := s.loadTurnRecordByID(ctx, turnID)
	if err != nil {
		return fmt.Errorf("load corrupted turn before mark: %w", err)
	}

	// Preserve monotonic turn activity time when the durable row already carries a newer timestamp than the local clock sample.
	// 当长期 turn 行已有比本地时钟采样更新的时间戳时，保持 turn 活动时间单调不回退。
	updatedMs := nowMs
	if beforeFound && !beforeTurn.UpdatedAt.IsZero() {
		beforeUpdatedMs := beforeTurn.UpdatedAt.UTC().UnixMilli()
		if beforeUpdatedMs > updatedMs {
			updatedMs = beforeUpdatedMs
		}
	}

	statement := parameterizedMarkTurnAsCorruptedStatement(session.SessionID, turnID, updatedMs)
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredTurn, recoveredFound, reconcileErr := s.loadTurnRecordByID(ctx, turnID)
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "mark turn as corrupted",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if beforeFound && recoveredFound && sameCorruptedTurnMark(recoveredTurn, beforeTurn, session.SessionID, turnID, updatedMs) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "mark turn as corrupted",
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("mark turn as corrupted: %w", err)
	}
	if err := sqliteAutocommitRowsChangedDriftError("mark turn as corrupted", "mark turn as corrupted", result.RowsChanged, 1); err != nil {
		return fmt.Errorf("mark turn as corrupted: %w", err)
	}
	return nil
}

// LoadRecentSessionTurns returns the latest persisted turn rows for one session regardless of extracted status, ordered from oldest to newest after the final window is chosen.
// LoadRecentSessionTurns 用于返回某个 session 最近持久化的 turn 行，不区分 extracted 状态；最终结果按从旧到新排序。
func (s *Store) LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ?
ORDER BY id DESC
LIMIT ?
`, session.SessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent session turns: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	for left, right := 0, len(turns)-1; left < right; left, right = left+1, right-1 {
		turns[left], turns[right] = turns[right], turns[left]
	}
	return turns, nil
}

// LoadRecentSessionHistory returns the latest extracted turn summaries for one session, ordered from oldest to newest for prompt assembly.
// LoadRecentSessionHistory 用于返回某个 session 最近已提炼的 turn 精要，并按从旧到新的顺序排列，供提示词组装使用。
func (s *Store) LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ? AND extracted_status = ? AND LENGTH(TRIM(details)) > 0
ORDER BY id DESC
LIMIT ?
`, session.SessionID, logicdomain.TurnExtractedStatusDone, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent session history: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	for left, right := 0, len(turns)-1; left < right; left, right = left+1, right-1 {
		turns[left], turns[right] = turns[right], turns[left]
	}
	return turns, nil
}

// LoadTurnsByIDs returns one explicit set of dehydrated turn rows so memory-detail RPCs can inspect the exact persisted payloads behind recalled turn ids.
// LoadTurnsByIDs 用于返回一组明确指定的脱水 turn 行，让记忆详情 RPC 可以查看召回 turn id 背后的精确持久化载荷。
func (s *Store) LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	turnIDs = storageutil.NormalizeUint64List(turnIDs)
	if len(turnIDs) == 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	turnIDPlaceholders := sqlitePlaceholders(len(turnIDs))
	turnIDParams := sqliteUint64Params(turnIDs)
	rows, err := queryRows[turnRecordRow](s, ctx, fmt.Sprintf(`
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s)
ORDER BY id ASC
`, turnIDPlaceholders), turnIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query turns by ids: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	return turns, nil
}

// LoadTurnWindows returns the previous and next turn ids around each requested anchor turn inside the same session.
// LoadTurnWindows 用于返回每个请求锚点 turn 在同一 session 内前后相邻的 turn id。
func (s *Store) LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	turnIDs = storageutil.NormalizeUint64List(turnIDs)
	if len(turnIDs) == 0 || radius <= 0 {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
	turnIDPlaceholders := sqlitePlaceholders(len(turnIDs))
	queryParams := sqliteUint64Params(turnIDs)
	queryParams = append(queryParams, radius, radius)
	rows, err := queryRows[turnWindowRow](s, ctx, fmt.Sprintf(`
WITH ordered_turns AS (
  SELECT id, session_id,
         ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id ASC) AS rn
  FROM vmm_turn_records
),
target_turns AS (
  SELECT id AS target_id, session_id, rn AS target_rn
  FROM ordered_turns
  WHERE id IN (%s)
)
SELECT t.target_id,
       o.id AS turn_id,
       CAST(o.rn - t.target_rn AS INTEGER) AS relative_pos
FROM target_turns t
JOIN ordered_turns o
  ON o.session_id = t.session_id
 AND o.rn BETWEEN t.target_rn - ? AND t.target_rn + ?
 AND o.id <> t.target_id
ORDER BY t.target_id ASC, o.rn ASC
`, turnIDPlaceholders), queryParams...)
	if err != nil {
		return nil, fmt.Errorf("query turn windows: %w", err)
	}
	windows := make(map[uint64]logicdomain.TurnDetailWindow, len(turnIDs))
	for _, turnID := range turnIDs {
		windows[turnID] = logicdomain.TurnDetailWindow{TurnID: turnID}
	}
	for _, row := range rows {
		window := windows[row.TargetID]
		if row.RelativePos < 0 {
			window.PreviousTurnIDs = append(window.PreviousTurnIDs, row.TurnID)
		} else if row.RelativePos > 0 {
			window.NextTurnIDs = append(window.NextTurnIDs, row.TurnID)
		}
		windows[row.TargetID] = window
	}
	return windows, nil
}

// ListIdlePendingSessions returns sessions whose latest conversation activity is older than the idle timeout while still carrying pending turn rows.
// ListIdlePendingSessions 用于返回“最后会话时间已超过空闲阈值且仍存在待处理 turn”的 session 列表。
func (s *Store) ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error) {
	if idleTimeout <= 0 {
		return []logicdomain.SessionRef{}, nil
	}
	if limit <= 0 {
		limit = 128
	}
	cutoffMs := time.Now().UTC().Add(-idleTimeout).UnixMilli()
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE updated_timestamp <= ?
  AND EXISTS (
    SELECT 1
    FROM vmm_turn_records tr
    WHERE tr.session_id = vmm_sessions.id AND tr.extracted_status = ?
  )
ORDER BY updated_timestamp ASC, id ASC
LIMIT ?
`, cutoffMs, logicdomain.TurnExtractedStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query idle pending sessions: %w", err)
	}
	sessions := make([]logicdomain.SessionRef, 0, len(rows))
	for _, row := range rows {
		record := row.toDomain()
		sessions = append(sessions, logicdomain.SessionRef{
			SessionID:              record.ID,
			SessionKey:             record.SessionKey,
			UserID:                 record.UserID,
			TeamID:                 record.TeamID,
			SpaceID:                record.SpaceID,
			ProjectID:              record.ProjectID,
			TurnCount:              record.TurnCount,
			LastSummarizedID:       record.LastSummarizedID,
			LastCompactedTurnID:    record.LastCompactedTurnID,
			SummarizeContent:       record.SummarizeContent,
			SummarizeBudget:        record.SummarizeBudget,
			LastExtractObservedAt:  record.LastExtractObservedAt,
			LastExtractCompletedAt: record.LastExtractCompletedAt,
			LastCompactedAt:        record.LastCompactedAt,
			CreatedAt:              record.CreatedAt,
			UpdatedAt:              record.UpdatedAt,
		})
	}
	return sessions, nil
}

// AdvanceSessionExtractWindow stores the latest direct-memory observation window after one immediate turn analysis succeeds.
// AdvanceSessionExtractWindow 用于在一次即时 turn 分析成功后，记录最新的主动记忆观察窗口。
func (s *Store) AdvanceSessionExtractWindow(ctx context.Context, sessionID uint64, observedAt, completedAt time.Time) error {
	if sessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedAt.IsZero() && completedAt.IsZero() {
		return nil
	}
	observedMs := int64(0)
	completedMs := int64(0)
	if !observedAt.IsZero() {
		observedMs = observedAt.UTC().UnixMilli()
	}
	if !completedAt.IsZero() {
		completedMs = completedAt.UTC().UnixMilli()
	}
	updatedMs := completedMs
	if observedMs > updatedMs {
		updatedMs = observedMs
	}

	// Serialize session checkpoint writes with turn appends because both paths move the session activity timestamp that drives idle-session queue recovery.
	// 将 session 检查点写入与 turn 追加串行化，因为两条路径都会推进用于空闲 session 队列恢复的活动时间戳。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	statement := parameterizedSessionExtractWindowUpdateStatement(sessionID, observedMs, completedMs, updatedMs)
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredSession, reconcileErr := s.loadSessionByID(ctx, sessionID)
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "advance session extract window",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if sameAdvancedSessionExtractWindow(recoveredSession, sessionID, observedMs, completedMs, updatedMs) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "advance session extract window",
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("advance session extract window: %w", err)
	}
	if err := sqliteAutocommitRowsChangedDriftError("advance session extract window", "update session extract window", result.RowsChanged, 1); err != nil {
		return fmt.Errorf("advance session extract window: %w", err)
	}
	return nil
}
