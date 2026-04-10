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

// workspaceStore returns the configured workspace store and fails fast with one stable error when direct tests or manual integrations call the exported admin use case through a nil or partially constructed receiver.
// workspaceStore 用于返回当前配置的 workspace store；当直接测试或手工集成通过 nil 或部分装配的接收者调用导出管理用例时，会快速返回稳定错误。
func (u *WorkspaceUseCase) workspaceStore() (appports.WorkspaceStore, error) {
	if u == nil || u.store == nil {
		return nil, fmt.Errorf("workspace store is nil")
	}
	return u.store, nil
}

// ListProjects returns all canonical project nodes ordered by their Team/Space/Project display path.
// ListProjects 用于返回按 Team/Space/Project 展示路径排序的全部项目节点。
func (u *WorkspaceUseCase) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return nil, err
	}
	return store.ListProjects(ctx)
}

// ResolveProject resolves one project from either numeric id or canonical Team/Space/Project path.
// ResolveProject 用于按数字 ID 或标准 Team/Space/Project 路径解析单个项目。
func (u *WorkspaceUseCase) ResolveProject(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.ProjectRecord{}, err
	}
	return store.ResolveProjectRef(ctx, projectRef)
}

// EnsureProject resolves or creates one canonical project path according to the confirm-create contract.
// EnsureProject 用于按确认创建规则解析或创建标准项目路径。
func (u *WorkspaceUseCase) EnsureProject(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	return store.EnsureProjectPath(ctx, projectPath, confirmCreate)
}

// DeleteProject removes one project from the relational store and then clears all vector rows under the same flattened hierarchy filter.
// DeleteProject 用于先清理同一扁平层级范围下的全部向量行，再从关系库存储删除单个项目，避免向量删除失败导致数据不一致。
func (u *WorkspaceUseCase) DeleteProject(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	// Delete vectors first; if it fails, relational data remains intact and the operation can be retried.
	// 先删除向量：如果失败，关系数据保持完整，操作可重试。
	if u.vector != nil {
		// Resolve project to get vector filter fields before deleting relational data.
		// 在删除关系数据之前解析项目以获取向量过滤字段。
		project, err := store.ResolveProjectRef(ctx, projectPath)
		if err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("resolve project for vector cleanup: %w", err)
		}
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{
			TeamID:    project.TeamID,
			SpaceID:   project.SpaceID,
			ProjectID: project.ID,
		})
		if err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project vectors before relational delete: %w", err)
		}
		result, err := store.DeleteProjectPath(ctx, projectPath, confirmDelete)
		if err != nil || result.NeedsConfirm {
			return result, err
		}
		result.DeletedVectorRows = deletedRows
		return result, nil
	}
	return store.DeleteProjectPath(ctx, projectPath, confirmDelete)
}

// MigrateProject moves SQL rows first, then rebuilds target project vectors from the durable SQL-side memory entries.
// MigrateProject 用于先迁移 SQL 数据，再根据长期 SQL 侧记忆条目重建目标项目的向量数据。
func (u *WorkspaceUseCase) MigrateProject(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	result, err := store.MigrateProjectPath(ctx, sourcePath, targetPath, confirm)
	if err != nil || result.NeedsConfirm {
		return result, err
	}
	if u.vector == nil {
		return result, nil
	}
	// Rebuild target project vectors from source project memories first, then delete source vectors after confirmation.
	// 先从源项目记忆重建目标项目向量，确认全部成功后再删除源向量，避免中途失败导致数据丢失。
	memories, err := store.ListProjectMemories(ctx, result.Target.ID)
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
	// All target vectors rebuilt successfully; safe to delete source vectors now.
	// 目标向量全部重建成功，现在可以安全删除源向量。
	if _, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{
		TeamID:    result.Source.TeamID,
		SpaceID:   result.Source.SpaceID,
		ProjectID: result.Source.ID,
	}); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("delete source project vectors after migration: %w", err)
	}
	return result, nil
}

// ResolveUser resolves one user by numeric id or unique name, and optionally creates the user when confirmCreate is true.
// ResolveUser 用于按数字 ID 或唯一名称解析用户，并在 confirmCreate 为真时按需创建该用户。
func (u *WorkspaceUseCase) ResolveUser(ctx context.Context, userRef string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.UserResolveResult{}, err
	}
	if confirmCreate {
		return store.EnsureUserName(ctx, userRef, true)
	}
	user, err := store.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserResolveResult{}, err
	}
	return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s resolved", user.Name), Exists: true}, nil
}

// ListUsers returns all users ordered by id for deterministic admin output.
// ListUsers 用于按 ID 稳定返回所有用户。
func (u *WorkspaceUseCase) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return nil, err
	}
	return store.ListUsers(ctx)
}

// DeleteUser removes one user from the relational store and then clears all vector rows associated with that user.
// DeleteUser 用于先清理与用户关联的全部向量行，再从关系库存储删除用户，避免向量删除失败导致数据不一致。
func (u *WorkspaceUseCase) DeleteUser(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	// Delete vectors first; if it fails, relational data remains intact and the operation can be retried.
	// 先删除向量：如果失败，关系数据保持完整，操作可重试。
	if u.vector != nil {
		// Resolve user to get vector filter fields before deleting relational data.
		// 在删除关系数据之前解析用户以获取向量过滤字段。
		user, err := store.ResolveUserRef(ctx, userRef)
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("resolve user for vector cleanup: %w", err)
		}
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{UserID: user.ID})
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user vectors before relational delete: %w", err)
		}
		result, err := store.DeleteUserRef(ctx, userRef, confirmationCode)
		if err != nil || result.RequiresConfirmation {
			return result, err
		}
		result.DeletedVectorRows = deletedRows
		return result, nil
	}
	return store.DeleteUserRef(ctx, userRef, confirmationCode)
}
