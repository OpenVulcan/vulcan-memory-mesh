// workspace.go implements hierarchy and user management use cases used by the admin gRPC surface.
// workspace.go 用于实现管理 gRPC 面使用的层级与用户管理用例。
package usecase

import (
	"context"
	"fmt"
	"strings"

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

// workspaceOutcomeUncertainError converts one post-commit or post-cleanup failure into the shared outcome-uncertain contract so callers know side effects may already have partially happened.
// workspaceOutcomeUncertainError 用于把一次提交后或清理后的失败转换成统一的结果不确定契约，让调用方明确知道副作用可能已经部分发生。
func workspaceOutcomeUncertainError(operation, message string, cause error) error {
	if logicdomain.IsOutcomeUncertain(cause) {
		return cause
	}
	message = strings.TrimSpace(message)
	if cause != nil {
		causeMessage := strings.TrimSpace(cause.Error())
		if causeMessage != "" {
			if message == "" {
				message = causeMessage
			} else {
				message = message + ": " + causeMessage
			}
		}
	}
	return logicdomain.OutcomeUncertainError{
		Operation: operation,
		Message:   message,
	}
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

// DeleteProject blocks the protected bootstrap project, preserves the confirmation-first contract, and only then clears matching vector rows before the relational delete commits.
// DeleteProject 用于拦截受保护的启动默认项目，保持“先确认再删除”的契约，然后才在关系删除提交前清理匹配的向量行。
func (u *WorkspaceUseCase) DeleteProject(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}

	// Resolve the target once so protected default scopes are rejected before either confirmation branching
	// or destructive vector cleanup can start.
	// 先解析目标项目，确保受保护的默认范围会在确认分支和向量清理之前就被拒绝。
	project, err := store.ResolveProjectRef(ctx, projectPath)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if logicdomain.IsProtectedDefaultProject(project) {
		return logicdomain.ProjectDeleteResult{}, logicdomain.ProtectedResourceError{
			Resource: "project",
			Message:  fmt.Sprintf("default project %s cannot be deleted", project.Path()),
		}
	}

	// Preserve the public confirmation contract so callers asking for the first confirmation step do not
	// accidentally lose vectors before the relational delete is even approved.
	// 保持对外确认契约，让仍处于第一次确认阶段的调用不会在关系删除获准前误删向量。
	if !confirmDelete {
		return store.DeleteProjectPath(ctx, projectPath, false)
	}

	// Delete vectors first only after the protected-resource and confirmation gates pass; this keeps
	// relational rows intact when the vector backend fails during the actual destructive phase.
	// 仅在受保护资源校验和确认门禁都通过后才先删向量，这样真正破坏性阶段若向量后端失败，关系行仍保持完整。
	if u.vector != nil {
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{
			TeamID:    project.TeamID,
			SpaceID:   project.SpaceID,
			ProjectID: project.ID,
		})
		if err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project vectors before relational delete: %w", err)
		}
		result, err := store.DeleteProjectPath(ctx, projectPath, true)
		result.DeletedVectorRows = deletedRows
		if err != nil {
			return result, workspaceOutcomeUncertainError("delete project", "vector cleanup already finished before relational delete completed", err)
		}
		if result.NeedsConfirm {
			return result, workspaceOutcomeUncertainError("delete project", "vector cleanup already finished before relational delete completed and the store unexpectedly requested confirmation again", nil)
		}
		return result, nil
	}
	return store.DeleteProjectPath(ctx, projectPath, true)
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
		return result, workspaceOutcomeUncertainError("migrate project", "relational project migration already committed before target memory enumeration completed", err)
	}
	for _, memory := range memories {
		if len(memory.Vector) == 0 {
			continue
		}
		if err := u.vector.Upsert(ctx, memory); err != nil {
			return result, workspaceOutcomeUncertainError("migrate project", fmt.Sprintf("relational project migration already committed before rebuilding target vector %s completed", memory.ID), err)
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
		return result, workspaceOutcomeUncertainError("migrate project", "relational project migration already committed before source vector cleanup completed", err)
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

// DeleteUser blocks the protected bootstrap user, preserves the confirmation-code handshake, and only then clears that user's vector rows before the relational delete commits.
// DeleteUser 用于拦截受保护的启动默认用户，保持确认码握手契约，然后才在关系删除提交前清理该用户的向量行。
func (u *WorkspaceUseCase) DeleteUser(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	store, err := u.workspaceStore()
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}

	// Resolve the user up front so protected default users and stale confirmation requests are stopped
	// before any vector-side destructive work begins.
	// 先解析用户，确保受保护的默认用户和陈旧确认请求都会在任何向量侧破坏性操作开始前被拦下。
	user, err := store.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if logicdomain.IsProtectedDefaultUser(user) {
		return logicdomain.UserDeleteResult{}, logicdomain.ProtectedResourceError{
			Resource: "user",
			Message:  fmt.Sprintf("default user %s cannot be deleted", user.Name),
		}
	}
	trimmedConfirmationCode := strings.TrimSpace(confirmationCode)

	// Keep the confirmation handshake non-destructive until the caller presents the latest durable code.
	// This prevents the first-step confirmation fetch and obvious stale-code retries from deleting vectors early.
	// 在调用方提交最新持久确认码之前，保持确认握手阶段不具破坏性。
	// 这样第一次获取确认码以及明显的陈旧确认码重试都不会提前删掉向量。
	if trimmedConfirmationCode == "" || trimmedConfirmationCode != strings.TrimSpace(user.DeleteConfirmCode) {
		return store.DeleteUserRef(ctx, userRef, trimmedConfirmationCode)
	}

	// Delete vectors first only after the protected-resource and confirmation gates pass; this keeps
	// relational rows intact when the vector backend fails during the actual destructive phase.
	// 仅在受保护资源校验和确认门禁都通过后才先删向量，这样真正破坏性阶段若向量后端失败，关系行仍保持完整。
	if u.vector != nil {
		deletedRows, err := u.vector.DeleteByFilter(ctx, logicdomain.SearchFilter{UserID: user.ID})
		if err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user vectors before relational delete: %w", err)
		}
		result, err := store.DeleteUserRef(ctx, userRef, trimmedConfirmationCode)
		result.DeletedVectorRows = deletedRows
		if err != nil {
			return result, workspaceOutcomeUncertainError("delete user", "vector cleanup already finished before relational delete completed", err)
		}
		if result.RequiresConfirmation {
			return result, workspaceOutcomeUncertainError("delete user", "vector cleanup already finished before relational delete completed and the store unexpectedly requested confirmation again", nil)
		}
		return result, nil
	}
	return store.DeleteUserRef(ctx, userRef, trimmedConfirmationCode)
}
