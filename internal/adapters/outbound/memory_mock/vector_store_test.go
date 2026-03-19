package memory_mock

import (
	"context"
	"testing"

	"github.com/openvulcan/vmm/internal/core/domain"
)

func TestVectorStoreFilterIsolation(t *testing.T) {
	embed := NewEmbeddingClient(64)
	store := NewVectorStore()
	vec1, _ := embed.EmbedText(context.Background(), "fastapi backend")
	vec2, _ := embed.EmbedText(context.Background(), "spring backend")
	_ = store.Upsert(context.Background(), domain.MemoryRecord{ID:"m1", Text:"要求使用 FastAPI", Vector:vec1, Filter:domain.SearchFilter{UserID:"u1", ProjectID:"p1"}})
	_ = store.Upsert(context.Background(), domain.MemoryRecord{ID:"m2", Text:"要求使用 Spring", Vector:vec2, Filter:domain.SearchFilter{UserID:"u2", ProjectID:"p1"}})

	qv, _ := embed.EmbedText(context.Background(), "fastapi framework")
	hits, err := store.Search(context.Background(), qv, 5, domain.SearchFilter{UserID:"u1", ProjectID:"p1"})
	if err != nil { t.Fatal(err) }
	if len(hits) == 0 || hits[0].ID != "m1" { t.Fatalf("expected hit m1, got %+v", hits) }
	for _, hit := range hits {
		if hit.Filter.UserID == "u2" {
			t.Fatalf("cross-user leak detected")
		}
	}
}
