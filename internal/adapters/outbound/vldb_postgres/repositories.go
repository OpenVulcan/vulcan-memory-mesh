// repositories.go defines the internal PostgreSQL shared core and repository bundle boundaries so the public Store can stay as a thin facade.
// repositories.go 用于定义 PostgreSQL 适配器内部的共享核心与 repository bundle 边界，让对外 Store 可以保持为薄门面。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// storeShared owns the runtime-wide PostgreSQL pool, normalized config, and resolved search dialect shared by every repository slice.
// storeShared 用于承载所有仓储切面共享的 PostgreSQL 连接池、规范化配置以及已解析搜索方言。
type storeShared struct {
	pool    *pgxpool.Pool
	cfg     Config
	dialect searchDialect
}

// workspaceRepository marks the workspace and admin data-access slice inside the PostgreSQL combined store.
// workspaceRepository 用于标记 PostgreSQL 组合库中的 workspace 与 admin 数据访问切面。
type workspaceRepository struct {
	shared *storeShared
}

// workspaceQualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema for workspace-related tables.
// workspaceQualifiedTable 用于返回 workspace 相关表在当前配置 schema 下的完整限定名称。
func (r *workspaceRepository) workspaceQualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// usersTable returns the fully-qualified durable user table name.
// usersTable 用于返回长期用户表的完整限定名称。
func (r *workspaceRepository) usersTable() string {
	return r.workspaceQualifiedTable("vmm_users")
}

// teamsTable returns the fully-qualified durable team table name.
// teamsTable 用于返回长期 team 表的完整限定名称。
func (r *workspaceRepository) teamsTable() string {
	return r.workspaceQualifiedTable("vmm_teams")
}

// spacesTable returns the fully-qualified durable space table name.
// spacesTable 用于返回长期 space 表的完整限定名称。
func (r *workspaceRepository) spacesTable() string {
	return r.workspaceQualifiedTable("vmm_spaces")
}

// projectsTable returns the fully-qualified durable project table name.
// projectsTable 用于返回长期 project 表的完整限定名称。
func (r *workspaceRepository) projectsTable() string {
	return r.workspaceQualifiedTable("vmm_projects")
}

// sessionsTable returns the fully-qualified durable session table name.
// sessionsTable 用于返回长期 session 表的完整限定名称。
func (r *workspaceRepository) sessionsTable() string {
	return r.workspaceQualifiedTable("vmm_sessions")
}

// workspaceQueryContext derives one bounded query context so shared PostgreSQL workspace calls stay inside the configured runtime timeout.
// workspaceQueryContext 用于派生一个有界查询上下文，确保 PostgreSQL workspace 共享调用始终受运行时超时约束。
func (r *workspaceRepository) workspaceQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// workspaceBootstrapContext derives a startup-oriented timeout so workspace bootstrap work is less brittle than regular request-time queries.
// workspaceBootstrapContext 用于派生面向 workspace 启动阶段的超时，让 workspace 启动工作相比普通请求查询更稳健。
func (r *workspaceRepository) workspaceBootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// workspaceMaintenanceReadContext derives a maintenance-oriented read timeout so workspace scans can run longer than ordinary online requests.
// workspaceMaintenanceReadContext 用于派生面向 workspace 维护读取的超时，让 workspace 扫描可以长于普通在线请求。
func (r *workspaceRepository) workspaceMaintenanceReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceReadTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceReadTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// profileNodesTable returns the fully-qualified durable profile-node table name.
// profileNodesTable 用于返回长期画像节点表的完整限定名称。
func (r *workspaceRepository) profileNodesTable() string {
	return r.workspaceQualifiedTable("vmm_profile_nodes")
}

// memoryNodesTable returns the fully-qualified durable memory table name.
// memoryNodesTable 用于返回长期记忆表的完整限定名称。
func (r *workspaceRepository) memoryNodesTable() string {
	return r.workspaceQualifiedTable("vmm_memory_nodes")
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (r *workspaceRepository) turnsTable() string {
	return r.workspaceQualifiedTable("vmm_turn_records")
}

// turnRepository marks the session-turn persistence and lookup slice inside the PostgreSQL combined store.
// turnRepository 用于标记 PostgreSQL 组合库中的 session turn 持久化与查询切面。
type turnRepository struct {
	shared *storeShared
}

// turnQualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema for turn-related tables.
// turnQualifiedTable 用于返回 turn 相关表在当前配置 schema 下的完整限定名称。
func (r *turnRepository) turnQualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (r *turnRepository) turnsTable() string {
	return r.turnQualifiedTable("vmm_turn_records")
}

// sessionsTable returns the fully-qualified durable session table name.
// sessionsTable 用于返回长期 session 表的完整限定名称。
func (r *turnRepository) sessionsTable() string {
	return r.turnQualifiedTable("vmm_sessions")
}

// turnQueryContext derives one bounded query context so shared PostgreSQL turn calls stay inside the configured runtime timeout.
// turnQueryContext 用于派生一个有界查询上下文，确保 PostgreSQL turn 共享调用始终受运行时超时约束。
func (r *turnRepository) turnQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// turnBootstrapContext derives a startup-oriented timeout so turn-related bootstrap work is less brittle than regular request-time queries.
// turnBootstrapContext 用于派生面向 turn 启动阶段的超时，让 turn 相关启动工作相比普通请求查询更稳健。
func (r *turnRepository) turnBootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// analysisRepository marks the turn-analysis apply and analysis-assist slice inside the PostgreSQL combined store.
// analysisRepository 用于标记 PostgreSQL 组合库中的 turn analysis 落地与分析辅助切面。
type analysisRepository struct {
	shared *storeShared
}

// memoryRepository marks the durable memory query and direct-write slice inside the PostgreSQL combined store.
// memoryRepository 用于标记 PostgreSQL 组合库中的长期记忆查询与主动写入切面。
type memoryRepository struct {
	shared   *storeShared
	analysis analysisRepository
}

// qualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema.
// qualifiedTable 用于返回当前配置 schema 下的完整限定表名。
func (r *memoryRepository) qualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// queryContext derives one bounded query context so shared PostgreSQL calls stay inside the configured runtime timeout.
// queryContext 用于派生一个有界查询上下文，确保 PostgreSQL 共享调用始终受运行时超时约束。
func (r *memoryRepository) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// bootstrapContext derives a startup-oriented timeout so schema bootstrap and dialect index creation are less brittle than regular request-time queries.
// bootstrapContext 用于派生面向启动阶段的超时，让 schema 启动和方言索引创建相比普通请求查询更稳健。
func (r *memoryRepository) bootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// maintenanceReadContext derives a maintenance-oriented read timeout so project-memory exports and vector-rebuild fact scans can run longer than ordinary online requests without becoming unbounded.
// maintenanceReadContext 用于派生面向维护读取的超时，让项目记忆导出和向量重建事实扫描可以长于普通在线请求，但又不会变成无界等待。
func (r *memoryRepository) maintenanceReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceReadTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceReadTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// memoryNodesTable returns the fully-qualified durable memory table name used by the combined store.
// memoryNodesTable 用于返回组合库使用的统一长期记忆表的完整限定名称。
func (r *memoryRepository) memoryNodesTable() string {
	return r.qualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (r *memoryRepository) memoryContextEdgesTable() string {
	return r.qualifiedTable("vmm_memory_context_edges")
}

// trgmSimilarityThreshold returns the configured trigram similarity threshold used by lexical SQL generation.
// trgmSimilarityThreshold 用于返回 lexical SQL 生成所需的配置 trigram 相似度阈值。
func (r *memoryRepository) trgmSimilarityThreshold() float64 {
	return r.shared.cfg.TRGMSimilarityThreshold
}

// profileRepository marks the profile review, lifecycle, and rendered-profile persistence slice inside the PostgreSQL combined store.
// profileRepository 用于标记 PostgreSQL 组合库中的画像评审、生命周期与渲染画像持久化切面。
type profileRepository struct {
	shared *storeShared
}

// retentionRepository marks the recycle, trash purge, and lifecycle retention slice inside the PostgreSQL combined store.
// retentionRepository 用于标记 PostgreSQL 组合库中的回收、垃圾清理与保留期维护切面。
type retentionRepository struct {
	shared *storeShared
}

// retentionQualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema for retention-related tables.
// retentionQualifiedTable 用于返回 retention 相关表在当前配置 schema 下的完整限定名称。
func (r *retentionRepository) retentionQualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// memoryNodesTable returns the fully-qualified durable memory table name.
// memoryNodesTable 用于返回长期记忆表的完整限定名称。
func (r *retentionRepository) memoryNodesTable() string {
	return r.retentionQualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (r *retentionRepository) memoryContextEdgesTable() string {
	return r.retentionQualifiedTable("vmm_memory_context_edges")
}

// sessionsTable returns the fully-qualified durable session table name.
// sessionsTable 用于返回长期 session 表的完整限定名称。
func (r *retentionRepository) sessionsTable() string {
	return r.retentionQualifiedTable("vmm_sessions")
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (r *retentionRepository) turnsTable() string {
	return r.retentionQualifiedTable("vmm_turn_records")
}

// memoryNodesTrashTable returns the fully-qualified memory nodes trash table name.
// memoryNodesTrashTable 用于返回记忆节点回收站的完整限定名称。
func (r *retentionRepository) memoryNodesTrashTable() string {
	return r.retentionQualifiedTable("vmm_memory_nodes_trash")
}

// memoryContextEdgesTrashTable returns the fully-qualified memory context edges trash table name.
// memoryContextEdgesTrashTable 用于返回记忆情境边回收站的完整限定名称。
func (r *retentionRepository) memoryContextEdgesTrashTable() string {
	return r.retentionQualifiedTable("vmm_memory_context_edges_trash")
}

// turnsTrashTable returns the fully-qualified turn records trash table name.
// turnsTrashTable 用于返回 turn 记录回收站的完整限定名称。
func (r *retentionRepository) turnsTrashTable() string {
	return r.retentionQualifiedTable("vmm_turn_records_trash")
}

// recycleBatchesTable returns the fully-qualified recycle batches table name.
// recycleBatchesTable 用于返回回收批次的完整限定名称。
func (r *retentionRepository) recycleBatchesTable() string {
	return r.retentionQualifiedTable("vmm_recycle_batches")
}

// recycleJobsTable returns the fully-qualified recycle jobs table name.
// recycleJobsTable 用于返回回收任务的完整限定名称。
func (r *retentionRepository) recycleJobsTable() string {
	return r.retentionQualifiedTable("vmm_recycle_jobs")
}

// profileNodesTable returns the fully-qualified profile nodes table name.
// profileNodesTable 用于返回画像节点表的完整限定名称。
func (r *retentionRepository) profileNodesTable() string {
	return r.retentionQualifiedTable("vmm_profile_nodes")
}

// retentionQueryContext derives one bounded query context so shared PostgreSQL retention calls stay inside the configured runtime timeout.
// retentionQueryContext 用于派生一个有界查询上下文，确保 PostgreSQL retention 共享调用始终受运行时超时约束。
func (r *retentionRepository) retentionQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// retentionMaintenanceReadContext derives a maintenance-oriented read timeout for retention scans.
// retentionMaintenanceReadContext 用于派生面向 retention 扫描的维护读取超时。
func (r *retentionRepository) retentionMaintenanceReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceReadTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceReadTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// retentionMaintenanceWriteContext derives a maintenance-oriented write timeout for destructive retention operations.
// retentionMaintenanceWriteContext 用于派生面向破坏性 retention 操作的维护写入超时。
func (r *retentionRepository) retentionMaintenanceWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceWriteTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceWriteTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// vectorRepository marks the vector-facing persistence slice inside the PostgreSQL combined store.
// vectorRepository 用于标记 PostgreSQL 组合库中的向量侧持久化切面。
type vectorRepository struct {
	shared *storeShared
}

// vectorQualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema for vector-related tables.
// vectorQualifiedTable 用于返回 vector 相关表在当前配置 schema 下的完整限定名称。
func (r *vectorRepository) vectorQualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// vectorQueryContext derives one bounded query context so shared PostgreSQL vector calls stay inside the configured runtime timeout.
// vectorQueryContext 用于派生一个有界查询上下文，确保 PostgreSQL vector 共享调用始终受运行时超时约束。
func (r *vectorRepository) vectorQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// vectorBootstrapContext derives a startup-oriented timeout for vector bootstrap work.
// vectorBootstrapContext 用于派生面向 vector 启动阶段的超时。
func (r *vectorRepository) vectorBootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// memoryNodesTable returns the fully-qualified durable memory table name.
// memoryNodesTable 用于返回长期记忆表的完整限定名称。
func (r *vectorRepository) memoryNodesTable() string {
	return r.vectorQualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (r *vectorRepository) memoryContextEdgesTable() string {
	return r.vectorQualifiedTable("vmm_memory_context_edges")
}

// recycleBatchesTable returns the fully-qualified recycle batches table name.
// recycleBatchesTable 用于返回回收批次的完整限定名称。
func (r *vectorRepository) recycleBatchesTable() string {
	return r.vectorQualifiedTable("vmm_recycle_batches")
}

// vectorGCJobsTable returns the fully-qualified vector GC jobs table name.
// vectorGCJobsTable 用于返回向量 GC 任务的完整限定名称。
func (r *vectorRepository) vectorGCJobsTable() string {
	return r.vectorQualifiedTable("vmm_vector_gc_jobs")
}

// maintenanceWriteContext derives a maintenance-oriented write timeout for destructive vector operations.
// maintenanceWriteContext 用于派生面向破坏性向量操作的维护写入超时。
func (r *vectorRepository) maintenanceWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceWriteTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceWriteTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// noiseEmbeddingsTable returns the fully-qualified noise embeddings cache table name.
// noiseEmbeddingsTable 用于返回噪声嵌入缓存表的完整限定名称。
func (r *vectorRepository) noiseEmbeddingsTable() string {
	return r.vectorQualifiedTable("vmm_noise_embeddings")
}

// memoryNodesTrashTable returns the fully-qualified memory nodes trash table name.
// memoryNodesTrashTable 用于返回记忆节点回收站的完整限定名称。
func (r *vectorRepository) memoryNodesTrashTable() string {
	return r.vectorQualifiedTable("vmm_memory_nodes_trash")
}

// maintenanceRepository marks schema, cache, migration, and maintenance helper responsibilities inside the PostgreSQL combined store.
// maintenanceRepository 用于标记 PostgreSQL 组合库中的 schema、缓存、迁移与维护辅助职责。
type maintenanceRepository struct {
	shared *storeShared
}

// maintenanceQualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema for maintenance-related tables.
// maintenanceQualifiedTable 用于返回 maintenance 相关表在当前配置 schema 下的完整限定名称。
func (r *maintenanceRepository) maintenanceQualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// maintenanceQueryContext derives one bounded query context so shared PostgreSQL maintenance calls stay inside the configured runtime timeout.
// maintenanceQueryContext 用于派生一个有界查询上下文，确保 PostgreSQL maintenance 共享调用始终受运行时超时约束。
func (r *maintenanceRepository) maintenanceQueryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// maintenanceBootstrapContext derives a startup-oriented timeout for maintenance bootstrap work.
// maintenanceBootstrapContext 用于派生面向 maintenance 启动阶段的超时。
func (r *maintenanceRepository) maintenanceBootstrapContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout < bootstrapTimeoutFloor {
		timeout = bootstrapTimeoutFloor
	}
	return context.WithTimeout(ctx, timeout)
}

// memoryNodesTable returns the fully-qualified durable memory table name.
// memoryNodesTable 用于返回长期记忆表的完整限定名称。
func (r *maintenanceRepository) memoryNodesTable() string {
	return r.maintenanceQualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (r *maintenanceRepository) memoryContextEdgesTable() string {
	return r.maintenanceQualifiedTable("vmm_memory_context_edges")
}

// usersTable returns the fully-qualified durable user table name.
// usersTable 用于返回长期用户表的完整限定名称。
func (r *maintenanceRepository) usersTable() string {
	return r.maintenanceQualifiedTable("vmm_users")
}

// teamsTable returns the fully-qualified durable team table name.
// teamsTable 用于返回长期 team 表的完整限定名称。
func (r *maintenanceRepository) teamsTable() string {
	return r.maintenanceQualifiedTable("vmm_teams")
}

// spacesTable returns the fully-qualified durable space table name.
// spacesTable 用于返回长期 space 表的完整限定名称。
func (r *maintenanceRepository) spacesTable() string {
	return r.maintenanceQualifiedTable("vmm_spaces")
}

// projectsTable returns the fully-qualified durable project table name.
// projectsTable 用于返回长期 project 表的完整限定名称。
func (r *maintenanceRepository) projectsTable() string {
	return r.maintenanceQualifiedTable("vmm_projects")
}

// sessionsTable returns the fully-qualified durable session table name.
// sessionsTable 用于返回长期 session 表的完整限定名称。
func (r *maintenanceRepository) sessionsTable() string {
	return r.maintenanceQualifiedTable("vmm_sessions")
}

// profileNodesTable returns the fully-qualified durable profile-node table name.
// profileNodesTable 用于返回长期画像节点表的完整限定名称。
func (r *maintenanceRepository) profileNodesTable() string {
	return r.maintenanceQualifiedTable("vmm_profile_nodes")
}

// profileInstructionsTable returns the fully-qualified durable profile-instructions table name.
// profileInstructionsTable 用于返回长期画像指令表的完整限定名称。
func (r *maintenanceRepository) profileInstructionsTable() string {
	return r.maintenanceQualifiedTable("vmm_profile_instructions")
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (r *maintenanceRepository) turnsTable() string {
	return r.maintenanceQualifiedTable("vmm_turn_records")
}

// noiseEmbeddingsTable returns the fully-qualified noise embeddings cache table name.
// noiseEmbeddingsTable 用于返回噪声嵌入缓存表的完整限定名称。
func (r *maintenanceRepository) noiseEmbeddingsTable() string {
	return r.maintenanceQualifiedTable("vmm_noise_embeddings")
}

// recycleBatchesTable returns the fully-qualified recycle batches table name.
// recycleBatchesTable 用于返回回收批次的完整限定名称。
func (r *maintenanceRepository) recycleBatchesTable() string {
	return r.maintenanceQualifiedTable("vmm_recycle_batches")
}

// recycleJobsTable returns the fully-qualified recycle jobs table name.
// recycleJobsTable 用于返回回收任务的完整限定名称。
func (r *maintenanceRepository) recycleJobsTable() string {
	return r.maintenanceQualifiedTable("vmm_recycle_jobs")
}

// vectorGCJobsTable returns the fully-qualified vector GC jobs table name.
// vectorGCJobsTable 用于返回向量 GC 任务的完整限定名称。
func (r *maintenanceRepository) vectorGCJobsTable() string {
	return r.maintenanceQualifiedTable("vmm_vector_gc_jobs")
}

// memoryNodesTrashTable returns the fully-qualified memory nodes trash table name.
// memoryNodesTrashTable 用于返回记忆节点回收站的完整限定名称。
func (r *maintenanceRepository) memoryNodesTrashTable() string {
	return r.maintenanceQualifiedTable("vmm_memory_nodes_trash")
}

// memoryContextEdgesTrashTable returns the fully-qualified memory context edges trash table name.
// memoryContextEdgesTrashTable 用于返回记忆情境边回收站的完整限定名称。
func (r *maintenanceRepository) memoryContextEdgesTrashTable() string {
	return r.maintenanceQualifiedTable("vmm_memory_context_edges_trash")
}

// turnsTrashTable returns the fully-qualified turn records trash table name.
// turnsTrashTable 用于返回 turn 记录回收站的完整限定名称。
func (r *maintenanceRepository) turnsTrashTable() string {
	return r.maintenanceQualifiedTable("vmm_turn_records_trash")
}

// maintenanceWriteContext derives a maintenance-oriented write timeout for destructive operations.
// maintenanceWriteContext 用于派生面向破坏性操作的维护写入超时。
func (r *maintenanceRepository) maintenanceWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.MaintenanceWriteTimeout
	if timeout <= 0 {
		timeout = defaultMaintenanceWriteTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// storeRepositories groups every internal repository slice so the public Store can make responsibility ownership explicit without changing the external adapter contract.
// storeRepositories 用于聚合全部内部 repository 切面，让对外 Store 在不改变适配器契约的前提下明确职责归属。
type storeRepositories struct {
	workspace   workspaceRepository
	turns       turnRepository
	analysis    analysisRepository
	memory      memoryRepository
	profile     profileRepository
	retention   retentionRepository
	vector      vectorRepository
	maintenance maintenanceRepository
}

// newStoreRepositories wires every internal repository slice to the same shared PostgreSQL runtime core.
// newStoreRepositories 用于把全部内部 repository 切面统一绑定到同一个 PostgreSQL 共享运行时核心。
func newStoreRepositories(shared *storeShared) storeRepositories {
	repos := storeRepositories{
		workspace: workspaceRepository{shared: shared},
		turns:     turnRepository{shared: shared},
		analysis:  analysisRepository{shared: shared},
		memory:    memoryRepository{shared: shared},
		profile:   profileRepository{shared: shared},
		retention: retentionRepository{shared: shared},
		vector:    vectorRepository{shared: shared},
		maintenance: maintenanceRepository{
			shared: shared,
		},
	}
	repos.memory.analysis = repos.analysis
	return repos
}

// qualifiedTable returns the fully-qualified table name inside the configured PostgreSQL schema.
// qualifiedTable 用于返回当前配置 schema 下的完整限定表名。
func (r *analysisRepository) qualifiedTable(name string) string {
	return quoteIdentifier(r.shared.cfg.Schema) + "." + quoteIdentifier(strings.TrimSpace(name))
}

// queryContext derives one bounded query context so shared PostgreSQL calls stay inside the configured runtime timeout.
// queryContext 用于派生一个有界查询上下文，确保 PostgreSQL 共享调用始终受运行时超时约束。
func (r *analysisRepository) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.shared.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// memoryNodesTable returns the fully-qualified durable memory table name used by the combined store.
// memoryNodesTable 用于返回组合库使用的统一长期记忆表的完整限定名称。
func (r *analysisRepository) memoryNodesTable() string {
	return r.qualifiedTable("vmm_memory_nodes")
}

// memoryContextEdgesTable returns the fully-qualified memory-context edge table name.
// memoryContextEdgesTable 用于返回长期记忆情境边表的完整限定名称。
func (r *analysisRepository) memoryContextEdgesTable() string {
	return r.qualifiedTable("vmm_memory_context_edges")
}

// usersTable returns the fully-qualified durable user table name.
// usersTable 用于返回长期用户表的完整限定名称。
func (r *analysisRepository) usersTable() string {
	return r.qualifiedTable("vmm_users")
}

// projectsTable returns the fully-qualified durable project table name.
// projectsTable 用于返回长期 project 表的完整限定名称。
func (r *analysisRepository) projectsTable() string {
	return r.qualifiedTable("vmm_projects")
}

// turnsTable returns the fully-qualified durable turn table name.
// turnsTable 用于返回长期 turn 表的完整限定名称。
func (r *analysisRepository) turnsTable() string {
	return r.qualifiedTable("vmm_turn_records")
}

// profileNodesTable returns the fully-qualified durable profile-node table name.
// profileNodesTable 用于返回长期画像节点表的完整限定名称。
func (r *analysisRepository) profileNodesTable() string {
	return r.qualifiedTable("vmm_profile_nodes")
}

// queryMemoryNodes executes one PostgreSQL memory query and decodes the rows into scan structs shared by multiple memory-facing methods.
// queryMemoryNodes 用于执行 PostgreSQL 记忆查询，并把结果解码为多个记忆方法共享的扫描结构。
func (r *analysisRepository) queryMemoryNodes(ctx context.Context, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	return r.queryMemoryNodesWithQueryer(ctx, r.shared.pool, sqlText, args...)
}

// queryMemoryNodesWithQueryer executes one PostgreSQL memory query against either the pool or a transaction so lifecycle updates can safely read locked rows.
// queryMemoryNodesWithQueryer 用于在连接池或事务上执行 PostgreSQL 记忆查询，让生命周期更新可以安全读取已加锁的行。
func (r *analysisRepository) queryMemoryNodesWithQueryer(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	return r.queryMemoryNodesWithContextBuilder(ctx, q, r.queryContext, sqlText, args...)
}

// queryMemoryNodesWithContextBuilder executes one PostgreSQL memory query against either the pool or a transaction while letting callers choose the timeout policy that wraps the SQL work.
// queryMemoryNodesWithContextBuilder 用于在连接池或事务上执行 PostgreSQL 记忆查询，并允许调用方选择包裹该 SQL 工作的超时策略。
func (r *analysisRepository) queryMemoryNodesWithContextBuilder(ctx context.Context, q profileQueryer, buildContext func(context.Context) (context.Context, context.CancelFunc), sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	if buildContext == nil {
		buildContext = r.queryContext
	}
	callCtx, cancel := buildContext(ctx)
	defer cancel()
	return scanMemoryNodeRows(callCtx, q, sqlText, args...)
}

// loadUserByQueryer executes one id-bounded user lookup through the given queryer so workspace and profile repositories share a single scan contract.
// loadUserByQueryer 用于通过给定 queryer 执行按 id 查找用户的操作，让 workspace 与 profile 仓储共享同一套扫描逻辑。
func loadUserByQueryer(ctx context.Context, q profileQueryer, userID uint64, userTable string, lockClause string) (logicdomain.UserRecord, error) {
	sqlText := fmt.Sprintf(`
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM %s
WHERE id = $1
LIMIT 1%s
`, userTable, lockClause)
	var row userScanRow
	err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(userID)).Scan(
		&row.ID, &row.Name, &row.Profile, &row.DeleteConfirmCode, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.UserRecord{}, fmt.Errorf("load postgres user by id: %w", err)
		}
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user id %d does not exist", userID)}
	}
	return row.toDomain(), nil
}

// loadProjectByQueryer executes one id-bounded project lookup with team/space joins through the given queryer so multiple repositories share a single scan contract.
// loadProjectByQueryer 用于通过给定 queryer 执行带 team/space 联接的按 id 查找项目的操作，让多个仓储共享同一套扫描逻辑。
func loadProjectByQueryer(ctx context.Context, q profileQueryer, projectID uint64, projectTable, teamTable, spaceTable string) (logicdomain.ProjectRecord, error) {
	sqlText := fmt.Sprintf(`
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, t.name AS team_name, sp.name AS space_name, p.created_at, p.updated_at
FROM %s AS p
JOIN %s AS t ON t.id = p.team_id
JOIN %s AS sp ON sp.id = p.space_id
WHERE p.id = $1
LIMIT 1
`, projectTable, teamTable, spaceTable)
	var row projectScanRow
	err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(projectID)).Scan(
		&row.ID, &row.TeamID, &row.SpaceID, &row.Name, &row.Profile, &row.TeamName, &row.SpaceName, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.ProjectRecord{}, fmt.Errorf("load postgres project by id: %w", err)
		}
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project id %d does not exist", projectID)}
	}
	return row.toDomain(), nil
}

// scanMemoryNodeRows scans all rows returned by one memory query into the shared memoryNodeScanRow shape so analysis and memory repositories avoid duplicating the same 34-field scan logic.
// scanMemoryNodeRows 用于把给定查询返回的所有行扫描到共享的 memoryNodeScanRow 结构，让 analysis 与 memory 仓储避免重复 34 字段的扫描逻辑。
func scanMemoryNodeRows(ctx context.Context, q profileQueryer, sqlText string, args ...any) ([]memoryNodeScanRow, error) {
	rows, err := q.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []memoryNodeScanRow
	for rows.Next() {
		var row memoryNodeScanRow
		if err := rows.Scan(
			&row.ID,
			&row.TeamID,
			&row.SpaceID,
			&row.ProjectID,
			&row.UserID,
			&row.OriginSessionID,
			&row.SourceTurnID,
			&row.VectorID,
			&row.EmbeddingText,
			&row.SourceKind,
			&row.ScopeLevel,
			&row.Category,
			&row.Abstract,
			&row.Details,
			&row.MemoryStatus,
			&row.Priority,
			&row.MemoryLevel,
			&row.RefreshWeight,
			&row.SupportCount,
			&row.RebuttalCount,
			&row.StatusReason,
			&row.ExpiresAt,
			&row.LastRecalledAt,
			&row.LastAdoptedAt,
			&row.LastReinforcedAt,
			&row.RecalledCount,
			&row.AdoptedCount,
			&row.ReinforcementCount,
			&row.CrossSessionAdoptedCount,
			&row.DecayDisabled,
			&row.DedupeHash,
			&row.CreatedAt,
			&row.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres memory node row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres memory node rows: %w", err)
	}
	return items, nil
}
