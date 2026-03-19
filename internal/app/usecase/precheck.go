// precheck.go implements application use cases.
// precheck.go 用于实现应用用例层。
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

// PreCheckCommand carries normalized pre-check inputs from the HTTP adapter into the orchestration flow.
// PreCheckCommand 用于承载从 HTTP 适配层进入编排流程的标准化 pre-check 输入。
type PreCheckCommand struct {
	SessionID, UserID, TeamID, SpaceID, ProjectID, CurrentContent string
	HistoryContent                                                []logicdomain.HistorySnippet
	IsFirstTurn                                                   bool
}

// PreCheckResult returns the assembled injection context and execution state back to the HTTP adapter.
// PreCheckResult 用于把组装后的注入上下文和执行状态返回给 HTTP 适配层。
type PreCheckResult struct {
	ShouldInject bool
	ContextText  string
	ContextItems []logicdomain.ContextItem
	Degraded     bool
	TraceID      string
}

// PreCheckExecutor is the interface consumed by the HTTP adapter to trigger the pre-check workflow.
// PreCheckExecutor 用于让 HTTP 适配层触发 pre-check 工作流。
type PreCheckExecutor interface {
	Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error)
}

// PreCheckUseCase orchestrates intent extraction, persona loading, recall, and context assembly.
// PreCheckUseCase 用于编排意图提取、画像加载、记忆召回和上下文组装。
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

// NewPreCheckUseCase creates a PreCheckUseCase instance.
// NewPreCheckUseCase 用于创建 PreCheckUseCase 实例。
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

// Execute executes the Execute logic.
// Execute 用于执行 Execute 逻辑。
func (u *PreCheckUseCase) Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error) {
	// Validate the request and short-circuit empty questions as early as possible.
	// 尽早完成请求校验，并对空问题做快速返回。
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

	// Prepare the intent extraction and persona loading branches.
	// 预先定义意图提取和画像加载两个分支。
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

	// Run both branches concurrently on the first turn, otherwise only extract intent.
	// 首轮对话并发执行两个分支，否则只执行意图提取。
	if cmd.IsFirstTurn {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); extractIntent() }()
		go func() { defer wg.Done(); loadPersona() }()
		wg.Wait()
	} else {
		extractIntent()
	}

	// Resolve search terms, retrieve memories, and assemble the final injected context.
	// 解析检索词、召回记忆，并组装最终注入上下文。
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

// validatePreCheck validates the input value.
// validatePreCheck 用于校验输入值。
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

// resolveSearchTerms resolves the target value.
// resolveSearchTerms 用于解析目标值。
func (u *PreCheckUseCase) resolveSearchTerms(question string, intent logicdomain.IntentResult, err error) ([]string, bool, bool) {
	// Fall back to the raw question whenever intent extraction times out or returns invalid output.
	// 当意图提取超时或输出异常时，回退为直接使用原始问题。
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

	// Deduplicate and clamp extracted keywords before they reach the embedding layer.
	// 在进入 embedding 层之前，对提取出的关键词做去重和截断。
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

// retrieveRelevantMemories executes the retrieveRelevantMemories logic.
// retrieveRelevantMemories 用于执行 retrieveRelevantMemories 逻辑。
func (u *PreCheckUseCase) retrieveRelevantMemories(ctx context.Context, filter logicdomain.SearchFilter, keywords []string) ([]logicdomain.MemoryHit, error) {
	// Embed all search terms first so each term can query the vector store independently.
	// 先对所有检索词做 embedding，再逐个查询向量库。
	if len(keywords) > u.maxSearchKeywords {
		keywords = keywords[:u.maxSearchKeywords]
	}
	embedded, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Model: u.embedModel, Texts: keywords, Dimension: u.embedDimension})
	if err != nil {
		return nil, fmt.Errorf("embed search keywords: %w", err)
	}
	merged := map[string]logicdomain.MemoryHit{}

	// Merge multi-keyword recalls and keep only the strongest hit per memory record.
	// 合并多关键词召回结果，并为每条记忆保留最高分命中。
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

	// Sort the final recall set and trim it down to the configured top-k window.
	// 对最终召回集排序，并裁剪到配置的 top-k 窗口内。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if u.topK > 0 && len(out) > u.topK {
		out = out[:u.topK]
	}
	if len(out) == 0 {
		return []logicdomain.MemoryHit{}, nil
	}
	return out, nil
}

// recentDialogue executes the recentDialogue logic.
// recentDialogue 用于执行 recentDialogue 逻辑。
func recentDialogue(history []logicdomain.HistorySnippet, rounds int) []logicdomain.HistorySnippet {
	// Keep only valid user/assistant text messages before slicing recent turns.
	// 在裁剪最近轮次之前，只保留合法的 user/assistant 文本消息。
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

	// Walk backward by user turns and then restore chronological order.
	// 按用户轮次倒序回溯，再恢复为正向时间顺序。
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
