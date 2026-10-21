// Memory use-case tests for memory delete test.
// 记忆应用用例层删除与向量清理测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"testing"
)

// TestMemoryUseCaseDeleteMarksScopedMemoriesAndCleansVectors verifies manual deletion resolves hierarchy scope, flips relation rows, and removes sidecar vectors best-effort.
// TestMemoryUseCaseDeleteMarksScopedMemoriesAndCleansVectors 用于验证手工删除会解析层级范围、切换关系行状态，并尽力删除旁路向量。
func TestMemoryUseCaseDeleteMarksScopedMemoriesAndCleansVectors(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		deleteMemoryResult: logicdomain.MemoryDeleteResult{
			DeletedMemoryIDs:  []uint64{301},
			NotFoundMemoryIDs: []uint64{404},
			DeletedVectorIDs:  []string{"vec-301"},
		},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, nil, vector, nil)

	result, err := uc.Delete(context.Background(), DeleteMemoriesCommand{
		UserID:    7,
		ProjectID: 9,
		MemoryIDs: []uint64{301, 301, 404},
		Reason:    "  无用记忆  ",
	})
	if err != nil {
		t.Fatalf("delete memories: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 1 || result.DeletedMemoryIDs[0] != 301 {
		t.Fatalf("unexpected deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 1 || result.NotFoundMemoryIDs[0] != 404 {
		t.Fatalf("unexpected not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if result.DeletedVectorRows != 1 {
		t.Fatalf("deleted vector rows = %d", result.DeletedVectorRows)
	}
	if len(store.deleteMemoryCalls) != 1 {
		t.Fatalf("delete call count = %d", len(store.deleteMemoryCalls))
	}
	call := store.deleteMemoryCalls[0]
	if got := call.MemoryIDs; len(got) != 2 || got[0] != 301 || got[1] != 404 {
		t.Fatalf("unexpected memory ids passed to store: %+v", got)
	}
	if call.Filter.UserID != 7 || call.Filter.TeamID != 3 || call.Filter.SpaceID != 5 || call.Filter.ProjectID != 9 {
		t.Fatalf("unexpected delete filter: %+v", call.Filter)
	}
	if call.Reason != "无用记忆" {
		t.Fatalf("delete reason = %q", call.Reason)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != "vec-301" {
		t.Fatalf("unexpected vector delete calls: %+v", vector.deleteIDsCalls)
	}
}

// TestMemoryUseCaseDeleteOrdersResultsByRequestMemoryIDs verifies store-level sorted delete results are projected back to the caller's first-seen memory id order.
// TestMemoryUseCaseDeleteOrdersResultsByRequestMemoryIDs 用于验证存储层排序后的删除结果会投射回调用方 memory id 的首次出现顺序。
func TestMemoryUseCaseDeleteOrdersResultsByRequestMemoryIDs(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		deleteMemoryResult: logicdomain.MemoryDeleteResult{
			DeletedMemoryIDs:  []uint64{301, 302},
			NotFoundMemoryIDs: []uint64{404, 405},
		},
	}
	uc := NewMemoryUseCase(profiles, store, nil, nil, nil)

	result, err := uc.Delete(context.Background(), DeleteMemoriesCommand{
		UserID:    7,
		ProjectID: 9,
		MemoryIDs: []uint64{404, 302, 301, 405, 302},
	})
	if err != nil {
		t.Fatalf("delete memories: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 2 || result.DeletedMemoryIDs[0] != 302 || result.DeletedMemoryIDs[1] != 301 {
		t.Fatalf("unexpected ordered deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if len(result.NotFoundMemoryIDs) != 2 || result.NotFoundMemoryIDs[0] != 404 || result.NotFoundMemoryIDs[1] != 405 {
		t.Fatalf("unexpected ordered not-found ids: %+v", result.NotFoundMemoryIDs)
	}
	if len(store.deleteMemoryCalls) != 1 {
		t.Fatalf("delete call count = %d", len(store.deleteMemoryCalls))
	}
	if got := store.deleteMemoryCalls[0].MemoryIDs; len(got) != 4 || got[0] != 404 || got[1] != 302 || got[2] != 301 || got[3] != 405 {
		t.Fatalf("unexpected memory ids passed to store: %+v", got)
	}
}

// TestMemoryUseCaseDeleteEnqueuesVectorGCWhenCleanupFails verifies manual deletion remains successful after relational status changes and persists failed sidecar cleanup for retry.
// TestMemoryUseCaseDeleteEnqueuesVectorGCWhenCleanupFails 用于验证关系状态变更成功后，即使旁路向量清理失败，手工删除仍成功并持久化重试任务。
func TestMemoryUseCaseDeleteEnqueuesVectorGCWhenCleanupFails(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		deleteMemoryResult: logicdomain.MemoryDeleteResult{
			DeletedMemoryIDs: []uint64{301},
			DeletedVectorIDs: []string{"vec-301"},
		},
	}
	vector := &stubVectorStore{deleteErr: errors.New("vector delete unavailable")}
	uc := NewMemoryUseCase(profiles, store, nil, vector, nil)

	result, err := uc.Delete(context.Background(), DeleteMemoriesCommand{
		UserID:    7,
		ProjectID: 9,
		MemoryIDs: []uint64{301},
	})
	if err != nil {
		t.Fatalf("delete memories: %v", err)
	}
	if len(result.DeletedMemoryIDs) != 1 || result.DeletedMemoryIDs[0] != 301 {
		t.Fatalf("unexpected deleted ids: %+v", result.DeletedMemoryIDs)
	}
	if result.DeletedVectorRows != 0 {
		t.Fatalf("deleted vector rows = %d", result.DeletedVectorRows)
	}
	if store.vectorGCEnqueueQuery.JobType != logicdomain.VectorGCJobTypeManualMemoryDelete {
		t.Fatalf("vector gc job type = %q, want %q", store.vectorGCEnqueueQuery.JobType, logicdomain.VectorGCJobTypeManualMemoryDelete)
	}
	if got := store.vectorGCEnqueueQuery.VectorIDs; len(got) != 1 || got[0] != "vec-301" {
		t.Fatalf("vector gc ids = %+v", got)
	}
	if store.vectorGCEnqueueQuery.NextRunAt.IsZero() {
		t.Fatal("expected manual-delete vector gc compensation to schedule a retry time")
	}
}

// TestMemoryUseCaseDeleteCleansVectorsBeforeReturningOutcomeUncertain verifies manual deletion compensates confirmed sidecar vectors before surfacing a post-delete uncertain store boundary.
// TestMemoryUseCaseDeleteCleansVectorsBeforeReturningOutcomeUncertain 用于验证手工删除在返回删除后结果不确定错误前，会先补偿已经确认的旁路向量。
func TestMemoryUseCaseDeleteCleansVectorsBeforeReturningOutcomeUncertain(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		deleteMemoryResult: logicdomain.MemoryDeleteResult{
			DeletedMemoryIDs: []uint64{301},
			DeletedVectorIDs: []string{"vec-301"},
		},
		deleteMemoryErr: logicdomain.OutcomeUncertainError{
			Operation: "delete memory nodes",
			Message:   "sync memory fts after manual delete: fts delete unavailable",
		},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, nil, vector, nil)

	result, err := uc.Delete(context.Background(), DeleteMemoriesCommand{
		UserID:    7,
		ProjectID: 9,
		MemoryIDs: []uint64{301},
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected manual delete outcome-uncertain error, got %v", err)
	}
	if len(result.DeletedMemoryIDs) != 1 || result.DeletedMemoryIDs[0] != 301 {
		t.Fatalf("unexpected deleted ids returned with outcome uncertainty: %+v", result.DeletedMemoryIDs)
	}
	if result.DeletedVectorRows != 1 {
		t.Fatalf("deleted vector rows = %d", result.DeletedVectorRows)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 || vector.deleteIDsCalls[0][0] != "vec-301" {
		t.Fatalf("expected vector cleanup before returning uncertain delete error, got %+v", vector.deleteIDsCalls)
	}
	if store.vectorGCEnqueueQuery.JobType != "" || len(store.vectorGCEnqueueQuery.VectorIDs) != 0 {
		t.Fatalf("did not expect vector gc compensation when immediate cleanup succeeds, got %+v", store.vectorGCEnqueueQuery)
	}
}
