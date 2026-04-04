// schema_migrations.go implements the reusable SQLite schema-migration runner used by the durable relational store.
// schema_migrations.go 用于实现长期关系存储使用的可复用 SQLite schema 迁移执行器。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// schemaComponentSQLite names the relational-schema component tracked in the shared version table.
	// schemaComponentSQLite 用于标识在共享版本表里跟踪的关系 schema 组件。
	schemaComponentSQLite = "sqlite"

	// legacySQLiteIncrementalMigrationBaseline marks the oldest SQLite schema version that can continue on the explicit incremental migration chain.
	// legacySQLiteIncrementalMigrationBaseline 用于标记仍可继续走显式增量迁移链的最老 SQLite schema 版本。
	legacySQLiteIncrementalMigrationBaseline = 14
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
	Component     string
	TargetVersion int
	Bootstrap     func(context.Context, *Store) error
	Steps         []schemaMigrationStep
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

// ensureSchemaVersionTables bootstraps both the legacy singleton table and the new component-version table used for future incremental upgrades.
// ensureSchemaVersionTables 用于初始化旧版单例版本表和新版组件版本表，为未来的增量升级共同提供基础。
func (s *Store) ensureSchemaVersionTables(ctx context.Context) error {
	if err := s.exec(ctx, `CREATE TABLE IF NOT EXISTS vmm_version (singleton_id INTEGER PRIMARY KEY, schema_version INTEGER NOT NULL, updated_at TEXT NOT NULL DEFAULT '')`); err != nil {
		return fmt.Errorf("bootstrap legacy sqlite schema version table: %w", err)
	}
	if err := s.exec(ctx, `
CREATE TABLE IF NOT EXISTS vmm_schema_versions (
  component TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return fmt.Errorf("bootstrap component schema version table: %w", err)
	}
	return nil
}

// buildSQLiteSchemaMigrationPlan returns the current relational-schema migration chain so future upgrades only need one appended step.
// buildSQLiteSchemaMigrationPlan 用于返回当前关系 schema 的迁移链，后续版本升级只需要继续追加一步即可。
func buildSQLiteSchemaMigrationPlan() schemaMigrationPlan {
	return schemaMigrationPlan{
		Component:     schemaComponentSQLite,
		TargetVersion: currentSchemaVersion,
		Bootstrap:     bootstrapCurrentSQLiteSchema,
		Steps: []schemaMigrationStep{
			{
				FromVersion: 14,
				ToVersion:   15,
				Name:        "add session compact boundary columns",
				Up:          migrateSQLiteSchema14To15,
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
	if plan.TargetVersion <= 0 {
		return fmt.Errorf("schema migration target version must be > 0")
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
	if requiresLegacySQLiteReset(plan.Component, currentVersion) {
		if err := s.resetLegacySQLiteSchemaToCurrent(ctx, currentVersion, plan.TargetVersion); err != nil {
			return fmt.Errorf("reset legacy sqlite schema %d -> %d: %w", currentVersion, plan.TargetVersion, err)
		}
		if err := s.persistSchemaComponentVersion(ctx, plan.Component, plan.TargetVersion); err != nil {
			return err
		}
		return nil
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

// requiresLegacySQLiteReset reports whether one historical SQLite schema version predates the supported incremental migration baseline and therefore must be rebuilt to the current schema directly.
// requiresLegacySQLiteReset 用于判断某个历史 SQLite schema 版本是否早于受支持的增量迁移基线，因此必须直接重建到当前 schema。
func requiresLegacySQLiteReset(component string, currentVersion int) bool {
	return strings.EqualFold(strings.TrimSpace(component), schemaComponentSQLite) &&
		currentVersion > 0 &&
		currentVersion < legacySQLiteIncrementalMigrationBaseline
}

// resetLegacySQLiteSchemaToCurrent rebuilds very old local SQLite databases with the current managed schema so startup stays compatible with pre-incremental local installs.
// resetLegacySQLiteSchemaToCurrent 用于把非常老的本地 SQLite 数据库直接重建到当前受管 schema，保证增量迁移框架启用前的本地安装仍可启动。
func (s *Store) resetLegacySQLiteSchemaToCurrent(ctx context.Context, fromVersion, toVersion int) error {
	if err := s.exec(ctx, resetManagedSchemaSQL); err != nil {
		return fmt.Errorf("drop managed sqlite tables for legacy reset %d -> %d: %w", fromVersion, toVersion, err)
	}
	if err := bootstrapCurrentSQLiteSchema(ctx, s); err != nil {
		return fmt.Errorf("bootstrap current sqlite schema after legacy reset %d -> %d: %w", fromVersion, toVersion, err)
	}
	return nil
}

// loadSchemaComponentVersion resolves one component version from the new table and falls back to the legacy singleton row for SQLite upgrades.
// loadSchemaComponentVersion 用于从新版组件表读取某个组件的版本，并在 SQLite 升级时回退到旧版单例版本行。
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
	if strings.EqualFold(strings.TrimSpace(component), schemaComponentSQLite) {
		legacyRows, err := queryRows[versionRow](s, ctx, `SELECT schema_version FROM vmm_version WHERE singleton_id = ? LIMIT 1`, versionSingletonID)
		if err != nil {
			return 0, fmt.Errorf("query legacy sqlite schema version: %w", err)
		}
		if len(legacyRows) > 0 {
			return legacyRows[0].SchemaVersion, nil
		}
	}
	return 0, nil
}

// persistSchemaComponentVersion upserts the component version row and keeps the legacy sqlite singleton row synchronized for smoother local debugging.
// persistSchemaComponentVersion 用于 upsert 组件版本记录，并同步维护旧版 sqlite 单例版本行，便于本地调试平滑过渡。
func (s *Store) persistSchemaComponentVersion(ctx context.Context, component string, version int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.exec(ctx, `
INSERT INTO vmm_schema_versions (component, schema_version, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(component) DO UPDATE SET
  schema_version = excluded.schema_version,
  updated_at = excluded.updated_at
`, strings.TrimSpace(component), version, now); err != nil {
		return fmt.Errorf("persist component %s schema version %d: %w", component, version, err)
	}
	if !strings.EqualFold(strings.TrimSpace(component), schemaComponentSQLite) {
		return nil
	}
	if err := s.exec(ctx, `DELETE FROM vmm_version WHERE singleton_id = ?`, versionSingletonID); err != nil {
		return fmt.Errorf("clear legacy sqlite schema version row: %w", err)
	}
	if err := s.exec(ctx, `INSERT INTO vmm_version (singleton_id, schema_version, updated_at) VALUES (?, ?, ?)`, versionSingletonID, version, now); err != nil {
		return fmt.Errorf("persist legacy sqlite schema version row: %w", err)
	}
	return nil
}

// bootstrapCurrentSQLiteSchema applies the latest full relational schema to an empty database and seeds the deterministic debug hierarchy only when the workspace is still empty.
// bootstrapCurrentSQLiteSchema 用于把最新完整关系 schema 应用到空库，并只在工作区仍为空时补种确定性的调试层级。
func bootstrapCurrentSQLiteSchema(ctx context.Context, s *Store) error {
	if err := s.exec(ctx, currentSchemaSQL); err != nil {
		return fmt.Errorf("apply current sqlite schema: %w", err)
	}
	if err := s.seedDebugWorkspaceIfEmpty(ctx); err != nil {
		return err
	}
	return nil
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
	if err := s.exec(ctx, buildDebugSeedWorkspaceSQL(time.Now().UTC())); err != nil {
		return fmt.Errorf("seed debug sqlite workspace: %w", err)
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
