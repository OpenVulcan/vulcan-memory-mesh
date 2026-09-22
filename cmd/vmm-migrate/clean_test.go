// clean_test.go verifies maintenance cleanup target parsing for the standalone migration tool.
// clean_test.go 用于验证独立迁移工具的维护清理目标解析。
package main

import (
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestParseMaintenanceCleanSelectionAcceptsSingleAndCombinedTargets verifies one CLI value can address one backend or the supported backend combinations.
// TestParseMaintenanceCleanSelectionAcceptsSingleAndCombinedTargets 用于验证单个 CLI 值既可以指向单个后端，也可以指向受支持的后端组合。
func TestParseMaintenanceCleanSelectionAcceptsSingleAndCombinedTargets(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		expected maintenanceCleanSelection
	}{
		{name: "sqlite only", raw: "sqlite", expected: maintenanceCleanSelection{SQLite: true}},
		{name: "lancedb only", raw: "lancedb", expected: maintenanceCleanSelection{LanceDB: true}},
		{name: "postgres only", raw: "postgres", expected: maintenanceCleanSelection{Postgres: true}},
		{name: "all keyword", raw: "all", expected: maintenanceCleanSelection{All: true, SQLite: true, LanceDB: true, Postgres: true}},
		{name: "comma separated", raw: "sqlite,lancedb", expected: maintenanceCleanSelection{SQLite: true, LanceDB: true}},
		{name: "plus separated with spaces", raw: " sqlite + postgres ", expected: maintenanceCleanSelection{SQLite: true, Postgres: true}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMaintenanceCleanSelection(tc.raw)
			if err != nil {
				t.Fatalf("parseMaintenanceCleanSelection returned error: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("selection = %+v, want %+v", got, tc.expected)
			}
		})
	}
}

// TestParseMaintenanceCleanSelectionRejectsUnsupportedTargets verifies invalid cleanup values fail before any destructive gateway call starts.
// TestParseMaintenanceCleanSelectionRejectsUnsupportedTargets 用于验证非法清理值会在任何破坏性网关调用开始前直接失败。
func TestParseMaintenanceCleanSelectionRejectsUnsupportedTargets(t *testing.T) {
	for _, raw := range []string{"", "   ", "duckdb", "duckdb,postgres", "pg"} {
		if _, err := parseMaintenanceCleanSelection(raw); err == nil {
			t.Fatalf("expected parse failure for %q", raw)
		}
	}
}

// TestUsesOwnedMaintenanceStorageKeepsNativeBackendsUnderThePairOwner verifies native and controller local cleanup never falls through to independent gateway opens.
// TestUsesOwnedMaintenanceStorageKeepsNativeBackendsUnderThePairOwner 用于验证 native 与 controller 的本地清理不会退回独立网关打开路径。
func TestUsesOwnedMaintenanceStorageKeepsNativeBackendsUnderThePairOwner(t *testing.T) {
	selection := maintenanceCleanSelection{SQLite: true, LanceDB: true}
	cases := []struct {
		name string
		mode string
		want bool
	}{
		{name: "native", mode: "native", want: true},
		{name: "controller", mode: "controller", want: true},
		{name: "split", mode: "split", want: false},
		{name: "combined", mode: "combined", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultLocal()
			cfg.Storage.Mode = tc.mode
			if got := usesOwnedMaintenanceStorage(cfg, selection); got != tc.want {
				t.Fatalf("usesOwnedMaintenanceStorage(%q) = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}

	cfg := config.DefaultLocal()
	cfg.Storage.Mode = "native"
	if usesOwnedMaintenanceStorage(cfg, maintenanceCleanSelection{Postgres: true}) {
		t.Fatal("postgres-only cleanup should not open the native pair owner")
	}
}
