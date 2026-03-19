package usecase

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

type PreCheckCommand struct {
	SessionID, UserID, TeamID, SpaceID, ProjectID, CurrentContent string
	HistoryContent                                                []logicdomain.HistorySnippet
	IsFirstTurn                                                   bool
}
type PreCheckResult struct {
	ShouldInject bool
	ContextText  string
	ContextItems []logicdomain.ContextItem
	Degraded     bool
	TraceID      string
}
type PreCheckExecutor interface {
	Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error)
}

type PreCheckUseCase struct {
	intentExtractor   *processor.IntentExtractor
	contextAssembler  *processor.ContextAssembler
	embedding         appports.EmbeddingClient
	vector            appports.VectorStore
	persona           appports.ContextPersonaProvider
	logger            *log.Logger
	intentTimeout     time.Duration
	topK              int
	maxSearchKeywords int
	minSimilarity     *float64
	embedModel        string
	embedDimension    int
}

func NewPreCheckUseCase(intentExtractor *processor.IntentExtractor, contextAssembler *processor.ContextAssembler, embedding appports.EmbeddingClient, vector appports.VectorStore, persona appports.ContextPersonaProvider, logger *log.Logger, intentTimeout time.Duration, topK, maxSearchKeywords int, minSimilarity *float64, embedModel string, embedDimension int) *PreCheckUseCase {
	if logger == nil {
		logger = log.Default()
	}
	if intentTimeout <= 0 {
		intentTimeout = 2 * time.Second
	}
	if topK <= 0 {
		topK = 5
	}
	if maxSearchKeywords <= 0 {
		maxSearchKeywords = 5
	}
	if maxSearchKeywords > 10 {
		maxSearchKeywords = 10
	}
	resolvedMinSimilarity := 0.75
	if minSimilarity != nil {
		resolvedMinSimilarity = *minSimilarity
	}
	return &PreCheckUseCase{intentExtractor: intentExtractor, contextAssembler: contextAssembler, embedding: embedding, vector: vector, persona: persona, logger: logger, intentTimeout: intentTimeout, topK: topK, maxSearchKeywords: maxSearchKeywords, minSimilarity: &resolvedMinSimilarity, embedModel: embedModel, embedDimension: embedDimension}
}

func (u *PreCheckUseCase) Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error) {
	if err := validatePreCheck(cmd); err != nil {
		return PreCheckResult{}, err
	}
	traceID := trace.IDFromContext(ctx)
	question := strings.TrimSpace(cmd.CurrentContent)
	if question == "" {
		return PreCheckResult{ShouldInject: false, ContextText: "", ContextItems: []logicdomain.ContextItem{}, Degraded: false, TraceID: traceID}, nil
	}
	session := logicdomain.SessionRef{SessionID: cmd.SessionID, UserID: cmd.UserID, TeamID: cmd.TeamID, SpaceID: cmd.SpaceID, ProjectID: cmd.ProjectID}
	recentHistory := recentDialogue(cmd.HistoryContent, 2)
	var personaCtx logicdomain.PersonaContext
	var intentRes logicdomain.IntentResult
	var intentErr error
	degraded := false
	var mu sync.Mutex
	extractIntent := func() {
		llmCtx, cancel := context.WithTimeout(ctx, u.intentTimeout)
		defer cancel()
		res, err := u.intentExtractor.Extract(llmCtx, recentHistory, question)
		mu.Lock()
		defer mu.Unlock()
		intentRes = res
		intentErr = err
	}
	loadPersona := func() {
		if u.persona == nil {
			return
		}
		res, err := u.persona.Load(ctx, session)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			degraded = true
			u.logger.Printf("trace_id=%s persona degraded: %v", traceID, err)
			return
		}
		personaCtx = res
	}
	if cmd.IsFirstTurn {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); extractIntent() }()
		go func() { defer wg.Done(); loadPersona() }()
		wg.Wait()
	} else {
		extractIntent()
	}
	searchTerms, needMemory, fallbackDegraded := u.resolveSearchTerms(question, intentRes, intentErr)
	degraded = degraded || fallbackDegraded
	var hits []logicdomain.MemoryHit
	if needMemory && len(searchTerms) > 0 {
		var err error
		hits, err = u.retrieveRelevantMemories(ctx, session.SearchFilter(), searchTerms)
		if err != nil {
			return PreCheckResult{}, err
		}
	}
	contextText, items, err := u.contextAssembler.Assemble(ctx, personaCtx, hits)
	if err != nil {
		return PreCheckResult{}, err
	}
	return PreCheckResult{ShouldInject: strings.TrimSpace(contextText) != "", ContextText: contextText, ContextItems: items, Degraded: degraded, TraceID: traceID}, nil
}

func validatePreCheck(cmd PreCheckCommand) error {
	if strings.TrimSpace(cmd.SessionID) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.UserID) == "" {
		return logicdomain.ValidationError{Field: "user_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.TeamID) == "" {
		return logicdomain.ValidationError{Field: "team_id", Message: "is required"}
	}
	if strings.TrimSpace(cmd.ProjectID) == "" {
		return logicdomain.ValidationError{Field: "project_id", Message: "is required"}
	}
	return nil
}

func (u *PreCheckUseCase) resolveSearchTerms(question string, intent logicdomain.IntentResult, err error) ([]string, bool, bool) {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			u.logger.Printf("llm timeout fallback to raw question")
		} else {
			u.logger.Printf("llm degraded: %v", err)
		}
		return []string{question}, true, true
	}
	if !intent.NeedMemory {
		return nil, false, false
	}
	keywords := make([]string, 0, len(intent.Keywords))
	seen := map[string]struct{}{}
	for _, kw := range intent.Keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if _, ok := seen[kw]; ok {
			continue
		}
		seen[kw] = struct{}{}
		keywords = append(keywords, kw)
	}
	if len(keywords) > u.maxSearchKeywords {
		keywords = keywords[:u.maxSearchKeywords]
	}
	if len(keywords) == 0 {
		return []string{question}, true, true
	}
	return keywords, true, false
}

func (u *PreCheckUseCase) retrieveRelevantMemories(ctx context.Context, filter logicdomain.SearchFilter, keywords []string) ([]logicdomain.MemoryHit, error) {
	if len(keywords) > u.maxSearchKeywords {
		keywords = keywords[:u.maxSearchKeywords]
	}
	embedded, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Model: u.embedModel, Texts: keywords, Dimension: u.embedDimension})
	if err != nil {
		return nil, fmt.Errorf("embed search keywords: %w", err)
	}
	merged := map[string]logicdomain.MemoryHit{}
	for _, vector := range embedded.Vectors {
		hits, err := u.vector.Search(ctx, vector, u.topK, filter)
		if err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		for _, hit := range hits {
			if u.minSimilarity != nil && hit.Score < *u.minSimilarity {
				continue
			}
			if existing, ok := merged[hit.ID]; !ok || hit.Score > existing.Score {
				merged[hit.ID] = hit
			}
		}
	}
	out := make([]logicdomain.MemoryHit, 0, len(merged))
	for _, hit := range merged {
		out = append(out, hit)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if u.topK > 0 && len(out) > u.topK {
		out = out[:u.topK]
	}
	if len(out) == 0 {
		return []logicdomain.MemoryHit{}, nil
	}
	return out, nil
}

func recentDialogue(history []logicdomain.HistorySnippet, rounds int) []logicdomain.HistorySnippet {
	valid := make([]logicdomain.HistorySnippet, 0, len(history))
	for _, item := range history {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		text := strings.TrimSpace(item.Content)
		if text == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			continue
		}
		valid = append(valid, logicdomain.HistorySnippet{Role: role, Content: text})
	}
	if len(valid) == 0 || rounds <= 0 {
		return nil
	}
	picked := make([]logicdomain.HistorySnippet, 0, rounds*2)
	users := 0
	for i := len(valid) - 1; i >= 0; i-- {
		picked = append(picked, valid[i])
		if valid[i].Role == "user" {
			users++
			if users >= rounds {
				break
			}
		}
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	return picked
}
