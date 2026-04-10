// workspace.go implements the minimal workspace, profile-target, and session-scope resolution flows required for combined PostgreSQL mode to boot and serve memory APIs.
// workspace.go 用于实现组合 PostgreSQL 模式启动和服务记忆 API 所需的最小 workspace、画像目标与 session 范围解析流程。
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

// ResolveRequestScope validates numeric user/project identifiers, resolves hierarchy names, and lazily creates the session row when needed.
// ResolveRequestScope 用于校验数字 user/project 标识、解析层级名称，并在需要时惰性创建 session 行。
func (r *workspaceRepository) ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.SessionRef{}, fmt.Errorf("postgres store is not initialized")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if userID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if projectID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	user, err := r.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	project, err := r.loadProjectByID(ctx, projectID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	session, err := r.ensureSession(ctx, sessionKey, user.ID, project)
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
	}, nil
}

// ResolveProfileTarget resolves the requested profile scope from the durable user/project hierarchy rows already present in PostgreSQL.
// ResolveProfileTarget 用于从 PostgreSQL 中已有的 user/project 层级行解析请求的画像目标范围。
func (r *workspaceRepository) ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProfileTargetRef{}, fmt.Errorf("postgres store is not initialized")
	}
	return r.resolveProfileTargetWithQueryer(ctx, r.shared.pool, profileType, userID, projectID)
}

// resolveProfileTargetWithQueryer resolves one profile target through the shared pool/transaction query abstraction so the scope contract can be tested without a live PostgreSQL connection.
// resolveProfileTargetWithQueryer 用于通过连接池/事务共享查询抽象解析画像目标，让范围契约可以在无真实 PostgreSQL 连接时被测试覆盖。
func (r *workspaceRepository) resolveProfileTargetWithQueryer(ctx context.Context, q profileQueryer, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	if !logicdomain.ValidProfileType(profileType) {
		return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile type"}
	}
	// Branch by target type so PostgreSQL keeps the same single-scope contract as SQLite and the use case validators.
	// 按目标类型分支，只解析该范围真正需要的层级，保证 PostgreSQL 与 SQLite 及用例层校验契约一致。
	switch profileType {
	case logicdomain.ProfileTypeUser:
		if userID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
		}
		user, err := r.loadUserByIDWithQueryer(ctx, q, userID, false)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		return logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			BindID:      user.ID,
			UserID:      user.ID,
			UserName:    user.Name,
		}, nil
	case logicdomain.ProfileTypeProject, logicdomain.ProfileTypeTeam, logicdomain.ProfileTypeSpace:
		if projectID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
		}
		project, err := r.loadProjectByIDWithQueryer(ctx, q, projectID)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		target := logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			UserID:      userID,
			TeamID:      project.TeamID,
			SpaceID:     project.SpaceID,
			ProjectID:   project.ID,
			TeamName:    project.TeamName,
			SpaceName:   project.SpaceName,
			ProjectName: project.Name,
		}
		switch profileType {
		case logicdomain.ProfileTypeTeam:
			target.BindID = project.TeamID
		case logicdomain.ProfileTypeSpace:
			target.BindID = project.SpaceID
		default:
			target.BindID = project.ID
		}
		return target, nil
	default:
		return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile type"}
	}
}

// ListProjects returns all durable projects together with their display path components in deterministic path order.
// ListProjects 用于按确定性的路径顺序返回全部长期项目及其展示路径组成部分。
func (r *workspaceRepository) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	return r.listProjectsWithQueryerAndContextBuilder(ctx, r.shared.pool, r.workspaceQueryContext)
}

// ListProjectsForMaintenance returns the same durable project list as the online admin path but wraps the enumeration in the dedicated maintenance read timeout so one-shot rebuild/export tools can scan larger workspaces safely.
// ListProjectsForMaintenance 用于返回与在线管理路径相同的长期项目列表，但会套用专用维护读取超时，让一次性重建/导出工具可以安全扫描更大的工作区。
func (r *workspaceRepository) ListProjectsForMaintenance(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	return r.listProjectsWithQueryerAndContextBuilder(ctx, r.shared.pool, r.workspaceMaintenanceReadContext)
}

// listProjectsWithQueryerAndContextBuilder centralizes project enumeration so online callers keep the normal query timeout while maintenance callers can explicitly opt into the longer maintenance read budget.
// listProjectsWithQueryerAndContextBuilder 用于集中承载项目枚举逻辑，让在线调用方继续使用常规查询超时，而维护调用方可以显式切到更长的维护读预算。
func (r *workspaceRepository) listProjectsWithQueryerAndContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc)) ([]logicdomain.ProjectRecord, error) {
	if r == nil || q == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, t.name AS team_name, sp.name AS space_name, p.created_at, p.updated_at
FROM %s AS p
JOIN %s AS t ON t.id = p.team_id
JOIN %s AS sp ON sp.id = p.space_id
ORDER BY t.name ASC, sp.name ASC, p.name ASC
`, r.projectsTable(), r.teamsTable(), r.spacesTable())
	if buildContext == nil {
		buildContext = r.workspaceQueryContext
	}
	callCtx, cancel := buildContext(ctx)
	defer cancel()
	rows, err := q.Query(callCtx, strings.TrimSpace(sqlText))
	if err != nil {
		return nil, fmt.Errorf("list postgres projects: %w", err)
	}
	defer rows.Close()

	projects := make([]logicdomain.ProjectRecord, 0)
	for rows.Next() {
		var row projectScanRow
		if err := rows.Scan(&row.ID, &row.TeamID, &row.SpaceID, &row.Name, &row.Profile, &row.TeamName, &row.SpaceName, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan postgres project row: %w", err)
		}
		projects = append(projects, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres project rows: %w", err)
	}
	return projects, nil
}

// ResolveProjectRef resolves either a numeric project id or the canonical Team/Space/Project path used by workspace admin flows.
// ResolveProjectRef 用于解析数字 project id，或 workspace 管理流使用的标准 Team/Space/Project 路径。
func (r *workspaceRepository) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return logicdomain.ProjectRecord{}, logicdomain.ValidationError{Field: "project_ref", Message: "is required"}
	}
	if projectID, ok := parseUint64(projectRef); ok {
		return r.loadProjectByID(ctx, projectID)
	}
	teamName, spaceName, projectName, err := parseProjectPath(projectRef)
	if err != nil {
		return logicdomain.ProjectRecord{}, err
	}
	sqlText := fmt.Sprintf(`
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, t.name AS team_name, sp.name AS space_name, p.created_at, p.updated_at
FROM %s AS p
JOIN %s AS t ON t.id = p.team_id
JOIN %s AS sp ON sp.id = p.space_id
WHERE t.name = $1 AND sp.name = $2 AND p.name = $3
LIMIT 1
`, r.projectsTable(), r.teamsTable(), r.spacesTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	var row projectScanRow
	err = r.shared.pool.QueryRow(callCtx, strings.TrimSpace(sqlText), teamName, spaceName, projectName).Scan(
		&row.ID, &row.TeamID, &row.SpaceID, &row.Name, &row.Profile, &row.TeamName, &row.SpaceName, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.ProjectRecord{}, fmt.Errorf("resolve postgres project path: %w", err)
		}
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project path %q does not exist", projectRef)}
	}
	return row.toDomain(), nil
}

// ResolveUserRef resolves either a numeric user id or one unique user name.
// ResolveUserRef 用于解析数字 user id 或唯一用户名。
func (r *workspaceRepository) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.UserRecord{}, fmt.Errorf("postgres store is not initialized")
	}
	userRef = strings.TrimSpace(userRef)
	if userRef == "" {
		return logicdomain.UserRecord{}, logicdomain.ValidationError{Field: "user_ref", Message: "is required"}
	}
	if userID, ok := parseUint64(userRef); ok {
		return r.loadUserByID(ctx, userID)
	}
	sqlText := fmt.Sprintf(`
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM %s
WHERE name = $1
LIMIT 1
`, r.usersTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	var row userScanRow
	err := r.shared.pool.QueryRow(callCtx, strings.TrimSpace(sqlText), userRef).Scan(
		&row.ID, &row.Name, &row.Profile, &row.DeleteConfirmCode, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.UserRecord{}, fmt.Errorf("resolve postgres user by name: %w", err)
		}
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist", userRef)}
	}
	return row.toDomain(), nil
}

// EnsureUserName resolves one user by name or creates it when confirmCreate is explicitly true.
// EnsureUserName 用于按名称解析用户，或在 confirmCreate 明确为真时创建用户。
func (r *workspaceRepository) EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("postgres store is not initialized")
	}
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return logicdomain.UserResolveResult{}, logicdomain.ValidationError{Field: "user_name", Message: "is required"}
	}
	if user, err := r.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{
			User:    user,
			Message: fmt.Sprintf("user %s already exists", user.Name),
			Exists:  true,
		}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	if !confirmCreate {
		return logicdomain.UserResolveResult{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist; use confirm_create=1 to create it", userName)}
	}
	now := time.Now().UTC()
	sqlText := fmt.Sprintf(`
INSERT INTO %s (name, delete_confirm_code, created_at, updated_at)
VALUES ($1, '', $2, $2)
ON CONFLICT (name) DO NOTHING
RETURNING id, name, profile, delete_confirm_code, created_at, updated_at
`, r.usersTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	var row userScanRow
	err := r.shared.pool.QueryRow(callCtx, strings.TrimSpace(sqlText), userName, now).Scan(
		&row.ID, &row.Name, &row.Profile, &row.DeleteConfirmCode, &row.CreatedAt, &row.UpdatedAt,
	)
	if err == nil {
		user := row.toDomain()
		return logicdomain.UserResolveResult{
			User:    user,
			Message: fmt.Sprintf("user %s created", user.Name),
			Created: true,
		}, nil
	}
	user, resolveErr := r.ResolveUserRef(ctx, userName)
	if resolveErr != nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("create postgres user: %w", err)
	}
	return logicdomain.UserResolveResult{
		User:    user,
		Message: fmt.Sprintf("user %s already exists", user.Name),
		Exists:  true,
	}, nil
}

// ListUsers returns all durable users ordered by id for deterministic admin output.
// ListUsers 用于按 id 稳定返回全部长期用户，服务管理输出。
func (r *workspaceRepository) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM %s
ORDER BY id ASC
`, r.usersTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, strings.TrimSpace(sqlText))
	if err != nil {
		return nil, fmt.Errorf("list postgres users: %w", err)
	}
	defer rows.Close()

	users := make([]logicdomain.UserRecord, 0)
	for rows.Next() {
		var row userScanRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Profile, &row.DeleteConfirmCode, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan postgres user row: %w", err)
		}
		users = append(users, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres user rows: %w", err)
	}
	return users, nil
}

// loadUserByID loads one user row by numeric id and returns a stable not-found error when it does not exist.
// loadUserByID 用于按数字 id 加载用户行，并在不存在时返回稳定的 not-found 错误。
func (r *workspaceRepository) loadUserByID(ctx context.Context, userID uint64) (logicdomain.UserRecord, error) {
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	return loadUserByQueryer(callCtx, r.shared.pool, userID, r.usersTable(), "")
}

// loadUserByIDWithQueryer loads one user row through either the pool or a transaction, with optional row locking for admin flows that must serialize confirmation state.
// loadUserByIDWithQueryer 用于通过连接池或事务加载单条用户行，并可选择加行锁，供需要串行化确认状态的管理流程复用。
func (r *workspaceRepository) loadUserByIDWithQueryer(ctx context.Context, q profileQueryer, userID uint64, forUpdate bool) (logicdomain.UserRecord, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	return loadUserByQueryer(ctx, q, userID, r.usersTable(), lockClause)
}

// loadProjectByID loads one project row by numeric id together with its Team/Space display names.
// loadProjectByID 用于按数字 id 加载项目行及其 Team/Space 展示名称。
func (r *workspaceRepository) loadProjectByID(ctx context.Context, projectID uint64) (logicdomain.ProjectRecord, error) {
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	return loadProjectByQueryer(callCtx, r.shared.pool, projectID, r.projectsTable(), r.teamsTable(), r.spacesTable())
}

// loadProjectByIDWithQueryer loads one project row through either the pool or a transaction so profile and admin helpers can share the same hierarchy lookup contract.
// loadProjectByIDWithQueryer 用于通过连接池或事务加载单条项目行，让画像与管理辅助逻辑共享同一套层级查询契约。
func (r *workspaceRepository) loadProjectByIDWithQueryer(ctx context.Context, q profileQueryer, projectID uint64) (logicdomain.ProjectRecord, error) {
	return loadProjectByQueryer(ctx, q, projectID, r.projectsTable(), r.teamsTable(), r.spacesTable())
}

// ensureSession loads or creates one session row under the resolved user/project scope.
// ensureSession 用于在已解析 user/project 范围下加载或创建一条 session 行。
func (r *workspaceRepository) ensureSession(ctx context.Context, sessionKey string, userID uint64, project logicdomain.ProjectRecord) (logicdomain.SessionRecord, error) {
	sqlText := fmt.Sprintf(`
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_at, last_extract_completed_at, last_compacted_at,
       created_at, updated_at
FROM %s
WHERE project_id = $1 AND session_key = $2
LIMIT 1
`, r.sessionsTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	var existing sessionScanRow
	err := r.shared.pool.QueryRow(callCtx, strings.TrimSpace(sqlText), int64(project.ID), sessionKey).Scan(
		&existing.ID,
		&existing.SessionKey,
		&existing.UserID,
		&existing.TeamID,
		&existing.SpaceID,
		&existing.ProjectID,
		&existing.TurnCount,
		&existing.LastSummarizedID,
		&existing.LastCompactedTurnID,
		&existing.SummarizeContent,
		&existing.SummarizeBudget,
		&existing.LastExtractObservedAt,
		&existing.LastExtractCompletedAt,
		&existing.LastCompactedAt,
		&existing.CreatedAt,
		&existing.UpdatedAt,
	)
	if err == nil {
		return existing.toDomain(), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.SessionRecord{}, fmt.Errorf("load postgres session: %w", err)
	}

	now := time.Now().UTC()
	insertSQL := fmt.Sprintf(`
INSERT INTO %s (
	session_key, user_id, team_id, space_id, project_id,
	turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
	last_extract_observed_at, last_extract_completed_at, last_compacted_at,
	created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5,
	0, 0, 0, '', 0,
	NULL, NULL, NULL,
	$6, $6
)
ON CONFLICT (project_id, session_key)
DO UPDATE SET updated_at = %s.updated_at
RETURNING id, session_key, user_id, team_id, space_id, project_id,
          turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
          last_extract_observed_at, last_extract_completed_at, last_compacted_at,
          created_at, updated_at
`, r.sessionsTable(), r.sessionsTable())
	var created sessionScanRow
	if err := r.shared.pool.QueryRow(callCtx, strings.TrimSpace(insertSQL),
		sessionKey,
		int64(userID),
		int64(project.TeamID),
		int64(project.SpaceID),
		int64(project.ID),
		now,
	).Scan(
		&created.ID,
		&created.SessionKey,
		&created.UserID,
		&created.TeamID,
		&created.SpaceID,
		&created.ProjectID,
		&created.TurnCount,
		&created.LastSummarizedID,
		&created.LastCompactedTurnID,
		&created.SummarizeContent,
		&created.SummarizeBudget,
		&created.LastExtractObservedAt,
		&created.LastExtractCompletedAt,
		&created.LastCompactedAt,
		&created.CreatedAt,
		&created.UpdatedAt,
	); err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("ensure postgres session: %w", err)
	}
	return created.toDomain(), nil
}

// ResolveRequestScope delegates to the workspace repository for request-scope resolution.
// ResolveRequestScope 用于把请求范围解析委托给 workspace 仓储。
func (s *Store) ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).ResolveRequestScope(ctx, sessionKey, userID, projectID)
	}
	return s.repos.workspace.ResolveRequestScope(ctx, sessionKey, userID, projectID)
}

// ResolveProfileTarget delegates to the workspace repository for profile-target resolution.
// ResolveProfileTarget 用于把画像目标解析委托给 workspace 仓储。
func (s *Store) ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	return s.repos.workspace.ResolveProfileTarget(ctx, profileType, userID, projectID)
}

// ListProjects delegates to the workspace repository for project enumeration.
// ListProjects 用于把项目枚举委托给 workspace 仓储。
func (s *Store) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return s.repos.workspace.ListProjects(ctx)
}

// ListProjectsForMaintenance delegates to the workspace repository for maintenance-oriented project enumeration.
// ListProjectsForMaintenance 用于把维护导向的项目枚举委托给 workspace 仓储。
func (s *Store) ListProjectsForMaintenance(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return s.repos.workspace.ListProjectsForMaintenance(ctx)
}

// ResolveProjectRef delegates to the workspace repository for project path resolution.
// ResolveProjectRef 用于把项目路径解析委托给 workspace 仓储。
func (s *Store) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	return s.repos.workspace.ResolveProjectRef(ctx, projectRef)
}

// ResolveUserRef delegates to the workspace repository for user resolution.
// ResolveUserRef 用于把用户解析委托给 workspace 仓储。
func (s *Store) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	return s.repos.workspace.ResolveUserRef(ctx, userRef)
}

// EnsureUserName delegates to the workspace repository for user-by-name resolution or creation.
// EnsureUserName 用于把按名称解析/创建用户委托给 workspace 仓储。
func (s *Store) EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	return s.repos.workspace.EnsureUserName(ctx, userName, confirmCreate)
}

// ListUsers delegates to the workspace repository for user enumeration.
// ListUsers 用于把用户枚举委托给 workspace 仓储。
func (s *Store) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	return s.repos.workspace.ListUsers(ctx)
}

// resolveProfileTargetWithQueryer delegates to the workspace repository for test-compatible profile target resolution.
// resolveProfileTargetWithQueryer 用于把画像目标解析委托给 workspace 仓储，供测试兼容调用。
func (s *Store) resolveProfileTargetWithQueryer(ctx context.Context, q profileQueryer, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).resolveProfileTargetWithQueryer(ctx, q, profileType, userID, projectID)
	}
	return s.repos.workspace.resolveProfileTargetWithQueryer(ctx, q, profileType, userID, projectID)
}

// insertTeam delegates to the workspace repository for team insertion.
// insertTeam 用于把 team 插入委托给 workspace 仓储。
func (s *Store) insertTeam(ctx context.Context, q profileQueryer, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).insertTeam(ctx, q, teamName, now)
	}
	return s.repos.workspace.insertTeam(ctx, q, teamName, now)
}

// insertSpace delegates to the workspace repository for space insertion.
// insertSpace 用于把 space 插入委托给 workspace 仓储。
func (s *Store) insertSpace(ctx context.Context, q profileQueryer, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).insertSpace(ctx, q, teamID, spaceName, now)
	}
	return s.repos.workspace.insertSpace(ctx, q, teamID, spaceName, now)
}

// insertProject delegates to the workspace repository for project insertion.
// insertProject 用于把 project 插入委托给 workspace 仓储。
func (s *Store) insertProject(ctx context.Context, q profileQueryer, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).insertProject(ctx, q, team, space, projectName, now)
	}
	return s.repos.workspace.insertProject(ctx, q, team, space, projectName, now)
}

// listProjectsWithQueryerAndContextBuilder delegates to the workspace repository for project enumeration with custom query context.
// listProjectsWithQueryerAndContextBuilder 用于把带自定义上下文的项目枚举委托给 workspace 仓储。
func (s *Store) listProjectsWithQueryerAndContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc)) ([]logicdomain.ProjectRecord, error) {
	if s.repos.workspace.shared == nil {
		return (&workspaceRepository{shared: &storeShared{pool: s.pool, cfg: s.cfg, dialect: s.dialect}}).listProjectsWithQueryerAndContextBuilder(ctx, q, buildContext)
	}
	return s.repos.workspace.listProjectsWithQueryerAndContextBuilder(ctx, q, buildContext)
}
