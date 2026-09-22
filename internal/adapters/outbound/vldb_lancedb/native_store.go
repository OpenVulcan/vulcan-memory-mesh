// native_store.go constructs the VMM adapter on the official Rust LanceDB cdylib.
// native_store.go 使用官方 Rust LanceDB cdylib 构造 VMM 适配器。
package vldb_lancedb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/platform/ffi/nativelance"
)

// NewNativeStore opens the official native LanceDB library and ensures the dimension-qualified table.
// NewNativeStore 打开官方原生 LanceDB 库并确保带维度后缀的目标表。
func NewNativeStore(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newNativeStore(libraryPath, databaseDir, timeout, tableName, vectorColumn, dimension, true)
}

// NewNativeStoreWithoutInit opens the official native library without creating a missing table.
// NewNativeStoreWithoutInit 打开官方原生库，但不创建缺失目标表。
func NewNativeStoreWithoutInit(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int) (*Store, error) {
	return newNativeStore(libraryPath, databaseDir, timeout, tableName, vectorColumn, dimension, false)
}

// newNativeStore validates the native storage contract and opens a runtime in the process-resident Rust DLL.
// newNativeStore 校验原生存储契约，并在进程驻留的 Rust DLL 中打开运行时。
func newNativeStore(libraryPath string, databaseDir string, timeout time.Duration, tableName, vectorColumn string, dimension int, ensureTable bool) (*Store, error) {
	if strings.TrimSpace(libraryPath) == "" {
		return nil, fmt.Errorf("native lancedb library path is required")
	}
	if strings.TrimSpace(databaseDir) == "" {
		return nil, fmt.Errorf("native lancedb database dir is required")
	}
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("lancedb table_name is required")
	}
	if strings.TrimSpace(vectorColumn) == "" {
		return nil, fmt.Errorf("lancedb vector_column is required")
	}
	if dimension <= 0 {
		return nil, fmt.Errorf("lancedb dimension must be > 0")
	}
	if err := os.MkdirAll(databaseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create native lancedb database dir: %w", err)
	}
	lib, err := nativelance.Open(strings.TrimSpace(libraryPath))
	if err != nil {
		return nil, err
	}
	runtimeHandle, err := lib.CreateRuntimeWithTimeout(strings.TrimSpace(databaseDir), timeout)
	if err != nil {
		_ = lib.Close()
		return nil, err
	}
	engine, err := runtimeHandle.OpenDefaultEngine()
	if err != nil {
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, fmt.Errorf("open native lancedb engine: %w", err)
	}
	store := &Store{
		lib:          lib,
		runtime:      runtimeHandle,
		engine:       engine,
		timeout:      timeout,
		tableName:    resolveVectorTableName(tableName, dimension),
		vectorColumn: strings.TrimSpace(vectorColumn),
		dimension:    dimension,
	}
	if ensureTable {
		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		if err := store.init(ctx); err != nil {
			_ = store.Shutdown(context.Background())
			return nil, err
		}
	}
	return store, nil
}
