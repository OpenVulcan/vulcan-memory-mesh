// debug_clean_test.go verifies the debug-clean CLI target parsing used by the local entrypoint.
// debug_clean_test.go 用于验证本地入口所使用的 debug-clean 命令行目标解析。
package main

import "testing"

// TestParseDebugCleanSelectionAcceptsSingleAndCombinedTargets verifies one flag value can address one backend or the remaining supported combination.
// TestParseDebugCleanSelectionAcceptsSingleAndCombinedTargets 用于验证单个参数既可以指向一个后端，也可以指向当前剩余支持的组合。
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
			name:     "lancedb only",
			raw:      "lancedb",
			expected: debugCleanSelection{LanceDB: true},
		},
		{
			name:     "postgres only",
			raw:      "postgres",
			expected: debugCleanSelection{Postgres: true},
		},
		{
			name:     "all keyword",
			raw:      "all",
			expected: debugCleanSelection{All: true, SQLite: true, LanceDB: true, Postgres: true},
		},
		{
			name:     "comma separated",
			raw:      "sqlite,lancedb",
			expected: debugCleanSelection{SQLite: true, LanceDB: true},
		},
		{
			name:     "postgres plus sqlite",
			raw:      "postgres+sqlite",
			expected: debugCleanSelection{SQLite: true, Postgres: true},
		},
		{
			name:     "plus separated with spaces",
			raw:      " sqlite + lancedb ",
			expected: debugCleanSelection{SQLite: true, LanceDB: true},
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
		"duckdb",
		"duckdb,postgres",
		"pg",
	}

	for _, raw := range cases {
		if _, err := parseDebugCleanSelection(raw); err == nil {
			t.Fatalf("expected parse failure for %q", raw)
		}
	}
}
