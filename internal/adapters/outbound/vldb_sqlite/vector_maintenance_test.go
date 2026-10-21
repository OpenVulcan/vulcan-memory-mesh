// SQLite adapter tests for vector maintenance test.
// SQLite 适配层向量维护测试，使用同包夹具验证存储行为。
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

// TestStoreReplaceMemoryVectorsUsesExecuteBatch verifies vector_json rebuilds are persisted through one homogeneous ExecuteBatch call.
// TestStoreReplaceMemoryVectorsUsesExecuteBatch 用于验证 vector_json 重建会通过一次同构 ExecuteBatch 持久化。
func TestStoreReplaceMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
		{ID: "vec-2", Vector: []float32{4, 5, 6}},
	})
	if err != nil {
		t.Fatalf("ReplaceMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector rebuild, exec=%d batch=%d", execCalls, batchCalls)
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "[1,2,3]" {
		t.Fatalf("expected vector json string param, got %#v", first[0])
	}
	if first[1].Kind != sqliteffi.SQLValueString || first[1].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[1])
	}
}

// TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift verifies split rebuild cannot report success when a durable vector row was missed.
// TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift 用于验证 split 重建漏掉长期向量行时不能报告成功。
func TestStoreReplaceMemoryVectorsRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 1, StatementsExecuted: 2}, nil
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
		{ID: "vec-2", Vector: []float32{4, 5, 6}},
	})
	if err == nil {
		t.Fatal("expected vector replacement row-count drift to fail")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected partial vector replacement drift to be outcome-uncertain, got %v", err)
	}
	if !strings.Contains(err.Error(), "replace sqlite memory vectors affected 1 rows, want 2") {
		t.Fatalf("unexpected vector replacement drift error: %v", err)
	}
}

// TestStoreReplaceMemoryVectorsCommitOutcomeUncertain verifies split rebuild vector rewrites preserve ambiguous ExecuteBatch commit semantics.
// TestStoreReplaceMemoryVectorsCommitOutcomeUncertain 用于验证 split 重建向量重写会保留 ExecuteBatch 提交结果不明语义。
func TestStoreReplaceMemoryVectorsCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite vector replacement batch")
	}

	err := store.ReplaceMemoryVectors(context.Background(), []logicdomain.MemoryRecord{
		{ID: "vec-1", Vector: []float32{1, 2, 3}},
	})
	if err == nil {
		t.Fatal("expected vector replacement commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector replacement error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "replace sqlite memory vectors") {
		t.Fatalf("unexpected vector replacement error detail: %v", err)
	}
}

// TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry verifies split-mode post-action writes keep explicit analyzer expiry values instead of falling back to the default horizon.
// TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry 用于验证 split 模式 post-action 写入会保留分析器显式过期时间，而不是退回默认保留周期。
func TestNormalizeTurnMemoryNodeRecordPreservesCandidateExpiry(t *testing.T) {
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	expiresAt := now.Add(2 * time.Hour)
	record := normalizeTurnMemoryNodeRecord(
		logicdomain.SessionRef{TeamID: 1, SpaceID: 2, ProjectID: 3, UserID: 4, SessionID: 5},
		logicdomain.PersistedTurnRecord{ID: 6, CreatedAt: now},
		logicdomain.MemoryNodeCandidate{
			VectorID:      "vec-explicit",
			Vector:        []float32{0.1, 0.2},
			Category:      logicdomain.MemoryNodeCategoryProjectContext,
			Abstract:      "显式短期记忆",
			ScopeLevel:    logicdomain.MemoryScopeLevelSession,
			RefreshWeight: 1,
			ExpiresAt:     expiresAt,
		},
		7,
		now,
	)
	if !record.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires_at = %v, want %v", record.ExpiresAt, expiresAt)
	}
}

// TestStoreClearMemoryVectorsUsesExecuteBatch verifies vector resets clear vector_json through one homogeneous ExecuteBatch call.
// TestStoreClearMemoryVectorsUsesExecuteBatch 用于验证向量重置会通过一次同构 ExecuteBatch 清空 vector_json。
func TestStoreClearMemoryVectorsUsesExecuteBatch(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	execCalls := 0
	batchCalls := 0
	var capturedBatch *fakeExecuteBatchRequest
	fake.executeScriptFunc = func(_ context.Context, _ *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		execCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(_ context.Context, req *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		capturedBatch = req
		return &fakeExecuteResponse{Success: true, RowsChanged: int64(len(req.GetItems())), StatementsExecuted: int64(len(req.GetItems()))}, nil
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1", "vec-2"})
	if err != nil {
		t.Fatalf("ClearMemoryVectors returned error: %v", err)
	}
	if execCalls != 0 || batchCalls != 1 {
		t.Fatalf("expected batch-only vector clear, exec=%d batch=%d", execCalls, batchCalls)
	}
	if !strings.Contains(capturedBatch.GetSql(), "SET vector_json = '[]'") {
		t.Fatalf("expected vector reset sql, got %q", capturedBatch.GetSql())
	}
	first := capturedBatch.GetItems()[0].GetParams()
	if first[0].Kind != sqliteffi.SQLValueString || first[0].String != "vec-1" {
		t.Fatalf("expected vector id string param, got %#v", first[0])
	}
}

// TestStoreClearMemoryVectorsRejectsRowsChangedDrift verifies split reset fails when durable vector rows are not all cleared.
// TestStoreClearMemoryVectorsRejectsRowsChangedDrift 用于验证 split reset 未清空全部长期向量行时会失败。
func TestStoreClearMemoryVectorsRejectsRowsChangedDrift(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(_ context.Context, _ *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true, RowsChanged: 0, StatementsExecuted: 2}, nil
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1", "vec-2"})
	if err == nil {
		t.Fatal("expected vector clear row-count drift to fail")
	}
	if logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected zero-row vector clear drift to remain a clean failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "clear sqlite memory vectors affected 0 rows, want 2") {
		t.Fatalf("unexpected vector clear drift error: %v", err)
	}
}

// TestStoreClearMemoryVectorsCommitOutcomeUncertain verifies split reset vector clears preserve ambiguous ExecuteBatch commit semantics.
// TestStoreClearMemoryVectorsCommitOutcomeUncertain 用于验证 split reset 向量清空会保留 ExecuteBatch 提交结果不明语义。
func TestStoreClearMemoryVectorsCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while clearing sqlite vectors")
	}

	err := store.ClearMemoryVectors(context.Background(), []string{"vec-1"})
	if err == nil {
		t.Fatal("expected vector clear commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain vector clear error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "clear sqlite memory vectors") {
		t.Fatalf("unexpected vector clear error detail: %v", err)
	}
}
