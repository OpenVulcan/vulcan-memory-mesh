// ports_memory.go keeps memory, vector, relational, and maintenance store contracts used by memory-centric workflows.
// ports_memory.go 用于承载记忆相关工作流依赖的 memory、vector、relational 与维护存储契约。
package ports

import (
	"context"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// VectorStore is the port used to persist, search, and administratively clean memory vectors inside the recall pipeline.
// VectorStore 用于抽象记忆召回流水线中的向量写入、检索和管理清理能力。
type VectorStore interface {
	Upsert(ctx context.Context, record logicdomain.MemoryRecord) error
	// Search returns ranked recall hits and must stamp Metadata["origin"] with the retrieval channel before handing them to the app layer.
	// Search 用于返回排序后的召回命中，并必须在交给应用层前用 Metadata["origin"] 标明检索通道。
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
	LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error)
	LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error)
	LoadRecentDirectMemoryWrites(ctx context.Context, session logicdomain.SessionRef, observedAfter, observedBefore time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error)
	ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error)
	LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error)
	LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error)
	ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error)
	ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error
	AdvanceSessionExtractWindow(ctx context.Context, sessionID uint64, observedAt, completedAt time.Time) error
	ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) ([]logicdomain.MemoryRecord, error)
	ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error)
	MarkTurnAsCorrupted(ctx context.Context, session logicdomain.SessionRef, turnID uint64) error
	Shutdowner
}

// TurnLookupStore is the narrow relational read port used by memory-query RPCs to load dehydrated turn rows by turn id.
// TurnLookupStore 用于给记忆查询 RPC 提供按 turn id 读取脱水 turn 行的窄关系读端口。
type TurnLookupStore interface {
	LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error)
	LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error)
}

// MemoryStore is the relational read/write port used by memory-query and direct-write RPCs to inspect and mutate unified durable memory rows.
// MemoryStore 用于给记忆查询和主动写入 RPC 提供统一长期记忆行的关系读写能力。
type MemoryStore interface {
	LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error)
	LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error)
	LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error)
	LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error)
	LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error)
	SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error)
	FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error)
	CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error)
	DeleteMemoryNodes(ctx context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error)
}

// MemoryVectorRebuildStore is the narrow maintenance port used by one-shot rebuild tools to rewrite durable stored vectors in place without replaying the whole business write pipeline.
// MemoryVectorRebuildStore 用于给一次性重建工具提供狭窄维护端口，使其能原地重写长期存储中的向量，而无需重放完整业务写入链路。
type MemoryVectorRebuildStore interface {
	ReplaceMemoryVectors(ctx context.Context, records []logicdomain.MemoryRecord) error
}

// MemoryVectorResetStore is the split-mode maintenance port used to clear durable vector payloads before SQLite and the detached vector store restart from the same empty baseline.
// MemoryVectorResetStore 用于描述 split 模式下的维护端口：在 SQLite 与独立向量库从同一个空基线重新启动前，先清空 durable 向量载荷。
type MemoryVectorResetStore interface {
	ClearMemoryVectors(ctx context.Context, vectorIDs []string) error
}

// MemoryVectorDimensionMigrationStore is the narrow maintenance port used when a model switch changes embedding dimensions and combined durable storage must atomically rebuild its vector columns together with the refreshed active payload.
// MemoryVectorDimensionMigrationStore 用于描述模型切换导致 embedding 维度变化时的狭窄维护端口，让组合 durable 存储能把向量列重建与 active 载荷回填放进同一原子维护动作。
type MemoryVectorDimensionMigrationStore interface {
	RebuildMemoryVectorDimensions(ctx context.Context, records []logicdomain.MemoryRecord) error
}

// MaintenanceProjectMemoryLister is the narrow maintenance port used by one-shot rebuild/export flows to enumerate project memories with maintenance-specific timeout policy instead of the normal online request budget.
// MaintenanceProjectMemoryLister 用于描述一次性重建/导出流程使用的狭窄维护端口，让项目记忆枚举改走维护专用超时策略，而不是普通在线请求预算。
type MaintenanceProjectMemoryLister interface {
	ListProjectMemoriesForMaintenance(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error)
}

// MaintenanceProjectLister is the narrow maintenance port used by one-shot rebuild/export flows to enumerate project rows with maintenance-specific timeout policy instead of the normal online request budget.
// MaintenanceProjectLister 用于描述一次性重建/导出流程使用的狭窄维护端口，让项目列表枚举改走维护专用超时策略，而不是普通在线请求预算。
type MaintenanceProjectLister interface {
	ListProjectsForMaintenance(ctx context.Context) ([]logicdomain.ProjectRecord, error)
}
