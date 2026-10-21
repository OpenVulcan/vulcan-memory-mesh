// Memory use-case tests for memory write commit test.
// 记忆应用用例层写入提交与向量清理测试，覆盖召回或写入链路。
package usecase

import (
	"context"
	"errors"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
)

// TestMemoryUseCaseWriteKeepsFreshVectorOnDirectWriteOutcomeUncertain verifies a partial relational direct-write failure does not delete the vector that a durable memory row may already reference.
// TestMemoryUseCaseWriteKeepsFreshVectorOnDirectWriteOutcomeUncertain 用于验证主动写关系侧结果不确定时，不会删除可能已被长期记忆行引用的新向量。
func TestMemoryUseCaseWriteKeepsFreshVectorOnDirectWriteOutcomeUncertain(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyErr: logicdomain.OutcomeUncertainError{
			Operation:            "apply direct memory write",
			Message:              "supersede direct memory nodes affected 1 rows, want 2",
			FreshVectorReference: true,
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	_, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  47,
			SessionKey: "sess-direct-outcome-uncertain",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录新的稳定工程约束。",
			Details:    "记录新的稳定工程约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected direct write outcome-uncertain error, got %v", err)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one fresh vector upsert before relational uncertainty, got %+v", vector.upserts)
	}
	if len(vector.deleteIDsCalls) != 0 {
		t.Fatalf("did not expect uncertain relational outcome to rollback fresh vector, got %+v", vector.deleteIDsCalls)
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one direct write persistence attempt, got %+v", store.directWriteApplyCalls)
	}
}

// TestMemoryUseCaseWriteRollsBackFreshVectorWhenOutcomeUncertainButUnreferenced verifies direct-write rollback depends on durable vector references instead of the broad uncertain-outcome marker alone.
// TestMemoryUseCaseWriteRollsBackFreshVectorWhenOutcomeUncertainButUnreferenced 用于验证主动写回滚取决于长期向量引用标记，而不是只看宽泛的结果不确定标记。
func TestMemoryUseCaseWriteRollsBackFreshVectorWhenOutcomeUncertainButUnreferenced(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyErr: logicdomain.OutcomeUncertainError{
			Operation: "apply direct memory write",
			Message:   "pre-insert profile check became uncertain",
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	_, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  48,
			SessionKey: "sess-direct-outcome-uncertain-unreferenced",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "记录新的稳定工程约束。",
			Details:    "记录新的稳定工程约束。",
			Category:   logicdomain.MemoryNodeCategoryTechSpecAPI,
		}},
	})
	if err == nil || !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected direct write outcome-uncertain error, got %v", err)
	}
	if len(vector.upserts) != 1 {
		t.Fatalf("expected one fresh vector upsert before relational uncertainty, got %+v", vector.upserts)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDsCalls[0]) != 1 {
		t.Fatalf("expected fresh vector rollback when relation has no fresh vector reference, got %+v", vector.deleteIDsCalls)
	}
}

// TestMemoryUseCaseWriteScrubsPIIBeforeEmbeddingAndPersistence verifies direct-write memories are redacted before the write flow computes embeddings and persists durable rows, so later post-action recall never reintroduces raw PII from this entry path.
// TestMemoryUseCaseWriteScrubsPIIBeforeEmbeddingAndPersistence 用于验证主动写记忆会在进入 embedding 与长期持久化前先完成脱敏，避免后续 post-action 从这条入口重新读回明文 PII。
func TestMemoryUseCaseWriteScrubsPIIBeforeEmbeddingAndPersistence(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)
	uc.ConfigurePIIScrubber(stubPIIScrubber{
		replacements: map[string]string{
			"13800138000":       "[PHONE]",
			"alice@example.com": "[EMAIL]",
		},
	})

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  52,
			SessionKey: "sess-direct-pii",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel: logicdomain.MemoryScopeLevelProject,
			Abstract:   "联系人电话 13800138000",
			Details:    "联系邮箱 alice@example.com，电话 13800138000",
			Category:   logicdomain.MemoryNodeCategoryProjectContext,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Ref.ID == 0 {
		t.Fatalf("expected one persisted result, got %+v", result.Items)
	}
	if len(store.directWriteApplyCalls) != 1 {
		t.Fatalf("expected one direct-write apply call, got %+v", store.directWriteApplyCalls)
	}
	call := store.directWriteApplyCalls[0]
	if strings.Contains(call.Record.Abstract, "13800138000") || !strings.Contains(call.Record.Abstract, "[PHONE]") {
		t.Fatalf("expected persisted abstract to be scrubbed, got %+v", call.Record)
	}
	if strings.Contains(call.Record.Details, "13800138000") || strings.Contains(call.Record.Details, "alice@example.com") {
		t.Fatalf("expected persisted details to be scrubbed, got %+v", call.Record)
	}
	if !strings.Contains(call.Record.Details, "[PHONE]") || !strings.Contains(call.Record.Details, "[EMAIL]") {
		t.Fatalf("expected scrubbed placeholders to remain in details, got %+v", call.Record)
	}
	if len(embedding.requests) != 1 || len(embedding.requests[0].Texts) != 1 {
		t.Fatalf("expected one embedding request, got %+v", embedding.requests)
	}
	if strings.Contains(embedding.requests[0].Texts[0], "13800138000") || strings.Contains(embedding.requests[0].Texts[0], "alice@example.com") {
		t.Fatalf("expected embedding text to be scrubbed, got %+v", embedding.requests[0])
	}
}

// TestMemoryUseCaseWriteEnqueuesVectorGCCompensationWhenSupersededCleanupFails verifies direct-write success still persists a retry job when deleting superseded vectors fails after the relational transaction commits.
// TestMemoryUseCaseWriteEnqueuesVectorGCCompensationWhenSupersededCleanupFails 用于验证当关系事务已提交、但删除 superseded 向量失败时，direct-write 成功路径仍会持久化一个重试任务。
func TestMemoryUseCaseWriteEnqueuesVectorGCCompensationWhenSupersededCleanupFails(t *testing.T) {
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:          1402,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "新的灰度发布策略已经生效。",
				Details:     "新的灰度发布策略已经生效。",
				VectorID:    "vec-new-direct",
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			},
			SupersededVectorIDs: []string{" vec-old-direct-1 ", "", "vec-old-direct-1", "vec-old-direct-2"},
		},
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{deleteErr: errors.New("vector delete unavailable")}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(context.Background(), WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  48,
			SessionKey: "sess-direct-vector-gc",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    "新的灰度发布策略已经生效。",
			Details:     "新的灰度发布策略已经生效。",
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Ref.ID != 1402 || result.Items[0].Deduped {
		t.Fatalf("unexpected direct-write result: %+v", result.Items)
	}
	if len(vector.deleteIDsCalls) != 1 {
		t.Fatalf("expected one superseded-vector delete attempt, got %+v", vector.deleteIDsCalls)
	}
	if got, want := vector.deleteIDsCalls[0], []string{"vec-old-direct-1", "vec-old-direct-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("superseded vector delete ids = %+v, want %+v", got, want)
	}
	if store.vectorGCEnqueueQuery.JobType != logicdomain.VectorGCJobTypeDirectWriteSupersedeCleanup {
		t.Fatalf("vector gc job type = %q, want %q", store.vectorGCEnqueueQuery.JobType, logicdomain.VectorGCJobTypeDirectWriteSupersedeCleanup)
	}
	if got, want := store.vectorGCEnqueueQuery.VectorIDs, []string{"vec-old-direct-1", "vec-old-direct-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("vector gc compensation ids = %+v, want %+v", got, want)
	}
	if store.vectorGCEnqueueQuery.NextRunAt.IsZero() {
		t.Fatal("expected direct-write vector gc compensation to schedule a retry time")
	}
}

// TestMemoryUseCaseWriteSupersededCleanupSurvivesCallerCancellationAfterCommit verifies post-commit direct-write vector cleanup does not inherit a caller context that is cancelled after the relational outcome commits.
// TestMemoryUseCaseWriteSupersededCleanupSurvivesCallerCancellationAfterCommit 用于验证 direct-write 关系结果提交后的向量清理不会继承已在提交后取消的调用方 context。
func TestMemoryUseCaseWriteSupersededCleanupSurvivesCallerCancellationAfterCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	profiles := &stubProfileStore{
		targets: map[int]logicdomain.ProfileTargetRef{
			logicdomain.ProfileTypeUser:    {ProfileType: logicdomain.ProfileTypeUser, BindID: 7, UserID: 7},
			logicdomain.ProfileTypeProject: {ProfileType: logicdomain.ProfileTypeProject, BindID: 9, UserID: 7, TeamID: 3, SpaceID: 5, ProjectID: 9},
		},
	}
	store := &stubTurnLookupStore{
		directWriteApplyResult: logicdomain.DirectMemoryWriteApplyResult{
			InsertedMemoryNode: logicdomain.MemoryNodeRecord{
				ID:          1403,
				SourceKind:  logicdomain.MemorySourceKindGRPCAIWrite,
				ScopeLevel:  logicdomain.MemoryScopeLevelProject,
				Abstract:    "新的发布窗口已经确定。",
				Details:     "新的发布窗口已经确定。",
				VectorID:    "vec-new-direct",
				Category:    logicdomain.MemoryNodeCategoryProjectContext,
				Priority:    logicdomain.MemoryPriorityP1,
				MemoryLevel: logicdomain.MemoryLevelStable,
			},
			SupersededVectorIDs: []string{"vec-old-direct"},
		},
		afterDirectWriteApply: cancel,
	}
	embedding := &stubEmbeddingClient{
		response: appports.EmbeddingResponse{Vectors: [][]float32{{0.1, 0.2, 0.3}}},
	}
	vector := &stubVectorStore{deleteErr: errors.New("vector delete unavailable")}
	uc := NewMemoryUseCase(profiles, store, embedding, vector, nil)

	result, err := uc.Write(ctx, WriteMemoriesCommand{
		Session: logicdomain.SessionRef{
			SessionID:  49,
			SessionKey: "sess-direct-cleanup-detached",
			UserID:     7,
			TeamID:     3,
			SpaceID:    5,
			ProjectID:  9,
		},
		Items: []WriteMemoryItem{{
			ScopeLevel:  logicdomain.MemoryScopeLevelProject,
			Abstract:    "新的发布窗口已经确定。",
			Details:     "新的发布窗口已经确定。",
			Category:    logicdomain.MemoryNodeCategoryProjectContext,
			Priority:    logicdomain.MemoryPriorityP1,
			MemoryLevel: logicdomain.MemoryLevelStable,
		}},
	})
	if err != nil {
		t.Fatalf("write memories after caller cancellation at commit boundary: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("expected original caller context to be cancelled by the store hook")
	}
	if len(result.Items) != 1 || result.Items[0].Ref.ID != 1403 {
		t.Fatalf("unexpected direct-write result: %+v", result.Items)
	}
	if len(vector.deleteIDsCalls) != 1 || len(vector.deleteIDContextErrs) != 1 {
		t.Fatalf("expected one detached superseded-vector delete attempt, got calls=%+v ctxErrs=%+v", vector.deleteIDsCalls, vector.deleteIDContextErrs)
	}
	if vector.deleteIDContextErrs[0] != nil {
		t.Fatalf("expected vector cleanup to use a non-cancelled context, got %v", vector.deleteIDContextErrs[0])
	}
	if got, want := store.vectorGCEnqueueQuery.VectorIDs, []string{"vec-old-direct"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("vector gc compensation ids = %+v, want %+v", got, want)
	}
	if len(store.vectorGCEnqueueCtxErrs) != 1 || store.vectorGCEnqueueCtxErrs[0] != nil {
		t.Fatalf("expected vector gc compensation to use a non-cancelled context, got %+v", store.vectorGCEnqueueCtxErrs)
	}
}
