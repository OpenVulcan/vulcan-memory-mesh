// management.go implements bounded human-management reads on the SQLite relational store.
// management.go 用于在 SQLite 关系存储上实现有界人工管理读取。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// managementSessionRow stores the joined SQLite projection for one session-management item.
// managementSessionRow 用于保存单条会话管理项目的 SQLite 联表投影。
type managementSessionRow struct {
	ID                            uint64 `json:"id"`
	SessionKey                    string `json:"session_key"`
	UserID                        uint64 `json:"user_id"`
	TeamID                        uint64 `json:"team_id"`
	SpaceID                       uint64 `json:"space_id"`
	ProjectID                     uint64 `json:"project_id"`
	TurnCount                     int    `json:"turn_count"`
	LastSummarizedID              uint64 `json:"last_summarized_id"`
	LastCompactedTurnID           uint64 `json:"last_compacted_turn_id"`
	SummarizeContent              string `json:"summarize_content"`
	SummarizeBudget               int    `json:"summarize_budget"`
	LastExtractObservedTimestamp  int64  `json:"last_extract_observed_timestamp"`
	LastExtractCompletedTimestamp int64  `json:"last_extract_completed_timestamp"`
	LastCompactedTimestamp        int64  `json:"last_compacted_timestamp"`
	CreatedTimestamp              int64  `json:"created_timestamp"`
	UpdatedTimestamp              int64  `json:"updated_timestamp"`
	UserName                      string `json:"user_name"`
	TeamName                      string `json:"team_name"`
	SpaceName                     string `json:"space_name"`
	ProjectName                   string `json:"project_name"`
	FirstTurnContent              string `json:"first_turn_content"`
	VisibleTurnCount              int    `json:"visible_turn_count"`
	PendingTurnCount              int    `json:"pending_turn_count"`
	ActiveMemoryCount             int    `json:"active_memory_count"`
	ProfileReferenceCount         int    `json:"profile_reference_count"`
	ManagementStatus              string `json:"management_status"`
}

// managementSessionStatusRow stores the optional lifecycle status joined to a durable session.
// managementSessionStatusRow 用于保存与长期会话关联的可选生命周期状态。
type managementSessionStatusRow struct {
	Status string `json:"status"`
}

// loadSessionMemoryStatus returns the authoritative lifecycle state and rejects unknown stored values.
// loadSessionMemoryStatus 用于返回权威生命周期状态，并拒绝未知的持久化值。
func (s *Store) loadSessionMemoryStatus(ctx context.Context, sessionID uint64) (logicdomain.SessionMemoryStatus, error) {
	rows, err := queryRows[managementSessionStatusRow](s, ctx, `
SELECT status FROM vmm_management_session_states WHERE session_id = ? LIMIT 1
`, sessionID)
	if err != nil {
		return "", fmt.Errorf("load sqlite session memory status: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionMemoryStatusActive, nil
	}
	status := logicdomain.SessionMemoryStatus(strings.TrimSpace(rows[0].Status))
	switch status {
	case logicdomain.SessionMemoryStatusActive,
		logicdomain.SessionMemoryStatusArchived,
		logicdomain.SessionMemoryStatusRecycled,
		logicdomain.SessionMemoryStatusForgotten:
		return status, nil
	default:
		return "", fmt.Errorf("unsupported sqlite session memory status %q for session %d", rows[0].Status, sessionID)
	}
}

// managementTurnRow stores one joined SQLite turn-management projection.
// managementTurnRow 用于保存一条 SQLite 回合管理联表投影。
type managementTurnRow struct {
	turnRecordRow
	UserID            uint64 `json:"user_id"`
	UserName          string `json:"user_name"`
	TeamName          string `json:"team_name"`
	SpaceName         string `json:"space_name"`
	ProjectName       string `json:"project_name"`
	DerivedMemories   int    `json:"derived_memories"`
	ProfileReferences int    `json:"profile_references"`
}

// toDomain converts one joined SQLite turn row into the storage-neutral management projection.
// toDomain 用于把一条 SQLite 回合联表行转换为存储无关管理投影。
func (r managementTurnRow) toDomain() logicdomain.ManagementTurnRecord {
	return logicdomain.ManagementTurnRecord{
		SessionTurnRecord: r.turnRecordRow.toDomain(), UserID: r.UserID, UserName: r.UserName,
		TeamName: r.TeamName, SpaceName: r.SpaceName, ProjectName: r.ProjectName,
		DerivedMemories: r.DerivedMemories, ProfileReferences: r.ProfileReferences,
	}
}

// toDomain converts one joined SQLite management row into the storage-neutral projection.
// toDomain 用于把一条 SQLite 管理联表行转换为存储无关投影。
func (r managementSessionRow) toDomain() logicdomain.ManagementSessionRecord {
	return logicdomain.ManagementSessionRecord{
		SessionRef: logicdomain.SessionRef{
			SessionID: r.ID, SessionKey: r.SessionKey, UserID: r.UserID, TeamID: r.TeamID,
			SpaceID: r.SpaceID, ProjectID: r.ProjectID, TurnCount: r.TurnCount,
			LastSummarizedID: r.LastSummarizedID, LastCompactedTurnID: r.LastCompactedTurnID,
			SummarizeContent: r.SummarizeContent, SummarizeBudget: r.SummarizeBudget,
			LastExtractObservedAt:  unixMilliToTime(r.LastExtractObservedTimestamp),
			LastExtractCompletedAt: unixMilliToTime(r.LastExtractCompletedTimestamp),
			LastCompactedAt:        unixMilliToTime(r.LastCompactedTimestamp),
			CreatedAt:              unixMilliToTime(r.CreatedTimestamp), UpdatedAt: unixMilliToTime(r.UpdatedTimestamp),
			UserName: r.UserName, TeamName: r.TeamName, SpaceName: r.SpaceName, ProjectName: r.ProjectName,
		},
		FirstTurnContent: r.FirstTurnContent, VisibleTurnCount: r.VisibleTurnCount,
		PendingTurnCount: r.PendingTurnCount, ActiveMemoryCount: r.ActiveMemoryCount,
		ProfileReferenceCount: r.ProfileReferenceCount, Status: r.ManagementStatus,
	}
}

// ListManagementSessions returns one deterministic cursor page with aggregate counts and human-readable hierarchy names.
// ListManagementSessions 用于返回包含聚合计数与人类可读层级名称的一页确定性游标结果。
func (s *Store) ListManagementSessions(ctx context.Context, query logicdomain.ManagementSessionQuery) (logicdomain.ManagementSessionPage, error) {
	if query.Limit <= 0 {
		return logicdomain.ManagementSessionPage{}, logicdomain.ValidationError{Field: "limit", Message: "must be positive"}
	}
	where := []string{"1 = 1"}
	params := make([]any, 0, 12)
	if query.UserID != 0 {
		where = append(where, "s.user_id = ?")
		params = append(params, query.UserID)
	}
	if query.ProjectID != 0 {
		where = append(where, "s.project_id = ?")
		params = append(params, query.ProjectID)
	}
	if query.Status != "all" {
		where = append(where, "COALESCE(ms.status, 'active') = ?")
		params = append(params, query.Status)
	}
	if query.CreatedFrom.UnixMilli() > 0 {
		where = append(where, "s.created_timestamp >= ?")
		params = append(params, query.CreatedFrom.UTC().UnixMilli())
	}
	if query.CreatedTo.UnixMilli() > 0 {
		where = append(where, "s.created_timestamp <= ?")
		params = append(params, query.CreatedTo.UTC().UnixMilli())
	}
	if query.UpdatedFrom.UnixMilli() > 0 {
		where = append(where, "s.updated_timestamp >= ?")
		params = append(params, query.UpdatedFrom.UTC().UnixMilli())
	}
	if query.UpdatedTo.UnixMilli() > 0 {
		where = append(where, "s.updated_timestamp <= ?")
		params = append(params, query.UpdatedTo.UTC().UnixMilli())
	}
	if search := strings.TrimSpace(query.Query); search != "" {
		pattern := "%" + search + "%"
		where = append(where, `(s.session_key LIKE ? OR u.name LIKE ? OR t.name LIKE ? OR sp.name LIKE ? OR p.name LIKE ? OR COALESCE(first_turn.dehydrated_content, '') LIKE ?)`)
		params = append(params, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	order := "DESC"
	comparison := "<"
	if query.Sort == "updated_asc" {
		order = "ASC"
		comparison = ">"
	}
	if query.Cursor.ID != 0 {
		where = append(where, fmt.Sprintf("(s.updated_timestamp %s ? OR (s.updated_timestamp = ? AND s.id %s ?))", comparison, comparison))
		cursorMs := query.Cursor.UpdatedAt.UTC().UnixMilli()
		params = append(params, cursorMs, cursorMs, query.Cursor.ID)
	}
	params = append(params, query.Limit+1)
	rows, err := queryRows[managementSessionRow](s, ctx, fmt.Sprintf(`
SELECT s.id, s.session_key, s.user_id, s.team_id, s.space_id, s.project_id,
       s.turn_count, s.last_summarized_id, s.last_compacted_turn_id,
       s.summarize_content, s.summarize_budget,
       s.last_extract_observed_timestamp, s.last_extract_completed_timestamp,
       s.last_compacted_timestamp, s.created_timestamp, s.updated_timestamp,
       u.name AS user_name, t.name AS team_name, sp.name AS space_name, p.name AS project_name,
       COALESCE(first_turn.dehydrated_content, '') AS first_turn_content,
       (SELECT COUNT(*) FROM vmm_turn_records tr WHERE tr.session_id = s.id) AS visible_turn_count,
       (SELECT COUNT(*) FROM vmm_turn_records tr WHERE tr.session_id = s.id AND tr.extracted_status = %d) AS pending_turn_count,
       (SELECT COUNT(*) FROM vmm_memory_nodes mn WHERE mn.origin_session_id = s.id AND mn.memory_status = %d AND (mn.expires_timestamp = 0 OR mn.expires_timestamp > %d)) AS active_memory_count,
       (SELECT COUNT(*) FROM vmm_profile_nodes pn JOIN vmm_turn_records tr ON tr.id = pn.turn_id WHERE tr.session_id = s.id AND pn.profile_status = %d) AS profile_reference_count,
       COALESCE(ms.status, 'active') AS management_status
FROM vmm_sessions s
JOIN vmm_users u ON u.id = s.user_id
JOIN vmm_teams t ON t.id = s.team_id
JOIN vmm_spaces sp ON sp.id = s.space_id
JOIN vmm_projects p ON p.id = s.project_id
LEFT JOIN vmm_turn_records first_turn ON first_turn.id = (
  SELECT tr.id FROM vmm_turn_records tr WHERE tr.session_id = s.id ORDER BY tr.id ASC LIMIT 1
)
LEFT JOIN vmm_management_session_states ms ON ms.session_id = s.id
WHERE %s
ORDER BY s.updated_timestamp %s, s.id %s
LIMIT ?
`, logicdomain.TurnExtractedStatusPending, logicdomain.MemoryStatusActive, time.Now().UTC().UnixMilli(), logicdomain.ProfileStatusActive, strings.Join(where, " AND "), order, order), params...)
	if err != nil {
		return logicdomain.ManagementSessionPage{}, fmt.Errorf("list management sessions: %w", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]logicdomain.ManagementSessionRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toDomain())
	}
	page := logicdomain.ManagementSessionPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		page.NextCursor = logicdomain.ManagementCursor{UpdatedAt: last.UpdatedAt, ID: last.SessionID}
	}
	return page, nil
}

// GetManagementSession returns one joined management projection by session identifier.
// GetManagementSession 用于按会话标识返回一条联表管理投影。
func (s *Store) GetManagementSession(ctx context.Context, sessionID uint64) (logicdomain.ManagementSessionRecord, error) {
	rows, queryErr := queryRows[managementSessionRow](s, ctx, fmt.Sprintf(`
SELECT s.id, s.session_key, s.user_id, s.team_id, s.space_id, s.project_id,
       s.turn_count, s.last_summarized_id, s.last_compacted_turn_id, s.summarize_content, s.summarize_budget,
       s.last_extract_observed_timestamp, s.last_extract_completed_timestamp, s.last_compacted_timestamp,
       s.created_timestamp, s.updated_timestamp, u.name AS user_name, t.name AS team_name,
       sp.name AS space_name, p.name AS project_name,
       COALESCE(first_turn.dehydrated_content, '') AS first_turn_content,
       (SELECT COUNT(*) FROM vmm_turn_records tr WHERE tr.session_id = s.id) AS visible_turn_count,
       (SELECT COUNT(*) FROM vmm_turn_records tr WHERE tr.session_id = s.id AND tr.extracted_status = %d) AS pending_turn_count,
       (SELECT COUNT(*) FROM vmm_memory_nodes mn WHERE mn.origin_session_id = s.id AND mn.memory_status = %d AND (mn.expires_timestamp = 0 OR mn.expires_timestamp > %d)) AS active_memory_count,
       (SELECT COUNT(*) FROM vmm_profile_nodes pn JOIN vmm_turn_records tr ON tr.id = pn.turn_id WHERE tr.session_id = s.id AND pn.profile_status = %d) AS profile_reference_count,
       COALESCE(ms.status, 'active') AS management_status
FROM vmm_sessions s
JOIN vmm_users u ON u.id = s.user_id JOIN vmm_teams t ON t.id = s.team_id
JOIN vmm_spaces sp ON sp.id = s.space_id JOIN vmm_projects p ON p.id = s.project_id
LEFT JOIN vmm_turn_records first_turn ON first_turn.id = (SELECT tr.id FROM vmm_turn_records tr WHERE tr.session_id = s.id ORDER BY tr.id ASC LIMIT 1)
LEFT JOIN vmm_management_session_states ms ON ms.session_id = s.id
WHERE s.id = ? LIMIT 1
`, logicdomain.TurnExtractedStatusPending, logicdomain.MemoryStatusActive, time.Now().UTC().UnixMilli(), logicdomain.ProfileStatusActive), sessionID)
	if queryErr != nil {
		return logicdomain.ManagementSessionRecord{}, fmt.Errorf("get management session: %w", queryErr)
	}
	if len(rows) == 0 {
		return logicdomain.ManagementSessionRecord{}, logicdomain.NotFoundError{Resource: "session", Message: "session does not exist"}
	}
	return rows[0].toDomain(), nil
}

// ListManagementTurns returns one stable ascending page of turns for a session.
// ListManagementTurns 用于返回某个会话下一页稳定升序回合。
func (s *Store) ListManagementTurns(ctx context.Context, query logicdomain.ManagementTurnQuery) (logicdomain.ManagementTurnPage, error) {
	where := []string{"1 = 1"}
	params := make([]any, 0, 12)
	if query.SessionID != 0 {
		where = append(where, "tr.session_id = ?")
		params = append(params, query.SessionID)
	}
	if query.UserID != 0 {
		where = append(where, "s.user_id = ?")
		params = append(params, query.UserID)
	}
	if query.ProjectID != 0 {
		where = append(where, "tr.project_id = ?")
		params = append(params, query.ProjectID)
	}
	if !query.CreatedFrom.IsZero() {
		where = append(where, "tr.created_timestamp >= ?")
		params = append(params, query.CreatedFrom.UTC().UnixMilli())
	}
	if !query.CreatedTo.IsZero() {
		where = append(where, "tr.created_timestamp <= ?")
		params = append(params, query.CreatedTo.UTC().UnixMilli())
	}
	if search := strings.TrimSpace(query.Query); search != "" {
		pattern := "%" + search + "%"
		where = append(where, "(tr.dehydrated_content LIKE ? OR tr.details LIKE ?)")
		params = append(params, pattern, pattern)
	}
	if query.Status == "pending" {
		where = append(where, "tr.extracted_status = ?")
		params = append(params, logicdomain.TurnExtractedStatusPending)
	} else if query.Status == "extracted" {
		where = append(where, "tr.extracted_status = ?")
		params = append(params, logicdomain.TurnExtractedStatusDone)
	} else if query.Status == "passed" {
		where = append(where, "tr.extracted_status = ?")
		params = append(params, logicdomain.TurnExtractedStatusPassed)
	}
	if query.HasMemory != nil {
		operator := "NOT EXISTS"
		if *query.HasMemory {
			operator = "EXISTS"
		}
		where = append(where, operator+" (SELECT 1 FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id)")
	}
	if query.HasProfile != nil {
		operator := "NOT EXISTS"
		if *query.HasProfile {
			operator = "EXISTS"
		}
		where = append(where, operator+" (SELECT 1 FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id)")
	}
	order := "ASC"
	comparison := ">"
	if query.Sort == "id_desc" {
		order = "DESC"
		comparison = "<"
	}
	if query.CursorID != 0 {
		where = append(where, "tr.id "+comparison+" ?")
		params = append(params, query.CursorID)
	}
	params = append(params, query.Limit+1)
	rows, err := queryRows[managementTurnRow](s, ctx, fmt.Sprintf(`
SELECT tr.id, tr.session_id, tr.project_id, tr.dehydrated_content, tr.dehydrated_budget,
       tr.extracted_status, tr.details, tr.details_budget, tr.created_timestamp, tr.updated_timestamp,
       s.user_id, u.name AS user_name, t.name AS team_name, sp.name AS space_name, p.name AS project_name,
       (SELECT COUNT(*) FROM vmm_memory_nodes mn WHERE mn.source_turn_id = tr.id) AS derived_memories,
       (SELECT COUNT(*) FROM vmm_profile_nodes pn WHERE pn.turn_id = tr.id) AS profile_references
FROM vmm_turn_records tr
JOIN vmm_sessions s ON s.id = tr.session_id
JOIN vmm_users u ON u.id = s.user_id JOIN vmm_teams t ON t.id = s.team_id
JOIN vmm_spaces sp ON sp.id = s.space_id JOIN vmm_projects p ON p.id = tr.project_id
WHERE %s ORDER BY tr.id %s LIMIT ?
`, strings.Join(where, " AND "), order), params...)
	if err != nil {
		return logicdomain.ManagementTurnPage{}, fmt.Errorf("list management turns: %w", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]logicdomain.ManagementTurnRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toDomain())
	}
	page := logicdomain.ManagementTurnPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextCursorID = items[len(items)-1].ID
	}
	return page, nil
}

// GetManagementTurn returns one persisted turn by identifier.
// GetManagementTurn 用于按标识返回一条持久化回合。
func (s *Store) GetManagementTurn(ctx context.Context, turnID uint64) (logicdomain.ManagementTurnRecord, error) {
	page, err := s.ListManagementTurns(ctx, logicdomain.ManagementTurnQuery{CursorID: turnID - 1, Limit: 1, Sort: "id_asc", Status: "all"})
	if err != nil {
		return logicdomain.ManagementTurnRecord{}, err
	}
	if len(page.Items) == 0 || page.Items[0].ID != turnID {
		return logicdomain.ManagementTurnRecord{}, logicdomain.NotFoundError{Resource: "turn", Message: "turn does not exist"}
	}
	return page.Items[0], nil
}
