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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// currentSchemaVersion tracks the newest SQLite schema version understood by this runtime.
	// currentSchemaVersion 用于标记当前运行时理解的最新 SQLite 表结构版本。
	currentSchemaVersion = 19

	// versionSingletonID pins the schema-version row to one deterministic singleton record.
	// versionSingletonID 用于把 schema 版本记录固定到一条确定性的单例行。
	versionSingletonID = 1

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
	debugSeedDefaultName = "default"

	// sqliteRetryMaxAttempts keeps retryable SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA responses bounded so callers do not spin forever.
	// sqliteRetryMaxAttempts 用于限制 SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA 这类可重试响应的最大重试次数，避免调用方无限自旋。
	sqliteRetryMaxAttempts = 3

	// sqliteRetryBaseDelay is the small exponential-backoff seed used after the gateway marks one SQLite response as retryable.
	// sqliteRetryBaseDelay 用于在网关把某次 SQLite 响应标记为可重试后，提供一个较小的指数退避起始值。
	sqliteRetryBaseDelay = 50 * time.Millisecond
)

const resetManagedSchemaSQL = `
DROP TABLE IF EXISTS vmm_profile_nodes;
DROP TABLE IF EXISTS vmm_profile_instructions;
DROP TABLE IF EXISTS vmm_turn_records_trash;
DROP TABLE IF EXISTS vmm_memory_context_edges_trash;
DROP TABLE IF EXISTS vmm_memory_nodes_trash;
DROP TABLE IF EXISTS vmm_recycle_batches;
DROP TABLE IF EXISTS vmm_recycle_jobs;
DROP TABLE IF EXISTS vmm_vector_gc_jobs;
DROP TABLE IF EXISTS vmm_memory_nodes_fts;
DROP TABLE IF EXISTS vmm_memory_context_edges;
DROP TABLE IF EXISTS vmm_memory_nodes;
DROP TABLE IF EXISTS vmm_turn_records;
DROP TABLE IF EXISTS vmm_chat_messages;
DROP TABLE IF EXISTS vmm_memory_entries;
DROP TABLE IF EXISTS vmm_scratchpad_nodes;
DROP TABLE IF EXISTS vmm_scratchpad_plans;
DROP TABLE IF EXISTS vmm_sessions;
DROP TABLE IF EXISTS vmm_projects;
DROP TABLE IF EXISTS vmm_spaces;
DROP TABLE IF EXISTS vmm_teams;
DROP TABLE IF EXISTS vmm_users;
DROP TABLE IF EXISTS vmm_noise_embeddings;
`

const currentSchemaSQL = `
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

CREATE TABLE IF NOT EXISTS vmm_scratchpad_plans (
  id BIGINT PRIMARY KEY,
  project_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  session_key TEXT NOT NULL,
  plan_name TEXT NOT NULL,
  plan_name_norm TEXT NOT NULL,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  UNIQUE(project_id, user_id, session_key),
  FOREIGN KEY(user_id) REFERENCES vmm_users(id),
  FOREIGN KEY(project_id) REFERENCES vmm_projects(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_plans_scope ON vmm_scratchpad_plans(project_id, user_id, session_key, updated_timestamp);
CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_plans_gc ON vmm_scratchpad_plans(updated_timestamp, id);

CREATE TABLE IF NOT EXISTS vmm_scratchpad_nodes (
  id BIGINT PRIMARY KEY,
  plan_id BIGINT NOT NULL,
  item_key TEXT NOT NULL,
  item_value TEXT NOT NULL,
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  UNIQUE(plan_id, item_key),
  FOREIGN KEY(plan_id) REFERENCES vmm_scratchpad_plans(id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_nodes_plan ON vmm_scratchpad_nodes(plan_id, updated_timestamp, id);
CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_nodes_created ON vmm_scratchpad_nodes(created_timestamp, id);

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

CREATE VIRTUAL TABLE IF NOT EXISTS vmm_memory_nodes_fts USING fts5(
  memory_id UNINDEXED,
  abstract,
  details,
  tokenize='unicode61 remove_diacritics 2'
);

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

`

// Store is the SQLite-gateway adapter used for hierarchy metadata, session/turn persistence, and cache storage.
// Store 用于作为 SQLite 网关适配器，承接层级元数据、session/turn 持久化以及缓存存储。
type Store struct {
	conn             *grpc.ClientConn
	client           sqlitev1.SqliteServiceClient
	timeout          time.Duration
	writeMu          sync.Mutex
	lexicalTokenizer *textutil.LexicalTokenizer
}

// StoreOptions collects optional SQLite adapter features that change lexical indexing behavior without affecting unrelated callers.
// StoreOptions 用于收集会改变 lexical 索引行为、但不会影响其他调用方的 SQLite 适配器可选特性。
type StoreOptions struct {
	LexicalPreTokenize bool
}

// NewStore dials the SQLite gateway and ensures the local schema is initialized before serving traffic.
// NewStore 用于连接 SQLite 网关，并在对外提供服务前确保本地表结构已经初始化。
func NewStore(address string, timeout time.Duration, options ...StoreOptions) (*Store, error) {
	// Validate and normalize the gateway endpoint first so startup failures remain easy to diagnose.
	// 先校验并规范化网关地址，确保启动失败原因保持易于诊断。
	if strings.TrimSpace(address) == "" {
		return nil, fmt.Errorf("sqlite address is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	storeOptions := StoreOptions{LexicalPreTokenize: true}
	if len(options) > 0 {
		storeOptions = options[0]
	}

	// Initialize the tokenizer before dialing so lexical configuration errors fail during startup instead of the first recall request.
	// 在建立连接前初始化分词器，让 lexical 配置错误在启动阶段暴露，而不是拖到首次召回时才失败。
	lexicalTokenizer, err := textutil.NewLexicalTokenizer(textutil.LexicalTokenizerConfig{
		EnablePreTokenize: storeOptions.LexicalPreTokenize,
	})
	if err != nil {
		return nil, fmt.Errorf("init lexical tokenizer: %w", err)
	}

	// Dial the gateway eagerly so configuration drift is caught during application boot.
	// 以阻塞方式建立网关连接，让配置漂移在应用启动阶段就暴露出来。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		strings.TrimSpace(address),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial sqlite gateway: %w", err)
	}

	store := &Store{
		conn:             conn,
		client:           sqlitev1.NewSqliteServiceClient(conn),
		timeout:          timeout,
		lexicalTokenizer: lexicalTokenizer,
	}
	if err := store.init(context.Background()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return store, nil
}

// Shutdown closes the gRPC client connection used by the SQLite gateway adapter.
// Shutdown 用于关闭 SQLite 网关适配器所使用的 gRPC 客户端连接。
func (s *Store) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// init ensures the SQLite schema is bootstrapped or incrementally migrated before the store starts serving traffic.
// init 用于在存储开始提供服务前，确保 SQLite schema 已完成初始化或增量迁移。
func (s *Store) init(ctx context.Context) error {
	return s.ensureSQLiteSchema(ctx)
}

// buildDebugSeedWorkspaceSQL renders the deterministic testing-stage seed rows so grpc debugging always starts with user_id=1 and project_id=1 available.
// buildDebugSeedWorkspaceSQL 用于渲染测试阶段的确定性种子数据，让 gRPC 调试时始终直接拥有 user_id=1 和 project_id=1。
func buildDebugSeedWorkspaceSQL(now time.Time) string {
	now = now.UTC()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	return fmt.Sprintf(`
INSERT INTO vmm_users (id, name, profile, delete_confirm_code, created_at, updated_at)
VALUES (%d, %s, '', '', %s, %s);

INSERT INTO vmm_teams (id, name, profile, created_at, updated_at)
VALUES (%d, %s, '', %s, %s);

INSERT INTO vmm_spaces (id, team_id, name, profile, created_at, updated_at)
VALUES (%d, %d, %s, '', %s, %s);

INSERT INTO vmm_projects (id, team_id, space_id, name, profile, created_at, updated_at)
VALUES (%d, %d, %d, %s, '', %s, %s);
`, debugSeedUserID, sqlStringLiteral(debugSeedDefaultName), sqlStringLiteral(nowRFC3339), sqlStringLiteral(nowRFC3339),
		debugSeedTeamID, sqlStringLiteral(debugSeedDefaultName), sqlStringLiteral(nowRFC3339), sqlStringLiteral(nowRFC3339),
		debugSeedSpaceID, debugSeedTeamID, sqlStringLiteral(debugSeedDefaultName), sqlStringLiteral(nowRFC3339), sqlStringLiteral(nowRFC3339),
		debugSeedProjectID, debugSeedTeamID, debugSeedSpaceID, sqlStringLiteral(debugSeedDefaultName), sqlStringLiteral(nowRFC3339), sqlStringLiteral(nowRFC3339))
}

// exec sends one SQL statement or script to the SQLite gateway and prefers native typed params for the sqlite-first runtime path.
// exec 用于把单条 SQL 或脚本发送给 SQLite 网关，并在 sqlite-first 运行路径里优先使用原生强类型参数。
func (s *Store) exec(ctx context.Context, sql string, params ...any) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return err
	}
	req := &sqlitev1.ExecuteRequest{
		Sql: strings.TrimSpace(sql),
	}
	applySQLiteExecParams(req, prepared)
	resp, err := s.executeScript(ctx, req)
	if err != nil {
		return fmt.Errorf("sqlite execute: %w", err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("sqlite execute: %s", strings.TrimSpace(resp.GetMessage()))
	}
	return nil
}

// execBatch sends one repeated-shape write workload through SQLite's native ExecuteBatch RPC so sqlite-first storage avoids one-RPC-per-row churn.
// execBatch 用于通过 SQLite 原生 ExecuteBatch RPC 发送同构写入，避免 sqlite-first 存储退化成“一行一次 RPC”。
func (s *Store) execBatch(ctx context.Context, sql string, items [][]any) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(items) == 0 {
		return nil
	}
	batchItems := make([]*sqlitev1.ExecuteBatchItem, 0, len(items))
	for idx, item := range items {
		values, err := prepareSQLiteBatchParams(item)
		if err != nil {
			return fmt.Errorf("prepare sqlite batch item %d: %w", idx, err)
		}
		batchItems = append(batchItems, &sqlitev1.ExecuteBatchItem{Params: values})
	}
	resp, err := s.executeBatch(ctx, &sqlitev1.ExecuteBatchRequest{
		Sql:   strings.TrimSpace(sql),
		Items: batchItems,
	})
	if err != nil {
		return fmt.Errorf("sqlite execute batch: %w", err)
	}
	if !resp.GetSuccess() {
		return fmt.Errorf("sqlite execute batch: %s", strings.TrimSpace(resp.GetMessage()))
	}
	return nil
}

// queryRows decodes one JSON query response into a typed slice so higher-level methods can stay small and explicit while still preferring native sqlite params.
// queryRows 用于把 JSON 查询结果解码为强类型切片，在保持上层逻辑简洁的同时优先使用 sqlite 原生参数。
func queryRows[T any](s *Store, ctx context.Context, sql string, params ...any) ([]T, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	req := &sqlitev1.QueryRequest{
		Sql: strings.TrimSpace(sql),
	}
	applySQLiteQueryParams(req, prepared)
	resp, err := s.queryJSON(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sqlite query: %w", err)
	}
	rows := make([]T, 0)
	body := strings.TrimSpace(resp.GetJsonData())
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
	Values       []*sqlitev1.SqliteValue
	FallbackJSON string
}

// prepareSQLiteParams converts Go scalar values into the typed sqlite gRPC payload expected by the sqlite-first gateway path.
// prepareSQLiteParams 用于把 Go 标量参数转换成 sqlite-first 网关期望的强类型 gRPC 载荷。
func prepareSQLiteParams(params []any) (sqlitePreparedParams, error) {
	if len(params) == 0 {
		return sqlitePreparedParams{}, nil
	}
	values := make([]*sqlitev1.SqliteValue, 0, len(params))
	for idx, param := range params {
		value, err := toSQLiteValue(param)
		if err != nil {
			return sqlitePreparedParams{}, fmt.Errorf("convert sqlite param %d: %w", idx, err)
		}
		values = append(values, value)
	}
	return sqlitePreparedParams{Values: values}, nil
}

// prepareSQLiteBatchParams converts one ExecuteBatch item into native sqlite gRPC values so repeated writes never fall back to JSON strings.
// prepareSQLiteBatchParams 用于把单个 ExecuteBatch item 转成原生 sqlite gRPC 值，确保重复写入不会退回 JSON 兼容模式。
func prepareSQLiteBatchParams(params []any) ([]*sqlitev1.SqliteValue, error) {
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prepared.FallbackJSON) != "" {
		return nil, fmt.Errorf("sqlite batch params do not support JSON fallback")
	}
	return prepared.Values, nil
}

// applySQLiteExecParams attaches either native typed params or the compatibility JSON payload to one ExecuteScript request.
// applySQLiteExecParams 用于把原生强类型参数或兼容 JSON 参数附加到单个 ExecuteScript 请求。
func applySQLiteExecParams(req *sqlitev1.ExecuteRequest, prepared sqlitePreparedParams) {
	if req == nil {
		return
	}
	req.Params = prepared.Values
	req.ParamsJson = strings.TrimSpace(prepared.FallbackJSON)
}

// applySQLiteQueryParams attaches either native typed params or the compatibility JSON payload to one QueryJson request.
// applySQLiteQueryParams 用于把原生强类型参数或兼容 JSON 参数附加到单个 QueryJson 请求。
func applySQLiteQueryParams(req *sqlitev1.QueryRequest, prepared sqlitePreparedParams) {
	if req == nil {
		return
	}
	req.Params = prepared.Values
	req.ParamsJson = strings.TrimSpace(prepared.FallbackJSON)
}

// toSQLiteValue maps one Go scalar to the sqlite gateway's typed protobuf value so the adapter can stay sqlite-native by default.
// toSQLiteValue 用于把单个 Go 标量映射为 sqlite 网关的强类型 protobuf 值，让适配器默认保持 sqlite 原生风格。
func toSQLiteValue(param any) (*sqlitev1.SqliteValue, error) {
	switch value := param.(type) {
	case nil:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_NullValue{NullValue: &sqlitev1.NullValue{}}}, nil
	case bool:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_BoolValue{BoolValue: value}}, nil
	case string:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_StringValue{StringValue: value}}, nil
	case []byte:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_BytesValue{BytesValue: value}}, nil
	case json.RawMessage:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_StringValue{StringValue: string(value)}}, nil
	case int:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case int8:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case int16:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case int32:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case int64:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: value}}, nil
	case uint:
		if uint64(value) > math.MaxInt64 {
			return nil, fmt.Errorf("uint %d overflows sqlite int64 binding", value)
		}
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case uint8:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case uint16:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case uint32:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case uint64:
		if value > math.MaxInt64 {
			return nil, fmt.Errorf("uint64 %d overflows sqlite int64 binding", value)
		}
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Int64Value{Int64Value: int64(value)}}, nil
	case float32:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Float64Value{Float64Value: float64(value)}}, nil
	case float64:
		return &sqlitev1.SqliteValue{Kind: &sqlitev1.SqliteValue_Float64Value{Float64Value: value}}, nil
	default:
		return nil, fmt.Errorf("unsupported sqlite param type %T", param)
	}
}

// executeScript performs one ExecuteScript RPC with bounded retry logic for gateway-declared retryable SQLite errors.
// executeScript 用于执行单次 ExecuteScript RPC，并在网关声明可重试的 SQLite 错误上做有界重试。
func (s *Store) executeScript(ctx context.Context, req *sqlitev1.ExecuteRequest) (*sqlitev1.ExecuteResponse, error) {
	var response *sqlitev1.ExecuteResponse
	err := s.withSQLiteRetry(ctx, func(callCtx context.Context, trailer *metadata.MD) error {
		var err error
		response, err = s.client.ExecuteScript(callCtx, req, grpc.Trailer(trailer))
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// executeBatch performs one ExecuteBatch RPC with bounded retry logic so SQLITE_BUSY / SQLITE_LOCKED do not immediately bubble up to the caller.
// executeBatch 用于执行单次 ExecuteBatch RPC，并对 SQLITE_BUSY / SQLITE_LOCKED 之类错误做有界重试，避免立刻上抛给调用方。
func (s *Store) executeBatch(ctx context.Context, req *sqlitev1.ExecuteBatchRequest) (*sqlitev1.ExecuteBatchResponse, error) {
	var response *sqlitev1.ExecuteBatchResponse
	err := s.withSQLiteRetry(ctx, func(callCtx context.Context, trailer *metadata.MD) error {
		var err error
		response, err = s.client.ExecuteBatch(callCtx, req, grpc.Trailer(trailer))
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// queryJSON performs one QueryJson RPC with the same retry contract used by writes so retryable SQLITE_SCHEMA / SQLITE_BUSY failures can self-heal.
// queryJSON 用于执行单次 QueryJson RPC，并复用与写请求一致的重试契约，让 SQLITE_SCHEMA / SQLITE_BUSY 这类可重试错误有机会自行恢复。
func (s *Store) queryJSON(ctx context.Context, req *sqlitev1.QueryRequest) (*sqlitev1.QueryJsonResponse, error) {
	var response *sqlitev1.QueryJsonResponse
	err := s.withSQLiteRetry(ctx, func(callCtx context.Context, trailer *metadata.MD) error {
		var err error
		response, err = s.client.QueryJson(callCtx, req, grpc.Trailer(trailer))
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// withSQLiteRetry wraps one sqlite RPC so retryable trailer-signaled errors are retried with a tiny exponential backoff inside the caller deadline.
// withSQLiteRetry 用于包裹单个 sqlite RPC，在调用方 deadline 内对 trailer 标记为可重试的错误做一个很小的指数退避重试。
func (s *Store) withSQLiteRetry(ctx context.Context, call func(context.Context, *metadata.MD) error) error {
	var lastErr error
	for attempt := 0; attempt < sqliteRetryMaxAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		trailer := metadata.MD{}
		err := call(callCtx, &trailer)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isSQLiteRetryableRPCError(err, trailer) || attempt == sqliteRetryMaxAttempts-1 {
			return lastErr
		}
		if err := waitSQLiteRetry(ctx, attempt); err != nil {
			return lastErr
		}
	}
	return lastErr
}

// isSQLiteRetryableRPCError checks the sqlite gateway retry trailer first, then falls back to the documented gRPC status codes used for SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA.
// isSQLiteRetryableRPCError 用于优先检查 sqlite 网关的重试 trailer，再回退到文档里为 SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA 定义的 gRPC 状态码。
func isSQLiteRetryableRPCError(err error, trailer metadata.MD) bool {
	if err == nil {
		return false
	}
	if values := trailer.Get("x-vldb-retryable"); len(values) > 0 && strings.EqualFold(strings.TrimSpace(values[0]), "true") {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.Aborted:
		lowered := strings.ToLower(strings.TrimSpace(err.Error()))
		return strings.Contains(lowered, "sqlite_busy") ||
			strings.Contains(lowered, "sqlite_locked") ||
			strings.Contains(lowered, "sqlite_schema")
	default:
		return false
	}
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

// versionRow mirrors the tiny schema-version query payload returned by the gateway.
// versionRow 用于映射网关返回的 schema 版本查询结果。
type versionRow struct {
	SchemaVersion int `json:"schema_version"`
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

// ReplaceNoiseEmbeddingCache rewrites one semantic prototype bundle atomically by deleting the old rows and batch-inserting the refreshed set.
// ReplaceNoiseEmbeddingCache 用于通过“先删后批量插入”原子重写一组语义原型缓存。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, `
DELETE FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash)); err != nil {
		return fmt.Errorf("clear noise embedding cache: %w", err)
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
		return fmt.Errorf("insert noise embedding cache rows: %w", err)
	}
	return nil
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
	session, err := s.ensureSession(ctx, strings.TrimSpace(sessionKey), user.ID, project)
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

	// Serialize turn writes so session counters and turn rows stay consistent inside one SQLite transaction boundary.
	// 串行化 turn 写入，确保 session 计数和 turn 行在同一个 SQLite 写入边界内保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_turn_records")
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("allocate turn record id: %w", err)
	}
	dehydratedContent, dehydratedBudget, err := buildDehydratedTurn(turn)
	if err != nil {
		return logicdomain.PersistedTurnRecord{}, err
	}
	nowMs := time.Now().UTC().UnixMilli()
	createdMs := nowMs
	if !turn.CreatedAt.IsZero() {
		createdMs = turn.CreatedAt.UTC().UnixMilli()
	}
	if err := s.exec(ctx, buildTurnInsertSQL(nextID, session.SessionID, session.ProjectID, dehydratedContent, dehydratedBudget, createdMs, nowMs)); err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("insert turn record: %w", err)
	}
	if err := s.exec(ctx, buildSessionTurnUpdateSQL(session.SessionID, dehydratedBudget, nowMs)); err != nil {
		return logicdomain.PersistedTurnRecord{}, fmt.Errorf("update session turn counters: %w", err)
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
		analysis.DetailsBudget = estimateTokenBudget(analysis.Details)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	memoryStartID := uint64(0)
	profileStartID := uint64(0)
	var err error
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

	// Resolve the vector ids for superseded memory rows before the status flip so the caller can delete those vector rows after SQL commits.
	// 在状态切换前先解析被覆盖记忆的 vector_id，供调用方在 SQL 提交后删除对应向量行。
	supersededMemoryIDs := normalizeUint64List(analysis.SupersededMemoryIDs)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load superseded vector ids: %w", err)
	}

	script := buildTurnAnalysisUpdateSQL(turn.ID, strings.TrimSpace(analysis.Details), analysis.DetailsBudget, nowMs)
	if analysis.UserProfileMerged {
		script += buildUserProfileUpdateSQL(session.UserID, analysis.MergedUserProfile, nowRFC3339)
	}
	if analysis.ProjectProfileMerged {
		script += buildProjectProfileUpdateSQL(session.ProjectID, analysis.MergedProjectProfile, nowRFC3339)
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
		contextEdges := normalizeTurnMemoryContextEdges(record.ID, node.ContextEdges, now)
		record.SupportCount, record.RebuttalCount = summarizeMemoryContextEdges(contextEdges)
		script += buildMemoryNodeInsertSQL(record)
		script += s.buildMemoryNodeFTSUpsertSQL(record)
		script += buildMemoryContextEdgesReplaceSQL(record.ID, contextEdges)
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
			node.ProfileDate = turn.CreatedAt.UTC().Format("2006-01-02")
		}
		script += buildProfileNodeInsertSQL(profileStartID+uint64(idx), uint64Ptr(turn.ID), node.ProfileType, bindID, node, nowMs)
	}
	if len(supersededMemoryIDs) > 0 {
		script += buildMemoryNodesSupersedeSQL(supersededMemoryIDs, nowMs)
		script += buildMemoryNodesFTSDeleteSQL(supersededMemoryIDs)
	}
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("apply turn analysis: %w", err)
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

// ListActiveProfileNodes returns only the current active nodes for one resolved target in a bounded, deterministic order.
// ListActiveProfileNodes 用于按确定性且受限的顺序返回某个已解析目标当前 active 的画像节点。
func (s *Store) ListActiveProfileNodes(ctx context.Context, target logicdomain.ProfileTargetRef, limit int) ([]logicdomain.ProfileNodeRecord, error) {
	if !logicdomain.ValidProfileType(target.ProfileType) {
		return nil, logicdomain.ValidationError{Field: "target", Message: "must be one supported profile target"}
	}
	if target.BindID == 0 {
		return nil, logicdomain.ValidationError{Field: "bind_id", Message: "must resolve to one persisted target"}
	}
	if limit <= 0 {
		limit = 128
	}
	if limit > 256 {
		limit = 256
	}
	nowMs := time.Now().UTC().UnixMilli()
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT id, turn_id, profile_type, bind_id, content, profile_status,
       priority, profile_level, level_reason, refresh_weight,
       source_kind, source_id, status_reason,
       expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE profile_type = ? AND bind_id = ? AND profile_status = ? AND (expires_timestamp <= 0 OR expires_timestamp > ?)
ORDER BY priority ASC, refresh_weight DESC, profile_date DESC, id ASC
LIMIT ?
`, target.ProfileType, target.BindID, logicdomain.ProfileStatusActive, nowMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query active profile nodes: %w", err)
	}
	out := make([]logicdomain.ProfileNodeRecord, 0, len(rows))
	for _, row := range rows {
		node := row.toDomain()
		out = append(out, logicdomain.ProfileNodeRecord{
			ID:             node.ID,
			TurnID:         node.TurnID,
			ProfileType:    node.ProfileType,
			BindID:         node.BindID,
			Content:        node.Content,
			Status:         node.Status,
			Priority:       node.Priority,
			ProfileLevel:   node.ProfileLevel,
			LevelReason:    node.LevelReason,
			RefreshWeight:  node.RefreshWeight,
			ProfileDate:    node.ProfileDate,
			SourceKind:     node.SourceKind,
			SourceID:       node.SourceID,
			StatusReason:   node.StatusReason,
			ExpiresAt:      node.ExpiresAt,
			SupersededByID: node.SupersededByID,
			CreatedAt:      node.CreatedAt,
			UpdatedAt:      node.UpdatedAt,
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
	rows, err := queryRows[profileInstructionRow](s, ctx, fmt.Sprintf(`
SELECT id, profile_type, bind_id, instruction, instruction_status,
       review_result_json, failure_reason, created_timestamp, updated_timestamp
FROM vmm_profile_instructions
WHERE id = %d
LIMIT 1
`, instructionID))
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
	rows, err := queryRows[profileNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, turn_id, profile_type, bind_id, content, profile_status,
       priority, profile_level, level_reason, refresh_weight,
       source_kind, source_id, status_reason,
       expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE id = %d
LIMIT 1
`, nodeID))
	if err != nil {
		return logicdomain.ProfileNodeRecord{}, false, err
	}
	if len(rows) == 0 {
		return logicdomain.ProfileNodeRecord{}, false, nil
	}
	return rows[0].toRecord(), true, nil
}

// loadProfileNodeStatusesByIDs fetches the lightweight retirement state for a small node-id set so uncertain supersede/retire updates can be reconciled.
// loadProfileNodeStatusesByIDs 用于读取一小批节点的轻量状态，让“不确定”的 supersede 或 retire 更新能够做状态对账。
func (s *Store) loadProfileNodeStatusesByIDs(ctx context.Context, nodeIDs []uint64) ([]profileNodeStatusRow, error) {
	nodeIDs = normalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	rows, err := queryRows[profileNodeStatusRow](s, ctx, fmt.Sprintf(`
SELECT id, profile_status, superseded_by_id
FROM vmm_profile_nodes
WHERE id IN (%s)
ORDER BY id ASC
`, sqlUint64List(nodeIDs)))
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// loadRenderedProfileByTarget reads the durable scope blob after one uncertain update so callers can verify whether the final profile text already committed.
// loadRenderedProfileByTarget 用于在 scope 画像更新不确定后读取长期 Blob，让调用方验证最终画像文本是否已经提交成功。
func (s *Store) loadRenderedProfileByTarget(ctx context.Context, profileType int, bindID uint64) (string, error) {
	query := ""
	switch profileType {
	case logicdomain.ProfileTypeUser:
		query = `SELECT profile FROM vmm_users WHERE id = ? LIMIT 1`
	case logicdomain.ProfileTypeTeam:
		query = `SELECT profile FROM vmm_teams WHERE id = ? LIMIT 1`
	case logicdomain.ProfileTypeSpace:
		query = `SELECT profile FROM vmm_spaces WHERE id = ? LIMIT 1`
	case logicdomain.ProfileTypeProject:
		query = `SELECT profile FROM vmm_projects WHERE id = ? LIMIT 1`
	default:
		return "", logicdomain.ValidationError{Field: "profile_type", Message: "must be one supported profile target"}
	}
	rows, err := queryRows[profileBlobRow](s, ctx, query, bindID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Profile, nil
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
	if len(rows) != len(normalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus != logicdomain.ProfileStatusSuperseded || row.SupersededByID != supersededByID {
			return false, nil
		}
	}
	return true, nil
}

// reconcileProfileNodesRetired verifies whether all target nodes already left the active set after one uncertain retire update.
// reconcileProfileNodesRetired 用于验证在一次不确定退役更新后，目标节点是否已经全部离开 active 集合。
func (s *Store) reconcileProfileNodesRetired(ctx context.Context, nodeIDs []uint64) (bool, error) {
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, nodeIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(normalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus == logicdomain.ProfileStatusActive {
			return false, nil
		}
	}
	return true, nil
}

// reconcileProfileInstructionApplied verifies whether the instruction row already moved to the applied state after one uncertain update.
// reconcileProfileInstructionApplied 用于验证一条 instruction 行在不确定更新后是否已经进入 applied 状态。
func (s *Store) reconcileProfileInstructionApplied(ctx context.Context, instructionID uint64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return stored.Status == logicdomain.ProfileInstructionStatusApplied, nil
}

// reconcileProfileInstructionFailed verifies whether the instruction row already moved to the failed state after one uncertain failure-mark update.
// reconcileProfileInstructionFailed 用于验证一条 instruction 行在不确定失败回写后是否已经进入 failed 状态。
func (s *Store) reconcileProfileInstructionFailed(ctx context.Context, instructionID uint64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return stored.Status == logicdomain.ProfileInstructionStatusFailed, nil
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
	if err := s.exec(ctx, fmt.Sprintf(`
INSERT INTO vmm_profile_instructions (
  id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_timestamp, updated_timestamp
) VALUES (%d, %d, %d, %s, %d, %s, %s, %d, %d);
`, nextID, record.ProfileType, record.BindID, sqlStringLiteral(strings.TrimSpace(record.Instruction)), record.Status, sqlStringLiteral(strings.TrimSpace(record.ReviewResult)), sqlStringLiteral(strings.TrimSpace(record.FailureReason)), nowMs, nowMs)); err != nil {
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
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_profile_instructions
SET instruction_status = %d,
    review_result_json = %s,
    failure_reason = %s,
    updated_timestamp = %d
WHERE id = %d;
`, logicdomain.ProfileInstructionStatusFailed, sqlStringLiteral(strings.TrimSpace(reviewResult)), sqlStringLiteral(strings.TrimSpace(failureReason)), nowMs, instructionID)); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionFailed(ctx, instructionID)
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
			node.ProfileDate = now.Format("2006-01-02")
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
		// Persist the new active node first so later retire/supersede updates can point at a durable replacement id.
		// 先持久化新的 active 节点，让后续退役或 supersede 更新能够引用已经存在的替代节点 id。
		if err := s.exec(ctx, buildProfileNodeInsertSQL(insertedID, nil, target.ProfileType, target.BindID, node, nowMs)); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, ok, reconcileErr := s.reconcileManualProfileNodeInsert(ctx, target, node, insertedID)
				if reconcileErr == nil && ok {
					acceptedRecord = recovered
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
				return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("insert manual profile node %d: %w", insertedID, err)
			}
		}
		if len(node.SupersedeNodeIDs) > 0 {
			// Retire the superseded nodes in a separate execute call because SQLite's shared-connection batch path
			// can enter a resource-deadlock state when one script both inserts and updates the same hot table.
			// 单独执行 supersede 更新，因为 SQLite 的共享连接批量路径在同一脚本里同时插入并更新热点表时，
			// 可能进入 resource deadlock 状态。
			if err := s.exec(ctx, buildProfileNodesSupersedeSQL(normalizeUint64List(node.SupersedeNodeIDs), insertedID, strings.TrimSpace(node.StatusReason), nowMs)); err != nil {
				if isSQLiteOutcomeUncertainError(err) {
					recovered, reconcileErr := s.reconcileProfileNodesSuperseded(ctx, node.SupersedeNodeIDs, insertedID)
					if reconcileErr == nil && recovered {
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
				return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("supersede manual profile nodes for %d: %w", insertedID, err)
			}
		}
		accepted = append(accepted, acceptedRecord)
	}
	for _, decision := range retired {
		if decision.NodeID == 0 {
			continue
		}
		// Apply standalone retire decisions after inserts so reviewer-directed removals stay explicit and debuggable.
		// 在插入完成后再应用独立退役决策，让评审器给出的移除动作保持明确且可调试。
		if err := s.exec(ctx, buildSingleProfileNodeRetireSQL(decision.NodeID, decision.Reason, nowMs)); err != nil {
			if isSQLiteOutcomeUncertainError(err) {
				recovered, reconcileErr := s.reconcileProfileNodesRetired(ctx, []uint64{decision.NodeID})
				if reconcileErr == nil && recovered {
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
			return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("retire manual profile node %d: %w", decision.NodeID, err)
		}
	}

	// Mark the instruction as applied only after every node mutation has succeeded, then refresh the rendered scope blob last.
	// 只有在全部节点变更都成功后，才把指令标记为 applied，并把 scope 画像 Blob 的回写放在最后。
	if err := s.exec(ctx, buildProfileInstructionAppliedSQL(instruction.ID, reviewResult, nowMs)); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileProfileInstructionApplied(ctx, instruction.ID)
			if reconcileErr == nil && recovered {
				goto updateRenderedProfile
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
		return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("mark manual profile instruction applied: %w", err)
	}
updateRenderedProfile:
	if err := s.exec(ctx, buildProfileTargetUpdateSQL(target.ProfileType, target.BindID, renderedProfile, nowRFC3339)); err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileRenderedProfileTarget(ctx, target.ProfileType, target.BindID, renderedProfile)
			if reconcileErr == nil && recovered {
				return logicdomain.ManualProfileInstructionApplyResult{
					InstructionID: instruction.ID,
					AcceptedNodes: accepted,
					RetiredNodes:  retired,
				}, nil
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
		return logicdomain.ManualProfileInstructionApplyResult{}, fmt.Errorf("update rendered manual profile target: %w", err)
	}
	return logicdomain.ManualProfileInstructionApplyResult{
		InstructionID: instruction.ID,
		AcceptedNodes: accepted,
		RetiredNodes:  retired,
	}, nil
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
	if err := s.exec(ctx, buildProfileNodesExpireSQL(expiredIDs, "expired by lifecycle convergence", nowMs)); err != nil {
		return nil, fmt.Errorf("mark expired profile nodes: %w", err)
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
	userIDs := sortedProfileBindingIDs(updates.UserProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, `
UPDATE vmm_users
SET profile = ?, updated_at = ?
WHERE id = ?
`, userIDs, updates.UserProfiles, nowRFC3339); err != nil {
		return fmt.Errorf("replace rendered user profiles: %w", err)
	}
	teamIDs := sortedProfileBindingIDs(updates.TeamProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, `
UPDATE vmm_teams
SET profile = ?, updated_at = ?
WHERE id = ?
`, teamIDs, updates.TeamProfiles, nowRFC3339); err != nil {
		return fmt.Errorf("replace rendered team profiles: %w", err)
	}
	spaceIDs := sortedProfileBindingIDs(updates.SpaceProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, `
UPDATE vmm_spaces
SET profile = ?, updated_at = ?
WHERE id = ?
`, spaceIDs, updates.SpaceProfiles, nowRFC3339); err != nil {
		return fmt.Errorf("replace rendered space profiles: %w", err)
	}
	projectIDs := sortedProfileBindingIDs(updates.ProjectProfiles)
	if err := s.replaceRenderedProfileBatch(ctx, `
UPDATE vmm_projects
SET profile = ?, updated_at = ?
WHERE id = ?
`, projectIDs, updates.ProjectProfiles, nowRFC3339); err != nil {
		return fmt.Errorf("replace rendered project profiles: %w", err)
	}
	return nil
}

// replaceRenderedProfileBatch reuses SQLite ExecuteBatch for one scope table so repeated profile-blob updates stay in the sqlite-native transport path.
// replaceRenderedProfileBatch 用于对单个 scope 表复用 SQLite ExecuteBatch，让重复的 profile Blob 更新保持在 sqlite 原生传输路径内。
func (s *Store) replaceRenderedProfileBatch(ctx context.Context, sql string, ids []uint64, profiles map[uint64]string, nowRFC3339 string) error {
	if len(ids) == 0 {
		return nil
	}
	items := make([][]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, []any{profiles[id], nowRFC3339, id})
	}
	return s.execBatch(ctx, sql, items)
}

// loadActiveProfileNodes is the shared query helper used by profile review and expiry convergence to fetch only currently renderable active nodes.
// loadActiveProfileNodes 用于作为画像评审和过期收敛共用查询助手，只加载当前仍可渲染的 active 节点。
func (s *Store) loadActiveProfileNodes(ctx context.Context, profileType int, bindID uint64, nowMs int64) ([]logicdomain.ProfileActiveNodeRecord, error) {
	rows, err := queryRows[profileNodeRow](s, ctx, `
SELECT id, turn_id, profile_type, bind_id, content, profile_status,
       priority, profile_level, level_reason, refresh_weight,
       source_kind, source_id, status_reason,
       expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE profile_type = ? AND bind_id = ? AND profile_status = ? AND (expires_timestamp <= 0 OR expires_timestamp > ?)
ORDER BY profile_date ASC, priority ASC, refresh_weight DESC, id ASC
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
	turnIDs = normalizeUint64List(turnIDs)
	if len(turnIDs) == 0 {
		return []logicdomain.SessionTurnRecord{}, nil
	}
	rows, err := queryRows[turnRecordRow](s, ctx, fmt.Sprintf(`
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id IN (%s)
ORDER BY id ASC
`, sqlUint64List(turnIDs)))
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
	turnIDs = normalizeUint64List(turnIDs)
	if len(turnIDs) == 0 || radius <= 0 {
		return map[uint64]logicdomain.TurnDetailWindow{}, nil
	}
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
 AND o.rn BETWEEN t.target_rn - %d AND t.target_rn + %d
 AND o.id <> t.target_id
ORDER BY t.target_id ASC, o.rn ASC
`, sqlUint64List(turnIDs), radius, radius))
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
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
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
`, sqlUint64List(memoryIDs)))
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
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryContextEdge{}, nil
	}
	rows, err := queryRows[memoryContextEdgeRow](s, ctx, fmt.Sprintf(`
SELECT memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE memory_id IN (%s)
ORDER BY memory_id ASC, context_key ASC, context_value ASC
`, sqlUint64List(memoryIDs)))
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
	vectorIDs = normalizeStringList(vectorIDs)
	if len(vectorIDs) == 0 {
		return []logicdomain.MemoryNodeRecord{}, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
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
`, sqlStringList(vectorIDs), buildActiveUnexpiredMemoryCondition("", nowMs)))
	if err != nil {
		return nil, fmt.Errorf("query memory nodes by vector ids: %w", err)
	}
	out := make([]logicdomain.MemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toMemoryNodeRecord())
	}
	return out, nil
}

// SearchLexicalMemory runs one SQLite FTS recall over durable memory text and returns ranked memory ids for later relational materialization plus RRF fusion.
// SearchLexicalMemory 用于在长期记忆文本上执行一次 SQLite FTS 召回，并返回后续回表与 RRF 融合所需的排序 memory id。
func (s *Store) SearchLexicalMemory(ctx context.Context, query string, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	query = s.lexicalTokenizerOrFallback().BuildSQLiteFTSMatchExpression(query)
	if query == "" || topK <= 0 {
		return []logicdomain.MemoryLexicalHit{}, nil
	}
	if topK > 32 {
		topK = 32
	}

	// Reuse the same project hierarchy filter as vector recall so hybrid fusion compares candidates from one consistent scope.
	// 复用与向量召回一致的项目层级过滤，确保混合融合比较的是同一作用域中的候选。
	sqlText := `
SELECT n.id AS memory_id, bm25(vmm_memory_nodes_fts, 2.0, 1.0) AS rank
FROM vmm_memory_nodes_fts
JOIN vmm_memory_nodes AS n ON n.id = vmm_memory_nodes_fts.rowid
WHERE vmm_memory_nodes_fts MATCH ?
`
	params := []any{query}
	if filter.TeamID > 0 {
		sqlText += `  AND n.team_id = ?` + "\n"
		params = append(params, filter.TeamID)
	}
	if filter.SpaceID > 0 {
		sqlText += `  AND n.space_id = ?` + "\n"
		params = append(params, filter.SpaceID)
	}
	if filter.ProjectID > 0 {
		sqlText += `  AND n.project_id = ?` + "\n"
		params = append(params, filter.ProjectID)
	}
	if filter.UserID > 0 {
		sqlText += `  AND (n.user_id = 0 OR n.user_id = ?)` + "\n"
		params = append(params, filter.UserID)
	}
	if filter.SessionID > 0 {
		sqlText += `  AND n.origin_session_id = ?` + "\n"
		params = append(params, filter.SessionID)
	}
	if filter.BoundarySessionID > 0 {
		if filter.ExcludeBoundaryTurn {
			sqlText += `  AND (n.origin_session_id != ? OR n.source_turn_id IS NULL OR n.source_turn_id = 0)` + "\n"
			params = append(params, filter.BoundarySessionID)
		} else {
			sqlText += `  AND (n.origin_session_id != ? OR n.source_turn_id IS NULL OR n.source_turn_id = 0 OR n.source_turn_id <= ?)` + "\n"
			params = append(params, filter.BoundarySessionID, filter.BoundaryMaxTurnID)
		}
	}
	sqlText += fmt.Sprintf("  AND %s\nORDER BY rank ASC, n.id ASC\nLIMIT ?\n", buildActiveUnexpiredMemoryCondition("n", time.Now().UTC().UnixMilli()))
	params = append(params, topK)

	rows, err := queryRows[memoryLexicalRow](s, ctx, sqlText, params...)
	if err != nil {
		return nil, fmt.Errorf("search lexical memory: %w", err)
	}
	hits := make([]logicdomain.MemoryLexicalHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, logicdomain.MemoryLexicalHit{
			MemoryID: row.MemoryID,
			Score:    -row.Rank,
		})
	}
	return hits, nil
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
	script := buildMemoryNodeInsertSQL(record) + s.buildMemoryNodeFTSUpsertSQL(record)
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("insert direct memory node: %w", err)
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
	supersededMemoryIDs = normalizeUint64List(supersededMemoryIDs)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("load superseded direct-write vector ids: %w", err)
	}

	// Keep the insert and the old-row status flip inside one serialized SQL script so direct writes cannot leave “new row inserted but old row still active” gaps behind.
	// 把插入新行和旧行状态切换放进同一段串行 SQL 脚本，避免主动写记忆留下“新行已插入但旧行仍 active”的缝隙。
	script := buildMemoryNodeInsertSQL(record) + s.buildMemoryNodeFTSUpsertSQL(record)
	if len(supersededMemoryIDs) > 0 {
		script += buildMemoryNodesSupersedeSQL(supersededMemoryIDs, nowMs)
		script += buildMemoryNodesFTSDeleteSQL(supersededMemoryIDs)
	}
	if err := s.exec(ctx, script); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("apply direct memory write: %w", err)
	}
	return logicdomain.DirectMemoryWriteApplyResult{
		InsertedMemoryNode:  record,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// LoadActiveSessionMemoryNodes returns the active unified memory rows anchored to one origin session so analyzers can reason about duplicates and supersedes.
// LoadActiveSessionMemoryNodes 用于返回绑定到同一个 origin session 的活跃统一记忆行，让分析器可以判断重复和覆盖关系。
func (s *Store) LoadActiveSessionMemoryNodes(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
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
  AND memory_status = ?
  AND (expires_timestamp <= 0 OR expires_timestamp > ?)
ORDER BY COALESCE(source_turn_id, 0) ASC, id ASC
`, session.SessionID, logicdomain.MemoryStatusActive, nowMs)
	if err != nil {
		return nil, fmt.Errorf("query active session memory nodes: %w", err)
	}
	nodes := make([]logicdomain.SessionMemoryNodeRecord, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, row.toDomain())
	}
	return nodes, nil
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
func (s *Store) ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error {
	if session.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil
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
		return fmt.Errorf("load memory adoption targets: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	script := ""
	for _, row := range rows {
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, adoptedAt) {
			continue
		}
		evolved := evolveAdoptedMemoryRecord(session, row, adoptedAt)
		script += buildMemoryAdoptionUpdateSQL(evolved)
	}
	if strings.TrimSpace(script) == "" {
		return nil
	}
	if err := s.exec(ctx, script); err != nil {
		return fmt.Errorf("apply memory adoption: %w", err)
	}
	return nil
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
	if err := s.exec(ctx, `
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
WHERE id = ?
`, observedMs, observedMs, completedMs, completedMs, completedMs, completedMs, sessionID); err != nil {
		return fmt.Errorf("advance session extract window: %w", err)
	}
	return nil
}

// loadActiveMemoryVectorIDs loads the vector ids of active unified memory rows by memory id so callers can clean those vector rows after a supersede update commits.
// loadActiveMemoryVectorIDs 用于按记忆 id 读取 active 统一记忆行的 vector_id，供调用方在 supersede 提交后清理对应向量行。
func (s *Store) loadActiveMemoryVectorIDs(ctx context.Context, memoryIDs []uint64) ([]string, error) {
	memoryIDs = normalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	rows, err := queryRows[obsoleteVectorRow](s, ctx, fmt.Sprintf(`
SELECT vector_id
FROM vmm_memory_nodes
WHERE memory_status = %d AND id IN (%s)
ORDER BY id ASC
`, logicdomain.MemoryStatusActive, sqlUint64List(memoryIDs)))
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

// ensureSession loads one existing session inside the target project or creates it under the resolved hierarchy when it does not exist yet.
// ensureSession 用于在目标项目内按外部 session_key 读取 session；如果还不存在，则在已解析层级下创建一条新 session。
func (s *Store) ensureSession(ctx context.Context, sessionKey string, userID uint64, project logicdomain.ProjectRecord) (logicdomain.SessionRecord, error) {
	rows, err := queryRows[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id, turn_count,
       last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE project_id = ? AND session_key = ?
LIMIT 1
`, project.ID, sessionKey)
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("query session: %w", err)
	}
	if len(rows) > 0 {
		// Reuse the project-scoped session row directly even when the upstream plugin has switched user ids during debugging.
		// 在调试阶段，即使上游插件手动切换了 user_id，也直接复用同一 project 下的 session 行。
		return rows[0].toDomain(), nil
	}

	// Auto-create the session row only after user and project are both confirmed to exist.
	// 只有在 user 和 project 都确认存在之后，才自动创建 session 行。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nextID, err := s.nextNumericID(ctx, "vmm_sessions")
	if err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("allocate session id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	if err := s.exec(ctx, `
INSERT INTO vmm_sessions (
  id, session_key, user_id, team_id, space_id, project_id,
  turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, '', 0, 0, 0, 0, ?, ?)
`, nextID, sessionKey, userID, project.TeamID, project.SpaceID, project.ID, nowMs, nowMs); err != nil {
		return logicdomain.SessionRecord{}, fmt.Errorf("insert session: %w", err)
	}
	return logicdomain.SessionRecord{
		ID:                     nextID,
		SessionKey:             sessionKey,
		UserID:                 userID,
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

// ResolveProjectRef resolves either a numeric project id or a canonical Team/Space/Project path.
// ResolveProjectRef 用于解析数字 project id，或标准的 Team/Space/Project 路径。
func (s *Store) ResolveProjectRef(ctx context.Context, projectRef string) (logicdomain.ProjectRecord, error) {
	projectRef = strings.TrimSpace(projectRef)
	if projectRef == "" {
		return logicdomain.ProjectRecord{}, logicdomain.ValidationError{Field: "project_ref", Message: "is required"}
	}
	if projectID, ok := parseUint64(projectRef); ok {
		return s.loadProjectByID(ctx, projectID)
	}
	teamName, spaceName, projectName, err := parseProjectPath(projectRef)
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
		out = append(out, logicdomain.MemoryRecord{
			ID:           record.VectorID,
			Text:         record.Abstract,
			Vector:       append([]float32(nil), record.Vector...),
			Filter:       filter,
			SourceTurnID: record.SourceTurnID,
			Metadata:     metadata,
			CreatedAt:    record.CreatedAt,
		})
	}
	return out, nil
}

// EnsureProjectPath resolves or creates a Team/Space/Project path according to the confirm flag rules required by the admin RPCs.
// EnsureProjectPath 用于按管理 RPC 约定的确认规则，解析或创建一个 Team/Space/Project 路径。
func (s *Store) EnsureProjectPath(ctx context.Context, projectPath string, confirmCreate bool) (logicdomain.ProjectMutationResult, error) {
	teamName, spaceName, projectName, err := parseProjectPath(projectPath)
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
			Message:      buildProjectConfirmMessage(teamName, spaceName, projectName, teamExists, spaceExists),
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
	if !teamExists {
		team, err = s.insertTeam(ctx, teamName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdTeam = true
	}
	if !spaceExists {
		space, err = s.insertSpace(ctx, team.ID, spaceName, now)
		if err != nil {
			return logicdomain.ProjectMutationResult{}, err
		}
		createdSpace = true
	}
	project, err := s.insertProject(ctx, team, space, projectName, now)
	if err != nil {
		return logicdomain.ProjectMutationResult{}, err
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
	if err := s.exec(ctx, deleteSQL, deleteParams...); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project profile nodes: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_memory_nodes WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project memory nodes: %w", err)
	}
	if err := s.exec(ctx, `
DELETE FROM vmm_scratchpad_nodes
WHERE plan_id IN (SELECT id FROM vmm_scratchpad_plans WHERE project_id = ?)
`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project scratchpad nodes: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_scratchpad_plans WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project scratchpad plans: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_turn_records WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project turn records: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_sessions WHERE project_id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project sessions: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_projects WHERE id = ?`, project.ID); err != nil {
		return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete project row: %w", err)
	}
	if deletePlan.DeletedSpaces > 0 {
		if err := s.exec(ctx, `DELETE FROM vmm_spaces WHERE id = ?`, project.SpaceID); err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete empty project space row: %w", err)
		}
	}
	if deletePlan.DeletedTeams > 0 {
		if err := s.exec(ctx, `DELETE FROM vmm_teams WHERE id = ?`, project.TeamID); err != nil {
			return logicdomain.ProjectDeleteResult{}, fmt.Errorf("delete empty project team row: %w", err)
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

// MigrateProjectPath moves SQL-backed sessions/turn records/memories from one project scope onto another when explicitly confirmed.
// MigrateProjectPath 用于在显式确认后，把 SQL 侧的 sessions/turn 记录/memories 从源项目范围迁移到目标项目范围。
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

	sessions, messages, memories, err := s.countProjectRows(ctx, source.ID)
	if err != nil {
		return logicdomain.ProjectMigrationResult{}, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	nowMs := time.Now().UTC().UnixMilli()
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_sessions
SET team_id = %d, space_id = %d, project_id = %d, updated_timestamp = %d
WHERE project_id = %d
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate sessions: %w", err)
	}
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_turn_records
SET project_id = %d, updated_timestamp = %d
WHERE project_id = %d
`, target.ID, nowMs, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate turn records: %w", err)
	}
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET team_id = %d, space_id = %d, project_id = %d, updated_timestamp = %d
WHERE project_id = %d
`, target.TeamID, target.SpaceID, target.ID, nowMs, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate memory nodes: %w", err)
	}
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET bind_id = %d
WHERE profile_type = %d AND bind_id = %d
`, target.ID, logicdomain.ProfileTypeProject, source.ID)); err != nil {
		return logicdomain.ProjectMigrationResult{}, fmt.Errorf("migrate project profile nodes: %w", err)
	}
	return logicdomain.ProjectMigrationResult{
		Source:           source,
		Target:           target,
		Message:          fmt.Sprintf("migrated project %s -> %s", source.Path(), target.Path()),
		MigratedSessions: sessions,
		MigratedMessages: messages,
		MigratedMemories: memories,
	}, nil
}

// ResolveUserRef resolves either a numeric user id or a unique user name.
// ResolveUserRef 用于解析数字 user id，或唯一用户名。
func (s *Store) ResolveUserRef(ctx context.Context, userRef string) (logicdomain.UserRecord, error) {
	userRef = strings.TrimSpace(userRef)
	if userRef == "" {
		return logicdomain.UserRecord{}, logicdomain.ValidationError{Field: "user_ref", Message: "is required"}
	}
	if userID, ok := parseUint64(userRef); ok {
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
	now := time.Now().UTC()
	nextID, err := s.nextNumericID(ctx, "vmm_users")
	if err != nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("allocate user id: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_users (id, name, delete_confirm_code, created_at, updated_at)
VALUES (?, ?, '', ?, ?)
`, nextID, userName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.UserResolveResult{}, fmt.Errorf("insert user: %w", err)
	}
	return logicdomain.UserResolveResult{
		User:    logicdomain.UserRecord{ID: nextID, Name: userName, CreatedAt: now, UpdatedAt: now},
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
	if err := s.exec(ctx, `DELETE FROM vmm_profile_nodes WHERE profile_type = ? AND bind_id = ?`, logicdomain.ProfileTypeUser, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user profile nodes by bind: %w", err)
	}
	// Detach surviving shared-scope profile facts from the user's historical turns before those turn rows disappear.
	// These nodes must keep living under project/team/space scope, but they can no longer point at deleted turn ids.
	// 在删除用户历史 turn 前，先把仍应保留的共享范围画像节点与原 turn 脱钩。
	// 这些节点继续归属于 project/team/space，但不能再指向即将被删除的 turn id。
	if err := s.exec(ctx, `
UPDATE vmm_profile_nodes
SET turn_id = NULL,
    source_kind = ?,
    source_id = ?,
    status_reason = ?,
    updated_timestamp = ?
WHERE profile_type <> ? AND turn_id IN (
  SELECT tr.id
  FROM vmm_turn_records tr
  JOIN vmm_sessions s ON s.id = tr.session_id
  WHERE s.user_id = ?
)`, logicdomain.ProfileSourceKindRetainedAfterUserDelete, currentUser.ID, "source user deleted; shared scope node retained without original turn binding", nowMs, logicdomain.ProfileTypeUser, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("detach surviving shared profile nodes from deleted user turns: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_memory_nodes WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user memory nodes: %w", err)
	}
	if err := s.exec(ctx, `
DELETE FROM vmm_scratchpad_nodes
WHERE plan_id IN (SELECT id FROM vmm_scratchpad_plans WHERE user_id = ?)
`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user scratchpad nodes: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_scratchpad_plans WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user scratchpad plans: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_turn_records WHERE session_id IN (SELECT id FROM vmm_sessions WHERE user_id = ?)`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user turn records: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_sessions WHERE user_id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user sessions: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_users WHERE id = ?`, currentUser.ID); err != nil {
		return logicdomain.UserDeleteResult{}, fmt.Errorf("delete user row: %w", err)
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
		if err := s.exec(ctx, buildUserDeleteConfirmationSQL(currentUser.ID, code, time.Now().UTC().Format(time.RFC3339Nano))); err != nil {
			return logicdomain.UserDeleteResult{}, fmt.Errorf("persist user delete confirmation code: %w", err)
		}
		currentUser.DeleteConfirmCode = code
	}
	return logicdomain.UserDeleteResult{
		User:                 currentUser,
		Message:              fmt.Sprintf("confirm deletion of user %s with the provided confirmation code", currentUser.Name),
		RequiresConfirmation: true,
		ConfirmationCode:     code,
	}, nil
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

// insertTeam creates one missing team node under the write lock.
// insertTeam 用于在写锁保护下创建缺失的 team 节点。
func (s *Store) insertTeam(ctx context.Context, teamName string, now time.Time) (logicdomain.TeamRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_teams")
	if err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("allocate team id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_teams (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, nextID, teamName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.TeamRecord{}, fmt.Errorf("insert team: %w", err)
	}
	return logicdomain.TeamRecord{ID: nextID, Name: teamName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertSpace creates one missing space node under an existing team.
// insertSpace 用于在已存在的 team 下创建缺失的 space 节点。
func (s *Store) insertSpace(ctx context.Context, teamID uint64, spaceName string, now time.Time) (logicdomain.SpaceRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_spaces")
	if err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("allocate space id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_spaces (id, team_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, nextID, teamID, spaceName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.SpaceRecord{}, fmt.Errorf("insert space: %w", err)
	}
	return logicdomain.SpaceRecord{ID: nextID, TeamID: teamID, Name: spaceName, CreatedAt: now, UpdatedAt: now}, nil
}

// insertProject creates one missing project node under the resolved Team/Space hierarchy.
// insertProject 用于在已解析的 Team/Space 层级下创建缺失的 project 节点。
func (s *Store) insertProject(ctx context.Context, team logicdomain.TeamRecord, space logicdomain.SpaceRecord, projectName string, now time.Time) (logicdomain.ProjectRecord, error) {
	nextID, err := s.nextNumericID(ctx, "vmm_projects")
	if err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("allocate project id: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_projects (id, team_id, space_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, nextID, team.ID, space.ID, projectName, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return logicdomain.ProjectRecord{}, fmt.Errorf("insert project: %w", err)
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

// userDeletePlan records the exact row categories that one user deletion will remove.
// userDeletePlan 用于记录一次用户删除将真正移除的行类别。
type userDeletePlan struct {
	DeletedUsers    int
	DeletedSessions int
	DeletedMessages int
	DeletedMemories int
	DeletedProfiles int
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
	return userDeletePlan{
		DeletedUsers:    1,
		DeletedSessions: sessions,
		DeletedMessages: messages,
		DeletedMemories: memories,
		DeletedProfiles: deletedProfiles,
	}, nil
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

// buildProjectConfirmMessage generates the stable confirmation text returned when missing Team/Space nodes require explicit confirmation.
// buildProjectConfirmMessage 用于生成稳定的确认提示文本，说明缺失 Team/Space 节点需要显式确认。
func buildProjectConfirmMessage(teamName, spaceName, projectName string, teamExists, spaceExists bool) string {
	missing := make([]string, 0, 2)
	if !teamExists {
		missing = append(missing, "team")
	}
	if !spaceExists {
		missing = append(missing, "space")
	}
	return fmt.Sprintf("path %s/%s/%s is incomplete; missing %s, use confirm_create=1 to create them", teamName, spaceName, projectName, strings.Join(missing, ", "))
}

// parseProjectPath enforces the canonical Team/Space/Project path format required by the admin RPCs.
// parseProjectPath 用于强制要求管理 RPC 使用标准的 Team/Space/Project 路径格式。
func parseProjectPath(path string) (string, string, string, error) {
	parts := strings.Split(strings.TrimSpace(path), "/")
	if len(parts) != 3 {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	teamName := strings.TrimSpace(parts[0])
	spaceName := strings.TrimSpace(parts[1])
	projectName := strings.TrimSpace(parts[2])
	if teamName == "" || spaceName == "" || projectName == "" {
		return "", "", "", logicdomain.ValidationError{Field: "project_path", Message: "must be TeamName/SpaceName/ProjectName"}
	}
	return teamName, spaceName, projectName, nil
}

// parseUint64 parses one numeric identifier string and reports whether the conversion succeeded.
// parseUint64 用于解析数字标识字符串，并返回转换是否成功。
func parseUint64(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
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

// estimateTokenBudget applies the local domestic estimator to one text blob so turn summaries and extracted payloads share one stable budget heuristic.
// estimateTokenBudget 用于对文本载荷应用本地估算器，让 turn 总结和提炼结果共享同一套稳定的 token 预算口径。
func estimateTokenBudget(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return estimator.Estimate(text)
}

// dehydratedTurnPayload mirrors the JSON document persisted into vmm_turn_records after one cleaned turn is flattened into a stable analysis unit.
// dehydratedTurnPayload 用于映射写入 vmm_turn_records 的 JSON 文档，此时一条清洗后的 turn 已经被压平成稳定分析单元。
type dehydratedTurnPayload struct {
	User      string                       `json:"user"`
	Timeline  []dehydratedTurnTimelineItem `json:"timeline"`
	Assistant string                       `json:"assistant"`
}

// dehydratedTurnTimelineItem stores one middle timeline node inside the dehydrated turn JSON payload.
// dehydratedTurnTimelineItem 用于保存脱水 turn JSON 载荷中的一条中间 timeline 节点。
type dehydratedTurnTimelineItem struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// buildDehydratedTurn converts one cleaned turn into the persisted JSON payload and estimates its token budget.
// buildDehydratedTurn 用于把一条清洗后的 turn 转换成持久化 JSON 载荷，并估算对应的 token 预算。
func buildDehydratedTurn(turn logicdomain.TurnRecord) (string, int, error) {
	// Preserve the storage-ready timeline exactly as the upstream sanitizer produced it because media/noise filtering already happened earlier.
	// 原样保留上游清洗后的 timeline 文本，因为媒体与噪声过滤已经在更前面的阶段完成。
	timeline := make([]dehydratedTurnTimelineItem, 0, len(turn.Timeline))
	for _, item := range turn.Timeline {
		timeline = append(timeline, dehydratedTurnTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: strings.TrimSpace(item.Content),
		})
	}
	payload := dehydratedTurnPayload{
		User:      strings.TrimSpace(turn.UserContent),
		Timeline:  timeline,
		Assistant: strings.TrimSpace(turn.AssistantContent),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("marshal dehydrated turn: %w", err)
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return string(body), estimator.Estimate(string(body)), nil
}

// buildTurnInsertSQL renders one raw INSERT statement so the gateway avoids the buggy optional-pointer update path seen during debug runs.
// buildTurnInsertSQL 用于渲染原始 INSERT 语句，让网关绕开调试阶段已出现过的 optional-pointer 更新故障路径。
// The dehydrated payload stays as JSON text on purpose so OSS-local boot no longer depends on SQLite's json extension.
// 脱水载荷会刻意以 JSON 文本形式保存，避免 OSS 本地版启动继续依赖 SQLite 的 json 扩展。
func buildTurnInsertSQL(id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) string {
	return fmt.Sprintf(`
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, created_timestamp, updated_timestamp
) VALUES (%d, %d, %d, %s, %d, 0, %d, %d)
`, id, sessionID, projectID, sqlStringLiteral(dehydratedContent), dehydratedBudget, createdMs, updatedMs)
}

// buildSessionTurnUpdateSQL renders one raw UPDATE that increments the turn counter and the unsummarized token budget after a turn row is inserted successfully.
// buildSessionTurnUpdateSQL 用于渲染原始 UPDATE，在 turn 行成功插入后同步递增 turn 计数和未总结 token 预算。
func buildSessionTurnUpdateSQL(sessionID uint64, addedBudget int, updatedMs int64) string {
	return fmt.Sprintf(`
UPDATE vmm_sessions
SET turn_count = turn_count + 1, summarize_budget = summarize_budget + %d, updated_timestamp = %d
WHERE id = %d
`, addedBudget, updatedMs, sessionID)
}

// buildTurnAnalysisUpdateSQL renders the raw UPDATE used to store the extracted turn summary and mark the turn as processed.
// buildTurnAnalysisUpdateSQL 用于渲染原始 UPDATE 语句，把提炼出的 turn 总结写回并标记该 turn 已处理。
func buildTurnAnalysisUpdateSQL(turnID uint64, details string, detailsBudget int, updatedMs int64) string {
	return fmt.Sprintf(`
UPDATE vmm_turn_records
SET details = %s, details_budget = %d, extracted_status = %d, updated_timestamp = %d
WHERE id = %d;
`, sqlStringLiteral(details), detailsBudget, logicdomain.TurnExtractedStatusDone, updatedMs, turnID)
}

// buildMemoryNodeInsertSQL renders the raw INSERT used for one unified durable memory row so turn extraction and direct-write paths share the same relational schema.
// buildMemoryNodeInsertSQL 用于渲染统一长期记忆行的原始 INSERT 语句，让 turn 提炼和主动写入共用同一套关系表结构。
func buildMemoryNodeInsertSQL(record logicdomain.MemoryNodeRecord) string {
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
	vectorJSON := encodeFloat32Slice(record.Vector)
	return fmt.Sprintf(`
INSERT INTO vmm_memory_nodes (
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
  vector_id, vector_json, source_kind, scope_level, category, abstract, details,
  memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
  expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
  recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_timestamp, updated_timestamp
) VALUES (%d, %d, %d, %d, %d, %d, %s, %s, %s, %d, %d, %d, %s, %s, %d, %d, %d, %d, %d, %d, %s, %d, %d, %d, %d, %d, %d, %d, %d, %d, %s, %d, %d);
`, record.ID, record.TeamID, record.SpaceID, record.ProjectID, record.UserID, record.OriginSessionID, sqlNullableUint64(nullableUint64(record.SourceTurnID)),
		sqlStringLiteral(strings.TrimSpace(record.VectorID)), sqlStringLiteral(vectorJSON), record.SourceKind, record.ScopeLevel, record.Category,
		sqlStringLiteral(strings.TrimSpace(record.Abstract)), sqlStringLiteral(strings.TrimSpace(record.Details)),
		record.Status, record.Priority, record.MemoryLevel, record.RefreshWeight, record.SupportCount, record.RebuttalCount, sqlStringLiteral(strings.TrimSpace(record.StatusReason)),
		expiresMs, lastRecalledMs, lastAdoptedMs, lastReinforcedMs, record.RecalledCount, record.AdoptedCount, record.ReinforcementCount, record.CrossSessionAdoptedCount,
		boolToSQLiteInt(record.DecayDisabled), sqlStringLiteral(strings.TrimSpace(record.DedupeHash)), record.CreatedAt.UTC().UnixMilli(), record.UpdatedAt.UTC().UnixMilli())
}

// buildMemoryNodeFTSUpsertSQL mirrors one durable memory row into the SQLite FTS table and pre-tokenizes Chinese text before it reaches unicode61.
// buildMemoryNodeFTSUpsertSQL 用于把长期记忆行同步镜像到 SQLite FTS 表，并在写入 unicode61 之前先完成中文预分词。
func (s *Store) buildMemoryNodeFTSUpsertSQL(record logicdomain.MemoryNodeRecord) string {
	tokenizer := s.lexicalTokenizerOrFallback()
	return fmt.Sprintf(`
INSERT OR REPLACE INTO vmm_memory_nodes_fts (rowid, memory_id, abstract, details)
VALUES (%d, %d, %s, %s);
`, record.ID, record.ID, sqlStringLiteral(tokenizer.BuildSQLiteFTSIndexText(record.Abstract)), sqlStringLiteral(tokenizer.BuildSQLiteFTSIndexText(record.Details)))
}

// buildMemoryNodesFTSDeleteSQL removes superseded durable rows from the SQLite FTS mirror so lexical recall does not waste work on dead memories.
// buildMemoryNodesFTSDeleteSQL 用于把已 superseded 的长期行从 SQLite FTS 镜像中删除，避免 lexical 召回继续在失效记忆上浪费开销。
func buildMemoryNodesFTSDeleteSQL(memoryIDs []uint64) string {
	if len(memoryIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`
DELETE FROM vmm_memory_nodes_fts
WHERE rowid IN (%s);
`, sqlUint64List(memoryIDs))
}

// buildMemoryContextEdgesReplaceSQL rewrites one memory row's contextual evidence edges in one deterministic script so later contextual retrieval can trust relational support/rebuttal stats.
// buildMemoryContextEdgesReplaceSQL 用于以确定性脚本重写某条记忆的情境证据边，让后续情境检索能够信赖关系层的支持/反驳统计。
func buildMemoryContextEdgesReplaceSQL(memoryID uint64, edges []logicdomain.MemoryContextEdge) string {
	if memoryID == 0 {
		return ""
	}
	script := fmt.Sprintf(`
DELETE FROM vmm_memory_context_edges
WHERE memory_id = %d;
`, memoryID)
	for _, edge := range edges {
		if edge.MemoryID == 0 || strings.TrimSpace(edge.ContextKey) == "" || strings.TrimSpace(edge.ContextValue) == "" {
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
		script += fmt.Sprintf(`
INSERT INTO vmm_memory_context_edges (
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
) VALUES (%d, %s, %s, %d, %d, %d, %d, %d, %d);
`, edge.MemoryID, sqlStringLiteral(strings.TrimSpace(edge.ContextKey)), sqlStringLiteral(strings.TrimSpace(edge.ContextValue)),
			edge.SupportCount, edge.RebuttalCount, lastSupportedMs, lastRebuttedMs, edge.CreatedAt.UTC().UnixMilli(), edge.UpdatedAt.UTC().UnixMilli())
	}
	return script
}

// buildMemoryAdoptionUpdateSQL renders one raw UPDATE for a memory row that has just been adopted by pre-check.
// buildMemoryAdoptionUpdateSQL 用于渲染一条刚被 pre-check 采纳的记忆行更新语句。
func buildMemoryAdoptionUpdateSQL(record logicdomain.MemoryNodeRecord) string {
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
	return fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET scope_level = %d,
    memory_status = %d,
    memory_level = %d,
    refresh_weight = %d,
    expires_timestamp = %d,
    last_recalled_timestamp = %d,
    last_adopted_timestamp = %d,
    last_reinforced_timestamp = %d,
    recalled_count = %d,
    adopted_count = %d,
    reinforcement_count = %d,
    cross_session_adopted_count = %d,
    decay_disabled = %d,
    updated_timestamp = %d
WHERE id = %d;
`, record.ScopeLevel, record.Status, record.MemoryLevel, record.RefreshWeight, expiresMs, lastRecalledMs, lastAdoptedMs, lastReinforcedMs,
		record.RecalledCount, record.AdoptedCount, record.ReinforcementCount, record.CrossSessionAdoptedCount, boolToSQLiteInt(record.DecayDisabled), record.UpdatedAt.UTC().UnixMilli(), record.ID)
}

// buildProfileNodeInsertSQL renders the raw INSERT used for one extracted profile node together with its lifecycle metadata and final status.
// buildProfileNodeInsertSQL 用于渲染单条画像节点的原始 INSERT 语句，并携带生命周期元数据和最终状态。
func buildProfileNodeInsertSQL(id uint64, turnID *uint64, profileType int, bindID uint64, node logicdomain.ProfileNodeCandidate, createdMs int64) string {
	expiresMs := int64(0)
	if !node.ExpiresAt.IsZero() {
		expiresMs = node.ExpiresAt.UTC().UnixMilli()
	}
	return fmt.Sprintf(`
INSERT INTO vmm_profile_nodes (
  id, turn_id, profile_type, bind_id, content, profile_status,
  priority, profile_level, level_reason, refresh_weight, source_kind, source_id, status_reason, expires_timestamp,
  superseded_by_id, profile_date, created_timestamp, updated_timestamp
) VALUES (%d, %s, %d, %d, %s, %d, %d, %d, %s, %d, %d, %d, %s, %d, 0, %s, %d, %d);
`, id, sqlNullableUint64(turnID), profileType, bindID, sqlStringLiteral(strings.TrimSpace(node.Content)), node.Status, node.Priority, node.ProfileLevel, sqlStringLiteral(strings.TrimSpace(node.LevelReason)), node.RefreshWeight, node.SourceKind, node.SourceID, sqlStringLiteral(strings.TrimSpace(node.StatusReason)), expiresMs, sqlStringLiteral(strings.TrimSpace(node.ProfileDate)), createdMs, createdMs)
}

// uint64Ptr returns one pointer for SQL builders that need an explicit numeric binding while still allowing nil to mean "no turn source".
// uint64Ptr 用于为 SQL 构造器返回一个数字指针，让调用方在需要显式绑定时传值，同时保留 nil 表示“没有 turn 来源”。
func uint64Ptr(value uint64) *uint64 {
	return &value
}

// sqlNullableUint64 renders either one integer literal or NULL so manual profile nodes can stay semantically unbound to any conversational turn.
// sqlNullableUint64 用于渲染整数文本或 NULL，让手工画像节点在语义上真正不绑定任何对话 turn。
func sqlNullableUint64(value *uint64) string {
	if value == nil {
		return "NULL"
	}
	return strconv.FormatUint(*value, 10)
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

// buildUserProfileUpdateSQL renders the raw UPDATE used to replace the durable user profile blob once the merge scene has accepted the latest evidence.
// buildUserProfileUpdateSQL 用于渲染原始 UPDATE 语句，在合并场景接纳最新证据后替换长期用户画像 Blob。
func buildUserProfileUpdateSQL(userID uint64, profile, updatedAt string) string {
	return fmt.Sprintf(`
UPDATE vmm_users
SET profile = %s, updated_at = %s
WHERE id = %d;
`, sqlStringLiteral(strings.TrimSpace(profile)), sqlStringLiteral(strings.TrimSpace(updatedAt)), userID)
}

// buildTeamProfileUpdateSQL renders the raw UPDATE used to replace the durable team profile blob after lifecycle convergence or manual edits.
// buildTeamProfileUpdateSQL 用于渲染原始 UPDATE 语句，在生命周期收敛或手工修改后替换长期 team 画像 Blob。
func buildTeamProfileUpdateSQL(teamID uint64, profile, updatedAt string) string {
	return fmt.Sprintf(`
UPDATE vmm_teams
SET profile = %s, updated_at = %s
WHERE id = %d;
`, sqlStringLiteral(strings.TrimSpace(profile)), sqlStringLiteral(strings.TrimSpace(updatedAt)), teamID)
}

// buildSpaceProfileUpdateSQL renders the raw UPDATE used to replace the durable space profile blob after lifecycle convergence or manual edits.
// buildSpaceProfileUpdateSQL 用于渲染原始 UPDATE 语句，在生命周期收敛或手工修改后替换长期 space 画像 Blob。
func buildSpaceProfileUpdateSQL(spaceID uint64, profile, updatedAt string) string {
	return fmt.Sprintf(`
UPDATE vmm_spaces
SET profile = %s, updated_at = %s
WHERE id = %d;
`, sqlStringLiteral(strings.TrimSpace(profile)), sqlStringLiteral(strings.TrimSpace(updatedAt)), spaceID)
}

// buildProjectProfileUpdateSQL renders the raw UPDATE used to replace the durable project profile blob once the merge scene has accepted the latest evidence.
// buildProjectProfileUpdateSQL 用于渲染原始 UPDATE 语句，在合并场景接纳最新证据后替换长期项目画像 Blob。
func buildProjectProfileUpdateSQL(projectID uint64, profile, updatedAt string) string {
	return fmt.Sprintf(`
UPDATE vmm_projects
SET profile = %s, updated_at = %s
WHERE id = %d;
`, sqlStringLiteral(strings.TrimSpace(profile)), sqlStringLiteral(strings.TrimSpace(updatedAt)), projectID)
}

// buildUserDeleteConfirmationSQL renders the guarded UPDATE used by the first phase of user deletion so duplicate confirmation requests reuse one stable code instead of racing on the same row.
// buildUserDeleteConfirmationSQL 用于渲染用户删除第一阶段的受保护 UPDATE，让重复确认请求复用同一确认码，而不是在同一行上并发竞争。
func buildUserDeleteConfirmationSQL(userID uint64, confirmationCode, updatedAt string) string {
	return fmt.Sprintf(`
UPDATE vmm_users
SET delete_confirm_code = %s, updated_at = %s
WHERE id = %d AND delete_confirm_code = '';
`, sqlStringLiteral(strings.TrimSpace(confirmationCode)), sqlStringLiteral(strings.TrimSpace(updatedAt)), userID)
}

// buildProfileNodesSupersedeSQL renders the raw UPDATE used to retire older profile nodes once a fresher node has replaced them.
// buildProfileNodesSupersedeSQL 用于渲染原始 UPDATE 语句，在较新的画像节点替代旧节点后把旧节点标成 superseded。
func buildProfileNodesSupersedeSQL(nodeIDs []uint64, supersededByID uint64, statusReason string, updatedMs int64) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = %d, superseded_by_id = %d, status_reason = %s, updated_timestamp = %d
WHERE profile_status = %d AND id IN (%s);
`, logicdomain.ProfileStatusSuperseded, supersededByID, sqlStringLiteral(strings.TrimSpace(statusReason)), updatedMs, logicdomain.ProfileStatusActive, sqlUint64List(nodeIDs))
}

// buildProfileNodesRetireSQL renders the raw UPDATE used to retire active profile nodes without attaching them to one newly inserted replacement node.
// buildProfileNodesRetireSQL 用于渲染原始 UPDATE 语句，在没有新替代节点时把活跃画像节点直接退役。
func buildProfileNodesRetireSQL(nodeIDs []uint64, statusReason string, updatedMs int64) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = %d, status_reason = %s, updated_timestamp = %d
WHERE profile_status = %d AND id IN (%s);
`, logicdomain.ProfileStatusSuperseded, sqlStringLiteral(strings.TrimSpace(statusReason)), updatedMs, logicdomain.ProfileStatusActive, sqlUint64List(nodeIDs))
}

// buildProfileNodesExpireSQL renders the raw UPDATE used by the periodic lifecycle convergence path to mark due active profile nodes as expired.
// buildProfileNodesExpireSQL 用于渲染周期性生命周期收敛路径使用的原始 UPDATE 语句，把已到期的 active 画像节点标记为 expired。
func buildProfileNodesExpireSQL(nodeIDs []uint64, statusReason string, updatedMs int64) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = %d, status_reason = %s, updated_timestamp = %d
WHERE profile_status = %d AND id IN (%s);
`, logicdomain.ProfileStatusExpired, sqlStringLiteral(strings.TrimSpace(statusReason)), updatedMs, logicdomain.ProfileStatusActive, sqlUint64List(nodeIDs))
}

// buildSingleProfileNodeRetireSQL renders the raw UPDATE used by manual profile instructions to retire one active node together with the explicit reviewer reason.
// buildSingleProfileNodeRetireSQL 用于渲染手工画像指令退役单条 active 节点的原始 UPDATE 语句，并写入评审给出的明确原因。
func buildSingleProfileNodeRetireSQL(nodeID uint64, statusReason string, updatedMs int64) string {
	if nodeID == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = %d, status_reason = %s, updated_timestamp = %d
WHERE profile_status = %d AND id = %d;
`, logicdomain.ProfileStatusSuperseded, sqlStringLiteral(strings.TrimSpace(statusReason)), updatedMs, logicdomain.ProfileStatusActive, nodeID)
}

// buildProfileInstructionAppliedSQL renders the raw UPDATE used to mark one manual profile instruction as applied after all reviewed node changes have been persisted.
// buildProfileInstructionAppliedSQL 用于渲染原始 UPDATE 语句，在评审后的节点变更全部落库后把手工画像指令标记为 applied。
func buildProfileInstructionAppliedSQL(instructionID uint64, reviewResult string, updatedMs int64) string {
	if instructionID == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_profile_instructions
SET instruction_status = %d,
    review_result_json = %s,
    failure_reason = '',
    updated_timestamp = %d
WHERE id = %d;
`, logicdomain.ProfileInstructionStatusApplied, sqlStringLiteral(strings.TrimSpace(reviewResult)), updatedMs, instructionID)
}

// buildProfileTargetUpdateSQL renders the raw UPDATE used to replace one durable scope profile blob after manual instruction review or lifecycle convergence.
// buildProfileTargetUpdateSQL 用于渲染原始 UPDATE 语句，在手工画像评审或生命周期收敛后替换某个 scope 的长期画像 Blob。
func buildProfileTargetUpdateSQL(profileType int, bindID uint64, profile, updatedAt string) string {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return buildUserProfileUpdateSQL(bindID, profile, updatedAt)
	case logicdomain.ProfileTypeTeam:
		return buildTeamProfileUpdateSQL(bindID, profile, updatedAt)
	case logicdomain.ProfileTypeSpace:
		return buildSpaceProfileUpdateSQL(bindID, profile, updatedAt)
	case logicdomain.ProfileTypeProject:
		return buildProjectProfileUpdateSQL(bindID, profile, updatedAt)
	default:
		return ""
	}
}

// buildMemoryNodesSupersedeSQL renders the raw UPDATE used to mark obsolete active memory rows as superseded by id.
// buildMemoryNodesSupersedeSQL 用于渲染原始 UPDATE 语句，按记忆 id 把过时的 active 记忆行标记为 superseded。
func buildMemoryNodesSupersedeSQL(memoryIDs []uint64, updatedMs int64) string {
	if len(memoryIDs) == 0 {
		return ""
	}
	return fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET memory_status = %d, updated_timestamp = %d
WHERE memory_status = %d AND id IN (%s);
`, logicdomain.MemoryStatusSuperseded, updatedMs, logicdomain.MemoryStatusActive, sqlUint64List(memoryIDs))
}

// normalizeTurnMemoryContextEdges aggregates one extracted candidate's situational evidence into durable per-context counters so later retrieval can reason over explicit support/rebuttal traces.
// normalizeTurnMemoryContextEdges 用于把一条提炼候选上的情境证据聚合成长期的逐情境计数，让后续检索能够基于显式支持/反驳轨迹推理。
func normalizeTurnMemoryContextEdges(memoryID uint64, candidates []logicdomain.MemoryContextEdgeCandidate, now time.Time) []logicdomain.MemoryContextEdge {
	if memoryID == 0 || len(candidates) == 0 {
		return nil
	}
	aggregated := make(map[string]logicdomain.MemoryContextEdge, len(candidates))
	for _, candidate := range candidates {
		contextKey := logicdomain.NormalizeMemoryContextKey(candidate.ContextKey)
		contextValue := logicdomain.NormalizeMemoryContextValue(candidate.ContextValue)
		relation := strings.TrimSpace(candidate.Relation)
		if contextKey == "" || contextValue == "" || !logicdomain.ValidMemoryContextRelation(relation) {
			continue
		}
		key := contextKey + "|" + contextValue
		edge := aggregated[key]
		if edge.MemoryID == 0 {
			edge = logicdomain.MemoryContextEdge{
				MemoryID:     memoryID,
				ContextKey:   contextKey,
				ContextValue: contextValue,
				CreatedAt:    now.UTC(),
				UpdatedAt:    now.UTC(),
			}
		}
		switch relation {
		case logicdomain.MemoryContextRelationRebuttal:
			edge.RebuttalCount++
			edge.LastRebuttedAt = now.UTC()
		default:
			edge.SupportCount++
			edge.LastSupportedAt = now.UTC()
		}
		edge.UpdatedAt = now.UTC()
		aggregated[key] = edge
	}
	if len(aggregated) == 0 {
		return nil
	}
	keys := make([]string, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	edges := make([]logicdomain.MemoryContextEdge, 0, len(keys))
	for _, key := range keys {
		edges = append(edges, aggregated[key])
	}
	return edges
}

// summarizeMemoryContextEdges folds one edge slice into memory-level support/rebuttal totals so the main memory row can expose quick ranking signals without joining the edge table.
// summarizeMemoryContextEdges 用于把情境边切片折叠成记忆级 support/rebuttal 总数，让主记忆行无需 join 边表也能暴露快速排序信号。
func summarizeMemoryContextEdges(edges []logicdomain.MemoryContextEdge) (int, int) {
	supportCount := 0
	rebuttalCount := 0
	for _, edge := range edges {
		supportCount += edge.SupportCount
		rebuttalCount += edge.RebuttalCount
	}
	return supportCount, rebuttalCount
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

// nullableUint64 returns a pointer only when the value is non-zero, so SQL builders can emit NULL for optional ids.
// nullableUint64 用于仅在数值非零时返回指针，让 SQL 构造器可以为可空 id 输出 NULL。
func nullableUint64(value uint64) *uint64 {
	if value == 0 {
		return nil
	}
	return &value
}

// normalizeUint64List removes zeros and duplicates from generic uint64 id lists while keeping a deterministic ascending order.
// normalizeUint64List 用于从通用 uint64 id 列表中去掉零值和重复项，并保持确定性的升序。
func normalizeUint64List(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

// normalizeStringList removes blanks and duplicates from generic string id lists while keeping deterministic ascending order.
// normalizeStringList 用于从通用字符串 id 列表中去掉空值和重复项，并保持确定性的升序。
func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
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

// lexicalTokenizerOrFallback returns the configured tokenizer when available, otherwise one shared disabled tokenizer so tests that instantiate Store manually keep deterministic legacy behavior.
// lexicalTokenizerOrFallback 用于在存在配置分词器时返回该实例；否则回退到共享的 disabled tokenizer，让手工构造 Store 的测试继续保持确定性的旧行为。
func (s *Store) lexicalTokenizerOrFallback() *textutil.LexicalTokenizer {
	if s != nil && s.lexicalTokenizer != nil {
		return s.lexicalTokenizer
	}
	return textutil.DisabledLexicalTokenizer()
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

// sqlUint64List converts one uint64 slice into a comma-separated SQL list for the debug-stage raw statement builders.
// sqlUint64List 用于把 uint64 切片转换成逗号分隔的 SQL 列表，供调试阶段原始语句构建器使用。
func sqlUint64List(values []uint64) string {
	if len(values) == 0 {
		return "0"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		parts = append(parts, strconv.FormatUint(value, 10))
	}
	if len(parts) == 0 {
		return "0"
	}
	return strings.Join(parts, ",")
}

// sqlStringList converts one string slice into a comma-separated SQL literal list for raw statement builders.
// sqlStringList 用于把字符串切片转换成逗号分隔的 SQL 字面量列表，供原始语句构造器使用。
func sqlStringList(values []string) string {
	if len(values) == 0 {
		return "''"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parts = append(parts, sqlStringLiteral(value))
	}
	if len(parts) == 0 {
		return "''"
	}
	return strings.Join(parts, ",")
}

// sqlStringLiteral escapes one string into a single-quoted SQL literal for debug-stage raw statement rendering.
// sqlStringLiteral 用于把字符串转成单引号 SQL 字面量，服务调试阶段的原始语句渲染。
func sqlStringLiteral(raw string) string {
	return "'" + strings.ReplaceAll(raw, "'", "''") + "'"
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
	ID               uint64  `json:"id"`
	TurnID           *uint64 `json:"turn_id"`
	ProfileType      int     `json:"profile_type"`
	BindID           uint64  `json:"bind_id"`
	Content          string  `json:"content"`
	ProfileStatus    int     `json:"profile_status"`
	Priority         int     `json:"priority"`
	ProfileLevel     int     `json:"profile_level"`
	LevelReason      string  `json:"level_reason"`
	RefreshWeight    int     `json:"refresh_weight"`
	SourceKind       int     `json:"source_kind"`
	SourceID         uint64  `json:"source_id"`
	StatusReason     string  `json:"status_reason"`
	ExpiresTimestamp int64   `json:"expires_timestamp"`
	SupersededByID   uint64  `json:"superseded_by_id"`
	ProfileDate      string  `json:"profile_date"`
	CreatedTimestamp int64   `json:"created_timestamp"`
	UpdatedTimestamp int64   `json:"updated_timestamp"`
}

// toRecord converts one SQL row into the public profile-node record shape returned by query RPCs and manual profile writebacks.
// toRecord 用于把一条 SQL 行转换成查询 RPC 与手工画像写回返回的公开画像节点结构。
func (r profileNodeRow) toRecord() logicdomain.ProfileNodeRecord {
	return logicdomain.ProfileNodeRecord{
		ID:             r.ID,
		TurnID:         optionalUint64Value(r.TurnID),
		ProfileType:    r.ProfileType,
		BindID:         r.BindID,
		Content:        r.Content,
		Status:         r.ProfileStatus,
		Priority:       r.Priority,
		ProfileLevel:   r.ProfileLevel,
		LevelReason:    r.LevelReason,
		RefreshWeight:  r.RefreshWeight,
		ProfileDate:    r.ProfileDate,
		SourceKind:     r.SourceKind,
		SourceID:       r.SourceID,
		StatusReason:   r.StatusReason,
		ExpiresAt:      unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID: r.SupersededByID,
		CreatedAt:      unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:      unixMilliToTime(r.UpdatedTimestamp),
	}
}

func (r profileNodeRow) toDomain() logicdomain.ProfileActiveNodeRecord {
	return logicdomain.ProfileActiveNodeRecord{
		ID:             r.ID,
		TurnID:         optionalUint64Value(r.TurnID),
		ProfileType:    r.ProfileType,
		BindID:         r.BindID,
		Content:        r.Content,
		Status:         r.ProfileStatus,
		Priority:       r.Priority,
		ProfileLevel:   r.ProfileLevel,
		LevelReason:    r.LevelReason,
		RefreshWeight:  r.RefreshWeight,
		ProfileDate:    r.ProfileDate,
		SourceKind:     r.SourceKind,
		SourceID:       r.SourceID,
		StatusReason:   r.StatusReason,
		ExpiresAt:      unixMilliToTime(r.ExpiresTimestamp),
		SupersededByID: r.SupersededByID,
		CreatedAt:      unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:      unixMilliToTime(r.UpdatedTimestamp),
	}
}

type profileNodeStatusRow struct {
	ID             uint64 `json:"id"`
	ProfileStatus  int    `json:"profile_status"`
	SupersededByID uint64 `json:"superseded_by_id"`
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
