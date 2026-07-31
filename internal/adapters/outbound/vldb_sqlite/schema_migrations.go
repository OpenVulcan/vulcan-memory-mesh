// schema_migrations.go implements the reusable SQLite schema-migration runner used by the durable relational store.
// schema_migrations.go 用于实现长期关系存储使用的可复用 SQLite schema 迁移执行器。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// schemaComponentSQLite names the relational-schema component tracked in the shared version table.
	// schemaComponentSQLite 用于标识在共享版本表里跟踪的关系 schema 组件。
	schemaComponentSQLite = "sqlite"
)

// componentVersionRow mirrors one component-scoped schema-version row loaded from SQLite.
// componentVersionRow 用于映射从 SQLite 读取的一条组件级 schema 版本记录。
type componentVersionRow struct {
	Component     string `json:"component"`
	SchemaVersion int    `json:"schema_version"`
}

// pragmaTableInfoRow mirrors one PRAGMA table_info row so migrations can detect whether a column already exists.
// pragmaTableInfoRow 用于映射一条 PRAGMA table_info 结果，让迁移流程可以判断列是否已经存在。
type pragmaTableInfoRow struct {
	Name string `json:"name"`
}

// schemaMigrationStep describes one explicit upgrade hop between two adjacent schema versions.
// schemaMigrationStep 用于描述两个相邻 schema 版本之间的一步显式升级动作。
type schemaMigrationStep struct {
	FromVersion int
	ToVersion   int
	Name        string
	Up          func(context.Context, *Store) error
}

// schemaMigrationPlan collects one component's target version, fresh bootstrap logic, and ordered upgrade steps.
// schemaMigrationPlan 用于收集某个组件的目标版本、空库初始化逻辑和有序升级步骤。
type schemaMigrationPlan struct {
	Component               string
	MinimumSupportedVersion int
	TargetVersion           int
	Bootstrap               func(context.Context, *Store) error
	Steps                   []schemaMigrationStep
}

// ensureSQLiteSchema applies the reusable component-version framework so SQLite upgrades stop deleting historical data on startup.
// ensureSQLiteSchema 用于应用可复用的组件版本框架，让 SQLite 升级在启动时不再删除历史数据。
func (s *Store) ensureSQLiteSchema(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("sqlite store is nil")
	}

	// Serialize schema upgrades so concurrent boots never interleave bootstrap, ALTER TABLE, or version writes.
	// 串行化 schema 升级流程，避免并发启动交错执行 bootstrap、ALTER TABLE 或版本写入。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.ensureSchemaVersionTables(ctx); err != nil {
		return err
	}
	return s.applySchemaMigrationPlan(ctx, buildSQLiteSchemaMigrationPlan())
}

// ensureSchemaVersionTables bootstraps the component-version table used by current and future incremental upgrades.
// ensureSchemaVersionTables 用于初始化当前和未来增量升级共同使用的组件版本表。
func (s *Store) ensureSchemaVersionTables(ctx context.Context) error {
	if err := s.exec(ctx, `
CREATE TABLE IF NOT EXISTS vmm_schema_versions (
  component TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return sqliteSchemaVersionTableBootstrapError(err)
	}
	return nil
}

// sqliteSchemaVersionTableBootstrapError preserves deterministic version-table DDL errors while marking ambiguous SQLite commit boundaries.
// sqliteSchemaVersionTableBootstrapError 用于保留确定性的版本表 DDL 错误，同时标记 SQLite 提交边界不确定。
func sqliteSchemaVersionTableBootstrapError(err error) error {
	if err == nil {
		return nil
	}
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: "bootstrap sqlite schema version table",
			Message:   err.Error(),
		}
	}
	return fmt.Errorf("bootstrap component schema version table: %w", err)
}

// buildSQLiteSchemaMigrationPlan returns the current relational-schema migration chain so future upgrades only need one appended step.
// buildSQLiteSchemaMigrationPlan 用于返回当前关系 schema 的迁移链，后续版本升级只需要继续追加一步即可。
func buildSQLiteSchemaMigrationPlan() schemaMigrationPlan {
	return schemaMigrationPlan{
		Component:               schemaComponentSQLite,
		MinimumSupportedVersion: 19,
		TargetVersion:           currentSchemaVersion,
		Bootstrap:               bootstrapCurrentSQLiteSchema,
		Steps: []schemaMigrationStep{
			{
				FromVersion: 19,
				ToVersion:   20,
				Name:        "drop removed work-memory tables",
				Up:          migrateSQLiteSchema19To20,
			},
			{
				FromVersion: 20,
				ToVersion:   21,
				Name:        "add human-management recycle and operation tables",
				Up:          migrateSQLiteSchema20To21,
			},
		},
	}
}

// applySchemaMigrationPlan upgrades one component from its recorded version to the target version by executing each declared step exactly once.
// applySchemaMigrationPlan 用于把某个组件从已记录版本升级到目标版本，并确保每个声明的迁移步骤只执行一次。
func (s *Store) applySchemaMigrationPlan(ctx context.Context, plan schemaMigrationPlan) error {
	if strings.TrimSpace(plan.Component) == "" {
		return fmt.Errorf("schema migration component is required")
	}
	if plan.MinimumSupportedVersion <= 0 {
		return fmt.Errorf("schema migration minimum supported version must be > 0")
	}
	if plan.TargetVersion <= 0 {
		return fmt.Errorf("schema migration target version must be > 0")
	}
	if plan.MinimumSupportedVersion > plan.TargetVersion {
		return fmt.Errorf("schema migration minimum supported version %d cannot exceed target version %d", plan.MinimumSupportedVersion, plan.TargetVersion)
	}
	currentVersion, err := s.loadSchemaComponentVersion(ctx, plan.Component)
	if err != nil {
		return err
	}
	if currentVersion == 0 {
		if plan.Bootstrap == nil {
			return fmt.Errorf("schema migration bootstrap is required for component %s", plan.Component)
		}
		if err := plan.Bootstrap(ctx, s); err != nil {
			return fmt.Errorf("bootstrap component %s schema to version %d: %w", plan.Component, plan.TargetVersion, err)
		}
		if err := s.persistSchemaComponentVersion(ctx, plan.Component, plan.TargetVersion); err != nil {
			return err
		}
		return nil
	}
	if currentVersion < plan.MinimumSupportedVersion {
		return fmt.Errorf(
			"component %s schema version %d is no longer supported; minimum supported version is %d",
			plan.Component,
			currentVersion,
			plan.MinimumSupportedVersion,
		)
	}
	if currentVersion > plan.TargetVersion {
		return fmt.Errorf("component %s schema version %d is newer than runtime target %d", plan.Component, currentVersion, plan.TargetVersion)
	}

	stepsByFromVersion := make(map[int]schemaMigrationStep, len(plan.Steps))
	for _, step := range plan.Steps {
		if step.FromVersion <= 0 || step.ToVersion <= step.FromVersion {
			return fmt.Errorf("component %s has invalid migration step %d -> %d", plan.Component, step.FromVersion, step.ToVersion)
		}
		if step.Up == nil {
			return fmt.Errorf("component %s migration step %d -> %d is missing an up function", plan.Component, step.FromVersion, step.ToVersion)
		}
		if _, exists := stepsByFromVersion[step.FromVersion]; exists {
			return fmt.Errorf("component %s has duplicate migration step starting at version %d", plan.Component, step.FromVersion)
		}
		stepsByFromVersion[step.FromVersion] = step
	}

	for currentVersion < plan.TargetVersion {
		step, ok := stepsByFromVersion[currentVersion]
		if !ok {
			return fmt.Errorf("component %s has no migration path from version %d to %d", plan.Component, currentVersion, plan.TargetVersion)
		}
		if err := step.Up(ctx, s); err != nil {
			return fmt.Errorf("migrate component %s schema %d -> %d (%s): %w", plan.Component, step.FromVersion, step.ToVersion, step.Name, err)
		}
		currentVersion = step.ToVersion
		if err := s.persistSchemaComponentVersion(ctx, plan.Component, currentVersion); err != nil {
			return err
		}
	}
	return nil
}

// loadSchemaComponentVersion resolves one component version from the shared component table used by the current migration framework.
// loadSchemaComponentVersion 用于从当前迁移框架使用的共享组件版本表中读取某个组件的版本。
func (s *Store) loadSchemaComponentVersion(ctx context.Context, component string) (int, error) {
	rows, err := queryRows[componentVersionRow](s, ctx, `
SELECT component, schema_version
FROM vmm_schema_versions
WHERE component = ?
LIMIT 1
`, strings.TrimSpace(component))
	if err != nil {
		return 0, fmt.Errorf("query component schema version for %s: %w", component, err)
	}
	if len(rows) > 0 {
		return rows[0].SchemaVersion, nil
	}
	return 0, nil
}

// persistSchemaComponentVersion upserts the component version row so future schema upgrades can compare against one monotonic stored baseline.
// persistSchemaComponentVersion 用于 upsert 组件版本记录，让未来 schema 升级能够对照一个单调递增的持久化基线。
func (s *Store) persistSchemaComponentVersion(ctx context.Context, component string, version int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.exec(ctx, `
INSERT INTO vmm_schema_versions (component, schema_version, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(component) DO UPDATE SET
  schema_version = excluded.schema_version,
  updated_at = excluded.updated_at
`, strings.TrimSpace(component), version, now); err != nil {
		return sqliteSchemaVersionPersistError(component, version, err)
	}
	return nil
}

// sqliteSchemaVersionPersistError preserves deterministic schema-version write errors while marking commit-boundary failures as uncertain.
// sqliteSchemaVersionPersistError 用于保留确定性的 schema 版本写入错误，同时把提交边界失败标记为结果不确定。
func sqliteSchemaVersionPersistError(component string, version int, err error) error {
	if err == nil {
		return nil
	}
	message := fmt.Sprintf("persist component %s schema version %d", strings.TrimSpace(component), version)
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: "persist sqlite schema version",
			Message:   fmt.Sprintf("%s: %v", message, err),
		}
	}
	return fmt.Errorf("%s: %w", message, err)
}

// bootstrapCurrentSQLiteSchema applies the latest full relational schema to an empty database and seeds the deterministic debug hierarchy only when the workspace is still empty.
// bootstrapCurrentSQLiteSchema 用于把最新完整关系 schema 应用到空库，并只在工作区仍为空时补种确定性的调试层级。
func bootstrapCurrentSQLiteSchema(ctx context.Context, s *Store) error {
	if err := s.exec(ctx, currentSchemaSQL); err != nil {
		return sqliteSchemaMigrationError("bootstrap sqlite schema", fmt.Errorf("apply current sqlite schema: %w", err))
	}
	if err := s.seedDebugWorkspaceIfEmpty(ctx); err != nil {
		return err
	}
	return nil
}

// sqliteSchemaMigrationError classifies schema scripts that include their own commit boundary without hiding ordinary deterministic DDL failures.
// sqliteSchemaMigrationError 用于分类自带提交边界的 schema 脚本错误，同时不掩盖普通确定性 DDL 失败。
func sqliteSchemaMigrationError(operation string, err error) error {
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

// seedDebugWorkspaceIfEmpty inserts the deterministic default hierarchy only for fresh local workspaces, preventing later migrations from duplicating debug seed rows.
// seedDebugWorkspaceIfEmpty 用于仅在全新本地工作区中插入确定性的默认层级，避免后续迁移重复写入调试种子数据。
func (s *Store) seedDebugWorkspaceIfEmpty(ctx context.Context) error {
	projectCount, err := s.countRows(ctx, `SELECT COUNT(*) AS count FROM vmm_projects`)
	if err != nil {
		return fmt.Errorf("count projects before debug seed bootstrap: %w", err)
	}
	if projectCount > 0 {
		return nil
	}
	// Seed hierarchy rows as single-statement typed calls because the real SQLite gateway rejects flat params on multi-statement scripts.
	// 以单语句强类型调用补种层级行，因为真实 SQLite 网关会拒绝把扁平参数绑定到多语句脚本。
	for index, seedStatement := range parameterizedDebugSeedWorkspaceStatements(time.Now().UTC()) {
		if err := s.exec(ctx, seedStatement.SQL, seedStatement.Params...); err != nil {
			return fmt.Errorf("seed debug sqlite workspace statement %d: %w", index+1, err)
		}
	}
	return nil
}

// migrateSQLiteSchema14To15 adds the compact-boundary columns needed by ChatCompact and future pre-check boundary filtering without rewriting historical rows.
// migrateSQLiteSchema14To15 用于新增 ChatCompact 和后续 pre-check 边界过滤所需的 compact 字段，同时避免重写历史行。
func migrateSQLiteSchema14To15(ctx context.Context, s *Store) error {
	operations := []struct {
		TableName  string
		ColumnName string
		SQL        string
	}{
		{
			TableName:  "vmm_sessions",
			ColumnName: "last_compacted_turn_id",
			SQL:        `ALTER TABLE vmm_sessions ADD COLUMN last_compacted_turn_id BIGINT NOT NULL DEFAULT 0`,
		},
		{
			TableName:  "vmm_sessions",
			ColumnName: "last_compacted_timestamp",
			SQL:        `ALTER TABLE vmm_sessions ADD COLUMN last_compacted_timestamp BIGINT NOT NULL DEFAULT 0`,
		},
	}
	for _, operation := range operations {
		exists, err := s.sqliteColumnExists(ctx, operation.TableName, operation.ColumnName)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := s.exec(ctx, operation.SQL); err != nil {
			return err
		}
	}
	if err := s.exec(ctx, `CREATE INDEX IF NOT EXISTS idx_vmm_sessions_compact_boundary ON vmm_sessions(project_id, last_compacted_turn_id, id)`); err != nil {
		return fmt.Errorf("create compact boundary index: %w", err)
	}
	return nil
}

// migrateSQLiteSchema15To16 adds the first retention batch, trash, and vector-gc tables so cold-memory cleanup can leave an offline audit buffer instead of only marking rows stale.
// migrateSQLiteSchema15To16 用于新增 retention 批次、回收站和向量 GC 表，让冷记忆清理在移出热表时保留离线审计缓冲，而不只是标记过时。
func migrateSQLiteSchema15To16(ctx context.Context, s *Store) error {
	statements := []string{
		`
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_recycle_batches_lookup ON vmm_recycle_batches(recycle_type, recycled_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_recycle_batches_session ON vmm_recycle_batches(session_id, project_id, id)`,
		`
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_trash_recycled ON vmm_memory_nodes_trash(recycled_at, batch_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_memory_nodes_trash_batch ON vmm_memory_nodes_trash(batch_id, id)`,
		`
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_trash_recycled ON vmm_memory_context_edges_trash(recycled_at, batch_id, memory_id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_memory_context_edges_trash_batch ON vmm_memory_context_edges_trash(batch_id, memory_id)`,
		`
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
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_unique ON vmm_vector_gc_jobs(vector_id, job_type, batch_id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_pending ON vmm_vector_gc_jobs(completed_timestamp, next_run_timestamp, id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_vector_gc_jobs_batch ON vmm_vector_gc_jobs(batch_id, id)`,
	}
	for _, statement := range statements {
		if err := s.exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// migrateSQLiteSchema16To17 adds the turn-trash table needed by idle-session recycle so cold turns can leave the hot table without losing offline audit breadcrumbs.
// migrateSQLiteSchema16To17 用于新增 idle-session 回收所需的 turn 回收站表，让冷 turn 退出热表时仍保留离线审计痕迹。
func migrateSQLiteSchema16To17(ctx context.Context, s *Store) error {
	statements := []string{
		`
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_trash_recycled ON vmm_turn_records_trash(recycled_at, batch_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_turn_records_trash_batch ON vmm_turn_records_trash(batch_id, session_id, id)`,
	}
	for _, statement := range statements {
		if err := s.exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// migrateSQLiteSchema17To18 adds the recycle-job queue used by the independent cold-turn scan/claim/execute pipeline.
// migrateSQLiteSchema17To18 用于新增独立冷 turn 扫描/领取/执行链路使用的回收任务队列表。
func migrateSQLiteSchema17To18(ctx context.Context, s *Store) error {
	statements := []string{
		`
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
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_unique ON vmm_recycle_jobs(session_id, job_type)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_pending ON vmm_recycle_jobs(job_type, next_run_timestamp, id)`,
		`CREATE INDEX IF NOT EXISTS idx_vmm_recycle_jobs_session ON vmm_recycle_jobs(session_id, project_id, id)`,
	}
	for _, statement := range statements {
		if err := s.exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// migrateSQLiteSchema19To20 drops removed work-memory tables so upgraded local databases match the current runtime schema.
// migrateSQLiteSchema19To20 用于删除已移除的工作记忆表，让升级后的本地数据库与当前运行时 schema 保持一致。
func migrateSQLiteSchema19To20(ctx context.Context, s *Store) error {
	statement := `
BEGIN IMMEDIATE;
DROP TABLE IF EXISTS vmm_scratchpad_nodes;
DROP TABLE IF EXISTS vmm_scratchpad_plans;
COMMIT;
`
	if err := s.exec(ctx, statement); err != nil {
		return sqliteSchemaMigrationError("migrate sqlite schema 19 to 20", fmt.Errorf("drop removed sqlite work-memory tables: %w", err))
	}
	return nil
}

// migrateSQLiteSchema20To21 adds the isolated management schema to existing stores.
// migrateSQLiteSchema20To21 用于向现有存储增加独立管理 schema。
func migrateSQLiteSchema20To21(ctx context.Context, s *Store) error {
	const script = `
BEGIN IMMEDIATE;
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
  batch_id BIGINT NOT NULL, recycled_at BIGINT NOT NULL DEFAULT 0, recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL, session_key TEXT NOT NULL, user_id BIGINT NOT NULL, team_id BIGINT NOT NULL,
  space_id BIGINT NOT NULL, project_id BIGINT NOT NULL, turn_count INTEGER NOT NULL DEFAULT 0,
  last_summarized_id BIGINT NOT NULL DEFAULT 0, last_compacted_turn_id BIGINT NOT NULL DEFAULT 0,
  summarize_content TEXT NOT NULL DEFAULT '', summarize_budget INTEGER NOT NULL DEFAULT 0,
  last_extract_observed_timestamp BIGINT NOT NULL DEFAULT 0, last_extract_completed_timestamp BIGINT NOT NULL DEFAULT 0,
  last_compacted_timestamp BIGINT NOT NULL DEFAULT 0, created_timestamp BIGINT NOT NULL, updated_timestamp BIGINT NOT NULL,
  PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_sessions_trash_batch ON vmm_sessions_trash(batch_id, id);
CREATE TABLE IF NOT EXISTS vmm_profile_nodes_trash (
  batch_id BIGINT NOT NULL, recycled_at BIGINT NOT NULL DEFAULT 0, recycle_reason TEXT NOT NULL DEFAULT '',
  id BIGINT NOT NULL, turn_id BIGINT, profile_type TINYINT NOT NULL, bind_id BIGINT NOT NULL,
  content TEXT NOT NULL, profile_status TINYINT NOT NULL DEFAULT 1, priority TINYINT NOT NULL DEFAULT 2,
  profile_level TINYINT NOT NULL DEFAULT 0, level_reason TEXT NOT NULL DEFAULT '', refresh_weight INTEGER NOT NULL DEFAULT 0,
  source_kind TINYINT NOT NULL DEFAULT 0, source_id BIGINT NOT NULL DEFAULT 0, status_reason TEXT NOT NULL DEFAULT '',
  expires_timestamp BIGINT NOT NULL DEFAULT 0, superseded_by_id BIGINT NOT NULL DEFAULT 0, profile_date TEXT NOT NULL DEFAULT '',
  created_timestamp BIGINT NOT NULL, updated_timestamp BIGINT NOT NULL, PRIMARY KEY (batch_id, id)
);
CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_trash_batch ON vmm_profile_nodes_trash(batch_id, id);
COMMIT;
`
	if err := s.exec(ctx, script); err != nil {
		return sqliteSchemaMigrationError("migrate sqlite schema 20 to 21", err)
	}
	return nil
}

// sqliteColumnExists checks PRAGMA table_info so migrations can stay idempotent across partially upgraded local databases.
// sqliteColumnExists 用于查询 PRAGMA table_info，让迁移在部分升级过的本地数据库上依然保持幂等。
func (s *Store) sqliteColumnExists(ctx context.Context, tableName, columnName string) (bool, error) {
	rows, err := queryRows[pragmaTableInfoRow](s, ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return false, fmt.Errorf("query pragma table_info(%s): %w", tableName, err)
	}
	target := strings.TrimSpace(columnName)
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.Name), target) {
			return true, nil
		}
	}
	return false, nil
}

// GetSchemaComponentVersion exposes one component version to the composition root so SQL and vector schema lifecycles can be coordinated independently.
// GetSchemaComponentVersion 用于向组合根暴露某个组件的版本信息，使 SQL 与向量 schema 生命周期能够独立协调。
func (s *Store) GetSchemaComponentVersion(ctx context.Context, component string) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("sqlite store is nil")
	}
	if err := s.ensureSchemaVersionTables(ctx); err != nil {
		return 0, err
	}
	return s.loadSchemaComponentVersion(ctx, component)
}

// SetSchemaComponentVersion persists one component version for infrastructure-only upgrade coordination outside the business use cases.
// SetSchemaComponentVersion 用于持久化某个组件的版本，服务业务用例之外的基础设施升级协调。
func (s *Store) SetSchemaComponentVersion(ctx context.Context, component string, version int) error {
	if s == nil {
		return fmt.Errorf("sqlite store is nil")
	}
	if version < 0 {
		return fmt.Errorf("schema version must be >= 0")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.ensureSchemaVersionTables(ctx); err != nil {
		return err
	}
	return s.persistSchemaComponentVersion(ctx, component, version)
}
