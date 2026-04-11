// store.go implements the shared PostgreSQL combined-store adapter used by the dialect-pattern runtime.
// store.go 用于实现 Dialect Pattern 运行时共享的 PostgreSQL 组合库适配器。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// defaultSchemaVersionComponent stores the shared PostgreSQL combined-store schema version under one stable component key.
	// defaultSchemaVersionComponent 用于把 PostgreSQL 组合库的共享 schema 版本固定到稳定的组件键名下。
	defaultSchemaVersionComponent = "postgres_combined"

	// defaultMaintenanceReadTimeout keeps direct programmatic store construction aligned with the shipped maintenance-tool defaults when callers omit dedicated maintenance read budgets.
	// defaultMaintenanceReadTimeout 用于在调用方省略专用维护读取预算时，让代码内直接构造 store 的行为仍与随仓库分发的维护工具默认值保持一致。
	defaultMaintenanceReadTimeout = 30 * time.Second

	// defaultMaintenanceWriteTimeout keeps direct programmatic store construction aligned with the shipped maintenance-tool defaults for destructive maintenance transactions.
	// defaultMaintenanceWriteTimeout 用于在调用方省略破坏性维护事务预算时，让代码内直接构造 store 的行为仍与仓库分发的维护工具默认值保持一致。
	defaultMaintenanceWriteTimeout = 10 * time.Minute
)

// Config stores the runtime connection and dialect options required by the PostgreSQL combined-store adapter.
// Config 用于保存 PostgreSQL 组合库适配器运行时所需的连接与方言配置。
type Config struct {
	DSN                     string
	Schema                  string
	Flavor                  string
	QueryTimeout            time.Duration
	MaintenanceReadTimeout  time.Duration
	MaintenanceWriteTimeout time.Duration
	ConnectTimeout          time.Duration
	MaxOpenConns            int
	MinIdleConns            int
	AutoCreateExtensions    bool
	BM25IndexConcurrently   bool
	BM25IndexName           string
	TRGMSimilarityThreshold float64
	VectorLists             int
	VectorProbes            int
	MigrationBatchSize      int
	EmbeddingDimension      int
}

// memoryTableResolver defines the minimum surface the dialect SQL builders need from the combined store.
// memoryTableResolver 用于定义方言 SQL 构建器对组合库的最小依赖面。
type memoryTableResolver interface {
	memoryNodesTable() string
	trgmSimilarityThreshold() float64
}

// Store acts as the public PostgreSQL combined-store facade while delegating shared runtime state and future repository ownership to internal repository bundles.
// Store 用于作为 PostgreSQL 组合库对外暴露的统一门面，并把共享运行时状态与后续仓储职责收口到内部 repository bundle。
type Store struct {
	// shared and repos express the intended runtime boundary: one shared core plus one explicit repository bundle.
	// shared 与 repos 用于表达目标运行时边界：一个共享核心，加上一组显式 repository bundle。
	shared *storeShared
	repos  storeRepositories

	// pool/cfg/dialect stay on Store as compatibility mirrors so existing focused tests can still construct lightweight Store literals directly.
	// pool/cfg/dialect 暂时保留在 Store 上作为兼容镜像，保证现有轻量测试仍可直接通过 Store 字面量构造实例。
	pool    *pgxpool.Pool
	cfg     Config
	dialect searchDialect
}

// NewStore dials PostgreSQL, validates the configured dialect extensions, and bootstraps the shared schema metadata required by the combined runtime.
// NewStore 用于连接 PostgreSQL、校验当前方言所需扩展，并初始化组合运行时需要的共享 schema 元数据。
func NewStore(cfg Config) (*Store, error) {
	cfg = normalizeConfig(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	poolCfg.MinConns = int32(cfg.MinIdleConns)
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("dial postgres: %w", err)
	}
	store := &Store{
		shared: &storeShared{
			pool:    pool,
			cfg:     cfg,
			dialect: newSearchDialect(cfg.Flavor),
		},
		pool:    pool,
		cfg:     cfg,
		dialect: newSearchDialect(cfg.Flavor),
	}
	store.repos = newStoreRepositories(store.shared)
	if err := store.init(context.Background()); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

// Shutdown closes the shared PostgreSQL pool used by the combined-store runtime.
// Shutdown 用于关闭组合库运行时使用的 PostgreSQL 共享连接池。
func (s *Store) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.pool == nil {
		return nil
	}
	s.pool.Close()
	return nil
}

// GetSchemaComponentVersion loads one shared PostgreSQL combined-store schema version row from the component version table.
// GetSchemaComponentVersion 用于从组件版本表读取一条 PostgreSQL 组合库 schema 版本记录。
func (s *Store) GetSchemaComponentVersion(ctx context.Context, component string) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	component = strings.TrimSpace(component)
	if component == "" {
		component = defaultSchemaVersionComponent
	}
	query := fmt.Sprintf(`SELECT schema_version FROM %s WHERE component = $1`, s.schemaTable())
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	var version int
	err := s.pool.QueryRow(callCtx, query, component).Scan(&version)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no rows") {
			return 0, nil
		}
		return 0, fmt.Errorf("query postgres schema version for %s: %w", component, err)
	}
	return version, nil
}

// SetSchemaComponentVersion upserts one shared PostgreSQL combined-store schema version row into the component version table.
// SetSchemaComponentVersion 用于把一条 PostgreSQL 组合库 schema 版本记录 upsert 到组件版本表。
func (s *Store) SetSchemaComponentVersion(ctx context.Context, component string, version int) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	component = strings.TrimSpace(component)
	if component == "" {
		component = defaultSchemaVersionComponent
	}
	query := fmt.Sprintf(`
INSERT INTO %s (component, schema_version, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (component)
DO UPDATE SET schema_version = EXCLUDED.schema_version, updated_at = EXCLUDED.updated_at
`, s.schemaTable())
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	if _, err := s.pool.Exec(callCtx, query, component, version); err != nil {
		return fmt.Errorf("persist postgres schema version for %s: %w", component, err)
	}
	return nil
}

// init bootstraps the shared schema version table and validates the configured search dialect before the runtime starts serving traffic.
// init 用于在运行时开始对外服务前，初始化共享 schema 版本表并校验当前搜索方言。
func (s *Store) init(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if err := s.dialect.EnsureSearchExtensions(ctx, s.pool, s.cfg.AutoCreateExtensions); err != nil {
		return err
	}
	if err := s.repos.maintenance.ensureSchema(ctx); err != nil {
		return err
	}
	return nil
}

// schemaTable returns the fully-qualified component-version table name under the configured PostgreSQL schema.
// schemaTable 用于返回配置 schema 下完整限定的组件版本表名。
func (s *Store) schemaTable() string {
	return s.qualifiedTable("vmm_schema_versions")
}

// trgmSimilarityThreshold returns the configured trigram similarity threshold used by lexical SQL generation.
// trgmSimilarityThreshold 用于返回 lexical SQL 生成所需的配置 trigram 相似度阈值。
func (s *Store) trgmSimilarityThreshold() float64 {
	return s.cfg.TRGMSimilarityThreshold
}

// queryContext derives one bounded query context so shared PostgreSQL calls stay inside the configured runtime timeout.
// queryContext 用于派生一个有界查询上下文，确保 PostgreSQL 共享调用始终受运行时超时约束。
func (s *Store) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

// normalizeConfig canonicalizes PostgreSQL combined-store settings before the adapter dials the pool so startup errors stay deterministic.
// normalizeConfig 用于在适配器拨号前规范化 PostgreSQL 组合库配置，确保启动错误保持确定性。
func normalizeConfig(cfg Config) Config {
	cfg.DSN = strings.TrimSpace(cfg.DSN)
	cfg.Schema = strings.TrimSpace(cfg.Schema)
	if cfg.Schema == "" {
		cfg.Schema = "public"
	}
	cfg.Flavor = strings.ToLower(strings.TrimSpace(cfg.Flavor))
	if cfg.Flavor == "" {
		cfg.Flavor = "paradedb"
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 5 * time.Second
	}
	if cfg.MaintenanceReadTimeout <= 0 {
		cfg.MaintenanceReadTimeout = defaultMaintenanceReadTimeout
	}
	if cfg.MaintenanceWriteTimeout <= 0 {
		cfg.MaintenanceWriteTimeout = defaultMaintenanceWriteTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 10
	}
	if cfg.MinIdleConns < 0 {
		cfg.MinIdleConns = 0
	}
	if strings.TrimSpace(cfg.BM25IndexName) == "" {
		cfg.BM25IndexName = "vmm_memory_nodes_bm25_idx"
	}
	if cfg.TRGMSimilarityThreshold <= 0 {
		cfg.TRGMSimilarityThreshold = 0.2
	}
	if cfg.VectorLists <= 0 {
		cfg.VectorLists = 100
	}
	if cfg.VectorProbes <= 0 {
		cfg.VectorProbes = 10
	}
	if cfg.MigrationBatchSize <= 0 {
		cfg.MigrationBatchSize = 500
	}
	return cfg
}

// validateConfig checks the minimum PostgreSQL combined-store runtime contract before the adapter attempts to dial the pool.
// validateConfig 用于在适配器尝试连接连接池前校验 PostgreSQL 组合库的最小运行时契约。
func validateConfig(cfg Config) error {
	if cfg.DSN == "" {
		return fmt.Errorf("postgres dsn is required")
	}
	if cfg.EmbeddingDimension <= 0 {
		return fmt.Errorf("postgres embedding dimension must be > 0")
	}
	switch cfg.Flavor {
	case "paradedb", "standard":
	default:
		return fmt.Errorf("unsupported postgres flavor: %s", cfg.Flavor)
	}
	return nil
}

// quoteIdentifier escapes one PostgreSQL identifier so schema-qualified DDL stays deterministic and safe for runtime-generated names.
// quoteIdentifier 用于转义 PostgreSQL 标识符，确保运行时生成的 schema 与表名在 DDL 中保持确定且安全。
func quoteIdentifier(raw string) string {
	return `"` + strings.ReplaceAll(strings.TrimSpace(raw), `"`, `""`) + `"`
}

// LoadRecentDirectMemoryWrites delegates to the analysis repository so existing port interfaces continue to compile while ownership moves inward.
// LoadRecentDirectMemoryWrites 用于委托给 analysis repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadRecentDirectMemoryWrites(ctx context.Context, session logicdomain.SessionRef, observedAfter, observedBefore time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	return s.repos.analysis.LoadRecentDirectMemoryWrites(ctx, session, observedAfter, observedBefore)
}

// ApplyMemoryAdoption delegates to the analysis repository so existing port interfaces continue to compile while ownership moves inward.
// ApplyMemoryAdoption 用于委托给 analysis repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error {
	return s.repos.analysis.ApplyMemoryAdoption(ctx, session, memoryIDs, adoptedAt)
}

// ApplyTurnAnalysis delegates to the analysis repository so existing port interfaces continue to compile while ownership moves inward.
// ApplyTurnAnalysis 用于委托给 analysis repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	return s.repos.analysis.ApplyTurnAnalysis(ctx, session, turn, analysis)
}

// LoadProfileTargets delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// LoadProfileTargets 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	return s.repos.profile.LoadProfileTargets(ctx, session)
}

// LoadProfileReviewTargets delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// LoadProfileReviewTargets 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	return s.repos.profile.LoadProfileReviewTargets(ctx, session)
}

// ListActiveProfileNodes delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// ListActiveProfileNodes 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	return s.repos.profile.ListActiveProfileNodes(ctx, target, limit)
}

// LoadRenderedProfile delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// LoadRenderedProfile 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	return s.repos.profile.LoadRenderedProfile(ctx, target)
}

// CreateProfileInstruction delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// CreateProfileInstruction 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	return s.repos.profile.CreateProfileInstruction(ctx, record)
}

// FailProfileInstruction delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// FailProfileInstruction 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error {
	return s.repos.profile.FailProfileInstruction(ctx, instructionID, failureReason, reviewResult)
}

// ApplyManualProfileInstruction delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// ApplyManualProfileInstruction 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	return s.repos.profile.ApplyManualProfileInstruction(ctx, target, instruction, nodes, retired, renderedProfile, reviewResult)
}

// ConvergeExpiredProfileNodes delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// ConvergeExpiredProfileNodes 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	return s.repos.profile.ConvergeExpiredProfileNodes(ctx, limit)
}

// ReplaceRenderedProfiles delegates to the profile repository so existing port interfaces continue to compile while ownership moves inward.
// ReplaceRenderedProfiles 用于委托给 profile repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error {
	return s.repos.profile.ReplaceRenderedProfiles(ctx, updates)
}
