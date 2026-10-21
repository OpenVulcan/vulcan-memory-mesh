// SQLite adapter tests for memory fts test.
// SQLite 适配层全文索引同步测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	"strings"
	"testing"
	"time"
)

// TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult verifies FFI result bits are consumed before tokenizer metadata is trusted.
// TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult 用于验证在信任分词器元数据前，会消费 FFI 返回的结果位。
func TestStoreEnsureBuiltinMemoryFTSRejectsUnsuccessfulEnsureResult(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	fake.ensureFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error) {
		return sqliteffi.EnsureFtsIndexResult{Success: false, TokenizerMode: mode}, nil
	}

	err := store.ensureBuiltinMemoryFTS(context.Background())
	if err == nil {
		t.Fatalf("expected unsuccessful FTS ensure result to fail")
	}
	if !strings.Contains(err.Error(), "ensure sqlite builtin fts index") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected ensure error: %v", err)
	}
}

// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure verifies successful FFI status with a failed mutation result still triggers rebuild fallback.
// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure 用于验证 FFI 状态成功但变更结果位失败时，仍会触发重建兜底。
func TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulUpsertAsFailure(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	rebuildCalls := 0
	fake.upsertFtsDocumentFunc = func(_ string, _ sqliteffi.TokenizerMode, _ string, _ string, _ string, _ string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{Success: false, AffectedRows: 0}, nil
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	err := store.syncMemoryFTSAfterWrite(context.Background(), []logicdomain.MemoryNodeRecord{{
		ID:       55,
		VectorID: "vec-55",
		Abstract: "memory abstract",
		Details:  "memory details",
	}}, nil)
	if err == nil {
		t.Fatalf("expected unsuccessful FTS upsert result to fail")
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after unsuccessful upsert, got %d", rebuildCalls)
	}
	if !strings.Contains(err.Error(), "upsert sqlite memory fts document 55") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected upsert error: %v", err)
	}
}

// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure verifies delete result bits are handled the same way as upsert result bits.
// TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure 用于验证删除结果位会按照与写入结果位相同的方式处理。
func TestStoreSyncMemoryFTSAfterWriteTreatsUnsuccessfulDeleteAsFailure(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	rebuildCalls := 0
	fake.deleteFtsDocumentFunc = func(_ string, _ string) (sqliteffi.FtsMutationResult, error) {
		return sqliteffi.FtsMutationResult{Success: false, AffectedRows: 0}, nil
	}
	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		rebuildCalls++
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}

	err := store.syncMemoryFTSAfterWrite(context.Background(), nil, []uint64{77})
	if err == nil {
		t.Fatalf("expected unsuccessful FTS delete result to fail")
	}
	if rebuildCalls != 1 {
		t.Fatalf("expected one FTS rebuild attempt after unsuccessful delete, got %d", rebuildCalls)
	}
	if !strings.Contains(err.Error(), "delete sqlite memory fts document 77") || !strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected delete error: %v", err)
	}
}

// TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult verifies fallback rebuild result bits are part of the recovery contract.
// TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult 用于验证兜底重建的结果位属于恢复契约的一部分。
func TestStoreRebuildMemoryFTSWithFallbackRejectsUnsuccessfulRebuildResult(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second, tokenizerMode: sqliteffi.TokenizerJieba, ftsIndexName: memoryFTSIndexName}

	fake.rebuildFtsIndexFunc = func(_ string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
		return sqliteffi.RebuildFtsIndexResult{Success: false, TokenizerMode: mode}, nil
	}

	err := store.rebuildMemoryFTSWithFallback(context.Background(), errors.New("incremental fts failed"))
	if err == nil {
		t.Fatalf("expected unsuccessful FTS rebuild result to fail")
	}
	if !strings.Contains(err.Error(), "incremental fts failed") ||
		!strings.Contains(err.Error(), "rebuild sqlite builtin fts index also failed") ||
		!strings.Contains(err.Error(), "unsuccessful") {
		t.Fatalf("unexpected rebuild fallback error: %v", err)
	}
}
