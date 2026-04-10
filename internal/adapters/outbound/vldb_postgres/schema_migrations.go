// schema_migrations.go implements the incremental PostgreSQL schema-upgrade chain used by the combined store.
// schema_migrations.go 用于实现 PostgreSQL 组合库存储使用的增量 schema 升级链。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
)

// trackedSchemaMigrationStep describes one explicit PostgreSQL schema hop between two adjacent tracked versions.
// trackedSchemaMigrationStep 用于描述 PostgreSQL 跟踪版本之间的一步显式 schema 升级动作。
type trackedSchemaMigrationStep struct {
	Component   string
	FromVersion int
	ToVersion   int
	Name        string
	Up          func(context.Context, *maintenanceRepository) error
}

// trackedSchemaMigrationSteps returns the explicit PostgreSQL migration chain known by the current runtime.
// trackedSchemaMigrationSteps 用于返回当前运行时已知的 PostgreSQL 显式迁移链。
func trackedSchemaMigrationSteps(flavor string) []trackedSchemaMigrationStep {
	return []trackedSchemaMigrationStep{
		{
			Component:   defaultSchemaVersionComponent,
			FromVersion: 1,
			ToVersion:   2,
			Name:        "add isolated scratchpad tables and indexes",
			Up: func(ctx context.Context, r *maintenanceRepository) error {
				return r.migrateCombinedSchema1To2(ctx)
			},
		},
	}
}

// validateTrackedSchemaVersion rejects impossible newer rows while allowing older rows to continue into the explicit migration stage.
// validateTrackedSchemaVersion 用于拒绝不可能的新版本记录，同时允许旧版本继续进入显式迁移阶段。
func validateTrackedSchemaVersion(component string, stored, current int) error {
	component = strings.TrimSpace(component)
	if stored == 0 {
		return nil
	}
	if stored > current {
		return fmt.Errorf("tracked postgres schema version for %s (%d) is newer than runtime target %d", component, stored, current)
	}
	return nil
}

// resolveTrackedSchemaMigrationPath expands one component's explicit migration path so startup can deterministically step forward without guessing.
// resolveTrackedSchemaMigrationPath 用于展开某个组件的显式迁移路径，让启动流程能确定性前进而不做推断。
func resolveTrackedSchemaMigrationPath(component string, stored, target int, steps []trackedSchemaMigrationStep) ([]trackedSchemaMigrationStep, error) {
	component = strings.TrimSpace(component)
	if stored == 0 || stored == target {
		return nil, nil
	}
	if err := validateTrackedSchemaVersion(component, stored, target); err != nil {
		return nil, err
	}
	stepsByFrom := make(map[int]trackedSchemaMigrationStep, len(steps))
	for _, step := range steps {
		if !strings.EqualFold(strings.TrimSpace(step.Component), component) {
			continue
		}
		if step.FromVersion <= 0 || step.ToVersion <= step.FromVersion {
			return nil, fmt.Errorf("postgres schema migration for %s has invalid step %d -> %d", component, step.FromVersion, step.ToVersion)
		}
		if step.Up == nil {
			return nil, fmt.Errorf("postgres schema migration for %s step %d -> %d is missing an up function", component, step.FromVersion, step.ToVersion)
		}
		if _, exists := stepsByFrom[step.FromVersion]; exists {
			return nil, fmt.Errorf("postgres schema migration for %s has duplicate step starting at %d", component, step.FromVersion)
		}
		stepsByFrom[step.FromVersion] = step
	}

	path := make([]trackedSchemaMigrationStep, 0)
	current := stored
	for current < target {
		step, ok := stepsByFrom[current]
		if !ok {
			return nil, fmt.Errorf("postgres schema migration for %s has no path from version %d to %d", component, current, target)
		}
		path = append(path, step)
		current = step.ToVersion
	}
	return path, nil
}

// applyTrackedSchemaMigrations executes every explicit PostgreSQL migration step before the generic current-schema ensure phase runs.
// applyTrackedSchemaMigrations 用于在通用当前 schema 幂等补齐之前执行所有显式 PostgreSQL 迁移步骤。
func (r *maintenanceRepository) applyTrackedSchemaMigrations(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	steps := trackedSchemaMigrationSteps(r.shared.cfg.Flavor)
	for _, component := range trackedSchemaComponents(r.shared.cfg.Flavor) {
		stored, err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).GetSchemaComponentVersion(ctx, component.component)
		if err != nil {
			return err
		}
		path, err := resolveTrackedSchemaMigrationPath(component.component, stored, component.version, steps)
		if err != nil {
			return err
		}
		current := stored
		for _, step := range path {
			if err := step.Up(ctx, r); err != nil {
				return fmt.Errorf("migrate postgres schema component %s %d -> %d (%s): %w", step.Component, step.FromVersion, step.ToVersion, step.Name, err)
			}
			current = step.ToVersion
			if err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).SetSchemaComponentVersion(ctx, component.component, current); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateCombinedSchema1To2 adds the isolated scratchpad tables and indexes so older PostgreSQL combined stores can upgrade in place.
// migrateCombinedSchema1To2 用于增加隔离 scratchpad 表和索引，让旧版 PostgreSQL 组合库可以原地升级。
func (r *maintenanceRepository) migrateCombinedSchema1To2(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if err := r.execDDLStatements(ctx, r.scratchpadSchemaDDLs(), "postgres scratchpad schema migration"); err != nil {
		return err
	}
	if err := r.execDDLStatements(ctx, r.scratchpadIndexDDLs(), "postgres scratchpad index migration"); err != nil {
		return err
	}
	return nil
}

// execDDLStatements executes one deterministic DDL batch sequentially so migration errors stay attributable to one schema phase.
// execDDLStatements 用于顺序执行一组确定性的 DDL，让迁移错误可以稳定归因到某个 schema 阶段。
func (r *maintenanceRepository) execDDLStatements(ctx context.Context, statements []string, phase string) error {
	for _, statement := range statements {
		if _, err := r.shared.pool.Exec(ctx, strings.TrimSpace(statement)); err != nil {
			return fmt.Errorf("%s: %w", strings.TrimSpace(phase), err)
		}
	}
	return nil
}

// scratchpadSchemaDDLs returns the isolated scratchpad table DDL shared by bootstrap and explicit migration flows.
// scratchpadSchemaDDLs 用于返回 bootstrap 与显式迁移共享的 scratchpad 表 DDL。
func (r *maintenanceRepository) scratchpadSchemaDDLs() []string {
	return []string{
		r.scratchpadPlansTableDDL(),
		r.scratchpadNodesTableDDL(),
	}
}

// scratchpadPlansTableDDL returns the canonical PostgreSQL DDL for the isolated DWM plan-lock table.
// scratchpadPlansTableDDL 用于返回隔离 DWM 计划锁表的标准 PostgreSQL DDL。
func (r *maintenanceRepository) scratchpadPlansTableDDL() string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
	project_id BIGINT NOT NULL REFERENCES %s(id),
	user_id BIGINT NOT NULL REFERENCES %s(id),
	session_key TEXT NOT NULL,
	plan_name TEXT NOT NULL,
	plan_name_norm TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE(project_id, user_id, session_key)
)`, r.scratchpadPlansTable(), r.projectsTable(), r.usersTable())
}

// scratchpadNodesTableDDL returns the canonical PostgreSQL DDL for the isolated DWM key/value node table.
// scratchpadNodesTableDDL 用于返回隔离 DWM key/value 节点表的标准 PostgreSQL DDL。
func (r *maintenanceRepository) scratchpadNodesTableDDL() string {
	return fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
	plan_id BIGINT NOT NULL REFERENCES %s(id) ON DELETE CASCADE,
	item_key TEXT NOT NULL,
	item_value TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	UNIQUE(plan_id, item_key)
)`, r.scratchpadNodesTable(), r.scratchpadPlansTable())
}

// scratchpadIndexDDLs returns the isolated scratchpad index DDL shared by bootstrap and explicit migration flows.
// scratchpadIndexDDLs 用于返回 bootstrap 与显式迁移共享的 scratchpad 索引 DDL。
func (r *maintenanceRepository) scratchpadIndexDDLs() []string {
	return []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_plans_scope ON %s (project_id, user_id, session_key, updated_at DESC)`, r.scratchpadPlansTable()),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_plans_gc ON %s (updated_at, id)`, r.scratchpadPlansTable()),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_nodes_plan ON %s (plan_id, updated_at DESC, id)`, r.scratchpadNodesTable()),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_vmm_scratchpad_nodes_created ON %s (created_at, id)`, r.scratchpadNodesTable()),
	}
}
