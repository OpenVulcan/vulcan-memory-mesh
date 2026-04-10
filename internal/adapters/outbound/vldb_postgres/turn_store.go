// turn_store.go implements PostgreSQL-backed session-turn persistence, turn lookup, and compact-boundary maintenance for combined mode.
// turn_store.go 用于实现组合模式下基于 PostgreSQL 的 session-turn 持久化、turn 查询与 compact 边界维护。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// AppendTurnRecord persists one cleaned turn into the resolved session, increments the session turn counter, and returns the new durable identifier.
// AppendTurnRecord 用于把一条清洗后的 turn 写入已解析 session、递增会话 turn 计数，并返回新的长期标识。
func (r *turnRepository) AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	if strings.TrimSpace(turn.UserContent) == "" && strings.TrimSpace(turn.AssistantContent) == "" && len(turn.Timeline) == 0 {
		return logicdomain.PersistedTurnRecord{}, nil
	}
	if session.SessionID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}

	dehydratedContent, dehydratedBudget, err := buildDehydratedTurn(turn)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	createdAt := chooseNonZeroTime(turn.CreatedAt, time.Now().UTC())

	// Keep the turn insert and session counter update inside one explicit PostgreSQL transaction so combined mode never exposes a half-written turn window.
	// 把 turn 插入和 session 计数更新放进同一个显式 PostgreSQL 事务，确保组合模式不会暴露“半写入”的 turn 窗口。
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("begin postgres turn append tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	insertSQL := fmt.Sprintf(`
INSERT INTO %s (
	session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, '', 0, $6, $6
)
RETURNING id, session_id, project_id, dehydrated_budget, created_at, updated_at
`, r.turnsTable())
	var persisted logicdomain.PersistedTurnRecord
	if err := tx.QueryRow(
		callCtx,
		strings.TrimSpace(insertSQL),
		int64(session.SessionID),
		int64(session.ProjectID),
		dehydratedContent,
		dehydratedBudget,
		logicdomain.TurnExtractedStatusPending,
		createdAt.UTC(),
	).Scan(
		&persisted.ID,
		&persisted.SessionID,
		&persisted.ProjectID,
		&persisted.DehydratedBudget,
		&persisted.CreatedAt,
		&persisted.UpdatedAt,
	); err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("insert postgres turn record: %w", err)
	}

	updateSessionSQL := fmt.Sprintf(`
UPDATE %s
SET turn_count = turn_count + 1,
    summarize_budget = summarize_budget + $1,
    updated_at = CASE
        WHEN updated_at < $2 THEN $2
        ELSE updated_at
    END
WHERE id = $3
`, r.sessionsTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateSessionSQL), dehydratedBudget, createdAt.UTC(), int64(session.SessionID)); err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("update postgres session turn counters: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("commit postgres turn append tx: %w", err)
	}
	persisted.CreatedAt = persisted.CreatedAt.UTC()
	persisted.UpdatedAt = persisted.UpdatedAt.UTC()
	return persisted, nil
}

// LoadPendingSessionTurns returns the oldest not-yet-extracted turn rows for one session so queued post-action workers can drain durable work in order.
// LoadPendingSessionTurns 用于返回某个 session 中尚未提炼的最早 turn 行，让排队的 post-action 工作器按持久化顺序消化任务。
func (r *turnRepository) LoadPendingSessionTurns(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	sqlText := fmt.Sprintf(`
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM %s
WHERE session_id = $1 AND extracted_status = $2
ORDER BY id ASC
`, r.turnsTable())
	rows, err := r.queryTurnRecords(ctx, strings.TrimSpace(sqlText), int64(session.SessionID), logicdomain.TurnExtractedStatusPending)
	if err != nil {
		return nil, fmt.Errorf("query postgres pending session turns: %w", err)
	}
	return rows, nil
}

// LoadRecentSessionTurns returns the latest persisted turn rows for one session regardless of extracted status, ordered from oldest to newest after the final window is chosen.
// LoadRecentSessionTurns 用于返回某个 session 最近持久化的 turn 行，不区分 extracted 状态；最终结果按从旧到新排序。
func (r *turnRepository) LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM (
	SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
	FROM %s
	WHERE session_id = $1
	ORDER BY id DESC
	LIMIT $2
) AS recent_turns
ORDER BY id ASC
`, r.turnsTable())
	rows, err := r.queryTurnRecords(ctx, strings.TrimSpace(sqlText), int64(session.SessionID), limit)
	if err != nil {
		return nil, fmt.Errorf("query postgres recent session turns: %w", err)
	}
	return rows, nil
}

// LoadRecentSessionHistory returns the latest extracted turn summaries for one session, ordered from oldest to newest for prompt assembly.
// LoadRecentSessionHistory 用于返回某个 session 最近已提炼的 turn 精要，并按从旧到新排序，供提示词组装使用。
func (r *turnRepository) LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM (
	SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
	FROM %s
	WHERE session_id = $1
	  AND extracted_status = $2
	  AND LENGTH(BTRIM(details)) > 0
	ORDER BY id DESC
	LIMIT $3
) AS recent_history
ORDER BY id ASC
`, r.turnsTable())
	rows, err := r.queryTurnRecords(ctx, strings.TrimSpace(sqlText), int64(session.SessionID), logicdomain.TurnExtractedStatusDone, limit)
	if err != nil {
		return nil, fmt.Errorf("query postgres recent session history: %w", err)
	}
	return rows, nil
}

// LoadTurnsByIDs returns one explicit set of dehydrated turn rows so memory-detail RPCs can inspect the exact persisted payload behind recalled turn ids.
// LoadTurnsByIDs 用于返回一组明确指定的脱水 turn 行，让记忆详情 RPC 可以查看召回 turn id 背后的精确持久化载荷。
func (r *turnRepository) LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	turnIDs = normalizeUint64List(turnIDs)
	if len(turnIDs) == 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	sqlText := fmt.Sprintf(`
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM %s
WHERE id = ANY($1)
ORDER BY id ASC
`, r.turnsTable())
	rows, err := r.queryTurnRecords(ctx, strings.TrimSpace(sqlText), toInt64List(turnIDs))
	if err != nil {
		return nil, fmt.Errorf("query postgres turns by ids: %w", err)
	}
	return rows, nil
}

// LoadTurnWindows returns the previous and next turn ids around each requested anchor turn inside the same session.
// LoadTurnWindows 用于返回每个请求锚点 turn 在同一 session 内前后相邻的 turn id。
func (r *turnRepository) LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	turnIDs = normalizeUint64List(turnIDs)
	if len(turnIDs) == 0 || radius <= 0 {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
	sqlText := fmt.Sprintf(`
WITH ordered_turns AS (
	SELECT id, session_id,
	       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id ASC) AS rn
	FROM %s
),
target_turns AS (
	SELECT id AS target_id, session_id, rn AS target_rn
	FROM ordered_turns
	WHERE id = ANY($1)
)
SELECT t.target_id,
       o.id AS turn_id,
       CAST(o.rn - t.target_rn AS INTEGER) AS relative_pos
FROM target_turns t
JOIN ordered_turns o
  ON o.session_id = t.session_id
 AND o.rn BETWEEN t.target_rn - $2 AND t.target_rn + $2
 AND o.id <> t.target_id
ORDER BY t.target_id ASC, o.rn ASC
`, r.turnsTable())
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, strings.TrimSpace(sqlText), toInt64List(turnIDs), radius)
	if err != nil {
		return nil, fmt.Errorf("query postgres turn windows: %w", err)
	}
	defer rows.Close()

	windows := make(map[uint64]logicdomain.TurnDetailWindow, len(turnIDs))
	for _, turnID := range turnIDs {
		windows[turnID] = logicdomain.TurnDetailWindow{TurnID: turnID}
	}
	for rows.Next() {
		var (
			targetID    uint64
			turnID      uint64
			relativePos int
		)
		if err := rows.Scan(&targetID, &turnID, &relativePos); err != nil {
			return nil, fmt.Errorf("scan postgres turn window row: %w", err)
		}
		window := windows[targetID]
		if relativePos < 0 {
			window.PreviousTurnIDs = append(window.PreviousTurnIDs, turnID)
		} else if relativePos > 0 {
			window.NextTurnIDs = append(window.NextTurnIDs, turnID)
		}
		windows[targetID] = window
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres turn window rows: %w", err)
	}
	return windows, nil
}

// ListIdlePendingSessions returns sessions whose latest conversation activity is older than the idle timeout while still carrying pending turn rows.
// ListIdlePendingSessions 用于返回"最后会话时间已超过空闲阈值且仍存在待处理 turn"的 session 列表。
func (r *turnRepository) ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if idleTimeout <= 0 {
		return []logicdomain.SessionRef{}, nil
	}
	if limit <= 0 {
		limit = 128
	}
	cutoff := time.Now().UTC().Add(-idleTimeout)
	sqlText := fmt.Sprintf(`
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_at, last_extract_completed_at, last_compacted_at,
       created_at, updated_at
FROM %s
WHERE updated_at <= $1
  AND EXISTS (
    SELECT 1
    FROM %s tr
    WHERE tr.session_id = %s.id AND tr.extracted_status = $2
  )
ORDER BY updated_at ASC, id ASC
LIMIT $3
`, r.sessionsTable(), r.turnsTable(), r.sessionsTable())
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, strings.TrimSpace(sqlText), cutoff, logicdomain.TurnExtractedStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query postgres idle pending sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]logicdomain.SessionRef, 0)
	for rows.Next() {
		var row sessionScanRow
		if err := rows.Scan(
			&row.ID,
			&row.SessionKey,
			&row.UserID,
			&row.TeamID,
			&row.SpaceID,
			&row.ProjectID,
			&row.TurnCount,
			&row.LastSummarizedID,
			&row.LastCompactedTurnID,
			&row.SummarizeContent,
			&row.SummarizeBudget,
			&row.LastExtractObservedAt,
			&row.LastExtractCompletedAt,
			&row.LastCompactedAt,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres idle pending session: %w", err)
		}
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres idle pending sessions: %w", err)
	}
	return sessions, nil
}

// AdvanceSessionExtractWindow stores the latest direct-memory observation window after one immediate turn analysis succeeds.
// AdvanceSessionExtractWindow 用于在一次即时 turn 分析成功后，记录最新的主动记忆观察窗口。
func (r *turnRepository) AdvanceSessionExtractWindow(ctx context.Context, sessionID uint64, observedAt, completedAt time.Time) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if sessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedAt.IsZero() && completedAt.IsZero() {
		return nil
	}
	observedAt = chooseNonZeroTime(observedAt, time.Time{})
	completedAt = chooseNonZeroTime(completedAt, time.Time{})
	updatedAt := completedAt
	if updatedAt.IsZero() || (!observedAt.IsZero() && observedAt.After(updatedAt)) {
		updatedAt = observedAt
	}
	sqlText := fmt.Sprintf(`
UPDATE %s
SET last_extract_observed_at = CASE
        WHEN $1::timestamptz IS NULL THEN last_extract_observed_at
        WHEN last_extract_observed_at IS NULL OR last_extract_observed_at < $1 THEN $1
        ELSE last_extract_observed_at
    END,
    last_extract_completed_at = CASE
        WHEN $2::timestamptz IS NULL THEN last_extract_completed_at
        WHEN last_extract_completed_at IS NULL OR last_extract_completed_at < $2 THEN $2
        ELSE last_extract_completed_at
    END,
    updated_at = CASE
        WHEN $3::timestamptz IS NULL THEN updated_at
        WHEN updated_at < $3 THEN $3
        ELSE updated_at
    END
WHERE id = $4
`, r.sessionsTable())
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	if _, err := r.shared.pool.Exec(callCtx, strings.TrimSpace(sqlText), nullableTime(observedAt), nullableTime(completedAt), nullableTime(updatedAt), int64(sessionID)); err != nil {
		return fmt.Errorf("advance postgres session extract window: %w", err)
	}
	return nil
}

// MarkSessionCompacted stores the latest persisted turn id as the current compact boundary for one resolved session and keeps repeated calls idempotent.
// MarkSessionCompacted 用于把某个已解析 session 的最新持久化 turn id 记录为当前 compact 边界，并保持重复调用幂等。
func (r *turnRepository) MarkSessionCompacted(ctx context.Context, session logicdomain.SessionRef, compactedAt time.Time) (uint64, bool, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return 0, false, fmt.Errorf("postgres store is not initialized")
	}
	if session.SessionID == 0 {
		return 0, false, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if compactedAt.IsZero() {
		compactedAt = time.Now().UTC()
	}

	// Keep the latest-turn read and compact-boundary write inside one transaction so compaction acknowledgements cannot race with fresh turn inserts.
	// 把"读取最新 turn"和"写入 compact 边界"放进同一个事务，避免 compact 确认与新的 turn 插入发生竞态。
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return 0, false, fmt.Errorf("begin postgres compact tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	var latestTurnID uint64
	latestSQL := fmt.Sprintf(`SELECT COALESCE(MAX(id), 0) AS latest_turn_id FROM %s WHERE session_id = $1`, r.turnsTable())
	if err := tx.QueryRow(callCtx, latestSQL, int64(session.SessionID)).Scan(&latestTurnID); err != nil {
		return 0, false, fmt.Errorf("query postgres latest session turn: %w", err)
	}
	if latestTurnID == 0 {
		return 0, false, nil
	}

	var currentBoundary uint64
	boundarySQL := fmt.Sprintf(`SELECT last_compacted_turn_id FROM %s WHERE id = $1 FOR UPDATE`, r.sessionsTable())
	if err := tx.QueryRow(callCtx, boundarySQL, int64(session.SessionID)).Scan(&currentBoundary); err != nil {
		return 0, false, fmt.Errorf("lock postgres session compact boundary: %w", err)
	}
	if latestTurnID == currentBoundary {
		return latestTurnID, false, nil
	}

	updateSQL := fmt.Sprintf(`
UPDATE %s
SET last_compacted_turn_id = $1,
    last_compacted_at = $2,
    updated_at = CASE
        WHEN updated_at < $2 THEN $2
        ELSE updated_at
    END
WHERE id = $3
`, r.sessionsTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateSQL), int64(latestTurnID), compactedAt.UTC(), int64(session.SessionID)); err != nil {
		return 0, false, fmt.Errorf("update postgres session compact boundary: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return 0, false, fmt.Errorf("commit postgres compact tx: %w", err)
	}
	return latestTurnID, true, nil
}

// queryTurnRecords executes one PostgreSQL turn query and maps the result rows into the shared durable session-turn model.
// queryTurnRecords 用于执行一条 PostgreSQL turn 查询，并把结果行映射成共享的长期 session-turn 模型。
func (r *turnRepository) queryTurnRecords(ctx context.Context, sqlText string, args ...any) ([]logicdomain.SessionTurnRecord, error) {
	return r.queryTurnRecordsWithQueryer(ctx, r.shared.pool, sqlText, args...)
}

// queryTurnRecordsWithQueryer executes one PostgreSQL turn query against either the shared pool or one transaction and maps the result rows into the shared durable session-turn model.
// queryTurnRecordsWithQueryer 用于通过连接池或事务执行 PostgreSQL turn 查询，并把结果行映射成共享的长期 session-turn 模型。
func (r *turnRepository) queryTurnRecordsWithQueryer(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]logicdomain.SessionTurnRecord, error) {
	callCtx, cancel := r.turnQueryContext(ctx)
	defer cancel()
	rows, err := q.Query(callCtx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	turns := make([]logicdomain.SessionTurnRecord, 0)
	for rows.Next() {
		var row turnRecordScanRow
		if err := rows.Scan(
			&row.ID,
			&row.SessionID,
			&row.ProjectID,
			&row.DehydratedContent,
			&row.DehydratedBudget,
			&row.ExtractedStatus,
			&row.Details,
			&row.DetailsBudget,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres turn row: %w", err)
		}
		turns = append(turns, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres turn rows: %w", err)
	}
	return turns, nil
}

// AppendTurnRecord delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// AppendTurnRecord 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	return s.repos.turns.AppendTurnRecord(ctx, session, turn)
}

// LoadPendingSessionTurns delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// LoadPendingSessionTurns 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadPendingSessionTurns(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	return s.repos.turns.LoadPendingSessionTurns(ctx, session)
}

// LoadRecentSessionTurns delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// LoadRecentSessionTurns 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	return s.repos.turns.LoadRecentSessionTurns(ctx, session, limit)
}

// LoadRecentSessionHistory delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// LoadRecentSessionHistory 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	return s.repos.turns.LoadRecentSessionHistory(ctx, session, limit)
}

// LoadTurnsByIDs delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// LoadTurnsByIDs 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	return s.repos.turns.LoadTurnsByIDs(ctx, turnIDs)
}

// LoadTurnWindows delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// LoadTurnWindows 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	return s.repos.turns.LoadTurnWindows(ctx, turnIDs, radius)
}

// ListIdlePendingSessions delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// ListIdlePendingSessions 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error) {
	return s.repos.turns.ListIdlePendingSessions(ctx, idleTimeout, limit)
}

// AdvanceSessionExtractWindow delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// AdvanceSessionExtractWindow 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) AdvanceSessionExtractWindow(ctx context.Context, sessionID uint64, observedAt, completedAt time.Time) error {
	return s.repos.turns.AdvanceSessionExtractWindow(ctx, sessionID, observedAt, completedAt)
}

// MarkSessionCompacted delegates to the turn repository so existing port interfaces continue to compile while ownership moves inward.
// MarkSessionCompacted 用于委托给 turn repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) MarkSessionCompacted(ctx context.Context, session logicdomain.SessionRef, compactedAt time.Time) (uint64, bool, error) {
	return s.repos.turns.MarkSessionCompacted(ctx, session, compactedAt)
}
