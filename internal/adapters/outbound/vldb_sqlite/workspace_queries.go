// SQLite workspace identity resolution and creation for request scope lookup.
// SQLite 适配层请求范围查询使用的工作区身份解析与创建。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// parameterizedDebugSeedWorkspaceStatements builds deterministic testing-stage seed writes as single-statement typed-param calls because the real FFI only accepts flat params for one SQL statement.
// parameterizedDebugSeedWorkspaceStatements 用于把确定性的测试阶段 seed 构造成单语句强类型参数调用，因为真实 FFI 只接受单条 SQL 的扁平参数。
func parameterizedDebugSeedWorkspaceStatements(now time.Time) []sqliteWriteStatement {
	now = now.UTC()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	return []sqliteWriteStatement{
		{
			SQL: `
INSERT INTO vmm_users (id, name, profile, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', '', ?, ?);
`,
			Params: []any{debugSeedUserID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_teams (id, name, profile, created_at, updated_at)
VALUES (?, ?, '', ?, ?);
`,
			Params: []any{debugSeedTeamID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_spaces (id, team_id, name, profile, created_at, updated_at)
VALUES (?, ?, ?, '', ?, ?);
`,
			Params: []any{debugSeedSpaceID, debugSeedTeamID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_projects (id, team_id, space_id, name, profile, created_at, updated_at)
VALUES (?, ?, ?, ?, '', ?, ?);
`,
			Params: []any{debugSeedProjectID, debugSeedTeamID, debugSeedSpaceID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
	}
}

// ResolveRequestScope validates numeric user/project identifiers, resolves hierarchy names, and auto-creates the session row when needed.
// ResolveRequestScope 用于校验数字 user/project 标识、解析层级名称，并在需要时自动创建 session 记录。
func (s *Store) ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	// Reject incomplete numeric identifiers here so PreCheck/PostAction never enter business code with unresolved scopes.
	// 在这里拒绝不完整的数字标识，确保 PreCheck/PostAction 不会带着未解析的范围进入业务代码。
	if strings.TrimSpace(sessionKey) == "" {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if userID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if projectID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}

	user, err := s.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	project, err := s.loadProjectByID(ctx, projectID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	session, err := s.ensureSession(ctx, strings.TrimSpace(sessionKey), user, project)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	memoryStatus, err := s.loadSessionMemoryStatus(ctx, session.ID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	return logicdomain.SessionRef{
		SessionID:              session.ID,
		SessionKey:             session.SessionKey,
		UserID:                 user.ID,
		TeamID:                 project.TeamID,
		SpaceID:                project.SpaceID,
		ProjectID:              project.ID,
		TurnCount:              session.TurnCount,
		LastSummarizedID:       session.LastSummarizedID,
		LastCompactedTurnID:    session.LastCompactedTurnID,
		SummarizeContent:       session.SummarizeContent,
		SummarizeBudget:        session.SummarizeBudget,
		LastExtractObservedAt:  session.LastExtractObservedAt,
		LastExtractCompletedAt: session.LastExtractCompletedAt,
		LastCompactedAt:        session.LastCompactedAt,
		CreatedAt:              session.CreatedAt,
		UpdatedAt:              session.UpdatedAt,
		UserName:               user.Name,
		TeamName:               project.TeamName,
		SpaceName:              project.SpaceName,
		ProjectName:            project.Name,
		MemoryStatus:           memoryStatus,
	}, nil
}

// loadUserByID resolves one durable user row by numeric id and converts absence into a stable domain not-found error.
// loadUserByID 用于按数字 ID 解析长期用户记录，并把不存在情况转换成稳定的领域 not-found 错误。
func (s *Store) loadUserByID(ctx context.Context, userID uint64) (logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE id = ?
LIMIT 1
`, userID)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("query user: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user_id %d does not exist", userID)}
	}
	return rows[0].toDomain(), nil
}

// loadProjectByID resolves one project together with its parent team and space names.
// loadProjectByID 用于解析单个项目，以及它对应的 team 和 space 名称。
func (s *Store) loadProjectByID(ctx context.Context, projectID uint64) (logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.id = ?
LIMIT 1
`, projectID)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("query project: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project_id %d does not exist", projectID)}
	}
	return rows[0].toDomain(), nil
}

// loadSessionByID resolves one durable session row by numeric id so append writes can derive counters from the current locked state.
// loadSessionByID 用于按数字 ID 解析一条长期 session 行，让追加写入可以从当前锁内状态推导计数。
func (s *Store) loadSessionByID(ctx context.Context, sessionID uint64) (logicdomain.SessionRecord, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count,
       last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE id = ?
LIMIT 1
`, sessionID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("query session: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionRecord{}, logicdomain.NotFoundError{Resource: "session", Message: fmt.Sprintf("session_id %d does not exist", sessionID)}
	}
	return rows[0].toDomain(), nil
}

// lookupSessionByProjectAndKey resolves one project-scoped session key and reports absence without treating it as an error.
// lookupSessionByProjectAndKey 用于解析一个项目作用域内的 session key，并把不存在作为布尔结果返回。
func (s *Store) lookupSessionByProjectAndKey(ctx context.Context, projectID uint64, sessionKey string) (logicdomain.SessionRecord, bool, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count,
       last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE project_id = ? AND session_key = ?
LIMIT 1
`, projectID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, false, fmt.Errorf("query session: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// ensureSession loads one existing session inside the target project or creates it under the locked, freshly verified user/project hierarchy.
// ensureSession 用于在目标项目内读取已有 session，或在写锁内重新校验 user/project 后创建缺失的 session。
func (s *Store) ensureSession(ctx context.Context, sessionKey string, user logicdomain.UserRecord, project logicdomain.ProjectRecord) (logicdomain.SessionRecord, error) {
	session, exists, err := s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, err
	}
	if exists {
		// Reuse the project-scoped session row directly even when the upstream plugin has switched user ids during debugging.
		// 在调试阶段，即使上游插件手动切换了 user_id，也直接复用同一 project 下的 session 行。
		return session, nil
	}

	// Serialize the missing-session create window so duplicate requests converge before allocating a fresh numeric id.
	// 串行化缺失 session 的创建窗口，让重复请求在分配新数字 ID 前先收敛到已有行。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	session, exists, err = s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, err
	}
	if exists {
		return session, nil
	}

	// Revalidate the resolved user and project after the write lock is acquired so a delete/recreate race cannot leave an orphaned session.
	// 获取写锁后重新校验已解析的 user 与 project，避免删除/重建竞争留下孤儿 session。
	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("reload user before session create: %w", err)
	}
	if !sameUserIdentity(currentUser, user) {
		return logicdomain.SessionRecord{}, logicdomain.ConflictError{
			Resource: "user",
			Message:  fmt.Sprintf("user changed before session create: resolved %s, current %s", user.Name, currentUser.Name),
		}
	}
	currentProject, err := s.loadProjectByID(ctx, project.ID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("reload project before session create: %w", err)
	}
	if !sameProjectIdentity(currentProject, project) {
		return logicdomain.SessionRecord{}, logicdomain.ConflictError{
			Resource: "project",
			Message:  fmt.Sprintf("project changed before session create: resolved %s, current %s", project.Path(), currentProject.Path()),
		}
	}
	return s.insertSession(ctx, sessionKey, currentUser, currentProject, time.Now().UTC())
}

// ListProjects returns all projects together with their display path components, ordered for deterministic UX.
// ListProjects 用于返回全部项目及其展示路径组成部分，并按稳定顺序输出。
func (s *Store) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
ORDER BY t.name ASC, sp.name ASC, p.name ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]logicdomain.ProjectRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// ListProjectsForMaintenance reuses the normal SQLite project listing because SQLite workspace enumeration already runs under the same bounded gateway timeout used by maintenance commands.
// ListProjectsForMaintenance 用于让维护命令复用常规 SQLite 项目列表读取，因为 SQLite 的项目枚举本就运行在同一套有界网关超时之下。
func (s *Store) ListProjectsForMaintenance(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return s.ListProjects(ctx)
}

// ResolveProjectRef resolves either a numeric project id or a canonical Team/Space/Project path.
// ResolveProjectRef 用于解析数字 project id，或标准的 Team/Space/Project 路径。
func (s *Store) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return logicdomain.ProjectRecord{}, logicdomain.ValidationError{Field: "project_ref", Message: "is required"}
	}
	if projectID, ok := storageutil.ParsePositiveUint64(projectRef); ok {
		return s.loadProjectByID(ctx, projectID)
	}
	teamName, spaceName, projectName, err := storageutil.ParseProjectPath(projectRef)
	if err != nil {
		return logicdomain.ProjectRecord{}, err
	}
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE t.name = ? AND sp.name = ? AND p.name = ?
LIMIT 1
`, teamName, spaceName, projectName)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("resolve project path: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project path %q does not exist", projectRef)}
	}
	return rows[0].toDomain(), nil
}

// insertSession creates one missing session row and reconciles ambiguous commits by the project-scoped session key.
// insertSession 用于创建一条缺失的 session 行，并通过项目作用域内的 session key 对账提交结果不明的插入。
func (s *Store) insertSession(ctx context.Context, sessionKey string, user logicdomain.UserRecord, project logicdomain.ProjectRecord, now time.Time) (logicdomain.SessionRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_sessions")
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("allocate session id: %w", err)
	}
	nowMs := now.UnixMilli()
	if err := s.execInsertOneRow(ctx, "insert session", `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id,
  turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, '', 0, 0, 0, 0, ?, ?)
`, nextID, sessionKey, user.ID, project.TeamID, project.SpaceID, project.ID, nowMs, nowMs); err != nil {
		// Re-read the project-scoped unique key only when SQLite reports an ambiguous commit boundary, so ordinary insert failures stay retry-safe.
		// 仅当 SQLite 报告提交边界不明确时才按项目作用域唯一键回读，确保普通插入失败仍保持可安全重试。
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
			if reconcileErr != nil {
				return logicdomain.SessionRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert session",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.SessionRecord{}, err
	}
	return logicdomain.SessionRecord{
		ID:                     nextID,
		SessionKey:             sessionKey,
		UserID:                 user.ID,
		TeamID:                 project.TeamID,
		SpaceID:                project.SpaceID,
		ProjectID:              project.ID,
		TurnCount:              0,
		LastSummarizedID:       0,
		LastCompactedTurnID:    0,
		SummarizeContent:       "",
		SummarizeBudget:        0,
		LastExtractObservedAt:  time.Time{},
		LastExtractCompletedAt: time.Time{},
		LastCompactedAt:        time.Time{},
		CreatedAt:              now,
		UpdatedAt:              now,
	}, nil
}

// insertUser creates one missing user row and reconciles ambiguous commits by the unique user name.
// insertUser 用于创建一条缺失的 user 行，并通过唯一用户名对账提交结果不明的插入。
func (s *Store) insertUser(ctx context.Context, userName string, now time.Time) (logicdomain.UserRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_users")
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("allocate user id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert user", `
INSERT INTO vmm_users (id, name, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', ?, ?)
`, nextID, userName, nowRFC3339, nowRFC3339); err != nil {
		// Re-read the unique user name only after a commit-unknown insert, preserving the original uncertain error when the row never became durable.
		// 仅在插入提交结果不明后回读唯一用户名；如果长期行并未落库，则保留原始结果不明错误。
		if sqliteInsertCommitUnknownError(err) {
			recovered, reconcileErr := s.ResolveUserRef(ctx, userName)
			if reconcileErr != nil {
				if logicdomain.IsNotFoundError(reconcileErr) {
					return logicdomain.UserRecord{}, err
				}
				return logicdomain.UserRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert user",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return recovered, nil
		}
		return logicdomain.UserRecord{}, err
	}
	return logicdomain.UserRecord{ID: nextID, Name: userName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertTeam creates one missing team node under the write lock and reconciles ambiguous commits by the unique team name.
// insertTeam 用于在写锁保护下创建缺失的 team 节点，并通过唯一 team 名称对账提交结果不明的插入。
func (s *Store) insertTeam(ctx context.Context, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_teams")
	if err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("allocate team id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert team", `INSERT INTO vmm_teams (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, nextID, teamName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupTeamByName(ctx, teamName)
			if reconcileErr != nil {
				return logicdomain.TeamRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert team",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.TeamRecord{}, err
	}
	return logicdomain.TeamRecord{ID: nextID, Name: teamName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertSpace creates one missing space node under an existing team and reconciles ambiguous commits by the team/name unique key.
// insertSpace 用于在已存在的 team 下创建缺失的 space 节点，并通过 team/name 唯一键对账提交结果不明的插入。
func (s *Store) insertSpace(ctx context.Context, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_spaces")
	if err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("allocate space id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert space", `INSERT INTO vmm_spaces (id, team_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, nextID, teamID, spaceName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupSpaceByName(ctx, teamID, spaceName, true)
			if reconcileErr != nil {
				return logicdomain.SpaceRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert space",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.SpaceRecord{}, err
	}
	return logicdomain.SpaceRecord{ID: nextID, TeamID: teamID, Name: spaceName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertProject creates one missing project node under the resolved Team/Space hierarchy and reconciles ambiguous commits by the scoped project name.
// insertProject 用于在已解析的 Team/Space 层级下创建缺失的 project 节点，并通过范围内 project 名称对账提交结果不明的插入。
func (s *Store) insertProject(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_projects")
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("allocate project id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert project", `INSERT INTO vmm_projects (id, team_id, space_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, nextID, team.ID, space.ID, projectName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupProjectBySpaceAndName(ctx, team, space, projectName)
			if reconcileErr != nil {
				return logicdomain.ProjectRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert project",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.ProjectRecord{}, err
	}
	return logicdomain.ProjectRecord{
		ID:        nextID,
		TeamID:    team.ID,
		SpaceID:   space.ID,
		TeamName:  team.Name,
		SpaceName: space.Name,
		Name:      projectName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
