// server.go implements the inbound gRPC adapter that exposes workspace administration plus the main PreCheck/PostAction business chain.
// server.go 用于实现入站 gRPC 适配层，对外暴露空间管理接口以及主业务链路 PreCheck/PostAction。
package grpcapi

import (
	"context"
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
	PreCheck          usecase.PreCheckExecutor
	PostAction        usecase.PostActionExecutor
	ScopeResolver     appports.RequestScopeResolver
	Logger            *logx.Logger
	Validator         *RequestValidator
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
	preCheck         usecase.PreCheckExecutor
	postAction       usecase.PostActionExecutor
	workspaceTimeout time.Duration
	preTimeout       time.Duration
	postTimeout      time.Duration
	logger           *logx.Logger
	validate         *RequestValidator
	sanitizer        *textutil.PostActionTextSanitizer
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
		preCheck:         deps.PreCheck,
		postAction:       deps.PostAction,
		workspaceTimeout: deps.WorkspaceTimeout,
		preTimeout:       deps.PreCheckTimeout,
		postTimeout:      deps.PostActionTimeout,
		logger:           deps.Logger,
		validate:         deps.Validator,
		sanitizer:        textutil.NewPostActionTextSanitizer(),
	}
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
	interceptors = append(interceptors, deps.ExtraInterceptors...)
	return interceptors
}

// Healthz reports a healthy status for the local runtime.
// Healthz 用于报告本地运行时的健康状态。
func (s *Server) Healthz(ctx context.Context, _ *emptypb.Empty) (*vmmv1.HealthzResponse, error) {
	return &vmmv1.HealthzResponse{Status: "ok", TraceId: trace.IDFromContext(ctx)}, nil
}

// ListProjects returns the deterministic Team/Space/Project list used by clients to browse available project scopes.
// ListProjects 用于返回客户端浏览可用项目范围时需要的确定性 Team/Space/Project 列表。
func (s *Server) ListProjects(ctx context.Context, _ *emptypb.Empty) (*vmmv1.ListProjectsResponse, error) {
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
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeResolveProjectRequest(req)
	if err := s.validate.ValidateResolveProject(req); err != nil {
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
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeEnsureProjectRequest(req)
	if err := s.validate.ValidateEnsureProject(req); err != nil {
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
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeDeleteProjectRequest(req)
	if err := s.validate.ValidateDeleteProject(req); err != nil {
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
		DeletedSessions:   int64(result.DeletedSessions),
		DeletedMessages:   int64(result.DeletedMessages),
		DeletedMemories:   int64(result.DeletedMemories),
		DeletedVectorRows: result.DeletedVectorRows,
		TraceId:           trace.IDFromContext(ctx),
	}, nil
}

// MigrateProject migrates all SQL/vector data from one source project path onto another target project path.
// MigrateProject 用于把某个源项目路径下的全部 SQL/向量数据迁移到目标项目路径。
func (s *Server) MigrateProject(ctx context.Context, req *vmmv1.MigrateProjectRequest) (*vmmv1.MigrateProjectResponse, error) {
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeMigrateProjectRequest(req)
	if err := s.validate.ValidateMigrateProject(req); err != nil {
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
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeResolveUserRequest(req)
	if err := s.validate.ValidateResolveUser(req); err != nil {
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
	if s.workspace == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeDeleteUserRequest(req)
	if err := s.validate.ValidateDeleteUser(req); err != nil {
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
		DeletedSessions:      int64(result.DeletedSessions),
		DeletedMessages:      int64(result.DeletedMessages),
		DeletedMemories:      int64(result.DeletedMemories),
		DeletedVectorRows:    result.DeletedVectorRows,
		TraceId:              trace.IDFromContext(ctx),
	}, nil
}

// PreCheck validates the request, consumes the scope resolved by the interceptor, and returns the current deterministic no-injection response.
// PreCheck 用于校验请求、消费拦截器解析出的范围，并返回当前确定性的“不注入”响应。
func (s *Server) PreCheck(ctx context.Context, req *vmmv1.PreCheckRequest) (*vmmv1.PreCheckResponse, error) {
	if s.preCheck == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePreCheckRequest(req)
	if err := s.validate.ValidatePreCheck(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.preTimeout)
	defer cancel()
	result, err := s.preCheck.Execute(ctx, usecase.PreCheckCommand{
		Session:     session,
		UserContent: req.GetUserContent(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
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

// PostAction validates the request, logs raw and cleaned payloads, returns immediately, and continues persistence in the background.
// PostAction 用于校验请求、记录原始与清洗后载荷、立即返回，并在后台继续持久化。
func (s *Server) PostAction(ctx context.Context, req *vmmv1.PostActionRequest) (*vmmv1.PostActionResponse, error) {
	if s.postAction == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizePostActionRequest(req)
	if err := s.validate.ValidatePostAction(req); err != nil {
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

	// Acknowledge first so upstream plugins are not blocked by downstream persistence work.
	// 先返回确认，避免上游插件被下游持久化工作阻塞。
	go func(cmd usecase.PostActionCommand, traceID string) {
		bgctx, cancel := withTimeout(context.Background(), s.postTimeout)
		defer cancel()
		bgctx = trace.WithTraceID(bgctx, traceID)
		if _, err := s.postAction.Execute(bgctx, cmd); err != nil && s.logger != nil {
			s.logger.Error("async post-action failed", "trace_id", traceID, "session_key", cmd.Session.SessionKey, "err", err)
		}
	}(cmd, traceID)

	return &vmmv1.PostActionResponse{Accepted: true, TraceId: traceID}, nil
}

// withTimeout wraps one RPC context with a configured timeout when the timeout is positive.
// withTimeout 用于在超时为正数时给 RPC 上下文包一层配置化超时。
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
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

// sanitizePostActionRequest clones the validated request into one storage-ready copy so raw logs and persisted text can diverge safely.
// sanitizePostActionRequest 用于把已校验请求复制成一份面向存储的版本，让原始日志与入库文本可以安全分离。
func (s *Server) sanitizePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	cloned := clonePostActionRequest(req)
	if cloned == nil || s == nil || s.sanitizer == nil {
		return cloned
	}
	cloned.UserContent = s.sanitizer.Sanitize(cloned.GetUserContent())
	cloned.AssistantContent = s.sanitizer.Sanitize(cloned.GetAssistantContent())
	for _, item := range cloned.GetTimeline() {
		item.Content = s.sanitizer.Sanitize(item.GetContent())
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

// logPostActionReceipt writes one accepted payload snapshot into the runtime logger for local debugging.
// logPostActionReceipt 用于把已接收载荷快照写入运行时日志，方便本地调试。
func (s *Server) logPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
	if s == nil || s.logger == nil || req == nil {
		return
	}
	timelineJSON, err := json.Marshal(req.GetTimeline())
	if err != nil {
		s.logger.Warn(message+" timeline marshal failed", "trace_id", traceID, "session_id", req.GetSessionId(), "err", err)
		s.logger.Info(message, "trace_id", traceID, "session_id", req.GetSessionId(), "user_content", req.GetUserContent(), "assistant_content", req.GetAssistantContent(), "timeline_items", len(req.GetTimeline()))
		return
	}
	s.logger.Info(message, "trace_id", traceID, "session_id", req.GetSessionId(), "user_content", req.GetUserContent(), "assistant_content", req.GetAssistantContent(), "timeline", string(timelineJSON))
}
