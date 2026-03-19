package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/ports"
)

type countingPersona struct {
	calls int
	ports.ContextPersonaProvider
}

func (p *countingPersona) Load(ctx context.Context, session domain.SessionRef) (domain.PersonaContext, error) {
	p.calls++
	if p.ContextPersonaProvider == nil {
		return domain.PersonaContext{}, nil
	}
	return p.ContextPersonaProvider.Load(ctx, session)
}

type captureEmbedding struct {
	lastText string
	inner    ports.EmbeddingClient
}

func (c *captureEmbedding) EmbedText(ctx context.Context, text string) ([]float32, error) {
	c.lastText = text
	return c.inner.EmbedText(ctx, text)
}

type captureVector struct {
	lastFilter domain.SearchFilter
	inner      ports.VectorStore
}

func (c *captureVector) Upsert(ctx context.Context, record domain.MemoryRecord) error { return c.inner.Upsert(ctx, record) }
func (c *captureVector) Search(ctx context.Context, vector []float32, topK int, filter domain.SearchFilter) ([]domain.MemoryHit, error) {
	c.lastFilter = filter
	return c.inner.Search(ctx, vector, topK, filter)
}
func (c *captureVector) Shutdown(ctx context.Context) error { return c.inner.Shutdown(ctx) }

func TestPreCheckFirstTurnLoadsPersonaAndSearch(t *testing.T) {
	embed := memory_mock.NewEmbeddingClient(64)
	vecStore := memory_mock.NewVectorStore()
	seedVec, _ := embed.EmbedText(context.Background(), "fastapi backend framework")
	_ = vecStore.Upsert(context.Background(), domain.MemoryRecord{ID:"m1", Text:"要求使用 FastAPI", Vector:seedVec, Filter:domain.SearchFilter{UserID:"u1", ProjectID:"p1"}})

	persona := &countingPersona{ContextPersonaProvider: memory_mock.NewPersonaProvider()}
	service := NewPreCheckService(memory_mock.NewLLMClient(), embed, vecStore, persona, nil, 2*time.Second, 5, 0.1)
	resp, err := service.Execute(context.Background(), domain.PreCheckRequest{
		SessionID:"s1", UserID:"usr_8899", TeamID:"t1", ProjectID:"p1",
		CurrentContent:"我们后端用什么框架？", IsFirstTurn:true,
	})
	if err != nil { t.Fatal(err) }
	if persona.calls != 1 { t.Fatalf("expected persona called once, got %d", persona.calls) }
	if !resp.ShouldInject { t.Fatalf("expected should_inject=true") }
	if len(resp.ContextItems) == 0 { t.Fatalf("expected context items") }
}

func TestPreCheckNonFirstTurnSkipsPersona(t *testing.T) {
	persona := &countingPersona{}
	service := NewPreCheckService(memory_mock.NewLLMClient(), memory_mock.NewEmbeddingClient(64), memory_mock.NewVectorStore(), persona, nil, 2*time.Second, 5, 0.4)
	_, err := service.Execute(context.Background(), domain.PreCheckRequest{
		SessionID:"s1", UserID:"u1", TeamID:"t1", ProjectID:"p1", CurrentContent:"hello", IsFirstTurn:false,
	})
	if err != nil { t.Fatal(err) }
	if persona.calls != 0 { t.Fatalf("expected persona not called, got %d", persona.calls) }
}

func TestPreCheckFallbackToRawQuestionOnLLMError(t *testing.T) {
	llm := memory_mock.NewLLMClient()
	llm.ForceError = errors.New("boom")
	embed := &captureEmbedding{inner: memory_mock.NewEmbeddingClient(64)}
	service := NewPreCheckService(llm, embed, memory_mock.NewVectorStore(), nil, nil, 2*time.Second, 5, 0.4)
	resp, err := service.Execute(context.Background(), domain.PreCheckRequest{
		SessionID:"s1", UserID:"u1", TeamID:"t1", ProjectID:"p1", CurrentContent:"原始问题", IsFirstTurn:false,
	})
	if err != nil { t.Fatal(err) }
	if embed.lastText != "原始问题" { t.Fatalf("expected fallback to raw question, got %s", embed.lastText) }
	if !resp.Degraded { t.Fatalf("expected degraded=true") }
}

func TestPreCheckPassesSearchFilter(t *testing.T) {
	llm := memory_mock.NewLLMClient()
	embed := memory_mock.NewEmbeddingClient(64)
	cvec := &captureVector{inner: memory_mock.NewVectorStore()}
	service := NewPreCheckService(llm, embed, cvec, nil, nil, 2*time.Second, 5, 0.4)
	_, err := service.Execute(context.Background(), domain.PreCheckRequest{
		SessionID:"s1", UserID:"u1", TeamID:"t1", SpaceID:"sp1", ProjectID:"p1", CurrentContent:"test", IsFirstTurn:false,
	})
	if err != nil { t.Fatal(err) }
	if cvec.lastFilter.UserID != "u1" || cvec.lastFilter.ProjectID != "p1" || cvec.lastFilter.SpaceID != "sp1" {
		t.Fatalf("unexpected filter: %+v", cvec.lastFilter)
	}
}
