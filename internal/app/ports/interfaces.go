package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

type EmbeddingRequest struct {
	Model         string
	Texts         []string
	Dimension     int
	ProviderHints map[string]any
}

type EmbeddingResponse struct {
	Vectors [][]float32
}

type EmbeddingClient interface {
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

type VectorStore interface {
	Upsert(ctx context.Context, record logicdomain.MemoryRecord) error
	Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error)
	Shutdowner
}

type RelationalStore interface {
	UpsertChatLogs(ctx context.Context, session logicdomain.SessionRef, turns []logicdomain.NormalizedTurn) error
	RefreshSession(ctx context.Context, session logicdomain.SessionRef) error
	Shutdowner
}

type ContextPersonaProvider interface {
	Load(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error)
}

type IDGenerator interface {
	NewID(prefix string) string
}
