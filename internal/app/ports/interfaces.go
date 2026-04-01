// interfaces.go declares application-facing storage and utility ports used by use cases.
// interfaces.go 用于声明应用层用例依赖的存储与基础能力端口。
package ports

import (
	"context"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Shutdowner abstracts dependencies that must participate in application shutdown sequencing.
// Shutdowner 用于抽象需要参与应用关闭顺序控制的依赖。
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// EmbeddingRequest carries the normalized embedding call parameters passed from use cases to adapters.
// EmbeddingRequest 用于承载从用例层传给适配器的标准化 embedding 调用参数。
type EmbeddingRequest struct {
	Model         string
	Texts         []string
	Dimension     int
	ProviderHints map[string]any
}

// EmbeddingResponse returns vectors produced by the configured embedding backend.
// EmbeddingResponse 用于返回当前 embedding 后端生成的向量结果。
type EmbeddingResponse struct {
	Vectors [][]float32
}

// EmbeddingClient is the port used by use cases to obtain embeddings without depending on a concrete SDK.
// EmbeddingClient 用于让用例层在不依赖具体 SDK 的前提下获取向量表示。
type EmbeddingClient interface {
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// VectorStore is the port used to persist, search, and administratively clean memory vectors inside the recall pipeline.
// VectorStore 用于抽象记忆召回流水线中的向量写入、检索和管理清理能力。
type VectorStore interface {
	Upsert(ctx context.Context, record logicdomain.MemoryRecord) error
	Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error)
	DeleteByFilter(ctx context.Context, filter logicdomain.SearchFilter) (uint64, error)
	DeleteByIDs(ctx context.Context, ids []string) (uint64, error)
	Shutdowner
}

// RelationalStore is the port used by post-action flows to persist one cleaned turn inside one resolved session scope.
// RelationalStore 用于给 post-action 流程在某个已解析的 session 范围内持久化一条清洗后的 turn。
type RelationalStore interface {
	AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error)
	LoadPendingSessionTurns(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error)
	LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error)
	LoadActiveSessionMemoryNodes(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error)
	ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error)
	LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error)
	LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error)
	ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error)
	ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error
	ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) error
	ApplySessionBatchAnalysis(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.SessionTurnRecord, analysis logicdomain.SessionBatchAnalysis) (logicdomain.SessionAnalysisApplyResult, error)
	Shutdowner
}

// TurnLookupStore is the narrow relational read port used by memory-query RPCs to load dehydrated turn rows by turn id.
// TurnLookupStore 用于给记忆查询 RPC 提供按 turn id 读取脱水 turn 行的窄关系读端口。
type TurnLookupStore interface {
	LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error)
	LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error)
}

// ProfileStore is the port used by profile-query and manual profile-instruction RPCs to resolve targets, inspect active nodes, and persist reviewed updates.
// ProfileStore 用于让画像查询与手工画像指令 RPC 解析目标、查看 active 节点并持久化评审后的更新结果。
type ProfileStore interface {
	ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error)
	ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error)
	LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error)
	CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error)
	FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error
	ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error)
}

// NoiseTurnFilter is the port used by post-action flows to drop noisy normalized turns before relational persistence.
// NoiseTurnFilter 用于让 post-action 流程在关系持久化前过滤掉噪声标准化轮次。
type NoiseTurnFilter interface {
	FilterPersistableTurns(ctx context.Context, turns []logicdomain.NormalizedTurn) []logicdomain.NormalizedTurn
}

// NoiseEmbeddingCache is the port used by startup processors to reuse previously computed semantic prototype vectors.
// NoiseEmbeddingCache 用于让启动期处理器复用已计算好的语义原型向量缓存。
type NoiseEmbeddingCache interface {
	LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error)
	ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error
}

// ContextPersonaProvider is the port used by pre-check flows to load stable persona and project context.
// ContextPersonaProvider 用于给 pre-check 流程加载稳定的画像和项目上下文。
type ContextPersonaProvider interface {
	Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error)
}

// RequestScopeResolver is the port used by gRPC interceptors to validate numeric project/user ids and auto-create sessions.
// RequestScopeResolver 用于让 gRPC 拦截器校验数字 project/user id，并在需要时自动创建 session。
type RequestScopeResolver interface {
	ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error)
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

// IDGenerator is the utility port used to create stable IDs for traces and seeded memories.
// IDGenerator 用于生成 trace 等运行时标识所需的稳定 ID。
type IDGenerator interface {
	NewID(prefix string) string
}
