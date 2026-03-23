// precheck.go implements application use cases.
// precheck.go 用于实现应用用例层。
package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
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

// PreCheckUseCase owns the pre-check entrypoint that currently short-circuits all injections.
// PreCheckUseCase 用于承载当前 pre-check 入口的统一短路逻辑，直接禁止注入。
type PreCheckUseCase struct {
	intentExtractor   *processor.IntentExtractor
	contextAssembler  *processor.ContextAssembler
	embedding         appports.EmbeddingClient
	vector            appports.VectorStore
	persona           appports.ContextPersonaProvider
	logger            *logx.Logger
	intentTimeout     time.Duration
	topK              int
	maxSearchKeywords int
	minSimilarity     *float64
	embedModel        string
	embedDimension    int
}

// NewPreCheckUseCase creates a PreCheckUseCase instance.
// NewPreCheckUseCase 用于创建 PreCheckUseCase 实例。
func NewPreCheckUseCase(intentExtractor *processor.IntentExtractor, contextAssembler *processor.ContextAssembler, embedding appports.EmbeddingClient, vector appports.VectorStore, persona appports.ContextPersonaProvider, logger *logx.Logger, intentTimeout time.Duration, topK, maxSearchKeywords int, minSimilarity *float64, embedModel string, embedDimension int) *PreCheckUseCase {
	if logger == nil {
		logger = logx.Default()
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
	// Keep contract validation intact so malformed requests still fail fast at the use-case boundary.
	// 保留用例边界上的契约校验，确保非法请求仍然能够快速失败。
	if err := validatePreCheck(cmd); err != nil {
		return PreCheckResult{}, err
	}
	traceID := trace.IDFromContext(ctx)

	// Force-disable pre-check injection regardless of the incoming question so plugins always receive
	// a deterministic no-op response while the endpoint stays contract-compatible.
	// 无论传入什么问题都强制关闭 pre-check 注入，让插件始终得到一个稳定的空响应，同时保持接口契约不变。
	if u.logger != nil {
		u.logger.Info("pre-check bypassed", "trace_id", traceID, "session_id", cmd.SessionID)
	}
	return PreCheckResult{
		ShouldInject: false,
		ContextText:  "",
		ContextItems: []logicdomain.ContextItem{},
		Degraded:     false,
		TraceID:      traceID,
	}, nil
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

// resolveSearchTerms keeps the legacy fallback helper available for unit-level behavior checks.
// resolveSearchTerms 用于保留旧的回退辅助逻辑，方便单元级行为验证。
func (u *PreCheckUseCase) resolveSearchTerms(question string, intent logicdomain.IntentResult, err error) ([]string, bool, bool) {
	// Fall back to the raw question whenever intent extraction times out or returns invalid output.
	// 当意图提取超时或输出异常时，回退为直接使用原始问题。
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			u.logger.Warn("llm timeout fallback to raw question")
		} else {
			u.logger.Warn("llm degraded", "err", err)
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

// retrieveRelevantMemories keeps the legacy retrieval helper available for deterministic helper tests.
// retrieveRelevantMemories 用于保留旧的召回辅助逻辑，方便确定性辅助测试。
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
