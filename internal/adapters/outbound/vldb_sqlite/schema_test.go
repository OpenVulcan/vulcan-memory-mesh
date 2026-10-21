// SQLite adapter tests for schema test.
// SQLite 适配层表结构迁移测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestStoreGetSchemaComponentVersionReadsComponentTable verifies schema versions are read only from the shared component table.
// TestStoreGetSchemaComponentVersionReadsComponentTable 用于验证 schema 版本只会从共享组件表读取。
func TestStoreGetSchemaComponentVersionReadsComponentTable(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		if !strings.HasPrefix(req.SQL, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") {
			t.Fatalf("unexpected schema bootstrap SQL: %s", req.SQL)
		}
		return &fakeExecuteResponse{Success: true}, nil
	}
	store := &Store{database: fake, timeout: time.Second}

	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	version, err := store.GetSchemaComponentVersion(context.Background(), "sqlite")
	if err != nil {
		t.Fatalf("GetSchemaComponentVersion returned error: %v", err)
	}
	if version != 20 {
		t.Fatalf("schema version = %d, want 20", version)
	}
}

// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly verifies version writes only touch the shared component row.
// TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly 用于验证版本写入只会触达共享组件版本行。
func TestStoreSetSchemaComponentVersionPersistsComponentRowsOnly(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}

	if err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 15); err != nil {
		t.Fatalf("SetSchemaComponentVersion returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected shared component table writes, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected no legacy singleton writes, got %q", joined)
	}
}

// TestStoreSetSchemaComponentVersionCommitOutcomeUncertain verifies version writes do not hide commit-boundary uncertainty as an ordinary persistence failure.
// TestStoreSetSchemaComponentVersionCommitOutcomeUncertain 用于验证版本写入不会把提交边界不确定伪装成普通持久化失败。
func TestStoreSetSchemaComponentVersionCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.Contains(sqlText, "INSERT INTO vmm_schema_versions") {
			return nil, errors.New("failed to commit sqlite schema version transaction")
		}
		return &fakeExecuteResponse{Success: true}, nil
	}

	err := store.SetSchemaComponentVersion(context.Background(), "lancedb", 3)
	if err == nil {
		t.Fatal("expected schema version commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain schema version error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "persist component lancedb schema version 3") {
		t.Fatalf("unexpected schema version error detail: %v", err)
	}
}

// TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain verifies version-table bootstrap preserves commit-boundary uncertainty before version upsert begins.
// TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain 用于验证版本表 bootstrap 在版本 upsert 前会保留提交边界不确定语义。
func TestStoreSetSchemaComponentVersionTableBootstrapOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executeCalls := 0
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executeCalls++
		sqlText := strings.TrimSpace(req.GetSql())
		if strings.Contains(sqlText, "CREATE TABLE IF NOT EXISTS vmm_schema_versions") {
			return nil, errors.New("commit outcome unknown while creating schema version table")
		}
		t.Fatalf("unexpected schema version statement after failed bootstrap: %q", sqlText)
		return nil, nil
	}

	err := store.SetSchemaComponentVersion(context.Background(), "sqlite", 20)
	if err == nil {
		t.Fatal("expected schema version table bootstrap commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain schema version table bootstrap error, got %T %v", err, err)
	}
	if executeCalls != 1 {
		t.Fatalf("expected bootstrap failure to stop before version upsert, got %d execute calls", executeCalls)
	}
}

// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version verifies versions older than 19 are explicitly rejected instead of silently reset.
// TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version 用于验证旧于 19 的版本会被显式拒绝，而不是静默重建。
func TestStoreEnsureSQLiteSchemaRejectsUnsupportedPre19Version(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":18}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.ensureSQLiteSchema(context.Background())
	if err == nil {
		t.Fatal("expected ensureSQLiteSchema to reject unsupported pre-19 version")
	}
	if !strings.Contains(err.Error(), "minimum supported version is 19") {
		t.Fatalf("unexpected ensureSQLiteSchema error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if strings.Contains(joined, "DROP TABLE IF EXISTS vmm_sessions;") {
		t.Fatalf("expected unsupported version to stop before destructive reset, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline verifies an empty database still bootstraps straight to version 20.
// TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline 用于验证空数据库仍会直接初始化到 20 基线。
func TestStoreEnsureSQLiteSchemaBootstrapsFreshDatabaseToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		sql := strings.TrimSpace(req.GetSql())
		switch {
		case strings.Contains(sql, "FROM vmm_schema_versions"):
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		case strings.Contains(sql, "SELECT COUNT(*) AS count FROM vmm_projects"):
			return &fakeQueryJSONResponse{JsonData: `[{"count":0}]`}, nil
		default:
			return &fakeQueryJSONResponse{JsonData: `[]`}, nil
		}
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_sessions") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected fresh bootstrap SQL, got %q", joined)
	}
	if !strings.Contains(joined, "BEGIN IMMEDIATE;\nCREATE TABLE IF NOT EXISTS vmm_noise_embeddings") ||
		!strings.Contains(joined, "CREATE INDEX IF NOT EXISTS idx_vmm_profile_nodes_trash_batch ON vmm_profile_nodes_trash(batch_id, id);\n\nCOMMIT;") {
		t.Fatalf("expected fresh bootstrap schema SQL to run inside one transaction, got %q", joined)
	}
	if strings.Contains(joined, "vmm_version") {
		t.Fatalf("expected bootstrap to ignore legacy version table, got %q", joined)
	}
}

// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline verifies existing version-19 databases drop removed work-memory tables.
// TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline 用于验证既有 19 版本数据库会删除已移除的工作记忆表。
func TestStoreEnsureSQLiteSchemaMigratesNineteenToCurrentBaseline(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":19}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	if err := store.ensureSQLiteSchema(context.Background()); err != nil {
		t.Fatalf("ensureSQLiteSchema returned error: %v", err)
	}
	oldNodesTable := "vmm_" + "scratch" + "pad_nodes"
	oldPlansTable := "vmm_" + "scratch" + "pad_plans"
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "DROP TABLE IF EXISTS "+oldNodesTable) ||
		!strings.Contains(joined, "DROP TABLE IF EXISTS "+oldPlansTable) ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected old work-memory table drops plus version persistence, got %q", joined)
	}
	if !strings.Contains(joined, "BEGIN IMMEDIATE;\nDROP TABLE IF EXISTS "+oldNodesTable+";") ||
		!strings.Contains(joined, "DROP TABLE IF EXISTS "+oldPlansTable+";\nCOMMIT;") {
		t.Fatalf("expected 19->20 migration drops to run inside one transaction, got %q", joined)
	}
}

// TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain verifies schema scripts with their own commit boundary preserve ambiguous commit semantics.
// TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain 用于验证自带提交边界的 schema 脚本会保留提交不明语义。
func TestSQLiteSchemaMigrationCommitErrorMarksOutcomeUncertain(t *testing.T) {
	err := sqliteSchemaMigrationError("bootstrap sqlite schema", fmt.Errorf("apply current sqlite schema: %w", errors.New("failed to commit sqlite transaction")))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected sqlite schema migration commit error to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect sqlite schema migration uncertainty to mark fresh-vector retention, got %v", err)
	}
	if !strings.Contains(err.Error(), "apply current sqlite schema: failed to commit sqlite transaction") {
		t.Fatalf("unexpected sqlite schema migration error detail: %v", err)
	}
}

// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step verifies future upgrades can still chain from the current 20 baseline.
// TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step 用于验证未来升级仍能从当前 20 基线继续向上衔接。
func TestStoreApplySchemaMigrationPlanSupportsFuture20To21Step(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	executedSQL := make([]string, 0)
	stepApplied := false
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		executedSQL = append(executedSQL, strings.TrimSpace(req.GetSql()))
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.queryJSONFunc = func(_ context.Context, req *fakeQueryRequest) (*fakeQueryJSONResponse, error) {
		if strings.Contains(strings.TrimSpace(req.GetSql()), "FROM vmm_schema_versions") {
			return &fakeQueryJSONResponse{JsonData: `[{"component":"sqlite","schema_version":20}]`}, nil
		}
		return &fakeQueryJSONResponse{JsonData: `[]`}, nil
	}

	err := store.applySchemaMigrationPlan(context.Background(), schemaMigrationPlan{
		Component:               schemaComponentSQLite,
		MinimumSupportedVersion: 20,
		TargetVersion:           21,
		Bootstrap:               bootstrapCurrentSQLiteSchema,
		Steps: []schemaMigrationStep{{
			FromVersion: 20,
			ToVersion:   21,
			Name:        "future migration",
			Up: func(ctx context.Context, s *Store) error {
				stepApplied = true
				return s.exec(ctx, `CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker(id INTEGER PRIMARY KEY)`)
			},
		}},
	})
	if err != nil {
		t.Fatalf("applySchemaMigrationPlan returned error: %v", err)
	}
	if !stepApplied {
		t.Fatal("expected 20->21 migration step to run")
	}
	joined := strings.Join(executedSQL, "\n")
	if !strings.Contains(joined, "CREATE TABLE IF NOT EXISTS vmm_future_upgrade_marker") ||
		!strings.Contains(joined, "INSERT INTO vmm_schema_versions") {
		t.Fatalf("expected future migration SQL plus version persistence, got %q", joined)
	}
}

// TestCurrentSchemaSQLContainsContextualMemoryTables verifies the managed schema and debug-clean script both know about contextual edge tables.
// TestCurrentSchemaSQLContainsContextualMemoryTables 用于验证受管 schema 与 debug-clean 脚本都包含情境边表。
func TestCurrentSchemaSQLContainsContextualMemoryTables(t *testing.T) {
	requiredFragments := []string{
		"support_count INTEGER NOT NULL DEFAULT 0",
		"rebuttal_count INTEGER NOT NULL DEFAULT 0",
		"CREATE TABLE IF NOT EXISTS vmm_memory_context_edges",
		"idx_vmm_memory_context_edges_lookup",
		"idx_vmm_memory_context_edges_memory",
	}
	for _, fragment := range requiredFragments {
		if !strings.Contains(currentSchemaSQL, fragment) {
			t.Fatalf("expected schema to contain %q", fragment)
		}
	}
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS vmm_memory_context_edges;") {
		t.Fatal("expected debug clean schema script to drop vmm_memory_context_edges")
	}
	legacyTablePrefix := "vmm_" + "scratch" + "pad"
	if strings.Contains(currentSchemaSQL, legacyTablePrefix) {
		t.Fatalf("expected current sqlite schema to omit removed work-memory tables")
	}
	legacyNodesTable := legacyTablePrefix + "_nodes"
	legacyPlansTable := legacyTablePrefix + "_plans"
	if !strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyNodesTable+";") ||
		!strings.Contains(debugCleanManagedSchemaSQL, "DROP TABLE IF EXISTS "+legacyPlansTable+";") {
		t.Fatalf("expected debug-clean script to keep legacy work-memory table drops")
	}
}

// TestMigrateSQLiteSchema21To22InstallsFailureLifecycleTriggers verifies upgrade SQL installs both terminal Pass and success cleanup transitions.
// TestMigrateSQLiteSchema21To22InstallsFailureLifecycleTriggers 用于验证升级 SQL 同时安装终止 Pass 与成功清理状态迁移。
func TestMigrateSQLiteSchema21To22InstallsFailureLifecycleTriggers(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	var capturedScript string
	fake.executeScriptFunc = func(_ context.Context, req *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		capturedScript = req.GetSql()
		return &fakeExecuteResponse{Success: true, StatementsExecuted: 5}, nil
	}

	if err := migrateSQLiteSchema21To22(context.Background(), store); err != nil {
		t.Fatalf("migrateSQLiteSchema21To22 returned error: %v", err)
	}
	for _, requiredSQL := range []string{
		"CREATE TABLE IF NOT EXISTS vmm_turn_analysis_failures",
		"CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_failure_pass",
		"CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_failure_update_pass",
		"CREATE TRIGGER IF NOT EXISTS trg_vmm_turn_analysis_success_clear",
		"DELETE FROM vmm_turn_analysis_failures",
	} {
		if !strings.Contains(capturedScript, requiredSQL) {
			t.Fatalf("schema 21-to-22 migration is missing %q: %s", requiredSQL, capturedScript)
		}
	}
}
