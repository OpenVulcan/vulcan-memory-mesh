package ports

import (
	"context"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

type LLMClient interface {
	ExtractIntent(ctx context.Context, history []domain.HistorySnippet, currentContent string) (string, error)
}

type EmbeddingClient interface {
	EmbedText(ctx context.Context, text string) ([]float32, error)
}

type VectorStore interface {
	Upsert(ctx context.Context, record domain.MemoryRecord) error
	Search(ctx context.Context, vector []float32, topK int, filter domain.SearchFilter) ([]domain.MemoryHit, error)
	Shutdowner
}

type RelationalStore interface {
	UpsertChatLogs(ctx context.Context, session domain.SessionRef, turns []domain.NormalizedTurn) error
	RefreshSession(ctx context.Context, session domain.SessionRef) error
	Shutdowner
}

type ContextPersonaProvider interface {
	Load(ctx context.Context, session domain.SessionRef) (domain.PersonaContext, error)
}

type IDGenerator interface {
	NewID(prefix string) string
}
