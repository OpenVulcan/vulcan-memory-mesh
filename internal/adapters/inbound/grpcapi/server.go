// server.go keeps the gRPC server wiring, shared transport helpers, and common conversion utilities used by the inbound adapter.
// server.go 用于承载入站 gRPC 适配层共享的服务装配、传输辅助函数与通用转换逻辑。
package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
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
	Scratchpad        usecase.ScratchpadExecutor
	ChatCompact       usecase.ChatCompactExecutor
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
	scratchpad       usecase.ScratchpadExecutor
	chatCompact      usecase.ChatCompactExecutor
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
		deps.Logger = logx.New(os.Stdout, logx.Config{
			Level:         "info",
			Format:        "text",
			DebugPayloads: deps.DebugRPCPayloads,
		})
	}
	if deps.Validator == nil {
		deps.Validator = NewRequestValidator()
	}
	return &Server{
		workspace:        deps.Workspace,
		profiles:         deps.Profiles,
		memory:           deps.Memory,
		scratchpad:       deps.Scratchpad,
		chatCompact:      deps.ChatCompact,
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

// toProtoScratchpadStatus converts the internal DWM business status into the transport enum exposed by gRPC callers.
// toProtoScratchpadStatus 用于把内部 DWM 业务状态转换成 gRPC 调用方可见的传输层枚举。
func toProtoScratchpadStatus(status logicdomain.ScratchpadStatus) vmmv1.ScratchpadStatus {
	switch status {
	case logicdomain.ScratchpadStatusSuccess:
		return vmmv1.ScratchpadStatus_SCRATCHPAD_STATUS_SUCCESS
	case logicdomain.ScratchpadStatusFailed:
		return vmmv1.ScratchpadStatus_SCRATCHPAD_STATUS_FAILED
	default:
		return vmmv1.ScratchpadStatus_SCRATCHPAD_STATUS_UNSPECIFIED
	}
}

// toProtoScratchpadItems converts isolated DWM key/value anchors into the compact protobuf transport shape returned by the scratchpad get RPC.
// toProtoScratchpadItems 用于把隔离 DWM key/value 锚点转换成 scratchpad get RPC 返回的紧凑 protobuf 结构。
func toProtoScratchpadItems(items []logicdomain.ScratchpadItem) []*vmmv1.ScratchpadItem {
	out := make([]*vmmv1.ScratchpadItem, 0, len(items))
	for _, item := range items {
		out = append(out, &vmmv1.ScratchpadItem{
			Key:   item.Key,
			Value: item.Value,
		})
	}
	return out
}

// collectScratchpadUpsertItems reconstructs the deterministic DWM item batch from either the explicit batch payload or the validated single-item shorthand.
// collectScratchpadUpsertItems 用于从显式批量载荷或已校验的单项简写中重建确定性的 DWM item 批次。
func collectScratchpadUpsertItems(req *vmmv1.ScratchpadUpsertRequest) []logicdomain.ScratchpadItem {
	if req == nil {
		return nil
	}
	if len(req.GetItems()) > 0 {
		items := make([]logicdomain.ScratchpadItem, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			if item == nil {
				continue
			}
			items = append(items, logicdomain.ScratchpadItem{
				Key:   item.GetKey(),
				Value: item.GetValue(),
			})
		}
		return items
	}
	return []logicdomain.ScratchpadItem{{
		Key:   req.GetKey(),
		Value: req.GetValue(),
	}}
}

// collectScratchpadDeleteKeys reconstructs the deterministic DWM delete batch from either the explicit key list or the validated single-key shorthand.
// collectScratchpadDeleteKeys 用于从显式 key 列表或已校验的单键简写中重建确定性的 DWM 删除批次。
func collectScratchpadDeleteKeys(req *vmmv1.ScratchpadDeleteRequest) []string {
	if req == nil {
		return nil
	}
	if len(req.GetKeys()) > 0 {
		return append([]string(nil), req.GetKeys()...)
	}
	return []string{req.GetKey()}
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
		ProfileDate:      logicdomain.NormalizeLegacyDisplayDateWithFallback(node.ProfileDate, node.ProfileDateAnchorAt, node.CreatedAt),
		ExpiresTimestamp: expiresTimestamp,
		LevelReason:      node.LevelReason,
		SourceKind:       toProtoProfileSourceKind(node.SourceKind),
		SourceId:         node.SourceID,
	}
}

// toTurnDetailEntry converts one detailed turn result into the compact AI-facing transport shape used by the turn-detail lookup RPC.
// toTurnDetailEntry 用于把一条 turn 详情结果转换成 turn 查询 RPC 使用的紧凑 AI 友好传输结构。
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
		TurnId:          turn.Turn.ID,
		UserQuestion:    turn.UserContent,
		Timeline:        timeline,
		AssistantAnswer: turn.AssistantContent,
		Detail:          turn.Turn.Details,
		PreviousTurnIds: append([]uint64(nil), turn.PreviousTurnIDs...),
		NextTurnIds:     append([]uint64(nil), turn.NextTurnIDs...),
	}
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

// memoryCategoryLabel converts one internal memory category id into the stable English label exposed to AI tools so callers do not need to understand internal numeric enums.
// memoryCategoryLabel 用于把内部记忆分类编号转换成暴露给 AI 工具的稳定英文标签，避免调用方理解内部数字枚举。
func memoryCategoryLabel(category int) string {
	switch category {
	case logicdomain.MemoryNodeCategoryArchitectureDecision:
		return "architecture_decision"
	case logicdomain.MemoryNodeCategoryTechSpecAPI:
		return "tech_spec_api"
	case logicdomain.MemoryNodeCategoryBusinessLogic:
		return "business_logic"
	case logicdomain.MemoryNodeCategoryRequirementTODO:
		return "requirement_todo"
	case logicdomain.MemoryNodeCategoryProjectContext:
		return "project_context"
	case logicdomain.MemoryNodeCategoryLogicalBugDebt:
		return "logical_bug_debt"
	case logicdomain.MemoryNodeCategorySecurityPolicy:
		return "security_policy"
	default:
		return "general"
	}
}

// normalizeTransportMemoryScopeLevel maps the compact numeric concept sent by AI tools into the internal scope enum while preserving -1 for omitted or unsupported values so the use case can still apply defaults.
// normalizeTransportMemoryScopeLevel 用于把 AI 工具传来的紧凑数字概念值映射成内部 scope 枚举；若省略或不支持则保留 -1，让用例层继续补默认值。
func normalizeTransportMemoryScopeLevel(scopeLevel int32) int {
	switch scopeLevel {
	case 1:
		return logicdomain.MemoryScopeLevelSession
	case 2:
		return logicdomain.MemoryScopeLevelProject
	case 3:
		return logicdomain.MemoryScopeLevelUser
	default:
		return -1
	}
}

// normalizeTransportMemoryPriority maps the compact numeric P-level sent by AI tools into the internal priority enum while preserving -1 for omitted values.
// normalizeTransportMemoryPriority 用于把 AI 工具传来的紧凑数字 P 级别映射成内部 priority 枚举；省略时保留 -1。
func normalizeTransportMemoryPriority(priority int32) int {
	switch priority {
	case 1:
		return logicdomain.MemoryPriorityP0
	case 2:
		return logicdomain.MemoryPriorityP1
	case 3:
		return logicdomain.MemoryPriorityP2
	default:
		return -1
	}
}

// normalizeTransportMemoryLevel maps the compact numeric L-level sent by AI tools into the internal lifecycle enum while preserving -1 for omitted values.
// normalizeTransportMemoryLevel 用于把 AI 工具传来的紧凑数字 L 级别映射成内部生命周期枚举；省略时保留 -1。
func normalizeTransportMemoryLevel(level int32) int {
	switch level {
	case 1:
		return logicdomain.MemoryLevelSession
	case 2:
		return logicdomain.MemoryLevelPhase
	case 3:
		return logicdomain.MemoryLevelStable
	case 4:
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
