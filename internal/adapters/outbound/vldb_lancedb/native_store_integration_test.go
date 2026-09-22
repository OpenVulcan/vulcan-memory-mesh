// native_store_integration_test.go exercises the official Rust LanceDB adapter when a DLL path is injected.
// native_store_integration_test.go 在注入 DLL 路径时验证官方 Rust LanceDB 适配器。
package vldb_lancedb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNativeStoreRoundTrip verifies startup deadline, schema health, row count, upsert, and canceled shutdown.
// TestNativeStoreRoundTrip 验证启动超时、Schema 健康检查、行数、upsert 及取消上下文下的关闭。
func TestNativeStoreRoundTrip(t *testing.T) {
	libraryPath := strings.TrimSpace(os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY"))
	if libraryPath == "" {
		t.Skip("VMM_NATIVE_LANCEDB_LIBRARY is not set")
	}
	store, err := NewNativeStore(
		libraryPath,
		t.TempDir(),
		5*time.Second,
		"vmm_memory_vectors",
		"vector",
		2,
	)
	if err != nil {
		t.Fatalf("open native store: %v", err)
	}
	if err := store.CheckHealth(context.Background()); err != nil {
		t.Fatalf("native store health: %v", err)
	}
	if err := store.Upsert(context.Background(), logicdomain.MemoryRecord{
		ID:        "native-1",
		Text:      "native row",
		Vector:    []float32{1, 0},
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]string{"source": "native-test"},
	}); err != nil {
		t.Fatalf("native store upsert: %v", err)
	}
	count, err := store.Count(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("native store count: count=%d err=%v", count, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Shutdown(canceled); err != nil {
		t.Fatalf("native store shutdown ignored canceled context: %v", err)
	}
	if _, err := store.Count(context.Background()); err == nil {
		t.Fatal("closed native store still accepted count")
	}
}
