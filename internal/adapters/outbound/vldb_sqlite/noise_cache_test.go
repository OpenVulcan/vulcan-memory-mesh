// SQLite adapter tests for noise cache test.
// SQLite 适配层语义噪声缓存测试，使用同包夹具验证存储行为。
package vldb_sqlite

import (
	"context"
	"errors"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"testing"
	"time"
)

// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch verifies repeated prototype inserts are merged into one ExecuteBatch call.
// TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch 用于验证重复原型写入会合并成一次 ExecuteBatch 调用。
func TestStoreReplaceNoiseEmbeddingCacheUsesExecuteBatch(t *testing.T) {
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

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "hello",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{1, 2, 3},
			UpdatedAt:    time.Unix(0, 0).UTC(),
		},
		{
			Scope:        "noise",
			Language:     "zh-CN",
			CategoryName: "chat",
			Phrase:       "world",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "rules",
			Vector:       []float32{4, 5, 6},
			UpdatedAt:    time.Unix(1, 0).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("ReplaceNoiseEmbeddingCache returned error: %v", err)
	}
	if execCalls != 1 || batchCalls != 1 {
		t.Fatalf("expected delete+batch write, exec=%d batch=%d", execCalls, batchCalls)
	}
	if capturedBatch == nil || len(capturedBatch.GetItems()) != 2 {
		t.Fatalf("unexpected batch capture: %#v", capturedBatch)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain verifies cache delete failures keep commit-boundary uncertainty visible while callers can still degrade.
// TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain 用于验证缓存删除失败会保留提交边界不确定语义，同时调用方仍可降级处理。
func TestStoreReplaceNoiseEmbeddingCacheClearCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	batchCalls := 0
	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("failed to commit sqlite noise cache delete")
	}
	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		batchCalls++
		return &fakeExecuteResponse{Success: true}, nil
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{{Vector: []float32{1}, UpdatedAt: time.Unix(0, 0).UTC()}})
	if err == nil {
		t.Fatal("expected noise cache delete commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain noise cache delete error, got %T %v", err, err)
	}
	if batchCalls != 0 {
		t.Fatalf("expected delete failure to stop before batch insert, got %d batch calls", batchCalls)
	}
}

// TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain verifies cache insert failures after delete keep commit-boundary uncertainty visible.
// TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain 用于验证缓存删除后的插入失败会保留提交边界不确定语义。
func TestStoreReplaceNoiseEmbeddingCacheInsertCommitOutcomeUncertain(t *testing.T) {
	fake := &fakeSQLiteDatabase{}
	store := &Store{database: fake, timeout: time.Second}

	fake.executeScriptFunc = func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error) {
		return &fakeExecuteResponse{Success: true}, nil
	}
	fake.executeBatchFunc = func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error) {
		return nil, errors.New("commit outcome unknown while inserting sqlite noise cache")
	}

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "rules",
	}, []logicdomain.NoiseEmbeddingCacheEntry{{Vector: []float32{1}, UpdatedAt: time.Unix(0, 0).UTC()}})
	if err == nil {
		t.Fatal("expected noise cache insert commit-boundary error")
	}
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected outcome-uncertain noise cache insert error, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "insert noise embedding cache rows") {
		t.Fatalf("unexpected noise cache insert error detail: %v", err)
	}
}
