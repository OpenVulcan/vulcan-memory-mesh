// management.go implements bounded human-management reads on the combined PostgreSQL store.
// management.go 用于在 PostgreSQL 组合存储上实现有界人工管理读取。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postgresManagementSessionRow stores one joined PostgreSQL management projection.
// postgresManagementSessionRow 用于保存一条 PostgreSQL 管理联表投影。
type postgresManagementSessionRow struct {
	Record           logicdomain.ManagementSessionRecord
	FirstTurnContent string
}

// loadPostgresSessionMemoryStatus returns the authoritative lifecycle state and rejects unknown stored values.
// loadPostgresSessionMemoryStatus 用于返回权威生命周期状态，并拒绝未知的持久化值。
func (s *Store) loadPostgresSessionMemoryStatus(ctx context.Context, sessionID uint64) (logicdomain.SessionMemoryStatus, error) {
	tables := s.managementTables()
	var raw string
	err := s.shared.pool.QueryRow(ctx, fmt.Sprintf(`SELECT status FROM %s WHERE session_id = $1 LIMIT 1`, tables.sessionStates), int64(sessionID)).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.SessionMemoryStatusActive, nil
	}
	if err != nil {
		return "", fmt.Errorf("load postgres session memory status: %w", err)
	}
	status := logicdomain.SessionMemoryStatus(strings.TrimSpace(raw))
	switch status {
	case logicdomain.SessionMemoryStatusActive,
		logicdomain.SessionMemoryStatusArchived,
		logicdomain.SessionMemoryStatusRecycled,
		logicdomain.SessionMemoryStatusForgotten:
		return status, nil
	default:
		return "", fmt.Errorf("unsupported postgres session memory status %q for session %d", raw, sessionID)
	}
}

// scanPostgresManagementSession scans one shared management projection from a PostgreSQL row.
// scanPostgresManagementSession 用于从 PostgreSQL 行扫描一条共享管理投影。
func scanPostgresManagementSession(scanner interface{ Scan(...any) error }) (logicdomain.ManagementSessionRecord, error) {
	record := logicdomain.ManagementSessionRecord{}
	var lastExtractObservedAt *time.Time
	var lastExtractCompletedAt *time.Time
	var lastCompactedAt *time.Time
	err := scanner.Scan(
		&record.SessionID, &record.SessionKey, &record.UserID, &record.TeamID, &record.SpaceID, &record.ProjectID,
		&record.TurnCount, &record.LastSummarizedID, &record.LastCompactedTurnID,
		&record.SummarizeContent, &record.SummarizeBudget, &lastExtractObservedAt,
		&lastExtractCompletedAt, &lastCompactedAt, &record.CreatedAt, &record.UpdatedAt,
		&record.UserName, &record.TeamName, &record.SpaceName, &record.ProjectName,
		&record.FirstTurnContent, &record.VisibleTurnCount, &record.PendingTurnCount,
		&record.ActiveMemoryCount, &record.ProfileReferenceCount, &record.Status,
	)
	if err != nil {
		return logicdomain.ManagementSessionRecord{}, err
	}
	if lastExtractObservedAt != nil {
		record.LastExtractObservedAt = lastExtractObservedAt.UTC()
	}
	if lastExtractCompletedAt != nil {
		record.LastExtractCompletedAt = lastExtractCompletedAt.UTC()
	}
	if lastCompactedAt != nil {
		record.LastCompactedAt = lastCompactedAt.UTC()
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record, nil
}

// scanPostgresManagementTurn scans one joined turn-management projection from a PostgreSQL row.
// scanPostgresManagementTurn 用于从 PostgreSQL 行扫描一条回合管理联表投影。
func scanPostgresManagementTurn(scanner interface{ Scan(...any) error }) (logicdomain.ManagementTurnRecord, error) {
	record := logicdomain.ManagementTurnRecord{}
	err := scanner.Scan(
		&record.ID, &record.SessionID, &record.ProjectID, &record.DehydratedContent,
		&record.DehydratedBudget, &record.ExtractedStatus, &record.Details, &record.DetailsBudget,
		&record.CreatedAt, &record.UpdatedAt, &record.UserID, &record.UserName,
		&record.TeamName, &record.SpaceName, &record.ProjectName,
		&record.DerivedMemories, &record.ProfileReferences,
	)
	if err != nil {
		return logicdomain.ManagementTurnRecord{}, err
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	return record, nil
}

// managementSessionProjectionSQL returns the shared joined projection used by session list and detail reads.
// managementSessionProjectionSQL 用于返回会话列表与详情读取共用的联表投影。
func (s *Store) managementSessionProjectionSQL() string {
	workspace := &s.repos.workspace
	return fmt.Sprintf(`
SELECT s.id, s.session_key, s.user_id, s.team_id, s.space_id, s.project_id,
       s.turn_count, s.last_summarized_id, s.last_compacted_turn_id,
       s.summarize_content, s.summarize_budget,
       s.last_extract_observed_at, s.last_extract_completed_at, s.last_compacted_at,
       s.created_at, s.updated_at, u.name, t.name, sp.name, p.name,
       COALESCE(first_turn.dehydrated_content, ''),
       (SELECT COUNT(*) FROM %s tr WHERE tr.session_id = s.id),
       (SELECT COUNT(*) FROM %s tr WHERE tr.session_id = s.id AND tr.extracted_status = %d),
       (SELECT COUNT(*) FROM %s mn WHERE mn.origin_session_id = s.id AND mn.memory_status = %d AND (mn.expires_at IS NULL OR mn.expires_at > NOW())),
		(SELECT COUNT(*) FROM %s pn JOIN %s tr ON tr.id = pn.turn_id WHERE tr.session_id = s.id AND pn.profile_status = %d),
		COALESCE(ms.status, 'active')
FROM %s s
JOIN %s u ON u.id = s.user_id
JOIN %s t ON t.id = s.team_id
JOIN %s sp ON sp.id = s.space_id
JOIN %s p ON p.id = s.project_id
LEFT JOIN LATERAL (
  SELECT tr.dehydrated_content FROM %s tr WHERE tr.session_id = s.id ORDER BY tr.id ASC LIMIT 1
) first_turn ON TRUE
LEFT JOIN %s ms ON ms.session_id = s.id
`, workspace.turnsTable(), workspace.turnsTable(), logicdomain.TurnExtractedStatusPending,
		workspace.memoryNodesTable(), logicdomain.MemoryStatusActive, workspace.profileNodesTable(), workspace.turnsTable(),
		logicdomain.ProfileStatusActive, workspace.sessionsTable(), workspace.usersTable(), workspace.teamsTable(),
		workspace.spacesTable(), workspace.projectsTable(), workspace.turnsTable(),
		s.repos.maintenance.maintenanceQualifiedTable("vmm_management_session_states"))
}

// ListManagementSessions returns one deterministic cursor page with aggregate counts and hierarchy names.
// ListManagementSessions 用于返回包含聚合计数与层级名称的一页确定性游标结果。
func (s *Store) ListManagementSessions(ctx context.Context, query logicdomain.ManagementSessionQuery) (logicdomain.ManagementSessionPage, error) {
	if s == nil || s.shared == nil || s.shared.pool == nil {
		return logicdomain.ManagementSessionPage{}, fmt.Errorf("postgres store is not initialized")
	}
	if query.Limit <= 0 {
		return logicdomain.ManagementSessionPage{}, logicdomain.ValidationError{Field: "limit", Message: "must be positive"}
	}
	args := &sqlArgsBuilder{}
	where := []string{"1 = 1"}
	if query.UserID != 0 {
		where = append(where, "s.user_id = "+args.Add(int64(query.UserID)))
	}
	if query.ProjectID != 0 {
		where = append(where, "s.project_id = "+args.Add(int64(query.ProjectID)))
	}
	if query.Status != "all" {
		where = append(where, "COALESCE(ms.status, 'active') = "+args.Add(query.Status))
	}
	if !query.CreatedFrom.IsZero() {
		where = append(where, "s.created_at >= "+args.Add(query.CreatedFrom.UTC()))
	}
	if !query.CreatedTo.IsZero() {
		where = append(where, "s.created_at <= "+args.Add(query.CreatedTo.UTC()))
	}
	if !query.UpdatedFrom.IsZero() {
		where = append(where, "s.updated_at >= "+args.Add(query.UpdatedFrom.UTC()))
	}
	if !query.UpdatedTo.IsZero() {
		where = append(where, "s.updated_at <= "+args.Add(query.UpdatedTo.UTC()))
	}
	if search := strings.TrimSpace(query.Query); search != "" {
		placeholder := args.Add("%" + search + "%")
		where = append(where, fmt.Sprintf("(s.session_key ILIKE %[1]s OR u.name ILIKE %[1]s OR t.name ILIKE %[1]s OR sp.name ILIKE %[1]s OR p.name ILIKE %[1]s OR COALESCE(first_turn.dehydrated_content, '') ILIKE %[1]s)", placeholder))
	}
	order := "DESC"
	comparison := "<"
	if query.Sort == "updated_asc" {
		order = "ASC"
		comparison = ">"
	}
	if query.Cursor.ID != 0 {
		updatedPlaceholder := args.Add(query.Cursor.UpdatedAt.UTC())
		idPlaceholder := args.Add(int64(query.Cursor.ID))
		where = append(where, fmt.Sprintf("(s.updated_at %s %s OR (s.updated_at = %s AND s.id %s %s))", comparison, updatedPlaceholder, updatedPlaceholder, comparison, idPlaceholder))
	}
	limitPlaceholder := args.Add(query.Limit + 1)
	sqlText := s.managementSessionProjectionSQL() + fmt.Sprintf(" WHERE %s ORDER BY s.updated_at %s, s.id %s LIMIT %s", strings.Join(where, " AND "), order, order, limitPlaceholder)
	callCtx, cancel := s.repos.workspace.workspaceQueryContext(ctx)
	defer cancel()
	rows, err := s.shared.pool.Query(callCtx, strings.TrimSpace(sqlText), args.Args()...)
	if err != nil {
		return logicdomain.ManagementSessionPage{}, fmt.Errorf("query postgres management sessions: %w", err)
	}
	defer rows.Close()
	items := make([]logicdomain.ManagementSessionRecord, 0, query.Limit+1)
	for rows.Next() {
		record, scanErr := scanPostgresManagementSession(rows)
		if scanErr != nil {
			return logicdomain.ManagementSessionPage{}, fmt.Errorf("scan postgres management session: %w", scanErr)
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.ManagementSessionPage{}, fmt.Errorf("iterate postgres management sessions: %w", err)
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
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
	if s == nil || s.shared == nil || s.shared.pool == nil {
		return logicdomain.ManagementSessionRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	callCtx, cancel := s.repos.workspace.workspaceQueryContext(ctx)
	defer cancel()
	record, err := scanPostgresManagementSession(s.shared.pool.QueryRow(callCtx, strings.TrimSpace(s.managementSessionProjectionSQL()+" WHERE s.id = $1 LIMIT 1"), int64(sessionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.ManagementSessionRecord{}, logicdomain.NotFoundError{Resource: "session", Message: "session does not exist"}
	}
	if err != nil {
		return logicdomain.ManagementSessionRecord{}, fmt.Errorf("query postgres management session: %w", err)
	}
	return record, nil
}

// ListManagementTurns returns one stable ascending page of turns for a session.
// ListManagementTurns 用于返回某个会话下一页稳定升序回合。
func (s *Store) ListManagementTurns(ctx context.Context, query logicdomain.ManagementTurnQuery) (logicdomain.ManagementTurnPage, error) {
	if s == nil || s.shared == nil || s.shared.pool == nil {
		return logicdomain.ManagementTurnPage{}, fmt.Errorf("postgres store is not initialized")
	}
	args := &sqlArgsBuilder{}
	where := []string{"1 = 1"}
	if query.SessionID != 0 {
		where = append(where, "tr.session_id = "+args.Add(int64(query.SessionID)))
	}
	if query.UserID != 0 {
		where = append(where, "s.user_id = "+args.Add(int64(query.UserID)))
	}
	if query.ProjectID != 0 {
		where = append(where, "tr.project_id = "+args.Add(int64(query.ProjectID)))
	}
	if !query.CreatedFrom.IsZero() {
		where = append(where, "tr.created_at >= "+args.Add(query.CreatedFrom.UTC()))
	}
	if !query.CreatedTo.IsZero() {
		where = append(where, "tr.created_at <= "+args.Add(query.CreatedTo.UTC()))
	}
	if search := strings.TrimSpace(query.Query); search != "" {
		placeholder := args.Add("%" + search + "%")
		where = append(where, fmt.Sprintf("(tr.dehydrated_content ILIKE %[1]s OR tr.details ILIKE %[1]s)", placeholder))
	}
	if query.Status == "pending" {
		where = append(where, "tr.extracted_status = "+args.Add(logicdomain.TurnExtractedStatusPending))
	} else if query.Status == "extracted" {
		where = append(where, "tr.extracted_status = "+args.Add(logicdomain.TurnExtractedStatusDone))
	}
	if query.HasMemory != nil {
		operator := "NOT EXISTS"
		if *query.HasMemory {
			operator = "EXISTS"
		}
		where = append(where, fmt.Sprintf("%s (SELECT 1 FROM %s mn WHERE mn.source_turn_id = tr.id)", operator, s.repos.workspace.memoryNodesTable()))
	}
	if query.HasProfile != nil {
		operator := "NOT EXISTS"
		if *query.HasProfile {
			operator = "EXISTS"
		}
		where = append(where, fmt.Sprintf("%s (SELECT 1 FROM %s pn WHERE pn.turn_id = tr.id)", operator, s.repos.workspace.profileNodesTable()))
	}
	order := "ASC"
	comparison := ">"
	if query.Sort == "id_desc" {
		order = "DESC"
		comparison = "<"
	}
	if query.CursorID != 0 {
		where = append(where, "tr.id "+comparison+" "+args.Add(int64(query.CursorID)))
	}
	limitPlaceholder := args.Add(query.Limit + 1)
	sqlText := fmt.Sprintf(`
SELECT tr.id, tr.session_id, tr.project_id, tr.dehydrated_content, tr.dehydrated_budget,
       tr.extracted_status, tr.details, tr.details_budget, tr.created_at, tr.updated_at,
       s.user_id, u.name, t.name, sp.name, p.name,
       (SELECT COUNT(*) FROM %s mn WHERE mn.source_turn_id = tr.id),
       (SELECT COUNT(*) FROM %s pn WHERE pn.turn_id = tr.id)
FROM %s tr
JOIN %s s ON s.id = tr.session_id
JOIN %s u ON u.id = s.user_id JOIN %s t ON t.id = s.team_id
JOIN %s sp ON sp.id = s.space_id JOIN %s p ON p.id = tr.project_id
WHERE %s ORDER BY tr.id %s LIMIT %s
`, s.repos.workspace.memoryNodesTable(), s.repos.workspace.profileNodesTable(),
		s.repos.turns.turnsTable(), s.repos.workspace.sessionsTable(), s.repos.workspace.usersTable(),
		s.repos.workspace.teamsTable(), s.repos.workspace.spacesTable(), s.repos.workspace.projectsTable(),
		strings.Join(where, " AND "), order, limitPlaceholder)
	callCtx, cancel := s.repos.turns.turnQueryContext(ctx)
	defer cancel()
	rows, err := s.shared.pool.Query(callCtx, strings.TrimSpace(sqlText), args.Args()...)
	if err != nil {
		return logicdomain.ManagementTurnPage{}, fmt.Errorf("query postgres management turns: %w", err)
	}
	defer rows.Close()
	items := make([]logicdomain.ManagementTurnRecord, 0, query.Limit+1)
	for rows.Next() {
		record, scanErr := scanPostgresManagementTurn(rows)
		if scanErr != nil {
			return logicdomain.ManagementTurnPage{}, fmt.Errorf("scan postgres management turn: %w", scanErr)
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.ManagementTurnPage{}, fmt.Errorf("iterate postgres management turns: %w", err)
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
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
