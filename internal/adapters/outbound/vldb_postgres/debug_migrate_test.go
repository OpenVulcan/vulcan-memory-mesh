// debug_migrate_test.go verifies PostgreSQL debug migration error semantics without requiring a live PostgreSQL instance.
// debug_migrate_test.go 用于在不依赖真实 PostgreSQL 实例的情况下验证 PostgreSQL 调试迁移错误语义。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
)

// TestPostgresDebugImportOutcomeUncertainPreservesReport verifies destructive import failures after the commit boundary keep the attempted row-count report and surface outcome uncertainty.
// TestPostgresDebugImportOutcomeUncertainPreservesReport 用于验证破坏性导入在提交边界后的失败会保留尝试导入行数报告，并上报结果不确定。
func TestPostgresDebugImportOutcomeUncertainPreservesReport(t *testing.T) {
	report := storagemigrate.Report{
		Users:       2,
		MemoryNodes: 1,
	}
	gotReport, err := postgresDebugImportOutcomeUncertain(report, "commit postgres debug migration tx", errors.New("commit acknowledgement lost"))
	if gotReport != report {
		t.Fatalf("report = %+v, want %+v", gotReport, report)
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected debug import error to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect debug import uncertainty to mark fresh-vector retention, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres debug migration tx: commit acknowledgement lost") {
		t.Fatalf("unexpected debug import error detail: %v", err)
	}
}
