// workspace_admin.go implements PostgreSQL-backed Team/Space/Project and user administration flows for the combined runtime.
// workspace_admin.go 用于实现组合运行时基于 PostgreSQL 的 Team/Space/Project 与用户管理流程。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// projectDeletePlan records the exact row categories that one project deletion will remove, including hierarchy parents that become empty after the project disappears.
// projectDeletePlan 用于记录一次项目删除将真正移除的行类别，包括项目删除后因变空而需要级联删除的父级层级节点。
type projectDeletePlan struct {
	DeletedProjects int
	DeletedSpaces   int
	DeletedTeams    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
}

// userDeletePlan records the exact row categories that one user deletion will remove.
// userDeletePlan 用于记录一次用户删除将真正移除的行类别。
type userDeletePlan struct {
	DeletedUsers    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
}

// EnsureProjectPath resolves or creates a Team/Space/Project path according to the confirm flag rules required by the admin RPCs.
// EnsureProjectPath 用于按管理 RPC 约定的确认规则，解析或创建一个 Team/Space/Project 路径。
func (r *workspaceRepository) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProjectMutationResult{}, fmt.Errorf("postgres store is not initialized")
	}
	teamName, spaceName, projectName, err := parseProjectPath(projectPath)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if existing, err := r.ResolveProjectRef(ctx, projectPath); err == nil {
		return logicdomain.ProjectMutationResult{
			Project: existing,
			Message: fmt.Sprintf("project %s already exists", existing.Path()),
			Exists:  true,
		}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.ProjectMutationResult{}, err
	}

	team, teamExists, err := r.lookupTeamByName(ctx, r.shared.pool, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err := r.lookupSpaceByName(ctx, r.shared.pool, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      buildProjectConfirmMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}

	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, fmt.Errorf("begin postgres ensure-project tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	now := time.Now().UTC()
	createdTeam := false
	createdSpace := false
	createdProject := false
	team, teamExists, err = r.lookupTeamByName(callCtx, tx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !teamExists {
		team, err = r.insertTeam(callCtx, tx, teamName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdTeam = true
	}
	space, spaceExists, err = r.lookupSpaceByName(callCtx, tx, team.ID, spaceName, true)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !spaceExists {
		space, err = r.insertSpace(callCtx, tx, team.ID, spaceName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdSpace = true
	}
	project, exists, err := r.lookupProjectBySpaceAndName(callCtx, tx, team, space, projectName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !exists {
		project, err = r.insertProject(callCtx, tx, team, space, projectName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdProject = true
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ProjectMutationResult{}, fmt.Errorf("commit postgres ensure-project tx: %w", err)
	}
	if !createdProject {
		return logicdomain.ProjectMutationResult{
			Project: project,
			Message: fmt.Sprintf("project %s already exists", project.Path()),
			Exists:  true,
		}, nil
	}
	return logicdomain.ProjectMutationResult{
		Project:        project,
		Message:        fmt.Sprintf("project %s created", project.Path()),
		CreatedTeam:    createdTeam,
		CreatedSpace:   createdSpace,
		CreatedProject: createdProject,
	}, nil
}

// DeleteProjectPath deletes one resolved project plus its sessions, turn records, and SQL-backed memories when confirmation is explicit.
// DeleteProjectPath 用于在确认删除时，删除一个已解析项目及其 sessions、turn 记录和 SQL 侧记忆。
func (r *workspaceRepository) DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("postgres store is not initialized")
	}
	project, err := r.ResolveProjectRef(ctx, projectPath)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if !confirmDelete {
		return logicdomain.ProjectDeleteResult{
			Project:      project,
			Message:      fmt.Sprintf("confirm delete project %s", project.Path()),
			NeedsConfirm: true,
		}, nil
	}

	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("begin postgres delete-project tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	deletePlan, err := r.planProjectDelete(callCtx, tx, project)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	whereSQL, args := r.buildProjectProfileNodesWhere(project.ID, project.SpaceID, project.TeamID, deletePlan.DeletedSpaces > 0, deletePlan.DeletedTeams > 0)
	deleteProfileSQL := fmt.Sprintf(`DELETE FROM %s WHERE %s`, r.profileNodesTable(), whereSQL)
	if _, err := tx.Exec(callCtx, deleteProfileSQL, args...); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres project profile nodes: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE project_id = $1`, r.memoryNodesTable()), int64(project.ID)); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres project memory nodes: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE project_id = $1`, r.turnsTable()), int64(project.ID)); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres project turn records: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE project_id = $1`, r.sessionsTable()), int64(project.ID)); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres project sessions: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, r.projectsTable()), int64(project.ID)); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres project row: %w", err)
	}
	if deletePlan.DeletedSpaces > 0 {
		if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, r.spacesTable()), int64(project.SpaceID)); err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres empty project space row: %w", err)
		}
	}
	if deletePlan.DeletedTeams > 0 {
		if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, r.teamsTable()), int64(project.TeamID)); err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete postgres empty project team row: %w", err)
		}
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("commit postgres delete-project tx: %w", err)
	}
	return logicdomain.ProjectDeleteResult{
		Project:         project,
		Message:         fmt.Sprintf("project %s deleted", project.Path()),
		DeletedProjects: deletePlan.DeletedProjects,
		DeletedSpaces:   deletePlan.DeletedSpaces,
		DeletedTeams:    deletePlan.DeletedTeams,
		DeletedSessions: deletePlan.DeletedSessions,
		DeletedMessages: deletePlan.DeletedMessages,
		DeletedMemories: deletePlan.DeletedMemories,
		DeletedProfiles: deletePlan.DeletedProfiles,
	}, nil
}

// MigrateProjectPath moves SQL-backed sessions, turn records, memories, and project-bound profile nodes from one project scope onto another when explicitly confirmed.
// MigrateProjectPath 用于在显式确认后，把 SQL 侧的 sessions、turn 记录、记忆和项目绑定画像节点从源项目范围迁移到目标项目范围。
func (r *workspaceRepository) MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("postgres store is not initialized")
	}
	source, err := r.ResolveProjectRef(ctx, sourcePath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err := r.ResolveProjectRef(ctx, targetPath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	if source.ID == target.ID {
		return logicdomain.ProjectMigrationResult{}, logicdomain.ConflictError{Resource: "project", Message: "source and target project are the same"}
	}
	if !confirm {
		return logicdomain.ProjectMigrationResult{
			Source:       source,
			Target:       target,
			Message:      fmt.Sprintf("confirm migrate project %s -> %s", source.Path(), target.Path()),
			NeedsConfirm: true,
		}, nil
	}
	sessions, messages, memories, err := r.countProjectRows(ctx, r.shared.pool, source.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}

	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("begin postgres migrate-project tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	// Reject colliding session keys before rewriting the hierarchy so the admin flow returns a deterministic conflict instead of surfacing a late unique-index failure.
	// 在改写层级归属前先拒绝冲突的 session_key，这样管理流程会返回稳定的冲突提示，而不是在后面抛出唯一键异常。
	conflictSessionKeys, err := r.loadProjectSessionKeyConflicts(callCtx, tx, source.ID, target.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	if len(conflictSessionKeys) > 0 {
		return logicdomain.ProjectMigrationResult{}, buildProjectMigrationSessionConflict(source, target, conflictSessionKeys)
	}
	now := time.Now().UTC()
	updateSessionsSQL := fmt.Sprintf(`
UPDATE %s
SET team_id = $1, space_id = $2, project_id = $3, updated_at = $4
WHERE project_id = $5
`, r.sessionsTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateSessionsSQL), int64(target.TeamID), int64(target.SpaceID), int64(target.ID), now, int64(source.ID)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return logicdomain.ProjectMigrationResult{}, buildProjectMigrationSessionConflict(source, target, nil)
		}
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate postgres sessions: %w", err)
	}
	updateTurnsSQL := fmt.Sprintf(`UPDATE %s SET project_id = $1, updated_at = $2 WHERE project_id = $3`, r.turnsTable())
	if _, err := tx.Exec(callCtx, updateTurnsSQL, int64(target.ID), now, int64(source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate postgres turn records: %w", err)
	}
	updateMemoriesSQL := fmt.Sprintf(`
UPDATE %s
SET team_id = $1, space_id = $2, project_id = $3, updated_at = $4
WHERE project_id = $5
`, r.memoryNodesTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateMemoriesSQL), int64(target.TeamID), int64(target.SpaceID), int64(target.ID), now, int64(source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate postgres memory nodes: %w", err)
	}
	updateProfileNodesSQL := fmt.Sprintf(`
UPDATE %s
SET bind_id = $1, updated_at = $2
WHERE profile_type = $3 AND bind_id = $4
`, r.profileNodesTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(updateProfileNodesSQL), int64(target.ID), now, logicdomain.ProfileTypeProject, int64(source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate postgres project profile nodes: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("commit postgres migrate-project tx: %w", err)
	}
	return logicdomain.ProjectMigrationResult{
		Source:           source,
		Target:           target,
		Message:          fmt.Sprintf("migrated project %s -> %s", source.Path(), target.Path()),
		MigratedSessions: sessions,
		MigratedMessages: messages,
		MigratedMemories: memories,
	}, nil
}

// DeleteUserRef executes the protected two-phase user deletion flow and removes the user plus all SQL-side dependent data.
// DeleteUserRef 用于执行受保护的双阶段用户删除流程，并删除该用户及其所有 SQL 侧关联数据。
func (r *workspaceRepository) DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("postgres store is not initialized")
	}
	user, err := r.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	confirmationCode = strings.TrimSpace(confirmationCode)
	if confirmationCode == "" {
		return r.ensureUserDeleteConfirmation(ctx, user)
	}

	currentUser, err := r.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if strings.TrimSpace(currentUser.DeleteConfirmCode) == "" {
		return r.ensureUserDeleteConfirmation(ctx, currentUser)
	}
	if confirmationCode != strings.TrimSpace(currentUser.DeleteConfirmCode) {
		return logicdomain.UserDeleteResult{
			User:                 currentUser,
			Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
			RequiresConfirmation: true,
			ConfirmationCode:     strings.TrimSpace(currentUser.DeleteConfirmCode),
		}, nil
	}

	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("begin postgres delete-user tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	deletePlan, err := r.planUserDelete(callCtx, tx, currentUser.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	now := time.Now().UTC()
	deleteUserProfilesSQL := fmt.Sprintf(`DELETE FROM %s WHERE profile_type = $1 AND bind_id = $2`, r.profileNodesTable())
	if _, err := tx.Exec(callCtx, deleteUserProfilesSQL, logicdomain.ProfileTypeUser, int64(currentUser.ID)); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete postgres user profile nodes by bind: %w", err)
	}
	detachSharedSQL := fmt.Sprintf(`
UPDATE %s
SET turn_id = NULL,
    source_kind = $1,
    source_id = $2,
    status_reason = $3,
    updated_at = $4
WHERE profile_type <> $5
  AND turn_id IN (
    SELECT tr.id
    FROM %s tr
    JOIN %s se ON se.id = tr.session_id
    WHERE se.user_id = $6
  )
`, r.profileNodesTable(), r.turnsTable(), r.sessionsTable())
	if _, err := tx.Exec(
		callCtx,
		strings.TrimSpace(detachSharedSQL),
		logicdomain.ProfileSourceKindRetainedAfterUserDelete,
		int64(currentUser.ID),
		"source user deleted; shared scope node retained without original turn binding",
		now,
		logicdomain.ProfileTypeUser,
		int64(currentUser.ID),
	); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("detach postgres shared profile nodes from deleted user turns: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE user_id = $1`, r.memoryNodesTable()), int64(currentUser.ID)); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete postgres user memory nodes: %w", err)
	}
	deleteTurnsSQL := fmt.Sprintf(`DELETE FROM %s WHERE session_id IN (SELECT id FROM %s WHERE user_id = $1)`, r.turnsTable(), r.sessionsTable())
	if _, err := tx.Exec(callCtx, deleteTurnsSQL, int64(currentUser.ID)); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete postgres user turn records: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE user_id = $1`, r.sessionsTable()), int64(currentUser.ID)); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete postgres user sessions: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, r.usersTable()), int64(currentUser.ID)); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete postgres user row: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("commit postgres delete-user tx: %w", err)
	}
	return logicdomain.UserDeleteResult{
		User:            currentUser,
		Message:         fmt.Sprintf("user %s deleted", currentUser.Name),
		DeletedUsers:    deletePlan.DeletedUsers,
		DeletedSessions: deletePlan.DeletedSessions,
		DeletedMessages: deletePlan.DeletedMessages,
		DeletedMemories: deletePlan.DeletedMemories,
		DeletedProfiles: deletePlan.DeletedProfiles,
	}, nil
}

// ensureUserDeleteConfirmation keeps the first delete step idempotent by generating one confirmation code only when the durable row still has none.
// ensureUserDeleteConfirmation 用于让删除第一步保持幂等：只有当长期行里还没有确认码时才生成新的确认码。
func (r *workspaceRepository) ensureUserDeleteConfirmation(ctx context.Context, user logicdomain.UserRecord) (logicdomain.UserDeleteResult, error) {
	if user.ID == 0 {
		return logicdomain.UserDeleteResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("begin postgres user confirmation tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	currentUser, err := r.loadUserByIDWithQueryer(callCtx, tx, user.ID, true)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	code := strings.TrimSpace(currentUser.DeleteConfirmCode)
	if code == "" {
		generatedCode, err := generateConfirmationCode()
		if err != nil {
			return logicdomain.UserDeleteResult{}, err
		}
		updateSQL := fmt.Sprintf(`
UPDATE %s
SET delete_confirm_code = $1,
    updated_at = $2
WHERE id = $3
  AND delete_confirm_code = ''
`, r.usersTable())
		updateResult, err := tx.Exec(callCtx, strings.TrimSpace(updateSQL), generatedCode, time.Now().UTC(), int64(currentUser.ID))
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("persist postgres user delete confirmation code: %w", err)
		}
		persistedCode := generatedCode
		if updateResult.RowsAffected() == 0 {
			// Re-read the durable row when the guarded update lost a race so the caller always receives the token that can actually authorize deletion.
			// 当带条件的更新在竞态中落败时重新读取持久化行，确保返回给调用方的一定是可实际用于授权删除的令牌。
			currentUser, err = r.loadUserByIDWithQueryer(callCtx, tx, user.ID, false)
			if err != nil {
				return logicdomain.UserDeleteResult{}, err
			}
			persistedCode = currentUser.DeleteConfirmCode
		}
		code, err = finalizeDeleteConfirmationCode("", generatedCode, updateResult.RowsAffected(), persistedCode)
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("resolve postgres user delete confirmation code: %w", err)
		}
		currentUser.DeleteConfirmCode = code
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("commit postgres user confirmation tx: %w", err)
	}
	return logicdomain.UserDeleteResult{
		User:                 currentUser,
		Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
		RequiresConfirmation: true,
		ConfirmationCode:     code,
	}, nil
}

// buildProjectMigrationSessionConflict constructs one stable conflict error for migrations that would duplicate session_key values inside the target project.
// buildProjectMigrationSessionConflict 用于为项目迁移过程中会在目标项目内重复 session_key 的场景构造稳定冲突错误。
func buildProjectMigrationSessionConflict(source, target logicdomain.ProjectRecord, sessionKeys []string) error {
	previewKeys := make([]string, 0, 3)
	for _, key := range sessionKeys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		previewKeys = append(previewKeys, trimmed)
		if len(previewKeys) == 3 {
			break
		}
	}
	message := fmt.Sprintf("cannot migrate project %s into %s because one or more session keys already exist in the target project", source.Path(), target.Path())
	if len(previewKeys) > 0 {
		message = fmt.Sprintf("%s: %s", message, strings.Join(previewKeys, ", "))
		if len(sessionKeys) > len(previewKeys) {
			message = fmt.Sprintf("%s (+%d more)", message, len(sessionKeys)-len(previewKeys))
		}
	}
	return logicdomain.ConflictError{Resource: "session", Message: message}
}

// finalizeDeleteConfirmationCode chooses the durable confirmation token that callers should receive after a guarded update, including zero-row races that require falling back to the persisted value.
// finalizeDeleteConfirmationCode 用于在带条件更新后选出调用方应收到的持久化确认令牌，并覆盖 0 行更新时回退到数据库中真实值的场景。
func finalizeDeleteConfirmationCode(existingCode, generatedCode string, rowsAffected int64, persistedCode string) (string, error) {
	existingCode = strings.TrimSpace(existingCode)
	if existingCode != "" {
		return existingCode, nil
	}
	if rowsAffected > 0 {
		return strings.TrimSpace(generatedCode), nil
	}
	persistedCode = strings.TrimSpace(persistedCode)
	if persistedCode == "" {
		return "", fmt.Errorf("postgres delete confirmation code is still empty after guarded update")
	}
	return persistedCode, nil
}

// loadProjectSessionKeyConflicts lists the session keys that already exist in both the source and target projects so migration can fail fast with a clear conflict.
// loadProjectSessionKeyConflicts 用于列出同时存在于源项目与目标项目的 session key，让迁移可以提前失败并返回清晰的冲突说明。
func (r *workspaceRepository) loadProjectSessionKeyConflicts(ctx context.Context, q profileQueryer, sourceProjectID, targetProjectID uint64) ([]string, error) {
	sqlText := fmt.Sprintf(`
SELECT DISTINCT src.session_key
FROM %s AS src
JOIN %s AS dst
  ON dst.project_id = $2
 AND dst.session_key = src.session_key
WHERE src.project_id = $1
ORDER BY src.session_key ASC
`, r.sessionsTable(), r.sessionsTable())
	callCtx, cancel := r.workspaceQueryContext(ctx)
	defer cancel()
	rows, err := q.Query(callCtx, strings.TrimSpace(sqlText), int64(sourceProjectID), int64(targetProjectID))
	if err != nil {
		return nil, fmt.Errorf("query postgres project migration session conflicts: %w", err)
	}
	defer rows.Close()
	conflicts := make([]string, 0)
	for rows.Next() {
		var sessionKey string
		if err := rows.Scan(&sessionKey); err != nil {
			return nil, fmt.Errorf("scan postgres project migration session conflict: %w", err)
		}
		conflicts = append(conflicts, strings.TrimSpace(sessionKey))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres project migration session conflicts: %w", err)
	}
	return conflicts, nil
}

// lookupTeamByName resolves one team name and reports whether it already exists.
// lookupTeamByName 用于解析单个 team 名称，并返回它是否已经存在。
func (r *workspaceRepository) lookupTeamByName(ctx context.Context, q profileQueryer, teamName string) (logicdomain.TeamRecord, bool, error) {
	sqlText := fmt.Sprintf(`
SELECT id, name, profile, created_at, updated_at
FROM %s
WHERE name = $1
LIMIT 1
`, r.teamsTable())
	var row teamScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), teamName).Scan(&row.ID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.TeamRecord{}, false, nil
		}
		return logicdomain.TeamRecord{}, false, fmt.Errorf("lookup postgres team: %w", err)
	}
	return row.toDomain(), true, nil
}

// lookupSpaceByName resolves one space under the given team and reports whether it already exists.
// lookupSpaceByName 用于在给定 team 下解析单个 space，并返回它是否已经存在。
func (r *workspaceRepository) lookupSpaceByName(ctx context.Context, q profileQueryer, teamID uint64, spaceName string, teamExists bool) (logicdomain.SpaceRecord, bool, error) {
	if !teamExists {
		return logicdomain.SpaceRecord{}, false, nil
	}
	sqlText := fmt.Sprintf(`
SELECT id, team_id, name, profile, created_at, updated_at
FROM %s
WHERE team_id = $1 AND name = $2
LIMIT 1
`, r.spacesTable())
	var row spaceScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(teamID), spaceName).Scan(&row.ID, &row.TeamID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.SpaceRecord{}, false, nil
		}
		return logicdomain.SpaceRecord{}, false, fmt.Errorf("lookup postgres space: %w", err)
	}
	return row.toDomain(), true, nil
}

// lookupProjectBySpaceAndName resolves one project under the given Team/Space hierarchy and reports whether it already exists.
// lookupProjectBySpaceAndName 用于在给定 Team/Space 层级下解析单个 project，并返回它是否已经存在。
func (r *workspaceRepository) lookupProjectBySpaceAndName(ctx context.Context, q profileQueryer, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string) (logicdomain.ProjectRecord, bool, error) {
	sqlText := fmt.Sprintf(`
SELECT id, team_id, space_id, name, profile, created_at, updated_at
FROM %s
WHERE space_id = $1 AND name = $2
LIMIT 1
`, r.projectsTable())
	var row projectScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(space.ID), projectName).Scan(&row.ID, &row.TeamID, &row.SpaceID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.ProjectRecord{}, false, nil
		}
		return logicdomain.ProjectRecord{}, false, fmt.Errorf("lookup postgres project: %w", err)
	}
	row.TeamName = team.Name
	row.SpaceName = space.Name
	return row.toDomain(), true, nil
}

// insertTeam creates one missing team row under the current transaction while collapsing duplicate concurrent creates into the same durable row.
// insertTeam 用于在当前事务中创建缺失的 team 行，并把并发重复创建收敛到同一条长期行上。
func (r *workspaceRepository) insertTeam(ctx context.Context, q profileQueryer, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	// Resolve both the first creator and any duplicate concurrent creator through one upsert statement so the transaction never aborts on a unique-name race.
	// 用单条 upsert 语句同时覆盖“首次创建者”和“并发重复创建者”，避免事务因为 team 名称唯一键竞争而直接中止。
	sqlText := fmt.Sprintf(`
INSERT INTO %s (name, profile, created_at, updated_at)
VALUES ($1, '', $2, $2)
ON CONFLICT (name)
DO UPDATE SET name = EXCLUDED.name
RETURNING id, name, profile, created_at, updated_at
`, r.teamsTable())
	var row teamScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), teamName, now.UTC()).Scan(&row.ID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("insert postgres team: %w", err)
	}
	return row.toDomain(), nil
}

// insertSpace creates one missing space row under an existing team inside the current transaction while remaining idempotent under duplicate concurrent requests.
// insertSpace 用于在当前事务中为已存在的 team 创建缺失的 space 行，并在并发重复请求下保持幂等。
func (r *workspaceRepository) insertSpace(ctx context.Context, q profileQueryer, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	sqlText := fmt.Sprintf(`
INSERT INTO %s (team_id, name, profile, created_at, updated_at)
VALUES ($1, $2, '', $3, $3)
ON CONFLICT (team_id, name)
DO UPDATE SET team_id = EXCLUDED.team_id
RETURNING id, team_id, name, profile, created_at, updated_at
`, r.spacesTable())
	var row spaceScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(teamID), spaceName, now.UTC()).Scan(&row.ID, &row.TeamID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("insert postgres space: %w", err)
	}
	return row.toDomain(), nil
}

// insertProject creates one missing project row under the resolved Team/Space hierarchy inside the current transaction while tolerating duplicate concurrent creates.
// insertProject 用于在当前事务中于已解析的 Team/Space 层级下创建缺失的 project 行，并容忍并发重复创建。
func (r *workspaceRepository) insertProject(ctx context.Context, q profileQueryer, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	sqlText := fmt.Sprintf(`
INSERT INTO %s (team_id, space_id, name, profile, created_at, updated_at)
VALUES ($1, $2, $3, '', $4, $4)
ON CONFLICT (space_id, name)
DO UPDATE SET team_id = EXCLUDED.team_id, space_id = EXCLUDED.space_id
RETURNING id, team_id, space_id, name, profile, created_at, updated_at
`, r.projectsTable())
	var row projectScanRow
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(team.ID), int64(space.ID), projectName, now.UTC()).Scan(&row.ID, &row.TeamID, &row.SpaceID, &row.Name, &row.Profile, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("insert postgres project: %w", err)
	}
	row.TeamName = team.Name
	row.SpaceName = space.Name
	return row.toDomain(), nil
}

// countRows centralizes the one-row COUNT(*) query pattern used by delete and migration planning.
// countRows 用于集中处理删除和迁移规划中使用的单行 COUNT(*) 查询模式。
func (r *workspaceRepository) countRows(ctx context.Context, q profileQueryer, sqlText string, args ...any) (int, error) {
	var count int
	if err := q.QueryRow(ctx, strings.TrimSpace(sqlText), args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// countProjectRows returns project-scoped row counts so delete and migrate operations can report meaningful summaries.
// countProjectRows 用于返回项目范围内的行计数，让删除和迁移操作能够输出有意义的结果摘要。
func (r *workspaceRepository) countProjectRows(ctx context.Context, q profileQueryer, projectID uint64) (int, int, int, error) {
	sessionCount, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id = $1`, r.sessionsTable()), int64(projectID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres project sessions: %w", err)
	}
	messageCount, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id = $1`, r.turnsTable()), int64(projectID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres project turn records: %w", err)
	}
	memoryCount, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id = $1`, r.memoryNodesTable()), int64(projectID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres project memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryCount, nil
}

// countUserRows returns user-scoped row counts so protected user deletion can explain what will be removed.
// countUserRows 用于返回用户范围内的行计数，让受保护的用户删除能够说明将要删除的内容。
func (r *workspaceRepository) countUserRows(ctx context.Context, q profileQueryer, userID uint64) (int, int, int, error) {
	sessionCount, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE user_id = $1`, r.sessionsTable()), int64(userID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres user sessions: %w", err)
	}
	messageSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE session_id IN (SELECT id FROM %s WHERE user_id = $1)`, r.turnsTable(), r.sessionsTable())
	messageCount, err := r.countRows(ctx, q, messageSQL, int64(userID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres user turn records: %w", err)
	}
	memoryCount, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE user_id = $1`, r.memoryNodesTable()), int64(userID))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count postgres user memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryCount, nil
}

// planProjectDelete computes the concrete delete counts and empty-parent cascade decisions for one resolved project.
// planProjectDelete 用于为一个已解析项目计算实际删除计数，以及空父级的级联删除决策。
func (r *workspaceRepository) planProjectDelete(ctx context.Context, q profileQueryer, project logicdomain.ProjectRecord) (projectDeletePlan, error) {
	sessions, messages, memories, err := r.countProjectRows(ctx, q, project.ID)
	if err != nil {
		return projectDeletePlan{}, err
	}
	remainingProjectsInSpace, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE space_id = $1 AND id <> $2`, r.projectsTable()), int64(project.SpaceID), int64(project.ID))
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count postgres remaining projects in space: %w", err)
	}
	deleteSpace := remainingProjectsInSpace == 0
	deleteTeam := false
	if deleteSpace {
		remainingSpacesInTeam, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE team_id = $1 AND id <> $2`, r.spacesTable()), int64(project.TeamID), int64(project.SpaceID))
		if err != nil {
			return projectDeletePlan{}, fmt.Errorf("count postgres remaining spaces in team: %w", err)
		}
		deleteTeam = remainingSpacesInTeam == 0
	}
	whereSQL, args := r.buildProjectProfileNodesWhere(project.ID, project.SpaceID, project.TeamID, deleteSpace, deleteTeam)
	deletedProfiles, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, r.profileNodesTable(), whereSQL), args...)
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count postgres project profile nodes: %w", err)
	}
	plan := projectDeletePlan{
		DeletedProjects: 1,
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
		DeletedProfiles: deletedProfiles,
	}
	if deleteSpace {
		plan.DeletedSpaces = 1
	}
	if deleteTeam {
		plan.DeletedTeams = 1
	}
	return plan, nil
}

// planUserDelete computes the concrete delete counts for one resolved user while excluding shared-scope profile nodes that are retained after the user disappears.
// planUserDelete 用于为一个已解析用户计算实际删除计数，并排除用户删除后仍会保留的共享范围画像节点。
func (r *workspaceRepository) planUserDelete(ctx context.Context, q profileQueryer, userID uint64) (userDeletePlan, error) {
	sessions, messages, memories, err := r.countUserRows(ctx, q, userID)
	if err != nil {
		return userDeletePlan{}, err
	}
	deletedProfiles, err := r.countRows(ctx, q, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE profile_type = $1 AND bind_id = $2`, r.profileNodesTable()), logicdomain.ProfileTypeUser, int64(userID))
	if err != nil {
		return userDeletePlan{}, fmt.Errorf("count postgres user profile nodes: %w", err)
	}
	return userDeletePlan{
		DeletedUsers:    1,
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
		DeletedProfiles: deletedProfiles,
	}, nil
}

// buildProjectProfileNodesWhere centralizes the delete/count predicate used by project deletion so statistics and actual row removal stay locked to the same scope definition.
// buildProjectProfileNodesWhere 用于集中维护项目删除时的画像节点条件，让删除统计与实际删行始终共享同一套范围定义。
func (r *workspaceRepository) buildProjectProfileNodesWhere(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	args := &sqlArgsBuilder{}
	conditions := []string{
		fmt.Sprintf("(profile_type = %s AND bind_id = %s)", args.Add(logicdomain.ProfileTypeProject), args.Add(int64(projectID))),
		fmt.Sprintf("(turn_id IN (SELECT id FROM %%TURN_TABLE%% WHERE project_id = %s))", args.Add(int64(projectID))),
	}
	if deleteSpace {
		conditions = append(conditions, fmt.Sprintf("(profile_type = %s AND bind_id = %s)", args.Add(logicdomain.ProfileTypeSpace), args.Add(int64(spaceID))))
	}
	if deleteTeam {
		conditions = append(conditions, fmt.Sprintf("(profile_type = %s AND bind_id = %s)", args.Add(logicdomain.ProfileTypeTeam), args.Add(int64(teamID))))
	}
	whereSQL := strings.Join(conditions, " OR ")
	whereSQL = strings.ReplaceAll(whereSQL, "%TURN_TABLE%", r.turnsTable())
	return whereSQL, args.Args()
}

// buildProjectConfirmMessage generates the stable confirmation text returned when missing Team/Space nodes require explicit confirmation.
// buildProjectConfirmMessage 用于生成稳定的确认提示文本，说明缺失 Team/Space 节点需要显式确认。
func buildProjectConfirmMessage(teamName, spaceName, projectName string, teamExists, spaceExists bool) string {
	missing := make([]string, 0, 2)
	if !teamExists {
		missing = append(missing, "team")
	}
	if !spaceExists {
		missing = append(missing, "space")
	}
	return fmt.Sprintf("path %s/%s/%s is incomplete; missing %s, use confirm_create=1 to create them", teamName, spaceName, projectName, strings.Join(missing, ", "))
}

// EnsureProjectPath delegates to the workspace repository for project path resolution or creation.
// EnsureProjectPath 用于把项目路径解析或创建委托给 workspace 仓储。
func (s *Store) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	return s.repos.workspace.EnsureProjectPath(ctx, projectPath, confirmCreate)
}

// DeleteProjectPath delegates to the workspace repository for project path deletion.
// DeleteProjectPath 用于把项目路径删除委托给 workspace 仓储。
func (s *Store) DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	return s.repos.workspace.DeleteProjectPath(ctx, projectPath, confirmDelete)
}

// MigrateProjectPath delegates to the workspace repository for project path migration.
// MigrateProjectPath 用于把项目路径迁移委托给 workspace 仓储。
func (s *Store) MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	return s.repos.workspace.MigrateProjectPath(ctx, sourcePath, targetPath, confirm)
}

// DeleteUserRef delegates to the workspace repository for user reference deletion.
// DeleteUserRef 用于把用户引用删除委托给 workspace 仓储。
func (s *Store) DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	return s.repos.workspace.DeleteUserRef(ctx, userRef, confirmationCode)
}
