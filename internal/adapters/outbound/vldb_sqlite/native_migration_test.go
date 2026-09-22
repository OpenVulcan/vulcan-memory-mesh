// native_migration_test.go verifies strict vector replay without active-only filtering or model calls.
// native_migration_test.go 验证严格向量回放不会仅筛选活跃记忆，也不调用模型。
package vldb_sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNativeMigrationVectorDecodeRejectsCorruptValues ensures corrupted values cannot silently become zero vectors.
// TestNativeMigrationVectorDecodeRejectsCorruptValues 确保损坏值不会静默转成零向量。
func TestNativeMigrationVectorDecodeRejectsCorruptValues(t *testing.T) {
	for _, raw := range []string{`[null,1]`, `["1",2]`, `[1e100]`, `{}`, `[NaN]`, `broken`} {
		if _, err := decodeNativeMigrationVector(raw); err == nil {
			t.Fatalf("accepted corrupt vector %s", raw)
		}
	}
	for _, raw := range []string{"", "null", "[]"} {
		vector, err := decodeNativeMigrationVector(raw)
		if err != nil || len(vector) != 0 {
			t.Fatalf("absent vector %q: %v, %v", raw, vector, err)
		}
	}
}

// TestWalkNativeMigrationIncludesInactiveAndExactIDs preserves integer IDs above JSON's floating-point precision limit.
// TestWalkNativeMigrationIncludesInactiveAndExactIDs 验证非活跃行参与回放，且超过浮点精度范围的整数 ID 保持准确。
func TestWalkNativeMigrationIncludesInactiveAndExactIDs(t *testing.T) {
	store, err := NewNativeStore(filepath.Join(t.TempDir(), "native.db"), 5*time.Second, StoreOptions{TokenizerMode: "unicode61", SkipDebugSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Shutdown(context.Background())
	ctx := context.Background()
	if err := store.exec(ctx, `INSERT INTO vmm_memory_nodes
(id,team_id,space_id,project_id,user_id,origin_session_id,vector_id,vector_json,category,abstract,details,memory_status,source_kind,scope_level,created_timestamp,updated_timestamp)
VALUES (?,0,0,0,0,?,?,'[1,2]',1,'中文原文','完整详情',?,1,0,1,1)`, int64(9007199254740993), int64(9007199254740995), "retired-vector", logicdomain.MemoryStatusSuperseded); err != nil {
		t.Fatal(err)
	}
	var records []logicdomain.MemoryRecord
	if err := store.WalkNativeMigrationMemories(ctx, func(record logicdomain.MemoryRecord) error { records = append(records, record); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != logicdomain.MemoryStatusSuperseded || records[0].Filter.SessionID != 9007199254740995 || records[0].Vector[1] != 2 {
		t.Fatalf("migration record changed: %+v", records)
	}
	if err := store.exec(ctx, "UPDATE vmm_memory_nodes SET vector_json='[null,2]'"); err != nil {
		t.Fatal(err)
	}
	if err := store.WalkNativeMigrationMemories(ctx, func(logicdomain.MemoryRecord) error { t.Fatal("corrupt vector reached callback"); return nil }); err == nil {
		t.Fatal("corrupt vector was ignored")
	}
}

// TestWalkNativeMigrationRestorableTrashFiltersBatchState keeps only rows whose management and retention state permits recovery.
// TestWalkNativeMigrationRestorableTrashFiltersBatchState 验证只有管理状态和保留状态都允许恢复的回收行才会被遍历。
func TestWalkNativeMigrationRestorableTrashFiltersBatchState(t *testing.T) {
	store := newNativeMigrationTrashTestStore(t)
	defer store.Shutdown(context.Background())
	now := time.UnixMilli(1_000_000)
	ctx := context.Background()

	insertNativeMigrationTrashBatch(t, store, 10, 0, 1, "recycled", 0)
	insertNativeMigrationTrashMemory(t, store, 10, 1, 10_000, "eligible-zero-expiry", `[1,2]`)
	insertNativeMigrationTrashBatch(t, store, 11, 0, 1, "recycled", now.UnixMilli()+1)
	insertNativeMigrationTrashMemory(t, store, 11, 1, 10_001, "eligible-future-expiry", `[3,4]`)

	// A batch without management metadata represents a system recycle and is intentionally excluded by the inner join.
	// 没有管理元数据的批次代表系统回收，必须由内连接排除。
	insertNativeMigrationRecycleBatch(t, store, 12, 0)
	insertNativeMigrationTrashMemory(t, store, 12, 1, 10_002, "system-batch", `[5,6]`)
	insertNativeMigrationTrashBatch(t, store, 13, 0, 1, "recycled", now.UnixMilli()-1)
	insertNativeMigrationTrashMemory(t, store, 13, 1, 10_003, "expired-batch", `[7,8]`)
	insertNativeMigrationTrashBatch(t, store, 14, now.UnixMilli(), 1, "recycled", 0)
	insertNativeMigrationTrashMemory(t, store, 14, 1, 10_004, "purged-batch", `[9,10]`)
	insertNativeMigrationTrashBatch(t, store, 15, 0, 1, "restored", 0)
	insertNativeMigrationTrashMemory(t, store, 15, 1, 10_005, "restored-batch", `[11,12]`)
	insertNativeMigrationTrashBatch(t, store, 16, 0, 0, "recycled", 0)
	insertNativeMigrationTrashMemory(t, store, 16, 1, 10_006, "non-restorable-batch", `[13,14]`)

	var records []logicdomain.MemoryRecord
	if err := store.WalkNativeMigrationRestorableTrash(ctx, now, func(record logicdomain.MemoryRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("restorable trash rows = %d, want 2: %+v", len(records), records)
	}
	got := map[string]bool{}
	for _, record := range records {
		got[record.ID] = true
	}
	for _, vectorID := range []string{"eligible-zero-expiry", "eligible-future-expiry"} {
		if !got[vectorID] {
			t.Fatalf("missing eligible trash vector %q: %+v", vectorID, got)
		}
	}
}

// TestWalkNativeMigrationRestorableTrashUsesCompositeKeyset proves page boundaries do not skip equal memory ids from later batches.
// TestWalkNativeMigrationRestorableTrashUsesCompositeKeyset 验证复合键分页不会跳过后续批次中相同的记忆 ID。
func TestWalkNativeMigrationRestorableTrashUsesCompositeKeyset(t *testing.T) {
	store := newNativeMigrationTrashTestStore(t)
	defer store.Shutdown(context.Background())
	ctx := context.Background()
	now := time.UnixMilli(2_000_000)
	insertNativeMigrationTrashBatch(t, store, 100, 0, 1, "recycled", 0)
	insertNativeMigrationTrashBatch(t, store, 200, 0, 1, "recycled", 0)
	items := make([][]any, 0, 259)
	for id := int64(1); id <= 257; id++ {
		vectorID := fmt.Sprintf("batch-100-%d", id)
		items = append(items, []any{int64(100), id, int64(10_000 + id), vectorID, `[1,2]`, vectorID})
	}
	items = append(items,
		[]any{int64(200), int64(1), int64(20_001), "batch-200-1", `[3,4]`, "batch-200-1"},
		[]any{int64(200), int64(2), int64(20_002), "batch-200-2", `[5,6]`, "batch-200-2"},
	)
	if err := store.execBatch(ctx, `
INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason, id, team_id, space_id, project_id, user_id,
  origin_session_id, source_turn_id, vector_id, vector_json, category, abstract, details,
  created_timestamp, updated_timestamp
) VALUES (?, 0, 'test', ?, 1, 2, 3, 4, ?, NULL, ?, ?, 1, ?, 'details', 1, 1)`, items); err != nil {
		t.Fatal(err)
	}

	var vectorIDs []string
	if err := store.WalkNativeMigrationRestorableTrash(ctx, now, func(record logicdomain.MemoryRecord) error {
		vectorIDs = append(vectorIDs, record.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(vectorIDs) != 259 {
		t.Fatalf("restorable trash rows = %d, want 259", len(vectorIDs))
	}
	for i := 0; i < 257; i++ {
		want := fmt.Sprintf("batch-100-%d", i+1)
		if vectorIDs[i] != want {
			t.Fatalf("row %d vector = %q, want %q", i, vectorIDs[i], want)
		}
	}
	if vectorIDs[257] != "batch-200-1" || vectorIDs[258] != "batch-200-2" {
		t.Fatalf("later batch rows were skipped or reordered: %v", vectorIDs[255:])
	}
}

// TestWalkNativeMigrationRestorableTrashPreservesLargeIDsAndRejectsCorruption checks exact integer transport and strict vector errors.
// TestWalkNativeMigrationRestorableTrashPreservesLargeIDsAndRejectsCorruption 验证大整数精确传输，并拒绝损坏的原始向量 JSON。
func TestWalkNativeMigrationRestorableTrashPreservesLargeIDsAndRejectsCorruption(t *testing.T) {
	store := newNativeMigrationTrashTestStore(t)
	defer store.Shutdown(context.Background())
	ctx := context.Background()
	now := time.UnixMilli(3_000_000)
	insertNativeMigrationTrashBatch(t, store, 300, 0, 1, "recycled", 0)
	const largeOriginSessionID = uint64(9_007_199_254_740_995)
	const largeMemoryID = int64(9_007_199_254_740_993)
	insertNativeMigrationTrashMemoryWithOrigin(t, store, 300, largeMemoryID, int64(largeOriginSessionID), "large-id", `[1,2]`)
	var records []logicdomain.MemoryRecord
	if err := store.WalkNativeMigrationRestorableTrash(ctx, now, func(record logicdomain.MemoryRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Filter.SessionID != largeOriginSessionID || records[0].Vector[1] != 2 {
		t.Fatalf("large identifiers or vector changed: %+v", records)
	}
	if err := store.exec(ctx, "UPDATE vmm_memory_nodes_trash SET vector_json='[null,2]' WHERE batch_id = ? AND id = ?", int64(300), largeMemoryID); err != nil {
		t.Fatal(err)
	}
	if err := store.WalkNativeMigrationRestorableTrash(ctx, now, func(record logicdomain.MemoryRecord) error {
		t.Fatalf("corrupt trash vector reached callback: %+v", record)
		return nil
	}); err == nil {
		t.Fatal("corrupt trash vector was ignored")
	}
}

// TestNativeRestorableTrashVectorUpdateUsesCompositeIdentity verifies duplicate vector ids update only the selected batch row.
// TestNativeRestorableTrashVectorUpdateUsesCompositeIdentity 验证重复 vector id 只会更新指定批次的回收行。
func TestNativeRestorableTrashVectorUpdateUsesCompositeIdentity(t *testing.T) {
	store := newNativeMigrationTrashTestStore(t)
	defer store.Shutdown(context.Background())
	ctx := context.Background()
	now := time.UnixMilli(4_000_000)
	insertNativeMigrationTrashBatch(t, store, 400, 0, 1, "recycled", 0)
	insertNativeMigrationTrashBatch(t, store, 401, 0, 1, "recycled", 0)
	insertNativeMigrationTrashMemory(t, store, 400, 7, 40_007, "duplicate-vector", `[1,2]`)
	insertNativeMigrationTrashMemory(t, store, 401, 7, 40_107, "duplicate-vector", `[3,4]`)

	var identities []NativeRestorableTrashMemory
	if err := store.WalkNativeMigrationRestorableTrashRows(ctx, now, func(row NativeRestorableTrashMemory) error {
		identities = append(identities, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 || identities[0].BatchID != 400 || identities[0].MemoryID != 7 || identities[1].BatchID != 401 || identities[1].MemoryID != 7 {
		t.Fatalf("trash composite identities changed: %+v", identities)
	}
	before, err := loadNativeTrashVectorProbeRows(store, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNativeRestorableTrashVector(ctx, 400, 7, []float32{9, 8, 7}); err != nil {
		t.Fatal(err)
	}
	after, err := loadNativeTrashVectorProbeRows(store, ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated := after["400/7"]
	untouched := after["401/7"]
	if updated.VectorJSON != `[9,8,7]` || untouched.VectorJSON != `[3,4]` {
		t.Fatalf("composite update changed wrong payloads: %+v", after)
	}
	for key, original := range before {
		current := after[key]
		if original.BatchID != current.BatchID || original.ID != current.ID || original.VectorID != current.VectorID || original.Abstract != current.Abstract || original.Details != current.Details || original.UpdatedTimestamp != current.UpdatedTimestamp {
			t.Fatalf("non-vector fields changed for %s: before=%+v after=%+v", key, original, current)
		}
	}
	if err := store.UpdateNativeRestorableTrashVector(ctx, 400, 999, []float32{6, 6, 6}); err == nil {
		t.Fatal("missing composite identity was accepted")
	}
	unchanged, err := loadNativeTrashVectorProbeRows(store, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged["400/7"].VectorJSON != `[9,8,7]` || unchanged["401/7"].VectorJSON != `[3,4]` {
		t.Fatalf("failed identity update changed rows: %+v", unchanged)
	}
}

// nativeTrashVectorProbe is a narrow raw-row snapshot used to prove vector-only updates preserve every other column.
// nativeTrashVectorProbe 是用于证明向量更新不会改变其他列的窄原始行快照。
type nativeTrashVectorProbe struct {
	BatchID          uint64 `json:"batch_id"`
	ID               uint64 `json:"id"`
	VectorID         string `json:"vector_id"`
	VectorJSON       string `json:"vector_json"`
	Abstract         string `json:"abstract"`
	Details          string `json:"details"`
	UpdatedTimestamp int64  `json:"updated_timestamp"`
}

// loadNativeTrashVectorProbeRows reads the exact two-row fixture after each update attempt.
// loadNativeTrashVectorProbeRows 读取每次更新尝试后的两条精确测试行。
func loadNativeTrashVectorProbeRows(store *Store, ctx context.Context) (map[string]nativeTrashVectorProbe, error) {
	rows, err := queryRows[nativeTrashVectorProbe](store, ctx, `
SELECT batch_id, id, vector_id, vector_json, abstract, details, updated_timestamp
FROM vmm_memory_nodes_trash
WHERE batch_id IN (?, ?)
ORDER BY batch_id ASC, id ASC`, int64(400), int64(401))
	if err != nil {
		return nil, err
	}
	result := make(map[string]nativeTrashVectorProbe, len(rows))
	for _, row := range rows {
		result[fmt.Sprintf("%d/%d", row.BatchID, row.ID)] = row
	}
	return result, nil
}

// newNativeMigrationTrashTestStore creates the real native SQLite backend used by trash traversal tests.
// newNativeMigrationTrashTestStore 创建回收遍历测试使用的真实原生 SQLite 后端。
func newNativeMigrationTrashTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewNativeStore(filepath.Join(t.TempDir(), "native.db"), 5*time.Second, StoreOptions{TokenizerMode: "unicode61", SkipDebugSeed: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// insertNativeMigrationRecycleBatch inserts the retention batch row shared by the management join.
// insertNativeMigrationRecycleBatch 插入管理回收连接所需的保留批次行。
func insertNativeMigrationRecycleBatch(t *testing.T, store *Store, batchID int64, purgedAt int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := store.exec(context.Background(), `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
) VALUES (?, 'manual_management', 0, 0, 'test', ?, ?, ?, ?)`, batchID, now, purgedAt, now, now); err != nil {
		t.Fatal(err)
	}
}

// insertNativeMigrationTrashBatch inserts both metadata and retention rows for one management recycle batch.
// insertNativeMigrationTrashBatch 为一个管理回收批次插入管理元数据和保留批次行。
func insertNativeMigrationTrashBatch(t *testing.T, store *Store, batchID int64, purgedAt int64, restorable int, state string, expiresAt int64) {
	t.Helper()
	insertNativeMigrationRecycleBatch(t, store, batchID, purgedAt)
	if err := store.exec(context.Background(), `
INSERT INTO vmm_management_recycle_batches (
  batch_id, source, target_type, target_ids_json, restorable, state,
  expires_timestamp, restored_timestamp, operation_id
) VALUES (?, 'manual', 'memory', '[]', ?, ?, ?, 0, '')`, batchID, restorable, state, expiresAt); err != nil {
		t.Fatal(err)
	}
}

// insertNativeMigrationTrashMemory inserts the minimal durable memory payload required by the trash schema.
// insertNativeMigrationTrashMemory 插入回收表结构所需的最小长期记忆载荷。
func insertNativeMigrationTrashMemory(t *testing.T, store *Store, batchID, id, originSessionID int64, vectorID, vectorJSON string) {
	t.Helper()
	insertNativeMigrationTrashMemoryWithOrigin(t, store, batchID, id, originSessionID, vectorID, vectorJSON)
}

// insertNativeMigrationTrashMemoryWithOrigin writes a trash row while retaining an exact origin-session identifier.
// insertNativeMigrationTrashMemoryWithOrigin 写入回收行并保留精确的来源会话标识。
func insertNativeMigrationTrashMemoryWithOrigin(t *testing.T, store *Store, batchID, id, originSessionID int64, vectorID, vectorJSON string) {
	t.Helper()
	if err := store.exec(context.Background(), `
INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason, id, team_id, space_id, project_id, user_id,
  origin_session_id, source_turn_id, vector_id, vector_json, category, abstract, details,
  created_timestamp, updated_timestamp
) VALUES (?, 0, 'test', ?, 1, 2, 3, 4, ?, NULL, ?, ?, 1, ?, 'details', 1, 1)`, batchID, id, originSessionID, vectorID, vectorJSON, vectorID); err != nil {
		t.Fatal(err)
	}
}
