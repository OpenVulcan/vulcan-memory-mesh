// store.go implements the shared PostgreSQL combined-store adapter used by the dialect-pattern runtime.
// store.go 用于实现 Dialect Pattern 运行时共享的 PostgreSQL 组合库适配器。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// defaultSchemaVersionComponent stores the shared PostgreSQL combined-store schema version under one stable component key.
	// defaultSchemaVersionComponent 用于把 PostgreSQL 组合库的共享 schema 版本固定到稳定的组件键名下。
	defaultSchemaVersionComponent = "postgres_combined"
)

// Config stores the runtime connection and dialect options required by the PostgreSQL combined-store adapter.
// Config 用于保存 PostgreSQL 组合库适配器运行时所需的连接与方言配置。
type Config struct {
	DSN                     string
	Schema                  string
	Flavor                  string
	QueryTimeout            time.Duration
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

// Store holds the shared PostgreSQL pool, resolved dialect, and minimal shared schema metadata for the combined-store runtime.
// Store 用于持有 PostgreSQL 组合库运行时共享的连接池、已解析方言与最小共享 schema 元数据。
type Store struct {
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
		pool:    pool,
		cfg:     cfg,
		dialect: newSearchDialect(cfg.Flavor),
	}
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
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	return nil
}

// schemaTable returns the fully-qualified component-version table name under the configured PostgreSQL schema.
// schemaTable 用于返回配置 schema 下完整限定的组件版本表名。
func (s *Store) schemaTable() string {
	return s.qualifiedTable("vmm_schema_versions")
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
