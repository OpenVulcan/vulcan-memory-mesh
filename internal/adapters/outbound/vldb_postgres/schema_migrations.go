// schema_migrations.go implements the incremental PostgreSQL schema-upgrade chain used by the combined store.
// schema_migrations.go 用于实现 PostgreSQL 组合库存储使用的增量 schema 升级链。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
)

// trackedSchemaMigrationStep describes one explicit PostgreSQL schema hop between two tracked versions.
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
			ToVersion:   3,
			Name:        "drop removed work-memory tables",
			Up: func(ctx context.Context, r *maintenanceRepository) error {
				return r.migrateCombinedSchemaDropRemovedWorkMemory(ctx)
			},
		},
		{
			Component:   defaultSchemaVersionComponent,
			FromVersion: 2,
			ToVersion:   3,
			Name:        "drop removed work-memory tables",
			Up: func(ctx context.Context, r *maintenanceRepository) error {
				return r.migrateCombinedSchemaDropRemovedWorkMemory(ctx)
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

// migrateCombinedSchemaDropRemovedWorkMemory drops removed work-memory tables so upgraded PostgreSQL stores match the current schema.
// migrateCombinedSchemaDropRemovedWorkMemory 用于删除已移除的工作记忆表，让升级后的 PostgreSQL 库与当前 schema 保持一致。
func (r *maintenanceRepository) migrateCombinedSchemaDropRemovedWorkMemory(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	statements := []string{
		fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, r.maintenanceQualifiedTable("vmm_scratchpad_nodes")),
		fmt.Sprintf(`DROP TABLE IF EXISTS %s CASCADE`, r.maintenanceQualifiedTable("vmm_scratchpad_plans")),
	}
	if err := r.execDDLStatements(ctx, statements, "postgres removed work-memory schema cleanup"); err != nil {
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
