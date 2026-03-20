// store_test.go implements the SQLite-backed outbound archive adapter tests.
// store_test.go 用于实现基于 SQLite 的出站归档适配器测试。
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNewStoreCreatesSchemaAndPersistsMemories verifies the TestNewStoreCreatesSchemaAndPersistsMemories behavior.
// TestNewStoreCreatesSchemaAndPersistsMemories 用于验证 TestNewStoreCreatesSchemaAndPersistsMemories 行为。
func TestNewStoreCreatesSchemaAndPersistsMemories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vmm.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	record := logicdomain.ArchivedMemory{
		ID:        "arc_001",
		SessionID: "sess_001",
		Content:   "[MOBILE_MASKED]",
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveMemory(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM vmm_memories WHERE session_id = ? AND content = ?`, "sess_001", "[MOBILE_MASKED]").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored row count = %d", count)
	}
}
