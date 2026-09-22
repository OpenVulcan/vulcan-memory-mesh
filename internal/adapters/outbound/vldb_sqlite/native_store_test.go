// native_store_test.go verifies the public native Store constructor against the complete relational schema.
// native_store_test.go 使用完整关系 schema 验证原生 Store 公开构造入口。
package vldb_sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestNewNativeStoreInitializesFullSchema checks an empty temporary target without touching repository data.
// TestNewNativeStoreInitializesFullSchema 在临时空目标上验证完整初始化，避免接触仓库真实数据。
func TestNewNativeStoreInitializesFullSchema(t *testing.T) {
	store, err := NewNativeStore(filepath.Join(t.TempDir(), "native-store.db"), time.Second, StoreOptions{TokenizerMode: "gse", SkipDebugSeed: true})
	if err != nil {
		t.Fatalf("open native store: %v", err)
	}
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown native store: %v", err)
		}
	}()
	if err := store.CheckHealth(context.Background()); err != nil {
		t.Fatalf("check native store health: %v", err)
	}
	rows, err := store.queryJSON(context.Background(), "SELECT COUNT(*) AS count FROM vmm_memory_nodes", nil)
	if err != nil {
		t.Fatalf("query native memory table: %v", err)
	}
	if rows.JSONData != `[{"count":0}]` {
		t.Fatalf("native migration target was not empty: %s", rows.JSONData)
	}
	if err := store.RebuildFTSIndex(context.Background()); err != nil {
		t.Fatalf("rebuild native store FTS: %v", err)
	}
}
