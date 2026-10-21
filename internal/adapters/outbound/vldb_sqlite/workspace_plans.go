// SQLite workspace mutation planning and affected-row counts.
// SQLite 适配层工作区变更计划与影响行数统计。
package vldb_sqlite

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
)

// lookupTeamByName resolves one team name and reports whether it already exists.
// lookupTeamByName 用于解析单个 team 名称，并返回它是否已经存在。
func (s *Store) lookupTeamByName(ctx context.Context, teamName string) (logicdomain.TeamRecord, bool, error) {
	rows, err := queryRows[teamRow](s, ctx, `
SELECT id, name, profile, created_at, updated_at
FROM vmm_teams
WHERE name = ?
LIMIT 1
`, teamName)
	if err != nil {
		return logicdomain.TeamRecord{}, false, fmt.Errorf("lookup team: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.TeamRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// lookupSpaceByName resolves one space under the given team and reports whether it already exists.
// lookupSpaceByName 用于在给定 team 下解析单个 space，并返回它是否已经存在。
func (s *Store) lookupSpaceByName(ctx context.Context, teamID uint64, spaceName string, teamExists bool) (logicdomain.SpaceRecord, bool, error) {
	if !teamExists {
		return logicdomain.SpaceRecord{}, false, nil
	}
	rows, err := queryRows[spaceRow](s, ctx, `
SELECT id, team_id, name, profile, created_at, updated_at
FROM vmm_spaces
WHERE team_id = ? AND name = ?
LIMIT 1
`, teamID, spaceName)
	if err != nil {
		return logicdomain.SpaceRecord{}, false, fmt.Errorf("lookup space: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SpaceRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// lookupProjectBySpaceAndName resolves one project under the locked Team/Space hierarchy and reports whether it already exists.
// lookupProjectBySpaceAndName 用于在已加锁的 Team/Space 层级下解析单个 project，并返回它是否已经存在。
func (s *Store) lookupProjectBySpaceAndName(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string) (logicdomain.ProjectRecord, bool, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.space_id = ? AND p.name = ?
LIMIT 1
`, space.ID, projectName)
	if err != nil {
		return logicdomain.ProjectRecord{}, false, fmt.Errorf("lookup project: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, false, nil
	}
	project := rows[0].toDomain()
	if project.TeamID != team.ID || project.SpaceID != space.ID {
		return logicdomain.ProjectRecord{}, false, logicdomain.ConflictError{Resource: "project", Message: fmt.Sprintf("project %s resolved outside expected team/space", projectName)}
	}
	return project, true, nil
}

// countProjectRows returns project-scoped row counts so delete and migrate operations can report meaningful summaries.
// countProjectRows 用于返回项目范围内的行计数，让删除和迁移操作能够输出有意义的结果摘要。
func (s *Store) countProjectRows(ctx context.Context, projectID uint64) (int, int, int, error) {
	sessionCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project sessions: %w", err)
	}
	messageCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project turn records: %w", err)
	}
	memoryNodeCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryNodeCount, nil
}

// countUserRows returns user-scoped row counts so protected user deletion can explain what will be removed.
// countUserRows 用于返回用户范围内的行计数，让受保护的用户删除能够说明将要删除的内容。
func (s *Store) countUserRows(ctx context.Context, userID uint64) (int, int, int, error) {
	sessionCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user sessions: %w", err)
	}
	messageCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user turn records: %w", err)
	}
	memoryNodeCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryNodeCount, nil
}

// countRows keeps the admin delete and migrate paths concise by centralizing the one-row COUNT(*) query pattern.
// countRows 用于集中处理单行 COUNT(*) 查询模式，让管理类删除和迁移路径保持简洁。
func (s *Store) countRows(ctx context.Context, sql string, params ...any) (int, error) {
	rows, err := queryRows[countRow](s, ctx, sql, params...)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Count, nil
}

// projectDeletePlan records the exact row categories that one project deletion will remove,
// including hierarchy parents that become empty after the project disappears.
// projectDeletePlan 用于记录一次项目删除将真正移除的行类别，
// 包括项目删除后因变空而需要级联删除的父级层级节点。
type projectDeletePlan struct {
	DeletedProjects int
	DeletedSpaces   int
	DeletedTeams    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
}

// projectMigrationPlan records the exact source rows that one confirmed project migration must move before vector rebuild starts.
// projectMigrationPlan 用于记录一次已确认项目迁移在向量重建开始前必须移动的精确源行。
type projectMigrationPlan struct {
	// MigratedSessions records session rows whose hierarchy ids move to the target project.
	// MigratedSessions 用于记录层级 ID 会迁移到目标项目的 session 行数。
	MigratedSessions int
	// MigratedMessages records turn rows whose project id moves to the target project.
	// MigratedMessages 用于记录 project id 会迁移到目标项目的 turn 行数。
	MigratedMessages int
	// MigratedMemories records SQL-backed memory rows whose project scope moves to the target project.
	// MigratedMemories 用于记录项目范围会迁移到目标项目的 SQL 侧 memory 行数。
	MigratedMemories int
	// MigratedProfiles records project-scope profile nodes rebound from the source project to the target project.
	// MigratedProfiles 用于记录从源项目重新绑定到目标项目的项目级画像节点数。
	MigratedProfiles int
}

// userDeletePlan records the exact row categories that one user deletion will remove or detach before turn rows disappear.
// userDeletePlan 用于记录一次用户删除将真正移除的行类别，以及 turn 行消失前必须脱钩的共享画像数量。
type userDeletePlan struct {
	DeletedUsers    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
	// DetachedProfiles records shared-scope profile nodes whose turn references must be cleared before deleting the user turns.
	// DetachedProfiles 用于记录在删除用户 turn 前必须清除 turn 引用的共享范围画像节点数量。
	DetachedProfiles int
}

// planProjectDelete computes the concrete delete counts and empty-parent cascade decisions for one resolved project.
// planProjectDelete 用于为一个已解析项目计算实际删除计数，以及空父级的级联删除决策。
func (s *Store) planProjectDelete(ctx context.Context, project logicdomain.ProjectRecord) (projectDeletePlan, error) {
	sessions, messages, memories, err := s.countProjectRows(ctx, project.ID)
	if err != nil {
		return projectDeletePlan{}, err
	}
	remainingProjectsInSpace, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_projects WHERE space_id = ? AND id <> ?`, project.SpaceID, project.ID)
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count remaining projects in project space: %w", err)
	}
	deleteSpace := remainingProjectsInSpace == 0
	deleteTeam := false
	if deleteSpace {
		remainingSpacesInTeam, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_spaces WHERE team_id = ? AND id <> ?`, project.TeamID, project.SpaceID)
		if err != nil {
			return projectDeletePlan{}, fmt.Errorf("count remaining spaces in project team: %w", err)
		}
		deleteTeam = remainingSpacesInTeam == 0
	}
	countSQL, countParams := buildProjectProfileNodesCountSQL(project.ID, project.SpaceID, project.TeamID, deleteSpace, deleteTeam)
	deletedProfiles, err := s.countRows(ctx, countSQL, countParams...)
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count project profile nodes: %w", err)
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

// planProjectMigration computes the concrete migration counts for one resolved source project, including project-bound profile nodes that are moved internally but not exposed in the public RPC summary.
// planProjectMigration 用于为一个已解析源项目计算实际迁移计数，包括内部迁移但不暴露到公开 RPC 摘要中的项目绑定画像节点。
func (s *Store) planProjectMigration(ctx context.Context, sourceProjectID uint64) (projectMigrationPlan, error) {
	sessions, messages, memories, err := s.countProjectRows(ctx, sourceProjectID)
	if err != nil {
		return projectMigrationPlan{}, err
	}
	profiles, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeProject, sourceProjectID)
	if err != nil {
		return projectMigrationPlan{}, fmt.Errorf("count project migration profile nodes: %w", err)
	}
	return projectMigrationPlan{
		MigratedSessions: sessions,
		MigratedMessages: messages,
		MigratedMemories: memories,
		MigratedProfiles: profiles,
	}, nil
}

// planUserDelete computes the concrete delete counts for one resolved user while excluding shared-scope profile nodes
// that are retained after the user and their turns disappear.
// planUserDelete 用于为一个已解析用户计算实际删除计数，并排除那些会在用户和其 turn 删除后仍被保留的共享范围画像节点。
func (s *Store) planUserDelete(ctx context.Context, userID uint64) (userDeletePlan, error) {
	sessions, messages, memories, err := s.countUserRows(ctx, userID)
	if err != nil {
		return userDeletePlan{}, err
	}
	deletedProfiles, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeUser, userID)
	if err != nil {
		return userDeletePlan{}, fmt.Errorf("count user profile nodes: %w", err)
	}
	detachSQL, detachParams := buildUserSharedProfileDetachCountSQL(userID)
	detachedProfiles, err := s.countRows(ctx, detachSQL, detachParams...)
	if err != nil {
		return userDeletePlan{}, fmt.Errorf("count shared profile nodes detached from deleted user turns: %w", err)
	}
	return userDeletePlan{
		DeletedUsers:     1,
		DeletedSessions:  sessions,
		DeletedMessages:  messages,
		DeletedMemories:  memories,
		DeletedProfiles:  deletedProfiles,
		DetachedProfiles: detachedProfiles,
	}, nil
}

// buildUserSharedProfileDetachCountSQL renders the COUNT query for shared profile nodes that will survive a user delete but must lose their source turn pointer.
// buildUserSharedProfileDetachCountSQL 用于渲染共享画像节点脱钩前的 COUNT 查询，这些节点会在用户删除后保留但必须失去源 turn 指针。
func buildUserSharedProfileDetachCountSQL(userID uint64) (string, []any) {
	whereSQL, params := buildUserSharedProfileDetachWhere(userID)
	return fmt.Sprintf("SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildUserSharedProfileDetachUpdateSQL renders the UPDATE that detaches surviving shared profile nodes from turns owned by the deleted user.
// buildUserSharedProfileDetachUpdateSQL 用于渲染共享画像节点脱钩 UPDATE，将保留节点与被删除用户拥有的 turn 断开。
func buildUserSharedProfileDetachUpdateSQL(userID uint64, nowMs int64) (string, []any) {
	whereSQL, whereParams := buildUserSharedProfileDetachWhere(userID)
	params := []any{
		logicdomain.ProfileSourceKindRetainedAfterUserDelete,
		userID,
		"source user deleted; shared scope node retained without original turn binding",
		nowMs,
	}
	params = append(params, whereParams...)
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET turn_id = NULL,
    source_kind = ?,
    source_id = ?,
    status_reason = ?,
    updated_timestamp = ?
WHERE %s
`, whereSQL), params
}

// buildUserSharedProfileDetachWhere centralizes the shared-profile predicate so the detach count and update cannot drift apart.
// buildUserSharedProfileDetachWhere 用于集中维护共享画像脱钩条件，避免脱钩计数与实际 UPDATE 范围发生漂移。
func buildUserSharedProfileDetachWhere(userID uint64) (string, []any) {
	return `profile_type <> ? AND turn_id IN (
  SELECT tr.id
  FROM vmm_turn_records tr
  JOIN vmm_sessions s ON s.id = tr.session_id
  WHERE s.user_id = ?
)`, []any{logicdomain.ProfileTypeUser, userID}
}

// buildProjectProfileNodesDeleteSQL renders one raw DELETE that removes every profile node
// actually owned by the project-delete path, including optional empty-parent scopes.
// buildProjectProfileNodesDeleteSQL 用于渲染一条原始 DELETE，
// 删除项目删除路径真正负责的全部画像节点，并在需要时覆盖变空的父级 scope。
func buildProjectProfileNodesDeleteSQL(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	whereSQL, params := buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID, deleteSpace, deleteTeam)
	return fmt.Sprintf("DELETE FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildProjectProfileNodesCountSQL renders one raw COUNT query that matches the same profile-node scope deleted by project removal.
// buildProjectProfileNodesCountSQL 用于渲染一条原始 COUNT 查询，并与项目删除时的画像节点删除范围保持完全一致。
func buildProjectProfileNodesCountSQL(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	whereSQL, params := buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID, deleteSpace, deleteTeam)
	return fmt.Sprintf("SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildProjectProfileNodesDeleteWhere centralizes the delete/count predicate used by project deletion
// so statistics and actual row removal stay locked to the same scope definition.
// buildProjectProfileNodesDeleteWhere 用于集中维护项目删除时的画像节点条件，
// 让删除统计与实际删行始终共享同一套范围定义。
func buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	conditions := []string{
		"(profile_type = ? AND bind_id = ?)",
		"(turn_id IN (SELECT id FROM vmm_turn_records WHERE project_id = ?))",
	}
	params := []any{logicdomain.ProfileTypeProject, projectID, projectID}
	if deleteSpace {
		conditions = append(conditions, "(profile_type = ? AND bind_id = ?)")
		params = append(params, logicdomain.ProfileTypeSpace, spaceID)
	}
	if deleteTeam {
		conditions = append(conditions, "(profile_type = ? AND bind_id = ?)")
		params = append(params, logicdomain.ProfileTypeTeam, teamID)
	}
	return strings.Join(conditions, " OR "), params
}
