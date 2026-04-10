// ports_workspace.go keeps workspace hierarchy and schema coordination contracts used by admin flows and the composition root.
// ports_workspace.go 用于承载管理流与组合根依赖的 workspace 层级管理和 schema 协调契约。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// SchemaVersionStore is the narrow infrastructure port used by the composition root to coordinate SQL/vector schema version upgrades without coupling business use cases to migration details.
// SchemaVersionStore 用于给组合根提供一个狭窄的基础设施端口，协调 SQL/向量 schema 版本升级，同时避免业务用例耦合迁移细节。
type SchemaVersionStore interface {
	GetSchemaComponentVersion(ctx context.Context, component string) (int, error)
	SetSchemaComponentVersion(ctx context.Context, component string, version int) error
}

// WorkspaceStore is the port used by admin RPCs to manage users plus Team/Space/Project hierarchy nodes.
// WorkspaceStore 用于让管理 RPC 管理用户和 Team/Space/Project 层级节点。
type WorkspaceStore interface {
	ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error)
	ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error)
	ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error)
	EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error)
	DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error)
	MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error)
	ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error)
	EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error)
	ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error)
	DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error)
}
