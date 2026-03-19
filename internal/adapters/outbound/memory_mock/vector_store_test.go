package memory_mock

import (
	"context"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

func TestVectorStoreSearchOrdersBySimilarity(t *testing.T) {
	embed := NewEmbeddingClient(16)
	store := NewVectorStore()
	q1, _ := embed.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"fastapi backend"}, Dimension: 16})
	q2, _ := embed.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"sqlite database"}, Dimension: 16})
	if err := store.Upsert(context.Background(), logicdomain.MemoryRecord{ID: "m1", Text: "fastapi backend", Vector: q1.Vectors[0], Filter: logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), logicdomain.MemoryRecord{ID: "m2", Text: "sqlite database", Vector: q2.Vectors[0], Filter: logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"}}); err != nil {
		t.Fatal(err)
	}
	query, _ := embed.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"fastapi service"}, Dimension: 16})
	hits, err := store.Search(context.Background(), query.Vectors[0], 2, logicdomain.SearchFilter{UserID: "u1", ProjectID: "p1"})
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
