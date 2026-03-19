package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/core/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type PreCheckService struct {
	llm                 ports.LLMClient
	embedding           ports.EmbeddingClient
	vector              ports.VectorStore
	persona             ports.ContextPersonaProvider
	logger              *log.Logger
	intentTimeout       time.Duration
	topK                int
	similarityThreshold float64
}

func NewPreCheckService(llm ports.LLMClient, embedding ports.EmbeddingClient, vector ports.VectorStore, persona ports.ContextPersonaProvider, logger *log.Logger, intentTimeout time.Duration, topK int, similarityThreshold float64) *PreCheckService {
	if logger == nil {
		logger = log.Default()
	}
	if intentTimeout <= 0 {
		intentTimeout = 2 * time.Second
	}
	if topK <= 0 {
		topK = 5
	}
	return &PreCheckService{llm: llm, embedding: embedding, vector: vector, persona: persona, logger: logger, intentTimeout: intentTimeout, topK: topK, similarityThreshold: similarityThreshold}
}

func (s *PreCheckService) Execute(ctx context.Context, req domain.PreCheckRequest) (domain.PreCheckResponse, error) {
	if err := s.validate(req); err != nil {
		return domain.PreCheckResponse{}, err
	}
	question := strings.TrimSpace(req.CurrentContent)
	traceID := trace.IDFromContext(ctx)
	if question == "" {
		return domain.PreCheckResponse{ShouldInject: false, ContextText: "", ContextItems: []domain.ContextItem{}, Degraded: false, TraceID: traceID}, nil
	}

	var (
		personaCtx domain.PersonaContext
		hits       []domain.MemoryHit
		degraded   bool
		firstErr   error
		mu         sync.Mutex
	)

	semanticFn := func() {
		localHits, localDegraded, err := s.retrieveRelevantMemories(ctx, req)
		mu.Lock()
		defer mu.Unlock()
		if err != nil && firstErr == nil {
			firstErr = err
			return
		}
		hits = localHits
		degraded = degraded || localDegraded
	}
	personaFn := func() {
		if s.persona == nil {
			return
		}
		localPersona, err := s.persona.Load(ctx, req.SessionRef())
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			degraded = true
			s.logger.Printf("trace_id=%s persona degraded: %v", traceID, err)
			return
		}
		personaCtx = localPersona
	}

	if req.IsFirstTurn {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); personaFn() }()
		go func() { defer wg.Done(); semanticFn() }()
		wg.Wait()
	} else {
		semanticFn()
	}
	if firstErr != nil {
		return domain.PreCheckResponse{}, firstErr
	}

	items := append(personaToItems(personaCtx), memoryHitsToItems(hits)...)
	injected := buildInjectedContext(items, degraded)
	injected.TraceID = traceID
	return injected, nil
}

func (s *PreCheckService) validate(req domain.PreCheckRequest) error {
	if strings.TrimSpace(req.SessionID) == "" { return domain.ValidationError{Field: "session_id", Message: "is required"} }
	if strings.TrimSpace(req.UserID) == "" { return domain.ValidationError{Field: "user_id", Message: "is required"} }
	if strings.TrimSpace(req.TeamID) == "" { return domain.ValidationError{Field: "team_id", Message: "is required"} }
	if strings.TrimSpace(req.ProjectID) == "" { return domain.ValidationError{Field: "project_id", Message: "is required"} }
	return nil
}

func (s *PreCheckService) retrieveRelevantMemories(ctx context.Context, req domain.PreCheckRequest) ([]domain.MemoryHit, bool, error) {
	intent, degraded, err := s.extractIntentWithFallback(ctx, recentDialogue(req.HistoryContent, 2), req.CurrentContent)
	if err != nil {
		return nil, degraded, err
	}
	vec, err := s.embedding.EmbedText(ctx, intent)
	if err != nil {
		return nil, degraded, fmt.Errorf("embed text: %w", err)
	}
	hits, err := s.vector.Search(ctx, vec, s.topK, req.SessionRef().SearchFilter())
	if err != nil {
		return nil, degraded, fmt.Errorf("vector search: %w", err)
	}
	return filterHits(hits, s.similarityThreshold, s.topK), degraded, nil
}

func (s *PreCheckService) extractIntentWithFallback(ctx context.Context, history []domain.HistorySnippet, current string) (string, bool, error) {
	raw := strings.TrimSpace(current)
	if raw == "" { return "", false, nil }
	if s.llm == nil { return raw, true, nil }

	llmCtx, cancel := context.WithTimeout(ctx, s.intentTimeout)
	defer cancel()
	out, err := s.llm.ExtractIntent(llmCtx, history, raw)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(llmCtx.Err(), context.DeadlineExceeded) {
			s.logger.Printf("trace_id=%s llm timeout fallback", trace.IDFromContext(ctx))
			return raw, true, nil
		}
		s.logger.Printf("trace_id=%s llm degraded: %v", trace.IDFromContext(ctx), err)
		return raw, true, nil
	}
	if strings.TrimSpace(out) == "" {
		return raw, true, nil
	}
	return strings.TrimSpace(out), false, nil
}

func recentDialogue(history []domain.HistorySnippet, rounds int) []domain.HistorySnippet {
	valid := make([]domain.HistorySnippet, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		text := strings.TrimSpace(item.Content)
		if text == "" { continue }
		if role != "user" && role != "assistant" { continue }
		valid = append(valid, domain.HistorySnippet{Role: role, Content: text})
	}
	if len(valid) == 0 || rounds <= 0 { return nil }
	picked := make([]domain.HistorySnippet, 0, rounds*2)
	users := 0
	for i := len(valid) - 1; i >= 0; i-- {
		picked = append(picked, valid[i])
		if valid[i].Role == "user" {
			users++
			if users >= rounds { break }
		}
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 { picked[i], picked[j] = picked[j], picked[i] }
	return picked
}

func filterHits(hits []domain.MemoryHit, threshold float64, topK int) []domain.MemoryHit {
	if len(hits) == 0 { return []domain.MemoryHit{} }
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	out := make([]domain.MemoryHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Score < threshold { continue }
		out = append(out, hit)
		if topK > 0 && len(out) >= topK { break }
	}
	return out
}

func personaToItems(persona domain.PersonaContext) []domain.ContextItem {
	items := make([]domain.ContextItem, 0, len(persona.ProjectConstraints)+len(persona.Profile)+len(persona.Preferences))
	for _, text := range persona.ProjectConstraints {
		if strings.TrimSpace(text) != "" { items = append(items, domain.ContextItem{Kind: "project_constraint", Title: "项目约束", Text: strings.TrimSpace(text), Source: "persona"}) }
	}
	for _, text := range persona.Profile {
		if strings.TrimSpace(text) != "" { items = append(items, domain.ContextItem{Kind: "persona", Title: "个人画像", Text: strings.TrimSpace(text), Source: "persona"}) }
	}
	for _, text := range persona.Preferences {
		if strings.TrimSpace(text) != "" { items = append(items, domain.ContextItem{Kind: "preference", Title: "偏好习惯", Text: strings.TrimSpace(text), Source: "persona"}) }
	}
	return items
}

func memoryHitsToItems(hits []domain.MemoryHit) []domain.ContextItem {
	items := make([]domain.ContextItem, 0, len(hits))
	for _, hit := range hits {
		items = append(items, domain.ContextItem{Kind: "memory", Title: "向量召回记忆", Text: strings.TrimSpace(hit.Text), Source: "vector", Score: hit.Score})
	}
	return items
}

func buildInjectedContext(items []domain.ContextItem, degraded bool) domain.PreCheckResponse {
	if len(items) == 0 {
		return domain.PreCheckResponse{ShouldInject: false, ContextText: "", ContextItems: []domain.ContextItem{}, Degraded: degraded}
	}
	order := []struct{ Kind, Title string }{
		{"project_constraint", "项目约束"},
		{"persona", "个人画像"},
		{"preference", "偏好习惯"},
		{"memory", "向量召回记忆"},
	}
	var b strings.Builder
	first := true
	for _, group := range order {
		section := make([]domain.ContextItem, 0)
		for _, item := range items { if item.Kind == group.Kind { section = append(section, item) } }
		if len(section) == 0 { continue }
		if !first { b.WriteString("\n\n") }
		first = false
		b.WriteString("[")
		b.WriteString(group.Title)
		b.WriteString("]\n")
		for i, item := range section {
			if item.Kind == "memory" {
				b.WriteString(fmt.Sprintf("%d. %s (score=%.3f)\n", i+1, item.Text, item.Score))
			} else {
				b.WriteString("- ")
				b.WriteString(item.Text)
				b.WriteString("\n")
			}
		}
		text := strings.TrimRight(b.String(), "\n")
		b.Reset()
		b.WriteString(text)
	}
	return domain.PreCheckResponse{ShouldInject: true, ContextText: strings.TrimSpace(b.String()), ContextItems: items, Degraded: degraded}
}
