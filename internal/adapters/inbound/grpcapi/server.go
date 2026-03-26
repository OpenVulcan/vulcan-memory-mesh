// server.go implements the inbound gRPC adapter that maps protobuf requests into use-case commands.
// server.go 用于实现入站 gRPC 适配层，把 protobuf 请求映射成用例命令。
package grpcapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Dependencies collects the gRPC-facing use cases plus timeout and validation settings.
// Dependencies 用于收集 gRPC 侧所需的用例、超时和校验配置。
type Dependencies struct {
	IDs               interface{ NewID(prefix string) string }
	Chat              usecase.ChatExecutor
	PreCheck          usecase.PreCheckExecutor
	PostAction        usecase.PostActionExecutor
	SeedMemory        usecase.SeedMemoryExecutor
	Logger            *logx.Logger
	Validator         *RequestValidator
	ChatTimeout       time.Duration
	PreCheckTimeout   time.Duration
	PostActionTimeout time.Duration
	SeedMemoryTimeout time.Duration
	ExtraInterceptors []grpc.UnaryServerInterceptor
}

// Server groups gRPC-facing use cases, validation, and timeout settings for the inbound adapter.
// Server 用于聚合入站 gRPC 适配层依赖的用例、校验器和超时配置。
type Server struct {
	vmmv1.UnimplementedVMMServiceServer

	chat        usecase.ChatExecutor
	preCheck    usecase.PreCheckExecutor
	postAction  usecase.PostActionExecutor
	seedMemory  usecase.SeedMemoryExecutor
	chatTimeout time.Duration
	preTimeout  time.Duration
	postTimeout time.Duration
	seedTimeout time.Duration
	logger      *logx.Logger
	validate    *RequestValidator
	sanitizer   *textutil.PostActionTextSanitizer
}

// NewServer creates a gRPC server implementation from the supplied dependencies.
// NewServer 用于根据输入依赖创建 gRPC 服务实现。
func NewServer(deps Dependencies) *Server {
	if deps.Logger == nil {
		deps.Logger = logx.Default()
	}
	if deps.Validator == nil {
		deps.Validator = NewRequestValidator("")
	}
	return &Server{
		chat:        deps.Chat,
		preCheck:    deps.PreCheck,
		postAction:  deps.PostAction,
		seedMemory:  deps.SeedMemory,
		chatTimeout: deps.ChatTimeout,
		preTimeout:  deps.PreCheckTimeout,
		postTimeout: deps.PostActionTimeout,
		seedTimeout: deps.SeedMemoryTimeout,
		logger:      deps.Logger,
		validate:    deps.Validator,
		sanitizer:   textutil.NewPostActionTextSanitizer(),
	}
}

// BuildUnaryInterceptors returns the standard unary interceptor chain used by the gRPC server.
// BuildUnaryInterceptors 用于返回 gRPC 服务使用的标准一元拦截器链。
func BuildUnaryInterceptors(deps Dependencies) []grpc.UnaryServerInterceptor {
	interceptors := []grpc.UnaryServerInterceptor{
		RecoveryInterceptor(deps.Logger),
		TraceIDInterceptor(deps.IDs),
		RequestLoggerInterceptor(deps.Logger),
	}
	interceptors = append(interceptors, deps.ExtraInterceptors...)
	return interceptors
}

// Healthz reports a healthy status for the local runtime.
// Healthz 用于报告本地运行时的健康状态。
func (s *Server) Healthz(ctx context.Context, _ *emptypb.Empty) (*vmmv1.HealthzResponse, error) {
	return &vmmv1.HealthzResponse{Status: "ok", TraceId: trace.IDFromContext(ctx)}, nil
}

// Chat validates the request, scrubs one message, and archives the sanitized result.
// Chat 用于校验请求、脱敏单条消息，并归档清洗后的结果。
func (s *Server) Chat(ctx context.Context, req *vmmv1.ChatRequest) (*vmmv1.ChatResponse, error) {
	if s.chat == nil {
		return nil, toStatus(errRouteDisabled)
	}
	normalizeChatRequest(req)
	if err := s.validate.ValidateChat(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.chatTimeout)
	defer cancel()
	result, err := s.chat.Execute(ctx, usecase.ChatCommand{
		SessionID: req.GetSessionId(),
		Message:   req.GetMessage(),
		Language:  preferredLanguage(req.GetAcceptLanguage()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ChatResponse{
		SessionId: result.SessionID,
		Message:   result.Message,
		Language:  result.Language,
		TraceId:   trace.IDFromContext(ctx),
	}, nil
}

// PreCheck validates the request and forwards it into the pre-check use case.
// PreCheck 用于校验请求并将其转发到 pre-check 用例。
func (s *Server) PreCheck(ctx context.Context, req *vmmv1.PreCheckRequest) (*vmmv1.PreCheckResponse, error) {
	if s.preCheck == nil {
		return nil, toStatus(errRouteDisabled)
	}
	normalizePreCheckRequest(req)
	if err := s.validate.ValidatePreCheck(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.preTimeout)
	defer cancel()
	result, err := s.preCheck.Execute(ctx, usecase.PreCheckCommand{
		SessionID:      req.GetSessionId(),
		UserID:         req.GetUserId(),
		TeamID:         req.GetTeamId(),
		SpaceID:        req.GetSpaceId(),
		ProjectID:      req.GetProjectId(),
		CurrentContent: req.GetUserContent(),
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

// PostAction accepts the new text-only contract, logs it, returns immediately, and continues in the background.
// PostAction 用于接收新的纯文本契约、打印日志、立即返回，并在后台继续处理。
func (s *Server) PostAction(ctx context.Context, req *vmmv1.PostActionRequest) (*vmmv1.PostActionResponse, error) {
	if s.postAction == nil {
		return nil, toStatus(errRouteDisabled)
	}
	rawReq := clonePostActionRequest(req)
	normalizePostActionRequest(req)
	if err := s.validate.ValidatePostAction(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	traceID := trace.IDFromContext(ctx)
	cleanedReq := s.sanitizePostActionRequest(req)
	cmd := toAsyncPostActionCommand(cleanedReq)
	s.logAsyncPostActionReceipt(traceID, "post-action received raw", rawReq)
	s.logAsyncPostActionReceipt(traceID, "post-action received cleaned", cleanedReq)

	// Acknowledge first so upstream plugins are not blocked by downstream persistence work.
	// 先返回确认，避免上游插件被下游持久化工作阻塞。
	go func(cmd usecase.PostActionCommand, traceID string) {
		bgctx, cancel := withTimeout(context.Background(), s.postTimeout)
		defer cancel()
		bgctx = trace.WithTraceID(bgctx, traceID)
		if _, err := s.postAction.Execute(bgctx, cmd); err != nil && s.logger != nil {
			s.logger.Error("async post-action failed", "trace_id", traceID, "session_id", cmd.SessionID, "err", err)
		}
	}(cmd, traceID)
	return &vmmv1.PostActionResponse{Accepted: true, TraceId: traceID}, nil
}

// PostActionOld validates the legacy snapshot request and executes the existing persistence flow synchronously.
// PostActionOld 用于校验旧版快照请求，并同步执行现有持久化流程。
func (s *Server) PostActionOld(ctx context.Context, req *vmmv1.PostActionOldRequest) (*vmmv1.PostActionResponse, error) {
	if s.postAction == nil {
		return nil, toStatus(errRouteDisabled)
	}
	if err := s.validate.PreparePostActionOld(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()
	result, err := s.postAction.Execute(ctx, usecase.PostActionCommand{
		SessionID:           req.GetSessionId(),
		UserID:              req.GetUserId(),
		TeamID:              req.GetTeamId(),
		SpaceID:             req.GetSpaceId(),
		ProjectID:           req.GetProjectId(),
		RawMessagesSnapshot: toRawMessagesDomain(req.GetRawMessagesSnapshot()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.PostActionResponse{Accepted: result.Accepted, TraceId: trace.IDFromContext(ctx)}, nil
}

// SeedMemory validates the seed request and preloads one vector memory record.
// SeedMemory 用于校验灌库请求并预热一条向量记忆记录。
func (s *Server) SeedMemory(ctx context.Context, req *vmmv1.SeedMemoryRequest) (*vmmv1.SeedMemoryResponse, error) {
	if s.seedMemory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	normalizeSeedMemoryRequest(req)
	if err := s.validate.ValidateSeedMemory(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.seedTimeout)
	defer cancel()
	result, err := s.seedMemory.Execute(ctx, usecase.SeedMemoryCommand{
		UserID:     req.GetUserId(),
		ProjectID:  req.GetProjectId(),
		MemoryText: req.GetMemoryText(),
		SpaceID:    req.GetSpaceId(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.SeedMemoryResponse{
		Accepted: result.Accepted,
		MemoryId: result.MemoryID,
		TraceId:  trace.IDFromContext(ctx),
	}, nil
}

// withTimeout wraps one RPC context with a configured timeout when the timeout is positive.
// withTimeout 用于在超时为正数时给 RPC 上下文包一层配置化超时。
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// preferredLanguage extracts the strongest language tag from the incoming preference string.
// preferredLanguage 用于从传入的语言偏好字符串中提取优先级最高的语言标签。
func preferredLanguage(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	first := strings.Split(header, ",")[0]
	first = strings.TrimSpace(first)
	first = strings.Split(first, ";")[0]
	return first
}

// toRawMessagesDomain converts the legacy protobuf snapshot into domain raw messages for the normalizer.
// toRawMessagesDomain 用于把旧版 protobuf 快照转换成供 normalizer 使用的领域原始消息。
func toRawMessagesDomain(items []*vmmv1.RawMessage) []logicdomain.RawMessage {
	out := make([]logicdomain.RawMessage, 0, len(items))
	for _, item := range items {
		toolCalls := make([]logicdomain.ToolCall, 0, len(item.GetToolCalls()))
		for _, toolCall := range item.GetToolCalls() {
			toolCalls = append(toolCalls, logicdomain.ToolCall{ID: toolCall.GetId(), Type: toolCall.GetType()})
		}
		var content any = json.RawMessage(item.GetContentJson())
		if strings.TrimSpace(item.GetContentJson()) == "" {
			content = ""
		}
		var meta map[string]any
		if strings.TrimSpace(item.GetMetaJson()) != "" {
			_ = json.Unmarshal([]byte(item.GetMetaJson()), &meta)
		}
		out = append(out, logicdomain.RawMessage{
			Role:      item.GetRole(),
			Content:   content,
			ToolCalls: toolCalls,
			Meta:      meta,
		})
	}
	return out
}

// toAsyncPostActionCommand converts the new protobuf contract into the legacy persistence command reused by the use case.
// toAsyncPostActionCommand 用于把新的 protobuf 契约转换成用例复用的旧版持久化命令。
func toAsyncPostActionCommand(req *vmmv1.PostActionRequest) usecase.PostActionCommand {
	raw := make([]logicdomain.RawMessage, 0, len(req.GetTimeline())+2)
	raw = append(raw, logicdomain.RawMessage{Role: "user", Content: req.GetUserContent()})
	for _, item := range req.GetTimeline() {
		raw = append(raw, logicdomain.RawMessage{Role: item.GetType(), Content: item.GetContent()})
	}
	raw = append(raw, logicdomain.RawMessage{Role: "assistant", Content: req.GetAssistantContent()})
	return usecase.PostActionCommand{
		SessionID:           req.GetSessionId(),
		UserID:              req.GetUserId(),
		TeamID:              req.GetTeamId(),
		SpaceID:             req.GetSpaceId(),
		ProjectID:           req.GetProjectId(),
		RawMessagesSnapshot: raw,
		SkipNoiseGate:       len(req.GetTimeline()) > 0,
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

// clonePostActionRequest copies the new post-action request so normalization, cleaning, and logging can operate on independent instances.
// clonePostActionRequest 用于复制新的 post-action 请求，让规范化、清洗和日志输出可以在独立实例上进行。
func clonePostActionRequest(req *vmmv1.PostActionRequest) *vmmv1.PostActionRequest {
	if req == nil {
		return nil
	}
	cloned := &vmmv1.PostActionRequest{
		SessionId:        req.GetSessionId(),
		UserId:           req.GetUserId(),
		TeamId:           req.GetTeamId(),
		SpaceId:          req.GetSpaceId(),
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

// logAsyncPostActionReceipt writes one accepted text-only payload snapshot into the runtime logger for local debugging.
// logAsyncPostActionReceipt 用于把某个已接收的纯文本载荷快照写入运行时日志，方便本地调试。
func (s *Server) logAsyncPostActionReceipt(traceID, message string, req *vmmv1.PostActionRequest) {
	if s == nil || s.logger == nil {
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
