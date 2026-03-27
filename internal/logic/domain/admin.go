// admin.go declares hierarchy-management models shared by admin RPCs and request-scope resolution.
// admin.go 用于声明管理 RPC 和请求范围解析共用的层级管理模型。
package domain

// ProjectMutationResult describes the outcome of resolving or creating one Team/Space/Project path.
// ProjectMutationResult 用于描述解析或创建单个 Team/Space/Project 路径后的结果。
type ProjectMutationResult struct {
	Project        ProjectRecord
	Message        string
	Exists         bool
	NeedsConfirm   bool
	CreatedTeam    bool
	CreatedSpace   bool
	CreatedProject bool
	MissingTeam    bool
	MissingSpace   bool
}

// ProjectDeleteResult describes the outcome of deleting one concrete project scope and its stored data.
// ProjectDeleteResult 用于描述删除某个具体项目范围及其存储数据后的结果。
type ProjectDeleteResult struct {
	Project           ProjectRecord
	Message           string
	NeedsConfirm      bool
	DeletedSessions   int
	DeletedMessages   int
	DeletedMemories   int
	DeletedVectorRows uint64
}

// ProjectMigrationResult describes the outcome of moving one source project scope onto a target project scope.
// ProjectMigrationResult 用于描述把源项目范围迁移到目标项目范围后的结果。
type ProjectMigrationResult struct {
	Source            ProjectRecord
	Target            ProjectRecord
	Message           string
	NeedsConfirm      bool
	MigratedSessions  int
	MigratedMessages  int
	MigratedMemories  int
	RebuiltVectorRows int
}

// UserResolveResult describes the outcome of resolving or creating one user by name.
// UserResolveResult 用于描述按名称解析或创建用户后的结果。
type UserResolveResult struct {
	User    UserRecord
	Message string
	Created bool
	Exists  bool
}

// UserDeleteResult describes the two-phase protected deletion flow for one user.
// UserDeleteResult 用于描述单个用户的双阶段保护删除流程。
type UserDeleteResult struct {
	User                 UserRecord
	Message              string
	RequiresConfirmation bool
	ConfirmationCode     string
	DeletedSessions      int
	DeletedMessages      int
	DeletedMemories      int
	DeletedVectorRows    uint64
}
