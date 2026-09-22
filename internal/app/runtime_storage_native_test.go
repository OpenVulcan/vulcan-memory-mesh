// Native schema tests ensure startup cannot silently downgrade or recreate an existing vector dataset.
// 原生 Schema 测试确保启动不会静默降级或重建已有向量数据集。
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/config"
)

// TestNativeInterruptedVectorRebuildRejectsOrdinaryOpen verifies quarantine before either native library or database is opened.
// TestNativeInterruptedVectorRebuildRejectsOrdinaryOpen 验证中断重建在加载库或打开数据库前被普通运行与维护入口隔离。
func TestNativeInterruptedVectorRebuildRejectsOrdinaryOpen(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultBase()
	cfg.Storage.Mode = "native"
	cfg.SQLite.Native.Path = filepath.Join(root, "sqlite.db")
	cfg.LanceDB.Native.Path = filepath.Join(root, "vectors")
	cfg.LanceDB.Native.LibraryPath = filepath.Join(root, "missing-library.dll")
	if err := os.WriteFile(cfg.SQLite.Native.Path+".vector-rebuild-incomplete", []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ensureTable := range []bool{true, false} {
		if _, err := buildNativeStorageDependencies(cfg, config.PromptLayout{}, ensureTable); err == nil || !strings.Contains(err.Error(), "vector rebuild is incomplete") {
			t.Fatalf("ordinary open accepted incomplete rebuild: %v", err)
		}
	}
	if _, err := os.Stat(cfg.SQLite.Native.Path); !os.IsNotExist(err) {
		t.Fatalf("quarantined open touched SQLite: %v", err)
	}
}

// TestNativeVectorSchemaRejectsMismatches checks that nonzero mismatches never overwrite tracked versions.
// TestNativeVectorSchemaRejectsMismatches 验证非零版本不匹配时不会覆盖已登记版本。
func TestNativeVectorSchemaRejectsMismatches(t *testing.T) {
	for _, version := range []int{-1, vldb_lancedb.CurrentSchemaVersion + 1} {
		store := &stubSchemaVersionStore{version: version}
		if err := ensureNativeVectorSchemaVersion(context.Background(), store); err == nil {
			t.Fatalf("accepted schema version %d", version)
		}
		if store.setCalls != 0 || store.version != version {
			t.Fatalf("changed incompatible schema: %+v", store)
		}
	}
}

// TestNativeVectorSchemaRecordsOnlyFirstVersion verifies idempotent registration and persistence errors.
// TestNativeVectorSchemaRecordsOnlyFirstVersion 验证初次登记的幂等性和持久化失败传播。
func TestNativeVectorSchemaRecordsOnlyFirstVersion(t *testing.T) {
	store := &stubSchemaVersionStore{}
	if err := ensureNativeVectorSchemaVersion(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if err := ensureNativeVectorSchemaVersion(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.setCalls != 1 || store.lastComponent != "lancedb" || store.version != vldb_lancedb.CurrentSchemaVersion {
		t.Fatalf("unexpected version registration: %+v", store)
	}
	failed := &stubSchemaVersionStore{setErr: errors.New("disk write failed")}
	if err := ensureNativeVectorSchemaVersion(context.Background(), failed); !errors.Is(err, failed.setErr) {
		t.Fatalf("lost persistence error: %v", err)
	}
}
