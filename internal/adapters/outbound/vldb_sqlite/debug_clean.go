// debug_clean.go implements the debug-only gateway cleanup helper for managed SQLite tables.
// debug_clean.go 用于实现调试专用的网关清理辅助逻辑，负责清空受管 SQLite 表。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

const debugCleanManagedSchemaSQL = `
BEGIN IMMEDIATE;
DROP TABLE IF EXISTS vmm_profile_nodes;
DROP TABLE IF EXISTS vmm_profile_instructions;
DROP TABLE IF EXISTS vmm_turn_records_trash;
DROP TABLE IF EXISTS vmm_memory_context_edges_trash;
DROP TABLE IF EXISTS vmm_memory_nodes_trash;
DROP TABLE IF EXISTS vmm_recycle_batches;
DROP TABLE IF EXISTS vmm_recycle_jobs;
DROP TABLE IF EXISTS vmm_vector_gc_jobs;
DROP TABLE IF EXISTS vmm_memory_nodes_fts;
DROP TABLE IF EXISTS vmm_memory_context_edges;
DROP TABLE IF EXISTS vmm_memory_nodes;
DROP TABLE IF EXISTS vmm_turn_records;
DROP TABLE IF EXISTS vmm_chat_messages;
DROP TABLE IF EXISTS vmm_memory_entries;
DROP TABLE IF EXISTS vmm_scratchpad_nodes;
DROP TABLE IF EXISTS vmm_scratchpad_plans;
DROP TABLE IF EXISTS vmm_sessions;
DROP TABLE IF EXISTS vmm_projects;
DROP TABLE IF EXISTS vmm_spaces;
DROP TABLE IF EXISTS vmm_teams;
DROP TABLE IF EXISTS vmm_users;
DROP TABLE IF EXISTS vmm_noise_embeddings;
DROP TABLE IF EXISTS vmm_version;
DROP TABLE IF EXISTS vmm_schema_versions;
COMMIT;
`

// DebugCleanManagedSchema opens the local SQLite FFI library, drops all VMM-managed tables, and then returns immediately.
// DebugCleanManagedSchema 用于打开本地 SQLite FFI 动态库，删除所有 VMM 受管表，然后立即返回。
func DebugCleanManagedSchema(ctx context.Context, libraryPath string, databasePath string, timeout time.Duration) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if libraryPath == "" {
		return fmt.Errorf("sqlite library path is required")
	}
	if databasePath == "" {
		return fmt.Errorf("sqlite database path is required")
	}
	lib, err := sqliteffi.Open(libraryPath)
	if err != nil {
		return fmt.Errorf("open sqlite library for debug clean: %w", err)
	}
	defer func() {
		_ = lib.Close()
	}()
	runtimeHandle, err := lib.CreateRuntime()
	if err != nil {
		return fmt.Errorf("create sqlite runtime for debug clean: %w", err)
	}
	defer func() {
		_ = runtimeHandle.Close()
	}()
	databaseHandle, err := runtimeHandle.OpenDatabase(databasePath)
	if err != nil {
		return fmt.Errorf("open sqlite database for debug clean: %w", err)
	}
	defer func() {
		_ = databaseHandle.Close()
	}()
	return debugCleanWithDatabase(ctx, databaseHandle, timeout)
}

// DebugCleanManagedSchema drops all VMM-managed tables through the database handle already owned by this store.
// DebugCleanManagedSchema 通过当前存储已经持有的数据库句柄删除全部 VMM 受管表。
func (s *Store) DebugCleanManagedSchema(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	return debugCleanWithDatabase(ctx, s.database, s.timeout)
}

// debugCleanWithDatabase sends the destructive cleanup script through an already prepared SQLite database handle.
// debugCleanWithDatabase 用于通过已准备好的 SQLite 数据库句柄发送破坏性清理脚本。
func debugCleanWithDatabase(ctx context.Context, database sqliteDatabaseHandle, timeout time.Duration) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if database == nil {
		return fmt.Errorf("sqlite database is not initialized")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := database.ExecuteScript(debugCleanManagedSchemaSQL, nil, "")
	if err != nil {
		return sqliteDebugCleanError(err)
	}
	if !resp.Success {
		return sqliteDebugCleanError(errors.New(resp.Message))
	}
	if err := checkSQLiteContext(callCtx); err != nil {
		return err
	}
	return nil
}

// sqliteDebugCleanError classifies destructive SQLite cleanup failures after the transactional drop script may have reached its commit boundary.
// sqliteDebugCleanError 用于分类事务化删表脚本可能已经到达提交边界后的 SQLite 破坏性清理失败。
func sqliteDebugCleanError(err error) error {
	if err == nil {
		return nil
	}
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: "clean sqlite managed schema",
			Message:   fmt.Sprintf("debug clean sqlite schema: %v", err),
		}
	}
	return fmt.Errorf("debug clean sqlite schema: %w", err)
}
