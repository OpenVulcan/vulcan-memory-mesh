// validation.go implements transport validation for the inbound gRPC adapter.
// validation.go 用于实现入站 gRPC 适配层的传输校验。
package grpcapi

import (
	"fmt"
	"strings"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"google.golang.org/protobuf/proto"
)

// RequestValidator performs lightweight transport validation without introducing a heavy generic validator.
// RequestValidator 用于在不引入重型通用校验库的前提下执行轻量传输层校验。
type RequestValidator struct{}

// NewRequestValidator creates the gRPC transport validator shared by unary handlers.
// NewRequestValidator 用于创建 gRPC 一元处理器共享的传输层校验器。
func NewRequestValidator() *RequestValidator { return &RequestValidator{} }

// NormalizePreCheckRequest trims the text-only pre-check payload before it reaches the use-case layer.
// NormalizePreCheckRequest 用于在进入用例层前裁剪纯文本 pre-check 载荷。
func NormalizePreCheckRequest(req *vmmv1.PreCheckRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	req.UserContent = textutil.CleanConversationText(req.GetUserContent())
}

// NormalizePostActionRequest trims the new text-only post-action payload before storage-oriented sanitization runs.
// NormalizePostActionRequest 用于在存储型清洗开始前裁剪新的纯文本 post-action 载荷。
func NormalizePostActionRequest(req *vmmv1.PostActionRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	req.UserContent = strings.TrimSpace(req.GetUserContent())
	req.AssistantContent = strings.TrimSpace(req.GetAssistantContent())
	for _, item := range req.GetTimeline() {
		if item == nil {
			continue
		}
		item.Type = strings.ToLower(strings.TrimSpace(item.GetType()))
		item.Content = strings.TrimSpace(item.GetContent())
	}
}

// NormalizeResolveProjectRequest trims the project reference before hierarchy resolution starts.
// NormalizeResolveProjectRequest 用于在层级解析开始前裁剪项目引用。
func NormalizeResolveProjectRequest(req *vmmv1.ResolveProjectRequest) {
	if req == nil {
		return
	}
	req.ProjectRef = strings.TrimSpace(req.GetProjectRef())
}

// NormalizeEnsureProjectRequest trims the canonical project path before hierarchy mutation checks start.
// NormalizeEnsureProjectRequest 用于在层级变更校验开始前裁剪标准项目路径。
func NormalizeEnsureProjectRequest(req *vmmv1.EnsureProjectRequest) {
	if req == nil {
		return
	}
	req.ProjectPath = strings.TrimSpace(req.GetProjectPath())
}

// NormalizeDeleteProjectRequest trims the canonical project path before delete confirmation checks start.
// NormalizeDeleteProjectRequest 用于在删除确认检查开始前裁剪标准项目路径。
func NormalizeDeleteProjectRequest(req *vmmv1.DeleteProjectRequest) {
	if req == nil {
		return
	}
	req.ProjectPath = strings.TrimSpace(req.GetProjectPath())
}

// NormalizeMigrateProjectRequest trims both project paths before migration checks start.
// NormalizeMigrateProjectRequest 用于在迁移检查开始前裁剪源/目标项目路径。
func NormalizeMigrateProjectRequest(req *vmmv1.MigrateProjectRequest) {
	if req == nil {
		return
	}
	req.SourceProjectPath = strings.TrimSpace(req.GetSourceProjectPath())
	req.TargetProjectPath = strings.TrimSpace(req.GetTargetProjectPath())
}

// NormalizeResolveUserRequest trims the user reference before resolution or creation starts.
// NormalizeResolveUserRequest 用于在解析或创建用户前裁剪用户引用。
func NormalizeResolveUserRequest(req *vmmv1.ResolveUserRequest) {
	if req == nil {
		return
	}
	req.UserRef = strings.TrimSpace(req.GetUserRef())
}

// NormalizeDeleteUserRequest trims the user reference and confirmation code before protected deletion starts.
// NormalizeDeleteUserRequest 用于在受保护删除开始前裁剪用户引用和确认码。
func NormalizeDeleteUserRequest(req *vmmv1.DeleteUserRequest) {
	if req == nil {
		return
	}
	req.UserRef = strings.TrimSpace(req.GetUserRef())
	req.ConfirmationCode = strings.TrimSpace(req.GetConfirmationCode())
}

// NormalizeGetProfileNodesRequest normalizes the target-scoped profile query payload before the use case resolves one concrete target.
// NormalizeGetProfileNodesRequest 用于在用例解析具体目标前，规范化目标化画像查询载荷。
func NormalizeGetProfileNodesRequest(req *vmmv1.GetProfileNodesRequest) {
	if req == nil {
		return
	}
}

// NormalizeGetProfileBundleRequest fills the default explanation behavior so FULL mode includes P/L/W help text unless callers explicitly disable it.
// NormalizeGetProfileBundleRequest 用于补齐 bundle 请求的默认说明行为，让 FULL 模式在调用方未显式关闭时默认带上 P/L/W 帮助说明。
func NormalizeGetProfileBundleRequest(req *vmmv1.GetProfileBundleRequest) {
	if req == nil {
		return
	}
	if req.IncludeExplanation != nil {
		return
	}
	switch req.GetMode() {
	case vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL:
		req.IncludeExplanation = proto.Bool(true)
	case vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_SPLIT:
		req.IncludeExplanation = proto.Bool(false)
	}
}

// NormalizeApplyProfileInstructionRequest trims the explicit manual profile instruction before the reviewer flow begins.
// NormalizeApplyProfileInstructionRequest 用于在手工画像评审流程开始前裁剪显式画像指令。
func NormalizeApplyProfileInstructionRequest(req *vmmv1.ApplyProfileInstructionRequest) {
	if req == nil {
		return
	}
	req.Instruction = strings.TrimSpace(req.GetInstruction())
}

// NormalizeSearchMemoryEventsRequest trims each query string before the memory-query use case validates and executes the AI-facing search request.
// NormalizeSearchMemoryEventsRequest 用于在记忆查询用例校验和执行 AI 面向的搜索请求前，先裁剪每一条查询字符串。
func NormalizeSearchMemoryEventsRequest(req *vmmv1.SearchMemoryEventsRequest) {
	if req == nil {
		return
	}
	for idx, query := range req.GetQueries() {
		req.Queries[idx] = strings.TrimSpace(query)
	}
}

// NormalizeGetTurnDetailsRequest keeps the turn-detail lookup hook in place even though the current request only carries numeric ids.
// NormalizeGetTurnDetailsRequest 用于为 turn 详情查询保留规范化入口，虽然当前请求只包含数字 id。
func NormalizeGetTurnDetailsRequest(req *vmmv1.GetTurnDetailsRequest) {
	if req == nil {
		return
	}
}

// NormalizeWriteMemoriesRequest trims the direct-write payload before the use case applies soft idempotency and lifecycle defaults.
// NormalizeWriteMemoriesRequest 用于在用例执行软幂等和生命周期补齐前，裁剪主动写记忆载荷。
func NormalizeWriteMemoriesRequest(req *vmmv1.WriteMemoriesRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	for _, item := range req.GetItems() {
		if item == nil {
			continue
		}
		item.Abstract = strings.TrimSpace(item.GetAbstract())
		item.Details = strings.TrimSpace(item.GetDetails())
	}
}

// NormalizeScratchpadUpsertRequest trims the isolated DWM upsert payload while preserving the caller's single-item vs batch-item intent for later conflict validation.
// NormalizeScratchpadUpsertRequest 用于裁剪隔离 DWM upsert 载荷，并保留调用方原始的单项/批量意图，供后续冲突校验使用。
func NormalizeScratchpadUpsertRequest(req *vmmv1.ScratchpadUpsertRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	req.PlanName = strings.TrimSpace(req.GetPlanName())
	singleKey := strings.TrimSpace(req.GetKey())
	singleValue := strings.TrimSpace(req.GetValue())
	if req.Key != nil {
		req.Key = proto.String(singleKey)
	}
	if req.Value != nil {
		req.Value = proto.String(singleValue)
	}
	for _, item := range req.GetItems() {
		if item == nil {
			continue
		}
		item.Key = strings.TrimSpace(item.GetKey())
		item.Value = strings.TrimSpace(item.GetValue())
	}
}

// NormalizeScratchpadDeleteRequest trims the isolated DWM delete payload while preserving the caller's single-key vs batch-key intent for later conflict validation.
// NormalizeScratchpadDeleteRequest 用于裁剪隔离 DWM delete 载荷，并保留调用方原始的单键/批量意图，供后续冲突校验使用。
func NormalizeScratchpadDeleteRequest(req *vmmv1.ScratchpadDeleteRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	req.PlanName = strings.TrimSpace(req.GetPlanName())
	singleKey := strings.TrimSpace(req.GetKey())
	if req.Key != nil {
		req.Key = proto.String(singleKey)
	}
	for idx, key := range req.GetKeys() {
		req.Keys[idx] = strings.TrimSpace(key)
	}
}

// NormalizeScratchpadGetRequest trims the isolated DWM get payload before validation and use-case execution begin.
// NormalizeScratchpadGetRequest 用于在校验和用例执行开始前裁剪隔离 DWM get 载荷。
func NormalizeScratchpadGetRequest(req *vmmv1.ScratchpadGetRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
	for idx, key := range req.GetKeys() {
		req.Keys[idx] = strings.TrimSpace(key)
	}
}

// NormalizeScratchpadListKeysRequest trims the isolated DWM list-keys payload before validation and use-case execution begin.
// NormalizeScratchpadListKeysRequest 用于在校验和用例执行开始前裁剪隔离 DWM list-keys 载荷。
func NormalizeScratchpadListKeysRequest(req *vmmv1.ScratchpadListKeysRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
}

// NormalizeScratchpadCleanRequest trims the isolated DWM clean payload before validation starts.
// NormalizeScratchpadCleanRequest 用于在校验开始前裁剪隔离 DWM clean 载荷。
func NormalizeScratchpadCleanRequest(req *vmmv1.ScratchpadCleanRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
}

// NormalizeChatCompactRequest trims the compact acknowledgement payload before the scope resolver runs.
// NormalizeChatCompactRequest 用于在范围解析执行前裁剪 compact 确认载荷。
func NormalizeChatCompactRequest(req *vmmv1.ChatCompactRequest) {
	if req == nil {
		return
	}
	req.SessionId = strings.TrimSpace(req.GetSessionId())
}

// ValidatePreCheck validates the pre-check RPC request before the scope resolver interceptor runs.
// ValidatePreCheck 用于在范围解析拦截器执行前校验 pre-check RPC 请求。
func (v *RequestValidator) ValidatePreCheck(req *vmmv1.PreCheckRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "pre_check", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if err := requireString("user_content", req.GetUserContent(), 16000); err != nil {
		return err
	}
	return nil
}

// ValidateChatCompact validates the compact acknowledgement RPC request before the scope resolver interceptor runs.
// ValidateChatCompact 用于在范围解析拦截器执行前校验 compact 确认 RPC 请求。
func (v *RequestValidator) ValidateChatCompact(req *vmmv1.ChatCompactRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "chat_compact", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	return nil
}

// ValidatePostAction validates the new text-only post-action request before synchronous persistence begins.
// ValidatePostAction 用于在同步持久化开始前校验新的纯文本 post-action 请求。
func (v *RequestValidator) ValidatePostAction(req *vmmv1.PostActionRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "post_action", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if err := requireString("user_content", req.GetUserContent(), 16000); err != nil {
		return err
	}
	if err := requireString("assistant_content", req.GetAssistantContent(), 16000); err != nil {
		return err
	}
	for idx, item := range req.GetTimeline() {
		if item == nil {
			return logicdomain.ValidationError{Field: fmt.Sprintf("timeline[%d]", idx), Message: "item is required"}
		}
		if err := requireOneOf(fmt.Sprintf("timeline[%d].type", idx), item.GetType(), "user", "assistant"); err != nil {
			return err
		}
		if err := requireString(fmt.Sprintf("timeline[%d].content", idx), item.GetContent(), 16000); err != nil {
			return err
		}
	}
	return nil
}

// ValidateResolveProject checks the project reference before one hierarchy lookup executes.
// ValidateResolveProject 用于在层级查询执行前校验项目引用。
func (v *RequestValidator) ValidateResolveProject(req *vmmv1.ResolveProjectRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "resolve_project", Message: "is required"}
	}
	return requireString("project_ref", req.GetProjectRef(), 256)
}

// ValidateEnsureProject checks the project path before one hierarchy creation flow starts.
// ValidateEnsureProject 用于在层级创建流程开始前校验项目路径。
func (v *RequestValidator) ValidateEnsureProject(req *vmmv1.EnsureProjectRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "ensure_project", Message: "is required"}
	}
	return requireString("project_path", req.GetProjectPath(), 256)
}

// ValidateDeleteProject checks the project path before one hierarchy delete flow starts.
// ValidateDeleteProject 用于在层级删除流程开始前校验项目路径。
func (v *RequestValidator) ValidateDeleteProject(req *vmmv1.DeleteProjectRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "delete_project", Message: "is required"}
	}
	return requireString("project_path", req.GetProjectPath(), 256)
}

// ValidateMigrateProject checks both source and target project paths before one migration flow starts.
// ValidateMigrateProject 用于在迁移流程开始前校验源/目标项目路径。
func (v *RequestValidator) ValidateMigrateProject(req *vmmv1.MigrateProjectRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "migrate_project", Message: "is required"}
	}
	if err := requireString("source_project_path", req.GetSourceProjectPath(), 256); err != nil {
		return err
	}
	if err := requireString("target_project_path", req.GetTargetProjectPath(), 256); err != nil {
		return err
	}
	return nil
}

// ValidateResolveUser checks the user reference before one user resolution flow starts.
// ValidateResolveUser 用于在用户解析流程开始前校验用户引用。
func (v *RequestValidator) ValidateResolveUser(req *vmmv1.ResolveUserRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "resolve_user", Message: "is required"}
	}
	return requireString("user_ref", req.GetUserRef(), 256)
}

// ValidateDeleteUser checks the user reference before one protected delete flow starts.
// ValidateDeleteUser 用于在受保护删除流程开始前校验用户引用。
func (v *RequestValidator) ValidateDeleteUser(req *vmmv1.DeleteUserRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "delete_user", Message: "is required"}
	}
	return requireString("user_ref", req.GetUserRef(), 256)
}

// ValidateGetProfileNodes checks the single-target profile query contract and keeps the public RPC restricted to active-node lookups only.
// ValidateGetProfileNodes 用于校验单目标画像查询契约，并保持公开 RPC 只暴露 active 节点查询能力。
func (v *RequestValidator) ValidateGetProfileNodes(req *vmmv1.GetProfileNodesRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "get_profile_nodes", Message: "is required"}
	}
	if req.GetTarget() == vmmv1.ProfileTarget_PROFILE_TARGET_UNSPECIFIED {
		return logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	switch req.GetTarget() {
	case vmmv1.ProfileTarget_PROFILE_TARGET_USER:
		if req.GetUserId() == 0 {
			return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
		}
	case vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT,
		vmmv1.ProfileTarget_PROFILE_TARGET_TEAM,
		vmmv1.ProfileTarget_PROFILE_TARGET_SPACE:
		if req.GetProjectId() == 0 {
			return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
		}
	default:
		return logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	return nil
}

// ValidateGetProfileBundle checks the user/project bundle contract and keeps the public RPC restricted to deterministic scope composition only.
// ValidateGetProfileBundle 用于校验 user/project 组合画像契约，并保持公开 RPC 只暴露确定性的 scope 拼接能力。
func (v *RequestValidator) ValidateGetProfileBundle(req *vmmv1.GetProfileBundleRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "get_profile_bundle", Message: "is required"}
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	switch req.GetMode() {
	case vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL,
		vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_SPLIT:
		return nil
	default:
		return logicdomain.ValidationError{Field: "mode", Message: "must be full or split"}
	}
}

// ValidateApplyProfileInstruction checks the single-target manual profile instruction payload before one reviewer call starts.
// ValidateApplyProfileInstruction 用于在单目标手工画像评审调用开始前校验输入载荷。
func (v *RequestValidator) ValidateApplyProfileInstruction(req *vmmv1.ApplyProfileInstructionRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "apply_profile_instruction", Message: "is required"}
	}
	if err := v.ValidateGetProfileNodes(&vmmv1.GetProfileNodesRequest{
		Target:    req.GetTarget(),
		UserId:    req.GetUserId(),
		ProjectId: req.GetProjectId(),
	}); err != nil {
		return err
	}
	return requireString("instruction", req.GetInstruction(), 16000)
}

// ValidateSearchMemoryEvents checks the simple query-list payload before hierarchy resolution, embedding, and vector recall begin.
// ValidateSearchMemoryEvents 用于在层级解析、embedding 和向量召回开始前校验简单查询列表载荷。
func (v *RequestValidator) ValidateSearchMemoryEvents(req *vmmv1.SearchMemoryEventsRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "search_memory_events", Message: "is required"}
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(req.GetQueries()) == 0 {
		return logicdomain.ValidationError{Field: "queries", Message: "must contain at least one query"}
	}
	if len(req.GetQueries()) > 16 {
		return logicdomain.ValidationError{Field: "queries", Message: "must contain at most 16 queries"}
	}
	for idx, query := range req.GetQueries() {
		if err := requireString(fmt.Sprintf("queries[%d]", idx), query, 4000); err != nil {
			return err
		}
	}
	if req.GetTopK() > 64 {
		return logicdomain.ValidationError{Field: "top_k", Message: "must be <= 64"}
	}
	return nil
}

// ValidateGetTurnDetails checks the turn-detail lookup payload before relational reads begin.
// ValidateGetTurnDetails 用于在关系读取开始前校验 turn 详情查询载荷。
func (v *RequestValidator) ValidateGetTurnDetails(req *vmmv1.GetTurnDetailsRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "get_turn_details", Message: "is required"}
	}
	if len(req.GetTurnIds()) == 0 {
		return logicdomain.ValidationError{Field: "turn_ids", Message: "must contain at least one id"}
	}
	if len(req.GetTurnIds()) > 256 {
		return logicdomain.ValidationError{Field: "turn_ids", Message: "must contain at most 256 ids"}
	}
	for idx, turnID := range req.GetTurnIds() {
		if turnID == 0 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("turn_ids[%d]", idx), Message: "must be a numeric id"}
		}
	}
	return nil
}

// ValidateWriteMemories checks the direct-write memory payload before scope resolution and embedding begin.
// ValidateWriteMemories 用于在范围解析和 embedding 开始前校验主动写记忆载荷。
func (v *RequestValidator) ValidateWriteMemories(req *vmmv1.WriteMemoriesRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "write_memories", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(req.GetItems()) == 0 {
		return logicdomain.ValidationError{Field: "items", Message: "must contain at least one item"}
	}
	if len(req.GetItems()) > 32 {
		return logicdomain.ValidationError{Field: "items", Message: "must contain at most 32 items"}
	}
	for idx, item := range req.GetItems() {
		if item == nil {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d]", idx), Message: "item is required"}
		}
		if item.GetScopeLevel() < 0 || item.GetScopeLevel() > 3 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].scope_level", idx), Message: "must be one supported memory scope"}
		}
		if err := requireString(fmt.Sprintf("items[%d].abstract", idx), item.GetAbstract(), 16000); err != nil {
			return err
		}
		if err := requireString(fmt.Sprintf("items[%d].details", idx), item.GetDetails(), 64000); err != nil {
			return err
		}
		if item.GetCategory() < 0 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].category", idx), Message: "must be >= 0"}
		}
		if item.GetPriority() < 0 || item.GetPriority() > 3 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].priority", idx), Message: "must be one supported memory priority"}
		}
		if item.GetMemoryLevel() < 0 || item.GetMemoryLevel() > 4 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].memory_level", idx), Message: "must be one supported memory level"}
		}
	}
	return nil
}

// ValidateScratchpadUpsert checks the isolated DWM upsert payload before any plan guard or SQL mutation begins.
// ValidateScratchpadUpsert 用于在任何计划守卫或 SQL 变更开始前校验隔离 DWM upsert 载荷。
func (v *RequestValidator) ValidateScratchpadUpsert(req *vmmv1.ScratchpadUpsertRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "scratchpad_upsert", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if err := requireString("plan_name", req.GetPlanName(), 128); err != nil {
		return err
	}
	hasBatch := len(req.GetItems()) > 0
	hasSingleKey := req.Key != nil
	hasSingleValue := req.Value != nil
	if hasBatch && (hasSingleKey || hasSingleValue) {
		return logicdomain.ValidationError{Field: "items", Message: "must not be combined with key/value"}
	}
	if !hasBatch {
		if !hasSingleKey || !hasSingleValue {
			return logicdomain.ValidationError{Field: "key", Message: "key/value or items[] is required"}
		}
		if err := requireString("key", req.GetKey(), 128); err != nil {
			return err
		}
		if err := requireString("value", req.GetValue(), 16000); err != nil {
			return err
		}
		return nil
	}
	if len(req.GetItems()) > 32 {
		return logicdomain.ValidationError{Field: "items", Message: "must contain at most 32 items"}
	}
	for idx, item := range req.GetItems() {
		if item == nil {
			return logicdomain.ValidationError{Field: fmt.Sprintf("items[%d]", idx), Message: "item is required"}
		}
		if err := requireString(fmt.Sprintf("items[%d].key", idx), item.GetKey(), 128); err != nil {
			return err
		}
		if err := requireString(fmt.Sprintf("items[%d].value", idx), item.GetValue(), 16000); err != nil {
			return err
		}
	}
	return nil
}

// ValidateScratchpadDelete checks the isolated DWM delete payload before plan-guard evaluation begins.
// ValidateScratchpadDelete 用于在计划守卫开始前校验隔离 DWM delete 载荷。
func (v *RequestValidator) ValidateScratchpadDelete(req *vmmv1.ScratchpadDeleteRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "scratchpad_delete", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if err := requireString("plan_name", req.GetPlanName(), 128); err != nil {
		return err
	}
	hasBatch := len(req.GetKeys()) > 0
	hasSingleKey := req.Key != nil
	if hasBatch && hasSingleKey {
		return logicdomain.ValidationError{Field: "keys", Message: "must not be combined with key"}
	}
	if !hasBatch {
		if !hasSingleKey {
			return logicdomain.ValidationError{Field: "key", Message: "key or keys[] is required"}
		}
		if err := requireString("key", req.GetKey(), 128); err != nil {
			return err
		}
		return nil
	}
	if len(req.GetKeys()) > 64 {
		return logicdomain.ValidationError{Field: "keys", Message: "must contain at most 64 keys"}
	}
	for idx, key := range req.GetKeys() {
		if err := requireString(fmt.Sprintf("keys[%d]", idx), key, 128); err != nil {
			return err
		}
	}
	return nil
}

// ValidateScratchpadGet checks the isolated DWM get payload before one read-only reload starts.
// ValidateScratchpadGet 用于在只读 reload 开始前校验隔离 DWM get 载荷。
func (v *RequestValidator) ValidateScratchpadGet(req *vmmv1.ScratchpadGetRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "scratchpad_get", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(req.GetKeys()) > 64 {
		return logicdomain.ValidationError{Field: "keys", Message: "must contain at most 64 keys"}
	}
	for idx, key := range req.GetKeys() {
		if err := requireString(fmt.Sprintf("keys[%d]", idx), key, 128); err != nil {
			return err
		}
	}
	return nil
}

// ValidateScratchpadListKeys checks the isolated DWM list-keys payload before one read-only key-catalog reload starts.
// ValidateScratchpadListKeys 用于在只读 key 目录回载开始前校验隔离 DWM list-keys 载荷。
func (v *RequestValidator) ValidateScratchpadListKeys(req *vmmv1.ScratchpadListKeysRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "scratchpad_list_keys", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	return nil
}

// ValidateScratchpadClean checks the isolated DWM clean payload before one explicit full-scope purge begins.
// ValidateScratchpadClean 用于在显式整范围清空开始前校验隔离 DWM clean 载荷。
func (v *RequestValidator) ValidateScratchpadClean(req *vmmv1.ScratchpadCleanRequest) error {
	if req == nil {
		return logicdomain.ValidationError{Field: "scratchpad_clean", Message: "is required"}
	}
	if err := requireString("session_id", req.GetSessionId(), 128); err != nil {
		return err
	}
	if req.GetUserId() == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if req.GetProjectId() == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	return nil
}

// requireString enforces one non-empty bounded string field.
// requireString 用于校验单个非空且长度受限的字符串字段。
func requireString(field, value string, max int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return logicdomain.ValidationError{Field: field, Message: "is required"}
	}
	return maxString(field, value, max)
}

// maxString enforces the maximum length of one optional string field.
// maxString 用于校验单个可选字符串字段的最大长度。
func maxString(field, value string, max int) error {
	if max <= 0 {
		return nil
	}
	if len(value) > max {
		return logicdomain.ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", max)}
	}
	return nil
}

// requireOneOf enforces that one string field belongs to a small explicit allowlist.
// requireOneOf 用于校验单个字符串字段必须属于一个明确的小型允许列表。
func requireOneOf(field, value string, allowed ...string) error {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return logicdomain.ValidationError{Field: field, Message: "is required"}
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return logicdomain.ValidationError{Field: field, Message: fmt.Sprintf("must be one of [%s]", strings.Join(allowed, " "))}
}
