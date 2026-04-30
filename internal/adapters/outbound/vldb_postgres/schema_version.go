// schema_version.go centralizes PostgreSQL combined-store schema version semantics so bootstrap, runtime checks, and debug migration share one authoritative source.
// schema_version.go 用于集中 PostgreSQL 组合库的 schema 版本语义，让启动、运行时校验和调试迁移共享同一份权威定义。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"
)

const (
	// currentCombinedSchemaVersion tracks the shared PostgreSQL physical table layout used by every flavor.
	// currentCombinedSchemaVersion 用于跟踪所有 flavor 共用的 PostgreSQL 物理表结构版本。
	currentCombinedSchemaVersion = 3

	// currentCombinedSearchSchemaVersion tracks the active lexical-search layout generation for the current flavor.
	// currentCombinedSearchSchemaVersion 用于跟踪当前 flavor 的词法检索布局版本。
	currentCombinedSearchSchemaVersion = 1
)

// trackedSchemaComponent binds one stable component key to the runtime version that code currently understands.
// trackedSchemaComponent 用于把稳定组件键名绑定到当前代码所理解的运行时版本号。
type trackedSchemaComponent struct {
	component string
	version   int
}

// trackedSchemaComponents returns the full set of version rows that must stay aligned with the active combined-store flavor.
// trackedSchemaComponents 用于返回当前组合库 flavor 下必须保持一致的全部版本记录。
func trackedSchemaComponents(flavor string) []trackedSchemaComponent {
	return []trackedSchemaComponent{
		{component: defaultSchemaVersionComponent, version: currentCombinedSchemaVersion},
		{component: searchSchemaVersionComponent(flavor), version: currentCombinedSearchSchemaVersion},
	}
}

// searchSchemaVersionComponent returns the stable component key used to track the active lexical-search layout for the selected PostgreSQL flavor.
// searchSchemaVersionComponent 用于返回当前 PostgreSQL flavor 对应的稳定词法检索版本组件键名。
func searchSchemaVersionComponent(flavor string) string {
	flavor = strings.ToLower(strings.TrimSpace(flavor))
	if flavor == "" {
		flavor = "paradedb"
	}
	return "postgres_combined_search_" + flavor
}

// ensureSchemaVersionTable bootstraps the schema-version table early so version checks can fail fast before later DDL mutates more objects.
// ensureSchemaVersionTable 用于优先初始化版本表，让版本校验能在后续 DDL 变更更多对象前尽早失败。
func (r *maintenanceRepository) ensureSchemaVersionTable(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	statements := []string{
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, quoteIdentifier(r.shared.cfg.Schema)),
		fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	component TEXT PRIMARY KEY,
	schema_version INTEGER NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`, r.maintenanceQualifiedTable("vmm_schema_versions")),
	}
	for _, statement := range statements {
		if _, err := r.shared.pool.Exec(ctx, strings.TrimSpace(statement)); err != nil {
			return fmt.Errorf("bootstrap postgres schema version table: %w", err)
		}
	}
	return nil
}

// validateTrackedSchemaVersions checks every stored version row that already exists and stops startup when the runtime cannot safely understand it.
// validateTrackedSchemaVersions 用于检查当前已存在的版本记录，并在运行时代码无法安全理解时阻止启动继续进行。
func (r *maintenanceRepository) validateTrackedSchemaVersions(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	for _, component := range trackedSchemaComponents(r.shared.cfg.Flavor) {
		stored, err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).GetSchemaComponentVersion(ctx, component.component)
		if err != nil {
			return err
		}
		if err := validateTrackedSchemaVersion(component.component, stored, component.version); err != nil {
			return err
		}
	}
	return nil
}

// backfillTrackedSchemaVersions writes the current runtime versions only when the table still lacks those rows after a successful bootstrap.
// backfillTrackedSchemaVersions 用于在启动成功后，仅为尚不存在的版本记录补写当前运行时版本。
func (r *maintenanceRepository) backfillTrackedSchemaVersions(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	for _, component := range trackedSchemaComponents(r.shared.cfg.Flavor) {
		stored, err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).GetSchemaComponentVersion(ctx, component.component)
		if err != nil {
			return err
		}
		if stored != 0 {
			continue
		}
		if err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).SetSchemaComponentVersion(ctx, component.component, component.version); err != nil {
			return err
		}
	}
	return nil
}

// writeTrackedSchemaVersions forcefully persists the current runtime versions and is used by destructive maintenance flows that intentionally replace all managed rows.
// writeTrackedSchemaVersions 用于强制写入当前运行时版本，供有意替换整套受管数据的破坏性维护流程复用。
func (r *maintenanceRepository) writeTrackedSchemaVersions(ctx context.Context) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	for _, component := range trackedSchemaComponents(r.shared.cfg.Flavor) {
		if err := (&Store{pool: r.shared.pool, cfg: r.shared.cfg}).SetSchemaComponentVersion(ctx, component.component, component.version); err != nil {
			return err
		}
	}
	return nil
}
