// debug_clean_test.go verifies the debug-clean CLI target parsing used by the local entrypoint.
// debug_clean_test.go 用于验证本地入口所使用的 debug-clean 命令行目标解析。
package main

import "testing"

// TestParseDebugCleanSelectionAcceptsSingleAndCombinedTargets verifies one flag value can address one backend or both backends.
// TestParseDebugCleanSelectionAcceptsSingleAndCombinedTargets 用于验证单个参数既可以指向一个后端，也可以同时指向两个后端。
func TestParseDebugCleanSelectionAcceptsSingleAndCombinedTargets(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		expected debugCleanSelection
	}{
		{
			name:     "sqlite only",
			raw:      "sqlite",
			expected: debugCleanSelection{SQLite: true},
		},
		{
			name:     "duckdb only",
			raw:      "duckdb",
			expected: debugCleanSelection{DuckDB: true},
		},
		{
			name:     "lancedb only",
			raw:      "lancedb",
			expected: debugCleanSelection{LanceDB: true},
		},
		{
			name:     "all keyword",
			raw:      "all",
			expected: debugCleanSelection{All: true, LanceDB: true},
		},
		{
			name:     "comma separated",
			raw:      "sqlite,lancedb",
			expected: debugCleanSelection{SQLite: true, LanceDB: true},
		},
		{
			name:     "plus separated with spaces",
			raw:      " sqlite + duckdb ",
			expected: debugCleanSelection{SQLite: true, DuckDB: true},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDebugCleanSelection(tc.raw)
			if err != nil {
				t.Fatalf("parse debug-clean selection: %v", err)
			}
			if got != tc.expected {
				t.Fatalf("selection = %+v, want %+v", got, tc.expected)
			}
		})
	}
}

// TestParseDebugCleanSelectionRejectsUnsupportedTargets verifies invalid debug-clean values fail fast before any gateway call starts.
// TestParseDebugCleanSelectionRejectsUnsupportedTargets 用于验证非法的 debug-clean 参数会在发起任何网关调用前快速失败。
func TestParseDebugCleanSelectionRejectsUnsupportedTargets(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"duckdb,postgres",
	}

	for _, raw := range cases {
		if _, err := parseDebugCleanSelection(raw); err == nil {
			t.Fatalf("expected parse failure for %q", raw)
		}
	}
}
