// store.go implements the SQLite-gateway outbound adapter used as the durable SQL source of truth.
// store.go 用于实现 SQLite 网关适配器，并把它作为长期 SQL 事实来源。
package vldb_sqlite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

const (
	// currentSchemaVersion tracks the newest SQLite schema version understood by this runtime.
	// currentSchemaVersion 用于标记当前运行时理解的最新 SQLite 表结构版本。
	currentSchemaVersion = 20

	// debugSeedUserID keeps the testing-stage default user row stable so grpc debugging can immediately target user_id=1.
	// debugSeedUserID 用于固定测试阶段的默认用户行，便于 gRPC 调试时直接使用 user_id=1。
	debugSeedUserID = 1

	// debugSeedTeamID keeps the testing-stage default team row stable so project hierarchy bootstraps deterministically.
	// debugSeedTeamID 用于固定测试阶段的默认 team 行，确保项目层级启动时保持确定性。
	debugSeedTeamID = 1

	// debugSeedSpaceID keeps the testing-stage default space row stable so project_id=1 always resolves through the same hierarchy.
	// debugSeedSpaceID 用于固定测试阶段的默认 space 行，确保 project_id=1 始终通过同一层级解析。
	debugSeedSpaceID = 1

	// debugSeedProjectID keeps the testing-stage default project row stable so grpc debugging can immediately target project_id=1.
	// debugSeedProjectID 用于固定测试阶段的默认项目行，便于 gRPC 调试时直接使用 project_id=1。
	debugSeedProjectID = 1

	// debugSeedDefaultName is reused across the default debug user/team/space/project rows so the initial hierarchy is predictable after every reset.
	// debugSeedDefaultName 用于复用默认调试 user/team/space/project 的名称，保证每次重置后的初始层级可预测。
	debugSeedDefaultName = logicdomain.DefaultWorkspaceResourceName

	// sqliteRetryMaxAttempts keeps retryable SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA responses bounded so callers do not spin forever.
	// sqliteRetryMaxAttempts 用于限制 SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA 这类可重试响应的最大重试次数，避免调用方无限自旋。
	sqliteRetryMaxAttempts = 3

	// sqliteRetryBaseDelay is the small exponential-backoff seed used after one local FFI SQLite call reports a retryable busy/locked/schema error.
	// sqliteRetryBaseDelay 用于在本地 FFI SQLite 调用报告 busy/locked/schema 可重试错误后，提供一个较小的指数退避起始值。
	sqliteRetryBaseDelay = 50 * time.Millisecond

	// memoryFTSIndexName pins the dedicated SQLite FTS document table used for durable memory lexical retrieval,
	// so the library can own one standalone fts5 table instead of colliding with the relational source table.
	// memoryFTSIndexName 用于固定长期记忆 lexical 检索所使用的独立 SQLite FTS 文档表名称，
	// 让库侧能够维护单独的 fts5 表，而不会与关系源表发生命名冲突。
	memoryFTSIndexName = "vmm_memory_nodes_fts"

	// memoryLexicalResultLimit caps final lexical hits so SQLite split-mode recall stays aligned with the existing query fan-out budget.
	// memoryLexicalResultLimit 用于限制最终 lexical 命中数，让 SQLite split 模式召回保持现有查询扇出预算。
	memoryLexicalResultLimit = 32

	// memoryLexicalCandidateLimit caps pre-filter FTS candidates so expired or inactive rows cannot crowd out valid rows before relational filtering.
	// memoryLexicalCandidateLimit 用于限制过滤前 FTS 候选数，避免过期或非 active 行在关系过滤前挤掉有效结果。
	memoryLexicalCandidateLimit = 128
)

const currentSchemaSQL = `
BEGIN IMMEDIATE;
CREATE TABLE IF NOT EXISTS vmm_noise_embeddings (
  scope TEXT NOT NULL,
  language TEXT NOT NULL,
  category_name TEXT NOT NULL,
  phrase TEXT NOT NULL,
  model TEXT NOT NULL,
  dimension INTEGER NOT NULL,
  rules_hash TEXT NOT NULL,
  vector_json TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (scope, language, category_name, phrase, model, dimension, rules_hash)
);

CREATE TABLE IF NOT EXISTS vmm_users (
  id BIGINT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  profile TEXT NOT NULL DEFAULT '',
  delete_confirm_code TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_teams (
  id BIGINT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  profile TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vmm_spaces (
  id BIGINT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  profile TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(team_id, name),
  FOREIGN KEY(team_id) REFERENCES vmm_teams(id)
);

CREATE TABLE IF NOT EXISTS vmm_projects (
  id BIGINT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  profile TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(space_id, name),
  FOREIGN KEY(team_id) REFERENCES vmm_teams(id),
  FOREIGN KEY(space_id) REFERENCES vmm_spaces(id)
);

CREATE TABLE IF NOT EXISTS vmm_sessions (
  id BIGINT PRIMARY KEY,
  session_key TEXT NOT NULL,
  user_id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  turn_count INTEGER NOT NULL DEFAULT 0,
  last_summarized_id BIGINT NOT NULL DEFAULT 0,
  last_compacted_turn_id BIGINT NOT NULL DEFAULT 0,
  summarize_content TEXT NOT NULL DEFAULT '',
  summarize_budget INTEGER NOT NULL DEFAULT 0,
  last_extract_observed_timestamp BIGINT NOT NULL DEFAULT 0,
  last_extract_completed_timestamp BIGINT NOT NULL DEFAULT 0,
  last_compacted_timestamp BIGINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  UNIQUE(project_id, session_key),
  FOREIGN KEY(user_id) REFERENCES vmm_users(id),
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_scope ON vmm_sessions(user_id, team_id, space_id, project_id, updated_timestamp);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_project_session ON vmm_sessions(project_id, session_key);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_compact_boundary ON vmm_sessions(project_id, last_compacted_turn_id, id);

CREATE TABLE IF NOT EXISTS vmm_turn_records (
  id BIGINT PRIMARY KEY,
  session_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  dehydrated_content TEXT NOT NULL,
  dehydrated_budget INTEGER NOT NULL DEFAULT 0,
  extracted_status TINYINT NOT NULL DEFAULT 0,
  details TEXT NOT NULL DEFAULT '',
  details_budget INTEGER NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_session ON vmm_turn_records(session_id, id);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_project_status ON vmm_turn_records(project_id, extracted_status, id);

CREATE TABLE IF NOT EXISTS vmm_memory_nodes (
  id BIGINT PRIMARY KEY,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  origin_session_id BIGINT NOT NULL DEFAULT 0,
  source_turn_id BIGINT,
  vector_id TEXT NOT NULL,
  vector_json TEXT NOT NULL DEFAULT '[]',
  source_kind INTEGER NOT NULL DEFAULT 0,
  scope_level INTEGER NOT NULL DEFAULT 1,
  category INTEGER NOT NULL,
  abstract TEXT NOT NULL,
  details TEXT NOT NULL,
  memory_status INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 2,
  memory_level INTEGER NOT NULL DEFAULT 2,
  refresh_weight INTEGER NOT NULL DEFAULT 0,
  support_count INTEGER NOT NULL DEFAULT 0,
  rebuttal_count INTEGER NOT NULL DEFAULT 0,
  status_reason TEXT NOT NULL DEFAULT '',
  expires_timestamp BIGINT NOT NULL DEFAULT 0,
  last_recalled_timestamp BIGINT NOT NULL DEFAULT 0,
  last_adopted_timestamp BIGINT NOT NULL DEFAULT 0,
  last_reinforced_timestamp BIGINT NOT NULL DEFAULT 0,
  recalled_count INTEGER NOT NULL DEFAULT 0,
  adopted_count INTEGER NOT NULL DEFAULT 0,
  reinforcement_count INTEGER NOT NULL DEFAULT 0,
  cross_session_adopted_count INTEGER NOT NULL DEFAULT 0,
  decay_disabled INTEGER NOT NULL DEFAULT 0,
  dedupe_hash TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_project_active_window ON vmm_memory_nodes(project_id, memory_status, expires_timestamp, id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_source_turn ON vmm_memory_nodes(source_turn_id, id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_origin_session_active ON vmm_memory_nodes(origin_session_id, memory_status, expires_timestamp, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vmm_memory_nodes_vector ON vmm_memory_nodes(vector_id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_dedupe_window ON vmm_memory_nodes(origin_session_id, project_id, user_id, source_kind, scope_level, dedupe_hash, memory_status, expires_timestamp, created_timestamp);

CREATE TABLE IF NOT EXISTS vmm_memory_context_edges (
  memory_id BIGINT NOT NULL,
  context_key TEXT NOT NULL,
  context_value TEXT NOT NULL,
  support_count INTEGER NOT NULL DEFAULT 0,
  rebuttal_count INTEGER NOT NULL DEFAULT 0,
  last_supported_timestamp BIGINT NOT NULL DEFAULT 0,
  last_rebutted_timestamp BIGINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (memory_id, context_key, context_value),
  FOREIGN KEY(memory_id) REFERENCES vmm_memory_nodes(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_lookup ON vmm_memory_context_edges(context_key, context_value, memory_id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_memory ON vmm_memory_context_edges(memory_id);

CREATE TABLE IF NOT EXISTS vmm_recycle_batches (
  id BIGINT PRIMARY KEY,
  recycle_type TEXT NOT NULL,
  session_id BIGINT NOT NULL DEFAULT 0,
  project_id BIGINT NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  recycled_at BIGINT NOT NULL DEFAULT 0,
  purged_at BIGINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_recycle_batches_lookup ON vmm_recycle_batches(recycle_type, recycled_at, id);
CREATE INDEX IF NOT EXISTS idx_vmm_recycle_batches_session ON vmm_recycle_batches(session_id, project_id, id);

CREATE TABLE IF NOT EXISTS vmm_recycle_jobs (
  id BIGINT PRIMARY KEY,
  session_id BIGINT NOT NULL DEFAULT 0,
  project_id BIGINT NOT NULL DEFAULT 0,
  job_type TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_run_timestamp BIGINT NOT NULL DEFAULT 0,
  claimed_timestamp BIGINT NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_unique ON vmm_recycle_jobs(session_id, job_type);
CREATE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_pending ON vmm_recycle_jobs(job_type, next_run_timestamp, id);
CREATE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_session ON vmm_recycle_jobs(session_id, project_id, id);

CREATE TABLE IF NOT EXISTS vmm_memory_nodes_trash (
  batch_id BIGINT NOT NULL,
  recycled_at BIGINT NOT NULL DEFAULT 0,
  recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL,
  team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  origin_session_id BIGINT NOT NULL DEFAULT 0,
  source_turn_id BIGINT,
  vector_id TEXT NOT NULL,
  vector_json TEXT NOT NULL DEFAULT '[]',
  source_kind INTEGER NOT NULL DEFAULT 0,
  scope_level INTEGER NOT NULL DEFAULT 1,
  category INTEGER NOT NULL,
  abstract TEXT NOT NULL,
  details TEXT NOT NULL,
  memory_status INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 2,
  memory_level INTEGER NOT NULL DEFAULT 2,
  refresh_weight INTEGER NOT NULL DEFAULT 0,
  support_count INTEGER NOT NULL DEFAULT 0,
  rebuttal_count INTEGER NOT NULL DEFAULT 0,
  status_reason TEXT NOT NULL DEFAULT '',
  expires_timestamp BIGINT NOT NULL DEFAULT 0,
  last_recalled_timestamp BIGINT NOT NULL DEFAULT 0,
  last_adopted_timestamp BIGINT NOT NULL DEFAULT 0,
  last_reinforced_timestamp BIGINT NOT NULL DEFAULT 0,
  recalled_count INTEGER NOT NULL DEFAULT 0,
  adopted_count INTEGER NOT NULL DEFAULT 0,
  reinforcement_count INTEGER NOT NULL DEFAULT 0,
  cross_session_adopted_count INTEGER NOT NULL DEFAULT 0,
  decay_disabled INTEGER NOT NULL DEFAULT 0,
  dedupe_hash TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_trash_recycled ON vmm_memory_nodes_trash(recycled_at, batch_id, id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_trash_batch ON vmm_memory_nodes_trash(batch_id, id);

CREATE TABLE IF NOT EXISTS vmm_memory_context_edges_trash (
  batch_id BIGINT NOT NULL,
  recycled_at BIGINT NOT NULL DEFAULT 0,
  recycle_reason TEXT NOT NULL DEFAULT '',
  memory_id BIGINT NOT NULL,
  context_key TEXT NOT NULL,
  context_value TEXT NOT NULL,
  support_count INTEGER NOT NULL DEFAULT 0,
  rebuttal_count INTEGER NOT NULL DEFAULT 0,
  last_supported_timestamp BIGINT NOT NULL DEFAULT 0,
  last_rebutted_timestamp BIGINT NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (batch_id, memory_id, context_key, context_value)
);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_trash_recycled ON vmm_memory_context_edges_trash(recycled_at, batch_id, memory_id);
CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_trash_batch ON vmm_memory_context_edges_trash(batch_id, memory_id);

CREATE TABLE IF NOT EXISTS vmm_turn_records_trash (
  batch_id BIGINT NOT NULL,
  recycled_at BIGINT NOT NULL DEFAULT 0,
  recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL,
  session_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  dehydrated_content TEXT NOT NULL,
  dehydrated_budget INTEGER NOT NULL DEFAULT 0,
  extracted_status TINYINT NOT NULL DEFAULT 0,
  details TEXT NOT NULL DEFAULT '',
  details_budget INTEGER NOT NULL DEFAULT 0,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_trash_recycled ON vmm_turn_records_trash(recycled_at, batch_id, id);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_trash_batch ON vmm_turn_records_trash(batch_id, session_id, id);

CREATE TABLE IF NOT EXISTS vmm_vector_gc_jobs (
  id BIGINT PRIMARY KEY,
  batch_id BIGINT NOT NULL DEFAULT 0,
  vector_id TEXT NOT NULL,
  job_type TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_run_timestamp BIGINT NOT NULL DEFAULT 0,
  claimed_timestamp BIGINT NOT NULL DEFAULT 0,
  completed_timestamp BIGINT NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_unique ON vmm_vector_gc_jobs(vector_id, job_type, batch_id);
CREATE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_pending ON vmm_vector_gc_jobs(completed_timestamp, next_run_timestamp, id);
CREATE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_batch ON vmm_vector_gc_jobs(batch_id, id);

CREATE TABLE IF NOT EXISTS vmm_profile_nodes (
  id BIGINT PRIMARY KEY,
  turn_id BIGINT,
  profile_type TINYINT NOT NULL,
  bind_id BIGINT NOT NULL,
  content TEXT NOT NULL,
  profile_status TINYINT NOT NULL DEFAULT 1,
  priority TINYINT NOT NULL DEFAULT 2,
  profile_level TINYINT NOT NULL DEFAULT 0,
  level_reason TEXT NOT NULL DEFAULT '',
  refresh_weight INTEGER NOT NULL DEFAULT 0,
  source_kind TINYINT NOT NULL DEFAULT 0,
  source_id BIGINT NOT NULL DEFAULT 0,
  status_reason TEXT NOT NULL DEFAULT '',
  expires_timestamp BIGINT NOT NULL DEFAULT 0,
  superseded_by_id BIGINT NOT NULL DEFAULT 0,
  profile_date TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_bind_status ON vmm_profile_nodes(profile_type, bind_id, profile_status, id);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_active_window ON vmm_profile_nodes(profile_type, bind_id, profile_status, expires_timestamp, id);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_turn ON vmm_profile_nodes(turn_id, id);

CREATE TABLE IF NOT EXISTS vmm_profile_instructions (
  id BIGINT PRIMARY KEY,
  profile_type TINYINT NOT NULL,
  bind_id BIGINT NOT NULL,
  instruction TEXT NOT NULL,
  instruction_status TINYINT NOT NULL DEFAULT 0,
  review_result_json TEXT NOT NULL DEFAULT '',
  failure_reason TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_instructions_target ON vmm_profile_instructions(profile_type, bind_id, id);

COMMIT;
`

// Store is the SQLite local-FFI adapter used for hierarchy metadata, session/turn persistence, cache storage, and built-in FTS.
// Store 用于作为 SQLite 本地 FFI 适配器，承接层级元数据、session/turn 持久化、缓存存储以及内建 FTS。
type Store struct {
	lib           *sqliteffi.Library
	runtime       *sqliteffi.Runtime
	database      sqliteDatabaseHandle
	timeout       time.Duration
	writeMu       sync.Mutex
	tokenizerMode sqliteffi.TokenizerMode
	ftsIndexName  string
}

// sqliteDatabaseHandle narrows the SQLite FFI surface that the adapter depends on so unit tests can replace the local database handle with one focused in-memory fake.
// sqliteDatabaseHandle 用于收窄适配器依赖的 SQLite FFI 能力面，这样单测就可以把本地数据库句柄替换成一个聚焦的内存 fake。
type sqliteDatabaseHandle interface {
	ExecuteScript(sql string, params []sqliteffi.SQLValue, paramsJSON string) (sqliteffi.ExecuteResult, error)
	ExecuteBatch(sql string, items [][]sqliteffi.SQLValue) (sqliteffi.ExecuteResult, error)
	QueryJSON(sql string, params []sqliteffi.SQLValue, paramsJSON string) (sqliteffi.QueryJSONResult, error)
	EnsureFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error)
	RebuildFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error)
	UpsertFtsDocument(indexName string, mode sqliteffi.TokenizerMode, id string, filePath string, title string, content string) (sqliteffi.FtsMutationResult, error)
	DeleteFtsDocument(indexName string, id string) (sqliteffi.FtsMutationResult, error)
	SearchFts(indexName string, mode sqliteffi.TokenizerMode, query string, limit uint32, offset uint32) (sqliteffi.SearchResult, error)
	Close() error
}

// hasSQLiteStore reports whether the adapter currently holds one local SQLite database handle.
// hasSQLiteStore 用于判断适配器当前是否持有一个本地 SQLite 数据库句柄。
func (s *Store) hasSQLiteStore() bool {
	return s != nil && s.database != nil
}

// StoreOptions collects optional SQLite adapter features that change local FTS behavior without affecting unrelated callers.
// StoreOptions 用于收集会改变本地 FTS 行为、但不会影响其他调用方的 SQLite 适配器可选特性。
type StoreOptions struct {
	TokenizerMode string
}

// NewStore opens the packaged SQLite dynamic library and ensures the local schema and built-in FTS index are initialized before serving traffic.
// NewStore 用于打开打包后的 SQLite 动态库，并在对外提供服务前确保本地表结构和内建 FTS 索引已经初始化。
func NewStore(libraryPath string, databasePath string, timeout time.Duration, options ...StoreOptions) (*Store, error) {
	if strings.TrimSpace(libraryPath) == "" {
		return nil, fmt.Errorf("sqlite library path is required")
	}
	if strings.TrimSpace(databasePath) == "" {
		return nil, fmt.Errorf("sqlite database path is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	storeOptions := StoreOptions{TokenizerMode: "jieba"}
	if len(options) > 0 {
		storeOptions = options[0]
	}

	if err := os.MkdirAll(filepathDir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite database dir: %w", err)
	}
	lib, err := sqliteffi.Open(strings.TrimSpace(libraryPath))
	if err != nil {
		return nil, err
	}
	runtimeHandle, err := lib.CreateRuntime()
	if err != nil {
		_ = lib.Close()
		return nil, fmt.Errorf("create sqlite runtime: %w", err)
	}
	databaseHandle, err := runtimeHandle.OpenDatabase(strings.TrimSpace(databasePath))
	if err != nil {
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	tokenizerMode, err := parseSQLiteTokenizerMode(storeOptions.TokenizerMode)
	if err != nil {
		_ = databaseHandle.Close()
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, err
	}

	store := &Store{
		lib:           lib,
		runtime:       runtimeHandle,
		database:      databaseHandle,
		timeout:       timeout,
		tokenizerMode: tokenizerMode,
		ftsIndexName:  memoryFTSIndexName,
	}
	if err := store.init(context.Background()); err != nil {
		_ = store.Shutdown(context.Background())
		return nil, err
	}
	return store, nil
}

// Shutdown closes the opened database, runtime, and dynamic-library handles used by the SQLite local-FFI adapter.
// Shutdown 用于关闭 SQLite 本地 FFI 适配器所使用的数据库、运行时和动态库句柄。
func (s *Store) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil {
		return nil
	}
	var closeErr error
	if s.database != nil {
		closeErr = s.database.Close()
		s.database = nil
	}
	if s.runtime != nil {
		if err := s.runtime.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		s.runtime = nil
	}
	if s.lib != nil {
		if err := s.lib.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		s.lib = nil
	}
	return closeErr
}

// init ensures the SQLite schema is bootstrapped or incrementally migrated before the store starts serving traffic.
// init 用于在存储开始提供服务前，确保 SQLite schema 已完成初始化或增量迁移。
func (s *Store) init(ctx context.Context) error {
	if err := s.ensureSQLiteSchema(ctx); err != nil {
		return err
	}
	return s.ensureBuiltinMemoryFTS(ctx)
}

// parameterizedDebugSeedWorkspaceStatements builds deterministic testing-stage seed writes as single-statement typed-param calls because the real FFI only accepts flat params for one SQL statement.
// parameterizedDebugSeedWorkspaceStatements 用于把确定性的测试阶段 seed 构造成单语句强类型参数调用，因为真实 FFI 只接受单条 SQL 的扁平参数。
func parameterizedDebugSeedWorkspaceStatements(now time.Time) []sqliteWriteStatement {
	now = now.UTC()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	return []sqliteWriteStatement{
		{
			SQL: `
INSERT INTO vmm_users (id, name, profile, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', '', ?, ?);
`,
			Params: []any{debugSeedUserID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_teams (id, name, profile, created_at, updated_at)
VALUES (?, ?, '', ?, ?);
`,
			Params: []any{debugSeedTeamID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_spaces (id, team_id, name, profile, created_at, updated_at)
VALUES (?, ?, ?, '', ?, ?);
`,
			Params: []any{debugSeedSpaceID, debugSeedTeamID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
		{
			SQL: `
INSERT INTO vmm_projects (id, team_id, space_id, name, profile, created_at, updated_at)
VALUES (?, ?, ?, ?, '', ?, ?);
`,
			Params: []any{debugSeedProjectID, debugSeedTeamID, debugSeedSpaceID, debugSeedDefaultName, nowRFC3339, nowRFC3339},
		},
	}
}

// exec sends one SQL statement or script to the SQLite gateway and prefers native typed params for the sqlite-first runtime path.
// exec 用于把单条 SQL 或脚本发送给 SQLite 网关，并在 sqlite-first 运行路径里优先使用原生强类型参数。
func (s *Store) exec(ctx context.Context, sql string, params ...any) error {
	_, err := s.execResult(ctx, sql, params...)
	return err
}

// execResult sends one SQL statement or script to SQLite and returns the execution metadata for callers that need affected-row checks.
// execResult 用于把单条 SQL 或脚本发送给 SQLite，并把执行元数据返回给需要校验影响行数的调用方。
func (s *Store) execResult(ctx context.Context, sql string, params ...any) (sqliteffi.ExecuteResult, error) {
	if !s.hasSQLiteStore() {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return sqliteffi.ExecuteResult{}, err
	}
	resp, err := s.executeScript(ctx, strings.TrimSpace(sql), prepared.Values)
	if err != nil {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite execute: %w", err)
	}
	if !resp.Success {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite execute: %s", strings.TrimSpace(resp.Message))
	}
	return resp, nil
}

// execExpectRowsChanged executes one write and requires SQLite to report the exact affected-row count for state-machine updates.
// execExpectRowsChanged 用于执行一次写入，并要求 SQLite 返回精确影响行数，服务于状态机类更新的命中校验。
func (s *Store) execExpectRowsChanged(ctx context.Context, operation string, expectedRows int64, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return err
	}
	if resp.RowsChanged != expectedRows {
		return fmt.Errorf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows)
	}
	return nil
}

// sqlitePartialMutationError converts a post-mutation failure into the shared uncertain-outcome contract so upper layers do not add unsafe cleanup or failure writes.
// sqlitePartialMutationError 用于把已发生部分写入后的失败转换为统一的结果不确定契约，避免上层追加不安全的清理或失败写入。
func sqlitePartialMutationError(mutated bool, operation string, err error) error {
	return sqlitePartialMutationErrorWithFreshVectorReference(mutated, false, operation, err)
}

// sqliteAutocommitRowsChangedDriftError classifies a single SQLite autocommit row-count drift as ordinary when no row changed and outcome-uncertain once any row has already been mutated.
// sqliteAutocommitRowsChangedDriftError 用于分类单条 SQLite 自动提交语句的行数漂移：未改变任何行时返回普通错误，已经改变至少一行时返回结果不确定错误。
func sqliteAutocommitRowsChangedDriftError(operation string, messagePrefix string, rowsChanged, expectedRows int64) error {
	if rowsChanged == expectedRows {
		return nil
	}
	err := fmt.Errorf("%s affected %d rows, want %d", messagePrefix, rowsChanged, expectedRows)
	return sqlitePartialMutationError(rowsChanged > 0, operation, err)
}

// sqliteWriteCommitBoundaryError marks SQLite write failures that report an unknown commit boundary while leaving ordinary write failures unchanged.
// sqliteWriteCommitBoundaryError 用于标记报告提交边界未知的 SQLite 写入失败，同时保持普通写入失败不变。
func sqliteWriteCommitBoundaryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: operation,
			Message:   err.Error(),
		}
	}
	return err
}

// sqliteWriteCommitOrPartialMutationError preserves explicit commit-boundary ambiguity before using confirmed prior writes to classify ordinary failures.
// sqliteWriteCommitOrPartialMutationError 用于优先保留明确的提交边界不确定语义，再用已确认的前序写入分类普通失败。
func sqliteWriteCommitOrPartialMutationError(mutated bool, operation string, err error) error {
	if err == nil {
		return nil
	}
	if logicdomain.IsOutcomeUncertain(err) {
		return err
	}
	if classifiedErr := sqliteWriteCommitBoundaryError(operation, err); logicdomain.IsOutcomeUncertain(classifiedErr) {
		return classifiedErr
	}
	return sqlitePartialMutationError(mutated, operation, err)
}

// sqlitePartialMutationErrorWithFreshVectorReference marks partial SQLite writes and records whether fresh vector rows may now be referenced by durable memory rows.
// sqlitePartialMutationErrorWithFreshVectorReference 用于标记 SQLite 部分写入，并记录新向量是否可能已经被长期 memory 行引用。
func sqlitePartialMutationErrorWithFreshVectorReference(mutated bool, freshVectorReference bool, operation string, err error) error {
	if err == nil {
		return nil
	}
	if !mutated || logicdomain.IsOutcomeUncertain(err) {
		return err
	}
	return logicdomain.OutcomeUncertainError{
		Operation:            operation,
		Message:              err.Error(),
		FreshVectorReference: freshVectorReference,
	}
}

// sqliteWriteStatement stores one ordered write statement plus its typed parameters.
// sqliteWriteStatement 用于保存顺序写入中的单条语句及其强类型参数。
type sqliteWriteStatement struct {
	// SQL stores one executable SQLite statement without transaction control.
	// SQL 用于保存一条不包含事务控制语句的 SQLite 可执行语句。
	SQL string
	// Params stores the typed values bound to SQL placeholders.
	// Params 用于保存绑定到 SQL 占位符的强类型参数值。
	Params []any
}

// turnAnalysisProfileInsertExpectation stores the exact profile-node insert target needed to reconcile an uncertain post-action profile write.
// turnAnalysisProfileInsertExpectation 用于保存 post-action 画像写入不确定时所需对账的精确画像节点插入目标。
type turnAnalysisProfileInsertExpectation struct {
	ID          uint64
	TurnID      uint64
	ProfileType int
	BindID      uint64
	Node        logicdomain.ProfileNodeCandidate
	CreatedMs   int64
}

// turnAnalysisProfileSupersedeExpectation stores the target retirement state for one post-action profile supersede statement.
// turnAnalysisProfileSupersedeExpectation 用于保存一条 post-action 画像替代语句的目标退役状态。
type turnAnalysisProfileSupersedeExpectation struct {
	NodeIDs        []uint64
	SupersededByID uint64
	ExpectedRows   int64
}

// turnAnalysisMemoryContextEdgeExpectation stores the exact edge-table state expected after one post-action context-edge statement.
// turnAnalysisMemoryContextEdgeExpectation 用于保存一条 post-action 情境边语句执行后应达到的精确边表状态。
type turnAnalysisMemoryContextEdgeExpectation struct {
	// MemoryID stores the durable memory row whose context edges are being rewritten.
	// MemoryID 保存正在重写情境边的长期 memory 行 ID。
	MemoryID uint64
	// ExpectedEdges stores the complete edge set that must be visible for the memory row after this statement.
	// ExpectedEdges 保存该语句完成后，这条 memory 行下必须可见的完整情境边集合。
	ExpectedEdges []logicdomain.MemoryContextEdge
	// RequireRowsChanged reports whether the statement must affect an exact number of rows when SQLite returns normally.
	// RequireRowsChanged 表示 SQLite 正常返回时是否必须校验精确影响行数。
	RequireRowsChanged bool
	// ExpectedRows stores the exact row count required when RequireRowsChanged is true.
	// ExpectedRows 保存 RequireRowsChanged 为 true 时要求的精确影响行数。
	ExpectedRows int64
}

// execWriteStatements executes ordered single-statement typed writes without pretending to hold a cross-call SQLite transaction that the real FFI cannot preserve.
// execWriteStatements 用于按顺序执行单语句强类型写入，不伪装成真实 FFI 无法跨调用保留的 SQLite 事务。
func (s *Store) execWriteStatements(ctx context.Context, statements ...sqliteWriteStatement) error {
	if len(statements) == 0 {
		return nil
	}
	for index, statement := range statements {
		if err := s.exec(ctx, statement.SQL, statement.Params...); err != nil {
			return fmt.Errorf("execute sqlite write statement %d: %w", index+1, err)
		}
	}
	return nil
}

// execBatch sends one repeated-shape write workload through SQLite's native ExecuteBatch RPC so sqlite-first storage avoids one-RPC-per-row churn.
// execBatch 用于通过 SQLite 原生 ExecuteBatch RPC 发送同构写入，避免 sqlite-first 存储退化成“一行一次 RPC”。
func (s *Store) execBatch(ctx context.Context, sql string, items [][]any) error {
	_, err := s.execBatchResult(ctx, sql, items)
	return err
}

// execBatchResult sends one repeated-shape write workload and returns SQLite execution metadata for callers that must verify batch row counts.
// execBatchResult 用于发送同构批量写入，并返回 SQLite 执行元数据，供必须校验批量命中行数的调用方使用。
func (s *Store) execBatchResult(ctx context.Context, sql string, items [][]any) (sqliteffi.ExecuteResult, error) {
	if !s.hasSQLiteStore() {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	if len(items) == 0 {
		return sqliteffi.ExecuteResult{Success: true}, nil
	}
	batchItems := make([][]sqliteffi.SQLValue, 0, len(items))
	for idx, item := range items {
		values, err := prepareSQLiteBatchParams(item)
		if err != nil {
			return sqliteffi.ExecuteResult{}, fmt.Errorf("prepare sqlite batch item %d: %w", idx, err)
		}
		batchItems = append(batchItems, values)
	}
	resp, err := s.executeBatch(ctx, strings.TrimSpace(sql), batchItems)
	if err != nil {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite execute batch: %w", err)
	}
	if !resp.Success {
		return sqliteffi.ExecuteResult{}, fmt.Errorf("sqlite execute batch: %s", strings.TrimSpace(resp.Message))
	}
	return resp, nil
}

// queryRows decodes one JSON query response into a typed slice so higher-level methods can stay small and explicit while still preferring native sqlite params.
// queryRows 用于把 JSON 查询结果解码为强类型切片，在保持上层逻辑简洁的同时优先使用 sqlite 原生参数。
func queryRows[T any](s *Store, ctx context.Context, sql string, params ...any) ([]T, error) {
	if !s.hasSQLiteStore() {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	resp, err := s.queryJSON(ctx, strings.TrimSpace(sql), prepared.Values)
	if err != nil {
		return nil, fmt.Errorf("sqlite query: %w", err)
	}
	rows := make([]T, 0)
	body := strings.TrimSpace(resp.JSONData)
	if body == "" {
		return rows, nil
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		return nil, fmt.Errorf("decode sqlite query rows: %w", err)
	}
	return rows, nil
}

// sqlitePreparedParams stores the normalized parameter payload that can be attached to ExecuteScript / QueryJson requests.
// sqlitePreparedParams 用于保存标准化后的参数载荷，便于附加到 ExecuteScript / QueryJson 请求。
type sqlitePreparedParams struct {
	Values []sqliteffi.SQLValue
}

// prepareSQLiteParams converts Go scalar values into the typed SQLite FFI payload expected by the local sqlite-first runtime path.
// prepareSQLiteParams 用于把 Go 标量参数转换成 sqlite-first 本地 FFI 路径期望的强类型载荷。
func prepareSQLiteParams(params []any) (sqlitePreparedParams, error) {
	if len(params) == 0 {
		return sqlitePreparedParams{}, nil
	}
	values := make([]sqliteffi.SQLValue, 0, len(params))
	for idx, param := range params {
		value, err := toSQLiteValue(param)
		if err != nil {
			return sqlitePreparedParams{}, fmt.Errorf("convert sqlite param %d: %w", idx, err)
		}
		values = append(values, value)
	}
	return sqlitePreparedParams{Values: values}, nil
}

// prepareSQLiteBatchParams converts one ExecuteBatch item into native SQLite FFI values.
// prepareSQLiteBatchParams 用于把单个 ExecuteBatch item 转换成原生 SQLite FFI 值。
func prepareSQLiteBatchParams(params []any) ([]sqliteffi.SQLValue, error) {
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	return prepared.Values, nil
}

// toSQLiteValue maps one Go scalar to the SQLite FFI typed value so the adapter can stay sqlite-native by default.
// toSQLiteValue 用于把单个 Go 标量映射为 SQLite FFI 的强类型值，让适配器默认保持 sqlite 原生风格。
func toSQLiteValue(param any) (sqliteffi.SQLValue, error) {
	switch value := param.(type) {
	case nil:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueNull}, nil
	case bool:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueBool, Bool: value}, nil
	case string:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueString, String: value}, nil
	case []byte:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueBytes, Bytes: append([]byte(nil), value...)}, nil
	case json.RawMessage:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueString, String: string(value)}, nil
	case int:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case int8:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case int16:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case int32:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case int64:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: value}, nil
	case uint:
		if uint64(value) > math.MaxInt64 {
			return sqliteffi.SQLValue{}, fmt.Errorf("uint %d overflows sqlite int64 binding", value)
		}
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case uint8:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case uint16:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case uint32:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case uint64:
		if value > math.MaxInt64 {
			return sqliteffi.SQLValue{}, fmt.Errorf("uint64 %d overflows sqlite int64 binding", value)
		}
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueInt64, Int64: int64(value)}, nil
	case float32:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueFloat64, Float64: float64(value)}, nil
	case float64:
		return sqliteffi.SQLValue{Kind: sqliteffi.SQLValueFloat64, Float64: value}, nil
	default:
		return sqliteffi.SQLValue{}, fmt.Errorf("unsupported sqlite param type %T", param)
	}
}

// executeScript performs one local ExecuteScript FFI call with bounded retry logic for retryable busy/locked/schema errors.
// executeScript 用于执行一次本地 ExecuteScript FFI 调用，并对可重试的 busy/locked/schema 错误做有界重试。
func (s *Store) executeScript(ctx context.Context, sql string, params []sqliteffi.SQLValue) (sqliteffi.ExecuteResult, error) {
	var response sqliteffi.ExecuteResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.ExecuteScript(sql, params, "")
		return err
	})
	return response, err
}

// executeBatch performs one local ExecuteBatch FFI call with bounded retry logic so SQLITE_BUSY / SQLITE_LOCKED do not immediately bubble up to the caller.
// executeBatch 用于执行一次本地 ExecuteBatch FFI 调用，并对 SQLITE_BUSY / SQLITE_LOCKED 之类错误做有界重试，避免立刻上抛给调用方。
func (s *Store) executeBatch(ctx context.Context, sql string, items [][]sqliteffi.SQLValue) (sqliteffi.ExecuteResult, error) {
	var response sqliteffi.ExecuteResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.ExecuteBatch(sql, items)
		return err
	})
	return response, err
}

// queryJSON performs one local QueryJSON FFI call with the same retry contract used by writes so retryable SQLITE_SCHEMA / SQLITE_BUSY failures can self-heal.
// queryJSON 用于执行一次本地 QueryJSON FFI 调用，并复用与写请求一致的重试契约，让 SQLITE_SCHEMA / SQLITE_BUSY 这类可重试错误有机会自行恢复。
func (s *Store) queryJSON(ctx context.Context, sql string, params []sqliteffi.SQLValue) (sqliteffi.QueryJSONResult, error) {
	var response sqliteffi.QueryJSONResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.QueryJSON(sql, params, "")
		return err
	})
	return response, err
}

// withSQLiteRetry wraps one local SQLite FFI call so retryable busy/locked/schema errors are retried with a tiny exponential backoff.
// withSQLiteRetry 用于包裹单个本地 SQLite FFI 调用，并对可重试的 busy/locked/schema 错误做一个很小的指数退避重试。
func (s *Store) withSQLiteRetry(ctx context.Context, call func() error) error {
	var lastErr error
	for attempt := 0; attempt < sqliteRetryMaxAttempts; attempt++ {
		if err := checkSQLiteContext(ctx); err != nil {
			return err
		}
		err := call()
		if err == nil {
			return checkSQLiteContext(ctx)
		}
		lastErr = err
		if !isSQLiteRetryableError(err) || attempt == sqliteRetryMaxAttempts-1 {
			return lastErr
		}
		if err := waitSQLiteRetry(ctx, attempt); err != nil {
			return lastErr
		}
	}
	return lastErr
}

// isSQLiteRetryableError detects retryable SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA errors returned by the local FFI layer.
// isSQLiteRetryableError 用于识别本地 FFI 层返回的 SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA 可重试错误。
func isSQLiteRetryableError(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lowered, "sqlite_busy") ||
		strings.Contains(lowered, "sqlite_locked") ||
		strings.Contains(lowered, "sqlite_schema") ||
		strings.Contains(lowered, "database is locked")
}

// waitSQLiteRetry sleeps for a tiny exponential backoff while still honoring the parent context deadline.
// waitSQLiteRetry 用于执行一个很小的指数退避等待，同时继续遵守父 context 的截止时间。
func waitSQLiteRetry(ctx context.Context, attempt int) error {
	delay := sqliteRetryBaseDelay * time.Duration(1<<attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isSQLiteOutcomeUncertainError detects gateway failures whose text indicates the commit result may already have happened even though the RPC returned an error.
// isSQLiteOutcomeUncertainError 用于识别这类网关失败：虽然 RPC 返回报错，但错误文本表明提交结果本身可能已经发生。
func isSQLiteOutcomeUncertainError(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(strings.TrimSpace(err.Error()))
	fragments := []string{
		"resource deadlock would occur",
		"failed to commit",
		"transactioncontext error",
		"commit outcome unknown",
		"outcome uncertain",
	}
	for _, fragment := range fragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}
	return false
}

// idRow mirrors one MAX(id) style query used to allocate deterministic numeric ids.
// idRow 用于映射 MAX(id) 这类查询结果，以便分配确定性的数字 ID。
type idRow struct {
	NextID uint64 `json:"next_id"`
}

// nextNumericID allocates the next numeric identifier from one table under the adapter write lock.
// nextNumericID 用于在适配器写锁保护下，从某张表里分配下一个数字 ID。
func (s *Store) nextNumericID(ctx context.Context, table string) (uint64, error) {
	rows, err := queryRows[idRow](s, ctx, fmt.Sprintf(`SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM %s`, table))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || rows[0].NextID == 0 {
		return 1, nil
	}
	return rows[0].NextID, nil
}

// loadTurnRecordByID loads one durable turn row by primary key for write reconciliation paths that cannot rely on retry safety.
// loadTurnRecordByID 用于按主键读取一条长期 turn 行，服务于无法依赖安全重试的写入对账路径。
func (s *Store) loadTurnRecordByID(ctx context.Context, turnID uint64) (logicdomain.SessionTurnRecord, bool, error) {
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id = ?
LIMIT 1
`, turnID)
	if err != nil {
		return logicdomain.SessionTurnRecord{}, false, fmt.Errorf("query turn record: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionTurnRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// sameAppendedTurnRecord reports whether a read-back row exactly matches the turn insert currently being reconciled.
// sameAppendedTurnRecord 用于判断回读行是否精确匹配当前正在对账的 turn 插入。
func sameAppendedTurnRecord(row logicdomain.SessionTurnRecord, id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) bool {
	return row.ID == id &&
		row.SessionID == sessionID &&
		row.ProjectID == projectID &&
		row.DehydratedContent == strings.TrimSpace(dehydratedContent) &&
		row.DehydratedBudget == dehydratedBudget &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusPending &&
		row.Details == "" &&
		row.DetailsBudget == 0 &&
		row.CreatedAt.Equal(unixMilliToTime(createdMs)) &&
		row.UpdatedAt.Equal(unixMilliToTime(updatedMs))
}

// sameCorruptedTurnMark reports whether a read-back turn row proves one pending-to-done corrupted mark reached the durable table without changing payload fields.
// sameCorruptedTurnMark 用于判断回读 turn 行是否证明一次损坏 turn 的 pending 到 done 标记已经落入长期表，且没有改写载荷字段。
func sameCorruptedTurnMark(row logicdomain.SessionTurnRecord, before logicdomain.SessionTurnRecord, sessionID, turnID uint64, updatedMs int64) bool {
	return before.ID == turnID &&
		before.SessionID == sessionID &&
		before.ExtractedStatus == logicdomain.TurnExtractedStatusPending &&
		row.ID == before.ID &&
		row.SessionID == before.SessionID &&
		row.ProjectID == before.ProjectID &&
		row.DehydratedContent == before.DehydratedContent &&
		row.DehydratedBudget == before.DehydratedBudget &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusDone &&
		row.Details == before.Details &&
		row.DetailsBudget == before.DetailsBudget &&
		row.CreatedAt.Equal(before.CreatedAt) &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// sameAnalyzedTurnUpdate reports whether a read-back turn row has reached the exact analysis status and details written by the first post-action mutation.
// sameAnalyzedTurnUpdate 用于判断回读 turn 行是否已经达到 post-action 首个写入所要求的分析状态与 details 内容。
func sameAnalyzedTurnUpdate(row logicdomain.SessionTurnRecord, sessionID, projectID, turnID uint64, details string, detailsBudget int, updatedMs int64) bool {
	return row.ID == turnID &&
		row.SessionID == sessionID &&
		row.ProjectID == projectID &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusDone &&
		row.Details == strings.TrimSpace(details) &&
		row.DetailsBudget == detailsBudget &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// sameSQLiteRecordTime compares a read-back SQLite millisecond timestamp with the time value that was written through UnixMilli.
// sameSQLiteRecordTime 用于比较 SQLite 回读的毫秒时间戳与通过 UnixMilli 写入的原始 time 值。
func sameSQLiteRecordTime(value, expected time.Time) bool {
	if expected.IsZero() {
		return value.IsZero()
	}
	return value.Equal(unixMilliToTime(expected.UTC().UnixMilli()))
}

// sameFloat32Vector reports whether two vectors are bit-identical after SQLite JSON round-trip decoding.
// sameFloat32Vector 用于判断两个向量在 SQLite JSON 往返解码后是否保持 bit 级一致。
func sameFloat32Vector(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if math.Float32bits(left[idx]) != math.Float32bits(right[idx]) {
			return false
		}
	}
	return true
}

// sameMemoryNodeRecord reports whether a read-back memory row exactly matches one pending durable memory target.
// sameMemoryNodeRecord 用于判断回读 memory 行是否精确匹配一个待落表的长期记忆目标。
func sameMemoryNodeRecord(row logicdomain.MemoryNodeRecord, expected logicdomain.MemoryNodeRecord) bool {
	return row.ID == expected.ID &&
		row.TeamID == expected.TeamID &&
		row.SpaceID == expected.SpaceID &&
		row.ProjectID == expected.ProjectID &&
		row.UserID == expected.UserID &&
		row.OriginSessionID == expected.OriginSessionID &&
		row.SourceTurnID == expected.SourceTurnID &&
		row.VectorID == strings.TrimSpace(expected.VectorID) &&
		sameFloat32Vector(row.Vector, expected.Vector) &&
		row.SourceKind == expected.SourceKind &&
		row.ScopeLevel == expected.ScopeLevel &&
		row.Category == expected.Category &&
		row.Abstract == strings.TrimSpace(expected.Abstract) &&
		row.Details == strings.TrimSpace(expected.Details) &&
		row.Status == expected.Status &&
		row.Priority == expected.Priority &&
		row.MemoryLevel == expected.MemoryLevel &&
		row.RefreshWeight == expected.RefreshWeight &&
		row.SupportCount == expected.SupportCount &&
		row.RebuttalCount == expected.RebuttalCount &&
		row.StatusReason == strings.TrimSpace(expected.StatusReason) &&
		sameSQLiteRecordTime(row.ExpiresAt, expected.ExpiresAt) &&
		sameSQLiteRecordTime(row.LastRecalledAt, expected.LastRecalledAt) &&
		sameSQLiteRecordTime(row.LastAdoptedAt, expected.LastAdoptedAt) &&
		sameSQLiteRecordTime(row.LastReinforcedAt, expected.LastReinforcedAt) &&
		row.RecalledCount == expected.RecalledCount &&
		row.AdoptedCount == expected.AdoptedCount &&
		row.ReinforcementCount == expected.ReinforcementCount &&
		row.CrossSessionAdoptedCount == expected.CrossSessionAdoptedCount &&
		row.DecayDisabled == expected.DecayDisabled &&
		row.DedupeHash == strings.TrimSpace(expected.DedupeHash) &&
		sameSQLiteRecordTime(row.CreatedAt, expected.CreatedAt) &&
		sameSQLiteRecordTime(row.UpdatedAt, expected.UpdatedAt)
}

// sameMemoryContextEdgeSet reports whether a read-back edge set exactly matches the durable edge state expected after one context-edge rewrite statement.
// sameMemoryContextEdgeSet 用于判断回读情境边集合是否精确匹配某条情境边重写语句执行后应达到的长期状态。
func sameMemoryContextEdgeSet(rows []logicdomain.MemoryContextEdge, expected []logicdomain.MemoryContextEdge) bool {
	if len(rows) != len(expected) {
		return false
	}
	orderedRows := append([]logicdomain.MemoryContextEdge(nil), rows...)
	orderedExpected := append([]logicdomain.MemoryContextEdge(nil), expected...)
	sortMemoryContextEdgesByPrimaryKey(orderedRows)
	sortMemoryContextEdgesByPrimaryKey(orderedExpected)
	for index := range orderedExpected {
		if !sameMemoryContextEdge(orderedRows[index], orderedExpected[index]) {
			return false
		}
	}
	return true
}

// sortMemoryContextEdgesByPrimaryKey aligns in-memory comparisons with the SQLite table primary key instead of relying on extraction-order side effects.
// sortMemoryContextEdgesByPrimaryKey 用于让内存比较对齐 SQLite 表主键顺序，而不是依赖提炼顺序的副作用。
func sortMemoryContextEdgesByPrimaryKey(edges []logicdomain.MemoryContextEdge) {
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].MemoryID != edges[right].MemoryID {
			return edges[left].MemoryID < edges[right].MemoryID
		}
		if edges[left].ContextKey != edges[right].ContextKey {
			return edges[left].ContextKey < edges[right].ContextKey
		}
		return edges[left].ContextValue < edges[right].ContextValue
	})
}

// sameMemoryContextEdge reports whether one read-back edge matches the exact primary-key row and counters written for a turn-extracted memory.
// sameMemoryContextEdge 用于判断单条回读情境边是否匹配 turn 提炼记忆写入的精确主键行与计数器。
func sameMemoryContextEdge(row logicdomain.MemoryContextEdge, expected logicdomain.MemoryContextEdge) bool {
	return row.MemoryID == expected.MemoryID &&
		row.ContextKey == strings.TrimSpace(expected.ContextKey) &&
		row.ContextValue == strings.TrimSpace(expected.ContextValue) &&
		row.SupportCount == expected.SupportCount &&
		row.RebuttalCount == expected.RebuttalCount &&
		sameSQLiteRecordTime(row.LastSupportedAt, expected.LastSupportedAt) &&
		sameSQLiteRecordTime(row.LastRebuttedAt, expected.LastRebuttedAt) &&
		sameSQLiteRecordTime(row.CreatedAt, expected.CreatedAt) &&
		sameSQLiteRecordTime(row.UpdatedAt, expected.UpdatedAt)
}

// sameProfileNodeInsert reports whether a read-back profile node matches the post-action insert target that may have committed already.
// sameProfileNodeInsert 用于判断回读画像节点是否匹配一次可能已经提交的 post-action 插入目标。
func sameProfileNodeInsert(row logicdomain.ProfileNodeRecord, expected turnAnalysisProfileInsertExpectation) bool {
	expiresAt := time.Time{}
	if !expected.Node.ExpiresAt.IsZero() {
		expiresAt = expected.Node.ExpiresAt.UTC()
	}
	return row.ID == expected.ID &&
		row.TurnID == expected.TurnID &&
		row.ProfileType == expected.ProfileType &&
		row.BindID == expected.BindID &&
		row.Content == strings.TrimSpace(expected.Node.Content) &&
		row.Status == expected.Node.Status &&
		row.Priority == expected.Node.Priority &&
		row.ProfileLevel == expected.Node.ProfileLevel &&
		row.LevelReason == strings.TrimSpace(expected.Node.LevelReason) &&
		row.RefreshWeight == expected.Node.RefreshWeight &&
		row.ProfileDate == strings.TrimSpace(expected.Node.ProfileDate) &&
		row.SourceKind == expected.Node.SourceKind &&
		row.SourceID == expected.Node.SourceID &&
		row.StatusReason == strings.TrimSpace(expected.Node.StatusReason) &&
		sameSQLiteRecordTime(row.ExpiresAt, expiresAt) &&
		row.SupersededByID == 0 &&
		row.CreatedAt.Equal(unixMilliToTime(expected.CreatedMs)) &&
		row.UpdatedAt.Equal(unixMilliToTime(expected.CreatedMs))
}

// sameAppendedSessionCounters reports whether a read-back session row has reached the absolute counter target for one appended turn.
// sameAppendedSessionCounters 用于判断回读 session 行是否已经达到一次 turn 追加对应的绝对计数目标。
func sameAppendedSessionCounters(row logicdomain.SessionRecord, sessionID, projectID uint64, turnCount, summarizeBudget int, updatedMs int64) bool {
	return row.ID == sessionID &&
		row.ProjectID == projectID &&
		row.TurnCount == turnCount &&
		row.SummarizeBudget == summarizeBudget &&
		!row.UpdatedAt.Before(unixMilliToTime(updatedMs))
}

// sessionTimestampReached reports whether one read-back session timestamp has reached a requested checkpoint while treating zero as "no checkpoint requested".
// sessionTimestampReached 用于判断回读 session 时间戳是否达到请求的检查点，同时把零值视为“未请求该检查点”。
func sessionTimestampReached(value time.Time, targetMs int64) bool {
	if targetMs <= 0 {
		return true
	}
	return !value.Before(unixMilliToTime(targetMs))
}

// sameAdvancedSessionExtractWindow reports whether a read-back session row proves one extract-window checkpoint update has reached every requested timestamp.
// sameAdvancedSessionExtractWindow 用于判断回读 session 行是否能够证明一次提炼窗口检查点更新已经达到所有请求的时间戳。
func sameAdvancedSessionExtractWindow(row logicdomain.SessionRecord, sessionID uint64, observedMs, completedMs, updatedMs int64) bool {
	return row.ID == sessionID &&
		sessionTimestampReached(row.LastExtractObservedAt, observedMs) &&
		sessionTimestampReached(row.LastExtractCompletedAt, completedMs) &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// generateConfirmationCode returns one 32-character random hex token used by protected destructive flows.
// generateConfirmationCode 用于生成 32 位随机十六进制确认码，服务需要保护的破坏性操作。
func generateConfirmationCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate confirmation code: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// LoadNoiseEmbeddingCache returns one persisted semantic prototype bundle keyed by scope, language, model, dimension, and rules hash.
// LoadNoiseEmbeddingCache 用于返回按作用域、语言、模型、维度和规则哈希定位的一组语义原型缓存。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	rows, err := queryRows[noiseEmbeddingRow](s, ctx, `
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
ORDER BY category_name ASC, phrase ASC
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash))
	if err != nil {
		return nil, fmt.Errorf("load noise embedding cache: %w", err)
	}
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(rows))
	for _, row := range rows {
		updatedAt, _ := time.Parse(time.RFC3339Nano, row.UpdatedAt)
		entries = append(entries, logicdomain.NoiseEmbeddingCacheEntry{
			Scope:        row.Scope,
			Language:     row.Language,
			CategoryName: row.CategoryName,
			Phrase:       row.Phrase,
			Model:        row.Model,
			Dimension:    row.Dimension,
			RulesHash:    row.RulesHash,
			Vector:       decodeFloat32Slice(row.VectorJSON),
			UpdatedAt:    updatedAt,
		})
	}
	return entries, nil
}

// ReplaceNoiseEmbeddingCache rewrites one semantic prototype bundle by deleting old rows and batch-inserting the refreshed set; callers treat failures as cache-refresh degradation.
// ReplaceNoiseEmbeddingCache 用于通过先删旧行再批量插入新行来重写一组语义原型缓存；调用方会把失败视为缓存刷新降级。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, `
DELETE FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash)); err != nil {
		return sqliteNoiseEmbeddingCacheReplaceError("clear noise embedding cache", err)
	}
	if len(entries) == 0 {
		return nil
	}
	batchItems := make([][]any, 0, len(entries))
	for _, entry := range entries {
		vectorJSON, err := json.Marshal(entry.Vector)
		if err != nil {
			return fmt.Errorf("encode noise embedding cache vector: %w", err)
		}
		batchItems = append(batchItems, []any{
			strings.TrimSpace(entry.Scope),
			strings.TrimSpace(entry.Language),
			strings.TrimSpace(entry.CategoryName),
			strings.TrimSpace(entry.Phrase),
			strings.TrimSpace(entry.Model),
			entry.Dimension,
			strings.TrimSpace(entry.RulesHash),
			string(vectorJSON),
			entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	if err := s.execBatch(ctx, `
INSERT INTO vmm_noise_embeddings (
  scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, batchItems); err != nil {
		return sqliteNoiseEmbeddingCacheReplaceError("insert noise embedding cache rows", err)
	}
	return nil
}

// sqliteNoiseEmbeddingCacheReplaceError keeps cache refresh failures non-fatal to callers while preserving commit-boundary uncertainty in the storage error type.
// sqliteNoiseEmbeddingCacheReplaceError 用于让调用方继续把缓存刷新失败视为非致命降级，同时在存储错误类型中保留提交边界不确定语义。
func sqliteNoiseEmbeddingCacheReplaceError(stage string, err error) error {
	if err == nil {
		return nil
	}
	stage = strings.TrimSpace(stage)
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: "replace sqlite noise embedding cache",
			Message:   fmt.Sprintf("%s: %v", stage, err),
		}
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// ResolveRequestScope validates numeric user/project identifiers, resolves hierarchy names, and auto-creates the session row when needed.
// ResolveRequestScope 用于校验数字 user/project 标识、解析层级名称，并在需要时自动创建 session 记录。
func (s *Store) ResolveRequestScope(ctx context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	// Reject incomplete numeric identifiers here so PreCheck/PostAction never enter business code with unresolved scopes.
	// 在这里拒绝不完整的数字标识，确保 PreCheck/PostAction 不会带着未解析的范围进入业务代码。
	if strings.TrimSpace(sessionKey) == "" {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if userID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if projectID == 0 {
		return logicdomain.SessionRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}

	user, err := s.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	project, err := s.loadProjectByID(ctx, projectID)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	session, err := s.ensureSession(ctx, strings.TrimSpace(sessionKey), user, project)
	if err != nil {
		return logicdomain.SessionRef{}, err
	}
	return logicdomain.SessionRef{
		SessionID:              session.ID,
		SessionKey:             session.SessionKey,
		UserID:                 user.ID,
		TeamID:                 project.TeamID,
		SpaceID:                project.SpaceID,
		ProjectID:              project.ID,
		TurnCount:              session.TurnCount,
		LastSummarizedID:       session.LastSummarizedID,
		LastCompactedTurnID:    session.LastCompactedTurnID,
		SummarizeContent:       session.SummarizeContent,
		SummarizeBudget:        session.SummarizeBudget,
		LastExtractObservedAt:  session.LastExtractObservedAt,
		LastExtractCompletedAt: session.LastExtractCompletedAt,
		LastCompactedAt:        session.LastCompactedAt,
		CreatedAt:              session.CreatedAt,
		UpdatedAt:              session.UpdatedAt,
		UserName:               user.Name,
		TeamName:               project.TeamName,
		SpaceName:              project.SpaceName,
		ProjectName:            project.Name,
	}, nil
}

// AppendTurnRecord persists one cleaned turn into the resolved session, increments the session turn counter, and returns the new turn identifier.
// AppendTurnRecord 用于把一条清洗后的 turn 写入已解析的 session、递增会话 turn 计数，并返回新的 turn 标识。
func (s *Store) AppendTurnRecord(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.TurnRecord) (logicdomain.PersistedTurnRecord, error) {
	if strings.TrimSpace(turn.UserContent) == "" && strings.TrimSpace(turn.AssistantContent) == "" && len(turn.Timeline) == 0 {
		return logicdomain.PersistedTurnRecord{}, nil
	}
	if session.SessionID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}

	// Serialize turn writes under the process write lock because the real SQLite FFI cannot bind typed params across a multi-statement transaction script.
	// 在进程写锁下串行化 turn 写入，因为真实 SQLite FFI 无法在多语句事务脚本中绑定强类型参数。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_turn_records")
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("allocate turn record id: %w", err)
	}
	dehydratedContent, dehydratedBudget, err := storageutil.DehydrateTurn(turn)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()
	createdMs := nowMs
	if !turn.CreatedAt.IsZero() {
		createdMs = turn.CreatedAt.UTC().UnixMilli()
	}
	currentSession, err := s.loadSessionByID(ctx, session.SessionID)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	if currentSession.ProjectID != session.ProjectID {
		return logicdomain.PersistedTurnRecord{}, logicdomain.ConflictError{
			Resource: "session",
			Message:  fmt.Sprintf("session %d belongs to project %d, not project %d", session.SessionID, currentSession.ProjectID, session.ProjectID),
		}
	}
	nextTurnCount := currentSession.TurnCount + 1
	nextSummarizeBudget := currentSession.SummarizeBudget + dehydratedBudget

	// Write and verify the durable turn row first because any later failure must be reported as an uncertain append outcome.
	// 先写入并验收持久化 turn 行，因为后续任何失败都必须以追加结果不确定的方式上报。
	turnInsertStatement := parameterizedTurnInsertStatement(nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs)
	if err := s.execInsertOneRow(ctx, "insert appended turn record", turnInsertStatement.SQL, turnInsertStatement.Params...); err != nil {
		insertRecovered := false
		// Re-read only the allocated primary key after a commit-unknown insert; a mismatched row cannot prove this append was persisted.
		// 仅在插入提交结果不明后回读本次分配的主键；不匹配的行无法证明本次追加已经落库。
		if sqliteInsertCommitUnknownError(err) {
			recovered, found, reconcileErr := s.loadTurnRecordByID(ctx, nextID)
			if reconcileErr != nil {
				return logicdomain.PersistedTurnRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "append turn record insert",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if found && sameAppendedTurnRecord(recovered, nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs) {
				insertRecovered = true
			}
		}
		if !insertRecovered {
			return logicdomain.PersistedTurnRecord{}, fmt.Errorf("append turn record insert: %w", err)
		}
	}

	// Update the owning session counter only after the insert is confirmed, then convert counter drift into outcome-uncertain instead of pretending the append failed cleanly.
	// 仅在确认插入成功后更新所属 session 计数，并把计数漂移转换为结果不确定，避免伪装成可安全重试的干净失败。
	sessionUpdateStatement := parameterizedSessionTurnUpdateStatement(session.SessionID, nextTurnCount, nextSummarizeBudget, nowMs)
	if err := s.execExpectRowsChanged(ctx, "update appended turn session counters", 1, sessionUpdateStatement.SQL, sessionUpdateStatement.Params...); err != nil {
		sessionUpdateRecovered := false
		// Re-read the absolute counter target only after a commit-unknown session update; row-count drift still means the append is uncertain.
		// 仅在 session 更新提交结果不明后回读绝对计数目标；行数漂移仍表示追加结果不确定。
		if isSQLiteOutcomeUncertainError(err) {
			recoveredSession, reconcileErr := s.loadSessionByID(ctx, session.SessionID)
			if reconcileErr != nil {
				return logicdomain.PersistedTurnRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "append turn record",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			sessionUpdateRecovered = sameAppendedSessionCounters(recoveredSession, session.SessionID, session.ProjectID, nextTurnCount, nextSummarizeBudget, nowMs)
		}
		if !sessionUpdateRecovered {
			return logicdomain.PersistedTurnRecord{}, sqlitePartialMutationError(true, "append turn record", err)
		}
	}
	return logicdomain.PersistedTurnRecord{
		ID:               nextID,
		SessionID:        session.SessionID,
		ProjectID:        session.ProjectID,
		DehydratedBudget: dehydratedBudget,
		CreatedAt:        unixMilliToTime(createdMs),
		UpdatedAt:        unixMilliToTime(nowMs),
	}, nil
}

// ApplyTurnAnalysis writes the extracted turn summary back to the turn row, inserts unified memory/profile nodes, and returns follow-up vector cleanup coordinates.
// ApplyTurnAnalysis 用于把提炼出的 turn 总结回写到 turn 行、插入统一记忆和画像节点，并返回后续向量清理坐标。
func (s *Store) ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	if turn.ID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	if session.SessionID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if session.UserID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}

	// Serialize analysis writes so the turn status flip, unified memory inserts, and profile writes stay aligned under one deterministic id allocation window.
	// 串行化分析结果写入，确保 turn 状态切换、统一记忆插入和画像写入在同一个确定性 ID 分配窗口内保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if analysis.DetailsBudget <= 0 {
		analysis.DetailsBudget = storageutil.EstimateTokenBudget(analysis.Details)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	memoryStartID := uint64(0)
	profileStartID := uint64(0)
	var err error
	// Resolve merged profile targets before the first turn-status write so missing scope rows remain clean rollback-safe failures.
	// 在首个 turn 状态写入之前解析合并画像目标，让缺失范围行保持为可安全回滚的干净失败。
	if analysis.UserProfileMerged {
		if _, err := s.loadUserByID(ctx, session.UserID); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load merged user profile target: %w", err)
		}
	}
	if analysis.ProjectProfileMerged {
		if _, err := s.loadProjectByID(ctx, session.ProjectID); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load merged project profile target: %w", err)
		}
	}
	if len(analysis.MemoryNodes) > 0 {
		memoryStartID, err = s.nextNumericID(ctx, "vmm_memory_nodes")
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("allocate memory node id: %w", err)
		}
	}
	if len(analysis.ProfileNodes) > 0 {
		profileStartID, err = s.nextNumericID(ctx, "vmm_profile_nodes")
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("allocate profile node id: %w", err)
		}
	}

	// Resolve the vector ids for reviewer-approved superseded memory rows before the status flip so the caller can delete those vector rows after SQL commits.
	// 在状态切换前先解析 reviewer 批准的被覆盖记忆 vector_id，供调用方在 SQL 提交后删除对应向量行。
	supersededMemoryIDs := collectTurnAnalysisSupersedeMemoryIDs(analysis.MemoryNodes)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load superseded vector ids: %w", err)
	}

	turnUpdateStatement := parameterizedTurnAnalysisUpdateStatement(session.SessionID, turn.ID, strings.TrimSpace(analysis.Details), analysis.DetailsBudget, nowMs)
	statements := []sqliteWriteStatement{}
	profileTargetStatementIndexes := map[int]struct{}{}
	memoryInsertStatementRecords := map[int]logicdomain.MemoryNodeRecord{}
	memoryContextEdgeStatementExpectations := map[int]turnAnalysisMemoryContextEdgeExpectation{}
	profileInsertStatementExpectations := map[int]turnAnalysisProfileInsertExpectation{}
	profileSupersedeStatementExpectations := map[int]turnAnalysisProfileSupersedeExpectation{}
	if analysis.UserProfileMerged {
		profileTargetStatementIndexes[len(statements)] = struct{}{}
		statements = append(statements, sqliteWriteStatement{
			SQL:    parameterizedProfileTargetUpdateSQL(logicdomain.ProfileTypeUser),
			Params: []any{strings.TrimSpace(analysis.MergedUserProfile), nowRFC3339, session.UserID},
		})
	}
	if analysis.ProjectProfileMerged {
		profileTargetStatementIndexes[len(statements)] = struct{}{}
		statements = append(statements, sqliteWriteStatement{
			SQL:    parameterizedProfileTargetUpdateSQL(logicdomain.ProfileTypeProject),
			Params: []any{strings.TrimSpace(analysis.MergedProjectProfile), nowRFC3339, session.ProjectID},
		})
	}
	insertedMemoryNodes := make([]logicdomain.MemoryNodeRecord, 0, len(analysis.MemoryNodes))
	for idx, node := range analysis.MemoryNodes {
		if strings.TrimSpace(node.VectorID) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].vector_id", Message: "is required after vector persistence"}
		}
		if !logicdomain.ValidMemoryNodeCategory(node.Category) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].category", Message: "must be one supported memory category"}
		}
		record := normalizeTurnMemoryNodeRecord(session, turn, node, memoryStartID+uint64(idx), now)
		contextEdges := logicdomain.NormalizeMemoryContextEdges(record.ID, node.ContextEdges, now)
		record.SupportCount, record.RebuttalCount = logicdomain.SummarizeMemoryContextEdges(contextEdges)
		memoryInsertStatementRecords[len(statements)] = record
		statements = append(statements, parameterizedMemoryNodeInsertStatement(record))
		// Track every delete-then-insert boundary because commit-unknown recovery must know the exact edge-table prefix that should be visible before later inserts run.
		// 跟踪每个先删后插边界，因为提交未知恢复必须知道后续插入执行前边表应可见的精确前缀集合。
		contextEdgeStatements := parameterizedMemoryContextEdgesReplaceStatements(record.ID, contextEdges)
		for contextStatementIndex := range contextEdgeStatements {
			expectedEdges := []logicdomain.MemoryContextEdge(nil)
			requireRowsChanged := false
			expectedRows := int64(0)
			if contextStatementIndex > 0 {
				expectedEdges = append(expectedEdges, contextEdges[:contextStatementIndex]...)
				requireRowsChanged = true
				expectedRows = 1
			}
			memoryContextEdgeStatementExpectations[len(statements)+contextStatementIndex] = turnAnalysisMemoryContextEdgeExpectation{
				MemoryID:           record.ID,
				ExpectedEdges:      expectedEdges,
				RequireRowsChanged: requireRowsChanged,
				ExpectedRows:       expectedRows,
			}
		}
		statements = append(statements, contextEdgeStatements...)
		insertedMemoryNodes = append(insertedMemoryNodes, record)
	}
	for idx, node := range analysis.ProfileNodes {
		if !logicdomain.ValidProfileType(node.ProfileType) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].profile_type", Message: "must be one supported profile type"}
		}
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].status", Message: "must be one supported profile status"}
		}
		bindID := session.ProjectID
		if node.ProfileType == logicdomain.ProfileTypeUser {
			bindID = session.UserID
		}
		if strings.TrimSpace(node.ProfileDate) == "" {
			node.ProfileDate = logicdomain.FormatDisplayDate(turn.CreatedAt)
		}
		insertedProfileID := profileStartID + uint64(idx)
		profileInsertStatementExpectations[len(statements)] = turnAnalysisProfileInsertExpectation{
			ID:          insertedProfileID,
			TurnID:      turn.ID,
			ProfileType: node.ProfileType,
			BindID:      bindID,
			Node:        node,
			CreatedMs:   nowMs,
		}
		statements = append(statements, parameterizedProfileNodeInsertStatement(insertedProfileID, &turn.ID, node.ProfileType, bindID, node, nowMs))
		supersedeNodeIDs := storageutil.NormalizeUint64List(node.SupersedeNodeIDs)
		// Retire reviewer-approved predecessor nodes right after the replacement insert so rendered profile readers do not see both facts as active.
		// 在替代节点插入后立即退役评审器批准的旧节点，避免画像读取链路同时看到新旧事实均为 active。
		if supersedeStatement, ok := parameterizedProfileNodesSupersedeStatement(node.ProfileType, bindID, supersedeNodeIDs, insertedProfileID, strings.TrimSpace(node.StatusReason), nowMs); ok {
			profileSupersedeStatementExpectations[len(statements)] = turnAnalysisProfileSupersedeExpectation{
				NodeIDs:        supersedeNodeIDs,
				SupersededByID: insertedProfileID,
				ExpectedRows:   int64(len(supersedeNodeIDs)),
			}
			statements = append(statements, supersedeStatement)
		}
	}
	// Track the first durable mutation because later failures must not be treated as clean relational failures by vector rollback paths.
	// 记录首次持久化突变，因为后续失败不能再被向量回滚路径当成干净的关系写入失败处理。
	mutated := false
	// Require the current session's pending turn before any memory row can reference the extracted result, so stale queue items remain clean rollback-safe failures.
	// 在任何 memory 行引用提炼结果之前先要求当前 session 的 pending turn 存在，让过期队列项保持为可安全回滚的干净失败。
	turnUpdateResult, err := s.execResult(ctx, turnUpdateStatement.SQL, turnUpdateStatement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredTurn, found, reconcileErr := s.loadTurnRecordByID(ctx, turn.ID)
			if reconcileErr != nil {
				return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "apply turn analysis",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !found || !sameAnalyzedTurnUpdate(recoveredTurn, session.SessionID, turn.ProjectID, turn.ID, analysis.Details, analysis.DetailsBudget, nowMs) {
				return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "apply turn analysis",
					Message:   err.Error(),
				}
			}
		} else {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update turn analysis: %w", err)
		}
	} else if err := sqliteAutocommitRowsChangedDriftError("apply turn analysis", "update analyzed turn", turnUpdateResult.RowsChanged, 1); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update turn analysis: %w", err)
	}
	mutated = true
	freshVectorReference := false
	for index, statement := range statements {
		var err error
		if _, ok := profileTargetStatementIndexes[index]; ok {
			err = s.execExpectRowsChanged(ctx, "update turn analysis profile target", 1, statement.SQL, statement.Params...)
		} else if record, ok := memoryInsertStatementRecords[index]; ok {
			insertResult, insertErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if insertErr != nil {
				if isSQLiteOutcomeUncertainError(insertErr) {
					recoveredRows, reconcileErr := s.LoadMemoryNodesByIDs(ctx, []uint64{record.ID})
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, insertErr, reconcileErr),
							FreshVectorReference: true,
						}
					}
					if len(recoveredRows) == 1 && sameMemoryNodeRecord(recoveredRows[0], record) {
						freshVectorReference = true
					} else {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, insertErr),
							FreshVectorReference: true,
						}
					}
				} else {
					err = insertErr
				}
			} else if insertResult.RowsChanged != 1 {
				rowDriftErr := fmt.Errorf("insert turn analysis memory node affected %d rows, want 1", insertResult.RowsChanged)
				if insertResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: true,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := memoryContextEdgeStatementExpectations[index]; ok {
			edgeResult, edgeErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if edgeErr != nil {
				if isSQLiteOutcomeUncertainError(edgeErr) {
					recoveredEdges, reconcileErr := s.LoadMemoryContextEdgesByMemoryIDs(ctx, []uint64{expected.MemoryID})
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, edgeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !sameMemoryContextEdgeSet(recoveredEdges, expected.ExpectedEdges) {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, edgeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue only after the edge table exactly matches the statement boundary, so later inserts do not duplicate or skip contextual evidence.
					// 仅在边表精确匹配当前语句边界后继续，避免后续插入重复或跳过情境证据。
				} else {
					err = edgeErr
				}
			} else if expected.RequireRowsChanged && edgeResult.RowsChanged != expected.ExpectedRows {
				rowDriftErr := fmt.Errorf("write turn analysis memory context edge affected %d rows, want %d", edgeResult.RowsChanged, expected.ExpectedRows)
				if edgeResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := profileInsertStatementExpectations[index]; ok {
			insertResult, insertErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if insertErr != nil {
				if isSQLiteOutcomeUncertainError(insertErr) {
					recovered, found, reconcileErr := s.loadProfileNodeByID(ctx, expected.ID)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, insertErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !found || !sameProfileNodeInsert(recovered, expected) {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, insertErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Keep processing later supersede statements because the profile node insert has reached the durable target.
					// 继续处理后续 supersede 语句，因为画像节点插入已经达到长期目标。
				} else {
					err = insertErr
				}
			} else if insertResult.RowsChanged != 1 {
				rowDriftErr := fmt.Errorf("insert turn analysis profile node affected %d rows, want 1", insertResult.RowsChanged)
				if insertResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := profileSupersedeStatementExpectations[index]; ok {
			supersedeResult, supersedeErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileProfileNodesSuperseded(ctx, expected.NodeIDs, expected.SupersededByID)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, supersedeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !recovered {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, supersedeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue after confirmed retirement so rendered profile readers do not see both old and replacement nodes as active.
					// 确认退役成功后继续执行，避免画像读取链路同时看到旧节点与替代节点均为 active。
				} else {
					err = supersedeErr
				}
			} else if supersedeResult.RowsChanged != expected.ExpectedRows {
				rowDriftErr := fmt.Errorf("supersede turn analysis profile nodes affected %d rows, want %d", supersedeResult.RowsChanged, expected.ExpectedRows)
				if supersedeResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else {
			err = s.exec(ctx, statement.SQL, statement.Params...)
		}
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("execute turn analysis write statement %d: %w", index+1, err))
		}
		if _, ok := memoryInsertStatementRecords[index]; ok {
			// Mark the boundary only after the durable memory row insert succeeds because that is when fresh vectors may become referenced.
			// 仅在长期 memory 行插入成功后标记边界，因为此时新向量才可能被关系行引用。
			freshVectorReference = true
		}
		mutated = true
	}
	if len(supersededMemoryIDs) > 0 {
		if supersedeStatement, ok := parameterizedMemoryNodesSupersedeStatement(supersededMemoryIDs, nowMs); ok {
			supersedeResult, supersedeErr := s.execResult(ctx, supersedeStatement.SQL, supersedeStatement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileMemoryNodesSuperseded(ctx, supersededMemoryIDs, nowMs)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("supersede turn analysis memory nodes: %v; reconcile failed: %v", supersedeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !recovered {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("supersede turn analysis memory nodes: %v", supersedeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue to FTS deletion only after the durable memory rows prove this supersede write reached the table.
					// 只有在长期 memory 行证明本次 supersede 已落表后，才继续执行 FTS 删除。
				} else {
					return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("supersede turn analysis memory nodes: %w", supersedeErr))
				}
			} else if supersedeResult.RowsChanged != int64(len(supersededMemoryIDs)) {
				rowDriftErr := fmt.Errorf("affected %d rows, want %d", supersedeResult.RowsChanged, len(supersededMemoryIDs))
				return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("supersede turn analysis memory nodes: %w", rowDriftErr))
			}
			mutated = true
		}
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, insertedMemoryNodes, supersededMemoryIDs); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("sync memory fts after turn analysis: %w", err))
	}
	return logicdomain.TurnAnalysisApplyResult{
		InsertedMemoryNodes: insertedMemoryNodes,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// LoadProfileTargets loads the current durable user/project profile blobs so post-action can merge fresh profile evidence before persistence.
// LoadProfileTargets 用于加载当前长期 user/project 画像 Blob，让 post-action 在持久化前先合并新的画像证据。
func (s *Store) LoadProfileTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileTargetsSnapshot, error) {
	if session.UserID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	user, err := s.loadUserByID(ctx, session.UserID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	project, err := s.loadProjectByID(ctx, session.ProjectID)
	if err != nil {
		return logicdomain.ProfileTargetsSnapshot{}, err
	}
	return logicdomain.ProfileTargetsSnapshot{
		UserProfile:    strings.TrimSpace(user.Profile),
		ProjectProfile: strings.TrimSpace(project.Profile),
	}, nil
}

// LoadProfileReviewTargets loads the currently active and non-expired user/project profile nodes so post-action can review new candidates against factual atomic records.
// LoadProfileReviewTargets 用于加载当前活跃且未过期的 user/project 画像节点，让 post-action 可以基于原子事实记录评审新候选。
func (s *Store) LoadProfileReviewTargets(ctx context.Context, session logicdomain.SessionRef) (logicdomain.ProfileReviewTargetsSnapshot, error) {
	if session.UserID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if session.ProjectID == 0 {
		return logicdomain.ProfileReviewTargetsSnapshot{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	userNodes, err := s.loadActiveProfileNodes(ctx, logicdomain.ProfileTypeUser, session.UserID, nowMs)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query user profile review targets: %w", err)
	}
	projectNodes, err := s.loadActiveProfileNodes(ctx, logicdomain.ProfileTypeProject, session.ProjectID, nowMs)
	if err != nil {
		return logicdomain.ProfileReviewTargetsSnapshot{}, fmt.Errorf("query project profile review targets: %w", err)
	}
	return logicdomain.ProfileReviewTargetsSnapshot{
		UserNodes:    userNodes,
		ProjectNodes: projectNodes,
	}, nil
}

// ResolveProfileTarget resolves one requested profile target into a stable bind id and hierarchy context for query or manual instruction flows.
// ResolveProfileTarget 用于把请求的画像目标解析成稳定的 bind id 和层级上下文，供查询或手工画像指令流程使用。
func (s *Store) ResolveProfileTarget(ctx context.Context, profileType int, userID, projectID uint64) (logicdomain.ProfileTargetRef, error) {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		if userID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
		}
		user, err := s.loadUserByID(ctx, userID)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		return logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			BindID:      user.ID,
			UserID:      user.ID,
			UserName:    user.Name,
		}, nil
	case logicdomain.ProfileTypeProject, logicdomain.ProfileTypeTeam, logicdomain.ProfileTypeSpace:
		if projectID == 0 {
			return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
		}
		project, err := s.loadProjectByID(ctx, projectID)
		if err != nil {
			return logicdomain.ProfileTargetRef{}, err
		}
		target := logicdomain.ProfileTargetRef{
			ProfileType: profileType,
			UserID:      userID,
			TeamID:      project.TeamID,
			SpaceID:     project.SpaceID,
			ProjectID:   project.ID,
			TeamName:    project.TeamName,
			SpaceName:   project.SpaceName,
			ProjectName: project.Name,
		}
		switch profileType {
		case logicdomain.ProfileTypeProject:
			target.BindID = project.ID
		case logicdomain.ProfileTypeTeam:
			target.BindID = project.TeamID
		case logicdomain.ProfileTypeSpace:
			target.BindID = project.SpaceID
		}
		return target, nil
	default:
		return logicdomain.ProfileTargetRef{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
}

// ListActiveProfileNodes returns only the current active nodes for one resolved target in a deterministic order, while respecting the caller's explicit limit when provided.
// ListActiveProfileNodes 用于按确定性顺序返回某个已解析目标当前 active 的画像节点，并在调用方显式提供 limit 时尊重该限制。
func (s *Store) ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return nil, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return nil, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	sqlText := `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.profile_type = ? AND n.bind_id = ? AND n.profile_status = ? AND (n.expires_timestamp <= 0 OR n.expires_timestamp > ?)
ORDER BY profile_date_anchor_timestamp DESC, n.id ASC
`
	args := []any{target.ProfileType, target.BindID, logicdomain.ProfileStatusActive, nowMs}
	if limit > 0 {
		sqlText += "LIMIT ?\n"
		args = append(args, limit)
	}
	rows, err := queryRows[profileNodeRow](s, ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query active profile nodes: %w", err)
	}
	out := make([]logicdomain.ProfileNodeRecord, 0, len(rows))
	for _, row := range rows {
		node := row.toDomain()
		out = append(out, logicdomain.ProfileNodeRecord{
			ID:                  node.ID,
			TurnID:              node.TurnID,
			ProfileType:         node.ProfileType,
			BindID:              node.BindID,
			Content:             node.Content,
			Status:              node.Status,
			Priority:            node.Priority,
			ProfileLevel:        node.ProfileLevel,
			LevelReason:         node.LevelReason,
			RefreshWeight:       node.RefreshWeight,
			ProfileDate:         node.ProfileDate,
			SourceKind:          node.SourceKind,
			SourceID:            node.SourceID,
			StatusReason:        node.StatusReason,
			ExpiresAt:           node.ExpiresAt,
			SupersededByID:      node.SupersededByID,
			ProfileDateAnchorAt: node.ProfileDateAnchorAt,
			CreatedAt:           node.CreatedAt,
			UpdatedAt:           node.UpdatedAt,
		})
	}
	return out, nil
}

// LoadRenderedProfile returns the durable scope-level rendered profile body stored on the resolved target row.
// LoadRenderedProfile 用于返回已解析目标行上持久化的 scope 级渲染画像正文。
func (s *Store) LoadRenderedProfile(ctx context.Context, target logicdomain.ProfileTargetRef) (string, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return "", logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return "", logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	return s.loadRenderedProfileByTarget(ctx, target.ProfileType, target.BindID)
}

// loadProfileInstructionByID fetches one manual profile-instruction row by its durable numeric id so uncertain commits can be reconciled before retrying.
// loadProfileInstructionByID 用于按长期数字 id 读取一条手工画像指令，让“不确定提交”在重试前先做状态对账。
func (s *Store) loadProfileInstructionByID(ctx context.Context, instructionID uint64) (logicdomain.ProfileInstructionRecord, bool, error) {
	rows, err := queryRows[profileInstructionRow](s, ctx, `
SELECT id, profile_type, bind_id, instruction, instruction_status,
       review_result_json, failure_reason, created_timestamp, updated_timestamp
FROM vmm_profile_instructions
WHERE id = ?
LIMIT 1
`, instructionID)
	if err != nil {
		return logicdomain.ProfileInstructionRecord{}, false, err
	}
	if len(rows) == 0 {
		return logicdomain.ProfileInstructionRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// loadProfileNodeByID fetches one durable profile node by id so manual-instruction persistence can verify whether a supposedly failed insert already landed.
// loadProfileNodeByID 用于按 id 读取一条长期画像节点，让手工画像持久化能判断一个“看似失败”的插入是否其实已经落库。
func (s *Store) loadProfileNodeByID(ctx context.Context, nodeID uint64) (logicdomain.ProfileNodeRecord, bool, error) {
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.id = ?
LIMIT 1
`, nodeID)
	if err != nil {
		return logicdomain.ProfileNodeRecord{}, false, err
	}
	if len(rows) == 0 {
		return logicdomain.ProfileNodeRecord{}, false, nil
	}
	return rows[0].toRecord(), true, nil
}

// loadProfileNodeStatusesByIDs fetches lightweight lifecycle state for a small node-id set so uncertain status updates can be reconciled.
// loadProfileNodeStatusesByIDs 用于读取一小批节点的轻量生命周期状态，让“不确定”的状态更新能够做状态对账。
func (s *Store) loadProfileNodeStatusesByIDs(ctx context.Context, nodeIDs []uint64) ([]profileNodeStatusRow, error) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	nodeIDParams := sqliteUint64Params(nodeIDs)
	rows, err := queryRows[profileNodeStatusRow](s, ctx, fmt.Sprintf(`
SELECT id, profile_status, superseded_by_id, status_reason, updated_timestamp
FROM vmm_profile_nodes
WHERE id IN (%s)
ORDER BY id ASC
`, nodeIDPlaceholders), nodeIDParams...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// renderedProfileScopeTable maps one domain profile type to the concrete scope table used for durable rendered-profile blobs.
// renderedProfileScopeTable 用于把一个领域画像类型映射到持久化渲染画像 Blob 所在的具体 scope 表。
func renderedProfileScopeTable(profileType int) (string, error) {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return "vmm_users", nil
	case logicdomain.ProfileTypeTeam:
		return "vmm_teams", nil
	case logicdomain.ProfileTypeSpace:
		return "vmm_spaces", nil
	case logicdomain.ProfileTypeProject:
		return "vmm_projects", nil
	default:
		return "", logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
}

// loadRenderedProfileByTarget reads the durable scope blob after one uncertain update so callers can verify whether the final profile text already committed.
// loadRenderedProfileByTarget 用于在 scope 画像更新不确定后读取长期 Blob，让调用方验证最终画像文本是否已经提交成功。
func (s *Store) loadRenderedProfileByTarget(ctx context.Context, profileType int, bindID uint64) (string, error) {
	table, err := renderedProfileScopeTable(profileType)
	if err != nil {
		return "", err
	}
	rows, err := queryRows[profileBlobRow](s, ctx, fmt.Sprintf(`SELECT profile FROM %s WHERE id = ? LIMIT 1`, table), bindID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Profile, nil
}

// loadRenderedProfileBatch reads exact rendered-profile rows for one scope so a batch update with uncertain commit outcome can be reconciled.
// loadRenderedProfileBatch 用于读取一个 scope 的精确渲染画像行，让提交结果不确定的批量更新可以执行对账。
func (s *Store) loadRenderedProfileBatch(ctx context.Context, profileType int, ids []uint64) ([]renderedProfileBatchRow, error) {
	normalizedIDs := storageutil.NormalizeUint64List(ids)
	if len(normalizedIDs) == 0 {
		return nil, nil
	}
	table, err := renderedProfileScopeTable(profileType)
	if err != nil {
		return nil, err
	}
	rows, err := queryRows[renderedProfileBatchRow](s, ctx, fmt.Sprintf(`
SELECT id, profile, updated_at
FROM %s
WHERE id IN (%s)
ORDER BY id ASC
`, table, sqlitePlaceholders(len(normalizedIDs))), sqliteUint64Params(normalizedIDs)...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// reconcileProfileInstructionCreate checks whether one insert that returned a commit-uncertain error has already materialized in SQLite.
// reconcileProfileInstructionCreate 用于检查一条返回“提交结果不确定”错误的插入，是否实际上已经在 SQLite 中落库。
func (s *Store) reconcileProfileInstructionCreate(ctx context.Context, expected logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, expected.ID)
	if err != nil || !found {
		return logicdomain.ProfileInstructionRecord{}, false, err
	}
	if stored.ProfileType != expected.ProfileType || stored.BindID != expected.BindID || strings.TrimSpace(stored.Instruction) != strings.TrimSpace(expected.Instruction) {
		return logicdomain.ProfileInstructionRecord{}, false, nil
	}
	return stored, true, nil
}

// reconcileManualProfileNodeInsert checks whether one node insert already committed even though the gateway returned an uncertain failure.
// reconcileManualProfileNodeInsert 用于检查某条节点插入是否已经提交，即便网关对这次写入返回了“不确定失败”。
func (s *Store) reconcileManualProfileNodeInsert(ctx context.Context, target logicdomain.ProfileTargetRef, expected logicdomain.ProfileNodeCandidate, expectedID uint64) (logicdomain.ProfileNodeRecord, bool, error) {
	stored, found, err := s.loadProfileNodeByID(ctx, expectedID)
	if err != nil || !found {
		return logicdomain.ProfileNodeRecord{}, false, err
	}
	if stored.ProfileType != target.ProfileType ||
		stored.BindID != target.BindID ||
		strings.TrimSpace(stored.Content) != strings.TrimSpace(expected.Content) ||
		stored.SourceKind != expected.SourceKind ||
		stored.SourceID != expected.SourceID {
		return logicdomain.ProfileNodeRecord{}, false, nil
	}
	return stored, true, nil
}

// reconcileProfileNodesSuperseded verifies whether all target nodes already reached the desired superseded state after one uncertain update error.
// reconcileProfileNodesSuperseded 用于验证在一次不确定更新报错后，目标节点是否已经全部进入期望的 superseded 状态。
func (s *Store) reconcileProfileNodesSuperseded(ctx context.Context, nodeIDs []uint64, supersededByID uint64) (bool, error) {
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, nodeIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(storageutil.NormalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus != logicdomain.ProfileStatusSuperseded || row.SupersededByID != supersededByID {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryNodesSuperseded verifies whether all target memory rows reached this write's superseded state after one uncertain update error.
// reconcileMemoryNodesSuperseded 用于验证在一次不确定更新报错后，目标 memory 行是否已经全部进入本次写入要求的 superseded 状态。
func (s *Store) reconcileMemoryNodesSuperseded(ctx context.Context, memoryIDs []uint64, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(memoryIDs)
	rows, err := s.LoadMemoryNodesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.Status != logicdomain.MemoryStatusSuperseded ||
			!sessionTimestampReached(row.UpdatedAt, updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryNodesDeleted verifies whether all target memory rows reached the manual-delete state after one uncertain status update.
// reconcileMemoryNodesDeleted 用于验证一次不确定状态更新后，目标 memory 行是否已经全部进入手工删除状态。
func (s *Store) reconcileMemoryNodesDeleted(ctx context.Context, memoryIDs []uint64, reason string, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(memoryIDs)
	rows, err := s.LoadMemoryNodesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	expectedReason := strings.TrimSpace(reason)
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.Status != logicdomain.MemoryStatusDeleted ||
			row.StatusReason != expectedReason ||
			!sessionTimestampReached(row.UpdatedAt, updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryAdoptionUpdate verifies whether one uncertain adoption update already reached its exact durable memory target.
// reconcileMemoryAdoptionUpdate 用于验证一次不确定的采纳更新是否已经达到精确的长期记忆目标。
func (s *Store) reconcileMemoryAdoptionUpdate(ctx context.Context, expected logicdomain.MemoryNodeRecord) (bool, error) {
	rows, err := s.LoadMemoryNodesByIDs(ctx, []uint64{expected.ID})
	if err != nil {
		return false, err
	}
	return len(rows) == 1 && sameMemoryNodeRecord(rows[0], expected), nil
}

// executeDirectMemoryInsert writes one direct-memory row and reconciles commit-unknown inserts because the vector row was already written before this relation step.
// executeDirectMemoryInsert 用于写入一条主动记忆关系行，并在提交未知时对账，因为进入该关系步骤前向量行已经写入成功。
func (s *Store) executeDirectMemoryInsert(ctx context.Context, operation string, record logicdomain.MemoryNodeRecord) error {
	insertStatement := parameterizedMemoryNodeInsertStatement(record)
	insertResult, insertErr := s.execResult(ctx, insertStatement.SQL, insertStatement.Params...)
	if insertErr != nil {
		if isSQLiteOutcomeUncertainError(insertErr) {
			recoveredRows, reconcileErr := s.LoadMemoryNodesByIDs(ctx, []uint64{record.ID})
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation:            operation,
					Message:              fmt.Sprintf("insert direct memory node: %v; reconcile failed: %v", insertErr, reconcileErr),
					FreshVectorReference: true,
				}
			}
			if len(recoveredRows) == 1 && sameMemoryNodeRecord(recoveredRows[0], record) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation:            operation,
				Message:              fmt.Sprintf("insert direct memory node: %v", insertErr),
				FreshVectorReference: true,
			}
		}
		return fmt.Errorf("insert direct memory node: %w", insertErr)
	}
	if insertResult.RowsChanged != 1 {
		rowDriftErr := fmt.Errorf("insert direct memory node affected %d rows, want 1", insertResult.RowsChanged)
		if insertResult.RowsChanged > 0 {
			return logicdomain.OutcomeUncertainError{
				Operation:            operation,
				Message:              rowDriftErr.Error(),
				FreshVectorReference: true,
			}
		}
		return rowDriftErr
	}
	return nil
}

// reconcileProfileNodesRetired verifies whether all target nodes already left the active set after one uncertain retire update.
// reconcileProfileNodesRetired 用于验证在一次不确定退役更新后，目标节点是否已经全部离开 active 集合。
func (s *Store) reconcileProfileNodesRetired(ctx context.Context, nodeIDs []uint64) (bool, error) {
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, nodeIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(storageutil.NormalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus == logicdomain.ProfileStatusActive {
			return false, nil
		}
	}
	return true, nil
}

// reconcileProfileNodesExpired verifies whether all selected lifecycle nodes already reached this expiry write after one uncertain update error.
// reconcileProfileNodesExpired 用于验证一次不确定更新错误后，所有选中的生命周期节点是否已经进入本次过期写入要求的状态。
func (s *Store) reconcileProfileNodesExpired(ctx context.Context, nodeIDs []uint64, reason string, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(nodeIDs)
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	expectedReason := strings.TrimSpace(reason)
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.ProfileStatus != logicdomain.ProfileStatusExpired ||
			strings.TrimSpace(row.StatusReason) != expectedReason ||
			!sessionTimestampReached(unixMilliToTime(row.UpdatedTimestamp), updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// sameProfileInstructionStatusUpdate reports whether a read-back instruction row matches the exact status-update payload that may have committed.
// sameProfileInstructionStatusUpdate 用于判断回读的 instruction 行是否精确匹配一次可能已经提交的状态更新载荷。
func sameProfileInstructionStatusUpdate(stored logicdomain.ProfileInstructionRecord, instructionID uint64, status int, reviewResult, failureReason string, updatedMs int64) bool {
	return stored.ID == instructionID &&
		stored.Status == status &&
		strings.TrimSpace(stored.ReviewResult) == strings.TrimSpace(reviewResult) &&
		strings.TrimSpace(stored.FailureReason) == strings.TrimSpace(failureReason) &&
		sessionTimestampReached(stored.UpdatedAt, updatedMs)
}

// reconcileProfileInstructionApplied verifies whether the instruction row already reached this write's applied payload after one uncertain update.
// reconcileProfileInstructionApplied 用于验证一条 instruction 行在不确定更新后是否已经进入本次写入要求的 applied 载荷。
func (s *Store) reconcileProfileInstructionApplied(ctx context.Context, instructionID uint64, reviewResult string, updatedMs int64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return sameProfileInstructionStatusUpdate(stored, instructionID, logicdomain.ProfileInstructionStatusApplied, reviewResult, "", updatedMs), nil
}

// reconcileProfileInstructionFailed verifies whether the instruction row already reached this write's failed payload after one uncertain failure-mark update.
// reconcileProfileInstructionFailed 用于验证一条 instruction 行在不确定失败回写后是否已经进入本次写入要求的 failed 载荷。
func (s *Store) reconcileProfileInstructionFailed(ctx context.Context, instructionID uint64, failureReason, reviewResult string, updatedMs int64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return sameProfileInstructionStatusUpdate(stored, instructionID, logicdomain.ProfileInstructionStatusFailed, reviewResult, failureReason, updatedMs), nil
}

// reconcileRenderedProfileTarget verifies whether the durable scope profile blob already contains the final rendered text after one uncertain update.
// reconcileRenderedProfileTarget 用于验证在一次不确定更新后，长期 scope 画像 Blob 是否已经包含最终渲染文本。
func (s *Store) reconcileRenderedProfileTarget(ctx context.Context, profileType int, bindID uint64, renderedProfile string) (bool, error) {
	stored, err := s.loadRenderedProfileByTarget(ctx, profileType, bindID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(stored) == strings.TrimSpace(renderedProfile), nil
}

// reconcileRenderedProfileBatch verifies every row in one uncertain scope batch reached the exact profile and updated_at payload from this write.
// reconcileRenderedProfileBatch 用于验证一次不确定 scope 批量写入中的每一行都达到本次写入的精确 profile 和 updated_at 载荷。
func (s *Store) reconcileRenderedProfileBatch(ctx context.Context, profileType int, ids []uint64, profiles map[uint64]string, updatedAt string) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(ids)
	if len(expectedIDs) == 0 {
		return true, nil
	}
	rows, err := s.loadRenderedProfileBatch(ctx, profileType, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	for index, expectedID := range expectedIDs {
		expectedProfile, ok := profiles[expectedID]
		if !ok {
			return false, nil
		}
		row := rows[index]
		if row.ID != expectedID || row.Profile != expectedProfile || row.UpdatedAt != updatedAt {
			return false, nil
		}
	}
	return true, nil
}

// CreateProfileInstruction inserts one pending manual profile instruction so later review results can reference a durable instruction id.
// CreateProfileInstruction 用于插入一条 pending 的手工画像指令，让后续评审结果能够引用稳定的 instruction id。
func (s *Store) CreateProfileInstruction(ctx context.Context, record logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, error) {
	if !logicdomain.ValidProfileType(record.ProfileType) {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
	if record.BindID == 0 {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if strings.TrimSpace(record.Instruction) == "" {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "instruction", Message: "is required"}
	}
	if !logicdomain.ValidProfileInstructionStatus(record.Status) {
		return logicdomain.ProfileInstructionRecord{}, logicdomain.ValidationError{Field: "status", Message: "must be one supported profile instruction status"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_profile_instructions")
	if err != nil {
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("allocate profile instruction id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	record.ID = nextID
	record.CreatedAt = now
	record.UpdatedAt = now
	if err := s.exec(ctx, `
INSERT INTO vmm_profile_instructions (
  id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
`, nextID, record.ProfileType, record.BindID, strings.TrimSpace(record.Instruction), record.Status, strings.TrimSpace(record.ReviewResult), strings.TrimSpace(record.FailureReason), nowMs, nowMs); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, ok, reconcileErr := s.reconcileProfileInstructionCreate(ctx, record)
			if reconcileErr == nil && ok {
				return recovered, nil
			}
			if reconcileErr != nil {
				return logicdomain.ProfileInstructionRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert profile instruction",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ProfileInstructionRecord{}, logicdomain.OutcomeUncertainError{
				Operation: "insert profile instruction",
				Message:   err.Error(),
			}
		}
		return logicdomain.ProfileInstructionRecord{}, fmt.Errorf("insert profile instruction: %w", err)
	}
	return record, nil
}

// FailProfileInstruction marks one pending manual profile instruction as failed and stores the failure reason for later debugging.
// FailProfileInstruction 用于把一条 pending 手工画像指令标记为失败，并保存失败原因，便于后续调试。
func (s *Store) FailProfileInstruction(ctx context.Context, instructionID uint64, failureReason, reviewResult string) error {
	if instructionID == 0 {
		return logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}
	nowMs := time.Now().UTC().UnixMilli()
	if err := s.execExpectRowsChanged(ctx, "mark profile instruction failed", 1, `
UPDATE vmm_profile_instructions
SET instruction_status = ?,
    review_result_json = ?,
    failure_reason = ?,
    updated_timestamp = ?
WHERE id = ?
  AND instruction_status = ?;
`, logicdomain.ProfileInstructionStatusFailed, strings.TrimSpace(reviewResult), strings.TrimSpace(failureReason), nowMs, instructionID, logicdomain.ProfileInstructionStatusPending); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionFailed(ctx, instructionID, failureReason, reviewResult, nowMs)
			if reconcileErr == nil && recovered {
				return nil
			}
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "mark profile instruction failed",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "mark profile instruction failed",
				Message:   err.Error(),
			}
		}
		return err
	}
	return nil
}

// ApplyManualProfileInstruction persists the reviewed manual profile instruction inside one serialized write section and
// reconciles "commit outcome uncertain" failures before giving up, so duplicate retries are less likely to create extra nodes.
// ApplyManualProfileInstruction 用于在单个串行写入区间内持久化手工画像评审结果，
// 并在遇到“提交结果不确定”错误时先做状态对账，从而降低重复重试制造额外节点的概率。
func (s *Store) ApplyManualProfileInstruction(ctx context.Context, target logicdomain.ProfileTargetRef, instruction logicdomain.ProfileInstructionRecord, nodes []logicdomain.ProfileNodeCandidate, retired []logicdomain.ProfileRetireDecision, renderedProfile, reviewResult string) (logicdomain.ManualProfileInstructionApplyResult, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if instruction.ID == 0 {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "instruction_id", Message: "must be one persisted profile instruction id"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	profileStartID := uint64(0)
	var err error
	if len(nodes) > 0 {
		profileStartID, err = s.nextNumericID(ctx, "vmm_profile_nodes")
		if err != nil {
			return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("allocate manual profile node id: %w", err)
		}
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	nowRFC3339 := now.Format(time.RFC3339Nano)

	accepted := make([]logicdomain.ProfileNodeRecord, 0, len(nodes))
	mutated := false
	// Track nodes already retired by supersede updates so returned retire decisions do not trigger duplicate SQLite writes.
	// 记录已通过 supersede 更新退役的节点，避免返回用退役决策再次触发重复 SQLite 写入。
	supersededNodeIDs := make(map[uint64]struct{})
	for idx, node := range nodes {
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].status", Message: "must be one supported profile status"}
		}
		if !logicdomain.ValidProfilePriority(node.Priority) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].priority", Message: "must be one supported profile priority"}
		}
		if !logicdomain.ValidProfileLevel(node.ProfileLevel) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].profile_level", Message: "must be one supported profile level"}
		}
		if !logicdomain.ValidProfileSourceKind(node.SourceKind) {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].source_kind", Message: "must be one supported profile source kind"}
		}
		if node.RefreshWeight < 0 {
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "nodes[" + strconv.Itoa(idx) + "].refresh_weight", Message: "must be >= 0"}
		}
		if strings.TrimSpace(node.ProfileDate) == "" {
			node.ProfileDate = logicdomain.FormatDisplayDate(now)
		}
		insertedID := profileStartID + uint64(idx)
		acceptedRecord := logicdomain.ProfileNodeRecord{
			ID:            insertedID,
			TurnID:        0,
			ProfileType:   target.ProfileType,
			BindID:        target.BindID,
			Content:       strings.TrimSpace(node.Content),
			Status:        node.Status,
			Priority:      node.Priority,
			ProfileLevel:  node.ProfileLevel,
			LevelReason:   strings.TrimSpace(node.LevelReason),
			RefreshWeight: node.RefreshWeight,
			ProfileDate:   strings.TrimSpace(node.ProfileDate),
			SourceKind:    node.SourceKind,
			SourceID:      node.SourceID,
			StatusReason:  strings.TrimSpace(node.StatusReason),
			ExpiresAt:     node.ExpiresAt.UTC(),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		// Persist the new active node with typed params so reviewer text never becomes executable SQL.
		// 使用强类型参数持久化新的 active 节点，避免评审文本进入可执行 SQL。
		insertStatement := parameterizedProfileNodeInsertStatement(insertedID, nil, target.ProfileType, target.BindID, node, nowMs)
		if err := s.exec(ctx, insertStatement.SQL, insertStatement.Params...); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, ok, reconcileErr := s.reconcileManualProfileNodeInsert(ctx, target, node, insertedID)
				if reconcileErr == nil && ok {
					acceptedRecord = recovered
					mutated = true
				} else if reconcileErr != nil {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("insert manual profile node %d", insertedID),
						Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
					}
				} else {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("insert manual profile node %d", insertedID),
						Message:   err.Error(),
					}
				}
			} else {
				return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("insert manual profile node %d: %w", insertedID, err))
			}
		}
		mutated = true
		supersedeNodeIDs := storageutil.NormalizeUint64List(node.SupersedeNodeIDs)
		if supersedeStatement, ok := parameterizedProfileNodesSupersedeStatement(target.ProfileType, target.BindID, supersedeNodeIDs, insertedID, strings.TrimSpace(node.StatusReason), nowMs); ok {
			// Retire the superseded nodes in a separate execute call because SQLite's shared-connection batch path
			// can enter a resource-deadlock state when one script both inserts and updates the same hot table.
			// 单独执行 supersede 更新，因为 SQLite 的共享连接批量路径在同一脚本里同时插入并更新热点表时，
			// 可能进入 resource deadlock 状态。
			if err := s.execExpectRowsChanged(ctx, fmt.Sprintf("supersede manual profile nodes for %d", insertedID), int64(len(supersedeNodeIDs)), supersedeStatement.SQL, supersedeStatement.Params...); err != nil {
				if isSQLiteOutcomeUncertainError(err) {
					recovered, reconcileErr := s.reconcileProfileNodesSuperseded(ctx, supersedeNodeIDs, insertedID)
					if reconcileErr == nil && recovered {
						mutated = true
						for _, nodeID := range supersedeNodeIDs {
							supersededNodeIDs[nodeID] = struct{}{}
						}
						accepted = append(accepted, acceptedRecord)
						continue
					}
					if reconcileErr != nil {
						return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation: fmt.Sprintf("supersede manual profile nodes for %d", insertedID),
							Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
						}
					}
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("supersede manual profile nodes for %d", insertedID),
						Message:   err.Error(),
					}
				}
				return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("supersede manual profile nodes for %d: %w", insertedID, err))
			}
			mutated = true
			for _, nodeID := range supersedeNodeIDs {
				supersededNodeIDs[nodeID] = struct{}{}
			}
		}
		accepted = append(accepted, acceptedRecord)
	}
	for _, decision := range standaloneSQLiteProfileRetireDecisions(retired, supersededNodeIDs) {
		if decision.NodeID == 0 {
			continue
		}
		// Apply standalone retire decisions after inserts so reviewer-directed removals stay explicit and debuggable.
		// 在插入完成后再应用独立退役决策，让评审器给出的移除动作保持明确且可调试。
		retireStatement, ok := parameterizedSingleProfileNodeRetireStatement(decision.NodeID, decision.Reason, nowMs)
		if !ok {
			continue
		}
		if err := s.execExpectRowsChanged(ctx, fmt.Sprintf("retire manual profile node %d", decision.NodeID), 1, retireStatement.SQL, retireStatement.Params...); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, reconcileErr := s.reconcileProfileNodesRetired(ctx, []uint64{decision.NodeID})
				if reconcileErr == nil && recovered {
					mutated = true
					continue
				}
				if reconcileErr != nil {
					return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation: fmt.Sprintf("retire manual profile node %d", decision.NodeID),
						Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
					}
				}
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: fmt.Sprintf("retire manual profile node %d", decision.NodeID),
					Message:   err.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("retire manual profile node %d: %w", decision.NodeID, err))
		}
		mutated = true
	}

	updateProfileSQL := parameterizedProfileTargetUpdateSQL(target.ProfileType)
	if strings.TrimSpace(updateProfileSQL) == "" {
		return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	// Refresh the rendered scope blob before marking the instruction as applied so a missing target row cannot leave a successful instruction status behind.
	// 先刷新 scope 画像 Blob，再把指令标记为 applied，避免目标行缺失时留下成功状态。
	if err := s.execExpectRowsChanged(ctx, "update rendered manual profile target", 1, updateProfileSQL, strings.TrimSpace(renderedProfile), nowRFC3339, target.BindID); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileRenderedProfileTarget(ctx, target.ProfileType, target.BindID, renderedProfile)
			if reconcileErr == nil && recovered {
				mutated = true
				goto markInstructionApplied
			}
			if reconcileErr != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "update rendered manual profile target",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
				Operation: "update rendered manual profile target",
				Message:   err.Error(),
			}
		}
		return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("update rendered manual profile target: %w", err))
	}
	mutated = true
markInstructionApplied:
	// Mark the instruction as applied only after every node mutation and the rendered profile update have succeeded.
	// 只有在全部节点变更与渲染画像回写都成功后，才把指令标记为 applied。
	if err := s.execExpectRowsChanged(ctx, "mark manual profile instruction applied", 1, `
UPDATE vmm_profile_instructions
SET instruction_status = ?,
    review_result_json = ?,
    failure_reason = '',
    updated_timestamp = ?
WHERE id = ?
  AND instruction_status = ?;
`, logicdomain.ProfileInstructionStatusApplied, strings.TrimSpace(reviewResult), nowMs, instruction.ID, logicdomain.ProfileInstructionStatusPending); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionApplied(ctx, instruction.ID, reviewResult, nowMs)
			if reconcileErr == nil && recovered {
				return logicdomain.ManualProfileInstructionApplyResult{
					InstructionID: instruction.ID,
					AcceptedNodes: accepted,
					RetiredNodes:  retired,
				}, nil
			}
			if reconcileErr != nil {
				return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "mark manual profile instruction applied",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return logicdomain.ManualProfileInstructionApplyResult{}, logicdomain.OutcomeUncertainError{
				Operation: "mark manual profile instruction applied",
				Message:   err.Error(),
			}
		}
		return logicdomain.ManualProfileInstructionApplyResult{}, sqlitePartialMutationError(mutated, "apply manual profile instruction", fmt.Errorf("mark manual profile instruction applied: %w", err))
	}
	return logicdomain.ManualProfileInstructionApplyResult{
		InstructionID: instruction.ID,
		AcceptedNodes: accepted,
		RetiredNodes:  retired,
	}, nil
}

// standaloneSQLiteProfileRetireDecisions removes retire decisions already persisted by a replacement-node supersede update.
// standaloneSQLiteProfileRetireDecisions 用于移除已经通过替代节点 supersede 更新持久化的退役决策。
func standaloneSQLiteProfileRetireDecisions(retired []logicdomain.ProfileRetireDecision, supersededNodeIDs map[uint64]struct{}) []logicdomain.ProfileRetireDecision {
	if len(retired) == 0 || len(supersededNodeIDs) == 0 {
		return retired
	}
	standalone := make([]logicdomain.ProfileRetireDecision, 0, len(retired))
	for _, decision := range retired {
		if _, alreadySuperseded := supersededNodeIDs[decision.NodeID]; alreadySuperseded {
			continue
		}
		standalone = append(standalone, decision)
	}
	return standalone
}

// ConvergeExpiredProfileNodes marks due active profile nodes as expired and returns the affected user/project targets with their remaining renderable active nodes.
// ConvergeExpiredProfileNodes 用于把已到期的 active 画像节点收敛为 expired，并返回受影响的 user/project 目标及其剩余可渲染活跃节点。
func (s *Store) ConvergeExpiredProfileNodes(ctx context.Context, limit int) ([]logicdomain.ProfileRenderTargetSnapshot, error) {
	if limit <= 0 {
		limit = 256
	}
	nowMs := time.Now().UTC().UnixMilli()

	// Serialize expiry convergence so status flips and follow-up profile snapshots are observed from one deterministic SQL window.
	// 串行化过期收敛，确保状态切换与后续画像快照都来自同一个确定性的 SQL 窗口。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT id, turn_id, profile_type, bind_id, content, profile_status,
       priority, profile_level, level_reason, refresh_weight,
       source_kind, source_id, status_reason,
       expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE profile_status = ? AND expires_timestamp > 0 AND expires_timestamp <= ?
ORDER BY expires_timestamp ASC, id ASC
LIMIT ?
`, logicdomain.ProfileStatusActive, nowMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query expired profile nodes: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	expiredIDs := make([]uint64, 0, len(rows))
	targetIndex := map[string]int{}
	targets := make([]logicdomain.ProfileRenderTargetSnapshot, 0)
	for _, row := range rows {
		expiredIDs = append(expiredIDs, row.ID)
		key := strconv.Itoa(row.ProfileType) + ":" + strconv.FormatUint(row.BindID, 10)
		if _, exists := targetIndex[key]; exists {
			continue
		}
		targetIndex[key] = len(targets)
		targets = append(targets, logicdomain.ProfileRenderTargetSnapshot{
			ProfileType: row.ProfileType,
			BindID:      row.BindID,
		})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ProfileType != targets[j].ProfileType {
			return targets[i].ProfileType < targets[j].ProfileType
		}
		return targets[i].BindID < targets[j].BindID
	})
	expireReason := "expired by lifecycle convergence"
	expireStatement, ok := parameterizedProfileNodesExpireStatement(expiredIDs, expireReason, nowMs)
	if !ok {
		return nil, fmt.Errorf("mark expired profile nodes: no valid expired node ids")
	}
	// Verify the exact expired-row boundary because the following render snapshots assume every selected node has already left the active set.
	// 校验精确的过期行边界，因为后续渲染快照默认所有已选节点都已经离开 active 集合。
	expireRecovered := false
	expireResult, err := s.execResult(ctx, expireStatement.SQL, expireStatement.Params...)
	if err != nil {
		writeErr := fmt.Errorf("mark expired profile nodes: %w", err)
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileNodesExpired(ctx, expiredIDs, expireReason, nowMs)
			if reconcileErr != nil {
				return nil, logicdomain.OutcomeUncertainError{
					Operation: "converge expired profile nodes",
					Message:   writeErr.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !recovered {
				return nil, logicdomain.OutcomeUncertainError{
					Operation: "converge expired profile nodes",
					Message:   writeErr.Error(),
				}
			}
			expireRecovered = true
		} else {
			return nil, writeErr
		}
	}
	if expectedRows := int64(len(expiredIDs)); !expireRecovered && expireResult.RowsChanged != expectedRows {
		err := fmt.Errorf("expire sqlite profile nodes by lifecycle convergence affected %d rows, want %d", expireResult.RowsChanged, expectedRows)
		return nil, sqlitePartialMutationError(expireResult.RowsChanged > 0, "converge expired profile nodes", err)
	}
	for idx := range targets {
		nodes, err := s.loadActiveProfileNodes(ctx, targets[idx].ProfileType, targets[idx].BindID, nowMs)
		if err != nil {
			return nil, fmt.Errorf("load active profile nodes for render target: %w", err)
		}
		targets[idx].Nodes = nodes
	}
	return targets, nil
}

// ReplaceRenderedProfiles writes the already rendered scope-level profile blobs back into SQLite after lifecycle convergence, manual instruction review, or batch review.
// ReplaceRenderedProfiles 用于在生命周期收敛、手工画像评审或批量评审之后，把已经渲染好的 scope 画像文本回写到 SQLite。
func (s *Store) ReplaceRenderedProfiles(ctx context.Context, updates logicdomain.RenderedProfileSet) error {
	if len(updates.UserProfiles) == 0 && len(updates.TeamProfiles) == 0 && len(updates.SpaceProfiles) == 0 && len(updates.ProjectProfiles) == 0 {
		return nil
	}

	// Serialize durable profile-blob replacement so the cached profile text stays aligned with the lifecycle decisions already committed in SQL.
	// 串行化长期 profile Blob 的替换，确保缓存画像文本与前面已经提交到 SQL 的生命周期决策保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nowRFC3339 := time.Now().UTC().Format(time.RFC3339Nano)
	// Track successful earlier scope batches because a later miss means rendered-profile replacement has already partially committed.
	// 记录前序 scope 批量写入是否已成功，因为后续漏命中表示渲染画像替换已经部分提交。
	mutated := false
	userIDs := sortedProfileBindingIDs(updates.UserProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeUser, `
UPDATE vmm_users
SET profile = ?, updated_at = ?
WHERE id = ?
`, userIDs, updates.UserProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered user profiles: %w", err))
	}
	if len(userIDs) > 0 {
		mutated = true
	}
	teamIDs := sortedProfileBindingIDs(updates.TeamProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeTeam, `
UPDATE vmm_teams
SET profile = ?, updated_at = ?
WHERE id = ?
`, teamIDs, updates.TeamProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered team profiles: %w", err))
	}
	if len(teamIDs) > 0 {
		mutated = true
	}
	spaceIDs := sortedProfileBindingIDs(updates.SpaceProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeSpace, `
UPDATE vmm_spaces
SET profile = ?, updated_at = ?
WHERE id = ?
`, spaceIDs, updates.SpaceProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered space profiles: %w", err))
	}
	if len(spaceIDs) > 0 {
		mutated = true
	}
	projectIDs := sortedProfileBindingIDs(updates.ProjectProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, logicdomain.ProfileTypeProject, `
UPDATE vmm_projects
SET profile = ?, updated_at = ?
WHERE id = ?
`, projectIDs, updates.ProjectProfiles, nowRFC3339); err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "replace rendered profiles", fmt.Errorf("replace rendered project profiles: %w", err))
	}
	return nil
}

// replaceRenderedProfileBatch reuses SQLite ExecuteBatch for one scope table so repeated profile-blob updates stay in the sqlite-native transport path.
// replaceRenderedProfileBatch 用于对单个 scope 表复用 SQLite ExecuteBatch，让重复的 profile Blob 更新保持在 sqlite 原生传输路径内。
func (s *Store) replaceRenderedProfileBatch(ctx context.Context, profileType int, sql string, ids []uint64, profiles map[uint64]string, nowRFC3339 string) error {
	if len(ids) == 0 {
		return nil
	}
	items := make([][]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, []any{profiles[id], nowRFC3339, id})
	}
	result, err := s.execBatchResult(ctx, sql, items)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileRenderedProfileBatch(ctx, profileType, ids, profiles, nowRFC3339)
			if reconcileErr != nil {
				return fmt.Errorf("%w; reconcile rendered profile batch: %v", err, reconcileErr)
			}
			if recovered {
				return nil
			}
		}
		return err
	}
	if expectedRows := int64(len(ids)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("replace rendered profile batch affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "replace rendered profile batch", err)
	}
	return nil
}

// loadActiveProfileNodes is the shared query helper used by profile review and expiry convergence to fetch only currently renderable active nodes.
// loadActiveProfileNodes 用于作为画像评审和过期收敛共用查询助手，只加载当前仍可渲染的 active 节点。
func (s *Store) loadActiveProfileNodes(ctx context.Context, profileType int, bindID uint64, nowMs int64) ([]logicdomain.ProfileActiveNodeRecord, error) {
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT n.id, n.turn_id, n.profile_type, n.bind_id, n.content, n.profile_status,
       n.priority, n.profile_level, n.level_reason, n.refresh_weight,
       n.source_kind, n.source_id, n.status_reason,
       n.expires_timestamp, n.superseded_by_id, n.profile_date,
       n.created_timestamp, n.updated_timestamp,
       COALESCE(t.created_timestamp, n.created_timestamp) AS profile_date_anchor_timestamp
FROM vmm_profile_nodes AS n
LEFT JOIN vmm_turn_records AS t ON t.id = n.turn_id
WHERE n.profile_type = ? AND n.bind_id = ? AND n.profile_status = ? AND (n.expires_timestamp <= 0 OR n.expires_timestamp > ?)
ORDER BY profile_date_anchor_timestamp ASC, n.id ASC
`, profileType, bindID, logicdomain.ProfileStatusActive, nowMs)
	if err != nil {
		return nil, err
	}
	nodes := make([]logicdomain.ProfileActiveNodeRecord, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, row.toDomain())
	}
	return nodes, nil
}

// LoadPendingSessionTurns returns the oldest not-yet-extracted turn rows for one session so queued post-action workers can drain pending work in durable order.
// LoadPendingSessionTurns 用于返回某个 session 中尚未提取的最早 turn 行，让排队的 post-action 工作器按持久化顺序消化待处理工作。
func (s *Store) LoadPendingSessionTurns(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ? AND extracted_status = ?
ORDER BY id ASC
`, session.SessionID, logicdomain.TurnExtractedStatusPending)
	if err != nil {
		return nil, fmt.Errorf("query pending session turns: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	return turns, nil
}

// parameterizedMarkTurnAsCorruptedStatement builds the guarded pending-to-done update used when a queued turn payload is unreadable.
// parameterizedMarkTurnAsCorruptedStatement 用于构造排队 turn 载荷不可读时使用的受保护 pending 到 done 更新语句。
func parameterizedMarkTurnAsCorruptedStatement(sessionID, turnID uint64, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_turn_records
SET extracted_status = ?, updated_timestamp = ?
WHERE id = ? AND session_id = ? AND extracted_status = ?;
`,
		Params: []any{logicdomain.TurnExtractedStatusDone, updatedMs, turnID, sessionID, logicdomain.TurnExtractedStatusPending},
	}
}

// MarkTurnAsCorrupted marks one pending turn whose dehydrated payload cannot be decoded as done so it stops being returned by LoadPendingSessionTurns.
// MarkTurnAsCorrupted 用于将无法解码的损坏 pending turn 标记为已处理，让它不再被 LoadPendingSessionTurns 返回。
func (s *Store) MarkTurnAsCorrupted(ctx context.Context, session logicdomain.SessionRef, turnID uint64) error {
	if session.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if turnID == 0 {
		return logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	nowMs := time.Now().UTC().UnixMilli()

	// Serialize corrupted-turn draining with normal turn analysis because both paths transition the same pending turn state machine.
	// 将损坏 turn 消化与正常 turn 分析串行化，因为两条路径都会推进同一个 pending turn 状态机。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Capture the exact pending row before the guarded update so commit-unknown reconciliation can prove this narrow state transition instead of guessing from status alone.
	// 在受保护更新前捕获精确 pending 行，让提交未知对账可以证明这次窄状态迁移，而不是只凭状态字段猜测。
	beforeTurn, beforeFound, err := s.loadTurnRecordByID(ctx, turnID)
	if err != nil {
		return fmt.Errorf("load corrupted turn before mark: %w", err)
	}

	// Preserve monotonic turn activity time when the durable row already carries a newer timestamp than the local clock sample.
	// 当长期 turn 行已有比本地时钟采样更新的时间戳时，保持 turn 活动时间单调不回退。
	updatedMs := nowMs
	if beforeFound && !beforeTurn.UpdatedAt.IsZero() {
		beforeUpdatedMs := beforeTurn.UpdatedAt.UTC().UnixMilli()
		if beforeUpdatedMs > updatedMs {
			updatedMs = beforeUpdatedMs
		}
	}

	statement := parameterizedMarkTurnAsCorruptedStatement(session.SessionID, turnID, updatedMs)
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredTurn, recoveredFound, reconcileErr := s.loadTurnRecordByID(ctx, turnID)
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "mark turn as corrupted",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if beforeFound && recoveredFound && sameCorruptedTurnMark(recoveredTurn, beforeTurn, session.SessionID, turnID, updatedMs) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "mark turn as corrupted",
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("mark turn as corrupted: %w", err)
	}
	if err := sqliteAutocommitRowsChangedDriftError("mark turn as corrupted", "mark turn as corrupted", result.RowsChanged, 1); err != nil {
		return fmt.Errorf("mark turn as corrupted: %w", err)
	}
	return nil
}

// LoadRecentSessionTurns returns the latest persisted turn rows for one session regardless of extracted status, ordered from oldest to newest after the final window is chosen.
// LoadRecentSessionTurns 用于返回某个 session 最近持久化的 turn 行，不区分 extracted 状态；最终结果按从旧到新排序。
func (s *Store) LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ?
ORDER BY id DESC
LIMIT ?
`, session.SessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent session turns: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	for left, right := 0, len(turns)-1; left < right; left, right = left+1, right-1 {
		turns[left], turns[right] = turns[right], turns[left]
	}
	return turns, nil
}

// LoadRecentSessionHistory returns the latest extracted turn summaries for one session, ordered from oldest to newest for prompt assembly.
// LoadRecentSessionHistory 用于返回某个 session 最近已提炼的 turn 精要，并按从旧到新的顺序排列，供提示词组装使用。
func (s *Store) LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if limit <= 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE session_id = ? AND extracted_status = ? AND LENGTH(TRIM(details)) > 0
ORDER BY id DESC
LIMIT ?
`, session.SessionID, logicdomain.TurnExtractedStatusDone, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent session history: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	for left, right := 0, len(turns)-1; left < right; left, right = left+1, right-1 {
		turns[left], turns[right] = turns[right], turns[left]
	}
	return turns, nil
}

// LoadTurnsByIDs returns one explicit set of dehydrated turn rows so memory-detail RPCs can inspect the exact persisted payloads behind recalled turn ids.
// LoadTurnsByIDs 用于返回一组明确指定的脱水 turn 行，让记忆详情 RPC 可以查看召回 turn id 背后的精确持久化载荷。
func (s *Store) LoadTurnsByIDs(ctx context.Context, turnIDs []uint64) ([]logicdomain.SessionTurnRecord, error) {
	turnIDs = storageutil.NormalizeUint64List(turnIDs)
	if len(turnIDs) == 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	turnIDPlaceholders := sqlitePlaceholders(len(turnIDs))
	turnIDParams := sqliteUint64Params(turnIDs)
	rows, err := queryRows[turnRecordRow](s, ctx, fmt.Sprintf(`
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s)
ORDER BY id ASC
`, turnIDPlaceholders), turnIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query turns by ids: %w", err)
	}
	turns := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	for _, row := range rows {
		turns = append(turns, row.toDomain())
	}
	return turns, nil
}

// LoadTurnWindows returns the previous and next turn ids around each requested anchor turn inside the same session.
// LoadTurnWindows 用于返回每个请求锚点 turn 在同一 session 内前后相邻的 turn id。
func (s *Store) LoadTurnWindows(ctx context.Context, turnIDs []uint64, radius int) (map[uint64]logicdomain.TurnDetailWindow, error) {
	turnIDs = storageutil.NormalizeUint64List(turnIDs)
	if len(turnIDs) == 0 || radius <= 0 {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
	turnIDPlaceholders := sqlitePlaceholders(len(turnIDs))
	queryParams := sqliteUint64Params(turnIDs)
	queryParams = append(queryParams, radius, radius)
	rows, err := queryRows[turnWindowRow](s, ctx, fmt.Sprintf(`
WITH ordered_turns AS (
  SELECT id, session_id,
         ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id ASC) AS rn
  FROM vmm_turn_records
),
target_turns AS (
  SELECT id AS target_id, session_id, rn AS target_rn
  FROM ordered_turns
  WHERE id IN (%s)
)
SELECT t.target_id,
       o.id AS turn_id,
       CAST(o.rn - t.target_rn AS INTEGER) AS relative_pos
FROM target_turns t
JOIN ordered_turns o
  ON o.session_id = t.session_id
 AND o.rn BETWEEN t.target_rn - ? AND t.target_rn + ?
 AND o.id <> t.target_id
ORDER BY t.target_id ASC, o.rn ASC
`, turnIDPlaceholders), queryParams...)
	if err != nil {
		return nil, fmt.Errorf("query turn windows: %w", err)
	}
	windows := make(map[uint64]logicdomain.TurnDetailWindow, len(turnIDs))
	for _, turnID := range turnIDs {
		windows[turnID] = logicdomain.TurnDetailWindow{TurnID: turnID}
	}
	for _, row := range rows {
		window := windows[row.TargetID]
		if row.RelativePos < 0 {
			window.PreviousTurnIDs = append(window.PreviousTurnIDs, row.TurnID)
		} else if row.RelativePos > 0 {
			window.NextTurnIDs = append(window.NextTurnIDs, row.TurnID)
		}
		windows[row.TargetID] = window
	}
	return windows, nil
}

// LoadMemoryNodesByIDs loads one mixed batch of unified durable memory rows by numeric ids and returns them in ascending id order.
// LoadMemoryNodesByIDs 用于按数字 id 批量读取统一长期记忆行，并按升序返回。
func (s *Store) LoadMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), memoryIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory nodes by ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// LoadMemoryContextEdgesByMemoryIDs loads the contextual evidence rows attached to one memory-id batch so query-time scoring can align candidate memories with the current situation.
// LoadMemoryContextEdgesByMemoryIDs 用于按 memory id 批量加载情境证据行，让查询期打分可以把候选记忆与当前场景对齐。
func (s *Store) LoadMemoryContextEdgesByMemoryIDs(ctx context.Context, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryContextEdge{}, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryContextEdgeRow](s, ctx, fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, memoryIDPlaceholders), memoryIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory context edges by memory ids: %w", err)
	}
	edges := make([]logicdomain.MemoryContextEdge, 0, len(rows))
	for _, row := range rows {
		edges = append(edges, row.toDomain())
	}
	return edges, nil
}

// LoadMemoryNodesByVectorIDs loads unified durable memory rows by vector ids so vector search hits can be enriched with relational refs.
// LoadMemoryNodesByVectorIDs 用于按 vector id 读取统一长期记忆行，让向量召回结果补全为关系层 ref。
func (s *Store) LoadMemoryNodesByVectorIDs(ctx context.Context, vectorIDs []string) ([]logicdomain.MemoryNodeRecord, error) {
	vectorIDs = storageutil.NormalizeStringList(vectorIDs)
	if len(vectorIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	vectorIDParams := make([]any, 0, len(vectorIDs))
	for _, vectorID := range vectorIDs {
		vectorIDParams = append(vectorIDParams, vectorID)
	}
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE vector_id IN (%s)
  AND %s
ORDER BY created_timestamp ASC, id ASC
`, sqlitePlaceholders(len(vectorIDs)), buildActiveUnexpiredMemoryCondition("", nowMs)), vectorIDParams...)
	if err != nil {
		return nil, fmt.Errorf("query memory nodes by vector ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// SearchLexicalMemory runs one SQLite FTS recall over durable memory text and returns ranked materialized memory rows for app-layer RRF fusion.
// SearchLexicalMemory 用于在长期记忆文本上执行一次 SQLite FTS 召回，并返回应用层 RRF 融合所需的已排序物化记忆行。
func (s *Store) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	if strings.TrimSpace(query) == "" || topK <= 0 {
		return []logicdomain.MemoryLexicalHit{}, nil
	}
	if topK > memoryLexicalResultLimit {
		topK = memoryLexicalResultLimit
	}
	if s == nil || s.database == nil {
		return nil, fmt.Errorf("sqlite lexical search requires one local ffi database")
	}
	result, err := s.database.SearchFts(s.ftsIndexName, s.tokenizerMode, strings.TrimSpace(query), uint32(memoryLexicalSearchCandidateLimit(topK)), 0)
	if err != nil {
		return nil, fmt.Errorf("search lexical memory: %w", err)
	}
	candidates := make([]sqliteLexicalCandidate, 0, len(result.Hits))
	candidateMemoryIDs := make([]uint64, 0, len(result.Hits))
	for _, hit := range result.Hits {
		memoryID, ok := storageutil.ParsePositiveUint64(hit.ID)
		if !ok || memoryID == 0 {
			continue
		}
		candidates = append(candidates, sqliteLexicalCandidate{MemoryID: memoryID, Score: hit.Score})
		candidateMemoryIDs = append(candidateMemoryIDs, memoryID)
	}
	activeRowsByID, err := s.loadActiveMemoryNodesByIDs(ctx, candidateMemoryIDs)
	if err != nil {
		return nil, fmt.Errorf("load lexical memory nodes: %w", err)
	}

	filtered := make([]logicdomain.MemoryLexicalHit, 0, len(candidates))
	for _, candidate := range candidates {
		record, found := activeRowsByID[candidate.MemoryID]
		if !found || !matchesLexicalMemoryFilter(record, filter) {
			continue
		}
		filtered = append(filtered, logicdomain.MemoryLexicalHit{
			MemoryID: candidate.MemoryID,
			Record:   record,
			Score:    candidate.Score,
		})
		if len(filtered) >= topK {
			break
		}
	}
	return filtered, nil
}

// sqliteLexicalCandidate stores one parsed FTS hit before relational active-row validation keeps or drops it.
// sqliteLexicalCandidate 用于保存一条已解析的 FTS 命中，等待关系侧 active 行校验决定保留或丢弃。
type sqliteLexicalCandidate struct {
	MemoryID uint64
	Score    float64
}

// memoryLexicalSearchCandidateLimit expands the pre-filter FTS window while preserving the final caller-visible topK cap.
// memoryLexicalSearchCandidateLimit 用于扩大过滤前 FTS 候选窗口，同时保持调用方可见的最终 topK 上限不变。
func memoryLexicalSearchCandidateLimit(topK int) int {
	if topK <= 0 {
		return 0
	}
	limit := topK * 4
	if limit < topK {
		return topK
	}
	if limit > memoryLexicalCandidateLimit {
		return memoryLexicalCandidateLimit
	}
	return limit
}

// FindRecentActiveMemoryByDedupe finds one recent active direct-write memory row inside the same resolved session scope and soft-idempotency window.
// FindRecentActiveMemoryByDedupe 用于在同一已解析 session 范围和软幂等窗口内查找最近的 active 主动写记忆行。
func (s *Store) FindRecentActiveMemoryByDedupe(ctx context.Context, session logicdomain.SessionRef, sourceKind, scopeLevel int, dedupeHash string, notBefore time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(dedupeHash) == "" {
		return logicdomain.MemoryNodeRecord{}, false, logicdomain.ValidationError{Field: "dedupe_hash", Message: "is required"}
	}
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE origin_session_id = ?
  AND project_id = ?
  AND user_id = ?
  AND source_kind = ?
  AND scope_level = ?
  AND dedupe_hash = ?
  AND memory_status = ?
  AND (expires_timestamp <= 0 OR expires_timestamp > ?)
  AND created_timestamp >= ?
ORDER BY created_timestamp DESC, id DESC
LIMIT 1
`, session.SessionID, session.ProjectID, session.UserID, sourceKind, scopeLevel, strings.TrimSpace(dedupeHash), logicdomain.MemoryStatusActive, time.Now().UTC().UnixMilli(), notBefore.UTC().UnixMilli())
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, false, fmt.Errorf("query memory dedupe row: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.MemoryNodeRecord{}, false, nil
	}
	return rows[0].toMemoryNodeRecord(), true, nil
}

// CreateDirectMemoryNode inserts one unified direct-write memory row after the vector row has already been persisted successfully.
// CreateDirectMemoryNode 用于在向量行已经成功持久化后，插入一条统一的主动写记忆行。
func (s *Store) CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_memory_nodes")
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("allocate direct memory node id: %w", err)
	}
	now := time.Now().UTC()
	record = normalizeDirectMemoryNodeRecord(session, record, nextID, now)
	if err := s.executeDirectMemoryInsert(ctx, "create direct memory node", record); err != nil {
		return logicdomain.MemoryNodeRecord{}, err
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, []logicdomain.MemoryNodeRecord{record}, nil); err != nil {
		return logicdomain.MemoryNodeRecord{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "create direct memory node", fmt.Errorf("sync memory fts after direct insert: %w", err))
	}
	return record, nil
}

// ApplyDirectMemoryWrite atomically inserts one direct-write memory row and supersedes any replaced active rows so the direct-write path keeps the same replacement semantics as post-action.
// ApplyDirectMemoryWrite 用于原子写入一条主动记忆，并同时 supersede 被其替代的活跃旧行，让主动写路径与 post-action 保持同一套替代语义。
func (s *Store) ApplyDirectMemoryWrite(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	if session.SessionID == 0 {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_memory_nodes")
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("allocate direct memory node id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	record = normalizeDirectMemoryNodeRecord(session, record, nextID, now)
	supersededMemoryIDs = storageutil.NormalizeUint64List(supersededMemoryIDs)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("load superseded direct-write vector ids: %w", err)
	}

	// Execute the insert before the old-row status flip so later failures are reported as partial relational writes instead of clean failures.
	// 先执行插入再切换旧行状态，让后续失败按关系侧部分写入上报，而不是被误认为干净失败。
	if err := s.executeDirectMemoryInsert(ctx, "apply direct memory write", record); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, err
	}
	if len(supersededMemoryIDs) > 0 {
		if supersedeStatement, ok := parameterizedMemoryNodesSupersedeStatement(supersededMemoryIDs, nowMs); ok {
			supersedeResult, supersedeErr := s.execResult(ctx, supersedeStatement.SQL, supersedeStatement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileMemoryNodesSuperseded(ctx, supersededMemoryIDs, nowMs)
					if reconcileErr != nil {
						return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply direct memory write",
							Message:              fmt.Sprintf("supersede direct memory nodes: %v; reconcile failed: %v", supersedeErr, reconcileErr),
							FreshVectorReference: true,
						}
					}
					if !recovered {
						return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply direct memory write",
							Message:              fmt.Sprintf("supersede direct memory nodes: %v", supersedeErr),
							FreshVectorReference: true,
						}
					}
					// Continue to FTS deletion only after the direct-write replacement rows prove this supersede reached SQLite.
					// 只有在主动写替代行证明本次 supersede 已进入 SQLite 后，才继续执行 FTS 删除。
				} else {
					return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("supersede direct memory nodes: %w", supersedeErr))
				}
			} else if supersedeResult.RowsChanged != int64(len(supersededMemoryIDs)) {
				rowDriftErr := fmt.Errorf("affected %d rows, want %d", supersedeResult.RowsChanged, len(supersededMemoryIDs))
				return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("supersede direct memory nodes: %w", rowDriftErr))
			}
		}
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, []logicdomain.MemoryNodeRecord{record}, supersededMemoryIDs); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("sync memory fts after direct write: %w", err))
	}
	return logicdomain.DirectMemoryWriteApplyResult{
		InsertedMemoryNode:  record,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// DeleteMemoryNodes marks active memory rows as deleted inside the provided hierarchy filter while leaving their source turn/detail rows untouched.
// DeleteMemoryNodes 用于在给定层级过滤范围内把 active 记忆行标记为 deleted，同时保留其来源 turn/detail 行不变。
func (s *Store) DeleteMemoryNodes(ctx context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return logicdomain.MemoryDeleteResult{}, nil
	}
	if s == nil || s.database == nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("sqlite memory delete requires one local ffi database")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Load the candidate rows under the write lock so the subsequent status flip uses the same active-row snapshot and cannot race with lifecycle writes.
	// 在写锁内加载候选行，让后续状态切换使用同一份 active 行快照，避免与生命周期写入发生竞态。
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	queryParams := append([]any{logicdomain.MemoryStatusActive}, memoryIDParams...)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE memory_status = ?
  AND id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), queryParams...)
	if err != nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("query sqlite memory nodes for delete: %w", err)
	}

	eligible := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		if matchesLexicalMemoryFilter(record, filter) {
			eligible[record.ID] = record
		}
	}
	deletedMemoryIDs := make([]uint64, 0, len(eligible))
	notFoundMemoryIDs := make([]uint64, 0, len(memoryIDs))
	deletedVectorIDs := make([]string, 0, len(eligible))
	for _, memoryID := range memoryIDs {
		record, ok := eligible[memoryID]
		if !ok {
			notFoundMemoryIDs = append(notFoundMemoryIDs, memoryID)
			continue
		}
		deletedMemoryIDs = append(deletedMemoryIDs, memoryID)
		if strings.TrimSpace(record.VectorID) != "" {
			deletedVectorIDs = append(deletedVectorIDs, strings.TrimSpace(record.VectorID))
		}
	}
	if len(deletedMemoryIDs) == 0 {
		return logicdomain.MemoryDeleteResult{
			NotFoundMemoryIDs: notFoundMemoryIDs,
		}, nil
	}

	// Mark only the scoped active rows as deleted so future recall/detail filters skip them while source turns remain available for audit.
	// 只把范围内的 active 行标记为 deleted，让后续召回/详情过滤跳过这些记忆，同时保留来源轮次供审计查看。
	deletedAt = chooseNonZeroTime(deletedAt, time.Now().UTC()).UTC()
	deletedAtMs := deletedAt.UnixMilli()
	deletedMemoryIDPlaceholders := sqlitePlaceholders(len(deletedMemoryIDs))
	deletedMemoryIDParams := sqliteUint64Params(deletedMemoryIDs)
	updateParams := append([]any{
		logicdomain.MemoryStatusDeleted,
		strings.TrimSpace(reason),
		deletedAtMs,
		logicdomain.MemoryStatusActive,
	}, deletedMemoryIDParams...)
	updateResult, err := s.execResult(ctx, fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET memory_status = ?,
    status_reason = ?,
    updated_timestamp = ?
WHERE memory_status = ?
  AND id IN (%s)
`, deletedMemoryIDPlaceholders), updateParams...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileMemoryNodesDeleted(ctx, deletedMemoryIDs, reason, deletedAtMs)
			if reconcileErr != nil {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   fmt.Sprintf("delete sqlite memory nodes: %v; reconcile failed: %v", err, reconcileErr),
				}
			}
			if !recovered {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   fmt.Sprintf("delete sqlite memory nodes: %v", err),
				}
			}
			// Continue only after every scoped memory row proves it has reached the deleted state, so vector cleanup coordinates are safe to expose.
			// 只有在范围内的每条 memory 行都证明已经进入 deleted 状态后，才继续暴露 vector 清理坐标。
		} else {
			return logicdomain.MemoryDeleteResult{}, fmt.Errorf("delete sqlite memory nodes: %w", err)
		}
	} else {
		// Do not hand vector ids back to the use case unless every loaded active row was actually marked deleted.
		// 只有所有已加载的 active 行都确实标记为 deleted 后，才把 vector id 返回给用例层清理。
		expectedRowsChanged := int64(len(deletedMemoryIDs))
		if updateResult.RowsChanged != expectedRowsChanged {
			err := fmt.Errorf("delete sqlite memory nodes affected %d rows, want %d", updateResult.RowsChanged, expectedRowsChanged)
			if updateResult.RowsChanged > 0 {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   err.Error(),
				}
			}
			return logicdomain.MemoryDeleteResult{}, err
		}
	}
	// Build the sidecar cleanup coordinates only after the relational status flip is fully verified, so post-delete index failures can still be compensated safely.
	// 只有在关系状态切换已完整验收后才构造旁路清理坐标，让删除后的索引失败仍可被安全补偿。
	result := logicdomain.MemoryDeleteResult{
		DeletedMemoryIDs:  deletedMemoryIDs,
		NotFoundMemoryIDs: notFoundMemoryIDs,
		DeletedVectorIDs:  storageutil.NormalizeStringList(deletedVectorIDs),
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, nil, deletedMemoryIDs); err != nil {
		return result, sqlitePartialMutationError(true, "delete memory nodes", fmt.Errorf("sync memory fts after manual delete: %w", err))
	}
	return result, nil
}

// LoadRecentDirectMemoryWrites returns the direct AI-written memory rows created inside one exclusion window so the turn analyzer can avoid duplicate extraction.
// LoadRecentDirectMemoryWrites 用于返回某个排斥窗口内新建的 AI 主动写记忆行，让 turn analyzer 避免重复提炼。
func (s *Store) LoadRecentDirectMemoryWrites(ctx context.Context, session logicdomain.SessionRef, observedAfter, observedBefore time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedBefore.IsZero() {
		return []logicdomain.TurnAnalysisDirectWrite{}, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE origin_session_id = ?
  AND source_kind = ?
  AND memory_status = ?
  AND (expires_timestamp <= 0 OR expires_timestamp > ?)
  AND created_timestamp > ?
  AND created_timestamp <= ?
ORDER BY created_timestamp ASC, id ASC
`, session.SessionID, logicdomain.MemorySourceKindGRPCAIWrite, logicdomain.MemoryStatusActive, nowMs, observedAfter.UTC().UnixMilli(), observedBefore.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query recent direct memory writes: %w", err)
	}
	items := make([]logicdomain.TurnAnalysisDirectWrite, 0, len(rows))
	for _, row := range rows {
		items = append(items, logicdomain.TurnAnalysisDirectWrite{
			MemoryID:         row.ID,
			ScopeLevel:       logicdomain.MemoryScopeLevelLabel(row.ScopeLevel),
			Abstract:         strings.TrimSpace(row.Abstract),
			Details:          strings.TrimSpace(row.Details),
			CreatedTimestamp: row.CreatedTimestamp,
		})
	}
	return items, nil
}

// ApplyMemoryAdoption increments lifecycle counters for the memory rows selected by pre-check and promotes hot session facts when they prove useful across sessions.
// ApplyMemoryAdoption 用于为被 pre-check 采纳的记忆行递增生命周期计数，并在 session 级事实跨会话多次命中后将其升级。
func (s *Store) ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) ([]logicdomain.MemoryRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}
	if adoptedAt.IsZero() {
		adoptedAt = time.Now().UTC()
	} else {
		adoptedAt = adoptedAt.UTC()
	}

	// Serialize the full read-modify-write window so the lifecycle update never snapshots rows before another writer supersedes or reinforces them.
	// 串行化整个读-改-写窗口，避免生命周期回写在其他写入完成 supersede 或强化之前先拍下旧快照。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Load the current durable rows first so the lifecycle update can respect each row's current scope, counters, and expiry horizon.
	// 先加载当前长期行，确保生命周期更新能够尊重每条记录已有的作用域、计数器和过期时间。
	rows, err := s.LoadMemoryNodesByIDs(ctx, memoryIDs)
	if err != nil {
		return nil, fmt.Errorf("load memory adoption targets: %w", err)
	}
	if len(rows) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}

	// Pair each planned write with the lifecycle record that may be returned only after the durable row is confirmed updated.
	// 将每条计划写入与生命周期记录配对，只有确认长期行被更新后才允许返回该记录。
	pendingWrites := make([]struct {
		statement sqliteWriteStatement
		expected  logicdomain.MemoryNodeRecord
		record    logicdomain.MemoryRecord
	}, 0, len(rows))
	for _, row := range rows {
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, adoptedAt) {
			continue
		}
		evolved := evolveAdoptedMemoryRecord(session, row, adoptedAt)
		pendingWrites = append(pendingWrites, struct {
			statement sqliteWriteStatement
			expected  logicdomain.MemoryNodeRecord
			record    logicdomain.MemoryRecord
		}{
			statement: parameterizedMemoryAdoptionUpdateStatement(evolved),
			expected:  evolved,
			record:    memoryRecordFromNode(evolved),
		})
	}
	if len(pendingWrites) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}

	// Keep only confirmed lifecycle records for vector-side sync so callers never upsert speculative adoption state.
	// 只保留已确认写入的生命周期记录供向量侧同步使用，避免调用方 upsert 推测性的采纳状态。
	updatedRecords := make([]logicdomain.MemoryRecord, 0, len(pendingWrites))
	// Track whether an earlier SQLite update succeeded so later drift is reported as an uncertain partial mutation.
	// 记录前序 SQLite 更新是否已经成功，让后续漂移按部分突变结果不确定上报。
	mutated := false
	for index, pendingWrite := range pendingWrites {
		updateResult, updateErr := s.execResult(ctx, pendingWrite.statement.SQL, pendingWrite.statement.Params...)
		if updateErr != nil {
			if isSQLiteOutcomeUncertainError(updateErr) {
				recovered, reconcileErr := s.reconcileMemoryAdoptionUpdate(ctx, pendingWrite.expected)
				if reconcileErr != nil {
					return nil, logicdomain.OutcomeUncertainError{
						Operation: "apply memory adoption",
						Message:   fmt.Sprintf("apply memory adoption statement %d: %v; reconcile failed: %v", index+1, updateErr, reconcileErr),
					}
				}
				if !recovered {
					return nil, logicdomain.OutcomeUncertainError{
						Operation: "apply memory adoption",
						Message:   fmt.Sprintf("apply memory adoption statement %d: %v", index+1, updateErr),
					}
				}
				// Continue only after the durable row proves this adoption update reached the exact lifecycle target.
				// 只有在长期行证明本次采纳更新已经达到精确生命周期目标后，才继续处理后续记录。
			} else {
				return nil, sqlitePartialMutationError(mutated, "apply memory adoption", fmt.Errorf("apply memory adoption statement %d: %w", index+1, updateErr))
			}
		} else if updateResult.RowsChanged != 1 {
			rowDriftErr := fmt.Errorf("update adopted sqlite memory node affected %d rows, want 1", updateResult.RowsChanged)
			return nil, sqlitePartialMutationError(mutated, "apply memory adoption", fmt.Errorf("apply memory adoption statement %d: %w", index+1, rowDriftErr))
		}
		mutated = true
		updatedRecords = append(updatedRecords, pendingWrite.record)
	}
	return updatedRecords, nil
}

// ListIdlePendingSessions returns sessions whose latest conversation activity is older than the idle timeout while still carrying pending turn rows.
// ListIdlePendingSessions 用于返回“最后会话时间已超过空闲阈值且仍存在待处理 turn”的 session 列表。
func (s *Store) ListIdlePendingSessions(ctx context.Context, idleTimeout time.Duration, limit int) ([]logicdomain.SessionRef, error) {
	if idleTimeout <= 0 {
		return []logicdomain.SessionRef{}, nil
	}
	if limit <= 0 {
		limit = 128
	}
	cutoffMs := time.Now().UTC().Add(-idleTimeout).UnixMilli()
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE updated_timestamp <= ?
  AND EXISTS (
    SELECT 1
    FROM vmm_turn_records tr
    WHERE tr.session_id = vmm_sessions.id AND tr.extracted_status = ?
  )
ORDER BY updated_timestamp ASC, id ASC
LIMIT ?
`, cutoffMs, logicdomain.TurnExtractedStatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query idle pending sessions: %w", err)
	}
	sessions := make([]logicdomain.SessionRef, 0, len(rows))
	for _, row := range rows {
		record := row.toDomain()
		sessions = append(sessions, logicdomain.SessionRef{
			SessionID:              record.ID,
			SessionKey:             record.SessionKey,
			UserID:                 record.UserID,
			TeamID:                 record.TeamID,
			SpaceID:                record.SpaceID,
			ProjectID:              record.ProjectID,
			TurnCount:              record.TurnCount,
			LastSummarizedID:       record.LastSummarizedID,
			LastCompactedTurnID:    record.LastCompactedTurnID,
			SummarizeContent:       record.SummarizeContent,
			SummarizeBudget:        record.SummarizeBudget,
			LastExtractObservedAt:  record.LastExtractObservedAt,
			LastExtractCompletedAt: record.LastExtractCompletedAt,
			LastCompactedAt:        record.LastCompactedAt,
			CreatedAt:              record.CreatedAt,
			UpdatedAt:              record.UpdatedAt,
		})
	}
	return sessions, nil
}

// AdvanceSessionExtractWindow stores the latest direct-memory observation window after one immediate turn analysis succeeds.
// AdvanceSessionExtractWindow 用于在一次即时 turn 分析成功后，记录最新的主动记忆观察窗口。
func (s *Store) AdvanceSessionExtractWindow(ctx context.Context, sessionID uint64, observedAt, completedAt time.Time) error {
	if sessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedAt.IsZero() && completedAt.IsZero() {
		return nil
	}
	observedMs := int64(0)
	completedMs := int64(0)
	if !observedAt.IsZero() {
		observedMs = observedAt.UTC().UnixMilli()
	}
	if !completedAt.IsZero() {
		completedMs = completedAt.UTC().UnixMilli()
	}
	updatedMs := completedMs
	if observedMs > updatedMs {
		updatedMs = observedMs
	}

	// Serialize session checkpoint writes with turn appends because both paths move the session activity timestamp that drives idle-session queue recovery.
	// 将 session 检查点写入与 turn 追加串行化，因为两条路径都会推进用于空闲 session 队列恢复的活动时间戳。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	statement := parameterizedSessionExtractWindowUpdateStatement(sessionID, observedMs, completedMs, updatedMs)
	result, err := s.execResult(ctx, statement.SQL, statement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredSession, reconcileErr := s.loadSessionByID(ctx, sessionID)
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation: "advance session extract window",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if sameAdvancedSessionExtractWindow(recoveredSession, sessionID, observedMs, completedMs, updatedMs) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation: "advance session extract window",
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("advance session extract window: %w", err)
	}
	if err := sqliteAutocommitRowsChangedDriftError("advance session extract window", "update session extract window", result.RowsChanged, 1); err != nil {
		return fmt.Errorf("advance session extract window: %w", err)
	}
	return nil
}

// loadActiveMemoryVectorIDs loads the vector ids of active unified memory rows by memory id so callers can clean those vector rows after a supersede update commits.
// loadActiveMemoryVectorIDs 用于按记忆 id 读取 active 统一记忆行的 vector_id，供调用方在 supersede 提交后清理对应向量行。
func (s *Store) loadActiveMemoryVectorIDs(ctx context.Context, memoryIDs []uint64) ([]string, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	queryParams := append([]any{logicdomain.MemoryStatusActive}, memoryIDParams...)
	rows, err := queryRows[obsoleteVectorRow](s, ctx, fmt.Sprintf(`
SELECT vector_id
FROM vmm_memory_nodes
WHERE memory_status = ? AND id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), queryParams...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.VectorID) == "" {
			continue
		}
		ids = append(ids, strings.TrimSpace(row.VectorID))
	}
	return ids, nil
}

// loadUserByID resolves one durable user row by numeric id and converts absence into a stable domain not-found error.
// loadUserByID 用于按数字 ID 解析长期用户记录，并把不存在情况转换成稳定的领域 not-found 错误。
func (s *Store) loadUserByID(ctx context.Context, userID uint64) (logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE id = ?
LIMIT 1
`, userID)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("query user: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user_id %d does not exist", userID)}
	}
	return rows[0].toDomain(), nil
}

// loadProjectByID resolves one project together with its parent team and space names.
// loadProjectByID 用于解析单个项目，以及它对应的 team 和 space 名称。
func (s *Store) loadProjectByID(ctx context.Context, projectID uint64) (logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.id = ?
LIMIT 1
`, projectID)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("query project: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project_id %d does not exist", projectID)}
	}
	return rows[0].toDomain(), nil
}

// loadSessionByID resolves one durable session row by numeric id so append writes can derive counters from the current locked state.
// loadSessionByID 用于按数字 ID 解析一条长期 session 行，让追加写入可以从当前锁内状态推导计数。
func (s *Store) loadSessionByID(ctx context.Context, sessionID uint64) (logicdomain.SessionRecord, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count,
       last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE id = ?
LIMIT 1
`, sessionID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("query session: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionRecord{}, logicdomain.NotFoundError{Resource: "session", Message: fmt.Sprintf("session_id %d does not exist", sessionID)}
	}
	return rows[0].toDomain(), nil
}

// lookupSessionByProjectAndKey resolves one project-scoped session key and reports absence without treating it as an error.
// lookupSessionByProjectAndKey 用于解析一个项目作用域内的 session key，并把不存在作为布尔结果返回。
func (s *Store) lookupSessionByProjectAndKey(ctx context.Context, projectID uint64, sessionKey string) (logicdomain.SessionRecord, bool, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count,
       last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE project_id = ? AND session_key = ?
LIMIT 1
`, projectID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, false, fmt.Errorf("query session: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// ensureSession loads one existing session inside the target project or creates it under the locked, freshly verified user/project hierarchy.
// ensureSession 用于在目标项目内读取已有 session，或在写锁内重新校验 user/project 后创建缺失的 session。
func (s *Store) ensureSession(ctx context.Context, sessionKey string, user logicdomain.UserRecord, project logicdomain.ProjectRecord) (logicdomain.SessionRecord, error) {
	session, exists, err := s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, err
	}
	if exists {
		// Reuse the project-scoped session row directly even when the upstream plugin has switched user ids during debugging.
		// 在调试阶段，即使上游插件手动切换了 user_id，也直接复用同一 project 下的 session 行。
		return session, nil
	}

	// Serialize the missing-session create window so duplicate requests converge before allocating a fresh numeric id.
	// 串行化缺失 session 的创建窗口，让重复请求在分配新数字 ID 前先收敛到已有行。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	session, exists, err = s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, err
	}
	if exists {
		return session, nil
	}

	// Revalidate the resolved user and project after the write lock is acquired so a delete/recreate race cannot leave an orphaned session.
	// 获取写锁后重新校验已解析的 user 与 project，避免删除/重建竞争留下孤儿 session。
	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("reload user before session create: %w", err)
	}
	if !sameUserIdentity(currentUser, user) {
		return logicdomain.SessionRecord{}, logicdomain.ConflictError{
			Resource: "user",
			Message:  fmt.Sprintf("user changed before session create: resolved %s, current %s", user.Name, currentUser.Name),
		}
	}
	currentProject, err := s.loadProjectByID(ctx, project.ID)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("reload project before session create: %w", err)
	}
	if !sameProjectIdentity(currentProject, project) {
		return logicdomain.SessionRecord{}, logicdomain.ConflictError{
			Resource: "project",
			Message:  fmt.Sprintf("project changed before session create: resolved %s, current %s", project.Path(), currentProject.Path()),
		}
	}
	return s.insertSession(ctx, sessionKey, currentUser, currentProject, time.Now().UTC())
}

// ListProjects returns all projects together with their display path components, ordered for deterministic UX.
// ListProjects 用于返回全部项目及其展示路径组成部分，并按稳定顺序输出。
func (s *Store) ListProjects(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
ORDER BY t.name ASC, sp.name ASC, p.name ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]logicdomain.ProjectRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// ListProjectsForMaintenance reuses the normal SQLite project listing because SQLite workspace enumeration already runs under the same bounded gateway timeout used by maintenance commands.
// ListProjectsForMaintenance 用于让维护命令复用常规 SQLite 项目列表读取，因为 SQLite 的项目枚举本就运行在同一套有界网关超时之下。
func (s *Store) ListProjectsForMaintenance(ctx context.Context) ([]logicdomain.ProjectRecord, error) {
	return s.ListProjects(ctx)
}

// ResolveProjectRef resolves either a numeric project id or a canonical Team/Space/Project path.
// ResolveProjectRef 用于解析数字 project id，或标准的 Team/Space/Project 路径。
func (s *Store) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return logicdomain.ProjectRecord{}, logicdomain.ValidationError{Field: "project_ref", Message: "is required"}
	}
	if projectID, ok := storageutil.ParsePositiveUint64(projectRef); ok {
		return s.loadProjectByID(ctx, projectID)
	}
	teamName, spaceName, projectName, err := storageutil.ParseProjectPath(projectRef)
	if err != nil {
		return logicdomain.ProjectRecord{}, err
	}
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE t.name = ? AND sp.name = ? AND p.name = ?
LIMIT 1
`, teamName, spaceName, projectName)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("resolve project path: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, logicdomain.NotFoundError{Resource: "project", Message: fmt.Sprintf("project path %q does not exist", projectRef)}
	}
	return rows[0].toDomain(), nil
}

// ListProjectMemories returns unified durable memory rows under one project so migrations can rebuild vector rows directly from the main memory table.
// ListProjectMemories 用于返回某个项目下的统一长期记忆行，让迁移流程可以直接从主记忆表重建向量行。
func (s *Store) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	nowMs := time.Now().UTC().UnixMilli()
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE project_id = ? AND `+buildActiveUnexpiredMemoryCondition("", nowMs)+`
ORDER BY created_timestamp ASC, id ASC
`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project memories: %w", err)
	}
	out := make([]logicdomain.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		out = append(out, memoryRecordFromNode(record))
	}
	return out, nil
}

// memoryRecordFromNode converts one active durable SQLite memory row into the sidecar/vector-store record used by rebuild, migration, and lifecycle sync paths.
// memoryRecordFromNode 用于把一条 SQLite 长期记忆行转换成重建、迁移和生命周期同步路径使用的旁路向量记录。
func memoryRecordFromNode(record logicdomain.MemoryNodeRecord) logicdomain.MemoryRecord {
	filter := logicdomain.SearchFilter{
		UserID:    record.UserID,
		TeamID:    record.TeamID,
		SpaceID:   record.SpaceID,
		ProjectID: record.ProjectID,
	}
	if record.SourceKind == logicdomain.MemorySourceKindTurnExtract || record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
		filter.SessionID = record.OriginSessionID
	}
	metadata := map[string]string{
		"category":     strconv.Itoa(record.Category),
		"details":      record.Details,
		"source_kind":  logicdomain.MemorySourceKindLabel(record.SourceKind),
		"scope_level":  logicdomain.MemoryScopeLevelLabel(record.ScopeLevel),
		"priority":     strconv.Itoa(record.Priority),
		"memory_level": strconv.Itoa(record.MemoryLevel),
	}
	if record.SourceTurnID > 0 {
		metadata["turn_id"] = strconv.FormatUint(record.SourceTurnID, 10)
	}
	return logicdomain.MemoryRecord{
		ID:           record.VectorID,
		Text:         record.Abstract,
		Vector:       append([]float32(nil), record.Vector...),
		Filter:       filter,
		SourceTurnID: record.SourceTurnID,
		Status:       record.Status,
		ExpiresAt:    record.ExpiresAt,
		Metadata:     metadata,
		CreatedAt:    record.CreatedAt,
	}
}

// ListProjectMemoriesForMaintenance reuses the normal SQLite project-memory scan for maintenance commands because SQLite already executes the durable read under the same bounded gateway timeout.
// ListProjectMemoriesForMaintenance 用于让维护命令复用常规 SQLite 项目记忆扫描，因为 SQLite durable 读取本就走同一套有界网关超时。
func (s *Store) ListProjectMemoriesForMaintenance(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	return s.ListProjectMemories(ctx, projectID)
}

// ReplaceMemoryVectors rewrites the durable SQLite vector_json payload for one active-memory batch so split-mode rebuild tools can keep SQLite and LanceDB strictly synchronized after a model change.
// ReplaceMemoryVectors 用于重写一批长期记忆在 SQLite 中保存的 vector_json，让分离模式重建工具在模型切换后保持 SQLite 与 LanceDB 严格同步。
func (s *Store) ReplaceMemoryVectors(ctx context.Context, records []logicdomain.MemoryRecord) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(records) == 0 {
		return nil
	}

	// Keep the durable vector rewrite under the shared write lock so one maintenance rebuild cannot interleave with ordinary memory mutations.
	// 把 durable 向量重写放进共享写锁内，避免维护重建和普通记忆写入彼此交叉。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	items := make([][]any, 0, len(records))
	for idx, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].id", idx), Message: "is required"}
		}
		if len(record.Vector) == 0 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].vector", idx), Message: "must contain at least one dimension"}
		}
		items = append(items, []any{encodeFloat32Slice(record.Vector), vectorID})
	}
	result, err := s.execBatchResult(ctx, `
UPDATE vmm_memory_nodes
SET vector_json = ?
WHERE vector_id = ?
`, items)
	if err != nil {
		return sqliteMaintenanceBatchError("replace sqlite memory vectors", err)
	}
	// Require every selected durable row to be rewritten before the sidecar vector table can be refilled from the same batch.
	// 在用同一批数据回填旁路向量表之前，要求每个已选长期行都完成重写。
	if expectedRows := int64(len(items)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("replace sqlite memory vectors affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "replace sqlite memory vectors", err)
	}
	return nil
}

// ClearMemoryVectors clears one active durable batch's SQLite vector_json payload back to the empty baseline before split-mode maintenance rebuilds repopulate both SQLite and LanceDB from scratch.
// ClearMemoryVectors 用于把一批 active durable 记忆的 SQLite vector_json 清回空基线，再由 split 模式维护重建从零开始同时回填 SQLite 与 LanceDB。
func (s *Store) ClearMemoryVectors(ctx context.Context, vectorIDs []string) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(vectorIDs) == 0 {
		return nil
	}

	// Keep the destructive reset under the shared write lock so no foreground writer can observe or reintroduce stale vectors between the reset and the later rebuild batches.
	// 把破坏性的向量清空动作放进共享写锁内，避免前台写入在清空与后续重建批次之间观察到或重新引入陈旧向量。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	items := make([][]any, 0, len(vectorIDs))
	for idx, vectorID := range vectorIDs {
		cleanedVectorID := strings.TrimSpace(vectorID)
		if cleanedVectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("vector_ids[%d]", idx), Message: "is required"}
		}
		items = append(items, []any{cleanedVectorID})
	}
	result, err := s.execBatchResult(ctx, `
UPDATE vmm_memory_nodes
SET vector_json = '[]'
WHERE vector_id = ?
`, items)
	if err != nil {
		return sqliteMaintenanceBatchError("clear sqlite memory vectors", err)
	}
	// Require every durable row to be cleared so the reset phase cannot leave stale SQLite vectors behind the rebuilt sidecar table.
	// 要求每个长期行都被清空，避免 reset 阶段在旁路表重建后留下陈旧的 SQLite 向量。
	if expectedRows := int64(len(items)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("clear sqlite memory vectors affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "clear sqlite memory vectors", err)
	}
	return nil
}

// sqliteMaintenanceBatchError preserves ordinary batch failures while marking SQLite commit-boundary failures that may already have changed maintenance rows.
// sqliteMaintenanceBatchError 用于保留普通批处理失败，同时标记可能已经改变维护行的 SQLite 提交边界失败。
func sqliteMaintenanceBatchError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if classifiedErr := sqliteWriteCommitBoundaryError(operation, err); logicdomain.IsOutcomeUncertain(classifiedErr) {
		return classifiedErr
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// EnsureProjectPath resolves or creates a Team/Space/Project path according to the confirm flag rules required by the admin RPCs.
// EnsureProjectPath 用于按管理 RPC 约定的确认规则，解析或创建一个 Team/Space/Project 路径。
func (s *Store) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	teamName, spaceName, projectName, err := storageutil.ParseProjectPath(projectPath)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if existing, err := s.ResolveProjectRef(ctx, projectPath); err == nil {
		return logicdomain.ProjectMutationResult{
			Project: existing,
			Message: fmt.Sprintf("project %s already exists", existing.Path()),
			Exists:  true,
		}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.ProjectMutationResult{}, err
	}

	team, teamExists, err := s.lookupTeamByName(ctx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err := s.lookupSpaceByName(ctx, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      storageutil.ProjectCreateConfirmationMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	createdTeam := false
	createdSpace := false
	createdProject := false

	// Re-resolve the hierarchy under the write lock so the confirmation decision and inserts are based
	// on the same serialized SQLite window instead of stale pre-lock lookup results.
	// 在写锁内重新解析层级，让确认决策和插入基于同一个串行 SQLite 窗口，而不是锁前的陈旧查询结果。
	team, teamExists, err = s.lookupTeamByName(ctx, teamName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	space, spaceExists, err = s.lookupSpaceByName(ctx, team.ID, spaceName, teamExists)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
	}
	if !confirmCreate && (!teamExists || !spaceExists) {
		return logicdomain.ProjectMutationResult{
			Message:      storageutil.ProjectCreateConfirmationMessage(teamName, spaceName, projectName, teamExists, spaceExists),
			NeedsConfirm: true,
			MissingTeam:  !teamExists,
			MissingSpace: !spaceExists,
		}, nil
	}
	if !teamExists {
		team, err = s.insertTeam(ctx, teamName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdTeam = true
		space, spaceExists, err = s.lookupSpaceByName(ctx, team.ID, spaceName, true)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam, "ensure project path", err)
		}
	}
	if !spaceExists {
		space, err = s.insertSpace(ctx, team.ID, spaceName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam, "ensure project path", err)
		}
		createdSpace = true
	}
	project, projectExists, err := s.lookupProjectBySpaceAndName(ctx, team, space, projectName)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam || createdSpace, "ensure project path", err)
	}
	if projectExists {
		return logicdomain.ProjectMutationResult{
			Project: project,
			Message: fmt.Sprintf("project %s already exists", project.Path()),
			Exists:  true,
		}, nil
	}
	project, err = s.insertProject(ctx, team, space, projectName, now)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, sqlitePartialMutationError(createdTeam || createdSpace, "ensure project path", err)
	}
	createdProject = true
	return logicdomain.ProjectMutationResult{
		Project:        project,
		Message:        fmt.Sprintf("project %s created", project.Path()),
		CreatedTeam:    createdTeam,
		CreatedSpace:   createdSpace,
		CreatedProject: createdProject,
	}, nil
}

// DeleteProjectPath deletes one resolved project plus its sessions, turn records, and SQL-backed memories when confirmation is explicit.
// DeleteProjectPath 用于在确认删除时，删除一个已解析项目及其 sessions、turn 记录和 SQL 侧记忆。
func (s *Store) DeleteProjectPath(ctx context.Context, projectPath string, confirmDelete bool) (logicdomain.ProjectDeleteResult, error) {
	project, err := s.ResolveProjectRef(ctx, projectPath)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if !confirmDelete {
		return logicdomain.ProjectDeleteResult{
			Project:      project,
			Message:      fmt.Sprintf("confirm delete project %s", project.Path()),
			NeedsConfirm: true,
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Plan all row counts and hierarchy cascades under the same write lock so the reported numbers
	// match the concrete delete path seen by this runtime instance.
	// 在同一把写锁下规划删除计数和层级级联条件，确保当前运行时返回的统计与实际删除路径一致。
	deletePlan, err := s.planProjectDelete(ctx, project)
	if err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}

	// Remove all profile nodes that belong to the project itself, were derived from its turns,
	// or belong to one now-empty parent scope that will be deleted together with the project.
	// 删除项目自身、其 turn 派生、以及随空父级一并删除的 scope 所关联的画像节点。
	deleteSQL, deleteParams := buildProjectProfileNodesDeleteSQL(project.ID, project.SpaceID, project.TeamID, deletePlan.DeletedSpaces > 0, deletePlan.DeletedTeams > 0)
	// Track confirmed destructive steps so any later ordinary write failure cannot be reported as a clean retryable delete failure.
	// 跟踪已确认的破坏性步骤，避免后续普通写入失败被误报为可干净重试的删除失败。
	mutated := false
	runDeleteStep := func(operation string, expectedRows int, sql string, params ...any) error {
		if err := s.execProjectDeleteStep(ctx, operation, mutated, expectedRows, sql, params...); err != nil {
			return err
		}
		mutated = mutated || expectedRows > 0
		return nil
	}
	if err := runDeleteStep("delete project profile nodes", deletePlan.DeletedProfiles, deleteSQL, deleteParams...); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project memory nodes", deletePlan.DeletedMemories, `DELETE FROM vmm_memory_nodes WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project turn records", deletePlan.DeletedMessages, `DELETE FROM vmm_turn_records WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project sessions", deletePlan.DeletedSessions, `DELETE FROM vmm_sessions WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if err := runDeleteStep("delete project row", deletePlan.DeletedProjects, `DELETE FROM vmm_projects WHERE id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, err
	}
	if deletePlan.DeletedSpaces > 0 {
		if err := runDeleteStep("delete empty project space row", deletePlan.DeletedSpaces, `DELETE FROM vmm_spaces WHERE id = ?`, project.SpaceID); err != nil {
			return logicdomain.ProjectDeleteResult{}, err
		}
	}
	if deletePlan.DeletedTeams > 0 {
		if err := runDeleteStep("delete empty project team row", deletePlan.DeletedTeams, `DELETE FROM vmm_teams WHERE id = ?`, project.TeamID); err != nil {
			return logicdomain.ProjectDeleteResult{}, err
		}
	}
	return logicdomain.ProjectDeleteResult{
		Project:         project,
		Message:         fmt.Sprintf("project %s deleted", project.Path()),
		DeletedProjects: deletePlan.DeletedProjects,
		DeletedSpaces:   deletePlan.DeletedSpaces,
		DeletedTeams:    deletePlan.DeletedTeams,
		DeletedSessions: deletePlan.DeletedSessions,
		DeletedMessages: deletePlan.DeletedMessages,
		DeletedMemories: deletePlan.DeletedMemories,
		DeletedProfiles: deletePlan.DeletedProfiles,
	}, nil
}

// execProjectDeleteStep executes one planned project-delete write and classifies failures using both commit-boundary evidence and prior destructive progress.
// execProjectDeleteStep 用于执行一次已规划的项目删除写入，并结合提交边界证据与前序破坏性进度来分类失败。
func (s *Store) execProjectDeleteStep(ctx context.Context, operation string, mutated bool, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "delete project path", fmt.Errorf("%s: %w", operation, err))
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "delete project path",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// MigrateProjectPath moves SQL-backed sessions, turn records, memories, and project-bound profiles from one project scope onto another when explicitly confirmed.
// MigrateProjectPath 用于在显式确认后，把 SQL 侧 sessions、turn 记录、memories 和项目绑定画像从源项目范围迁移到目标项目范围。
func (s *Store) MigrateProjectPath(ctx context.Context, sourcePath, targetPath string, confirm bool) (logicdomain.ProjectMigrationResult, error) {
	source, err := s.ResolveProjectRef(ctx, sourcePath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err := s.ResolveProjectRef(ctx, targetPath)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	if source.ID == target.ID {
		return logicdomain.ProjectMigrationResult{}, logicdomain.ConflictError{Resource: "project", Message: "source and target project are the same"}
	}
	if !confirm {
		return logicdomain.ProjectMigrationResult{
			Source:       source,
			Target:       target,
			Message:      fmt.Sprintf("confirm migrate project %s -> %s", source.Path(), target.Path()),
			NeedsConfirm: true,
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-read both migration endpoints after acquiring the write lock so a stale path resolution cannot
	// migrate rows against a deleted or recreated project node.
	// 获取写锁后重新读取迁移两端，避免陈旧路径解析把数据迁移到已删除或重建的项目节点上。
	source, err = s.verifyProjectMigrationEndpoint(ctx, "source", source)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	target, err = s.verifyProjectMigrationEndpoint(ctx, "target", target)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}

	// Plan migration counts inside the write lock so the later affected-row checks validate the exact
	// source scope observed by this serialized SQLite mutation path.
	// 在写锁内规划迁移计数，让后续影响行数校验对齐当前串行 SQLite 写入路径实际观察到的源范围。
	migrationPlan, err := s.planProjectMigration(ctx, source.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()
	mutated := false
	if err := s.execProjectMigrationStep(ctx, "migrate project sessions", migrationPlan.MigratedSessions, `
UPDATE vmm_sessions
SET team_id = ?, space_id = ?, project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedSessions > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project turn records", migrationPlan.MigratedMessages, `
UPDATE vmm_turn_records
SET project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedMessages > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project memory nodes", migrationPlan.MigratedMemories, `
UPDATE vmm_memory_nodes
SET team_id = ?, space_id = ?, project_id = ?, updated_timestamp = ?
WHERE project_id = ?
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	mutated = mutated || migrationPlan.MigratedMemories > 0
	if err := s.execProjectMigrationStep(ctx, "migrate project profile nodes", migrationPlan.MigratedProfiles, `
UPDATE vmm_profile_nodes
SET bind_id = ?
WHERE profile_type = ? AND bind_id = ?
`, target.ID, logicdomain.ProfileTypeProject, source.ID); err != nil {
		return logicdomain.ProjectMigrationResult{}, sqlitePartialMutationError(mutated, "migrate project path", err)
	}
	return logicdomain.ProjectMigrationResult{
		Source:           source,
		Target:           target,
		Message:          fmt.Sprintf("migrated project %s -> %s", source.Path(), target.Path()),
		MigratedSessions: migrationPlan.MigratedSessions,
		MigratedMessages: migrationPlan.MigratedMessages,
		MigratedMemories: migrationPlan.MigratedMemories,
	}, nil
}

// execProjectMigrationStep executes one planned project-migration write and rejects affected-row drift before vector rebuild can start.
// execProjectMigrationStep 用于执行一次已规划的项目迁移写入，并在向量重建开始前拦截影响行数漂移。
func (s *Store) execProjectMigrationStep(ctx context.Context, operation string, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			return logicdomain.OutcomeUncertainError{
				Operation: "migrate project path",
				Message:   fmt.Sprintf("%s: %v", operation, err),
			}
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "migrate project path",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// verifyProjectMigrationEndpoint reloads one migration endpoint under the write lock and rejects stale source or target identity before any rows move.
// verifyProjectMigrationEndpoint 用于在写锁下重新读取迁移端点，并在任何行迁移前拒绝陈旧的源或目标身份。
func (s *Store) verifyProjectMigrationEndpoint(ctx context.Context, role string, resolved logicdomain.ProjectRecord) (logicdomain.ProjectRecord, error) {
	current, err := s.loadProjectByID(ctx, resolved.ID)
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("reload %s project before migration: %w", role, err)
	}
	if !sameProjectIdentity(current, resolved) {
		return logicdomain.ProjectRecord{}, logicdomain.ConflictError{
			Resource: "project",
			Message:  fmt.Sprintf("%s project changed before migration: resolved %s, current %s", role, resolved.Path(), current.Path()),
		}
	}
	return current, nil
}

// sameProjectIdentity compares the stable hierarchy identity fields that prove two project records refer to the same durable node.
// sameProjectIdentity 用于比较稳定的层级身份字段，确认两条项目记录指向同一个长期节点。
func sameProjectIdentity(left, right logicdomain.ProjectRecord) bool {
	return left.ID == right.ID &&
		left.TeamID == right.TeamID &&
		left.SpaceID == right.SpaceID &&
		left.TeamName == right.TeamName &&
		left.SpaceName == right.SpaceName &&
		left.Name == right.Name &&
		left.CreatedAt.Equal(right.CreatedAt)
}

// sameUserIdentity compares the stable user identity fields that prove one numeric user id was not deleted and reused.
// sameUserIdentity 用于比较稳定的用户身份字段，确认一个数字 user id 没有被删除后复用。
func sameUserIdentity(left, right logicdomain.UserRecord) bool {
	return left.ID == right.ID &&
		left.Name == right.Name &&
		left.CreatedAt.Equal(right.CreatedAt)
}

// ResolveUserRef resolves either a numeric user id or a unique user name.
// ResolveUserRef 用于解析数字 user id，或唯一用户名。
func (s *Store) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	userRef = strings.TrimSpace(userRef)
	if userRef == "" {
		return logicdomain.UserRecord{}, logicdomain.ValidationError{Field: "user_ref", Message: "is required"}
	}
	if userID, ok := storageutil.ParsePositiveUint64(userRef); ok {
		return s.loadUserByID(ctx, userID)
	}
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE name = ?
LIMIT 1
`, userRef)
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("resolve user by name: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.UserRecord{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist", userRef)}
	}
	return rows[0].toDomain(), nil
}

// EnsureUserName resolves one user by name or creates it when confirmation is explicit.
// EnsureUserName 用于按名称解析用户，或在确认创建时补建该用户。
func (s *Store) EnsureUserName(ctx context.Context, userName string, confirmCreate bool) (logicdomain.UserResolveResult, error) {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return logicdomain.UserResolveResult{}, logicdomain.ValidationError{Field: "user_name", Message: "is required"}
	}
	if user, err := s.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s already exists", user.Name), Exists: true}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	if !confirmCreate {
		return logicdomain.UserResolveResult{}, logicdomain.NotFoundError{Resource: "user", Message: fmt.Sprintf("user %q does not exist; use confirm_create=1 to create it", userName)}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-read under the write lock so duplicate in-process create requests converge to the existing row
	// before allocating a new numeric id.
	// 在写锁下重新读取，让进程内重复创建请求在分配新数字 ID 前收敛到已存在行。
	if user, err := s.ResolveUserRef(ctx, userName); err == nil {
		return logicdomain.UserResolveResult{User: user, Message: fmt.Sprintf("user %s already exists", user.Name), Exists: true}, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.UserResolveResult{}, err
	}
	now := time.Now().UTC()
	user, err := s.insertUser(ctx, userName, now)
	if err != nil {
		return logicdomain.UserResolveResult{}, err
	}
	return logicdomain.UserResolveResult{
		User:    user,
		Message: fmt.Sprintf("user %s created", userName),
		Created: true,
	}, nil
}

// ListUsers returns all durable users ordered by id for deterministic management output.
// ListUsers 用于按 id 稳定输出所有长期用户。
func (s *Store) ListUsers(ctx context.Context) ([]logicdomain.UserRecord, error) {
	rows, err := queryRows[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
ORDER BY id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	out := make([]logicdomain.UserRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// DeleteUserRef executes the protected two-phase user deletion flow and removes the user plus all SQL-side dependent data.
// DeleteUserRef 用于执行受保护的双阶段用户删除流程，并删除该用户及其所有 SQL 侧关联数据。
func (s *Store) DeleteUserRef(ctx context.Context, userRef, confirmationCode string) (logicdomain.UserDeleteResult, error) {
	user, err := s.ResolveUserRef(ctx, userRef)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}

	// Keep the first-phase confirmation idempotent so duplicate admin clicks reuse one stable confirmation code
	// instead of issuing concurrent row updates that can conflict inside the SQLite gateway.
	// 把删除确认第一阶段做成幂等流程，让重复的管理端点击复用同一确认码，
	// 避免在 SQLite 网关里对同一行发起并发更新而相互冲突。
	confirmationCode = strings.TrimSpace(confirmationCode)
	if confirmationCode == "" {
		return s.ensureUserDeleteConfirmation(ctx, user)
	}

	// Reload the latest durable code before destructive work so stale callers do not rotate the confirmation token.
	// 在真正删除前重新加载最新确认码，避免陈旧调用方把确认令牌无谓轮换。
	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if strings.TrimSpace(currentUser.DeleteConfirmCode) == "" {
		return s.ensureUserDeleteConfirmation(ctx, currentUser)
	}
	if confirmationCode != strings.TrimSpace(currentUser.DeleteConfirmCode) {
		return logicdomain.UserDeleteResult{
			User:                 currentUser,
			Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
			RequiresConfirmation: true,
			ConfirmationCode:     strings.TrimSpace(currentUser.DeleteConfirmCode),
		}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	deletePlan, err := s.planUserDelete(ctx, currentUser.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()

	// mutated records whether a prior planned delete step has already changed durable rows in this autocommit sequence.
	// mutated 用于记录此前规划删除步骤是否已经在本次自动提交序列中改变长期行。
	mutated := false
	// runDeleteStep executes one planned write and updates mutation state only after the affected-row check succeeds.
	// runDeleteStep 用于执行一条规划写入，并且只在影响行数校验成功后更新写入状态。
	runDeleteStep := func(operation string, expectedRows int, sql string, params ...any) error {
		if err := s.execUserDeleteStep(ctx, operation, mutated, expectedRows, sql, params...); err != nil {
			return err
		}
		mutated = mutated || expectedRows > 0
		return nil
	}

	if err := runDeleteStep("delete user profile nodes by bind", deletePlan.DeletedProfiles, `DELETE FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeUser, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	// Detach surviving shared-scope profile facts from the user's historical turns before those turn rows disappear.
	// These nodes must keep living under project/team/space scope, but they can no longer point at deleted turn ids.
	// 在删除用户历史 turn 前，先把仍应保留的共享范围画像节点与原 turn 脱钩。
	// 这些节点继续归属于 project/team/space，但不能再指向即将被删除的 turn id。
	detachSQL, detachParams := buildUserSharedProfileDetachUpdateSQL(currentUser.ID, nowMs)
	if err := runDeleteStep("detach surviving shared profile nodes from deleted user turns", deletePlan.DetachedProfiles, detachSQL, detachParams...); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user memory nodes", deletePlan.DeletedMemories, `DELETE FROM vmm_memory_nodes WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user turn records", deletePlan.DeletedMessages, `DELETE FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user sessions", deletePlan.DeletedSessions, `DELETE FROM vmm_sessions WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	if err := runDeleteStep("delete user row", deletePlan.DeletedUsers, `DELETE FROM vmm_users WHERE id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	return logicdomain.UserDeleteResult{
		User:            currentUser,
		Message:         fmt.Sprintf("user %s deleted", currentUser.Name),
		DeletedUsers:    deletePlan.DeletedUsers,
		DeletedSessions: deletePlan.DeletedSessions,
		DeletedMessages: deletePlan.DeletedMessages,
		DeletedMemories: deletePlan.DeletedMemories,
		DeletedProfiles: deletePlan.DeletedProfiles,
	}, nil
}

// execUserDeleteStep executes one planned user-delete write and classifies execution errors by confirmed prior mutation before rejecting affected-row drift.
// execUserDeleteStep 用于执行一次已规划的用户删除写入，并先按已确认的前序写入分类执行错误，再拦截影响行数漂移。
func (s *Store) execUserDeleteStep(ctx context.Context, operation string, mutated bool, expectedRows int, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return sqliteWriteCommitOrPartialMutationError(mutated, "delete user ref", fmt.Errorf("%s: %w", operation, err))
	}
	if resp.RowsChanged != int64(expectedRows) {
		return logicdomain.OutcomeUncertainError{
			Operation: "delete user ref",
			Message:   fmt.Sprintf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows),
		}
	}
	return nil
}

// ensureUserDeleteConfirmation makes the first delete step idempotent by generating one confirmation code only when the durable row still has none.
// ensureUserDeleteConfirmation 用于让删除第一步保持幂等：只有当长期行里还没有确认码时才生成新的确认码。
func (s *Store) ensureUserDeleteConfirmation(ctx context.Context, user logicdomain.UserRecord) (logicdomain.UserDeleteResult, error) {
	if user.ID == 0 {
		return logicdomain.UserDeleteResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}

	// Serialize confirmation-code generation so duplicate delete requests cannot race on the same user row.
	// 串行化确认码生成，避免重复删除请求在同一用户行上发生竞争更新。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	currentUser, err := s.loadUserByID(ctx, user.ID)
	if err != nil {
		return logicdomain.UserDeleteResult{}, err
	}
	code := strings.TrimSpace(currentUser.DeleteConfirmCode)
	if code == "" {
		code, err = generateConfirmationCode()
		if err != nil {
			return logicdomain.UserDeleteResult{}, err
		}
		confirmationStatement := parameterizedUserDeleteConfirmationStatement(currentUser.ID, code, time.Now().UTC().Format(time.RFC3339Nano))
		resp, err := s.execResult(ctx, confirmationStatement.SQL, confirmationStatement.Params...)
		if err != nil {
			wrappedErr := fmt.Errorf("persist user delete confirmation code: %w", err)
			classifiedErr := sqliteWriteCommitBoundaryError("persist user delete confirmation code", wrappedErr)
			if !logicdomain.IsOutcomeUncertain(classifiedErr) {
				return logicdomain.UserDeleteResult{}, wrappedErr
			}
			reconciledUser, reconciledCode, recovered, reconcileErr := s.reconcileUserDeleteConfirmationCode(ctx, currentUser.ID)
			if reconcileErr != nil {
				return logicdomain.UserDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "persist user delete confirmation code",
					Message:   classifiedErr.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !recovered {
				return logicdomain.UserDeleteResult{}, classifiedErr
			}
			currentUser = reconciledUser
			code = reconciledCode
		} else if resp.RowsChanged != 1 {
			// Reload after a guarded no-op so idempotent confirmation returns the durable code written by the winning request.
			// 受保护更新未命中时重新读取，让幂等确认流程返回获胜请求已经写入的长期确认码。
			reconciledUser, reconciledCode, recovered, err := s.reconcileUserDeleteConfirmationCode(ctx, currentUser.ID)
			if err != nil {
				return logicdomain.UserDeleteResult{}, err
			}
			if !recovered {
				return logicdomain.UserDeleteResult{}, fmt.Errorf("persist user delete confirmation code affected %d rows, want 1", resp.RowsChanged)
			}
			currentUser = reconciledUser
			code = reconciledCode
		} else {
			currentUser.DeleteConfirmCode = code
		}
	}
	return logicdomain.UserDeleteResult{
		User:                 currentUser,
		Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
		RequiresConfirmation: true,
		ConfirmationCode:     code,
	}, nil
}

// reconcileUserDeleteConfirmationCode reloads the durable user row after an ambiguous or guarded first-phase write and accepts any stored confirmation code as the source of truth.
// reconcileUserDeleteConfirmationCode 用于在第一阶段写入不明确或受保护更新未命中后重载长期用户行，并把任何已落库确认码作为真实结果。
func (s *Store) reconcileUserDeleteConfirmationCode(ctx context.Context, userID uint64) (logicdomain.UserRecord, string, bool, error) {
	currentUser, err := s.loadUserByID(ctx, userID)
	if err != nil {
		return logicdomain.UserRecord{}, "", false, err
	}
	code := strings.TrimSpace(currentUser.DeleteConfirmCode)
	if code == "" {
		return currentUser, "", false, nil
	}
	return currentUser, code, true, nil
}

// lookupTeamByName resolves one team name and reports whether it already exists.
// lookupTeamByName 用于解析单个 team 名称，并返回它是否已经存在。
func (s *Store) lookupTeamByName(ctx context.Context, teamName string) (logicdomain.TeamRecord, bool, error) {
	rows, err := queryRows[teamRow](s, ctx, `
SELECT id, name, profile, created_at, updated_at
FROM vmm_teams
WHERE name = ?
LIMIT 1
`, teamName)
	if err != nil {
		return logicdomain.TeamRecord{}, false, fmt.Errorf("lookup team: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.TeamRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// lookupSpaceByName resolves one space under the given team and reports whether it already exists.
// lookupSpaceByName 用于在给定 team 下解析单个 space，并返回它是否已经存在。
func (s *Store) lookupSpaceByName(ctx context.Context, teamID uint64, spaceName string, teamExists bool) (logicdomain.SpaceRecord, bool, error) {
	if !teamExists {
		return logicdomain.SpaceRecord{}, false, nil
	}
	rows, err := queryRows[spaceRow](s, ctx, `
SELECT id, team_id, name, profile, created_at, updated_at
FROM vmm_spaces
WHERE team_id = ? AND name = ?
LIMIT 1
`, teamID, spaceName)
	if err != nil {
		return logicdomain.SpaceRecord{}, false, fmt.Errorf("lookup space: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SpaceRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// lookupProjectBySpaceAndName resolves one project under the locked Team/Space hierarchy and reports whether it already exists.
// lookupProjectBySpaceAndName 用于在已加锁的 Team/Space 层级下解析单个 project，并返回它是否已经存在。
func (s *Store) lookupProjectBySpaceAndName(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string) (logicdomain.ProjectRecord, bool, error) {
	rows, err := queryRows[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, p.created_at, p.updated_at,
       t.name AS team_name,
       sp.name AS space_name
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.space_id = ? AND p.name = ?
LIMIT 1
`, space.ID, projectName)
	if err != nil {
		return logicdomain.ProjectRecord{}, false, fmt.Errorf("lookup project: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ProjectRecord{}, false, nil
	}
	project := rows[0].toDomain()
	if project.TeamID != team.ID || project.SpaceID != space.ID {
		return logicdomain.ProjectRecord{}, false, logicdomain.ConflictError{Resource: "project", Message: fmt.Sprintf("project %s resolved outside expected team/space", projectName)}
	}
	return project, true, nil
}

// execInsertOneRow executes one durable create statement and classifies row-count drift by confirmed mutation state.
// execInsertOneRow 用于执行一条长期创建语句，并按已确认写入状态分类行数漂移。
func (s *Store) execInsertOneRow(ctx context.Context, operation string, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			return logicdomain.OutcomeUncertainError{
				Operation: operation,
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	return sqliteAutocommitRowsChangedDriftError(operation, operation, resp.RowsChanged, 1)
}

// sqliteInsertCommitUnknownError reports whether one insert failure came from an ambiguous SQLite commit boundary instead of affected-row drift.
// sqliteInsertCommitUnknownError 用于判断一次插入失败是否来自 SQLite 提交边界不明确，而不是影响行数漂移。
func sqliteInsertCommitUnknownError(err error) bool {
	return err != nil && logicdomain.IsOutcomeUncertain(err) && isSQLiteOutcomeUncertainError(err)
}

// insertSession creates one missing session row and reconciles ambiguous commits by the project-scoped session key.
// insertSession 用于创建一条缺失的 session 行，并通过项目作用域内的 session key 对账提交结果不明的插入。
func (s *Store) insertSession(ctx context.Context, sessionKey string, user logicdomain.UserRecord, project logicdomain.ProjectRecord, now time.Time) (logicdomain.SessionRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_sessions")
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("allocate session id: %w", err)
	}
	nowMs := now.UnixMilli()
	if err := s.execInsertOneRow(ctx, "insert session", `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id,
  turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, '', 0, 0, 0, 0, ?, ?)
`, nextID, sessionKey, user.ID, project.TeamID, project.SpaceID, project.ID, nowMs, nowMs); err != nil {
		// Re-read the project-scoped unique key only when SQLite reports an ambiguous commit boundary, so ordinary insert failures stay retry-safe.
		// 仅当 SQLite 报告提交边界不明确时才按项目作用域唯一键回读，确保普通插入失败仍保持可安全重试。
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupSessionByProjectAndKey(ctx, project.ID, sessionKey)
			if reconcileErr != nil {
				return logicdomain.SessionRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert session",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.SessionRecord{}, err
	}
	return logicdomain.SessionRecord{
		ID:                     nextID,
		SessionKey:             sessionKey,
		UserID:                 user.ID,
		TeamID:                 project.TeamID,
		SpaceID:                project.SpaceID,
		ProjectID:              project.ID,
		TurnCount:              0,
		LastSummarizedID:       0,
		LastCompactedTurnID:    0,
		SummarizeContent:       "",
		SummarizeBudget:        0,
		LastExtractObservedAt:  time.Time{},
		LastExtractCompletedAt: time.Time{},
		LastCompactedAt:        time.Time{},
		CreatedAt:              now,
		UpdatedAt:              now,
	}, nil
}

// insertUser creates one missing user row and reconciles ambiguous commits by the unique user name.
// insertUser 用于创建一条缺失的 user 行，并通过唯一用户名对账提交结果不明的插入。
func (s *Store) insertUser(ctx context.Context, userName string, now time.Time) (logicdomain.UserRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_users")
	if err != nil {
		return logicdomain.UserRecord{}, fmt.Errorf("allocate user id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert user", `
INSERT INTO vmm_users (id, name, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', ?, ?)
`, nextID, userName, nowRFC3339, nowRFC3339); err != nil {
		// Re-read the unique user name only after a commit-unknown insert, preserving the original uncertain error when the row never became durable.
		// 仅在插入提交结果不明后回读唯一用户名；如果长期行并未落库，则保留原始结果不明错误。
		if sqliteInsertCommitUnknownError(err) {
			recovered, reconcileErr := s.ResolveUserRef(ctx, userName)
			if reconcileErr != nil {
				if logicdomain.IsNotFoundError(reconcileErr) {
					return logicdomain.UserRecord{}, err
				}
				return logicdomain.UserRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert user",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			return recovered, nil
		}
		return logicdomain.UserRecord{}, err
	}
	return logicdomain.UserRecord{ID: nextID, Name: userName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertTeam creates one missing team node under the write lock and reconciles ambiguous commits by the unique team name.
// insertTeam 用于在写锁保护下创建缺失的 team 节点，并通过唯一 team 名称对账提交结果不明的插入。
func (s *Store) insertTeam(ctx context.Context, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_teams")
	if err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("allocate team id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert team", `INSERT INTO vmm_teams (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, nextID, teamName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupTeamByName(ctx, teamName)
			if reconcileErr != nil {
				return logicdomain.TeamRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert team",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.TeamRecord{}, err
	}
	return logicdomain.TeamRecord{ID: nextID, Name: teamName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertSpace creates one missing space node under an existing team and reconciles ambiguous commits by the team/name unique key.
// insertSpace 用于在已存在的 team 下创建缺失的 space 节点，并通过 team/name 唯一键对账提交结果不明的插入。
func (s *Store) insertSpace(ctx context.Context, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_spaces")
	if err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("allocate space id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert space", `INSERT INTO vmm_spaces (id, team_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, nextID, teamID, spaceName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupSpaceByName(ctx, teamID, spaceName, true)
			if reconcileErr != nil {
				return logicdomain.SpaceRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert space",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.SpaceRecord{}, err
	}
	return logicdomain.SpaceRecord{ID: nextID, TeamID: teamID, Name: spaceName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertProject creates one missing project node under the resolved Team/Space hierarchy and reconciles ambiguous commits by the scoped project name.
// insertProject 用于在已解析的 Team/Space 层级下创建缺失的 project 节点，并通过范围内 project 名称对账提交结果不明的插入。
func (s *Store) insertProject(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_projects")
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("allocate project id: %w", err)
	}
	nowRFC3339 := now.Format(time.RFC3339Nano)
	if err := s.execInsertOneRow(ctx, "insert project", `INSERT INTO vmm_projects (id, team_id, space_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, nextID, team.ID, space.ID, projectName, nowRFC3339, nowRFC3339); err != nil {
		if sqliteInsertCommitUnknownError(err) {
			recovered, ok, reconcileErr := s.lookupProjectBySpaceAndName(ctx, team, space, projectName)
			if reconcileErr != nil {
				return logicdomain.ProjectRecord{}, logicdomain.OutcomeUncertainError{
					Operation: "insert project",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if ok {
				return recovered, nil
			}
		}
		return logicdomain.ProjectRecord{}, err
	}
	return logicdomain.ProjectRecord{
		ID:        nextID,
		TeamID:    team.ID,
		SpaceID:   space.ID,
		TeamName:  team.Name,
		SpaceName: space.Name,
		Name:      projectName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// countProjectRows returns project-scoped row counts so delete and migrate operations can report meaningful summaries.
// countProjectRows 用于返回项目范围内的行计数，让删除和迁移操作能够输出有意义的结果摘要。
func (s *Store) countProjectRows(ctx context.Context, projectID uint64) (int, int, int, error) {
	sessionCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project sessions: %w", err)
	}
	messageCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project turn records: %w", err)
	}
	memoryNodeCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE project_id = ?`, projectID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count project memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryNodeCount, nil
}

// countUserRows returns user-scoped row counts so protected user deletion can explain what will be removed.
// countUserRows 用于返回用户范围内的行计数，让受保护的用户删除能够说明将要删除的内容。
func (s *Store) countUserRows(ctx context.Context, userID uint64) (int, int, int, error) {
	sessionCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user sessions: %w", err)
	}
	messageCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user turn records: %w", err)
	}
	memoryNodeCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE user_id = ?`, userID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count user memory nodes: %w", err)
	}
	return sessionCount, messageCount, memoryNodeCount, nil
}

// countRows keeps the admin delete and migrate paths concise by centralizing the one-row COUNT(*) query pattern.
// countRows 用于集中处理单行 COUNT(*) 查询模式，让管理类删除和迁移路径保持简洁。
func (s *Store) countRows(ctx context.Context, sql string, params ...any) (int, error) {
	rows, err := queryRows[countRow](s, ctx, sql, params...)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Count, nil
}

// projectDeletePlan records the exact row categories that one project deletion will remove,
// including hierarchy parents that become empty after the project disappears.
// projectDeletePlan 用于记录一次项目删除将真正移除的行类别，
// 包括项目删除后因变空而需要级联删除的父级层级节点。
type projectDeletePlan struct {
	DeletedProjects int
	DeletedSpaces   int
	DeletedTeams    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
}

// projectMigrationPlan records the exact source rows that one confirmed project migration must move before vector rebuild starts.
// projectMigrationPlan 用于记录一次已确认项目迁移在向量重建开始前必须移动的精确源行。
type projectMigrationPlan struct {
	// MigratedSessions records session rows whose hierarchy ids move to the target project.
	// MigratedSessions 用于记录层级 ID 会迁移到目标项目的 session 行数。
	MigratedSessions int
	// MigratedMessages records turn rows whose project id moves to the target project.
	// MigratedMessages 用于记录 project id 会迁移到目标项目的 turn 行数。
	MigratedMessages int
	// MigratedMemories records SQL-backed memory rows whose project scope moves to the target project.
	// MigratedMemories 用于记录项目范围会迁移到目标项目的 SQL 侧 memory 行数。
	MigratedMemories int
	// MigratedProfiles records project-scope profile nodes rebound from the source project to the target project.
	// MigratedProfiles 用于记录从源项目重新绑定到目标项目的项目级画像节点数。
	MigratedProfiles int
}

// userDeletePlan records the exact row categories that one user deletion will remove or detach before turn rows disappear.
// userDeletePlan 用于记录一次用户删除将真正移除的行类别，以及 turn 行消失前必须脱钩的共享画像数量。
type userDeletePlan struct {
	DeletedUsers    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
	// DetachedProfiles records shared-scope profile nodes whose turn references must be cleared before deleting the user turns.
	// DetachedProfiles 用于记录在删除用户 turn 前必须清除 turn 引用的共享范围画像节点数量。
	DetachedProfiles int
}

// planProjectDelete computes the concrete delete counts and empty-parent cascade decisions for one resolved project.
// planProjectDelete 用于为一个已解析项目计算实际删除计数，以及空父级的级联删除决策。
func (s *Store) planProjectDelete(ctx context.Context, project logicdomain.ProjectRecord) (projectDeletePlan, error) {
	sessions, messages, memories, err := s.countProjectRows(ctx, project.ID)
	if err != nil {
		return projectDeletePlan{}, err
	}
	remainingProjectsInSpace, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_projects WHERE space_id = ? AND id <> ?`, project.SpaceID, project.ID)
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count remaining projects in project space: %w", err)
	}
	deleteSpace := remainingProjectsInSpace == 0
	deleteTeam := false
	if deleteSpace {
		remainingSpacesInTeam, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_spaces WHERE team_id = ? AND id <> ?`, project.TeamID, project.SpaceID)
		if err != nil {
			return projectDeletePlan{}, fmt.Errorf("count remaining spaces in project team: %w", err)
		}
		deleteTeam = remainingSpacesInTeam == 0
	}
	countSQL, countParams := buildProjectProfileNodesCountSQL(project.ID, project.SpaceID, project.TeamID, deleteSpace, deleteTeam)
	deletedProfiles, err := s.countRows(ctx, countSQL, countParams...)
	if err != nil {
		return projectDeletePlan{}, fmt.Errorf("count project profile nodes: %w", err)
	}
	plan := projectDeletePlan{
		DeletedProjects: 1,
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
		DeletedProfiles: deletedProfiles,
	}
	if deleteSpace {
		plan.DeletedSpaces = 1
	}
	if deleteTeam {
		plan.DeletedTeams = 1
	}
	return plan, nil
}

// planProjectMigration computes the concrete migration counts for one resolved source project, including project-bound profile nodes that are moved internally but not exposed in the public RPC summary.
// planProjectMigration 用于为一个已解析源项目计算实际迁移计数，包括内部迁移但不暴露到公开 RPC 摘要中的项目绑定画像节点。
func (s *Store) planProjectMigration(ctx context.Context, sourceProjectID uint64) (projectMigrationPlan, error) {
	sessions, messages, memories, err := s.countProjectRows(ctx, sourceProjectID)
	if err != nil {
		return projectMigrationPlan{}, err
	}
	profiles, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeProject, sourceProjectID)
	if err != nil {
		return projectMigrationPlan{}, fmt.Errorf("count project migration profile nodes: %w", err)
	}
	return projectMigrationPlan{
		MigratedSessions: sessions,
		MigratedMessages: messages,
		MigratedMemories: memories,
		MigratedProfiles: profiles,
	}, nil
}

// planUserDelete computes the concrete delete counts for one resolved user while excluding shared-scope profile nodes
// that are retained after the user and their turns disappear.
// planUserDelete 用于为一个已解析用户计算实际删除计数，并排除那些会在用户和其 turn 删除后仍被保留的共享范围画像节点。
func (s *Store) planUserDelete(ctx context.Context, userID uint64) (userDeletePlan, error) {
	sessions, messages, memories, err := s.countUserRows(ctx, userID)
	if err != nil {
		return userDeletePlan{}, err
	}
	deletedProfiles, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeUser, userID)
	if err != nil {
		return userDeletePlan{}, fmt.Errorf("count user profile nodes: %w", err)
	}
	detachSQL, detachParams := buildUserSharedProfileDetachCountSQL(userID)
	detachedProfiles, err := s.countRows(ctx, detachSQL, detachParams...)
	if err != nil {
		return userDeletePlan{}, fmt.Errorf("count shared profile nodes detached from deleted user turns: %w", err)
	}
	return userDeletePlan{
		DeletedUsers:     1,
		DeletedSessions:  sessions,
		DeletedMessages:  messages,
		DeletedMemories:  memories,
		DeletedProfiles:  deletedProfiles,
		DetachedProfiles: detachedProfiles,
	}, nil
}

// buildUserSharedProfileDetachCountSQL renders the COUNT query for shared profile nodes that will survive a user delete but must lose their source turn pointer.
// buildUserSharedProfileDetachCountSQL 用于渲染共享画像节点脱钩前的 COUNT 查询，这些节点会在用户删除后保留但必须失去源 turn 指针。
func buildUserSharedProfileDetachCountSQL(userID uint64) (string, []any) {
	whereSQL, params := buildUserSharedProfileDetachWhere(userID)
	return fmt.Sprintf("SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildUserSharedProfileDetachUpdateSQL renders the UPDATE that detaches surviving shared profile nodes from turns owned by the deleted user.
// buildUserSharedProfileDetachUpdateSQL 用于渲染共享画像节点脱钩 UPDATE，将保留节点与被删除用户拥有的 turn 断开。
func buildUserSharedProfileDetachUpdateSQL(userID uint64, nowMs int64) (string, []any) {
	whereSQL, whereParams := buildUserSharedProfileDetachWhere(userID)
	params := []any{
		logicdomain.ProfileSourceKindRetainedAfterUserDelete,
		userID,
		"source user deleted; shared scope node retained without original turn binding",
		nowMs,
	}
	params = append(params, whereParams...)
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET turn_id = NULL,
    source_kind = ?,
    source_id = ?,
    status_reason = ?,
    updated_timestamp = ?
WHERE %s
`, whereSQL), params
}

// buildUserSharedProfileDetachWhere centralizes the shared-profile predicate so the detach count and update cannot drift apart.
// buildUserSharedProfileDetachWhere 用于集中维护共享画像脱钩条件，避免脱钩计数与实际 UPDATE 范围发生漂移。
func buildUserSharedProfileDetachWhere(userID uint64) (string, []any) {
	return `profile_type <> ? AND turn_id IN (
  SELECT tr.id
  FROM vmm_turn_records tr
  JOIN vmm_sessions s ON s.id = tr.session_id
  WHERE s.user_id = ?
)`, []any{logicdomain.ProfileTypeUser, userID}
}

// buildProjectProfileNodesDeleteSQL renders one raw DELETE that removes every profile node
// actually owned by the project-delete path, including optional empty-parent scopes.
// buildProjectProfileNodesDeleteSQL 用于渲染一条原始 DELETE，
// 删除项目删除路径真正负责的全部画像节点，并在需要时覆盖变空的父级 scope。
func buildProjectProfileNodesDeleteSQL(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	whereSQL, params := buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID, deleteSpace, deleteTeam)
	return fmt.Sprintf("DELETE FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildProjectProfileNodesCountSQL renders one raw COUNT query that matches the same profile-node scope deleted by project removal.
// buildProjectProfileNodesCountSQL 用于渲染一条原始 COUNT 查询，并与项目删除时的画像节点删除范围保持完全一致。
func buildProjectProfileNodesCountSQL(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	whereSQL, params := buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID, deleteSpace, deleteTeam)
	return fmt.Sprintf("SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE %s", whereSQL), params
}

// buildProjectProfileNodesDeleteWhere centralizes the delete/count predicate used by project deletion
// so statistics and actual row removal stay locked to the same scope definition.
// buildProjectProfileNodesDeleteWhere 用于集中维护项目删除时的画像节点条件，
// 让删除统计与实际删行始终共享同一套范围定义。
func buildProjectProfileNodesDeleteWhere(projectID, spaceID, teamID uint64, deleteSpace, deleteTeam bool) (string, []any) {
	conditions := []string{
		"(profile_type = ? AND bind_id = ?)",
		"(turn_id IN (SELECT id FROM vmm_turn_records WHERE project_id = ?))",
	}
	params := []any{logicdomain.ProfileTypeProject, projectID, projectID}
	if deleteSpace {
		conditions = append(conditions, "(profile_type = ? AND bind_id = ?)")
		params = append(params, logicdomain.ProfileTypeSpace, spaceID)
	}
	if deleteTeam {
		conditions = append(conditions, "(profile_type = ? AND bind_id = ?)")
		params = append(params, logicdomain.ProfileTypeTeam, teamID)
	}
	return strings.Join(conditions, " OR "), params
}

// encodeFloat32Slice stores one vector payload as JSON text so unified memory rows can be rebuilt into vector rows later.
// encodeFloat32Slice 用于把向量载荷保存成 JSON 文本，让统一记忆行后续可以重建回向量行。
func encodeFloat32Slice(values []float32) string {
	if len(values) == 0 {
		return "[]"
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// decodeFloat32Slice restores one vector payload from JSON and falls back to an empty slice on malformed data.
// decodeFloat32Slice 用于从 JSON 还原向量载荷，并在数据损坏时退回空切片。
func decodeFloat32Slice(raw string) []float32 {
	if strings.TrimSpace(raw) == "" {
		return []float32{}
	}
	var out []float32
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []float32{}
	}
	return out
}

// decodeStringMap restores one metadata map from JSON and falls back to an empty map on malformed data.
// decodeStringMap 用于从 JSON 还原元数据 map，并在数据损坏时退回空 map。
func decodeStringMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// parameterizedTurnInsertStatement returns one turn INSERT with dehydrated JSON bound as a typed SQLite param.
// parameterizedTurnInsertStatement 用于返回 turn 插入语句，并把脱水 JSON 作为强类型 SQLite 参数绑定。
// The dehydrated payload stays as JSON text on purpose so OSS-local boot no longer depends on SQLite's json extension.
// 脱水载荷会刻意以 JSON 文本形式保存，避免 OSS 本地版启动继续依赖 SQLite 的 json 扩展。
func parameterizedTurnInsertStatement(id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
`,
		Params: []any{id, sessionID, projectID, strings.TrimSpace(dehydratedContent), dehydratedBudget, logicdomain.TurnExtractedStatusPending, createdMs, updatedMs},
	}
}

// parameterizedSessionTurnUpdateStatement returns the absolute session counter UPDATE paired after a durable turn append.
// parameterizedSessionTurnUpdateStatement 用于返回跟随 turn 持久化追加之后执行的绝对 session 计数更新语句。
func parameterizedSessionTurnUpdateStatement(sessionID uint64, turnCount, summarizeBudget int, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_sessions
SET turn_count = ?, summarize_budget = ?,
    updated_timestamp = CASE
      WHEN updated_timestamp < ? THEN ? ELSE updated_timestamp
    END
WHERE id = ?;
`,
		Params: []any{turnCount, summarizeBudget, updatedMs, updatedMs, sessionID},
	}
}

// parameterizedSessionExtractWindowUpdateStatement returns the monotonic extract-window checkpoint UPDATE for one resolved session.
// parameterizedSessionExtractWindowUpdateStatement 用于返回单个已解析 session 的单调提炼窗口检查点 UPDATE。
func parameterizedSessionExtractWindowUpdateStatement(sessionID uint64, observedMs, completedMs, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_sessions
SET last_extract_observed_timestamp = CASE
      WHEN last_extract_observed_timestamp < ? THEN ? ELSE last_extract_observed_timestamp
    END,
    last_extract_completed_timestamp = CASE
      WHEN last_extract_completed_timestamp < ? THEN ? ELSE last_extract_completed_timestamp
    END,
    updated_timestamp = CASE
      WHEN updated_timestamp < ? THEN ? ELSE updated_timestamp
    END
WHERE id = ?;
`,
		Params: []any{observedMs, observedMs, completedMs, completedMs, updatedMs, updatedMs, sessionID},
	}
}

// parameterizedTurnAnalysisUpdateStatement returns the pending-turn update guarded by current session ownership with extracted details bound as typed SQLite params.
// parameterizedTurnAnalysisUpdateStatement 用于返回受当前 session 归属保护的 pending turn 更新语句，并把提炼详情作为强类型 SQLite 参数绑定。
func parameterizedTurnAnalysisUpdateStatement(sessionID, turnID uint64, details string, detailsBudget int, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_turn_records
SET details = ?, details_budget = ?, extracted_status = ?, updated_timestamp = ?
WHERE id = ? AND session_id = ? AND extracted_status = ?;
`,
		Params: []any{strings.TrimSpace(details), detailsBudget, logicdomain.TurnExtractedStatusDone, updatedMs, turnID, sessionID, logicdomain.TurnExtractedStatusPending},
	}
}

// parameterizedMemoryNodeInsertStatement returns the unified memory INSERT used by post-action and direct-write paths with text fields bound outside the SQL template.
// parameterizedMemoryNodeInsertStatement 用于返回 post-action 与主动写路径使用的统一记忆 INSERT，并把文本字段绑定在 SQL 模板之外。
func parameterizedMemoryNodeInsertStatement(record logicdomain.MemoryNodeRecord) sqliteWriteStatement {
	expiresMs := int64(0)
	if !record.ExpiresAt.IsZero() {
		expiresMs = record.ExpiresAt.UTC().UnixMilli()
	}
	lastRecalledMs := int64(0)
	if !record.LastRecalledAt.IsZero() {
		lastRecalledMs = record.LastRecalledAt.UTC().UnixMilli()
	}
	lastAdoptedMs := int64(0)
	if !record.LastAdoptedAt.IsZero() {
		lastAdoptedMs = record.LastAdoptedAt.UTC().UnixMilli()
	}
	lastReinforcedMs := int64(0)
	if !record.LastReinforcedAt.IsZero() {
		lastReinforcedMs = record.LastReinforcedAt.UTC().UnixMilli()
	}
	var sourceTurnIDParam any
	if record.SourceTurnID != 0 {
		sourceTurnIDParam = record.SourceTurnID
	}
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_memory_nodes (
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
  vector_id, vector_json, source_kind, scope_level, category, abstract, details,
  memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
  expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
  recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`,
		Params: []any{
			record.ID,
			record.TeamID,
			record.SpaceID,
			record.ProjectID,
			record.UserID,
			record.OriginSessionID,
			sourceTurnIDParam,
			strings.TrimSpace(record.VectorID),
			encodeFloat32Slice(record.Vector),
			record.SourceKind,
			record.ScopeLevel,
			record.Category,
			strings.TrimSpace(record.Abstract),
			strings.TrimSpace(record.Details),
			record.Status,
			record.Priority,
			record.MemoryLevel,
			record.RefreshWeight,
			record.SupportCount,
			record.RebuttalCount,
			strings.TrimSpace(record.StatusReason),
			expiresMs,
			lastRecalledMs,
			lastAdoptedMs,
			lastReinforcedMs,
			record.RecalledCount,
			record.AdoptedCount,
			record.ReinforcementCount,
			record.CrossSessionAdoptedCount,
			boolToSQLiteInt(record.DecayDisabled),
			strings.TrimSpace(record.DedupeHash),
			record.CreatedAt.UTC().UnixMilli(),
			record.UpdatedAt.UTC().UnixMilli(),
		},
	}
}

// ensureBuiltinMemoryFTS ensures the built-in SQLite FTS index exists and rebuilds it when the configured tokenizer mode changes.
// ensureBuiltinMemoryFTS 用于确保内建 SQLite FTS 索引存在，并在配置的分词模式发生变化时触发重建。
func (s *Store) ensureBuiltinMemoryFTS(ctx context.Context) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if s == nil || s.database == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	result, err := s.database.EnsureFtsIndex(s.ftsIndexName, s.tokenizerMode)
	if err != nil {
		return fmt.Errorf("ensure sqlite builtin fts index: %w", err)
	}
	if err := sqliteFTSEnsureResultError("ensure sqlite builtin fts index", result); err != nil {
		return err
	}
	if result.TokenizerMode != s.tokenizerMode {
		rebuildResult, err := s.database.RebuildFtsIndex(s.ftsIndexName, s.tokenizerMode)
		if err != nil {
			return fmt.Errorf("rebuild sqlite builtin fts index: %w", err)
		}
		if err := sqliteFTSRebuildResultError("rebuild sqlite builtin fts index", rebuildResult); err != nil {
			return err
		}
	}
	return checkSQLiteContext(ctx)
}

// sqliteFTSEnsureResultError converts an unsuccessful ensure result into a boundary error before callers trust tokenizer metadata.
// sqliteFTSEnsureResultError 用于在调用方信任分词器元数据前，把 ensure 返回的失败结果位转换为边界错误。
func sqliteFTSEnsureResultError(stage string, result sqliteffi.EnsureFtsIndexResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// sqliteFTSRebuildResultError converts an unsuccessful rebuild result into a boundary error so full-index recovery cannot report success on a failed FFI result.
// sqliteFTSRebuildResultError 用于把 rebuild 返回的失败结果位转换为边界错误，避免全量索引恢复在 FFI 失败时被误判为成功。
func sqliteFTSRebuildResultError(stage string, result sqliteffi.RebuildFtsIndexResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// sqliteFTSMutationResultError converts an unsuccessful incremental mutation result into a boundary error before the relational/FTS write is considered synchronized.
// sqliteFTSMutationResultError 用于在关系表与 FTS 写入被视为同步前，把增量变更返回的失败结果位转换为边界错误。
func sqliteFTSMutationResultError(stage string, result sqliteffi.FtsMutationResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// syncMemoryFTSAfterWrite synchronizes inserted and deleted memory rows into the built-in SQLite FTS index and falls back to a full rebuild when incremental sync fails.
// syncMemoryFTSAfterWrite 用于把插入与删除的记忆行同步到内建 SQLite FTS 索引，并在增量同步失败时回退到全量重建。
func (s *Store) syncMemoryFTSAfterWrite(ctx context.Context, inserted []logicdomain.MemoryNodeRecord, deletedMemoryIDs []uint64) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if s == nil || s.database == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(inserted) == 0 && len(deletedMemoryIDs) == 0 {
		return nil
	}
	for _, record := range inserted {
		result, err := s.database.UpsertFtsDocument(
			s.ftsIndexName,
			s.tokenizerMode,
			strconv.FormatUint(record.ID, 10),
			record.VectorID,
			strings.TrimSpace(record.Abstract),
			strings.TrimSpace(record.Details),
		)
		if err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, fmt.Errorf("upsert sqlite memory fts document %d: %w", record.ID, err))
		}
		if err := sqliteFTSMutationResultError(fmt.Sprintf("upsert sqlite memory fts document %d", record.ID), result); err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, err)
		}
	}
	for _, memoryID := range storageutil.NormalizeUint64List(deletedMemoryIDs) {
		result, err := s.database.DeleteFtsDocument(s.ftsIndexName, strconv.FormatUint(memoryID, 10))
		if err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, fmt.Errorf("delete sqlite memory fts document %d: %w", memoryID, err))
		}
		if err := sqliteFTSMutationResultError(fmt.Sprintf("delete sqlite memory fts document %d", memoryID), result); err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, err)
		}
	}
	return checkSQLiteContext(ctx)
}

// rebuildMemoryFTSWithFallback rebuilds the full built-in FTS index so relational/FTS consistency can self-heal after one incremental mutation fails.
// rebuildMemoryFTSWithFallback 用于重建整个内建 FTS 索引，让关系表与 FTS 在单次增量同步失败后能够自愈。
func (s *Store) rebuildMemoryFTSWithFallback(ctx context.Context, cause error) error {
	result, err := s.database.RebuildFtsIndex(s.ftsIndexName, s.tokenizerMode)
	if err != nil {
		return fmt.Errorf("%w; rebuild sqlite builtin fts index also failed: %v", cause, err)
	}
	if err := sqliteFTSRebuildResultError("rebuild sqlite builtin fts index", result); err != nil {
		return fmt.Errorf("%w; rebuild sqlite builtin fts index also failed: %v", cause, err)
	}
	return cause
}

// parameterizedMemoryContextEdgesReplaceStatements returns the delete-then-insert edge rewrite used after a memory node write.
// parameterizedMemoryContextEdgesReplaceStatements 用于返回记忆节点写入后的先删后插情境边重写语句。
func parameterizedMemoryContextEdgesReplaceStatements(memoryID uint64, edges []logicdomain.MemoryContextEdge) []sqliteWriteStatement {
	if memoryID == 0 {
		return nil
	}
	statements := []sqliteWriteStatement{{
		SQL: `
DELETE FROM vmm_memory_context_edges
WHERE memory_id = ?;
`,
		Params: []any{memoryID},
	}}
	for _, edge := range edges {
		if strings.TrimSpace(edge.ContextKey) == "" || strings.TrimSpace(edge.ContextValue) == "" {
			continue
		}
		lastSupportedMs := int64(0)
		if !edge.LastSupportedAt.IsZero() {
			lastSupportedMs = edge.LastSupportedAt.UTC().UnixMilli()
		}
		lastRebuttedMs := int64(0)
		if !edge.LastRebuttedAt.IsZero() {
			lastRebuttedMs = edge.LastRebuttedAt.UTC().UnixMilli()
		}
		// Bind the contextual labels because they are extracted text, while the durable owner remains the outer memoryID.
		// 绑定情境标签文本，因为它们来自提炼结果；长期归属仍以外层 memoryID 为准。
		statements = append(statements, sqliteWriteStatement{
			SQL: `
INSERT INTO vmm_memory_context_edges (
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
`,
			Params: []any{
				memoryID,
				strings.TrimSpace(edge.ContextKey),
				strings.TrimSpace(edge.ContextValue),
				edge.SupportCount,
				edge.RebuttalCount,
				lastSupportedMs,
				lastRebuttedMs,
				edge.CreatedAt.UTC().UnixMilli(),
				edge.UpdatedAt.UTC().UnixMilli(),
			},
		})
	}
	return statements
}

// parameterizedMemoryAdoptionUpdateStatement returns one typed UPDATE for a memory row that has just been adopted by pre-check.
// parameterizedMemoryAdoptionUpdateStatement 用于返回一条刚被 pre-check 采纳的记忆行强类型更新语句。
func parameterizedMemoryAdoptionUpdateStatement(record logicdomain.MemoryNodeRecord) sqliteWriteStatement {
	expiresMs := int64(0)
	if !record.ExpiresAt.IsZero() {
		expiresMs = record.ExpiresAt.UTC().UnixMilli()
	}
	lastRecalledMs := int64(0)
	if !record.LastRecalledAt.IsZero() {
		lastRecalledMs = record.LastRecalledAt.UTC().UnixMilli()
	}
	lastAdoptedMs := int64(0)
	if !record.LastAdoptedAt.IsZero() {
		lastAdoptedMs = record.LastAdoptedAt.UTC().UnixMilli()
	}
	lastReinforcedMs := int64(0)
	if !record.LastReinforcedAt.IsZero() {
		lastReinforcedMs = record.LastReinforcedAt.UTC().UnixMilli()
	}
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_memory_nodes
SET scope_level = ?,
    memory_status = ?,
    memory_level = ?,
    refresh_weight = ?,
    expires_timestamp = ?,
    last_recalled_timestamp = ?,
    last_adopted_timestamp = ?,
    last_reinforced_timestamp = ?,
    recalled_count = ?,
    adopted_count = ?,
    reinforcement_count = ?,
    cross_session_adopted_count = ?,
    decay_disabled = ?,
    updated_timestamp = ?
WHERE id = ?;
`,
		Params: []any{
			record.ScopeLevel,
			record.Status,
			record.MemoryLevel,
			record.RefreshWeight,
			expiresMs,
			lastRecalledMs,
			lastAdoptedMs,
			lastReinforcedMs,
			record.RecalledCount,
			record.AdoptedCount,
			record.ReinforcementCount,
			record.CrossSessionAdoptedCount,
			boolToSQLiteInt(record.DecayDisabled),
			record.UpdatedAt.UTC().UnixMilli(),
			record.ID,
		},
	}
}

// parameterizedProfileNodeInsertStatement returns the INSERT statement used by profile-node write paths with all profile text bound as typed SQLite params.
// parameterizedProfileNodeInsertStatement 用于返回画像节点写入路径使用的 INSERT 语句，并把全部画像文本作为强类型 SQLite 参数绑定。
func parameterizedProfileNodeInsertStatement(id uint64, turnID *uint64, profileType int, bindID uint64, node logicdomain.ProfileNodeCandidate, createdMs int64) sqliteWriteStatement {
	expiresMs := int64(0)
	if !node.ExpiresAt.IsZero() {
		expiresMs = node.ExpiresAt.UTC().UnixMilli()
	}
	var turnIDParam any
	if turnID != nil {
		turnIDParam = *turnID
	}
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_profile_nodes (
  id, turn_id, profile_type, bind_id, content, profile_status,
  priority, profile_level, level_reason, refresh_weight, source_kind, source_id, status_reason, expires_timestamp,
  superseded_by_id, profile_date, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?);
`,
		Params: []any{
			id,
			turnIDParam,
			profileType,
			bindID,
			strings.TrimSpace(node.Content),
			node.Status,
			node.Priority,
			node.ProfileLevel,
			strings.TrimSpace(node.LevelReason),
			node.RefreshWeight,
			node.SourceKind,
			node.SourceID,
			strings.TrimSpace(node.StatusReason),
			expiresMs,
			strings.TrimSpace(node.ProfileDate),
			createdMs,
			createdMs,
		},
	}
}

// boolToSQLiteInt renders SQLite-friendly boolean literals because the managed schema persists bool-like fields as integer columns.
// boolToSQLiteInt 用于渲染 SQLite 友好的布尔字面量，因为当前受管 schema 会把布尔型字段持久化为整型列。
func boolToSQLiteInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// optionalUint64Value converts one optional numeric column into the internal zero sentinel used by current in-memory helpers.
// optionalUint64Value 用于把可空数字列转换成当前内存辅助逻辑使用的零值哨兵。
func optionalUint64Value(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

// parameterizedUserDeleteConfirmationStatement returns the guarded UPDATE used by the first phase of user deletion so duplicate confirmation requests reuse one stable code instead of racing on the same row.
// parameterizedUserDeleteConfirmationStatement 用于返回用户删除第一阶段的受保护 UPDATE，让重复确认请求复用同一确认码，而不是在同一行上并发竞争。
func parameterizedUserDeleteConfirmationStatement(userID uint64, confirmationCode, updatedAt string) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_users
SET delete_confirm_code = ?, updated_at = ?
WHERE id = ? AND delete_confirm_code = '';
`,
		Params: []any{strings.TrimSpace(confirmationCode), strings.TrimSpace(updatedAt), userID},
	}
}

// parameterizedProfileNodesSupersedeStatement returns the guarded UPDATE used to retire older profile nodes after one accepted replacement node is durable.
// parameterizedProfileNodesSupersedeStatement 用于返回替代节点落库后退役旧画像节点的受保护 UPDATE 语句。
func parameterizedProfileNodesSupersedeStatement(profileType int, bindID uint64, nodeIDs []uint64, supersededByID uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	params := append([]any{
		logicdomain.ProfileStatusSuperseded,
		supersededByID,
		strings.TrimSpace(statusReason),
		updatedMs,
		logicdomain.ProfileStatusActive,
		profileType,
		bindID,
	}, sqliteUint64Params(nodeIDs)...)
	// Bind reviewer-controlled reason text, target ownership, and node ids together so profile replacement cannot cross scope boundaries.
	// 将评审器控制的原因文本、目标归属和节点 id 一起绑定，确保画像替代不会跨范围更新。
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = ?, superseded_by_id = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (%s);
`, nodeIDPlaceholders),
		Params: params,
	}, true
}

// parameterizedProfileNodesExpireStatement returns the lifecycle UPDATE used to mark due active profile nodes as expired.
// parameterizedProfileNodesExpireStatement 用于返回生命周期收敛中把到期 active 画像节点标为 expired 的 UPDATE 语句。
func parameterizedProfileNodesExpireStatement(nodeIDs []uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	params := append([]any{
		logicdomain.ProfileStatusExpired,
		strings.TrimSpace(statusReason),
		updatedMs,
		logicdomain.ProfileStatusActive,
	}, sqliteUint64Params(nodeIDs)...)
	// Bind lifecycle reason text and node ids together so expiry writes share the same dynamic-IN contract as profile replacement writes.
	// 将生命周期原因文本和节点 id 一起绑定，让过期写入与画像替代写入共享同一套动态 IN 契约。
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND id IN (%s);
`, nodeIDPlaceholders),
		Params: params,
	}, true
}

// parameterizedSingleProfileNodeRetireStatement returns the manual-instruction UPDATE used to retire one active profile node with the reviewer reason.
// parameterizedSingleProfileNodeRetireStatement 用于返回手工画像指令退役单条 active 画像节点的 UPDATE 语句，并绑定评审原因。
func parameterizedSingleProfileNodeRetireStatement(nodeID uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	if nodeID == 0 {
		return sqliteWriteStatement{}, false
	}
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_profile_nodes
SET profile_status = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND id = ?;
`,
		Params: []any{logicdomain.ProfileStatusSuperseded, strings.TrimSpace(statusReason), updatedMs, logicdomain.ProfileStatusActive, nodeID},
	}, true
}

// parameterizedProfileTargetUpdateSQL returns the controlled table-specific UPDATE template used by manual profile instructions before binding rendered profile text as parameters.
// parameterizedProfileTargetUpdateSQL 用于返回手工画像指令使用的受控目标表 UPDATE 模板，并在外层把渲染画像文本作为参数绑定。
func parameterizedProfileTargetUpdateSQL(profileType int) string {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return `UPDATE vmm_users SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeTeam:
		return `UPDATE vmm_teams SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeSpace:
		return `UPDATE vmm_spaces SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeProject:
		return `UPDATE vmm_projects SET profile = ?, updated_at = ? WHERE id = ?;`
	default:
		return ""
	}
}

// parameterizedMemoryNodesSupersedeStatement returns the memory replacement UPDATE used to mark obsolete active memory rows as superseded by id.
// parameterizedMemoryNodesSupersedeStatement 用于返回记忆替代路径中按记忆 id 把过时 active 记忆行标记为 superseded 的 UPDATE 语句。
func parameterizedMemoryNodesSupersedeStatement(memoryIDs []uint64, updatedMs int64) (sqliteWriteStatement, bool) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	params := append([]any{
		logicdomain.MemoryStatusSuperseded,
		updatedMs,
		logicdomain.MemoryStatusActive,
	}, sqliteUint64Params(memoryIDs)...)
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET memory_status = ?, updated_timestamp = ?
WHERE memory_status = ? AND id IN (%s);
`, memoryIDPlaceholders),
		Params: params,
	}, true
}

// normalizeTurnMemoryNodeRecord fills unified-memory defaults for one turn-extracted node before it is persisted.
// normalizeTurnMemoryNodeRecord 用于在持久化前，为一条 turn 提炼记忆补齐统一记忆表默认值。
func normalizeTurnMemoryNodeRecord(session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, node logicdomain.MemoryNodeCandidate, id uint64, now time.Time) logicdomain.MemoryNodeRecord {
	record := logicdomain.MemoryNodeRecord{
		ID:              id,
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.SessionID,
		SourceTurnID:    turn.ID,
		VectorID:        strings.TrimSpace(node.VectorID),
		Vector:          append([]float32(nil), node.Vector...),
		SourceKind:      node.SourceKind,
		ScopeLevel:      node.ScopeLevel,
		Category:        node.Category,
		Abstract:        strings.TrimSpace(node.Abstract),
		Details:         strings.TrimSpace(node.Details),
		Status:          logicdomain.MemoryStatusActive,
		Priority:        node.Priority,
		MemoryLevel:     node.MemoryLevel,
		RefreshWeight:   node.RefreshWeight,
		ExpiresAt:       node.ExpiresAt,
		DedupeHash:      strings.TrimSpace(node.DedupeHash),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindTurnExtract
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	record.LastReinforcedAt = time.Time{}
	record.ReinforcementCount = 0
	return record
}

// normalizeDirectMemoryNodeRecord fills unified-memory defaults for one direct AI-written row before it is persisted.
// normalizeDirectMemoryNodeRecord 用于在持久化前，为一条 AI 主动写记忆补齐统一记忆表默认值。
func normalizeDirectMemoryNodeRecord(session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, id uint64, now time.Time) logicdomain.MemoryNodeRecord {
	record.ID = id
	record.TeamID = session.TeamID
	record.SpaceID = session.SpaceID
	record.ProjectID = session.ProjectID
	record.UserID = session.UserID
	if record.OriginSessionID == 0 {
		record.OriginSessionID = session.SessionID
	}
	record.VectorID = strings.TrimSpace(record.VectorID)
	record.Abstract = strings.TrimSpace(record.Abstract)
	record.Details = strings.TrimSpace(record.Details)
	record.DedupeHash = strings.TrimSpace(record.DedupeHash)
	record.CreatedAt = chooseNonZeroTime(record.CreatedAt, now)
	record.UpdatedAt = chooseNonZeroTime(record.UpdatedAt, now)
	record.SupportCount = 0
	record.RebuttalCount = 0
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindGRPCAIWrite
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if !logicdomain.ValidMemoryStatus(record.Status) {
		record.Status = logicdomain.MemoryStatusActive
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	record.LastReinforcedAt = chooseNonZeroTime(record.LastReinforcedAt, time.Time{})
	if record.ReinforcementCount < 0 {
		record.ReinforcementCount = 0
	}
	return record
}

// defaultUnifiedMemoryExpiry derives the default retention horizon for unified durable memory rows when callers omit an explicit expiry.
// defaultUnifiedMemoryExpiry 用于在调用方省略显式过期时间时，为统一长期记忆推导默认保留时长。
func defaultUnifiedMemoryExpiry(scopeLevel int, now time.Time) time.Time {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return now.UTC().Add(15 * 24 * time.Hour)
	case logicdomain.MemoryScopeLevelUser:
		return now.UTC().Add(365 * 24 * time.Hour)
	default:
		return now.UTC().Add(180 * 24 * time.Hour)
	}
}

// evolveAdoptedMemoryRecord computes the post-adoption lifecycle state for one active unified memory row.
// evolveAdoptedMemoryRecord 用于计算一条活跃统一记忆在被采纳后的生命周期状态。
func evolveAdoptedMemoryRecord(session logicdomain.SessionRef, row logicdomain.MemoryNodeRecord, adoptedAt time.Time) logicdomain.MemoryNodeRecord {
	updated := row
	updated.LastRecalledAt = adoptedAt.UTC()
	updated.LastAdoptedAt = adoptedAt.UTC()
	updated.LastReinforcedAt = adoptedAt.UTC()
	updated.RecalledCount++
	updated.AdoptedCount++
	updated.ReinforcementCount++
	if updated.RefreshWeight <= 0 {
		updated.RefreshWeight = 1
	}
	updated.RefreshWeight++
	if updated.AdoptedCount >= 2 && updated.MemoryLevel < logicdomain.MemoryLevelPhase {
		updated.MemoryLevel = logicdomain.MemoryLevelPhase
	}
	if row.OriginSessionID > 0 && row.OriginSessionID != session.SessionID {
		updated.CrossSessionAdoptedCount++
	}
	if row.ScopeLevel == logicdomain.MemoryScopeLevelSession && updated.CrossSessionAdoptedCount >= 2 {
		updated.ScopeLevel = logicdomain.MemoryScopeLevelProject
		if updated.MemoryLevel < logicdomain.MemoryLevelStable {
			updated.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if updated.CrossSessionAdoptedCount >= 4 && updated.Priority == logicdomain.MemoryPriorityP0 && updated.MemoryLevel < logicdomain.MemoryLevelPersistent {
		updated.MemoryLevel = logicdomain.MemoryLevelPersistent
	}
	updated.ExpiresAt = chooseLongerMemoryExpiry(updated.ExpiresAt, defaultUnifiedMemoryExpiry(updated.ScopeLevel, adoptedAt))
	updated.UpdatedAt = adoptedAt.UTC()
	return updated
}

// chooseLongerMemoryExpiry keeps the farther expiry horizon so adoption never shortens one record's lifetime by mistake.
// chooseLongerMemoryExpiry 用于保留更远的过期时间，避免采纳操作意外缩短某条记录的寿命。
func chooseLongerMemoryExpiry(current, candidate time.Time) time.Time {
	if current.IsZero() {
		return candidate.UTC()
	}
	if current.After(candidate) {
		return current.UTC()
	}
	return candidate.UTC()
}

// chooseNonZeroTime keeps helper callers concise when they want to preserve a provided timestamp but fall back to now.
// chooseNonZeroTime 用于在调用方希望优先保留已有时间戳、否则回退到 now 时，保持辅助逻辑简洁。
func chooseNonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

// collectTurnAnalysisSupersedeMemoryIDs unions reviewer-approved supersede ids from surviving memory nodes so persistence can retire only the durable memories still replaced after every filter.
// collectTurnAnalysisSupersedeMemoryIDs 用于从存活记忆节点中汇总 reviewer 批准的 supersede id，确保持久化只退役经过全部过滤后仍被新节点替代的旧记忆。
func collectTurnAnalysisSupersedeMemoryIDs(nodes []logicdomain.MemoryNodeCandidate) []uint64 {
	if len(nodes) == 0 {
		return nil
	}
	merged := make([]uint64, 0, len(nodes))
	for _, node := range nodes {
		merged = append(merged, node.SupersedeMemoryIDs...)
	}
	return storageutil.NormalizeUint64List(merged)
}

// buildActiveUnexpiredMemoryCondition renders the SQL predicate shared by hot-path memory reads that should ignore superseded or expired rows.
// buildActiveUnexpiredMemoryCondition 用于渲染热路径记忆查询共用的 SQL 条件，让这类读取自动忽略 superseded 或已过期行。
func buildActiveUnexpiredMemoryCondition(alias string, nowMs int64) string {
	alias = strings.TrimSpace(alias)
	if alias != "" {
		alias += "."
	}
	return fmt.Sprintf(`%smemory_status = %d AND (%sexpires_timestamp <= 0 OR %sexpires_timestamp > %d)`, alias, logicdomain.MemoryStatusActive, alias, alias, nowMs)
}

// parseSQLiteTokenizerMode maps the config string into the SQLite FFI tokenizer enum.
// parseSQLiteTokenizerMode 用于把配置字符串映射为 SQLite FFI 分词枚举。
func parseSQLiteTokenizerMode(mode string) (sqliteffi.TokenizerMode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "jieba":
		return sqliteffi.TokenizerJieba, nil
	case "none":
		return sqliteffi.TokenizerNone, nil
	default:
		return sqliteffi.TokenizerNone, fmt.Errorf("unsupported sqlite tokenizer mode: %s", mode)
	}
}

// filepathDir returns the parent directory of one file path and keeps empty input safe for early validation branches.
// filepathDir 用于返回一个文件路径的父目录，并在空输入场景下保持安全。
func filepathDir(path string) string {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return "."
	}
	lastSlash := strings.LastIndexAny(cleaned, `/\`)
	if lastSlash < 0 {
		return "."
	}
	if lastSlash == 0 {
		return cleaned[:1]
	}
	return cleaned[:lastSlash]
}

// checkSQLiteContext returns the current context error when the caller has already cancelled the operation.
// checkSQLiteContext 用于在调用方已取消操作时返回当前 context 错误。
func checkSQLiteContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// loadActiveMemoryNodesByIDs loads active and unexpired memory rows for a parsed FTS candidate set in one relational pass.
// loadActiveMemoryNodesByIDs 用于一次性回表加载已解析 FTS 候选集中的 active 且未过期记忆行。
func (s *Store) loadActiveMemoryNodesByIDs(ctx context.Context, memoryIDs []uint64) (map[uint64]logicdomain.MemoryNodeRecord, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}

	// Batch the relational validation so stale FTS documents are filtered by the same active/unexpired predicate without issuing one SQLite query per hit.
	// 批量执行关系侧校验，让陈旧 FTS 文档继续通过同一套 active/未过期谓词过滤，同时避免每个命中都单独查询 SQLite。
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id IN (%s)
  AND %s
ORDER BY id ASC
`, memoryIDPlaceholders, buildActiveUnexpiredMemoryCondition("", time.Now().UTC().UnixMilli())), memoryIDParams...)
	if err != nil {
		return nil, err
	}
	recordsByID := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		record := row.toMemoryNodeRecord()
		recordsByID[record.ID] = record
	}
	return recordsByID, nil
}

// matchesLexicalMemoryFilter reuses the relational-memory scope rules to keep lexical recall aligned with vector recall.
// matchesLexicalMemoryFilter 用于复用关系层的记忆作用域规则，让 lexical 召回与向量召回保持一致。
func matchesLexicalMemoryFilter(record logicdomain.MemoryNodeRecord, filter logicdomain.SearchFilter) bool {
	if filter.TeamID > 0 && record.TeamID != filter.TeamID {
		return false
	}
	if filter.SpaceID > 0 && record.SpaceID != filter.SpaceID {
		return false
	}
	if filter.ProjectID > 0 && record.ProjectID != filter.ProjectID {
		return false
	}
	if filter.UserID > 0 && record.UserID != 0 && record.UserID != filter.UserID {
		return false
	}
	if filter.SessionID > 0 && record.OriginSessionID != filter.SessionID {
		return false
	}
	if filter.BoundarySessionID > 0 && record.OriginSessionID == filter.BoundarySessionID {
		if filter.ExcludeBoundaryTurn {
			return record.SourceTurnID == 0
		}
		return record.SourceTurnID == 0 || record.SourceTurnID <= filter.BoundaryMaxTurnID
	}
	return true
}

// sortedProfileBindingIDs returns one deterministic ascending id slice from a rendered-profile update map so SQL scripts remain stable in tests and logs.
// sortedProfileBindingIDs 用于从渲染后画像更新 map 中返回确定性的升序 id 列表，确保 SQL 脚本在测试和日志里保持稳定。
func sortedProfileBindingIDs(values map[uint64]string) []uint64 {
	if len(values) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(values))
	for id := range values {
		if id == 0 {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	return ids
}

// sqliteUint64Params converts normalized numeric ids into typed SQLite bind parameters for dynamic IN predicates.
// sqliteUint64Params 用于把规范化后的数字 id 转换为 SQLite 强类型绑定参数，供动态 IN 条件使用。
func sqliteUint64Params(values []uint64) []any {
	if len(values) == 0 {
		return nil
	}
	params := make([]any, 0, len(values))
	for _, value := range values {
		params = append(params, value)
	}
	return params
}

// sqlitePlaceholders renders the exact placeholder list for one dynamic SQLite IN predicate whose values are supplied through typed params.
// sqlitePlaceholders 用于为动态 SQLite IN 条件渲染精确数量的占位符，实际值通过强类型参数传入。
func sqlitePlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for idx := range parts {
		parts[idx] = "?"
	}
	return strings.Join(parts, ",")
}

// unixMilliToTime converts one millisecond unix timestamp back into UTC time and tolerates zero values.
// unixMilliToTime 用于把毫秒 unix 时间戳转回 UTC time，并容忍零值。
func unixMilliToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

type noiseEmbeddingRow struct {
	Scope        string `json:"scope"`
	Language     string `json:"language"`
	CategoryName string `json:"category_name"`
	Phrase       string `json:"phrase"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	RulesHash    string `json:"rules_hash"`
	VectorJSON   string `json:"vector_json"`
	UpdatedAt    string `json:"updated_at"`
}

type userRow struct {
	ID                uint64 `json:"id"`
	Name              string `json:"name"`
	Profile           string `json:"profile"`
	DeleteConfirmCode string `json:"delete_confirm_code"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (r userRow) toDomain() logicdomain.UserRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.UserRecord{
		ID:                r.ID,
		Name:              r.Name,
		Profile:           r.Profile,
		DeleteConfirmCode: r.DeleteConfirmCode,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
}

type teamRow struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r teamRow) toDomain() logicdomain.TeamRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.TeamRecord{ID: r.ID, Name: r.Name, Profile: r.Profile, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type spaceRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r spaceRow) toDomain() logicdomain.SpaceRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.SpaceRecord{ID: r.ID, TeamID: r.TeamID, Name: r.Name, Profile: r.Profile, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

type projectJoinRow struct {
	ID        uint64 `json:"id"`
	TeamID    uint64 `json:"team_id"`
	SpaceID   uint64 `json:"space_id"`
	Name      string `json:"name"`
	Profile   string `json:"profile"`
	TeamName  string `json:"team_name"`
	SpaceName string `json:"space_name"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (r projectJoinRow) toDomain() logicdomain.ProjectRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, _ := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	return logicdomain.ProjectRecord{
		ID:        r.ID,
		TeamID:    r.TeamID,
		SpaceID:   r.SpaceID,
		TeamName:  r.TeamName,
		SpaceName: r.SpaceName,
		Name:      r.Name,
		Profile:   r.Profile,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
}

type sessionRow struct {
	ID                            uint64 `json:"id"`
	SessionKey                    string `json:"session_key"`
	UserID                        uint64 `json:"user_id"`
	TeamID                        uint64 `json:"team_id"`
	SpaceID                       uint64 `json:"space_id"`
	ProjectID                     uint64 `json:"project_id"`
	TurnCount                     int    `json:"turn_count"`
	LastSummarizedID              uint64 `json:"last_summarized_id"`
	LastCompactedTurnID           uint64 `json:"last_compacted_turn_id"`
	SummarizeContent              string `json:"summarize_content"`
	SummarizeBudget               int    `json:"summarize_budget"`
	LastExtractObservedTimestamp  int64  `json:"last_extract_observed_timestamp"`
	LastExtractCompletedTimestamp int64  `json:"last_extract_completed_timestamp"`
	LastCompactedTimestamp        int64  `json:"last_compacted_timestamp"`
	CreatedTimestamp              int64  `json:"created_timestamp"`
	UpdatedTimestamp              int64  `json:"updated_timestamp"`
}

func (r sessionRow) toDomain() logicdomain.SessionRecord {
	return logicdomain.SessionRecord{
		ID:                     r.ID,
		SessionKey:             r.SessionKey,
		UserID:                 r.UserID,
		TeamID:                 r.TeamID,
		SpaceID:                r.SpaceID,
		ProjectID:              r.ProjectID,
		TurnCount:              r.TurnCount,
		LastSummarizedID:       r.LastSummarizedID,
		LastCompactedTurnID:    r.LastCompactedTurnID,
		SummarizeContent:       r.SummarizeContent,
		SummarizeBudget:        r.SummarizeBudget,
		LastExtractObservedAt:  unixMilliToTime(r.LastExtractObservedTimestamp),
		LastExtractCompletedAt: unixMilliToTime(r.LastExtractCompletedTimestamp),
		LastCompactedAt:        unixMilliToTime(r.LastCompactedTimestamp),
		CreatedAt:              unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:              unixMilliToTime(r.UpdatedTimestamp),
	}
}

type turnRecordRow struct {
	ID                uint64 `json:"id"`
	SessionID         uint64 `json:"session_id"`
	ProjectID         uint64 `json:"project_id"`
	DehydratedContent string `json:"dehydrated_content"`
	DehydratedBudget  int    `json:"dehydrated_budget"`
	ExtractedStatus   int    `json:"extracted_status"`
	Details           string `json:"details"`
	DetailsBudget     int    `json:"details_budget"`
	CreatedTimestamp  int64  `json:"created_timestamp"`
	UpdatedTimestamp  int64  `json:"updated_timestamp"`
}

// turnWindowRow stores one neighboring turn id plus its relative position around one requested anchor turn.
// turnWindowRow 用于保存某个请求锚点 turn 周围的一条相邻 turn id 及其相对位置。
type turnWindowRow struct {
	TargetID    uint64 `json:"target_id"`
	TurnID      uint64 `json:"turn_id"`
	RelativePos int    `json:"relative_pos"`
}

func (r turnRecordRow) toDomain() logicdomain.SessionTurnRecord {
	return logicdomain.SessionTurnRecord{
		ID:                r.ID,
		SessionID:         r.SessionID,
		ProjectID:         r.ProjectID,
		DehydratedContent: r.DehydratedContent,
		DehydratedBudget:  r.DehydratedBudget,
		ExtractedStatus:   r.ExtractedStatus,
		Details:           r.Details,
		DetailsBudget:     r.DetailsBudget,
		CreatedAt:         unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:         unixMilliToTime(r.UpdatedTimestamp),
	}
}

type memoryNodeRow struct {
	ID                       uint64  `json:"id"`
	TeamID                   uint64  `json:"team_id"`
	SpaceID                  uint64  `json:"space_id"`
	ProjectID                uint64  `json:"project_id"`
	UserID                   uint64  `json:"user_id"`
	OriginSessionID          uint64  `json:"origin_session_id"`
	SourceTurnID             *uint64 `json:"source_turn_id"`
	VectorID                 string  `json:"vector_id"`
	VectorJSON               string  `json:"vector_json"`
	SourceKind               int     `json:"source_kind"`
	ScopeLevel               int     `json:"scope_level"`
	Category                 int     `json:"category"`
	Abstract                 string  `json:"abstract"`
	Details                  string  `json:"details"`
	MemoryStatus             int     `json:"memory_status"`
	Priority                 int     `json:"priority"`
	MemoryLevel              int     `json:"memory_level"`
	RefreshWeight            int     `json:"refresh_weight"`
	SupportCount             int     `json:"support_count"`
	RebuttalCount            int     `json:"rebuttal_count"`
	StatusReason             string  `json:"status_reason"`
	ExpiresTimestamp         int64   `json:"expires_timestamp"`
	LastRecalledTimestamp    int64   `json:"last_recalled_timestamp"`
	LastAdoptedTimestamp     int64   `json:"last_adopted_timestamp"`
	LastReinforcedTimestamp  int64   `json:"last_reinforced_timestamp"`
	RecalledCount            int     `json:"recalled_count"`
	AdoptedCount             int     `json:"adopted_count"`
	ReinforcementCount       int     `json:"reinforcement_count"`
	CrossSessionAdoptedCount int     `json:"cross_session_adopted_count"`
	DecayDisabledValue       int     `json:"decay_disabled"`
	DedupeHash               string  `json:"dedupe_hash"`
	CreatedTimestamp         int64   `json:"created_timestamp"`
	UpdatedTimestamp         int64   `json:"updated_timestamp"`
}

// memoryLexicalRow stores one lexical recall row produced by SQLite FTS so the adapter can decode memory ids plus rank scores.
// memoryLexicalRow 用于保存 SQLite FTS 产出的 lexical 召回行，让适配器可以解码 memory id 和排序分数。
type memoryLexicalRow struct {
	MemoryID uint64  `json:"memory_id"`
	Rank     float64 `json:"rank"`
}

// memoryContextEdgeRow stores one durable contextual edge row so future filtering or debugging paths can decode the persisted support/rebuttal evidence graph.
// memoryContextEdgeRow 用于保存一条长期情境边行，便于未来过滤或调试链路解码已持久化的支持/反驳证据图。
type memoryContextEdgeRow struct {
	MemoryID               uint64 `json:"memory_id"`
	ContextKey             string `json:"context_key"`
	ContextValue           string `json:"context_value"`
	SupportCount           int    `json:"support_count"`
	RebuttalCount          int    `json:"rebuttal_count"`
	LastSupportedTimestamp int64  `json:"last_supported_timestamp"`
	LastRebuttedTimestamp  int64  `json:"last_rebutted_timestamp"`
	CreatedTimestamp       int64  `json:"created_timestamp"`
	UpdatedTimestamp       int64  `json:"updated_timestamp"`
}

// toDomain converts one SQL edge row into the public contextual-evidence record used by query-time scoring and future context-aware filters.
// toDomain 用于把一条 SQL 情境边行转换成查询期打分和未来情境过滤会复用的公开证据记录。
func (r memoryContextEdgeRow) toDomain() logicdomain.MemoryContextEdge {
	return logicdomain.MemoryContextEdge{
		MemoryID:        r.MemoryID,
		ContextKey:      strings.TrimSpace(r.ContextKey),
		ContextValue:    strings.TrimSpace(r.ContextValue),
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		LastSupportedAt: unixMilliToTime(r.LastSupportedTimestamp),
		LastRebuttedAt:  unixMilliToTime(r.LastRebuttedTimestamp),
		CreatedAt:       unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:       unixMilliToTime(r.UpdatedTimestamp),
	}
}

func (r memoryNodeRow) toDomain() logicdomain.SessionMemoryNodeRecord {
	return logicdomain.SessionMemoryNodeRecord{
		ID:              r.ID,
		TeamID:          r.TeamID,
		SpaceID:         r.SpaceID,
		ProjectID:       r.ProjectID,
		UserID:          r.UserID,
		OriginSessionID: r.OriginSessionID,
		TurnID:          optionalUint64Value(r.SourceTurnID),
		VectorID:        r.VectorID,
		Category:        r.Category,
		Abstract:        r.Abstract,
		Details:         r.Details,
		SourceKind:      r.SourceKind,
		ScopeLevel:      r.ScopeLevel,
		Priority:        r.Priority,
		MemoryLevel:     r.MemoryLevel,
		RefreshWeight:   r.RefreshWeight,
		SupportCount:    r.SupportCount,
		RebuttalCount:   r.RebuttalCount,
		NodeStatus:      r.MemoryStatus,
		CreatedAt:       unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:       unixMilliToTime(r.UpdatedTimestamp),
	}
}

// toMemoryNodeRecord converts one SQL row into the public unified durable-memory record used by mixed detail lookup and direct-write dedupe flows.
// toMemoryNodeRecord 用于把一条 SQL 行转换成混合详情查询和主动写入查重流程使用的统一长期记忆记录。
func (r memoryNodeRow) toMemoryNodeRecord() logicdomain.MemoryNodeRecord {
	return logicdomain.MemoryNodeRecord{
		ID:                       r.ID,
		TeamID:                   r.TeamID,
		SpaceID:                  r.SpaceID,
		ProjectID:                r.ProjectID,
		UserID:                   r.UserID,
		OriginSessionID:          r.OriginSessionID,
		SourceTurnID:             optionalUint64Value(r.SourceTurnID),
		VectorID:                 r.VectorID,
		Vector:                   decodeFloat32Slice(r.VectorJSON),
		SourceKind:               r.SourceKind,
		ScopeLevel:               r.ScopeLevel,
		Category:                 r.Category,
		Abstract:                 r.Abstract,
		Details:                  r.Details,
		Status:                   r.MemoryStatus,
		Priority:                 r.Priority,
		MemoryLevel:              r.MemoryLevel,
		RefreshWeight:            r.RefreshWeight,
		SupportCount:             r.SupportCount,
		RebuttalCount:            r.RebuttalCount,
		StatusReason:             r.StatusReason,
		ExpiresAt:                unixMilliToTime(r.ExpiresTimestamp),
		LastRecalledAt:           unixMilliToTime(r.LastRecalledTimestamp),
		LastAdoptedAt:            unixMilliToTime(r.LastAdoptedTimestamp),
		LastReinforcedAt:         unixMilliToTime(r.LastReinforcedTimestamp),
		RecalledCount:            r.RecalledCount,
		AdoptedCount:             r.AdoptedCount,
		ReinforcementCount:       r.ReinforcementCount,
		CrossSessionAdoptedCount: r.CrossSessionAdoptedCount,
		DecayDisabled:            r.DecayDisabledValue > 0,
		DedupeHash:               r.DedupeHash,
		CreatedAt:                unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:                unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileNodeRow struct {
	ID                         uint64  `json:"id"`
	TurnID                     *uint64 `json:"turn_id"`
	ProfileType                int     `json:"profile_type"`
	BindID                     uint64  `json:"bind_id"`
	Content                    string  `json:"content"`
	ProfileStatus              int     `json:"profile_status"`
	Priority                   int     `json:"priority"`
	ProfileLevel               int     `json:"profile_level"`
	LevelReason                string  `json:"level_reason"`
	RefreshWeight              int     `json:"refresh_weight"`
	SourceKind                 int     `json:"source_kind"`
	SourceID                   uint64  `json:"source_id"`
	StatusReason               string  `json:"status_reason"`
	ExpiresTimestamp           int64   `json:"expires_timestamp"`
	SupersededByID             uint64  `json:"superseded_by_id"`
	ProfileDate                string  `json:"profile_date"`
	CreatedTimestamp           int64   `json:"created_timestamp"`
	UpdatedTimestamp           int64   `json:"updated_timestamp"`
	ProfileDateAnchorTimestamp int64   `json:"profile_date_anchor_timestamp"`
}

// toRecord converts one SQL row into the public profile-node record shape returned by query RPCs and manual profile writebacks.
// toRecord 用于把一条 SQL 行转换成查询 RPC 与手工画像写回返回的公开画像节点结构。
func (r profileNodeRow) toRecord() logicdomain.ProfileNodeRecord {
	profileDateAnchorAt := unixMilliToTime(r.ProfileDateAnchorTimestamp)
	if profileDateAnchorAt.IsZero() {
		profileDateAnchorAt = unixMilliToTime(r.CreatedTimestamp)
	}
	return logicdomain.ProfileNodeRecord{
		ID:                  r.ID,
		TurnID:              optionalUint64Value(r.TurnID),
		ProfileType:         r.ProfileType,
		BindID:              r.BindID,
		Content:             r.Content,
		Status:              r.ProfileStatus,
		Priority:            r.Priority,
		ProfileLevel:        r.ProfileLevel,
		LevelReason:         r.LevelReason,
		RefreshWeight:       r.RefreshWeight,
		ProfileDate:         r.ProfileDate,
		SourceKind:          r.SourceKind,
		SourceID:            r.SourceID,
		StatusReason:        r.StatusReason,
		ExpiresAt:           unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID:      r.SupersededByID,
		ProfileDateAnchorAt: profileDateAnchorAt,
		CreatedAt:           unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:           unixMilliToTime(r.UpdatedTimestamp),
	}
}

func (r profileNodeRow) toDomain() logicdomain.ProfileActiveNodeRecord {
	profileDateAnchorAt := unixMilliToTime(r.ProfileDateAnchorTimestamp)
	if profileDateAnchorAt.IsZero() {
		profileDateAnchorAt = unixMilliToTime(r.CreatedTimestamp)
	}
	return logicdomain.ProfileActiveNodeRecord{
		ID:                  r.ID,
		TurnID:              optionalUint64Value(r.TurnID),
		ProfileType:         r.ProfileType,
		BindID:              r.BindID,
		Content:             r.Content,
		Status:              r.ProfileStatus,
		Priority:            r.Priority,
		ProfileLevel:        r.ProfileLevel,
		LevelReason:         r.LevelReason,
		RefreshWeight:       r.RefreshWeight,
		ProfileDate:         r.ProfileDate,
		SourceKind:          r.SourceKind,
		SourceID:            r.SourceID,
		StatusReason:        r.StatusReason,
		ExpiresAt:           unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID:      r.SupersededByID,
		ProfileDateAnchorAt: profileDateAnchorAt,
		CreatedAt:           unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:           unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileNodeStatusRow struct {
	ID             uint64 `json:"id"`
	ProfileStatus  int    `json:"profile_status"`
	SupersededByID uint64 `json:"superseded_by_id"`
	// StatusReason stores the lifecycle reason written by guarded status updates that need exact reconciliation.
	// StatusReason 保存受保护状态更新写入的生命周期原因，供精确对账使用。
	StatusReason string `json:"status_reason"`
	// UpdatedTimestamp stores the durable millisecond timestamp used to prove an uncertain update reached this write.
	// UpdatedTimestamp 保存长期毫秒时间戳，用于证明不确定更新已经达到本次写入。
	UpdatedTimestamp int64 `json:"updated_timestamp"`
}

type profileInstructionRow struct {
	ID                uint64 `json:"id"`
	ProfileType       int    `json:"profile_type"`
	BindID            uint64 `json:"bind_id"`
	Instruction       string `json:"instruction"`
	InstructionStatus int    `json:"instruction_status"`
	ReviewResultJSON  string `json:"review_result_json"`
	FailureReason     string `json:"failure_reason"`
	CreatedTimestamp  int64  `json:"created_timestamp"`
	UpdatedTimestamp  int64  `json:"updated_timestamp"`
}

func (r profileInstructionRow) toDomain() logicdomain.ProfileInstructionRecord {
	return logicdomain.ProfileInstructionRecord{
		ID:            r.ID,
		ProfileType:   r.ProfileType,
		BindID:        r.BindID,
		Instruction:   r.Instruction,
		Status:        r.InstructionStatus,
		ReviewResult:  r.ReviewResultJSON,
		FailureReason: r.FailureReason,
		CreatedAt:     unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:     unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileBlobRow struct {
	Profile string `json:"profile"`
}

// renderedProfileBatchRow mirrors one scope row read back after an uncertain rendered-profile batch update.
// renderedProfileBatchRow 用于映射渲染画像批量更新不确定后回读的一条 scope 行。
type renderedProfileBatchRow struct {
	// ID stores the durable scope-row id and must match the sorted batch id at the same index.
	// ID 保存长期 scope 行 id，必须与同一排序位置上的批量 id 匹配。
	ID uint64 `json:"id"`
	// Profile stores the rendered profile blob written by ReplaceRenderedProfiles.
	// Profile 保存 ReplaceRenderedProfiles 写入的渲染画像 Blob。
	Profile string `json:"profile"`
	// UpdatedAt stores the RFC3339Nano timestamp written with the profile blob, proving the row reached this batch.
	// UpdatedAt 保存随画像 Blob 一起写入的 RFC3339Nano 时间戳，用于证明该行已达到本次批量写入。
	UpdatedAt string `json:"updated_at"`
}

type obsoleteVectorRow struct {
	VectorID string `json:"vector_id"`
}

type memoryEntryRow struct {
	ID           string `json:"id"`
	TeamID       uint64 `json:"team_id"`
	SpaceID      uint64 `json:"space_id"`
	ProjectID    uint64 `json:"project_id"`
	SessionID    uint64 `json:"session_id"`
	UserID       uint64 `json:"user_id"`
	Content      string `json:"content"`
	VectorJSON   string `json:"vector_json"`
	MetadataJSON string `json:"metadata_json"`
	CreatedAt    string `json:"created_at"`
}

func (r memoryEntryRow) toDomain() logicdomain.MemoryRecord {
	createdAt, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
	return logicdomain.MemoryRecord{
		ID:     r.ID,
		Text:   r.Content,
		Vector: decodeFloat32Slice(r.VectorJSON),
		Filter: logicdomain.SearchFilter{
			UserID:    r.UserID,
			TeamID:    r.TeamID,
			SpaceID:   r.SpaceID,
			ProjectID: r.ProjectID,
			SessionID: r.SessionID,
		},
		Metadata:  decodeStringMap(r.MetadataJSON),
		CreatedAt: createdAt,
	}
}

type countRow struct {
	Count int `json:"count"`
}
