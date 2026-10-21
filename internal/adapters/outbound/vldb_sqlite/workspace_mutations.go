// SQLite project and user management operations with mutation outcome checks.
// SQLite 适配层项目与用户管理操作及变更结果检查。
package vldb_sqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// generateConfirmationCode returns one 32-character random hex token used by protected destructive flows.
// generateConfirmationCode 用于生成 32 位随机十六进制确认码，服务需要保护的破坏性操作。
func generateConfirmationCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate confirmation code: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// EnsureProjectPath resolves or creates a Team/Space/Project path according to the confirm flag rules required by the admin RPCs.
// EnsureProjectPath 用于按管理 RPC 约定的确认规则，解析或创建一个 Team/Space/Project 路径。
func (s *Store) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	teamName, spaceName, projectName, err := storageutil.ParseProjectPath(projectPath)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if existing, err := s.ResolveProjectRef(ctx, projectPath); err == nil {
		return logicdomain.ProjectMutationResult{
			Project: existing,
			Message: fmt.Sprintf("project %s already exists", existing.Path()),
			Exists:  true,
		}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.ProjectMutationResult{}, err
	}

	team, teamExists, err := s.lookupTeamByName(ctx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err := s.lookupSpaceByName(ctx, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      storageutil.ProjectCreateConfirmationMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	createdTeam := false
	createdSpace := false
	createdProject := false

	// Re-resolve the hierarchy under the write lock so the confirmation decision and inserts are based
	// on the same serialized SQLite window instead of stale pre-lock lookup results.
	// 在写锁内重新解析层级，让确认决策和插入基于同一个串行 SQLite 窗口，而不是锁前的陈旧查询结果。
	team, teamExists, err = s.lookupTeamByName(ctx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err = s.lookupSpaceByName(ctx, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      storageutil.ProjectCreateConfirmationMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}
	if !teamExists {
		team, err = s.insertTeam(ctx, teamName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdTeam = true
		space, spaceExists, err = s.lookupSpaceByName(ctx, team.ID, spaceName, true)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam, "ensure project path", err)
		}
	}
	if !spaceExists {
		space, err = s.insertSpace(ctx, team.ID, spaceName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam, "ensure project path", err)
		}
		createdSpace = true
	}
	project, projectExists, err := s.lookupProjectBySpaceAndName(ctx, team, space, projectName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam || createdSpace, "ensure project path", err)
	}
	if projectExists {
		return logicdomain.ProjectMutationResult{
			Project: project,
			Message: fmt.Sprintf("project %s already exists", project.Path()),
			Exists:  true,
		}, nil
	}
	project, err = s.insertProject(ctx, team, space, projectName, now)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam || createdSpace, "ensure project path", err)
	}
	createdProject = true
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
func (s *Store) DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	project, err := s.ResolveProjectRef(ctx, projectPath)
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Plan all row counts and hierarchy cascades under the same write lock so the reported numbers
	// match the concrete delete path seen by this runtime instance.
	// 在同一把写锁下规划删除计数和层级级联条件，确保当前运行时返回的统计与实际删除路径一致。
	deletePlan, err := s.planProjectDelete(ctx, project)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}

	// Remove all profile nodes that belong to the project itself, were derived from its turns,
	// or belong to one now-empty parent scope that will be deleted together with the project.
	// 删除项目自身、其 turn 派生、以及随空父级一并删除的 scope 所关联的画像节点。
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(project.ID, project.SpaceID, project.TeamID, deletePlan.DeletedSpaces > 0, deletePlan.DeletedTeams > 0)
	// Track confirmed destructive steps so any later ordinary write failure cannot be reported as a clean retryable delete failure.
	// 跟踪已确认的破坏性步骤，避免后续普通写入失败被误报为可干净重试的删除失败。
	mutated := false
	runDeleteStep := func(operation string, expectedRows int, sql string, params ...any) error {
		if err := s.execProjectDeleteStep(ctx, operation, mutated, expectedRows, sql, params...); err != nil {
			return err
		}
		mutated = mutated || expectedRows > 0
		return nil
	}
	if err := runDeleteStep("delete project profile nodes", deletePlan.DeletedProfiles, deleteSQL, deleteParams...); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project memory nodes", deletePlan.DeletedMemories, `DELETE FROM vmm_memory_nodes WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project turn records", deletePlan.DeletedMessages, `DELETE FROM vmm_turn_records WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project sessions", deletePlan.DeletedSessions, `DELETE FROM vmm_sessions WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project row", deletePlan.DeletedProjects, `DELETE FROM vmm_projects WHERE id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if deletePlan.DeletedSpaces > 0 {
		if err := runDeleteStep("delete empty project space row", deletePlan.DeletedSpaces, `DELETE FROM vmm_spaces WHERE id = ?`, project.SpaceID); err != nil {
			return logicdomain.ProjectDeleteResult{}, err
		}
	}
	if deletePlan.DeletedTeams > 0 {
		if err := runDeleteStep("delete empty project team row", deletePlan.DeletedTeams, `DELETE FROM vmm_teams WHERE id = ?`, project.TeamID); err != nil {
			return logicdomain.ProjectDeleteResult{}, err
		}
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

// execProjectDeleteStep executes one planned project-delete write and classifies failures using both commit-boundary evidence and prior destructive progress.
// execProjectDeleteStep 用于执行一次已规划的项目删除写入，并结合提交边界证据与前序破坏性进度来分类失败。
func (s *Store) execProjectDeleteStep(ctx context.Context, operation string, mutated bool, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "delete project path", fmt.Errorf("%s: %w", operation, err))
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "delete project path",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// MigrateProjectPath moves SQL-backed sessions, turn records, memories, and project-bound profiles from one project scope onto another when explicitly confirmed.
// MigrateProjectPath 用于在显式确认后，把 SQL 侧 sessions、turn 记录、memories 和项目绑定画像从源项目范围迁移到目标项目范围。
func (s *Store) MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	source, err := s.ResolveProjectRef(ctx, sourcePath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err := s.ResolveProjectRef(ctx, targetPath)
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-read both migration endpoints after acquiring the write lock so a stale path resolution cannot
	// migrate rows against a deleted or recreated project node.
	// 获取写锁后重新读取迁移两端，避免陈旧路径解析把数据迁移到已删除或重建的项目节点上。
	source, err = s.verifyProjectMigrationEndpoint(ctx, "source", source)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err = s.verifyProjectMigrationEndpoint(ctx, "target", target)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}

	// Plan migration counts inside the write lock so the later affected-row checks validate the exact
	// source scope observed by this serialized SQLite mutation path.
	// 在写锁内规划迁移计数，让后续影响行数校验对齐当前串行 SQLite 写入路径实际观察到的源范围。
	migrationPlan, err := s.planProjectMigration(ctx, source.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()
	mutated := false
	if err := s.execProjectMigrationStep(ctx, "migrate project sessions", migrationPlan.MigratedSessions, `
UPDATE vmm_sessions
SET team_id = ?, space_id = ?, project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedSessions > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project turn records", migrationPlan.MigratedMessages, `
UPDATE vmm_turn_records
SET project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedMessages > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project memory nodes", migrationPlan.MigratedMemories, `
UPDATE vmm_memory_nodes
SET team_id = ?, space_id = ?, project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedMemories > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project profile nodes", migrationPlan.MigratedProfiles, `
UPDATE vmm_profile_nodes
SET bind_id = ?
WHERE profile_type = ? AND bind_id = ?
`, target.ID, logicdomain.ProfileTypeProject, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	return logicdomain.ProjectMigrationResult{
		Source:           source,
		Target:           target,
		Message:          fmt.Sprintf("migrated project %s -> %s", source.Path(), target.Path()),
		MigratedSessions: migrationPlan.MigratedSessions,
		MigratedMessages: migrationPlan.MigratedMessages,
		MigratedMemories: migrationPlan.MigratedMemories,
	}, nil
}

// execProjectMigrationStep executes one planned project-migration write and rejects affected-row drift before vector rebuild can start.
// execProjectMigrationStep 用于执行一次已规划的项目迁移写入，并在向量重建开始前拦截影响行数漂移。
func (s *Store) execProjectMigrationStep(ctx context.Context, operation string, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			return logicdomain.OutcomeUncertainError{
				Operation: "migrate project path",
				Message:   fmt.Sprintf("%s: %v", operation, err),
			}
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "migrate project path",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// verifyProjectMigrationEndpoint reloads one migration endpoint under the write lock and rejects stale source or target identity before any rows move.
// verifyProjectMigrationEndpoint 用于在写锁下重新读取迁移端点，并在任何行迁移前拒绝陈旧的源或目标身份。
func (s *Store) verifyProjectMigrationEndpoint(ctx context.Context, role string, resolved logicdomain.ProjectRecord) (logicdomain.ProjectRecord, error) {
	current, err := s.loadProjectByID(ctx, resolved.ID)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("reload %s project before migration: %w", role, err)
	}
	if !sameProjectIdentity(current, resolved) {
		return logicdomain.ProjectRecord{}, logicdomain.ConflictError{
			Resource: "project",
			Message:  fmt.Sprintf("%s project changed before migration: resolved %s, current %s", role, resolved.Path(), current.Path()),
		}
	}
	return current, nil
}

// sameProjectIdentity compares the stable hierarchy identity fields that prove two project records refer to the same durable node.
// sameProjectIdentity 用于比较稳定的层级身份字段，确认两条项目记录指向同一个长期节点。
func sameProjectIdentity(left, right logicdomain.ProjectRecord) bool {
	return left.ID == right.ID &&
		left.TeamID == right.TeamID &&
		left.SpaceID == right.SpaceID &&
		left.TeamName == right.TeamName &&
		left.SpaceName == right.SpaceName &&
		left.Name == right.Name &&
		left.CreatedAt.Equal(right.CreatedAt)
}

// sameUserIdentity compares the stable user identity fields that prove one numeric user id was not deleted and reused.
// sameUserIdentity 用于比较稳定的用户身份字段，确认一个数字 user id 没有被删除后复用。
func sameUserIdentity(left, right logicdomain.UserRecord) bool {
	return left.ID == right.ID &&
		left.Name == right.Name &&
		left.CreatedAt.Equal(right.CreatedAt)
}

// ResolveUserRef resolves either a numeric user id or a unique user name.
// ResolveUserRef 用于解析数字 user id，或唯一用户名。
func (s *Store) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	userRef = strings.TrimSpace(userRef)
	if userRef == "" {
		return logicdomain.UserRecord{}, logicdomain.ValidationError{Field: "user_ref", Message: "is required"}
	}
	if userID, ok := storageutil.ParsePositiveUint64(userRef); ok {
		return s.loadUserByID(ctx, userID)
	}
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE name = ?
LIMIT 1
`, userRef)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("resolve user by name: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist", userRef)}
	}
	return rows[0].toDomain(), nil
}

// EnsureUserName resolves one user by name or creates it when confirmation is explicit.
// EnsureUserName 用于按名称解析用户，或在确认创建时补建该用户。
func (s *Store) EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return logicdomain.UserResolveResult{}, logicdomain.ValidationError{Field: "user_name", Message: "is required"}
	}
	if user, err := s.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s already exists", user.Name), Exists: true}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	if !confirmCreate {
		return logicdomain.UserResolveResult{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist; use confirm_create=1 to create it", userName)}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-read under the write lock so duplicate in-process create requests converge to the existing row
	// before allocating a new numeric id.
	// 在写锁下重新读取，让进程内重复创建请求在分配新数字 ID 前收敛到已存在行。
	if user, err := s.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s already exists", user.Name), Exists: true}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	now := time.Now().UTC()
	user, err := s.insertUser(ctx, userName, now)
	if err != nil {
		return logicdomain.UserResolveResult{}, err
	}
	return logicdomain.UserResolveResult{
		User:    user,
		Message: fmt.Sprintf("user %s created", userName),
		Created: true,
	}, nil
}

// ListUsers returns all durable users ordered by id for deterministic management output.
// ListUsers 用于按 id 稳定输出所有长期用户。
func (s *Store) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
ORDER BY id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]logicdomain.UserRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// DeleteUserRef executes the protected two-phase user deletion flow and removes the user plus all SQL-side dependent data.
// DeleteUserRef 用于执行受保护的双阶段用户删除流程，并删除该用户及其所有 SQL 侧关联数据。
func (s *Store) DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	user, err := s.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}

	// Keep the first-phase confirmation idempotent so duplicate admin clicks reuse one stable confirmation code
	// instead of issuing concurrent row updates that can conflict inside the SQLite gateway.
	// 把删除确认第一阶段做成幂等流程，让重复的管理端点击复用同一确认码，
	// 避免在 SQLite 网关里对同一行发起并发更新而相互冲突。
	confirmationCode = strings.TrimSpace(confirmationCode)
	if confirmationCode == "" {
		return s.ensureUserDeleteConfirmation(ctx, user)
	}

	// Reload the latest durable code before destructive work so stale callers do not rotate the confirmation token.
	// 在真正删除前重新加载最新确认码，避免陈旧调用方把确认令牌无谓轮换。
	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if strings.TrimSpace(currentUser.DeleteConfirmCode) == "" {
		return s.ensureUserDeleteConfirmation(ctx, currentUser)
	}
	if confirmationCode != strings.TrimSpace(currentUser.DeleteConfirmCode) {
		return logicdomain.UserDeleteResult{
			User:                 currentUser,
			Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
			RequiresConfirmation: true,
			ConfirmationCode:     strings.TrimSpace(currentUser.DeleteConfirmCode),
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	deletePlan, err := s.planUserDelete(ctx, currentUser.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()

	// mutated records whether a prior planned delete step has already changed durable rows in this autocommit sequence.
	// mutated 用于记录此前规划删除步骤是否已经在本次自动提交序列中改变长期行。
	mutated := false
	// runDeleteStep executes one planned write and updates mutation state only after the affected-row check succeeds.
	// runDeleteStep 用于执行一条规划写入，并且只在影响行数校验成功后更新写入状态。
	runDeleteStep := func(operation string, expectedRows int, sql string, params ...any) error {
		if err := s.execUserDeleteStep(ctx, operation, mutated, expectedRows, sql, params...); err != nil {
			return err
		}
		mutated = mutated || expectedRows > 0
		return nil
	}

	if err := runDeleteStep("delete user profile nodes by bind", deletePlan.DeletedProfiles, `DELETE FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeUser, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	// Detach surviving shared-scope profile facts from the user's historical turns before those turn rows disappear.
	// These nodes must keep living under project/team/space scope, but they can no longer point at deleted turn ids.
	// 在删除用户历史 turn 前，先把仍应保留的共享范围画像节点与原 turn 脱钩。
	// 这些节点继续归属于 project/team/space，但不能再指向即将被删除的 turn id。
	detachSQL, detachParams := buildUserSharedProfileDetachUpdateSQL(currentUser.ID, nowMs)
	if err := runDeleteStep("detach surviving shared profile nodes from deleted user turns", deletePlan.DetachedProfiles, detachSQL, detachParams...); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user memory nodes", deletePlan.DeletedMemories, `DELETE FROM vmm_memory_nodes WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user turn records", deletePlan.DeletedMessages, `DELETE FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user sessions", deletePlan.DeletedSessions, `DELETE FROM vmm_sessions WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user row", deletePlan.DeletedUsers, `DELETE FROM vmm_users WHERE id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
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

// execUserDeleteStep executes one planned user-delete write and classifies execution errors by confirmed prior mutation before rejecting affected-row drift.
// execUserDeleteStep 用于执行一次已规划的用户删除写入，并先按已确认的前序写入分类执行错误，再拦截影响行数漂移。
func (s *Store) execUserDeleteStep(ctx context.Context, operation string, mutated bool, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "delete user ref", fmt.Errorf("%s: %w", operation, err))
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "delete user ref",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// ensureUserDeleteConfirmation makes the first delete step idempotent by generating one confirmation code only when the durable row still has none.
// ensureUserDeleteConfirmation 用于让删除第一步保持幂等：只有当长期行里还没有确认码时才生成新的确认码。
func (s *Store) ensureUserDeleteConfirmation(ctx context.Context, user logicdomain.UserRecord) (logicdomain.UserDeleteResult, error) {
	if user.ID == 0 {
		return logicdomain.UserDeleteResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}

	// Serialize confirmation-code generation so duplicate delete requests cannot race on the same user row.
	// 串行化确认码生成，避免重复删除请求在同一用户行上发生竞争更新。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	code := strings.TrimSpace(currentUser.DeleteConfirmCode)
	if code == "" {
		code, err = generateConfirmationCode()
		if err != nil {
			return logicdomain.UserDeleteResult{}, err
		}
		confirmationStatement := parameterizedUserDeleteConfirmationStatement(currentUser.ID, code, time.Now().UTC().Format(time.RFC3339Nano))
		resp, err := s.execResult(ctx, confirmationStatement.SQL, confirmationStatement.Params...)
		if err != nil {
			wrappedErr := fmt.Errorf("persist user delete confirmation code: %w", err)
			classifiedErr := sqliteWriteCommitBoundaryError("persist user delete confirmation code", wrappedErr)
			if !logicdomain.IsOutcomeUncertain(classifiedErr) {
				return logicdomain.UserDeleteResult{}, wrappedErr
			}
			reconciledUser, reconciledCode, recovered, reconcileErr := s.reconcileUserDeleteConfirmationCode(ctx, currentUser.ID)
			if reconcileErr != nil {
				return logicdomain.UserDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "persist user delete confirmation code",
					Message:   classifiedErr.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !recovered {
				return logicdomain.UserDeleteResult{}, classifiedErr
			}
			currentUser = reconciledUser
			code = reconciledCode
		} else if resp.RowsChanged != 1 {
			// Reload after a guarded no-op so idempotent confirmation returns the durable code written by the winning request.
			// 受保护更新未命中时重新读取，让幂等确认流程返回获胜请求已经写入的长期确认码。
			reconciledUser, reconciledCode, recovered, err := s.reconcileUserDeleteConfirmationCode(ctx, currentUser.ID)
			if err != nil {
				return logicdomain.UserDeleteResult{}, err
			}
			if !recovered {
				return logicdomain.UserDeleteResult{}, fmt.Errorf("persist user delete confirmation code affected %d rows, want 1", resp.RowsChanged)
			}
			currentUser = reconciledUser
			code = reconciledCode
		} else {
			currentUser.DeleteConfirmCode = code
		}
	}
	return logicdomain.UserDeleteResult{
		User:                 currentUser,
		Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
		RequiresConfirmation: true,
		ConfirmationCode:     code,
	}, nil
}

// reconcileUserDeleteConfirmationCode reloads the durable user row after an ambiguous or guarded first-phase write and accepts any stored confirmation code as the source of truth.
// reconcileUserDeleteConfirmationCode 用于在第一阶段写入不明确或受保护更新未命中后重载长期用户行，并把任何已落库确认码作为真实结果。
func (s *Store) reconcileUserDeleteConfirmationCode(ctx context.Context, userID uint64) (logicdomain.UserRecord, string, bool, error) {
	currentUser, err := s.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.UserRecord{}, "", false, err
	}
	code := strings.TrimSpace(currentUser.DeleteConfirmCode)
	if code == "" {
		return currentUser, "", false, nil
	}
	return currentUser, code, true, nil
}
