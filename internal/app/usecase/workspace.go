// workspace.go implements hierarchy and user management use cases used by the admin gRPC surface.
// workspace.go 用于实现管理 gRPC 面使用的层级与用户管理用例。
package usecase

import (
	"context"
	"fmt"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// WorkspaceExecutor groups the hierarchy and user management actions exposed by admin gRPC methods.
// WorkspaceExecutor 用于聚合管理 gRPC 方法对外暴露的层级和用户管理动作。
type WorkspaceExecutor interface {
	ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error)
	ResolveProject(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error)
	EnsureProject(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error)
	DeleteProject(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error)
	MigrateProject(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error)
	ResolveUser(ctx context.Context, userRef string, confirmCreate bool) (logicdomain.UserResolveResult, error)
	ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error)
	DeleteUser(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error)
}

// WorkspaceUseCase orchestrates hierarchy mutations on the relational store and vector cleanup/rebuild work on LanceDB.
// WorkspaceUseCase 用于编排关系库存储上的层级变更，以及 LanceDB 上的向量清理和重建工作。
type WorkspaceUseCase struct {
	store  appports.WorkspaceStore
	vector appports.VectorStore
}

// NewWorkspaceUseCase creates a WorkspaceUseCase instance.
// NewWorkspaceUseCase 用于创建 WorkspaceUseCase 实例。
func NewWorkspaceUseCase(store appports.WorkspaceStore, vector appports.VectorStore) *WorkspaceUseCase {
	return &WorkspaceUseCase{store: store, vector: vector}
}

// ListProjects returns all canonical project nodes ordered by their Team/Space/Project display path.
// ListProjects 用于返回按 Team/Space/Project 展示路径排序的全部项目节点。
func (u *WorkspaceUseCase) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return u.store.ListProjects(ctx)
}

// ResolveProject resolves one project from either numeric id or canonical Team/Space/Project path.
// ResolveProject 用于按数字 ID 或标准 Team/Space/Project 路径解析单个项目。
func (u *WorkspaceUseCase) ResolveProject(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	return u.store.ResolveProjectRef(ctx, projectRef)
}

// EnsureProject resolves or creates one canonical project path according to the confirm-create contract.
// EnsureProject 用于按确认创建规则解析或创建标准项目路径。
func (u *WorkspaceUseCase) EnsureProject(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	return u.store.EnsureProjectPath(ctx, projectPath, confirmCreate)
}

// DeleteProject removes one project from the relational store and then clears all vector rows under the same flattened hierarchy filter.
// DeleteProject 用于先从关系库存储删除单个项目，再清理同一扁平层级范围下的全部向量行。
func (u *WorkspaceUseCase) DeleteProject(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	result, err := u.store.DeleteProjectPath(ctx, projectPath, confirmDelete)
	if err != nil || result.NeedsConfirm {
		return result, err
	}
	if u.vector != nil {
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{
			TeamID:    result.Project.TeamID,
			SpaceID:   result.Project.SpaceID,
			ProjectID: result.Project.ID,
		})
		if err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project vectors: %w", err)
		}
		result.DeletedVectorRows = deletedRows
	}
	return result, nil
}

// MigrateProject moves SQL rows first, then rebuilds target project vectors from the durable SQL-side memory entries.
// MigrateProject 用于先迁移 SQL 数据，再根据长期 SQL 侧记忆条目重建目标项目的向量数据。
func (u *WorkspaceUseCase) MigrateProject(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	result, err := u.store.MigrateProjectPath(ctx, sourcePath, targetPath, confirm)
	if err != nil || result.NeedsConfirm {
		return result, err
	}
	if u.vector == nil {
		return result, nil
	}
	if _, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{
		TeamID:    result.Source.TeamID,
		SpaceID:   result.Source.SpaceID,
		ProjectID: result.Source.ID,
	}); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("delete source project vectors: %w", err)
	}
	memories, err := u.store.ListProjectMemories(ctx, result.Target.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("list target project memories: %w", err)
	}
	for _, memory := range memories {
		if len(memory.Vector) == 0 {
			continue
		}
		if err := u.vector.Upsert(ctx, memory); err != nil {
			return logicdomain.ProjectMigrationResult{}, fmt.Errorf("rebuild target project vector %s: %w", memory.ID, err)
		}
		result.RebuiltVectorRows++
	}
	return result, nil
}

// ResolveUser resolves one user by numeric id or unique name, and optionally creates the user when confirmCreate is true.
// ResolveUser 用于按数字 ID 或唯一名称解析用户，并在 confirmCreate 为真时按需创建该用户。
func (u *WorkspaceUseCase) ResolveUser(ctx context.Context, userRef string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	if confirmCreate {
		return u.store.EnsureUserName(ctx, userRef, true)
	}
	user, err := u.store.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserResolveResult{}, err
	}
	return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s resolved", user.Name), Exists: true}, nil
}

// ListUsers returns all users ordered by id for deterministic admin output.
// ListUsers 用于按 ID 稳定返回所有用户。
func (u *WorkspaceUseCase) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	return u.store.ListUsers(ctx)
}

// DeleteUser removes one user from the relational store and then clears all vector rows associated with that user.
// DeleteUser 用于先从关系库存储删除用户，再清理与该用户关联的全部向量行。
func (u *WorkspaceUseCase) DeleteUser(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	result, err := u.store.DeleteUserRef(ctx, userRef, confirmationCode)
	if err != nil || result.RequiresConfirmation {
		return result, err
	}
	if u.vector != nil {
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{UserID: result.User.ID})
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user vectors: %w", err)
		}
		result.DeletedVectorRows = deletedRows
	}
	return result, nil
}
