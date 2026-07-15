// retention_test.go verifies the cold-data maintenance worker keeps recycle and purge decisions aligned with the normalized runtime config.
// retention_test.go 用于验证冷数据维护工作器会按照规范化后的运行时配置执行回收与 purge 决策。
package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// fakeRetentionStore captures recycle and purge calls so retention maintenance tests can assert the exact governance inputs without touching a real database.
// fakeRetentionStore 用于捕获 recycle 与 purge 调用，让 retention 维护测试可以断言精确治理输入，而不依赖真实数据库。
type fakeRetentionStore struct {
	recycleQuery           logicdomain.MemoryRecycleQuery
	turnJobQuery           logicdomain.ColdTurnRecycleJobEnqueueQuery
	claimJobType           string
	claimJobDueBefore      time.Time
	claimJobUntil          time.Time
	claimJobLimit          int
	coldTurnQuery          logicdomain.ColdTurnRecycleQuery
	completedRecycleJobIDs []uint64
	completedJobIDCalls    [][]uint64
	completedRecycleAt     time.Time
	retriedRecycleJobIDs   []uint64
	recycleRetryAt         time.Time
	recycleRetryLastError  string
	idleSessionQuery       logicdomain.SessionIdleRecycleQuery
	purgeBefore            time.Time
	purgeLimit             int
	enqueueQuery           logicdomain.VectorGCJobEnqueueQuery
	claimDueBefore         time.Time
	claimUntil             time.Time
	claimLimit             int
	completedJobIDs        []uint64
	completedAt            time.Time
	retriedJobIDs          []uint64
	retryAt                time.Time
	retryLastError         string

	recycleResult      logicdomain.MemoryRecycleResult
	turnJobCount       int
	claimedRecycleJobs []logicdomain.RecycleJobRecord
	coldTurnResult     logicdomain.ColdTurnRecycleResult
	idleSessionResult  logicdomain.SessionIdleRecycleResult
	purgeResult        logicdomain.RetentionTrashPurgeResult
	claimedJobs        []logicdomain.VectorGCJobRecord

	recycleErr     error
	turnJobErr     error
	claimJobErr    error
	coldTurnErr    error
	idleSessionErr error
	purgeErr       error
}

// RecycleColdMemories records the latest recycle query and returns the configured fake result.
// RecycleColdMemories 用于记录最近一次 recycle 查询，并返回预设的 fake 结果。
func (f *fakeRetentionStore) RecycleColdMemories(_ context.Context, query logicdomain.MemoryRecycleQuery) (logicdomain.MemoryRecycleResult, error) {
	f.recycleQuery = query
	return f.recycleResult, f.recycleErr
}

// EnqueueColdTurnRecycleJobs records the latest cold-turn scan inputs and returns the configured fake enqueue count.
// EnqueueColdTurnRecycleJobs 用于记录最近一次冷 turn 扫描输入，并返回预设的 fake 入队数量。
func (f *fakeRetentionStore) EnqueueColdTurnRecycleJobs(_ context.Context, query logicdomain.ColdTurnRecycleJobEnqueueQuery) (int, error) {
	f.turnJobQuery = query
	return f.turnJobCount, f.turnJobErr
}

// ClaimPendingRecycleJobs records the latest recycle-job claim inputs and returns the configured fake recycle jobs.
// ClaimPendingRecycleJobs 用于记录最近一次回收任务领取输入，并返回预设的 fake 回收任务。
func (f *fakeRetentionStore) ClaimPendingRecycleJobs(_ context.Context, jobType string, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.RecycleJobRecord, error) {
	f.claimJobType = jobType
	f.claimJobDueBefore = dueBefore
	f.claimJobUntil = claimUntil
	f.claimJobLimit = limit
	return append([]logicdomain.RecycleJobRecord(nil), f.claimedRecycleJobs...), f.claimJobErr
}

// RecycleColdTurns records the latest cold-turn execution query and returns the configured fake recycle result.
// RecycleColdTurns 用于记录最近一次冷 turn 执行查询，并返回预设的 fake 回收结果。
func (f *fakeRetentionStore) RecycleColdTurns(_ context.Context, query logicdomain.ColdTurnRecycleQuery) (logicdomain.ColdTurnRecycleResult, error) {
	f.coldTurnQuery = query
	return f.coldTurnResult, f.coldTurnErr
}

// CompleteRecycleJobs records the recycle-job ids completed after one cold-turn execution succeeds.
// CompleteRecycleJobs 用于记录冷 turn 执行成功后被关闭的回收任务 id。
func (f *fakeRetentionStore) CompleteRecycleJobs(_ context.Context, jobIDs []uint64, completedAt time.Time) error {
	f.completedRecycleJobIDs = append([]uint64(nil), jobIDs...)
	f.completedRecycleAt = completedAt
	return nil
}

// RetryRecycleJobs records the recycle-job ids rescheduled after one cold-turn execution fails.
// RetryRecycleJobs 用于记录冷 turn 执行失败后被重新调度的回收任务 id。
func (f *fakeRetentionStore) RetryRecycleJobs(_ context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	f.retriedRecycleJobIDs = append([]uint64(nil), jobIDs...)
	f.recycleRetryAt = nextRunAt
	f.recycleRetryLastError = lastError
	return nil
}

// RecycleIdleSessions records the latest idle-session recycle inputs and returns the configured fake result.
// RecycleIdleSessions 用于记录最近一次 idle-session 回收输入，并返回预设的 fake 结果。
func (f *fakeRetentionStore) RecycleIdleSessions(_ context.Context, query logicdomain.SessionIdleRecycleQuery) (logicdomain.SessionIdleRecycleResult, error) {
	f.idleSessionQuery = query
	return f.idleSessionResult, f.idleSessionErr
}

// PurgeExpiredTrash records the latest purge inputs and returns the configured fake result.
// PurgeExpiredTrash 用于记录最近一次 purge 输入，并返回预设的 fake 结果。
func (f *fakeRetentionStore) PurgeExpiredTrash(_ context.Context, before time.Time, limit int) (logicdomain.RetentionTrashPurgeResult, error) {
	f.purgeBefore = before
	f.purgeLimit = limit
	return f.purgeResult, f.purgeErr
}

// EnqueueVectorGCJobs records the latest vector-gc enqueue request so retention maintenance tests can verify failed sidecar deletes are persisted for retry.
// EnqueueVectorGCJobs 用于记录最近一次向量 GC 入队请求，让 retention 维护测试验证失败的旁路删除会被持久化重试。
func (f *fakeRetentionStore) EnqueueVectorGCJobs(_ context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	f.enqueueQuery = query
	return nil
}

// ClaimPendingVectorGCJobs records the latest retry-claim inputs and returns the configured fake jobs.
// ClaimPendingVectorGCJobs 用于记录最近一次重试领取输入，并返回预设的 fake 任务。
func (f *fakeRetentionStore) ClaimPendingVectorGCJobs(_ context.Context, dueBefore, claimUntil time.Time, limit int) ([]logicdomain.VectorGCJobRecord, error) {
	f.claimDueBefore = dueBefore
	f.claimUntil = claimUntil
	f.claimLimit = limit
	return append([]logicdomain.VectorGCJobRecord(nil), f.claimedJobs...), nil
}

// CompleteVectorGCJobs records the completed retry job ids so tests can assert successful retry deletion closes the queue items.
// CompleteVectorGCJobs 用于记录已完成的重试任务 id，让测试断言成功重试删除后会关闭队列任务。
func (f *fakeRetentionStore) CompleteVectorGCJobs(_ context.Context, jobIDs []uint64, completedAt time.Time) error {
	f.completedJobIDs = append([]uint64(nil), jobIDs...)
	f.completedJobIDCalls = append(f.completedJobIDCalls, append([]uint64(nil), jobIDs...))
	f.completedAt = completedAt
	return nil
}

// RetryVectorGCJobs records the rescheduled retry job ids so tests can assert failed retry deletion is re-enqueued with a later next-run timestamp.
// RetryVectorGCJobs 用于记录被重新调度的重试任务 id，让测试断言失败的重试删除会带着更晚的 next-run 时间再次排队。
func (f *fakeRetentionStore) RetryVectorGCJobs(_ context.Context, jobIDs []uint64, nextRunAt time.Time, lastError string) error {
	f.retriedJobIDs = append([]uint64(nil), jobIDs...)
	f.retryAt = nextRunAt
	f.retryLastError = lastError
	return nil
}

// fakeVectorStore captures vector-id deletes so retention maintenance tests can verify relational recycle results are bridged into vector cleanup.
// fakeVectorStore 用于捕获向量 ID 删除调用，让 retention 维护测试验证关系回收结果已桥接到向量清理。
type fakeVectorStore struct {
	deletedIDs []string
	deleteErrs []error
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
	if len(f.deleteErrs) > 0 {
		err := f.deleteErrs[0]
		f.deleteErrs = f.deleteErrs[1:]
		if err != nil {
			return 0, err
		}
	}
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
			RecycledVectorIDs:    []string{" vec-1 ", "", "vec-1", "vec-2"},
		},
		idleSessionResult: logicdomain.SessionIdleRecycleResult{
			BatchIDs:             []uint64{9},
			SessionIDs:           []uint64{11},
			RecycledMemoryCount:  1,
			RecycledContextCount: 1,
			RecycledTurnCount:    4,
			RecycledVectorIDs:    []string{"vec-3", "vec-3"},
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
	if got, want := store.turnJobQuery.Limit, defaultRetentionTurnJobScanBatchSize; got != want {
		t.Fatalf("cold-turn job scan limit = %d, want %d", got, want)
	}
	if got, want := store.turnJobQuery.TurnHotWindowSize, 8; got != want {
		t.Fatalf("cold-turn job hot window = %d, want %d", got, want)
	}
	if got, want := store.claimJobType, logicdomain.RecycleJobTypeColdTurn; got != want {
		t.Fatalf("recycle job claim type = %q, want %q", got, want)
	}
	if got, want := store.claimJobLimit, defaultRetentionRecycleJobBatchSize; got != want {
		t.Fatalf("recycle job claim limit = %d, want %d", got, want)
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

// TestNewRetentionUseCaseStartsWorkerForVectorGCRetriesEvenWhenRetentionDisabled verifies the shared maintenance loop still starts when vector-gc retry work exists, even if classical retention recycle is disabled.
// TestNewRetentionUseCaseStartsWorkerForVectorGCRetriesEvenWhenRetentionDisabled 用于验证即使经典 retention recycle 被禁用，只要仍存在 vector-gc 重试工作，共享维护循环也会启动。
func TestNewRetentionUseCaseStartsWorkerForVectorGCRetriesEvenWhenRetentionDisabled(t *testing.T) {
	useCase := NewRetentionUseCase(&fakeRetentionStore{}, &fakeVectorStore{}, RetentionConfig{
		Enabled:                 false,
		RecycleScanInterval:     time.Minute,
		SessionIdleRecycleAfter: 15 * 24 * time.Hour,
		TurnHotWindowSize:       8,
		TrashRetention:          24 * time.Hour,
	}, nil)
	defer func() {
		_ = useCase.Shutdown(context.Background())
	}()
	if useCase.workerCancel == nil {
		t.Fatal("expected shared maintenance worker to start for vector-gc retries")
	}
}

// TestNewRetentionUseCaseSkipsWorkerWithoutMaintenanceTargets verifies the shared worker stays stopped when neither recycle nor vector-gc retry has any usable backing store.
// TestNewRetentionUseCaseSkipsWorkerWithoutMaintenanceTargets 用于验证当 recycle 与 vector-gc 重试都没有可用存储支撑时，共享工作器仍会保持停止。
func TestNewRetentionUseCaseSkipsWorkerWithoutMaintenanceTargets(t *testing.T) {
	useCase := NewRetentionUseCase(nil, nil, RetentionConfig{
		Enabled:             false,
		RecycleScanInterval: time.Minute,
	}, nil)
	if useCase.workerCancel != nil {
		t.Fatal("expected maintenance worker to stay stopped without any maintenance targets")
	}
}

// TestRetentionUseCaseRunMaintenanceSkipsColdRecycleVectorCleanupOnError verifies failed cold-memory recycle never triggers vector deletion even when the store returns partial diagnostic vector ids alongside the error.
// TestRetentionUseCaseRunMaintenanceSkipsColdRecycleVectorCleanupOnError 用于验证终态记忆回收失败时不会触发向量删除；即使存储层错误返回里带有部分诊断向量 id，也不能误删。
func TestRetentionUseCaseRunMaintenanceSkipsColdRecycleVectorCleanupOnError(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:           7,
			RecycledVectorIDs: []string{"vec-cold-1", "vec-cold-2"},
		},
		recycleErr: errors.New("cold recycle failed"),
		idleSessionResult: logicdomain.SessionIdleRecycleResult{
			BatchIDs:          []uint64{9},
			SessionIDs:        []uint64{11},
			RecycledVectorIDs: []string{"vec-idle-1"},
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	useCase.runMaintenance(context.Background())

	if len(vector.deletedIDs) != 1 || vector.deletedIDs[0] != "vec-idle-1" {
		t.Fatalf("deleted vector ids = %v, want only idle-session cleanup", vector.deletedIDs)
	}
}

// TestRetentionUseCaseRunMaintenanceCleansColdRecycleVectorsOnOutcomeUncertain verifies late uncertain recycle failures still preserve obsolete vector cleanup.
// TestRetentionUseCaseRunMaintenanceCleansColdRecycleVectorsOnOutcomeUncertain 用于验证后置结果不确定的回收失败仍会保留过时向量清理。
func TestRetentionUseCaseRunMaintenanceCleansColdRecycleVectorsOnOutcomeUncertain(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:           7,
			RecycledVectorIDs: []string{"vec-cold-1", "vec-cold-2"},
		},
		recycleErr: logicdomain.OutcomeUncertainError{
			Operation: "recycle cold memories",
			Message:   "sync sqlite memory fts after cold recycle: fts unavailable",
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	useCase.runMaintenance(context.Background())

	if len(vector.deletedIDs) != 2 || vector.deletedIDs[0] != "vec-cold-1" || vector.deletedIDs[1] != "vec-cold-2" {
		t.Fatalf("deleted vector ids = %v, want cold-memory cleanup despite outcome uncertainty", vector.deletedIDs)
	}
}

// TestRetentionUseCaseRunMaintenanceSkipsIdleSessionVectorCleanupOnError verifies failed idle-session recycle never deletes vectors from a partially populated error result after the cold-memory pass already succeeded.
// TestRetentionUseCaseRunMaintenanceSkipsIdleSessionVectorCleanupOnError 用于验证 idle-session 回收失败时不会删除错误结果中携带的部分向量，同时保留已成功完成的终态记忆向量清理。
func TestRetentionUseCaseRunMaintenanceSkipsIdleSessionVectorCleanupOnError(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:           7,
			RecycledVectorIDs: []string{"vec-cold-1"},
		},
		idleSessionResult: logicdomain.SessionIdleRecycleResult{
			BatchIDs:          []uint64{9},
			SessionIDs:        []uint64{11},
			RecycledVectorIDs: []string{"vec-idle-1", "vec-idle-2"},
		},
		idleSessionErr: errors.New("idle session recycle failed"),
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	useCase.runMaintenance(context.Background())

	if len(vector.deletedIDs) != 1 || vector.deletedIDs[0] != "vec-cold-1" {
		t.Fatalf("deleted vector ids = %v, want only cold-memory cleanup", vector.deletedIDs)
	}
}

// TestRetentionUseCaseRunMaintenanceCleansIdleSessionVectorsOnOutcomeUncertain verifies idle-session vectors are still cleaned when SQLite reports an uncertain late maintenance failure after archive rows are durable.
// TestRetentionUseCaseRunMaintenanceCleansIdleSessionVectorsOnOutcomeUncertain 用于验证 SQLite 在归档行已经长期化后报告后置维护结果不确定时，idle-session 向量仍会被清理。
func TestRetentionUseCaseRunMaintenanceCleansIdleSessionVectorsOnOutcomeUncertain(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:           7,
			RecycledVectorIDs: []string{"vec-cold-1"},
		},
		idleSessionResult: logicdomain.SessionIdleRecycleResult{
			BatchIDs:          []uint64{9},
			SessionIDs:        []uint64{11},
			RecycledVectorIDs: []string{"vec-idle-1", "vec-idle-2"},
		},
		idleSessionErr: logicdomain.OutcomeUncertainError{
			Operation: "recycle idle-session memories",
			Message:   "sync sqlite memory fts after idle recycle 11: fts unavailable",
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	useCase.runMaintenance(context.Background())

	if len(vector.deletedIDs) != 3 || vector.deletedIDs[0] != "vec-cold-1" || vector.deletedIDs[1] != "vec-idle-1" || vector.deletedIDs[2] != "vec-idle-2" {
		t.Fatalf("deleted vector ids = %v, want cold and idle cleanup despite idle outcome uncertainty", vector.deletedIDs)
	}
}

// TestRetentionUseCaseRunMaintenanceEnqueuesVectorGCJobsOnImmediateDeleteFailure verifies retention persists failed sidecar vector deletes into the retry queue instead of only logging and forgetting them.
// TestRetentionUseCaseRunMaintenanceEnqueuesVectorGCJobsOnImmediateDeleteFailure 用于验证 retention 会把失败的旁路向量删除持久化到重试队列，而不是只记日志后遗忘。
func TestRetentionUseCaseRunMaintenanceEnqueuesVectorGCJobsOnImmediateDeleteFailure(t *testing.T) {
	store := &fakeRetentionStore{
		recycleResult: logicdomain.MemoryRecycleResult{
			BatchID:           7,
			RecycledVectorIDs: []string{"vec-cold-1", "vec-cold-2"},
		},
	}
	vector := &fakeVectorStore{deleteErrs: []error{errors.New("vector store unavailable")}}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if store.enqueueQuery.BatchID != 7 {
		t.Fatalf("enqueue batch id = %d, want 7", store.enqueueQuery.BatchID)
	}
	if store.enqueueQuery.JobType != logicdomain.VectorGCJobTypeRetentionRecycle {
		t.Fatalf("enqueue job type = %q, want %q", store.enqueueQuery.JobType, logicdomain.VectorGCJobTypeRetentionRecycle)
	}
	if len(store.enqueueQuery.VectorIDs) != 2 || store.enqueueQuery.VectorIDs[0] != "vec-cold-1" || store.enqueueQuery.VectorIDs[1] != "vec-cold-2" {
		t.Fatalf("enqueue vector ids = %v", store.enqueueQuery.VectorIDs)
	}
	minNextRun := beforeRun.Add(defaultRetentionVectorGCRetryDelay)
	maxNextRun := afterRun.Add(defaultRetentionVectorGCRetryDelay)
	if store.enqueueQuery.NextRunAt.Before(minNextRun.Add(-time.Second)) || store.enqueueQuery.NextRunAt.After(maxNextRun.Add(time.Second)) {
		t.Fatalf("enqueue next run at = %v, want between %v and %v", store.enqueueQuery.NextRunAt, minNextRun, maxNextRun)
	}
	if len(store.completedJobIDs) != 0 || len(store.retriedJobIDs) != 0 {
		t.Fatalf("unexpected retry queue terminal updates: completed=%v retried=%v", store.completedJobIDs, store.retriedJobIDs)
	}
}

// TestRetentionUseCaseRunVectorGCMaintenanceCompletesClaimedVectorGCJobs verifies the dedicated vector-gc maintenance path claims one bounded retry batch and closes it after the sidecar delete finally succeeds, even when classic retention recycle is disabled.
// TestRetentionUseCaseRunVectorGCMaintenanceCompletesClaimedVectorGCJobs 用于验证独立的 vector-gc 维护路径会领取一批有界重试任务，并在旁路删除成功后把它们关闭；即使经典 retention recycle 已禁用也成立。
func TestRetentionUseCaseRunVectorGCMaintenanceCompletesClaimedVectorGCJobs(t *testing.T) {
	store := &fakeRetentionStore{
		claimedJobs: []logicdomain.VectorGCJobRecord{
			{ID: 51, BatchID: 7, VectorID: "vec-retry-1", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 1},
			{ID: 52, BatchID: 7, VectorID: "vec-retry-2", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 2},
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			Enabled:                 false,
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runVectorGCMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if store.claimLimit != defaultRetentionVectorGCBatchSize {
		t.Fatalf("claim limit = %d, want %d", store.claimLimit, defaultRetentionVectorGCBatchSize)
	}
	if len(vector.deletedIDs) != 2 || vector.deletedIDs[0] != "vec-retry-1" || vector.deletedIDs[1] != "vec-retry-2" {
		t.Fatalf("deleted vector ids = %v", vector.deletedIDs)
	}
	if len(store.completedJobIDs) != 2 || store.completedJobIDs[0] != 51 || store.completedJobIDs[1] != 52 {
		t.Fatalf("completed job ids = %v", store.completedJobIDs)
	}
	if !store.completedAt.IsZero() && (store.completedAt.Before(beforeRun.Add(-time.Second)) || store.completedAt.After(afterRun.Add(time.Second))) {
		t.Fatalf("completed at = %v, want between %v and %v", store.completedAt, beforeRun, afterRun)
	}
	if store.claimUntil.Before(beforeRun.Add(defaultRetentionVectorGCClaimLease-time.Second)) || store.claimUntil.After(afterRun.Add(defaultRetentionVectorGCClaimLease+time.Second)) {
		t.Fatalf("claim until = %v, want around maintenance time + lease", store.claimUntil)
	}
}

// TestRetentionUseCaseRunVectorGCMaintenanceNormalizesClaimedVectorIDs verifies retry deletion receives clean unique vector ids while every valid claimed job is still completed.
// TestRetentionUseCaseRunVectorGCMaintenanceNormalizesClaimedVectorIDs 用于验证重试删除只接收干净且唯一的向量 id，同时所有有效的已领取 job 仍会被完成。
func TestRetentionUseCaseRunVectorGCMaintenanceNormalizesClaimedVectorIDs(t *testing.T) {
	store := &fakeRetentionStore{
		claimedJobs: []logicdomain.VectorGCJobRecord{
			{ID: 53, BatchID: 7, VectorID: " vec-retry-1 ", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 1},
			{ID: 54, BatchID: 7, VectorID: "vec-retry-1", JobType: logicdomain.VectorGCJobTypeDirectWriteSupersedeCleanup, AttemptCount: 1},
			{ID: 55, BatchID: 8, VectorID: "vec-retry-2", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 1},
			{ID: 56, BatchID: 8, VectorID: "   ", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 1},
			{ID: 0, BatchID: 8, VectorID: "vec-missing-job-id", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 1},
		},
	}
	vector := &fakeVectorStore{}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			Enabled: false,
		},
	}

	useCase.runVectorGCMaintenance(context.Background())

	if len(vector.deletedIDs) != 2 || vector.deletedIDs[0] != "vec-retry-1" || vector.deletedIDs[1] != "vec-retry-2" {
		t.Fatalf("deleted vector ids = %v, want normalized unique ids", vector.deletedIDs)
	}
	if len(store.completedJobIDCalls) != 2 {
		t.Fatalf("completed job id calls = %v, want invalid and valid completion calls", store.completedJobIDCalls)
	}
	if got := store.completedJobIDCalls[0]; len(got) != 1 || got[0] != 56 {
		t.Fatalf("invalid completed job ids = %v, want [56]", got)
	}
	if got := store.completedJobIDCalls[1]; len(got) != 3 || got[0] != 53 || got[1] != 54 || got[2] != 55 {
		t.Fatalf("valid completed job ids = %v, want [53 54 55]", got)
	}
}

// TestRetentionUseCaseRunMaintenanceExecutesColdTurnRecycleJobs verifies maintenance now scans, claims, executes, and completes one bounded cold-turn recycle batch before idle-session compaction runs.
// TestRetentionUseCaseRunMaintenanceExecutesColdTurnRecycleJobs 用于验证维护流程现在会在 idle-session 压缩前扫描、领取、执行并关闭一批有界的冷 turn 回收任务。
func TestRetentionUseCaseRunMaintenanceExecutesColdTurnRecycleJobs(t *testing.T) {
	store := &fakeRetentionStore{
		turnJobCount: 1,
		claimedRecycleJobs: []logicdomain.RecycleJobRecord{
			{ID: 71, SessionID: 33, ProjectID: 9, JobType: logicdomain.RecycleJobTypeColdTurn, AttemptCount: 1},
		},
		coldTurnResult: logicdomain.ColdTurnRecycleResult{
			BatchID:           17,
			SessionID:         33,
			ProjectID:         9,
			RecycledTurnCount: 4,
		},
	}
	useCase := &RetentionUseCase{
		store:  store,
		vector: &fakeVectorStore{},
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if got, want := store.turnJobQuery.Limit, defaultRetentionTurnJobScanBatchSize; got != want {
		t.Fatalf("cold-turn job scan limit = %d, want %d", got, want)
	}
	if got, want := store.claimJobType, logicdomain.RecycleJobTypeColdTurn; got != want {
		t.Fatalf("recycle job claim type = %q, want %q", got, want)
	}
	if got, want := store.claimJobLimit, defaultRetentionRecycleJobBatchSize; got != want {
		t.Fatalf("recycle job claim limit = %d, want %d", got, want)
	}
	if got, want := store.coldTurnQuery.SessionID, uint64(33); got != want {
		t.Fatalf("cold-turn recycle session id = %d, want %d", got, want)
	}
	if got, want := store.coldTurnQuery.TurnHotWindowSize, 8; got != want {
		t.Fatalf("cold-turn recycle hot window = %d, want %d", got, want)
	}
	if got, want := store.coldTurnQuery.RecycleReason, logicdomain.RecycleReasonColdTurnArchive; got != want {
		t.Fatalf("cold-turn recycle reason = %q, want %q", got, want)
	}
	if len(store.completedRecycleJobIDs) != 1 || store.completedRecycleJobIDs[0] != 71 {
		t.Fatalf("completed recycle job ids = %v, want [71]", store.completedRecycleJobIDs)
	}
	if !store.completedRecycleAt.IsZero() && (store.completedRecycleAt.Before(beforeRun.Add(-time.Second)) || store.completedRecycleAt.After(afterRun.Add(time.Second))) {
		t.Fatalf("completed recycle at = %v, want between %v and %v", store.completedRecycleAt, beforeRun, afterRun)
	}
	if store.claimJobUntil.Before(beforeRun.Add(defaultRetentionRecycleJobClaimLease-time.Second)) || store.claimJobUntil.After(afterRun.Add(defaultRetentionRecycleJobClaimLease+time.Second)) {
		t.Fatalf("recycle job claim until = %v, want around maintenance time + lease", store.claimJobUntil)
	}
}

// TestRetentionUseCaseRunMaintenanceRetriesFailedColdTurnRecycleJobs verifies failed cold-turn execution is rescheduled with a later next-run timestamp instead of being silently dropped.
// TestRetentionUseCaseRunMaintenanceRetriesFailedColdTurnRecycleJobs 用于验证冷 turn 执行失败后会带着更晚的 next-run 时间重新调度，而不是被静默丢弃。
func TestRetentionUseCaseRunMaintenanceRetriesFailedColdTurnRecycleJobs(t *testing.T) {
	store := &fakeRetentionStore{
		claimedRecycleJobs: []logicdomain.RecycleJobRecord{
			{ID: 72, SessionID: 34, ProjectID: 9, JobType: logicdomain.RecycleJobTypeColdTurn, AttemptCount: 2},
		},
		coldTurnErr: errors.New("cold turn recycle failed"),
	}
	useCase := &RetentionUseCase{
		store:  store,
		vector: &fakeVectorStore{},
		cfg: RetentionConfig{
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if len(store.retriedRecycleJobIDs) != 1 || store.retriedRecycleJobIDs[0] != 72 {
		t.Fatalf("retried recycle job ids = %v, want [72]", store.retriedRecycleJobIDs)
	}
	if store.recycleRetryLastError == "" || !strings.Contains(store.recycleRetryLastError, "cold turn recycle failed") {
		t.Fatalf("recycle retry last error = %q", store.recycleRetryLastError)
	}
	minRetryAt := beforeRun.Add(defaultRetentionRecycleJobRetryDelay)
	maxRetryAt := afterRun.Add(defaultRetentionRecycleJobRetryDelay)
	if store.recycleRetryAt.Before(minRetryAt.Add(-time.Second)) || store.recycleRetryAt.After(maxRetryAt.Add(time.Second)) {
		t.Fatalf("recycle retry at = %v, want between %v and %v", store.recycleRetryAt, minRetryAt, maxRetryAt)
	}
	if len(store.completedRecycleJobIDs) != 0 {
		t.Fatalf("completed recycle job ids = %v, want none", store.completedRecycleJobIDs)
	}
}

// TestRetentionUseCaseRunVectorGCMaintenanceReschedulesClaimedVectorGCJobsOnRetryFailure verifies the dedicated vector-gc maintenance path reschedules failed retry batches with a later next-run time instead of dropping them after the second failure, even when classic retention recycle is disabled.
// TestRetentionUseCaseRunVectorGCMaintenanceReschedulesClaimedVectorGCJobsOnRetryFailure 用于验证独立的 vector-gc 维护路径会把再次删除失败的重试批次重新调度到更晚的 next-run 时间，而不是在第二次失败后丢失；即使经典 retention recycle 已禁用也成立。
func TestRetentionUseCaseRunVectorGCMaintenanceReschedulesClaimedVectorGCJobsOnRetryFailure(t *testing.T) {
	store := &fakeRetentionStore{
		claimedJobs: []logicdomain.VectorGCJobRecord{
			{ID: 61, BatchID: 0, VectorID: "vec-retry-3", JobType: logicdomain.VectorGCJobTypeRetentionRecycle, AttemptCount: 3},
		},
	}
	vector := &fakeVectorStore{deleteErrs: []error{errors.New("vector retry still failing")}}
	useCase := &RetentionUseCase{
		store:  store,
		vector: vector,
		cfg: RetentionConfig{
			Enabled:                 false,
			SessionIdleRecycleAfter: 15 * 24 * time.Hour,
			TurnHotWindowSize:       8,
			TrashRetention:          30 * 24 * time.Hour,
		},
	}

	beforeRun := time.Now().UTC()
	useCase.runVectorGCMaintenance(context.Background())
	afterRun := time.Now().UTC()

	if len(store.retriedJobIDs) != 1 || store.retriedJobIDs[0] != 61 {
		t.Fatalf("retried job ids = %v, want [61]", store.retriedJobIDs)
	}
	if store.retryLastError == "" || !strings.Contains(store.retryLastError, "vector retry still failing") {
		t.Fatalf("retry last error = %q", store.retryLastError)
	}
	minRetryAt := beforeRun.Add(defaultRetentionVectorGCRetryDelay)
	maxRetryAt := afterRun.Add(defaultRetentionVectorGCRetryDelay)
	if store.retryAt.Before(minRetryAt.Add(-time.Second)) || store.retryAt.After(maxRetryAt.Add(time.Second)) {
		t.Fatalf("retry at = %v, want between %v and %v", store.retryAt, minRetryAt, maxRetryAt)
	}
	if len(store.completedJobIDs) != 0 {
		t.Fatalf("completed job ids = %v, want none", store.completedJobIDs)
	}
}
