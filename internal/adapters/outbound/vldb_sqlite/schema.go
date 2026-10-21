// SQLite schema definitions for adapter startup.
// SQLite 适配层启动时使用的表结构定义。
package vldb_sqlite

import (
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"time"
)

const (
	// currentSchemaVersion tracks the newest SQLite schema version understood by this runtime.
	// currentSchemaVersion 用于标记当前运行时理解的最新 SQLite 表结构版本。
	currentSchemaVersion = 22

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

CREATE TABLE IF NOT EXISTS vmm_turn_analysis_failures (
  turn_id BIGINT NOT NULL,
  session_id BIGINT NOT NULL,
  stage TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  terminal_status TEXT NOT NULL DEFAULT 'retrying',
  last_error TEXT NOT NULL DEFAULT '',
  last_attempt_timestamp BIGINT NOT NULL,
  PRIMARY KEY (turn_id, stage),
  FOREIGN KEY(turn_id) REFERENCES vmm_turn_records(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_vmm_turn_analysis_failures_session ON vmm_turn_analysis_failures(session_id, terminal_status, turn_id);
CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_failure_pass
AFTER INSERT ON vmm_turn_analysis_failures
WHEN NEW.terminal_status IN ('passed', 'passed_uncertain')
BEGIN
  UPDATE vmm_turn_records
  SET extracted_status = 2,
      updated_timestamp = CASE
        WHEN updated_timestamp < NEW.last_attempt_timestamp THEN NEW.last_attempt_timestamp
        ELSE updated_timestamp
      END
  WHERE id = NEW.turn_id AND session_id = NEW.session_id AND extracted_status = 0;
END;
CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_failure_update_pass
AFTER UPDATE OF terminal_status, last_attempt_timestamp ON vmm_turn_analysis_failures
WHEN NEW.terminal_status IN ('passed', 'passed_uncertain')
BEGIN
  UPDATE vmm_turn_records
  SET extracted_status = 2,
      updated_timestamp = CASE
        WHEN updated_timestamp < NEW.last_attempt_timestamp THEN NEW.last_attempt_timestamp
        ELSE updated_timestamp
      END
  WHERE id = NEW.turn_id AND session_id = NEW.session_id AND extracted_status = 0;
END;
CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_success_clear
AFTER UPDATE OF extracted_status ON vmm_turn_records
WHEN OLD.extracted_status = 0 AND NEW.extracted_status = 1
BEGIN
  DELETE FROM vmm_turn_analysis_failures
  WHERE turn_id = NEW.id AND session_id = NEW.session_id;
END;

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

CREATE TABLE IF NOT EXISTS vmm_management_session_states (
  session_id BIGINT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'active',
  revision BIGINT NOT NULL DEFAULT 1,
  updated_timestamp BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_management_session_states_status ON vmm_management_session_states(status, updated_timestamp, session_id);

CREATE TABLE IF NOT EXISTS vmm_management_previews (
  preview_token TEXT PRIMARY KEY,
  target_type TEXT NOT NULL,
  target_ids_json TEXT NOT NULL,
  action TEXT NOT NULL,
  source TEXT NOT NULL,
  impact_json TEXT NOT NULL,
  impact_revision TEXT NOT NULL,
  confirm_text TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  expires_timestamp BIGINT NOT NULL,
  consumed_timestamp BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_vmm_management_previews_expiry ON vmm_management_previews(expires_timestamp, preview_token);

CREATE TABLE IF NOT EXISTS vmm_management_operations (
  operation_id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  action TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_ids_json TEXT NOT NULL,
  status TEXT NOT NULL,
  batch_id BIGINT NOT NULL DEFAULT 0,
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL,
  updated_timestamp BIGINT NOT NULL,
  completed_timestamp BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_vmm_management_operations_status ON vmm_management_operations(status, updated_timestamp, operation_id);

CREATE TABLE IF NOT EXISTS vmm_management_recycle_batches (
  batch_id BIGINT PRIMARY KEY,
  source TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_ids_json TEXT NOT NULL,
  restorable INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  expires_timestamp BIGINT NOT NULL DEFAULT 0,
  restored_timestamp BIGINT NOT NULL DEFAULT 0,
  operation_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_vmm_management_recycle_batches_state ON vmm_management_recycle_batches(state, batch_id);

CREATE TABLE IF NOT EXISTS vmm_sessions_trash (
  batch_id BIGINT NOT NULL,
  recycled_at BIGINT NOT NULL DEFAULT 0,
  recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL,
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
  PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_trash_batch ON vmm_sessions_trash(batch_id, id);

CREATE TABLE IF NOT EXISTS vmm_profile_nodes_trash (
  batch_id BIGINT NOT NULL,
  recycled_at BIGINT NOT NULL DEFAULT 0,
  recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL,
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
  updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_trash_batch ON vmm_profile_nodes_trash(batch_id, id);

COMMIT;
`
