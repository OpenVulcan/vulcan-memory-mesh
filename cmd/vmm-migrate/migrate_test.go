// migrate_test.go verifies maintenance migration target parsing for the standalone migration tool.
// migrate_test.go 用于验证独立迁移工具的维护迁移动作解析。
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
)

// TestParseMaintenanceMigrateTargetAcceptsSplitToCombined verifies the current maintenance command accepts the documented split-to-combined migration action.
// TestParseMaintenanceMigrateTargetAcceptsSplitToCombined 用于验证当前维护命令会接受已文档化的 split-to-combined 迁移动作。
func TestParseMaintenanceMigrateTargetAcceptsSplitToCombined(t *testing.T) {
	got, err := parseMaintenanceMigrateTarget(" split-to-combined ")
	if err != nil {
		t.Fatalf("parseMaintenanceMigrateTarget returned error: %v", err)
	}
	if got != maintenanceMigrateSplitToCombined {
		t.Fatalf("target = %q, want %q", got, maintenanceMigrateSplitToCombined)
	}
}

// TestParseMaintenanceMigrateTargetRejectsUnsupportedModes verifies unknown migration actions fail before any destructive storage workflow starts.
// TestParseMaintenanceMigrateTargetRejectsUnsupportedModes 用于验证未知迁移动作会在任何破坏性存储流程启动前直接失败。
func TestParseMaintenanceMigrateTargetRejectsUnsupportedModes(t *testing.T) {
	for _, raw := range []string{"", " ", "combined-to-split", "sqlite-only"} {
		if _, err := parseMaintenanceMigrateTarget(raw); err == nil {
			t.Fatalf("expected parse failure for %q", raw)
		}
	}
}

// TestBuildPostgresMaintenanceConfigCarriesMaintenanceTimeouts verifies standalone migration wiring forwards the dedicated maintenance read/write budgets into the PostgreSQL maintenance config instead of silently falling back to adapter defaults.
// TestBuildPostgresMaintenanceConfigCarriesMaintenanceTimeouts 用于验证独立迁移装配会把专用维护读写预算传给 PostgreSQL 维护配置，而不是静默回退到适配器默认值。
func TestBuildPostgresMaintenanceConfigCarriesMaintenanceTimeouts(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Postgres.DSN = "postgres://tester:secret@localhost:5432/vmm"
	cfg.Postgres.Schema = "tenant_a"
	cfg.Embedding.Dimension = 1024
	cfg.MaintenanceTool.Postgres.ReadTimeout = config.Duration{Duration: 45 * time.Second}
	cfg.MaintenanceTool.Postgres.WriteTimeout = config.Duration{Duration: 12 * time.Minute}

	postgresCfg, err := buildPostgresMaintenanceConfig(cfg)
	if err != nil {
		t.Fatalf("buildPostgresMaintenanceConfig returned error: %v", err)
	}
	if got, want := postgresCfg.MaintenanceReadTimeout, 45*time.Second; got != want {
		t.Fatalf("maintenance read timeout = %v, want %v", got, want)
	}
	if got, want := postgresCfg.MaintenanceWriteTimeout, 12*time.Minute; got != want {
		t.Fatalf("maintenance write timeout = %v, want %v", got, want)
	}
}

// TestFormatMigrationReportErrorDetailIncludesTotalAndTables verifies failed imports can still expose the same row-count facts that success output prints.
// TestFormatMigrationReportErrorDetailIncludesTotalAndTables 用于验证失败导入仍可暴露成功输出中相同的行数事实。
func TestFormatMigrationReportErrorDetailIncludesTotalAndTables(t *testing.T) {
	detail := formatMigrationReportErrorDetail(storagemigrate.Report{
		Users:       2,
		MemoryNodes: 1,
	})
	for _, want := range []string{"total_rows=3", "users=2", "memory_nodes=1", "profile_instructions=0"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("report error detail %q missing %q", detail, want)
		}
	}
}
