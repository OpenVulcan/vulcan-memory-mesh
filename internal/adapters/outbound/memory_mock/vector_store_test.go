// vector_store_test.go implements the in-memory mock outbound adapters.
// vector_store_test.go 用于实现内存版 mock 出站适配器。
package memory_mock

import (
	"context"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestVectorStoreSearchOrdersBySimilarity verifies the TestVectorStoreSearchOrdersBySimilarity behavior.
// TestVectorStoreSearchOrdersBySimilarity 用于验证 TestVectorStoreSearchOrdersBySimilarity 行为。
func TestVectorStoreSearchOrdersBySimilarity(t *testing.T) {
	store := NewVectorStore()
	fastAPI := normalizeVector([]float32{4, 1, 0})
	database := normalizeVector([]float32{0, 1, 4})
	query := normalizeVector([]float32{5, 1, 0})
	if err := store.Upsert(context.Background(), logicdomain.MemoryRecord{ID: "m1", Text: "fastapi backend", Vector: fastAPI, Filter: logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), logicdomain.MemoryRecord{ID: "m2", Text: "database backend", Vector: database, Filter: logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search(context.Background(), query, 2, logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].ID != "m1" {
		t.Fatalf("expected m1 first, got %s", hits[0].ID)
	}
}
