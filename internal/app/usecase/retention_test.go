// retention_test.go verifies the cold-data maintenance worker keeps recycle and purge decisions aligned with the normalized runtime config.
// retention_test.go 用于验证冷数据维护工作器会按照规范化后的运行时配置执行回收与 purge 决策。
package usecase

import (
	"context"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// fakeRetentionStore captures recycle and purge calls so retention maintenance tests can assert the exact governance inputs without touching a real database.
// fakeRetentionStore 用于捕获 recycle 与 purge 调用，让 retention 维护测试可以断言精确治理输入，而不依赖真实数据库。
type fakeRetentionStore struct {
	recycleQuery     logicdomain.MemoryRecycleQuery
	idleSessionQuery logicdomain.SessionIdleRecycleQuery
	purgeBefore      time.Time
	purgeLimit       int

	recycleResult     logicdomain.MemoryRecycleResult
	idleSessionResult logicdomain.SessionIdleRecycleResult
	purgeResult       logicdomain.RetentionTrashPurgeResult
}

// RecycleColdMemories records the latest recycle query and returns the configured fake result.
// RecycleColdMemories 用于记录最近一次 recycle 查询，并返回预设的 fake 结果。
func (f *fakeRetentionStore) RecycleColdMemories(_ context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	f.recycleQuery = query
	return f.recycleResult, nil
}

// RecycleIdleSessions records the latest idle-session recycle inputs and returns the configured fake result.
// RecycleIdleSessions 用于记录最近一次 idle-session 回收输入，并返回预设的 fake 结果。
func (f *fakeRetentionStore) RecycleIdleSessions(_ context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error) {
	f.idleSessionQuery = query
	return f.idleSessionResult, nil
}

// PurgeExpiredTrash records the latest purge inputs and returns the configured fake result.
// PurgeExpiredTrash 用于记录最近一次 purge 输入，并返回预设的 fake 结果。
func (f *fakeRetentionStore) PurgeExpiredTrash(_ context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error) {
	f.purgeBefore = before
	f.purgeLimit = limit
	return f.purgeResult, nil
}

// fakeVectorStore captures vector-id deletes so retention maintenance tests can verify relational recycle results are bridged into vector cleanup.
// fakeVectorStore 用于捕获向量 ID 删除调用，让 retention 维护测试验证关系回收结果已桥接到向量清理。
type fakeVectorStore struct {
	deletedIDs []string
}

// Upsert is unused in these maintenance tests and intentionally succeeds as a no-op.
// Upsert 在这些维护测试中不会被使用，因此有意作为 no-op 成功返回。
func (*fakeVectorStore) Upsert(context.Context, logicdomain.MemoryRecord) error { return nil }

// Search is unused in these maintenance tests and intentionally returns no hits.
// Search 在这些维护测试中不会被使用，因此有意返回空命中结果。
func (*fakeVectorStore) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, nil
}

// DeleteByFilter is unused in these maintenance tests and intentionally returns zero deletes.
// DeleteByFilter 在这些维护测试中不会被使用，因此有意返回零删除。
func (*fakeVectorStore) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	return 0, nil
}

// DeleteByIDs records the ids requested by the retention worker.
// DeleteByIDs 用于记录 retention 工作器请求删除的向量 id。
func (f *fakeVectorStore) DeleteByIDs(_ context.Context, ids []string) (uint64, error) {
	f.deletedIDs = append(f.deletedIDs, ids...)
	return uint64(len(ids)), nil
}

// Shutdown is unused in these maintenance tests and intentionally succeeds.
// Shutdown 在这些维护测试中不会被使用，因此有意直接成功返回。
func (*fakeVectorStore) Shutdown(context.Context) error { return nil }

var _ appports.RetentionStore = (*fakeRetentionStore)(nil)
var _ appports.VectorStore = (*fakeVectorStore)(nil)

// TestRetentionUseCaseRunMaintenanceRecyclesAndPurges verifies one maintenance pass forwards the normalized protection floors, runs idle-session recycle with the configured hot window, bridges vector cleanup, and computes the trash purge threshold from config.
// TestRetentionUseCaseRunMaintenanceRecyclesAndPurges 用于验证一次维护会透传规范化后的保护阈值、按配置热窗口执行 idle-session 回收、衔接向量清理，并根据配置计算回收站 purge 阈值。
func TestRetentionUseCaseRunMaintenanceRecyclesAndPurges(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:              7,
			RecycledMemoryCount:  2,
			RecycledContextCount: 3,
			RecycledVectorIDs:    []string{"vec-1", "vec-2"},
		},
		idleSessionResult: logicdomain.SessionIdleRecycleResult{
			BatchIDs:             []uint64{9},
			SessionIDs:           []uint64{11},
			RecycledMemoryCount:  1,
			RecycledContextCount: 1,
			RecycledTurnCount:    4,
			RecycledVectorIDs:    []string{"vec-3"},
		},
		purgeResult: logicdomain.RetentionTrashPurgeResult{
			BatchIDs:           []uint64{7, 9},
			PurgedMemoryCount:  3,
			PurgedContextCount: 4,
			PurgedTurnCount:    4,
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter:     15 * 24 * time.Hour,
			TurnHotWindowSize:           8,
			TrashRetention:              30 * 24 * time.Hour,
			ProtectPriorityFloor:        "P1",
			ProtectMemoryLevelFloor:     "stable",
			SkipProtectedSharedMemories: true,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if store.recycleQuery.Limit != defaultRetentionRecycleBatchSize {
		t.Fatalf("recycle limit = %d, want %d", store.recycleQuery.Limit, defaultRetentionRecycleBatchSize)
	}
	if store.recycleQuery.RecycleReason != logicdomain.RecycleReasonColdTerminalMemory {
		t.Fatalf("recycle reason = %q", store.recycleQuery.RecycleReason)
	}
	if store.recycleQuery.ProtectPriorityFloor != logicdomain.MemoryPriorityP1 {
		t.Fatalf("priority floor = %d, want %d", store.recycleQuery.ProtectPriorityFloor, logicdomain.MemoryPriorityP1)
	}
	if store.recycleQuery.ProtectMemoryLevelFloor != logicdomain.MemoryLevelStable {
		t.Fatalf("memory level floor = %d, want %d", store.recycleQuery.ProtectMemoryLevelFloor, logicdomain.MemoryLevelStable)
	}
	if !store.recycleQuery.SkipProtectedSharedMemories {
		t.Fatal("expected shared-memory protection to stay enabled")
	}
	if got, want := store.idleSessionQuery.Limit, defaultRetentionIdleSessionBatchSize; got != want {
		t.Fatalf("idle-session limit = %d, want %d", got, want)
	}
	if got, want := store.idleSessionQuery.RecycleReason, logicdomain.RecycleReasonIdleSessionCompact; got != want {
		t.Fatalf("idle-session recycle reason = %q, want %q", got, want)
	}
	if got, want := store.idleSessionQuery.TurnHotWindowSize, 8; got != want {
		t.Fatalf("idle-session hot window = %d, want %d", got, want)
	}
	idleMinBefore := beforeRun.Add(-15 * 24 * time.Hour)
	idleMaxBefore := afterRun.Add(-15 * 24 * time.Hour)
	if store.idleSessionQuery.IdleBefore.Before(idleMinBefore.Add(-time.Second)) || store.idleSessionQuery.IdleBefore.After(idleMaxBefore.Add(time.Second)) {
		t.Fatalf("idle-session cutoff = %v, want between %v and %v", store.idleSessionQuery.IdleBefore, idleMinBefore, idleMaxBefore)
	}
	if len(vector.deletedIDs) != 3 || vector.deletedIDs[0] != "vec-1" || vector.deletedIDs[1] != "vec-2" || vector.deletedIDs[2] != "vec-3" {
		t.Fatalf("deleted vector ids = %v", vector.deletedIDs)
	}
	if store.purgeLimit != defaultRetentionPurgeBatchSize {
		t.Fatalf("purge limit = %d, want %d", store.purgeLimit, defaultRetentionPurgeBatchSize)
	}
	minBefore := beforeRun.Add(-30 * 24 * time.Hour)
	maxBefore := afterRun.Add(-30 * 24 * time.Hour)
	if store.purgeBefore.Before(minBefore.Add(-time.Second)) || store.purgeBefore.After(maxBefore.Add(time.Second)) {
		t.Fatalf("purge before = %v, want between %v and %v", store.purgeBefore, minBefore, maxBefore)
	}
}

// TestNewRetentionUseCaseSkipsDisabledWorker verifies disabled retention config does not start a background goroutine.
// TestNewRetentionUseCaseSkipsDisabledWorker 用于验证在 retention 被禁用时不会启动后台 goroutine。
func TestNewRetentionUseCaseSkipsDisabledWorker(t *testing.T) {
	useCase := NewRetentionUseCase(&fakeRetentionStore{}, &fakeVectorStore{}, RetentionConfig{
		Enabled:                 false,
		RecycleScanInterval:     time.Minute,
		SessionIdleRecycleAfter: 15 * 24 * time.Hour,
		TurnHotWindowSize:       8,
		TrashRetention:          24 * time.Hour,
	}, nil)
	if useCase.workerCancel != nil {
		t.Fatal("expected disabled retention worker to stay stopped")
	}
}
