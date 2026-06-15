// unsupported.go provides explicit placeholder methods so the PostgreSQL combined-store adapter can enter the runtime wiring path before every relational workflow is fully ported.
// unsupported.go 用于提供显式占位方法，让 PostgreSQL 组合库适配器在全部关系工作流完成移植前，先进入运行时装配路径。
package vldb_postgres

import (
	"context"
	"fmt"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// unsupportedOperations groups the not-yet-ported PostgreSQL methods so the shared store can satisfy runtime type assertions with explicit errors instead of silent partial behavior.
// unsupportedOperations 用于集中尚未迁移完成的 PostgreSQL 方法，让共享 Store 在满足运行时类型断言时返回显式错误，而不是产生静默的半实现行为。
type unsupportedOperations struct{}

// unsupportedOperationError renders one stable explicit error for PostgreSQL workflows that still need a real implementation.
// unsupportedOperationError 用于为尚待实现的 PostgreSQL 工作流生成稳定且显式的错误信息。
func unsupportedOperationError(name string) error {
	return fmt.Errorf("postgres combined store %s is not implemented yet", name)
}

// Upsert rejects PostgreSQL vector writes until the combined-store write path is fully ported.
// Upsert 用于在 PostgreSQL 组合库完整写入路径落地前，显式拒绝向量写入调用。
func (unsupportedOperations) Upsert(context.Context, logicdomain.MemoryRecord) error {
	return unsupportedOperationError("Upsert")
}

// Search rejects PostgreSQL vector recall until the combined-store search path is fully ported.
// Search 用于在 PostgreSQL 组合库完整召回路径落地前，显式拒绝向量检索调用。
func (unsupportedOperations) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, unsupportedOperationError("Search")
}

// DeleteByFilter rejects PostgreSQL vector cleanup until the combined-store destructive flows are fully ported.
// DeleteByFilter 用于在 PostgreSQL 组合库的清理流程完整落地前，显式拒绝按过滤条件删除向量。
func (unsupportedOperations) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	return 0, unsupportedOperationError("DeleteByFilter")
}

// DeleteByIDs rejects PostgreSQL vector-id cleanup until the combined-store destructive flows are fully ported.
// DeleteByIDs 用于在 PostgreSQL 组合库的清理流程完整落地前，显式拒绝按向量 ID 删除。
func (unsupportedOperations) DeleteByIDs(context.Context, []string) (uint64, error) {
	return 0, unsupportedOperationError("DeleteByIDs")
}

// AppendTurnRecord rejects PostgreSQL turn persistence until the relational write workflow is fully ported.
// AppendTurnRecord 用于在 PostgreSQL 关系写入工作流完整迁移前，显式拒绝 turn 持久化。
func (unsupportedOperations) AppendTurnRecord(context.Context, logicdomain.SessionRef, logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	return logicdomain.PersistedTurnRecord{}, unsupportedOperationError("AppendTurnRecord")
}

// LoadPendingSessionTurns rejects PostgreSQL pending-turn loading until the relational read workflow is fully ported.
// LoadPendingSessionTurns 用于在 PostgreSQL 关系读取工作流完整迁移前，显式拒绝 pending turn 加载。
func (unsupportedOperations) LoadPendingSessionTurns(context.Context, logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	return nil, unsupportedOperationError("LoadPendingSessionTurns")
}

// LoadRecentSessionTurns rejects PostgreSQL recent-turn loading until the relational read workflow is fully ported.
// LoadRecentSessionTurns 用于在 PostgreSQL 关系读取工作流完整迁移前，显式拒绝最近 turn 加载。
func (unsupportedOperations) LoadRecentSessionTurns(context.Context, logicdomain.SessionRef, int) ([]logicdomain.SessionTurnRecord, error) {
	return nil, unsupportedOperationError("LoadRecentSessionTurns")
}

// LoadRecentSessionHistory rejects PostgreSQL session-history loading until the relational read workflow is fully ported.
// LoadRecentSessionHistory 用于在 PostgreSQL 关系读取工作流完整迁移前，显式拒绝 session 历史加载。
func (unsupportedOperations) LoadRecentSessionHistory(context.Context, logicdomain.SessionRef, int) ([]logicdomain.SessionTurnRecord, error) {
	return nil, unsupportedOperationError("LoadRecentSessionHistory")
}

// LoadRecentDirectMemoryWrites rejects PostgreSQL recent direct-write loading until the relational read workflow is fully ported.
// LoadRecentDirectMemoryWrites 用于在 PostgreSQL 关系读取工作流完整迁移前，显式拒绝最近主动写记忆加载。
func (unsupportedOperations) LoadRecentDirectMemoryWrites(context.Context, logicdomain.SessionRef, time.Time, time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	return nil, unsupportedOperationError("LoadRecentDirectMemoryWrites")
}

// ListIdlePendingSessions rejects PostgreSQL idle-session scanning until the relational maintenance workflow is fully ported.
// ListIdlePendingSessions 用于在 PostgreSQL 关系维护工作流完整迁移前，显式拒绝空闲待处理 session 扫描。
func (unsupportedOperations) ListIdlePendingSessions(context.Context, time.Duration, int) ([]logicdomain.SessionRef, error) {
	return nil, unsupportedOperationError("ListIdlePendingSessions")
}

// LoadProfileTargets rejects PostgreSQL profile-target loading until the profile merge workflow is fully ported.
// LoadProfileTargets 用于在 PostgreSQL 画像合并工作流完整迁移前，显式拒绝画像目标快照加载。
func (unsupportedOperations) LoadProfileTargets(context.Context, logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	return logicdomain.ProfileTargetsSnapshot{}, unsupportedOperationError("LoadProfileTargets")
}

// LoadProfileReviewTargets rejects PostgreSQL profile-review loading until the profile review workflow is fully ported.
// LoadProfileReviewTargets 用于在 PostgreSQL 画像评审工作流完整迁移前，显式拒绝画像评审目标加载。
func (unsupportedOperations) LoadProfileReviewTargets(context.Context, logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	return logicdomain.ProfileReviewTargetsSnapshot{}, unsupportedOperationError("LoadProfileReviewTargets")
}

// ConvergeExpiredProfileNodes rejects PostgreSQL profile lifecycle convergence until the profile maintenance workflow is fully ported.
// ConvergeExpiredProfileNodes 用于在 PostgreSQL 画像生命周期维护完整迁移前，显式拒绝画像收敛流程。
func (unsupportedOperations) ConvergeExpiredProfileNodes(context.Context, int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	return nil, unsupportedOperationError("ConvergeExpiredProfileNodes")
}

// ReplaceRenderedProfiles rejects PostgreSQL rendered-profile replacement until the profile maintenance workflow is fully ported.
// ReplaceRenderedProfiles 用于在 PostgreSQL 画像生命周期维护完整迁移前，显式拒绝渲染画像回写。
func (unsupportedOperations) ReplaceRenderedProfiles(context.Context, logicdomain.RenderedProfileSet) error {
	return unsupportedOperationError("ReplaceRenderedProfiles")
}

// AdvanceSessionExtractWindow rejects PostgreSQL extract-window advancement until the relational maintenance workflow is fully ported.
// AdvanceSessionExtractWindow 用于在 PostgreSQL 关系维护工作流完整迁移前，显式拒绝提炼窗口推进。
func (unsupportedOperations) AdvanceSessionExtractWindow(context.Context, uint64, time.Time, time.Time) error {
	return unsupportedOperationError("AdvanceSessionExtractWindow")
}

// ApplyMemoryAdoption rejects PostgreSQL memory-adoption writes until the lifecycle update workflow is fully ported.
// ApplyMemoryAdoption 用于在 PostgreSQL 生命周期更新工作流完整迁移前，显式拒绝记忆采纳回写。
func (unsupportedOperations) ApplyMemoryAdoption(context.Context, logicdomain.SessionRef, []uint64, time.Time) error {
	return unsupportedOperationError("ApplyMemoryAdoption")
}

// ApplyTurnAnalysis rejects PostgreSQL analysis write-back until the post-action workflow is fully ported.
// ApplyTurnAnalysis 用于在 PostgreSQL post-action 写回工作流完整迁移前，显式拒绝分析结果回写。
func (unsupportedOperations) ApplyTurnAnalysis(context.Context, logicdomain.SessionRef, logicdomain.PersistedTurnRecord, logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	return logicdomain.TurnAnalysisApplyResult{}, unsupportedOperationError("ApplyTurnAnalysis")
}

// LoadTurnsByIDs rejects PostgreSQL turn detail loading until the turn lookup workflow is fully ported.
// LoadTurnsByIDs 用于在 PostgreSQL turn 详情读取工作流完整迁移前，显式拒绝按 ID 加载 turn。
func (unsupportedOperations) LoadTurnsByIDs(context.Context, []uint64) ([]logicdomain.SessionTurnRecord, error) {
	return nil, unsupportedOperationError("LoadTurnsByIDs")
}

// LoadTurnWindows rejects PostgreSQL turn-window loading until the turn lookup workflow is fully ported.
// LoadTurnWindows 用于在 PostgreSQL turn 详情读取工作流完整迁移前，显式拒绝 turn 窗口加载。
func (unsupportedOperations) LoadTurnWindows(context.Context, []uint64, int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	return nil, unsupportedOperationError("LoadTurnWindows")
}

// LoadMemoryNodesByIDs rejects PostgreSQL memory-node loading until the unified memory workflow is fully ported.
// LoadMemoryNodesByIDs 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝按 ID 加载记忆节点。
func (unsupportedOperations) LoadMemoryNodesByIDs(context.Context, []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	return nil, unsupportedOperationError("LoadMemoryNodesByIDs")
}

// LoadMemoryContextEdgesByMemoryIDs rejects PostgreSQL context-edge loading until the unified memory workflow is fully ported.
// LoadMemoryContextEdgesByMemoryIDs 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝按记忆 ID 加载上下文边。
func (unsupportedOperations) LoadMemoryContextEdgesByMemoryIDs(context.Context, []uint64) ([]logicdomain.MemoryContextEdge, error) {
	return nil, unsupportedOperationError("LoadMemoryContextEdgesByMemoryIDs")
}

// LoadMemoryNodesByVectorIDs rejects PostgreSQL vector-id materialization until the unified memory workflow is fully ported.
// LoadMemoryNodesByVectorIDs 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝按向量 ID 回表。
func (unsupportedOperations) LoadMemoryNodesByVectorIDs(context.Context, []string) ([]logicdomain.MemoryNodeRecord, error) {
	return nil, unsupportedOperationError("LoadMemoryNodesByVectorIDs")
}

// SearchLexicalMemory rejects PostgreSQL lexical recall until the flavor-specific lexical search workflow is fully ported.
// SearchLexicalMemory 用于在 PostgreSQL 方言化词法检索工作流完整迁移前，显式拒绝词法召回。
func (unsupportedOperations) SearchLexicalMemory(context.Context, string, int, logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	return nil, unsupportedOperationError("SearchLexicalMemory")
}

// FindRecentActiveMemoryByDedupe rejects PostgreSQL direct-write dedupe checks until the unified memory workflow is fully ported.
// FindRecentActiveMemoryByDedupe 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝主动写幂等去重查询。
func (unsupportedOperations) FindRecentActiveMemoryByDedupe(context.Context, logicdomain.SessionRef, int, int, string, time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	return logicdomain.MemoryNodeRecord{}, false, unsupportedOperationError("FindRecentActiveMemoryByDedupe")
}

// CreateDirectMemoryNode rejects PostgreSQL direct memory writes until the unified memory workflow is fully ported.
// CreateDirectMemoryNode 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝主动写记忆节点创建。
func (unsupportedOperations) CreateDirectMemoryNode(context.Context, logicdomain.SessionRef, logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	return logicdomain.MemoryNodeRecord{}, unsupportedOperationError("CreateDirectMemoryNode")
}

// DeleteMemoryNodes rejects PostgreSQL manual memory deletion until the unified memory workflow is fully ported.
// DeleteMemoryNodes 用于在 PostgreSQL 统一记忆工作流完整迁移前，显式拒绝手工删除记忆节点。
func (unsupportedOperations) DeleteMemoryNodes(context.Context, []uint64, logicdomain.SearchFilter, time.Time, string) (logicdomain.MemoryDeleteResult, error) {
	return logicdomain.MemoryDeleteResult{}, unsupportedOperationError("DeleteMemoryNodes")
}

// ResolveProfileTarget rejects PostgreSQL profile target resolution until the profile workflow is fully ported.
// ResolveProfileTarget 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝画像目标解析。
func (unsupportedOperations) ResolveProfileTarget(context.Context, int, uint64, uint64) (logicdomain.ProfileTargetRef, error) {
	return logicdomain.ProfileTargetRef{}, unsupportedOperationError("ResolveProfileTarget")
}

// ListActiveProfileNodes rejects PostgreSQL active profile listing until the profile workflow is fully ported.
// ListActiveProfileNodes 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝活跃画像节点列表查询。
func (unsupportedOperations) ListActiveProfileNodes(context.Context, logicdomain.ProfileTargetRef, int) ([]logicdomain.ProfileNodeRecord, error) {
	return nil, unsupportedOperationError("ListActiveProfileNodes")
}

// LoadRenderedProfile rejects PostgreSQL rendered profile loading until the profile workflow is fully ported.
// LoadRenderedProfile 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝渲染画像读取。
func (unsupportedOperations) LoadRenderedProfile(context.Context, logicdomain.ProfileTargetRef) (string, error) {
	return "", unsupportedOperationError("LoadRenderedProfile")
}

// CreateProfileInstruction rejects PostgreSQL profile instruction writes until the profile workflow is fully ported.
// CreateProfileInstruction 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝画像指令写入。
func (unsupportedOperations) CreateProfileInstruction(context.Context, logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	return logicdomain.ProfileInstructionRecord{}, unsupportedOperationError("CreateProfileInstruction")
}

// FailProfileInstruction rejects PostgreSQL profile instruction failure writes until the profile workflow is fully ported.
// FailProfileInstruction 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝画像指令失败回写。
func (unsupportedOperations) FailProfileInstruction(context.Context, uint64, string, string) error {
	return unsupportedOperationError("FailProfileInstruction")
}

// ApplyManualProfileInstruction rejects PostgreSQL manual profile application until the profile workflow is fully ported.
// ApplyManualProfileInstruction 用于在 PostgreSQL 画像工作流完整迁移前，显式拒绝手工画像指令应用。
func (unsupportedOperations) ApplyManualProfileInstruction(context.Context, logicdomain.ProfileTargetRef, logicdomain.ProfileInstructionRecord, []logicdomain.ProfileNodeCandidate, []logicdomain.ProfileRetireDecision, string, string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	return logicdomain.ManualProfileInstructionApplyResult{}, unsupportedOperationError("ApplyManualProfileInstruction")
}

// LoadNoiseEmbeddingCache rejects PostgreSQL noise-cache reads until the semantic cache workflow is fully ported.
// LoadNoiseEmbeddingCache 用于在 PostgreSQL 语义缓存工作流完整迁移前，显式拒绝噪声向量缓存读取。
func (unsupportedOperations) LoadNoiseEmbeddingCache(context.Context, logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	return nil, unsupportedOperationError("LoadNoiseEmbeddingCache")
}

// ReplaceNoiseEmbeddingCache rejects PostgreSQL noise-cache writes until the semantic cache workflow is fully ported.
// ReplaceNoiseEmbeddingCache 用于在 PostgreSQL 语义缓存工作流完整迁移前，显式拒绝噪声向量缓存回写。
func (unsupportedOperations) ReplaceNoiseEmbeddingCache(context.Context, logicdomain.NoiseEmbeddingCacheQuery, []logicdomain.NoiseEmbeddingCacheEntry) error {
	return unsupportedOperationError("ReplaceNoiseEmbeddingCache")
}

// ResolveRequestScope rejects PostgreSQL request-scope resolution until the hierarchy workflow is fully ported.
// ResolveRequestScope 用于在 PostgreSQL 层级工作流完整迁移前，显式拒绝请求范围解析。
func (unsupportedOperations) ResolveRequestScope(context.Context, string, uint64, uint64) (logicdomain.SessionRef, error) {
	return logicdomain.SessionRef{}, unsupportedOperationError("ResolveRequestScope")
}

// ListProjects rejects PostgreSQL project listing until the workspace workflow is fully ported.
// ListProjects 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目列表查询。
func (unsupportedOperations) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return nil, unsupportedOperationError("ListProjects")
}

// ResolveProjectRef rejects PostgreSQL project resolution until the workspace workflow is fully ported.
// ResolveProjectRef 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目解析。
func (unsupportedOperations) ResolveProjectRef(context.Context, string) (logicdomain.ProjectRecord, error) {
	return logicdomain.ProjectRecord{}, unsupportedOperationError("ResolveProjectRef")
}

// ListProjectMemories rejects PostgreSQL project-memory listing until the workspace workflow is fully ported.
// ListProjectMemories 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目记忆列表查询。
func (unsupportedOperations) ListProjectMemories(context.Context, uint64) ([]logicdomain.MemoryRecord, error) {
	return nil, unsupportedOperationError("ListProjectMemories")
}

// EnsureProjectPath rejects PostgreSQL project-path creation until the workspace workflow is fully ported.
// EnsureProjectPath 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目路径创建。
func (unsupportedOperations) EnsureProjectPath(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, unsupportedOperationError("EnsureProjectPath")
}

// DeleteProjectPath rejects PostgreSQL project deletion until the workspace workflow is fully ported.
// DeleteProjectPath 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目删除。
func (unsupportedOperations) DeleteProjectPath(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	return logicdomain.ProjectDeleteResult{}, unsupportedOperationError("DeleteProjectPath")
}

// MigrateProjectPath rejects PostgreSQL project migration until the workspace workflow is fully ported.
// MigrateProjectPath 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝项目迁移。
func (unsupportedOperations) MigrateProjectPath(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, unsupportedOperationError("MigrateProjectPath")
}

// ResolveUserRef rejects PostgreSQL user resolution until the workspace workflow is fully ported.
// ResolveUserRef 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝用户解析。
func (unsupportedOperations) ResolveUserRef(context.Context, string) (logicdomain.UserRecord, error) {
	return logicdomain.UserRecord{}, unsupportedOperationError("ResolveUserRef")
}

// EnsureUserName rejects PostgreSQL user creation until the workspace workflow is fully ported.
// EnsureUserName 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝用户创建。
func (unsupportedOperations) EnsureUserName(context.Context, string, bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, unsupportedOperationError("EnsureUserName")
}

// ListUsers rejects PostgreSQL user listing until the workspace workflow is fully ported.
// ListUsers 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝用户列表查询。
func (unsupportedOperations) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return nil, unsupportedOperationError("ListUsers")
}

// DeleteUserRef rejects PostgreSQL user deletion until the workspace workflow is fully ported.
// DeleteUserRef 用于在 PostgreSQL workspace 工作流完整迁移前，显式拒绝用户删除。
func (unsupportedOperations) DeleteUserRef(context.Context, string, string) (logicdomain.UserDeleteResult, error) {
	return logicdomain.UserDeleteResult{}, unsupportedOperationError("DeleteUserRef")
}

// MarkSessionCompacted rejects PostgreSQL compact-boundary writes until the chat-compact workflow is fully ported.
// MarkSessionCompacted 用于在 PostgreSQL chat-compact 工作流完整迁移前，显式拒绝 compact 边界写入。
func (unsupportedOperations) MarkSessionCompacted(context.Context, logicdomain.SessionRef, time.Time) (uint64, bool, error) {
	return 0, false, unsupportedOperationError("MarkSessionCompacted")
}
