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

// TestValidateTrackedSchemaVersionAllowsBootstrapButRejectsDrift verifies missing bootstrap rows are tolerated while older/newer tracked versions fail fast.
// TestValidateTrackedSchemaVersionAllowsBootstrapButRejectsDrift 用于验证缺失的初始版本记录会被允许，而过旧或过新的已记录版本会快速失败。
func TestValidateTrackedSchemaVersionAllowsBootstrapButRejectsDrift(t *testing.T) {
	testCases := []struct {
		name      string
		stored    int
		current   int
		wantError bool
	}{
		{name: "missing bootstrap row is allowed", stored: 0, current: 1, wantError: false},
		{name: "matching version is allowed", stored: 1, current: 1, wantError: false},
		{name: "older version fails", stored: 1, current: 2, wantError: true},
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
