// store_test.go implements the SQLite-backed outbound archive adapter tests.
// store_test.go 用于实现基于 SQLite 的出站归档适配器测试。
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestNewStoreCreatesSchemaAndPersistsMemories verifies the TestNewStoreCreatesSchemaAndPersistsMemories behavior.
// TestNewStoreCreatesSchemaAndPersistsMemories 用于验证 TestNewStoreCreatesSchemaAndPersistsMemories 行为。
func TestNewStoreCreatesSchemaAndPersistsMemories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vmm.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	record := logicdomain.ArchivedMemory{
		ID:        "arc_001",
		SessionID: "sess_001",
		Content:   "[MOBILE_MASKED]",
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveMemory(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM vmm_memories WHERE session_id = ? AND content = ?`, "sess_001", "[MOBILE_MASKED]").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored row count = %d", count)
	}
}

// TestNoiseEmbeddingCacheReplaceAndLoad verifies semantic prototype vectors can be persisted and reused by fingerprint.
// TestNoiseEmbeddingCacheReplaceAndLoad 用于验证语义原型向量可以按指纹持久化并被后续复用。
func TestNoiseEmbeddingCacheReplaceAndLoad(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vmm.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	query := logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise_gate",
		Language:  "zh-CN",
		Model:     "text-embedding-v4",
		Dimension: 1024,
		RulesHash: "hash-a",
	}
	entries := []logicdomain.NoiseEmbeddingCacheEntry{
		{
			Scope:        query.Scope,
			Language:     query.Language,
			CategoryName: "meta_question",
			Phrase:       "你还记得吗",
			Model:        query.Model,
			Dimension:    query.Dimension,
			RulesHash:    query.RulesHash,
			Vector:       []float32{0.6, 0.8},
			UpdatedAt:    time.Now().UTC(),
		},
	}
	if err := store.ReplaceNoiseEmbeddingCache(context.Background(), query, entries); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadNoiseEmbeddingCache(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded cache rows = %d", len(loaded))
	}
	if loaded[0].CategoryName != "meta_question" || loaded[0].Phrase != "你还记得吗" {
		t.Fatalf("unexpected loaded entry = %+v", loaded[0])
	}
	if len(loaded[0].Vector) != 2 || loaded[0].Vector[0] != 0.6 || loaded[0].Vector[1] != 0.8 {
		t.Fatalf("loaded vector = %#v", loaded[0].Vector)
	}
}

// TestNoiseEmbeddingCacheReplaceClearsStaleLanguageRows verifies stale rows for one language bundle are pruned before refresh.
// TestNoiseEmbeddingCacheReplaceClearsStaleLanguageRows 用于验证刷新同一语言包缓存前会清理陈旧记录。
func TestNoiseEmbeddingCacheReplaceClearsStaleLanguageRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vmm.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	oldQuery := logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise_gate",
		Language:  "zh-CN",
		Model:     "model-a",
		Dimension: 1024,
		RulesHash: "old",
	}
	newQuery := logicdomain.NoiseEmbeddingCacheQuery{
		Scope:     "noise_gate",
		Language:  "zh-CN",
		Model:     "model-b",
		Dimension: 1536,
		RulesHash: "new",
	}
	if err := store.ReplaceNoiseEmbeddingCache(context.Background(), oldQuery, []logicdomain.NoiseEmbeddingCacheEntry{{
		Scope:        oldQuery.Scope,
		Language:     oldQuery.Language,
		CategoryName: "meta_question",
		Phrase:       "旧规则",
		Model:        oldQuery.Model,
		Dimension:    oldQuery.Dimension,
		RulesHash:    oldQuery.RulesHash,
		Vector:       []float32{1, 0},
		UpdatedAt:    time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceNoiseEmbeddingCache(context.Background(), newQuery, []logicdomain.NoiseEmbeddingCacheEntry{{
		Scope:        newQuery.Scope,
		Language:     newQuery.Language,
		CategoryName: "meta_question",
		Phrase:       "新规则",
		Model:        newQuery.Model,
		Dimension:    newQuery.Dimension,
		RulesHash:    newQuery.RulesHash,
		Vector:       []float32{0, 1},
		UpdatedAt:    time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	oldLoaded, err := store.LoadNoiseEmbeddingCache(context.Background(), oldQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldLoaded) != 0 {
		t.Fatalf("expected stale rows to be removed, got %d", len(oldLoaded))
	}
	newLoaded, err := store.LoadNoiseEmbeddingCache(context.Background(), newQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(newLoaded) != 1 || newLoaded[0].Phrase != "新规则" {
		t.Fatalf("unexpected refreshed rows = %#v", newLoaded)
	}
}
