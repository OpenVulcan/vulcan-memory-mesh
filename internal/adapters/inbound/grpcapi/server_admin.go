// server_admin.go keeps workspace and profile administration RPC handlers for the inbound gRPC adapter.
// server_admin.go 用于承载入站 gRPC 适配层中的空间管理与画像管理 RPC 处理器。
package grpcapi

import (
	"context"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ListProjects returns the deterministic Team/Space/Project list used by clients to browse available project scopes.
// ListProjects 用于返回客户端浏览可用项目范围时需要的确定性 Team/Space/Project 列表。
func (s *Server) ListProjects(ctx context.Context, _ *emptypb.Empty) (*vmmv1.ListProjectsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	projects, err := s.workspace.ListProjects(ctx)
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	items := make([]*vmmv1.ProjectEntry, 0, len(projects))
	for _, item := range projects {
		items = append(items, toProjectEntry(item))
	}
	return &vmmv1.ListProjectsResponse{Projects: items, TraceId: trace.IDFromContext(ctx)}, nil
}

// ResolveProject resolves one project by numeric id or canonical Team/Space/Project path.
// ResolveProject 用于按数字 ID 或标准 Team/Space/Project 路径解析单个项目。
func (s *Server) ResolveProject(ctx context.Context, req *vmmv1.ResolveProjectRequest) (*vmmv1.ResolveProjectResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeResolveProjectRequest(req)
	if err := s.validator().ValidateResolveProject(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	project, err := s.workspace.ResolveProject(ctx, req.GetProjectRef())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ResolveProjectResponse{
		Project: toProjectEntry(project),
		Message: "project resolved",
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// EnsureProject resolves or creates one canonical Team/Space/Project path according to the confirm-create contract.
// EnsureProject 用于按 confirm_create 规则解析或创建一条标准 Team/Space/Project 路径。
func (s *Server) EnsureProject(ctx context.Context, req *vmmv1.EnsureProjectRequest) (*vmmv1.EnsureProjectResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeEnsureProjectRequest(req)
	if err := s.validator().ValidateEnsureProject(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.workspace.EnsureProject(ctx, req.GetProjectPath(), req.GetConfirmCreate())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.EnsureProjectResponse{
		Project:        toProjectEntry(result.Project),
		Message:        result.Message,
		Exists:         result.Exists,
		NeedsConfirm:   result.NeedsConfirm,
		CreatedTeam:    result.CreatedTeam,
		CreatedSpace:   result.CreatedSpace,
		CreatedProject: result.CreatedProject,
		MissingTeam:    result.MissingTeam,
		MissingSpace:   result.MissingSpace,
		TraceId:        trace.IDFromContext(ctx),
	}, nil
}

// DeleteProject deletes one canonical project path and reports SQL/vector cleanup counts after explicit confirmation.
// DeleteProject 用于删除单个标准项目路径，并在显式确认后返回 SQL/向量清理计数。
func (s *Server) DeleteProject(ctx context.Context, req *vmmv1.DeleteProjectRequest) (*vmmv1.DeleteProjectResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeDeleteProjectRequest(req)
	if err := s.validator().ValidateDeleteProject(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.workspace.DeleteProject(ctx, req.GetProjectPath(), req.GetConfirmDelete())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.DeleteProjectResponse{
		Project:           toProjectEntry(result.Project),
		Message:           result.Message,
		NeedsConfirm:      result.NeedsConfirm,
		DeletedProjects:   int64(result.DeletedProjects),
		DeletedSpaces:     int64(result.DeletedSpaces),
		DeletedTeams:      int64(result.DeletedTeams),
		DeletedSessions:   int64(result.DeletedSessions),
		DeletedMessages:   int64(result.DeletedMessages),
		DeletedMemories:   int64(result.DeletedMemories),
		DeletedProfiles:   int64(result.DeletedProfiles),
		DeletedVectorRows: result.DeletedVectorRows,
		TraceId:           trace.IDFromContext(ctx),
	}, nil
}

// MigrateProject migrates all SQL/vector data from one source project path onto another target project path.
// MigrateProject 用于把某个源项目路径下的全部 SQL/向量数据迁移到目标项目路径。
func (s *Server) MigrateProject(ctx context.Context, req *vmmv1.MigrateProjectRequest) (*vmmv1.MigrateProjectResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeMigrateProjectRequest(req)
	if err := s.validator().ValidateMigrateProject(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.workspace.MigrateProject(ctx, req.GetSourceProjectPath(), req.GetTargetProjectPath(), req.GetConfirmMigrate())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.MigrateProjectResponse{
		SourceProject:     toProjectEntry(result.Source),
		TargetProject:     toProjectEntry(result.Target),
		Message:           result.Message,
		NeedsConfirm:      result.NeedsConfirm,
		MigratedSessions:  int64(result.MigratedSessions),
		MigratedMessages:  int64(result.MigratedMessages),
		MigratedMemories:  int64(result.MigratedMemories),
		RebuiltVectorRows: int64(result.RebuiltVectorRows),
		TraceId:           trace.IDFromContext(ctx),
	}, nil
}

// ResolveUser resolves one user by numeric id or unique name, and can optionally create it when confirm_create is true.
// ResolveUser 用于按数字 ID 或唯一名称解析单个用户，并可在 confirm_create 为真时按需创建。
func (s *Server) ResolveUser(ctx context.Context, req *vmmv1.ResolveUserRequest) (*vmmv1.ResolveUserResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeResolveUserRequest(req)
	if err := s.validator().ValidateResolveUser(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.workspace.ResolveUser(ctx, req.GetUserRef(), req.GetConfirmCreate())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ResolveUserResponse{
		User:    toUserEntry(result.User),
		Message: result.Message,
		Created: result.Created,
		Exists:  result.Exists,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// ListUsers returns the durable user list used by admin clients.
// ListUsers 用于返回管理客户端使用的长期用户列表。
func (s *Server) ListUsers(ctx context.Context, _ *emptypb.Empty) (*vmmv1.ListUsersResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	users, err := s.workspace.ListUsers(ctx)
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	items := make([]*vmmv1.UserEntry, 0, len(users))
	for _, item := range users {
		items = append(items, toUserEntry(item))
	}
	return &vmmv1.ListUsersResponse{Users: items, TraceId: trace.IDFromContext(ctx)}, nil
}

// DeleteUser deletes one user plus all SQL/vector rows after the caller presents the generated confirmation code.
// DeleteUser 用于在调用方提交生成的确认码后，删除单个用户以及其全部 SQL/向量数据。
func (s *Server) DeleteUser(ctx context.Context, req *vmmv1.DeleteUserRequest) (*vmmv1.DeleteUserResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeDeleteUserRequest(req)
	if err := s.validator().ValidateDeleteUser(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.workspace.DeleteUser(ctx, req.GetUserRef(), req.GetConfirmationCode())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.DeleteUserResponse{
		User:                 toUserEntry(result.User),
		Message:              result.Message,
		RequiresConfirmation: result.RequiresConfirmation,
		ConfirmationCode:     result.ConfirmationCode,
		DeletedUsers:         int64(result.DeletedUsers),
		DeletedSessions:      int64(result.DeletedSessions),
		DeletedMessages:      int64(result.DeletedMessages),
		DeletedMemories:      int64(result.DeletedMemories),
		DeletedProfiles:      int64(result.DeletedProfiles),
		DeletedVectorRows:    result.DeletedVectorRows,
		TraceId:              trace.IDFromContext(ctx),
	}, nil
}

// GetProfileNodes resolves one concrete target and returns only its current active atomic profile nodes.
// GetProfileNodes 用于解析一个具体目标，并只返回它当前 active 的原子化画像节点。
func (s *Server) GetProfileNodes(ctx context.Context, req *vmmv1.GetProfileNodesRequest) (*vmmv1.GetProfileNodesResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.profiles == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeGetProfileNodesRequest(req)
	if err := s.validator().ValidateGetProfileNodes(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	targetType, err := fromProtoProfileTarget(req.GetTarget())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.profiles.GetNodes(ctx, usecase.ProfileQueryCommand{
		ProfileType: targetType,
		UserID:      req.GetUserId(),
		ProjectID:   req.GetProjectId(),
		Limit:       int(req.GetLimit()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	nodes := make([]*vmmv1.ProfileNodeEntry, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes = append(nodes, toProfileNodeEntry(node))
	}
	return &vmmv1.GetProfileNodesResponse{
		Nodes:   nodes,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// GetProfileBundle resolves one user/project pair and returns either one combined prompt bundle or split TEAM/SPACE/PROJECT/USER sections.
// GetProfileBundle 用于解析一个 user/project 组合，并返回完整组合提示词或拆分后的 TEAM/SPACE/PROJECT/USER 段落。
func (s *Server) GetProfileBundle(ctx context.Context, req *vmmv1.GetProfileBundleRequest) (*vmmv1.GetProfileBundleResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.profiles == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeGetProfileBundleRequest(req)
	if err := s.validator().ValidateGetProfileBundle(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	mode, err := fromProtoProfileBundleMode(req.GetMode())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.profiles.GetBundle(ctx, usecase.ProfileBundleCommand{
		UserID:             req.GetUserId(),
		ProjectID:          req.GetProjectId(),
		Mode:               mode,
		IncludeExplanation: req.GetIncludeExplanation(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.GetProfileBundleResponse{
		Mode:                    toProtoProfileBundleMode(result.Mode),
		IncludeExplanation:      result.IncludeExplanation,
		ExplanationText:         result.ExplanationText,
		EnvironmentPriorityText: result.EnvironmentPriority,
		CombinedText:            result.CombinedText,
		TeamProfile:             result.TeamProfile,
		SpaceProfile:            result.SpaceProfile,
		ProjectProfile:          result.ProjectProfile,
		UserProfile:             result.UserProfile,
		TraceId:                 trace.IDFromContext(ctx),
	}, nil
}

// ApplyProfileInstruction reviews one explicit manual instruction for one target and persists the resulting node mutations synchronously.
// ApplyProfileInstruction 用于同步评审单个目标上的显式手工画像指令，并持久化得到的节点变更。
func (s *Server) ApplyProfileInstruction(ctx context.Context, req *vmmv1.ApplyProfileInstructionRequest) (*vmmv1.ApplyProfileInstructionResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.profiles == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeApplyProfileInstructionRequest(req)
	if err := s.validator().ValidateApplyProfileInstruction(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	targetType, err := fromProtoProfileTarget(req.GetTarget())
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()
	result, err := s.profiles.ApplyInstruction(ctx, usecase.ProfileInstructionCommand{
		ProfileType: targetType,
		UserID:      req.GetUserId(),
		ProjectID:   req.GetProjectId(),
		Instruction: req.GetInstruction(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	acceptedNodes := make([]*vmmv1.ProfileNodeEntry, 0, len(result.AcceptedNodes))
	for _, node := range result.AcceptedNodes {
		acceptedNodes = append(acceptedNodes, toProfileNodeEntry(node))
	}
	retiredNodes := make([]*vmmv1.RetiredProfileNodeEntry, 0, len(result.RetiredNodes))
	for _, decision := range result.RetiredNodes {
		retiredNodes = append(retiredNodes, &vmmv1.RetiredProfileNodeEntry{
			ProfileNodeId: decision.NodeID,
			Reason:        decision.Reason,
		})
	}
	return &vmmv1.ApplyProfileInstructionResponse{
		InstructionId: result.InstructionID,
		AcceptedNodes: acceptedNodes,
		RetiredNodes:  retiredNodes,
		ReviewReason:  result.ReviewReason,
		TraceId:       trace.IDFromContext(ctx),
	}, nil
}
