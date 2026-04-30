// schema_version_test.go verifies PostgreSQL combined-store schema version helpers without requiring a live database connection.
// schema_version_test.go 用于验证 PostgreSQL 组合库的 schema 版本辅助逻辑，且无需真实数据库连接。
package vldb_postgres

import "testing"

// TestTrackedSchemaComponentsReturnsSharedAndFlavorEntries verifies the runtime always tracks one shared component plus one flavor-scoped search component.
// TestTrackedSchemaComponentsReturnsSharedAndFlavorEntries 用于验证运行时始终会跟踪一条共享版本记录和一条 flavor 作用域的搜索版本记录。
func TestTrackedSchemaComponentsReturnsSharedAndFlavorEntries(t *testing.T) {
	components := trackedSchemaComponents("standard")
	if len(components) != 2 {
		t.Fatalf("trackedSchemaComponents length = %d, want 2", len(components))
	}
	if components[0].component != defaultSchemaVersionComponent || components[0].version != currentCombinedSchemaVersion {
		t.Fatalf("trackedSchemaComponents shared component = %+v", components[0])
	}
	if components[1].component != "postgres_combined_search_standard" || components[1].version != currentCombinedSearchSchemaVersion {
		t.Fatalf("trackedSchemaComponents search component = %+v", components[1])
	}
}

// TestSearchSchemaVersionComponentDefaultsToParadeDB verifies blank or noisy flavor strings still resolve to the stable ParadeDB search-version key.
// TestSearchSchemaVersionComponentDefaultsToParadeDB 用于验证空值或带噪声的 flavor 字符串仍会落到稳定的 ParadeDB 搜索版本键。
func TestSearchSchemaVersionComponentDefaultsToParadeDB(t *testing.T) {
	testCases := []struct {
		name   string
		flavor string
		want   string
	}{
		{name: "blank uses paradedb", flavor: "", want: "postgres_combined_search_paradedb"},
		{name: "whitespace uses paradedb", flavor: "   ", want: "postgres_combined_search_paradedb"},
		{name: "normalized standard", flavor: " Standard ", want: "postgres_combined_search_standard"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchSchemaVersionComponent(tc.flavor); got != tc.want {
				t.Fatalf("searchSchemaVersionComponent(%q) = %q, want %q", tc.flavor, got, tc.want)
			}
		})
	}
}

// TestValidateTrackedSchemaVersionAllowsBootstrapAndMigratableOlderVersions verifies missing rows and older rows can continue into the explicit migration stage while impossible newer rows still fail fast.
// TestValidateTrackedSchemaVersionAllowsBootstrapAndMigratableOlderVersions 用于验证缺失行和旧版本行都可以继续进入显式迁移阶段，而不可能的新版本仍会快速失败。
func TestValidateTrackedSchemaVersionAllowsBootstrapAndMigratableOlderVersions(t *testing.T) {
	testCases := []struct {
		name      string
		stored    int
		current   int
		wantError bool
	}{
		{name: "missing bootstrap row is allowed", stored: 0, current: 3, wantError: false},
		{name: "matching version is allowed", stored: 3, current: 3, wantError: false},
		{name: "older version is allowed for migration", stored: 2, current: 3, wantError: false},
		{name: "newer version fails", stored: 2, current: 1, wantError: true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTrackedSchemaVersion("postgres_combined", tc.stored, tc.current)
			if tc.wantError && err == nil {
				t.Fatalf("validateTrackedSchemaVersion error = nil, want non-nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("validateTrackedSchemaVersion error = %v, want nil", err)
			}
		})
	}
}

// TestTrackedSchemaMigrationStepsIncludeRemovedWorkMemoryCleanup verifies older combined-schema versions can upgrade to the current cleanup baseline.
// TestTrackedSchemaMigrationStepsIncludeRemovedWorkMemoryCleanup 用于验证较旧组合库 schema 版本可以升级到当前清理基线。
func TestTrackedSchemaMigrationStepsIncludeRemovedWorkMemoryCleanup(t *testing.T) {
	steps := trackedSchemaMigrationSteps("standard")
	foundFromOne := false
	foundFromTwo := false
	for _, step := range steps {
		if step.Component != defaultSchemaVersionComponent {
			continue
		}
		if step.FromVersion == 1 && step.ToVersion == 3 {
			foundFromOne = true
		}
		if step.FromVersion == 2 && step.ToVersion == 3 {
			foundFromTwo = true
		}
	}
	if !foundFromOne || !foundFromTwo {
		t.Fatalf("trackedSchemaMigrationSteps() missing postgres_combined cleanup paths: from1=%t from2=%t", foundFromOne, foundFromTwo)
	}
}

// TestResolveTrackedSchemaMigrationPathBuildsSequentialUpgrade verifies one older PostgreSQL tracked version resolves into the declared sequential migration path.
// TestResolveTrackedSchemaMigrationPathBuildsSequentialUpgrade 用于验证较旧 PostgreSQL 版本会被解析成声明好的顺序迁移路径。
func TestResolveTrackedSchemaMigrationPathBuildsSequentialUpgrade(t *testing.T) {
	steps := trackedSchemaMigrationSteps("standard")
	path, err := resolveTrackedSchemaMigrationPath(defaultSchemaVersionComponent, 1, 3, steps)
	if err != nil {
		t.Fatalf("resolveTrackedSchemaMigrationPath error = %v, want nil", err)
	}
	if len(path) != 1 {
		t.Fatalf("resolveTrackedSchemaMigrationPath length = %d, want 1", len(path))
	}
	if path[0].FromVersion != 1 || path[0].ToVersion != 3 {
		t.Fatalf("resolveTrackedSchemaMigrationPath[0] = %+v, want 1 -> 3", path[0])
	}
}

// TestResolveTrackedSchemaMigrationPathBuildsCurrentCleanupUpgrade verifies version-2 stores receive the current cleanup step.
// TestResolveTrackedSchemaMigrationPathBuildsCurrentCleanupUpgrade 用于验证 2 版本库存会执行当前清理升级步骤。
func TestResolveTrackedSchemaMigrationPathBuildsCurrentCleanupUpgrade(t *testing.T) {
	steps := trackedSchemaMigrationSteps("standard")
	path, err := resolveTrackedSchemaMigrationPath(defaultSchemaVersionComponent, 2, 3, steps)
	if err != nil {
		t.Fatalf("resolveTrackedSchemaMigrationPath error = %v, want nil", err)
	}
	if len(path) != 1 {
		t.Fatalf("resolveTrackedSchemaMigrationPath length = %d, want 1", len(path))
	}
	if path[0].FromVersion != 2 || path[0].ToVersion != 3 {
		t.Fatalf("resolveTrackedSchemaMigrationPath[0] = %+v, want 2 -> 3", path[0])
	}
}

// TestResolveTrackedSchemaMigrationPathRejectsMissingStep verifies startup still fails deterministically when one tracked component is older than the runtime but no explicit migration path exists.
// TestResolveTrackedSchemaMigrationPathRejectsMissingStep 用于验证当某个跟踪组件版本落后于运行时、却没有显式迁移路径时，启动仍会确定性失败。
func TestResolveTrackedSchemaMigrationPathRejectsMissingStep(t *testing.T) {
	_, err := resolveTrackedSchemaMigrationPath("postgres_combined_search_standard", 1, 2, trackedSchemaMigrationSteps("standard"))
	if err == nil {
		t.Fatalf("resolveTrackedSchemaMigrationPath error = nil, want non-nil")
	}
}

// TestPostgresManagedTableNamesForDebugCleanIncludesLegacyWorkMemoryTables verifies debug-clean still removes historical tables that are no longer created by the current schema.
// TestPostgresManagedTableNamesForDebugCleanIncludesLegacyWorkMemoryTables 用于验证 debug-clean 仍会删除当前 schema 不再创建的历史表。
func TestPostgresManagedTableNamesForDebugCleanIncludesLegacyWorkMemoryTables(t *testing.T) {
	legacyNodesTable := "vmm_" + "scratch" + "pad_nodes"
	legacyPlansTable := "vmm_" + "scratch" + "pad_plans"
	foundNodes := false
	foundPlans := false
	for _, tableName := range postgresManagedTableNamesForDebugClean() {
		if tableName == legacyNodesTable {
			foundNodes = true
		}
		if tableName == legacyPlansTable {
			foundPlans = true
		}
	}
	if !foundNodes || !foundPlans {
		t.Fatalf("postgres debug-clean legacy work-memory tables: nodes=%t plans=%t", foundNodes, foundPlans)
	}
}
