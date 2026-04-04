// debug_migrate_test.go verifies the debug-only migration selector parsing used by the local entrypoint.
// debug_migrate_test.go 用于验证本地入口使用的调试迁移动作解析逻辑。
package main

import "testing"

// TestParseDebugMigrateTargetAcceptsSplitToCombined verifies the current maintenance command accepts the only documented split-to-combined migration action.
// TestParseDebugMigrateTargetAcceptsSplitToCombined 用于验证当前维护命令会接受唯一已文档化的 split-to-combined 迁移动作。
func TestParseDebugMigrateTargetAcceptsSplitToCombined(t *testing.T) {
	got, err := parseDebugMigrateTarget(" split-to-combined ")
	if err != nil {
		t.Fatalf("parse debug-migrate target: %v", err)
	}
	if got != debugMigrateSplitToCombined {
		t.Fatalf("target = %q, want %q", got, debugMigrateSplitToCombined)
	}
}

// TestParseDebugMigrateTargetRejectsUnsupportedModes verifies unknown migration actions fail before any destructive storage workflow starts.
// TestParseDebugMigrateTargetRejectsUnsupportedModes 用于验证未知迁移动作会在任何破坏性存储流程启动前直接失败。
func TestParseDebugMigrateTargetRejectsUnsupportedModes(t *testing.T) {
	for _, raw := range []string{"", " ", "combined-to-split", "sqlite-only"} {
		if _, err := parseDebugMigrateTarget(raw); err == nil {
			t.Fatalf("expected parse failure for %q", raw)
		}
	}
}
