// session_compact.go implements session-level compact-boundary persistence on top of the SQLite durable store.
// session_compact.go 用于在 SQLite 长期存储之上实现 session 级 compact 边界持久化。
package vldb_sqlite

import (
	"context"
	"fmt"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// latestTurnRow mirrors the MAX(id) session-turn query used to locate the newest persisted turn before compact writes update the session boundary.
// latestTurnRow 用于映射 session 内 MAX(id) 查询结果，在 compact 写入前定位最新持久化 turn。
type latestTurnRow struct {
	LatestTurnID uint64 `json:"latest_turn_id"`
}

// sameCompactedSessionBoundary reports whether a read-back session row proves the compact-boundary update from this call became durable.
// sameCompactedSessionBoundary 用于判断回读 session 行是否能够证明本次 compact 边界更新已经持久化。
func sameCompactedSessionBoundary(row logicdomain.SessionRecord, sessionID, latestTurnID uint64, compactedMs int64) bool {
	return row.ID == sessionID &&
		row.LastCompactedTurnID == latestTurnID &&
		sessionTimestampReached(row.LastCompactedAt, compactedMs) &&
		sessionTimestampReached(row.UpdatedAt, compactedMs)
}

// parameterizedSessionCompactBoundaryUpdateStatement builds the single SQLite statement that advances one session compact boundary with typed params.
// parameterizedSessionCompactBoundaryUpdateStatement 用于构造推进单个 session compact 边界的 SQLite 单语句强类型参数写入。
func parameterizedSessionCompactBoundaryUpdateStatement(sessionID, latestTurnID uint64, compactedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_sessions
SET last_compacted_turn_id = ?,
    last_compacted_timestamp = ?,
    updated_timestamp = CASE
      WHEN updated_timestamp < ? THEN ? ELSE updated_timestamp
    END
WHERE id = ?
`,
		Params: []any{latestTurnID, compactedMs, compactedMs, compactedMs, sessionID},
	}
}

// MarkSessionCompacted stores the latest persisted turn id as the current compact boundary for one resolved session and keeps repeated calls idempotent.
// MarkSessionCompacted 用于把某个已解析 session 的最新持久化 turn id 记录为当前 compact 边界，并保持重复调用幂等。
func (s *Store) MarkSessionCompacted(ctx context.Context, session logicdomain.SessionRef, compactedAt time.Time) (uint64, bool, error) {
	if s == nil {
		return 0, false, fmt.Errorf("sqlite store is nil")
	}
	if session.SessionID == 0 {
		return 0, false, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if compactedAt.IsZero() {
		compactedAt = time.Now().UTC()
	}

	// Serialize both the latest-turn read and the boundary write so compact acknowledgements cannot observe a stale MAX(id) while post-action is appending a newer turn.
	// 串行化“读取最新 turn”和“写入 compact 边界”这两个步骤，避免 compact 确认在 post-action 正在追加更晚 turn 时读到过期的 MAX(id)。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[latestTurnRow](s, ctx, `
SELECT COALESCE(MAX(id), 0) AS latest_turn_id
FROM vmm_turn_records
WHERE session_id = ?
`, session.SessionID)
	if err != nil {
		return 0, false, fmt.Errorf("query latest session turn: %w", err)
	}
	latestTurnID := uint64(0)
	if len(rows) > 0 {
		latestTurnID = rows[0].LatestTurnID
	}
	if latestTurnID == 0 {
		return 0, false, nil
	}

	// Read the durable session boundary under the same write lock because SessionRef is only a caller snapshot and may be stale by the time compaction runs.
	// 在同一个写锁内读取持久化 session 边界，因为 SessionRef 只是调用方快照，compact 执行时可能已经过期。
	currentSession, err := s.loadSessionByID(ctx, session.SessionID)
	if err != nil {
		return 0, false, fmt.Errorf("load session compact boundary: %w", err)
	}
	if latestTurnID == currentSession.LastCompactedTurnID {
		return latestTurnID, false, nil
	}

	compactedMs := compactedAt.UTC().UnixMilli()
	statement := parameterizedSessionCompactBoundaryUpdateStatement(session.SessionID, latestTurnID, compactedMs)
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredSession, reconcileErr := s.loadSessionByID(ctx, session.SessionID)
			if reconcileErr != nil {
				return latestTurnID, true, logicdomain.OutcomeUncertainError{
					Operation: "mark session compacted",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if sameCompactedSessionBoundary(recoveredSession, session.SessionID, latestTurnID, compactedMs) {
				return latestTurnID, true, nil
			}
			return latestTurnID, true, logicdomain.OutcomeUncertainError{
				Operation: "mark session compacted",
				Message:   err.Error(),
			}
		}
		return 0, false, fmt.Errorf("update session compact boundary: %w", err)
	}
	if err := sqliteAutocommitRowsChangedDriftError("mark session compacted", "update session compact boundary", result.RowsChanged, 1); err != nil {
		return 0, false, fmt.Errorf("update session compact boundary: %w", err)
	}
	return latestTurnID, true, nil
}
