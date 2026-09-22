// management_test.go verifies opaque pagination, display projection, and UTF-8-safe content chunking.
// management_test.go 用于验证不透明分页、显示投影与 UTF-8 安全内容分片。
package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// testManagementStore is the focused management port stub used by use-case tests.
// testManagementStore 是管理用例测试使用的聚焦管理端口桩。
type testManagementStore struct {
	sessionPage      logicdomain.ManagementSessionPage
	lastSessionQuery logicdomain.ManagementSessionQuery
	lastTurnQuery    logicdomain.ManagementTurnQuery
}

// testManagementMutationStore records preview-bound writes without a database.
// testManagementMutationStore 用于在不依赖数据库的情况下记录受预览约束的写入。
type testManagementMutationStore struct {
	testManagementStore
	impact               logicdomain.ManagementImpact
	preview              logicdomain.ManagementPreview
	operation            logicdomain.ManagementOperation
	applyCalls           int
	purgeCalls           int
	restoreCalls         int
	operationByKeySeen   bool
	quarantinedVectorIDs []string
	vectorGCEnqueue      logicdomain.VectorGCJobEnqueueQuery
}

// testManagementVectorStore records permanent sidecar deletes requested by management purge.
// testManagementVectorStore 用于记录管理永久清理发起的旁路向量删除。
type testManagementVectorStore struct {
	deletedIDs []string
	deleteErr  error
}

// TestNormalizeManagementSelectionDefaultsToVMMSource verifies standalone management records carry an explicit VMM origin.
// TestNormalizeManagementSelectionDefaultsToVMMSource 用于验证独立管理记录会带有明确的 VMM 来源。
func TestNormalizeManagementSelectionDefaultsToVMMSource(t *testing.T) {
	selection, err := normalizeManagementSelection(logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession,
		Action:     logicdomain.ManagementActionArchive,
		TargetIDs:  []uint64{42},
	})
	if err != nil {
		t.Fatalf("normalizeManagementSelection returned error: %v", err)
	}
	if selection.Source != "vmm-local" {
		t.Fatalf("default management source = %q, want %q", selection.Source, "vmm-local")
	}
}

// Upsert is unused by management purge tests.
// Upsert 在管理永久清理测试中不使用。
func (*testManagementVectorStore) Upsert(context.Context, logicdomain.MemoryRecord) error { return nil }

// Search is unused by management purge tests.
// Search 在管理永久清理测试中不使用。
func (*testManagementVectorStore) Search(context.Context, []float32, int, logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return nil, nil
}

// DeleteByFilter is unused by management purge tests.
// DeleteByFilter 在管理永久清理测试中不使用。
func (*testManagementVectorStore) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	return 0, nil
}

// DeleteByIDs records the exact quarantined identifiers released by permanent purge.
// DeleteByIDs 记录永久清理释放的准确隔离向量标识。
func (s *testManagementVectorStore) DeleteByIDs(_ context.Context, ids []string) (uint64, error) {
	s.deletedIDs = append([]string(nil), ids...)
	return uint64(len(ids)), s.deleteErr
}

// Shutdown is a no-op for the focused test double.
// Shutdown 对聚焦测试替身不执行操作。
func (*testManagementVectorStore) Shutdown(context.Context) error { return nil }

// ListManagementRecycleBatchVectorIDs returns the configured quarantined vector ids.
// ListManagementRecycleBatchVectorIDs 返回已配置的隔离向量标识。
func (s *testManagementMutationStore) ListManagementRecycleBatchVectorIDs(context.Context, []uint64) ([]string, error) {
	return append([]string(nil), s.quarantinedVectorIDs...), nil
}

// EnqueueVectorGCJobs records one post-commit cleanup compensation request.
// EnqueueVectorGCJobs 记录一条提交后清理补偿请求。
func (s *testManagementMutationStore) EnqueueVectorGCJobs(_ context.Context, query logicdomain.VectorGCJobEnqueueQuery) error {
	s.vectorGCEnqueue = query
	return nil
}

// InspectManagementSelection returns the currently configured live impact.
// InspectManagementSelection 返回当前配置的实时影响。
func (s *testManagementMutationStore) InspectManagementSelection(context.Context, logicdomain.ManagementRemovalSelection, int) (logicdomain.ManagementImpact, error) {
	return s.impact, nil
}

// SaveManagementPreview records the issued preview.
// SaveManagementPreview 记录已签发的预览。
func (s *testManagementMutationStore) SaveManagementPreview(_ context.Context, preview logicdomain.ManagementPreview) error {
	s.preview = preview
	return nil
}

// GetManagementPreview returns the recorded preview.
// GetManagementPreview 返回已记录的预览。
func (s *testManagementMutationStore) GetManagementPreview(context.Context, string) (logicdomain.ManagementPreview, error) {
	return s.preview, nil
}

// ApplyManagementRemoval records one non-purge mutation.
// ApplyManagementRemoval 记录一次非永久删除变更。
func (s *testManagementMutationStore) ApplyManagementRemoval(_ context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	s.applyCalls++
	s.operation = logicdomain.ManagementOperation{ID: command.OperationID, IdempotencyKey: command.IdempotencyKey, Action: command.Preview.Selection.Action, TargetType: command.Preview.Selection.TargetType, TargetIDs: command.Preview.Selection.TargetIDs, Status: "succeeded"}
	return s.operation, nil
}

// RestoreManagementBatch records one restore mutation.
// RestoreManagementBatch 记录一次恢复变更。
func (s *testManagementMutationStore) RestoreManagementBatch(_ context.Context, command logicdomain.ManagementRestoreCommand) (logicdomain.ManagementOperation, error) {
	s.restoreCalls++
	return logicdomain.ManagementOperation{ID: command.OperationID, IdempotencyKey: command.IdempotencyKey, Action: logicdomain.ManagementActionRestore, TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{command.BatchID}, Status: "succeeded"}, nil
}

// PurgeManagementBatch records one permanent-delete mutation.
// PurgeManagementBatch 记录一次永久删除变更。
func (s *testManagementMutationStore) PurgeManagementBatch(_ context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	s.purgeCalls++
	return logicdomain.ManagementOperation{ID: command.OperationID, IdempotencyKey: command.IdempotencyKey, Action: command.Preview.Selection.Action, TargetType: command.Preview.Selection.TargetType, TargetIDs: command.Preview.Selection.TargetIDs, Status: "succeeded"}, nil
}

// GetManagementOperation returns the configured operation.
// GetManagementOperation 返回已配置的操作。
func (s *testManagementMutationStore) GetManagementOperation(context.Context, string) (logicdomain.ManagementOperation, error) {
	return s.operation, nil
}

// GetManagementOperationByIdempotencyKey returns a prior operation only after one write has completed.
// GetManagementOperationByIdempotencyKey 仅在一次写入完成后返回既有操作。
func (s *testManagementMutationStore) GetManagementOperationByIdempotencyKey(context.Context, string) (logicdomain.ManagementOperation, error) {
	if !s.operationByKeySeen && s.operation.ID == "" {
		return logicdomain.ManagementOperation{}, logicdomain.NotFoundError{Resource: "operation", Message: "operation does not exist"}
	}
	s.operationByKeySeen = true
	return s.operation, nil
}

// ListManagementRecycleBatches returns an empty page for interface completeness.
// ListManagementRecycleBatches 为满足接口返回空分页。
func (s *testManagementMutationStore) ListManagementRecycleBatches(context.Context, uint64, int) (logicdomain.ManagementRecycleBatchPage, error) {
	return logicdomain.ManagementRecycleBatchPage{}, nil
}

// GetManagementRecycleBatch returns an empty batch for interface completeness.
// GetManagementRecycleBatch 为满足接口返回空批次。
func (s *testManagementMutationStore) GetManagementRecycleBatch(context.Context, uint64) (logicdomain.ManagementRecycleBatch, error) {
	return logicdomain.ManagementRecycleBatch{}, nil
}

// ListManagementSessions records one normalized query and returns the configured page.
// ListManagementSessions 用于记录规范化查询并返回预设页面。
func (s *testManagementStore) ListManagementSessions(_ context.Context, query logicdomain.ManagementSessionQuery) (logicdomain.ManagementSessionPage, error) {
	s.lastSessionQuery = query
	return s.sessionPage, nil
}

// GetManagementSession returns an empty record because detail behavior is covered by adapter tests.
// GetManagementSession 返回空记录，因为详情行为由适配器测试覆盖。
func (s *testManagementStore) GetManagementSession(context.Context, uint64) (logicdomain.ManagementSessionRecord, error) {
	return logicdomain.ManagementSessionRecord{}, nil
}

// ListManagementTurns returns an empty page for interface completeness.
// ListManagementTurns 为满足接口返回空分页。
func (s *testManagementStore) ListManagementTurns(_ context.Context, query logicdomain.ManagementTurnQuery) (logicdomain.ManagementTurnPage, error) {
	s.lastTurnQuery = query
	return logicdomain.ManagementTurnPage{}, nil
}

// TestManagementTurnsAcceptPassedStatus verifies management readers can explicitly select durable analysis Pass terminals.
// TestManagementTurnsAcceptPassedStatus 用于验证管理读取端可以显式筛选持久化分析 Pass 终态。
func TestManagementTurnsAcceptPassedStatus(t *testing.T) {
	store := &testManagementStore{}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	if _, err := management.ListTurns(context.Background(), ManagementTurnFilters{Status: "passed"}); err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	if store.lastTurnQuery.Status != "passed" {
		t.Fatalf("turn status query = %q, want passed", store.lastTurnQuery.Status)
	}
}

// GetManagementTurn returns an empty record for interface completeness.
// GetManagementTurn 为满足接口返回空记录。
func (s *testManagementStore) GetManagementTurn(context.Context, uint64) (logicdomain.ManagementTurnRecord, error) {
	return logicdomain.ManagementTurnRecord{}, nil
}

// ListUsers returns an empty user list for interface completeness.
// ListUsers 为满足接口返回空用户列表。
func (s *testManagementStore) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return []logicdomain.UserRecord{}, nil
}

// ListProjects returns an empty project list for interface completeness.
// ListProjects 为满足接口返回空项目列表。
func (s *testManagementStore) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return []logicdomain.ProjectRecord{}, nil
}

// TestManagementSessionCursorRoundTrip verifies the next page preserves the exact sort and stable row boundary.
// TestManagementSessionCursorRoundTrip 用于验证下一页保留精确排序与稳定行边界。
func TestManagementSessionCursorRoundTrip(t *testing.T) {
	updatedAt := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	store := &testManagementStore{sessionPage: logicdomain.ManagementSessionPage{
		Items:      []logicdomain.ManagementSessionRecord{{SessionRef: logicdomain.SessionRef{SessionID: 42, UpdatedAt: updatedAt}}},
		NextCursor: logicdomain.ManagementCursor{UpdatedAt: updatedAt, ID: 42}, HasMore: true,
	}}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	first, err := management.ListSessions(context.Background(), ManagementSessionFilters{Sort: "updated_desc"})
	if err != nil {
		t.Fatalf("ListSessions(first) error = %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("ListSessions(first) next cursor is empty")
	}
	store.sessionPage = logicdomain.ManagementSessionPage{}
	if _, err := management.ListSessions(context.Background(), ManagementSessionFilters{Sort: "updated_desc", Cursor: first.NextCursor}); err != nil {
		t.Fatalf("ListSessions(second) error = %v", err)
	}
	if store.lastSessionQuery.Cursor.ID != 42 || !store.lastSessionQuery.Cursor.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("decoded cursor = %+v", store.lastSessionQuery.Cursor)
	}
	if _, err := management.ListSessions(context.Background(), ManagementSessionFilters{Sort: "updated_asc", Cursor: first.NextCursor}); err == nil {
		t.Fatal("ListSessions() accepted cursor from a different sort")
	}
}

// TestSliceUTF8ContentPreservesRuneBoundaries verifies chunks never split one multi-byte code point.
// TestSliceUTF8ContentPreservesRuneBoundaries 用于验证分片不会切断多字节码点。
func TestSliceUTF8ContentPreservesRuneBoundaries(t *testing.T) {
	content := strings.Repeat("火", 8)
	chunk, next, more, err := SliceUTF8Content(content, 0, 7)
	if err != nil {
		t.Fatalf("SliceUTF8Content() error = %v", err)
	}
	if chunk != "火火" || next != 6 || !more {
		t.Fatalf("first chunk = %q next=%d more=%v", chunk, next, more)
	}
	if _, _, _, err := SliceUTF8Content(content, 1, 7); err == nil {
		t.Fatal("SliceUTF8Content() accepted a non-boundary offset")
	}
}

// TestManagementDisplayTextUsesFirstUserMessage verifies session titles never expose the raw session key.
// TestManagementDisplayTextUsesFirstUserMessage 用于验证会话标题不会暴露原始会话键。
func TestManagementDisplayTextUsesFirstUserMessage(t *testing.T) {
	management, err := NewManagementUseCase(&testManagementStore{}, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	record := logicdomain.ManagementSessionRecord{
		SessionRef:       logicdomain.SessionRef{SessionKey: "secret-session-key", CreatedAt: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)},
		FirstTurnContent: `{"user":"  设计   管理界面 ","timeline":[],"assistant":""}`,
	}
	if got := management.SessionDisplayTitle(record); got != "设计 管理界面" {
		t.Fatalf("SessionDisplayTitle() = %q", got)
	}
}

// TestManagementRemovalRequiresFreshUnprotectedPreview verifies stale and protected targets never reach the mutation store.
// TestManagementRemovalRequiresFreshUnprotectedPreview 验证陈旧或受保护目标不会进入变更存储。
func TestManagementRemovalRequiresFreshUnprotectedPreview(t *testing.T) {
	store := &testManagementMutationStore{impact: logicdomain.ManagementImpact{Revision: "revision-1"}}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	preview, err := management.CreateRemovalPreview(context.Background(), logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetTurn, TargetIDs: []uint64{8}, Action: logicdomain.ManagementActionRecycle,
	})
	if err != nil {
		t.Fatalf("CreateRemovalPreview() error = %v", err)
	}
	store.impact = logicdomain.ManagementImpact{Revision: "revision-2"}
	if _, err := management.ExecuteRemoval(context.Background(), preview.Token, "stale-key", ""); !logicdomain.IsConflictError(err) {
		t.Fatalf("ExecuteRemoval(stale) error = %v, want conflict", err)
	}
	store.impact = logicdomain.ManagementImpact{Revision: "revision-1", ProtectedCount: 1}
	if _, err := management.ExecuteRemoval(context.Background(), preview.Token, "protected-key", ""); !logicdomain.IsProtectedResourceError(err) {
		t.Fatalf("ExecuteRemoval(protected) error = %v, want protected resource", err)
	}
	if store.applyCalls != 0 {
		t.Fatalf("apply calls = %d, want 0", store.applyCalls)
	}
}

// TestManagementPurgeRequiresConfirmation verifies permanent deletion cannot run without the exact phrase.
// TestManagementPurgeRequiresConfirmation 验证永久删除必须输入完全一致的确认短语。
func TestManagementPurgeRequiresConfirmation(t *testing.T) {
	store := &testManagementMutationStore{
		impact:               logicdomain.ManagementImpact{Revision: "purge-revision"},
		quarantinedVectorIDs: []string{" vector-2 ", "vector-1", "vector-2"},
	}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	vector := &testManagementVectorStore{}
	if err := management.ConfigureVectorQuarantine(vector); err != nil {
		t.Fatalf("ConfigureVectorQuarantine() error = %v", err)
	}
	preview, err := management.CreateRemovalPreview(context.Background(), logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{12}, Action: logicdomain.ManagementActionPurge,
	})
	if err != nil {
		t.Fatalf("CreateRemovalPreview() error = %v", err)
	}
	if _, err := management.ExecuteRemovalForAction(context.Background(), preview.Token, "purge-key", "删除", logicdomain.ManagementActionPurge); !logicdomain.IsConfirmationRequired(err) {
		t.Fatalf("ExecuteRemovalForAction(unconfirmed) error = %v, want confirmation", err)
	}
	if _, err := management.ExecuteRemovalForAction(context.Background(), preview.Token, "purge-key", ManagementPurgeConfirmation, logicdomain.ManagementActionPurge); err != nil {
		t.Fatalf("ExecuteRemovalForAction(confirmed) error = %v", err)
	}
	if store.purgeCalls != 1 {
		t.Fatalf("purge calls = %d, want 1", store.purgeCalls)
	}
	if got := strings.Join(vector.deletedIDs, ","); got != "vector-2,vector-1" {
		t.Fatalf("deleted vector ids = %q", got)
	}
}

// TestManagementPurgeQueuesFailedVectorCleanup verifies a committed purge never loses sidecar cleanup coordinates.
// TestManagementPurgeQueuesFailedVectorCleanup 验证已提交的永久清理不会丢失旁路向量清理坐标。
func TestManagementPurgeQueuesFailedVectorCleanup(t *testing.T) {
	store := &testManagementMutationStore{
		impact:               logicdomain.ManagementImpact{Revision: "purge-revision"},
		quarantinedVectorIDs: []string{"vector-retry"},
	}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	vector := &testManagementVectorStore{deleteErr: errors.New("temporary vector outage")}
	if err := management.ConfigureVectorQuarantine(vector); err != nil {
		t.Fatalf("ConfigureVectorQuarantine() error = %v", err)
	}
	preview, err := management.CreateRemovalPreview(context.Background(), logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{12}, Action: logicdomain.ManagementActionPurge,
	})
	if err != nil {
		t.Fatalf("CreateRemovalPreview() error = %v", err)
	}
	if _, err := management.ExecuteRemovalForAction(context.Background(), preview.Token, "purge-retry-key", ManagementPurgeConfirmation, logicdomain.ManagementActionPurge); err != nil {
		t.Fatalf("ExecuteRemovalForAction() error = %v", err)
	}
	if store.vectorGCEnqueue.JobType != logicdomain.VectorGCJobTypeManagementPurge || strings.Join(store.vectorGCEnqueue.VectorIDs, ",") != "vector-retry" {
		t.Fatalf("vector GC compensation = %+v", store.vectorGCEnqueue)
	}
}

// TestManagementIdempotentRetryReturnsExistingOperation verifies a consumed preview may safely replay the same key.
// TestManagementIdempotentRetryReturnsExistingOperation 验证已消费预览可以用同一幂等键安全重放。
func TestManagementIdempotentRetryReturnsExistingOperation(t *testing.T) {
	store := &testManagementMutationStore{impact: logicdomain.ManagementImpact{Revision: "archive-revision"}}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	preview, err := management.CreateRemovalPreview(context.Background(), logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession, TargetIDs: []uint64{3}, Action: logicdomain.ManagementActionArchive,
	})
	if err != nil {
		t.Fatalf("CreateRemovalPreview() error = %v", err)
	}
	first, err := management.ExecuteRemoval(context.Background(), preview.Token, "archive-key", "")
	if err != nil {
		t.Fatalf("ExecuteRemoval(first) error = %v", err)
	}
	store.preview.ConsumedAt = time.Now().UTC()
	second, err := management.ExecuteRemoval(context.Background(), preview.Token, "archive-key", "")
	if err != nil {
		t.Fatalf("ExecuteRemoval(retry) error = %v", err)
	}
	if first.ID != second.ID || store.applyCalls != 1 {
		t.Fatalf("retry operation = %#v, first = %#v, apply calls = %d", second, first, store.applyCalls)
	}
}

// TestManagementRestoreRejectsReusedIdempotencyKey verifies one key cannot silently target another recycle batch.
// TestManagementRestoreRejectsReusedIdempotencyKey 验证同一幂等键不能被静默复用于另一个回收批次。
func TestManagementRestoreRejectsReusedIdempotencyKey(t *testing.T) {
	store := &testManagementMutationStore{operation: logicdomain.ManagementOperation{
		ID: "operation_existing", IdempotencyKey: "restore-key", Action: logicdomain.ManagementActionRestore,
		TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{41}, Status: "succeeded",
	}}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	if _, err := management.RestoreRecycleBatch(context.Background(), 42, "restore-key"); !logicdomain.IsConflictError(err) {
		t.Fatalf("RestoreRecycleBatch() error = %v, want conflict", err)
	}
	if store.restoreCalls != 0 {
		t.Fatalf("restore calls = %d, want 0", store.restoreCalls)
	}
}

// TestManagementEndpointValidationPrecedesIdempotentReplay verifies a key cannot bypass the endpoint action contract.
// TestManagementEndpointValidationPrecedesIdempotentReplay 验证幂等重放不能绕过端点动作契约。
func TestManagementEndpointValidationPrecedesIdempotentReplay(t *testing.T) {
	store := &testManagementMutationStore{
		preview: logicdomain.ManagementPreview{Selection: logicdomain.ManagementRemovalSelection{
			TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{7}, Action: logicdomain.ManagementActionPurge,
		}},
		operation: logicdomain.ManagementOperation{
			ID: "operation_purge", IdempotencyKey: "purge-key", Action: logicdomain.ManagementActionPurge,
			TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{7}, Status: "succeeded",
		},
	}
	management, err := NewManagementUseCase(store, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	if _, err := management.ExecuteRemovalForAction(context.Background(), "preview", "purge-key", "", "non_purge"); err == nil {
		t.Fatal("ExecuteRemovalForAction() accepted a purge preview through the non-purge endpoint")
	}
}

// TestManagementInputBoundsRejectOversizedCoordinates verifies search, cursor, source, and idempotency data remain bounded.
// TestManagementInputBoundsRejectOversizedCoordinates 验证搜索、游标、来源和幂等数据始终保持有界。
func TestManagementInputBoundsRejectOversizedCoordinates(t *testing.T) {
	management, err := NewManagementUseCase(&testManagementMutationStore{}, 30, 100)
	if err != nil {
		t.Fatalf("NewManagementUseCase() error = %v", err)
	}
	if _, err := management.ListSessions(context.Background(), ManagementSessionFilters{Query: strings.Repeat("界", ManagementMaxQueryRunes+1)}); err == nil {
		t.Fatal("ListSessions() accepted an oversized query")
	}
	if _, err := management.ListTurns(context.Background(), ManagementTurnFilters{Cursor: strings.Repeat("x", ManagementMaxOpaqueIDBytes+1)}); err == nil {
		t.Fatal("ListTurns() accepted an oversized cursor")
	}
	if _, err := management.CreateRemovalPreview(context.Background(), logicdomain.ManagementRemovalSelection{
		TargetType: logicdomain.ManagementTargetSession, TargetIDs: []uint64{1}, Action: logicdomain.ManagementActionArchive,
		Source: strings.Repeat("s", ManagementMaxSourceBytes+1),
	}); err == nil {
		t.Fatal("CreateRemovalPreview() accepted an oversized source")
	}
	if _, err := management.RestoreRecycleBatch(context.Background(), 1, strings.Repeat("k", ManagementMaxOpaqueIDBytes+1)); err == nil {
		t.Fatal("RestoreRecycleBatch() accepted an oversized idempotency key")
	}
}
