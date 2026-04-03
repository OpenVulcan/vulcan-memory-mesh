// server.go implements the inbound gRPC adapter that exposes workspace administration plus the main PreCheck/PostAction business chain.
// server.go 用于实现入站 gRPC 适配层，对外暴露空间管理接口以及主业务链路 PreCheck/PostAction。
package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Dependencies collects the gRPC-facing use cases, scope resolver, and timeout settings used by the inbound adapter.
// Dependencies 用于收集入站 gRPC 适配层需要的用例、范围解析器和超时配置。
type Dependencies struct {
	IDs               appports.IDGenerator
	Workspace         usecase.WorkspaceExecutor
	Profiles          usecase.ProfileExecutor
	Memory            usecase.MemoryExecutor
	PreCheck          usecase.PreCheckExecutor
	PostAction        usecase.PostActionExecutor
	ScopeResolver     appports.RequestScopeResolver
	Logger            *logx.Logger
	Validator         *RequestValidator
	DebugRPCPayloads  bool
	WorkspaceTimeout  time.Duration
	PreCheckTimeout   time.Duration
	PostActionTimeout time.Duration
	ExtraInterceptors []grpc.UnaryServerInterceptor
}

// Server groups gRPC-facing use cases, validation, sanitization, and timeout settings for the inbound adapter.
// Server 用于聚合入站 gRPC 适配层所需的用例、校验器、清洗器和超时配置。
type Server struct {
	vmmv1.UnimplementedVMMServiceServer

	workspace        usecase.WorkspaceExecutor
	profiles         usecase.ProfileExecutor
	memory           usecase.MemoryExecutor
	preCheck         usecase.PreCheckExecutor
	postAction       usecase.PostActionExecutor
	workspaceTimeout time.Duration
	preTimeout       time.Duration
	postTimeout      time.Duration
	logger           *logx.Logger
	validate         *RequestValidator
	sanitizer        *textutil.PostActionTextSanitizer
	debugRPCPayloads bool
}

// NewServer creates one gRPC server implementation from the supplied dependencies.
// NewServer 用于根据输入依赖创建一个 gRPC 服务实现。
func NewServer(deps Dependencies) *Server {
	if deps.Logger == nil {
		deps.Logger = logx.Default()
	}
	if deps.Validator == nil {
		deps.Validator = NewRequestValidator()
	}
	return &Server{
		workspace:        deps.Workspace,
		profiles:         deps.Profiles,
		memory:           deps.Memory,
		preCheck:         deps.PreCheck,
		postAction:       deps.PostAction,
		workspaceTimeout: deps.WorkspaceTimeout,
		preTimeout:       deps.PreCheckTimeout,
		postTimeout:      deps.PostActionTimeout,
		logger:           deps.Logger,
		validate:         deps.Validator,
		sanitizer:        textutil.NewPostActionTextSanitizer(),
		debugRPCPayloads: deps.DebugRPCPayloads,
	}
}

// validator returns the configured request validator and falls back to the default transport validator when direct tests or manual integrations build one partial Server without calling NewServer.
// validator 用于返回当前配置的请求校验器；当直接测试或手工集成绕过 NewServer 构造了一个部分装配的 Server 时，会回退到默认传输层校验器。
func (s *Server) validator() *RequestValidator {
	if s == nil || s.validate == nil {
		return NewRequestValidator()
	}
	return s.validate
}

// postActionSanitizer returns the configured post-action sanitizer and falls back to the default storage-oriented sanitizer when direct tests or manual integrations build one partial Server without calling NewServer.
// postActionSanitizer 用于返回当前配置的 post-action 清洗器；当直接测试或手工集成绕过 NewServer 构造了一个部分装配的 Server 时，会回退到默认的存储型清洗器。
func (s *Server) postActionSanitizer() *textutil.PostActionTextSanitizer {
	if s == nil || s.sanitizer == nil {
		return textutil.NewPostActionTextSanitizer()
	}
	return s.sanitizer
}

// BuildUnaryInterceptors returns the standard unary interceptor chain used by the gRPC server.
// BuildUnaryInterceptors 用于返回 gRPC 服务使用的标准一元拦截器链。
func BuildUnaryInterceptors(deps Dependencies) []grpc.UnaryServerInterceptor {
	interceptors := []grpc.UnaryServerInterceptor{
		RecoveryInterceptor(deps.Logger),
		TraceIDInterceptor(deps.IDs),
	}
	if deps.ScopeResolver != nil {
		interceptors = append(interceptors, ScopeResolutionInterceptor(deps.ScopeResolver, deps.Logger))
	}
	interceptors = append(interceptors, RequestLoggerInterceptor(deps.Logger))
	interceptors = appendNonNilUnaryInterceptors(interceptors, deps.ExtraInterceptors...)
	return interceptors
}

// appendNonNilUnaryInterceptors keeps the transport chain deterministic by dropping nil interceptor slots before gRPC chains them into one executable pipeline.
// appendNonNilUnaryInterceptors 用于在 gRPC 把拦截器链接成可执行流水线之前，先丢弃 nil 槽位，保证传输链路保持确定性。
func appendNonNilUnaryInterceptors(base []grpc.UnaryServerInterceptor, extras ...grpc.UnaryServerInterceptor) []grpc.UnaryServerInterceptor {
	for _, interceptor := range extras {
		if interceptor == nil {
			continue
		}
		base = append(base, interceptor)
	}
	return base
}

// requireReceiver ensures direct tests and manual integrations get one stable internal error instead of a panic when they accidentally invoke exported RPC methods on a nil Server receiver.
// requireReceiver 用于保证直接测试和手工集成在误把导出 RPC 方法调用到 nil Server 接收者上时，拿到稳定的内部错误，而不是直接 panic。
func (s *Server) requireReceiver() error {
	if s == nil {
		return toStatus(withMessage(errInternal, "grpc server is not initialized"))
	}
	return nil
}

// Healthz reports a healthy status for the local runtime.
// Healthz 用于报告本地运行时的健康状态。
func (s *Server) Healthz(ctx context.Context, _ *emptypb.Empty) (*vmmv1.HealthzResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	return &vmmv1.HealthzResponse{Status: "ok", TraceId: trace.IDFromContext(ctx)}, nil
}

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

// SearchMemoryEvents parses one grouped JSON payload, embeds each query item, searches vector memories inside the resolved scope, and returns unified memory refs plus optional source-turn refs.
// SearchMemoryEvents 用于解析分组 JSON 载荷、对每条查询做 embedding、在已解析范围内搜索向量记忆，并返回统一 memory ref 以及可选来源 turn ref。
func (s *Server) SearchMemoryEvents(ctx context.Context, req *vmmv1.SearchMemoryEventsRequest) (*vmmv1.SearchMemoryEventsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeSearchMemoryEventsRequest(req)
	if err := s.validator().ValidateSearchMemoryEvents(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.memory.Search(ctx, usecase.MemoryQueryCommand{
		UserID:    req.GetUserId(),
		ProjectID: req.GetProjectId(),
		QueryJSON: req.GetQueryJson(),
		TopK:      int(req.GetTopK()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	groups := make([]*vmmv1.MemorySearchGroupResult, 0, len(result.Results))
	for _, group := range result.Results {
		hits := make([]*vmmv1.MemorySearchHit, 0, len(group.Hits))
		for _, hit := range group.Hits {
			hits = append(hits, &vmmv1.MemorySearchHit{
				MemoryRef:      toProtoMemoryRef(hit.MemoryRef),
				SourceRef:      toProtoMemoryRef(hit.SourceRef),
				SourceKind:     toProtoMemorySourceKind(hit.SourceKind),
				ScopeLevel:     toProtoMemoryScopeLevel(hit.ScopeLevel),
				SessionId:      hit.SessionID,
				Abstract:       hit.Abstract,
				DetailsPreview: hit.DetailsPreview,
				Category:       int32(hit.Category),
				Score:          hit.Score,
			})
		}
		groups = append(groups, &vmmv1.MemorySearchGroupResult{
			QueryIndex: uint32(group.QueryIndex),
			Background: group.Background,
			Query:      group.Query,
			Hits:       hits,
		})
	}
	return &vmmv1.SearchMemoryEventsResponse{
		Results: groups,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// GetTurnDetails loads one or more dehydrated turn rows by turn id and expands them with parsed dialogue fields plus nearby turn ids.
// GetTurnDetails 用于按 turn id 读取一条或多条脱水 turn 行，并补充解析后的对话字段和相邻 turn 编号。
func (s *Server) GetTurnDetails(ctx context.Context, req *vmmv1.GetTurnDetailsRequest) (*vmmv1.GetTurnDetailsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeGetTurnDetailsRequest(req)
	if err := s.validator().ValidateGetTurnDetails(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.memory.GetTurns(ctx, usecase.TurnDetailCommand{TurnIDs: req.GetTurnIds()})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	turns := make([]*vmmv1.TurnDetailEntry, 0, len(result.Turns))
	for _, turn := range result.Turns {
		turns = append(turns, toTurnDetailEntry(turn))
	}
	return &vmmv1.GetTurnDetailsResponse{
		Turns:   turns,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// GetMemoryDetails loads one ordered `TYPE + ID` ref list and returns mixed unified memory or turn details.
// GetMemoryDetails 用于按顺序读取一组 `TYPE + ID` 引用，并返回混合的统一记忆详情或 turn 详情。
func (s *Server) GetMemoryDetails(ctx context.Context, req *vmmv1.GetMemoryDetailsRequest) (*vmmv1.GetMemoryDetailsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeGetMemoryDetailsRequest(req)
	if err := s.validator().ValidateGetMemoryDetails(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()

	// Convert the transport `TYPE + ID` references into domain refs so the use case can batch load memories and turns separately.
	// 先把传输层 `TYPE + ID` 引用转换成领域层 ref，让用例可以分别批量读取 memory 和 turn。
	refs := make([]logicdomain.MemoryRef, 0, len(req.GetRefs()))
	for _, ref := range req.GetRefs() {
		refs = append(refs, fromProtoMemoryRef(ref))
	}
	result, err := s.memory.GetDetails(ctx, usecase.MemoryDetailCommand{Refs: refs})
	if err != nil {
		return nil, toStatus(describeError(err))
	}

	items := make([]*vmmv1.MemoryDetailItem, 0, len(result.Items))
	for _, item := range result.Items {
		entry := &vmmv1.MemoryDetailItem{Ref: toProtoMemoryRef(item.Ref)}
		if item.Memory != nil {
			entry.Payload = &vmmv1.MemoryDetailItem_Memory{Memory: toMemoryDetailEntry(*item.Memory)}
		}
		if item.Turn != nil {
			entry.Payload = &vmmv1.MemoryDetailItem_Turn{Turn: toTurnDetailEntry(*item.Turn)}
		}
		items = append(items, entry)
	}
	return &vmmv1.GetMemoryDetailsResponse{
		Items:   items,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// WriteMemories persists one batch of direct AI-written memory items inside the resolved session scope and returns unified refs.
// WriteMemories 用于在已解析 session 范围内持久化一批 AI 主动写入的记忆项，并返回统一引用。
func (s *Server) WriteMemories(ctx context.Context, req *vmmv1.WriteMemoriesRequest) (*vmmv1.WriteMemoriesResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeWriteMemoriesRequest(req)
	if err := s.validator().ValidateWriteMemories(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()

	// Keep the transport layer focused on enum and timestamp conversion so write defaults still live in the use case.
	// 让传输层只负责枚举和时间戳转换，写入默认值仍由用例层统一决定。
	items := make([]usecase.WriteMemoryItem, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		items = append(items, usecase.WriteMemoryItem{
			ScopeLevel:  fromProtoMemoryScopeLevel(item.GetScopeLevel()),
			Abstract:    item.GetAbstract(),
			Details:     item.GetDetails(),
			Category:    int(item.GetCategory()),
			Priority:    fromProtoMemoryPriority(item.GetPriority()),
			MemoryLevel: fromProtoMemoryLevel(item.GetMemoryLevel()),
			ExpiresAt:   fromUnixMillis(item.GetExpiresTimestamp()),
		})
	}
	result, err := s.memory.Write(ctx, usecase.WriteMemoriesCommand{
		Session: session,
		Items:   items,
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}

	entries := make([]*vmmv1.WriteMemoryResultItem, 0, len(result.Items))
	for _, item := range result.Items {
		entries = append(entries, &vmmv1.WriteMemoryResultItem{
			MemoryRef:  toProtoMemoryRef(item.Ref),
			SourceKind: toProtoMemorySourceKind(item.SourceKind),
			ScopeLevel: toProtoMemoryScopeLevel(item.ScopeLevel),
			Deduped:    item.Deduped,
		})
	}
	return &vmmv1.WriteMemoriesResponse{
		Items:   entries,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// PreCheck validates the request, consumes the scope resolved by the interceptor, and returns the live assembled pre-check context.
// PreCheck 用于校验请求、消费拦截器解析出的范围，并返回实时组装完成的 pre-check 上下文。
func (s *Server) PreCheck(ctx context.Context, req *vmmv1.PreCheckRequest) (*vmmv1.PreCheckResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.preCheck == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePreCheckRequest(req)
	if err := s.validator().ValidatePreCheck(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.preTimeout)
	defer cancel()
	s.logPreCheckReceipt(trace.IDFromContext(ctx), "pre-check received", req)
	result, err := s.preCheck.Execute(ctx, usecase.PreCheckCommand{
		Session:     session,
		UserContent: req.GetUserContent(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	s.logPreCheckResult(trace.IDFromContext(ctx), "pre-check returned", req, result)
	items := make([]*vmmv1.ContextItem, 0, len(result.ContextItems))
	for _, item := range result.ContextItems {
		items = append(items, &vmmv1.ContextItem{
			Kind:   item.Kind,
			Title:  item.Title,
			Text:   item.Text,
			Source: item.Source,
			Score:  item.Score,
		})
	}
	return &vmmv1.PreCheckResponse{
		ShouldInject: result.ShouldInject,
		ContextText:  result.ContextText,
		ContextItems: items,
		Degraded:     result.Degraded,
		TraceId:      trace.IDFromContext(ctx),
	}, nil
}

// logPreCheckReceipt writes one accepted pre-check request snapshot into the runtime logger, defaulting to redacted metadata and only emitting full request text when the explicit RPC payload debug switch is enabled.
// logPreCheckReceipt 用于把已接收的 pre-check 请求快照写入运行时日志；默认只输出脱敏元信息，只有显式 RPC 载荷调试开关开启时才会输出完整请求正文。
func (s *Server) logPreCheckReceipt(traceID, message string, req *vmmv1.PreCheckRequest) {
	if s == nil || s.logger == nil || req == nil {
		return
	}
	if s.debugRPCPayloads {
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"user_content", req.GetUserContent(),
		)
		return
	}
	s.logger.Info(
		message,
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"user_content_present", req.GetUserContent() != "",
	)
}

// logPreCheckResult writes one pre-check response snapshot into the runtime logger, defaulting to safe execution metadata and only emitting the assembled context payload when the explicit RPC payload debug switch is enabled.
// logPreCheckResult 用于把 pre-check 返回结果快照写入运行时日志；默认只输出安全执行元信息，只有显式 RPC 载荷调试开关开启时才会输出组装后的完整上下文。
func (s *Server) logPreCheckResult(traceID, message string, req *vmmv1.PreCheckRequest, result usecase.PreCheckResult) {
	if s == nil || s.logger == nil {
		return
	}
	nonEmptyContextItems := 0
	for _, item := range result.ContextItems {
		if item.Kind != "" || item.Title != "" || item.Text != "" || item.Source != "" || item.Score != 0 {
			nonEmptyContextItems++
		}
	}
	if s.debugRPCPayloads {
		contextItemsJSON, err := json.Marshal(result.ContextItems)
		if err != nil {
			s.logger.Warn(message+" context items marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
			s.logger.Info(
				message,
				"trace_id", traceID,
				"session_id", req.GetSessionId(),
				"should_inject", result.ShouldInject,
				"degraded", result.Degraded,
				"context_text", result.ContextText,
				"context_items", len(result.ContextItems),
				"context_nonempty_items", nonEmptyContextItems,
			)
			return
		}
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"should_inject", result.ShouldInject,
			"degraded", result.Degraded,
			"context_text", result.ContextText,
			"context_items", len(result.ContextItems),
			"context_nonempty_items", nonEmptyContextItems,
			"context_items_json", string(contextItemsJSON),
		)
		return
	}
	s.logger.Info(
		message,
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"should_inject", result.ShouldInject,
		"degraded", result.Degraded,
		"context_text_present", result.ContextText != "",
		"context_items", len(result.ContextItems),
		"context_nonempty_items", nonEmptyContextItems,
	)
}

// PostAction validates the request, logs raw and cleaned payloads, then completes synchronous persistence before returning.
// PostAction 用于校验请求、记录原始与清洗后载荷，并在返回前完成同步持久化。
func (s *Server) PostAction(ctx context.Context, req *vmmv1.PostActionRequest) (*vmmv1.PostActionResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.postAction == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePostActionRequest(req)
	if err := s.validator().ValidatePostAction(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	traceID := trace.IDFromContext(ctx)
	rawReq := clonePostActionRequest(req)
	cleanedReq := s.sanitizePostActionRequest(req)
	cmd := toPostActionCommand(session, rawReq, cleanedReq)
	s.logPostActionReceipt(traceID, "post-action received raw", rawReq)
	s.logPostActionReceipt(traceID, "post-action received cleaned", cleanedReq)

	// Run the post-action flow synchronously so the transport contract matches the immediate extraction mainline.
	// 同步执行 post-action 流程，让传输契约和即时提炼主链保持一致。
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()
	ctx = trace.WithTraceID(ctx, traceID)
	result, err := s.postAction.Execute(ctx, cmd)
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.PostActionResponse{
		Accepted: result.Accepted,
		TraceId:  traceID,
	}, nil
}

// withTimeout wraps one RPC context with a configured timeout when the timeout is positive.
// withTimeout 用于在超时为正数时给 RPC 上下文包一层配置化超时，并在直接测试或手工集成传入 nil context 时回退到 background context。
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// toProjectEntry converts one domain project record into the protobuf transport shape used by admin RPCs.
// toProjectEntry 用于把领域层项目记录转换成管理 RPC 使用的 protobuf 传输结构。
func toProjectEntry(project logicdomain.ProjectRecord) *vmmv1.ProjectEntry {
	if project.ID == 0 {
		return nil
	}
	return &vmmv1.ProjectEntry{
		ProjectId:   project.ID,
		TeamId:      project.TeamID,
		SpaceId:     project.SpaceID,
		TeamName:    project.TeamName,
		SpaceName:   project.SpaceName,
		ProjectName: project.Name,
		DisplayPath: fmt.Sprintf("[%d]%s", project.ID, project.Path()),
	}
}

// toUserEntry converts one domain user record into the protobuf transport shape used by admin RPCs.
// toUserEntry 用于把领域层用户记录转换成管理 RPC 使用的 protobuf 传输结构。
func toUserEntry(user logicdomain.UserRecord) *vmmv1.UserEntry {
	if user.ID == 0 {
		return nil
	}
	return &vmmv1.UserEntry{
		UserId:   user.ID,
		UserName: user.Name,
	}
}

// toProfileNodeEntry converts one active profile node into the protobuf transport shape used by profile query and manual instruction RPCs.
// toProfileNodeEntry 用于把一条 active 画像节点转换成画像查询与手工画像指令 RPC 使用的 protobuf 传输结构。
func toProfileNodeEntry(node logicdomain.ProfileNodeRecord) *vmmv1.ProfileNodeEntry {
	if node.ID == 0 {
		return nil
	}
	expiresTimestamp := int64(0)
	if !node.ExpiresAt.IsZero() {
		expiresTimestamp = node.ExpiresAt.UTC().UnixMilli()
	}
	return &vmmv1.ProfileNodeEntry{
		ProfileNodeId:    node.ID,
		Target:           toProtoProfileTarget(node.ProfileType),
		BindId:           node.BindID,
		Content:          node.Content,
		Priority:         profilePriorityLabel(node.Priority),
		Level:            profileLevelLabel(node.ProfileLevel),
		RefreshWeight:    uint32(maxInt(node.RefreshWeight, 0)),
		ProfileDate:      node.ProfileDate,
		ExpiresTimestamp: expiresTimestamp,
		LevelReason:      node.LevelReason,
		SourceKind:       toProtoProfileSourceKind(node.SourceKind),
		SourceId:         node.SourceID,
	}
}

// toTurnDetailEntry converts one detailed turn result into the protobuf transport shape used by the turn-detail lookup RPC.
// toTurnDetailEntry 用于把一条补齐内容和相邻编号的 turn 详情结果转换成 turn 查询 RPC 使用的 protobuf 传输结构。
func toTurnDetailEntry(turn usecase.TurnDetailRecord) *vmmv1.TurnDetailEntry {
	if turn.Turn.ID == 0 {
		return nil
	}
	timeline := make([]*vmmv1.PostActionTimelineItem, 0, len(turn.Timeline))
	for _, item := range turn.Timeline {
		timeline = append(timeline, &vmmv1.PostActionTimelineItem{
			Type:    item.Type,
			Content: item.Content,
		})
	}
	return &vmmv1.TurnDetailEntry{
		TurnId:            turn.Turn.ID,
		SessionId:         turn.Turn.SessionID,
		ProjectId:         turn.Turn.ProjectID,
		DehydratedContent: turn.Turn.DehydratedContent,
		DehydratedBudget:  int32(turn.Turn.DehydratedBudget),
		ExtractedStatus:   int32(turn.Turn.ExtractedStatus),
		Details:           turn.Turn.Details,
		DetailsBudget:     int32(turn.Turn.DetailsBudget),
		CreatedTimestamp:  turn.Turn.CreatedAt.UTC().UnixMilli(),
		UpdatedTimestamp:  turn.Turn.UpdatedAt.UTC().UnixMilli(),
		UserContent:       turn.UserContent,
		Timeline:          timeline,
		AssistantContent:  turn.AssistantContent,
		PreviousTurnIds:   append([]uint64(nil), turn.PreviousTurnIDs...),
		NextTurnIds:       append([]uint64(nil), turn.NextTurnIDs...),
	}
}

// toMemoryDetailEntry converts one unified durable memory row into the protobuf transport shape used by the mixed detail RPC.
// toMemoryDetailEntry 用于把一条统一长期记忆行转换成混合详情 RPC 使用的 protobuf 传输结构。
func toMemoryDetailEntry(memory logicdomain.MemoryNodeRecord) *vmmv1.MemoryDetailEntry {
	if memory.ID == 0 {
		return nil
	}
	return &vmmv1.MemoryDetailEntry{
		MemoryId:                 memory.ID,
		SourceKind:               toProtoMemorySourceKind(memory.SourceKind),
		ScopeLevel:               toProtoMemoryScopeLevel(memory.ScopeLevel),
		MemoryStatus:             toProtoMemoryStatus(memory.Status),
		TeamId:                   memory.TeamID,
		SpaceId:                  memory.SpaceID,
		ProjectId:                memory.ProjectID,
		UserId:                   memory.UserID,
		OriginSessionId:          memory.OriginSessionID,
		SourceTurnId:             memory.SourceTurnID,
		Category:                 int32(memory.Category),
		Abstract:                 memory.Abstract,
		Details:                  memory.Details,
		Priority:                 toProtoMemoryPriority(memory.Priority),
		MemoryLevel:              toProtoMemoryLevel(memory.MemoryLevel),
		RefreshWeight:            uint32(maxInt(memory.RefreshWeight, 0)),
		StatusReason:             memory.StatusReason,
		ExpiresTimestamp:         toUnixMillis(memory.ExpiresAt),
		LastRecalledTimestamp:    toUnixMillis(memory.LastRecalledAt),
		LastAdoptedTimestamp:     toUnixMillis(memory.LastAdoptedAt),
		RecalledCount:            uint32(maxInt(memory.RecalledCount, 0)),
		AdoptedCount:             uint32(maxInt(memory.AdoptedCount, 0)),
		CrossSessionAdoptedCount: uint32(maxInt(memory.CrossSessionAdoptedCount, 0)),
		CreatedTimestamp:         toUnixMillis(memory.CreatedAt),
		UpdatedTimestamp:         toUnixMillis(memory.UpdatedAt),
	}
}

// sanitizePostActionRequest clones the validated request into one storage-ready copy so raw logs and persisted text can diverge safely.
// sanitizePostActionRequest 用于把已校验请求复制成一份面向存储的版本，让原始日志与入库文本可以安全分离。
func (s *Server) sanitizePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	cloned := clonePostActionRequest(req)
	if cloned == nil {
		return cloned
	}

	// Reuse the default storage sanitizer for partially constructed servers so direct tests and manual integrations still see the same cleaned payload contract as the main runtime path.
	// 对于部分装配的服务实例，这里仍复用默认的存储型清洗器，保证直接测试和手工集成拿到与主运行时一致的清洗后载荷契约。
	sanitizer := s.postActionSanitizer()
	cloned.UserContent = sanitizer.Sanitize(cloned.GetUserContent())
	cloned.AssistantContent = sanitizer.Sanitize(cloned.GetAssistantContent())
	for _, item := range cloned.GetTimeline() {
		item.Content = sanitizer.Sanitize(item.GetContent())
	}
	return cloned
}

// toPostActionCommand converts both raw and cleaned transport requests plus the resolved session scope into the use-case command shape.
// toPostActionCommand 用于把原始/清洗后的传输层请求，以及已解析的 session 范围，一起转换成用例层命令。
func toPostActionCommand(session logicdomain.SessionRef, rawReq, cleanedReq *vmmv1.PostActionRequest) usecase.PostActionCommand {
	items := make([]usecase.PostActionTimelineItem, 0, len(cleanedReq.GetTimeline()))
	for _, item := range cleanedReq.GetTimeline() {
		items = append(items, usecase.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	rawItems := make([]usecase.PostActionTimelineItem, 0, len(rawReq.GetTimeline()))
	for _, item := range rawReq.GetTimeline() {
		rawItems = append(rawItems, usecase.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	return usecase.PostActionCommand{
		Session:             session,
		UserContent:         cleanedReq.GetUserContent(),
		AssistantContent:    cleanedReq.GetAssistantContent(),
		Timeline:            items,
		RawUserContent:      rawReq.GetUserContent(),
		RawAssistantContent: rawReq.GetAssistantContent(),
		RawTimeline:         rawItems,
	}
}

// clonePostActionRequest copies the PostAction request so normalization, cleaning, and logging can operate on independent instances.
// clonePostActionRequest 用于复制 PostAction 请求，让规范化、清洗和日志输出可以在独立实例上进行。
func clonePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	if req == nil {
		return nil
	}
	cloned := &vmmv1.PostActionRequest{
		SessionId:        req.GetSessionId(),
		UserId:           req.GetUserId(),
		ProjectId:        req.GetProjectId(),
		UserContent:      req.GetUserContent(),
		AssistantContent: req.GetAssistantContent(),
		Timeline:         make([]*vmmv1.PostActionTimelineItem, 0, len(req.GetTimeline())),
	}
	for _, item := range req.GetTimeline() {
		cloned.Timeline = append(cloned.Timeline, &vmmv1.PostActionTimelineItem{
			Type:    item.GetType(),
			Content: item.GetContent(),
		})
	}
	return cloned
}

// logPostActionReceipt writes one accepted payload snapshot into the runtime logger, defaulting to redacted metadata and only emitting full payload text when the explicit RPC payload debug switch is enabled.
// logPostActionReceipt 用于把已接收载荷快照写入运行时日志；默认只输出脱敏元信息，只有显式 RPC 载荷调试开关开启时才会输出完整正文。
func (s *Server) logPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
	if s == nil || s.logger == nil || req == nil {
		return
	}
	nonEmptyTimelineItems := 0
	for _, item := range req.GetTimeline() {
		if item == nil {
			continue
		}
		if item.GetType() != "" || item.GetContent() != "" {
			nonEmptyTimelineItems++
		}
	}

	// Keep raw payload text behind one explicit debug-only switch so production-safe logs
	// remain redacted by default while local troubleshooting can still opt into full receipts.
	// 把原始载荷正文放到显式调试开关之后，让默认日志继续保持安全脱敏，
	// 同时在本地排障场景下仍然可以选择输出完整收据。
	if s.debugRPCPayloads {
		timelineJSON, err := json.Marshal(req.GetTimeline())
		if err != nil {
			s.logger.Warn(message+" timeline marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
			s.logger.Info(
				message,
				"trace_id", traceID,
				"session_id", req.GetSessionId(),
				"user_content", req.GetUserContent(),
				"assistant_content", req.GetAssistantContent(),
				"timeline_items", len(req.GetTimeline()),
				"timeline_nonempty_items", nonEmptyTimelineItems,
			)
			return
		}
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"user_content", req.GetUserContent(),
			"assistant_content", req.GetAssistantContent(),
			"timeline_items", len(req.GetTimeline()),
			"timeline_nonempty_items", nonEmptyTimelineItems,
			"timeline_json", string(timelineJSON),
		)
		return
	}
	timelineJSON, err := json.Marshal(req.GetTimeline())
	if err != nil {
		s.logger.Warn(message+" timeline marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
		s.logger.Info(
			message,
			"trace_id", traceID,
			"session_id", req.GetSessionId(),
			"user_content_present", req.GetUserContent() != "",
			"assistant_content_present", req.GetAssistantContent() != "",
			"timeline_items", len(req.GetTimeline()),
			"timeline_nonempty_items", nonEmptyTimelineItems,
		)
		return
	}
	s.logger.Info(
		message,
		"trace_id", traceID,
		"session_id", req.GetSessionId(),
		"user_content_present", req.GetUserContent() != "",
		"assistant_content_present", req.GetAssistantContent() != "",
		"timeline_items", len(req.GetTimeline()),
		"timeline_nonempty_items", nonEmptyTimelineItems,
		"timeline_marshaled", len(timelineJSON) > 0,
	)
}

// shortContentDigest produces one short stable digest for sensitive payloads so logs can still correlate raw/cleaned receipts without writing the underlying text into runtime logs.
// shortContentDigest 用于为敏感载荷生成短且稳定的摘要，让日志在不暴露底层文本的前提下仍能关联 raw/cleaned 两次收据。
func shortContentDigest(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// fromProtoProfileTarget converts the protobuf profile target enum into the internal profile-type enum.
// fromProtoProfileTarget 用于把 protobuf 的画像目标枚举转换成内部画像类型枚举。
func fromProtoProfileTarget(target vmmv1.ProfileTarget) (int, error) {
	switch target {
	case vmmv1.ProfileTarget_PROFILE_TARGET_USER:
		return logicdomain.ProfileTypeUser, nil
	case vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT:
		return logicdomain.ProfileTypeProject, nil
	case vmmv1.ProfileTarget_PROFILE_TARGET_TEAM:
		return logicdomain.ProfileTypeTeam, nil
	case vmmv1.ProfileTarget_PROFILE_TARGET_SPACE:
		return logicdomain.ProfileTypeSpace, nil
	default:
		return 0, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
}

// fromProtoProfileBundleMode converts the protobuf bundle mode enum into the internal profile-bundle mode used by the use case.
// fromProtoProfileBundleMode 用于把 protobuf 的 bundle 模式枚举转换成用例层使用的内部模式。
func fromProtoProfileBundleMode(mode vmmv1.ProfileBundleMode) (int, error) {
	switch mode {
	case vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL:
		return usecase.ProfileBundleModeFull, nil
	case vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_SPLIT:
		return usecase.ProfileBundleModeSplit, nil
	default:
		return 0, logicdomain.ValidationError{Field: "mode", Message: "must be full or split"}
	}
}

// toProtoProfileTarget converts the internal profile-type enum back into the protobuf profile target enum.
// toProtoProfileTarget 用于把内部画像类型枚举转换回 protobuf 的画像目标枚举。
func toProtoProfileTarget(profileType int) vmmv1.ProfileTarget {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return vmmv1.ProfileTarget_PROFILE_TARGET_USER
	case logicdomain.ProfileTypeProject:
		return vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT
	case logicdomain.ProfileTypeTeam:
		return vmmv1.ProfileTarget_PROFILE_TARGET_TEAM
	case logicdomain.ProfileTypeSpace:
		return vmmv1.ProfileTarget_PROFILE_TARGET_SPACE
	default:
		return vmmv1.ProfileTarget_PROFILE_TARGET_UNSPECIFIED
	}
}

// toProtoProfileBundleMode converts the internal bundle mode back into the protobuf enum used by the transport response.
// toProtoProfileBundleMode 用于把内部 bundle 模式转换回传输层响应使用的 protobuf 枚举。
func toProtoProfileBundleMode(mode int) vmmv1.ProfileBundleMode {
	switch mode {
	case usecase.ProfileBundleModeFull:
		return vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL
	case usecase.ProfileBundleModeSplit:
		return vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_SPLIT
	default:
		return vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_UNSPECIFIED
	}
}

// toProtoProfileSourceKind converts the internal source kind enum into the protobuf profile-node source enum.
// toProtoProfileSourceKind 用于把内部来源类型枚举转换成 protobuf 的画像节点来源枚举。
func toProtoProfileSourceKind(sourceKind int) vmmv1.ProfileNodeSourceKind {
	switch sourceKind {
	case logicdomain.ProfileSourceKindManualInstruction:
		return vmmv1.ProfileNodeSourceKind_PROFILE_NODE_SOURCE_KIND_MANUAL_INSTRUCTION
	case logicdomain.ProfileSourceKindSystemSeed:
		return vmmv1.ProfileNodeSourceKind_PROFILE_NODE_SOURCE_KIND_SYSTEM_SEED
	case logicdomain.ProfileSourceKindRetainedAfterUserDelete:
		return vmmv1.ProfileNodeSourceKind_PROFILE_NODE_SOURCE_KIND_RETAINED_AFTER_USER_DELETE
	default:
		return vmmv1.ProfileNodeSourceKind_PROFILE_NODE_SOURCE_KIND_TURN_EXTRACT
	}
}

// toProtoMemoryRef converts one internal memory ref into the protobuf `TYPE + ID` transport shape.
// toProtoMemoryRef 用于把内部 memory ref 转换成 protobuf 的 `TYPE + ID` 传输结构。
func toProtoMemoryRef(ref logicdomain.MemoryRef) *vmmv1.MemoryRef {
	if ref.Empty() {
		return nil
	}
	return &vmmv1.MemoryRef{
		Type: toProtoMemoryRefType(ref.Type),
		Id:   ref.ID,
	}
}

// fromProtoMemoryRef converts one protobuf `TYPE + ID` pair into the internal memory-ref model.
// fromProtoMemoryRef 用于把 protobuf 的 `TYPE + ID` 组合转换成内部 memory-ref 模型。
func fromProtoMemoryRef(ref *vmmv1.MemoryRef) logicdomain.MemoryRef {
	if ref == nil {
		return logicdomain.MemoryRef{}
	}
	return logicdomain.MemoryRef{
		Type: fromProtoMemoryRefType(ref.GetType()),
		ID:   ref.GetId(),
	}
}

// toProtoMemoryRefType converts the internal memory-ref type into the protobuf enum.
// toProtoMemoryRefType 用于把内部 memory-ref 类型转换成 protobuf 枚举。
func toProtoMemoryRefType(refType int) vmmv1.MemoryRefType {
	switch refType {
	case logicdomain.MemoryRefTypeTurn:
		return vmmv1.MemoryRefType_MEMORY_REF_TYPE_TURN
	case logicdomain.MemoryRefTypeMemory:
		return vmmv1.MemoryRefType_MEMORY_REF_TYPE_MEMORY
	default:
		return vmmv1.MemoryRefType_MEMORY_REF_TYPE_UNSPECIFIED
	}
}

// fromProtoMemoryRefType converts the protobuf memory-ref enum into the internal ref type.
// fromProtoMemoryRefType 用于把 protobuf 的 memory-ref 枚举转换成内部 ref 类型。
func fromProtoMemoryRefType(refType vmmv1.MemoryRefType) int {
	switch refType {
	case vmmv1.MemoryRefType_MEMORY_REF_TYPE_TURN:
		return logicdomain.MemoryRefTypeTurn
	case vmmv1.MemoryRefType_MEMORY_REF_TYPE_MEMORY:
		return logicdomain.MemoryRefTypeMemory
	default:
		return 0
	}
}

// toProtoMemorySourceKind converts the internal unified-memory source kind into the protobuf enum.
// toProtoMemorySourceKind 用于把内部统一记忆来源类型转换成 protobuf 枚举。
func toProtoMemorySourceKind(sourceKind int) vmmv1.MemorySourceKind {
	switch sourceKind {
	case logicdomain.MemorySourceKindGRPCAIWrite:
		return vmmv1.MemorySourceKind_MEMORY_SOURCE_KIND_GRPC_AI_WRITE
	case logicdomain.MemorySourceKindSystemSeed:
		return vmmv1.MemorySourceKind_MEMORY_SOURCE_KIND_SYSTEM_SEED
	case logicdomain.MemorySourceKindLegacyEntry:
		return vmmv1.MemorySourceKind_MEMORY_SOURCE_KIND_LEGACY_ENTRY
	case logicdomain.MemorySourceKindTurnExtract:
		return vmmv1.MemorySourceKind_MEMORY_SOURCE_KIND_TURN_EXTRACT
	default:
		return vmmv1.MemorySourceKind_MEMORY_SOURCE_KIND_UNSPECIFIED
	}
}

// toProtoMemoryScopeLevel converts the internal unified-memory scope level into the protobuf enum.
// toProtoMemoryScopeLevel 用于把内部统一记忆作用域等级转换成 protobuf 枚举。
func toProtoMemoryScopeLevel(scopeLevel int) vmmv1.MemoryScopeLevel {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_SESSION
	case logicdomain.MemoryScopeLevelUser:
		return vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_USER
	case logicdomain.MemoryScopeLevelProject:
		return vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_PROJECT
	default:
		return vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_UNSPECIFIED
	}
}

// fromProtoMemoryScopeLevel converts the protobuf scope enum into the internal unified-memory scope level.
// fromProtoMemoryScopeLevel 用于把 protobuf 的作用域枚举转换成内部统一记忆作用域等级。
func fromProtoMemoryScopeLevel(scopeLevel vmmv1.MemoryScopeLevel) int {
	switch scopeLevel {
	case vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_SESSION:
		return logicdomain.MemoryScopeLevelSession
	case vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_PROJECT:
		return logicdomain.MemoryScopeLevelProject
	case vmmv1.MemoryScopeLevel_MEMORY_SCOPE_LEVEL_USER:
		return logicdomain.MemoryScopeLevelUser
	default:
		return -1
	}
}

// toProtoMemoryStatus converts the internal durable-memory status into the protobuf enum.
// toProtoMemoryStatus 用于把内部长期记忆状态转换成 protobuf 枚举。
func toProtoMemoryStatus(status int) vmmv1.MemoryStatus {
	switch status {
	case logicdomain.MemoryStatusSuperseded:
		return vmmv1.MemoryStatus_MEMORY_STATUS_SUPERSEDED
	case logicdomain.MemoryStatusDeleted:
		return vmmv1.MemoryStatus_MEMORY_STATUS_DELETED
	case logicdomain.MemoryStatusExpired:
		return vmmv1.MemoryStatus_MEMORY_STATUS_EXPIRED
	case logicdomain.MemoryStatusActive:
		return vmmv1.MemoryStatus_MEMORY_STATUS_ACTIVE
	default:
		return vmmv1.MemoryStatus_MEMORY_STATUS_UNSPECIFIED
	}
}

// toProtoMemoryPriority converts the internal memory priority into the protobuf enum.
// toProtoMemoryPriority 用于把内部记忆优先级转换成 protobuf 枚举。
func toProtoMemoryPriority(priority int) vmmv1.MemoryPriority {
	switch priority {
	case logicdomain.MemoryPriorityP0:
		return vmmv1.MemoryPriority_MEMORY_PRIORITY_P0
	case logicdomain.MemoryPriorityP1:
		return vmmv1.MemoryPriority_MEMORY_PRIORITY_P1
	case logicdomain.MemoryPriorityP2:
		return vmmv1.MemoryPriority_MEMORY_PRIORITY_P2
	default:
		return vmmv1.MemoryPriority_MEMORY_PRIORITY_UNSPECIFIED
	}
}

// fromProtoMemoryPriority converts the protobuf memory priority into the internal value, returning -1 for unspecified transport defaults.
// fromProtoMemoryPriority 用于把 protobuf 记忆优先级转换成内部值；若传输层未指定则返回 -1 以便用例层补默认值。
func fromProtoMemoryPriority(priority vmmv1.MemoryPriority) int {
	switch priority {
	case vmmv1.MemoryPriority_MEMORY_PRIORITY_P0:
		return logicdomain.MemoryPriorityP0
	case vmmv1.MemoryPriority_MEMORY_PRIORITY_P1:
		return logicdomain.MemoryPriorityP1
	case vmmv1.MemoryPriority_MEMORY_PRIORITY_P2:
		return logicdomain.MemoryPriorityP2
	default:
		return -1
	}
}

// toProtoMemoryLevel converts the internal memory lifecycle level into the protobuf enum.
// toProtoMemoryLevel 用于把内部记忆生命周期等级转换成 protobuf 枚举。
func toProtoMemoryLevel(level int) vmmv1.MemoryLevel {
	switch level {
	case logicdomain.MemoryLevelSession:
		return vmmv1.MemoryLevel_MEMORY_LEVEL_L0
	case logicdomain.MemoryLevelPhase:
		return vmmv1.MemoryLevel_MEMORY_LEVEL_L1
	case logicdomain.MemoryLevelStable:
		return vmmv1.MemoryLevel_MEMORY_LEVEL_L2
	case logicdomain.MemoryLevelPersistent:
		return vmmv1.MemoryLevel_MEMORY_LEVEL_L3
	default:
		return vmmv1.MemoryLevel_MEMORY_LEVEL_UNSPECIFIED
	}
}

// fromProtoMemoryLevel converts the protobuf memory lifecycle enum into the internal value, returning -1 when callers omit the field.
// fromProtoMemoryLevel 用于把 protobuf 记忆生命周期枚举转换成内部值；调用方省略字段时返回 -1。
func fromProtoMemoryLevel(level vmmv1.MemoryLevel) int {
	switch level {
	case vmmv1.MemoryLevel_MEMORY_LEVEL_L0:
		return logicdomain.MemoryLevelSession
	case vmmv1.MemoryLevel_MEMORY_LEVEL_L1:
		return logicdomain.MemoryLevelPhase
	case vmmv1.MemoryLevel_MEMORY_LEVEL_L2:
		return logicdomain.MemoryLevelStable
	case vmmv1.MemoryLevel_MEMORY_LEVEL_L3:
		return logicdomain.MemoryLevelPersistent
	default:
		return -1
	}
}

// toUnixMillis converts one UTC time into transport milliseconds and keeps zero-values empty on the wire.
// toUnixMillis 用于把 UTC 时间转换成传输层毫秒时间戳，并保持零值时间在传输层为空。
func toUnixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

// fromUnixMillis converts one transport millisecond timestamp into UTC time, preserving zero as the empty time.
// fromUnixMillis 用于把传输层毫秒时间戳转换成 UTC 时间，并把零值保留为空时间。
func fromUnixMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

// profilePriorityLabel renders the compact P-label used by the profile query transport.
// profilePriorityLabel 用于渲染画像查询传输层使用的紧凑 P 标签。
func profilePriorityLabel(priority int) string {
	switch priority {
	case logicdomain.ProfilePriorityP0:
		return "P0"
	case logicdomain.ProfilePriorityP1:
		return "P1"
	default:
		return "P2"
	}
}

// profileLevelLabel renders the compact L-label used by the profile query transport.
// profileLevelLabel 用于渲染画像查询传输层使用的紧凑 L 标签。
func profileLevelLabel(level int) string {
	switch level {
	case logicdomain.ProfileLevelTransient:
		return "L0"
	case logicdomain.ProfileLevelSituational:
		return "L1"
	case logicdomain.ProfileLevelStable:
		return "L2"
	default:
		return "L3"
	}
}

// maxInt keeps small transport conversions readable when wire-level numeric fields should never go below one fixed floor.
// maxInt 用于在传输层数值字段不应低于某个固定下限时，让小范围转换保持清晰可读。
func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}
