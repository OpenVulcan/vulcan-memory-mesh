// admin.go declares hierarchy-management models shared by admin RPCs and request-scope resolution.
// admin.go 用于声明管理 RPC 和请求范围解析共用的层级管理模型。
package domain

const (
	// DefaultWorkspaceResourceName keeps the deterministic name shared by the bootstrap user/team/space/project rows that local runtimes expose as the initial default scope.
	// DefaultWorkspaceResourceName 用于固定本地运行时暴露的初始默认范围启动 user/team/space/project 行共享名称。
	DefaultWorkspaceResourceName = "default"
)

// DefaultProjectPath returns the canonical Team/Space/Project path of the protected bootstrap project scope.
// DefaultProjectPath 用于返回受保护启动项目范围的标准 Team/Space/Project 路径。
func DefaultProjectPath() string {
	return DefaultWorkspaceResourceName + "/" + DefaultWorkspaceResourceName + "/" + DefaultWorkspaceResourceName
}

// IsProtectedDefaultProject reports whether one resolved project record points at the bootstrap default project that admin delete flows must preserve.
// IsProtectedDefaultProject 用于判断某个已解析项目记录是否指向启动默认项目，该项目必须在管理删除流程中被保留。
func IsProtectedDefaultProject(project ProjectRecord) bool {
	return project.TeamName == DefaultWorkspaceResourceName &&
		project.SpaceName == DefaultWorkspaceResourceName &&
		project.Name == DefaultWorkspaceResourceName
}

// IsProtectedDefaultUser reports whether one resolved user record points at the bootstrap default user that admin delete flows must preserve.
// IsProtectedDefaultUser 用于判断某个已解析用户记录是否指向启动默认用户，该用户必须在管理删除流程中被保留。
func IsProtectedDefaultUser(user UserRecord) bool {
	return user.Name == DefaultWorkspaceResourceName
}

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
	DeletedProjects   int
	DeletedSpaces     int
	DeletedTeams      int
	DeletedSessions   int
	DeletedMessages   int
	DeletedMemories   int
	DeletedProfiles   int
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
	DeletedUsers         int
	DeletedSessions      int
	DeletedMessages      int
	DeletedMemories      int
	DeletedProfiles      int
	DeletedVectorRows    uint64
}
