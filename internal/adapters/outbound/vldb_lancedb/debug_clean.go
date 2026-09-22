// debug_clean.go implements the debug-only gateway cleanup helper for the configured LanceDB vector table.
// debug_clean.go 用于实现调试专用的网关清理辅助逻辑，负责删除配置指定的 LanceDB 向量表。
package vldb_lancedb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"
	"github.com/openvulcan/vmm/internal/platform/storagecontract/lance"
)

// DebugDropConfiguredTable opens the local LanceDB FFI library, drops the resolved runtime table, and treats missing tables as already clean.
// DebugDropConfiguredTable 用于打开本地 LanceDB FFI 动态库，删除解析后的运行时表，并把“表不存在”视为已经清理完成。
func DebugDropConfiguredTable(ctx context.Context, libraryPath string, databaseDir string, timeout time.Duration, baseTableName string, dimension int) (string, error) {
	if strings.TrimSpace(libraryPath) == "" {
		return "", fmt.Errorf("lancedb library path is required")
	}
	if strings.TrimSpace(databaseDir) == "" {
		return "", fmt.Errorf("lancedb database dir is required")
	}
	if strings.TrimSpace(baseTableName) == "" {
		return "", fmt.Errorf("lancedb table_name is required")
	}
	if dimension <= 0 {
		return "", fmt.Errorf("lancedb dimension must be > 0")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := checkContext(ctx); err != nil {
		return "", err
	}
	lib, err := lancedbffi.Open(strings.TrimSpace(libraryPath))
	if err != nil {
		return "", fmt.Errorf("open lancedb library for debug clean: %w", err)
	}
	defer func() {
		_ = lib.Close()
	}()
	options := lib.DefaultRuntimeOptions()
	options.DefaultDBPath = strings.TrimSpace(databaseDir)
	runtimeHandle, err := lib.CreateRuntime(options)
	if err != nil {
		return "", fmt.Errorf("create lancedb runtime for debug clean: %w", err)
	}
	defer func() {
		_ = runtimeHandle.Close()
	}()
	engine, err := runtimeHandle.OpenDefaultEngine()
	if err != nil {
		return "", fmt.Errorf("open lancedb engine for debug clean: %w", err)
	}
	defer func() {
		_ = engine.Close()
	}()

	tableName := resolveVectorTableName(baseTableName, dimension)
	if err := debugDropTableWithEngine(ctx, engine, tableName, timeout); err != nil {
		return "", err
	}
	return tableName, nil
}

// DebugDropConfiguredTable drops the configured table through the engine handle already owned by this store.
// DebugDropConfiguredTable 通过当前存储已经持有的引擎句柄删除配置指定的表。
func (s *Store) DebugDropConfiguredTable(ctx context.Context) (string, error) {
	engine, release, err := s.engineForContext(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if err := debugDropTableWithEngine(ctx, engine, s.tableName, s.timeout); err != nil {
		return "", err
	}
	return s.tableName, nil
}

// debugDropTableWithEngine sends one drop-table request through an already prepared LanceDB engine handle.
// debugDropTableWithEngine 用于通过已准备好的 LanceDB engine 句柄发送删表请求。
func debugDropTableWithEngine(ctx context.Context, rawEngine any, tableName string, timeout time.Duration) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if rawEngine == nil {
		return fmt.Errorf("lancedb engine is not initialized")
	}
	var engine lance.Engine
	switch value := rawEngine.(type) {
	case lance.Engine:
		engine = value
	case legacyLanceDBEngine:
		engine = legacyEngineAdapter{legacy: value}
	default:
		return fmt.Errorf("unsupported lancedb engine implementation %T", rawEngine)
	}
	if strings.TrimSpace(tableName) == "" {
		return fmt.Errorf("lancedb table_name is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := engine.DropTable(callCtx, lance.DropTableRequest{TableName: strings.TrimSpace(tableName)})
	if err != nil {
		if isTableNotFoundMessage(err.Error()) {
			return nil
		}
		return fmt.Errorf("drop lancedb table %s: %w", strings.TrimSpace(tableName), err)
	}
	if !resp.Success {
		if isTableNotFoundMessage(resp.Message) {
			return nil
		}
		return fmt.Errorf("drop lancedb table %s: %s", strings.TrimSpace(tableName), strings.TrimSpace(resp.Message))
	}
	return nil
}

// isTableNotFoundMessage matches the gateway messages used when DropTable is retried on a missing table.
// isTableNotFoundMessage 用于匹配网关在重复删除缺失表时返回的典型消息。
func isTableNotFoundMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "not found") || strings.Contains(normalized, "does not exist")
}
