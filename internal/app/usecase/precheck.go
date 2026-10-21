// Pre-check use-case contracts and orchestration for inbound recall requests.
// 应用用例层 pre-check 契约与入站召回请求编排。
package usecase

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"strings"
	"time"
)

const (
	// defaultPreCheckTopK keeps vector recall bounded when callers omit a config override.
	// defaultPreCheckTopK 用于在调用方省略配置覆盖时，为向量召回提供受控默认值。
	defaultPreCheckTopK = 5

	// defaultPreCheckHistoryTurns keeps the first-stage turn window focused on only the most recent conversation context.
	// defaultPreCheckHistoryTurns 用于让第一层最近 turn 窗口只关注最新的一小段对话上下文。
	defaultPreCheckHistoryTurns = 3

	// defaultPreCheckHistoryTokens keeps the first-stage turn window under a bounded token budget.
	// defaultPreCheckHistoryTokens 用于让第一层最近 turn 窗口保持在受控 token 预算内。
	defaultPreCheckHistoryTokens = 6000

	// defaultPreCheckReviewCandidates caps the candidate fan-out sent to the second-stage reviewer so one request stays compact and stable.
	// defaultPreCheckReviewCandidates 用于限制送入第二层评审器的候选扇出，保证单次请求保持紧凑稳定。
	defaultPreCheckReviewCandidates = 12

	// defaultPreCheckSimilarity keeps recall aligned with the repo's default vector similarity floor when config does not override it.
	// defaultPreCheckSimilarity 用于在配置未覆盖时，让召回沿用仓库默认的相似度下限。
	defaultPreCheckSimilarity = 0.75
)

// PreCheckRecallMode describes which recall strategy pre-check should apply before it opens memory recall for the current request.
// PreCheckRecallMode 用于描述 pre-check 在为当前请求开放记忆召回前，应采用哪一种召回策略。
type PreCheckRecallMode int32

const (
	// PreCheckRecallModeUnspecified applies the compact-aware default when the transport omits one recall mode.
	// PreCheckRecallModeUnspecified 用于在传输层省略召回模式时应用 compact-aware 默认策略。
	PreCheckRecallModeUnspecified PreCheckRecallMode = 0

	// PreCheckRecallModeSessionCompact enables the session-level compact boundary so recall reopens only the turn history that may have fallen out of the compressed context.
	// PreCheckRecallModeSessionCompact 用于启用 session 级 compact 边界，让召回只重新开放可能已从压缩上下文中丢失的 turn 历史。
	PreCheckRecallModeSessionCompact PreCheckRecallMode = 1

	// PreCheckRecallModeFull explicitly disables the current-session compact boundary while preserving the resolved search scope and lifecycle filters.
	// PreCheckRecallModeFull 用于显式关闭当前 session 的 compact 边界，同时保留已解析检索作用域与生命周期过滤条件。
	PreCheckRecallModeFull PreCheckRecallMode = 2
)

// PreCheckCommand carries the resolved session scope plus the current user text into the pre-check workflow.
// PreCheckCommand 用于承载已解析的 session 范围和当前用户文本，进入 pre-check 工作流。
type PreCheckCommand struct {
	Session     logicdomain.SessionRef
	UserContent string
	RecallMode  PreCheckRecallMode
}

// PreCheckResult returns the assembled injection context and execution state back to the gRPC adapter.
// PreCheckResult 用于把组装后的注入上下文和执行状态返回给 gRPC 适配层。
type PreCheckResult struct {
	ShouldInject bool
	ContextText  string
	ContextItems []logicdomain.ContextItem
	Degraded     bool
	TraceID      string
}

// PreCheckExecutor is the interface consumed by the gRPC adapter to trigger the pre-check workflow.
// PreCheckExecutor 用于让 gRPC 适配层触发 pre-check 工作流。
type PreCheckExecutor interface {
	Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error)
}

// PreCheckIntentExtractor is the first-stage processor used to decide whether memory is needed and which search sentences should drive recall.
// PreCheckIntentExtractor 用于表示第一层处理器，负责判断是否需要记忆以及该用哪些检索语句驱动召回。
type PreCheckIntentExtractor interface {
	Extract(ctx context.Context, turns []logicdomain.PreCheckTurnContext, current string) (logicdomain.IntentResult, error)
	// ExtractModel returns the configured pre-check first-stage model label so degraded malformed-output logs can identify the exact route/model.
	// ExtractModel 用于返回当前 pre-check 第一层模型标识，便于降级的畸形输出日志定位准确路由/模型。
	ExtractModel() string
}

// PreCheckMemoryReviewer is the second-stage processor used to adopt truly useful numbered memory candidates from the recalled pool.
// PreCheckMemoryReviewer 用于表示第二层处理器，负责从召回池中的编号候选里采纳真正有用的记忆。
type PreCheckMemoryReviewer interface {
	Review(ctx context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error)
	// ReviewModel returns the configured pre-check second-stage model label so degraded malformed-output logs can identify the exact route/model.
	// ReviewModel 用于返回当前 pre-check 第二层模型标识，便于降级的畸形输出日志定位准确路由/模型。
	ReviewModel() string
}

// PreCheckContextAssembler turns adopted memories into the final injection payload returned by the RPC.
// PreCheckContextAssembler 用于把已采纳记忆转换成 RPC 返回的最终注入载荷。
type PreCheckContextAssembler interface {
	Assemble(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error)
}

// PreCheckMemorySearcher resolves grouped vector recall over the unified memory surface used by the gRPC memory APIs.
// PreCheckMemorySearcher 用于在统一记忆接口之上执行分组向量召回，复用 gRPC 记忆查询能力。
type PreCheckMemorySearcher interface {
	Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error)
}

// PreCheckStore loads recent mixed session turns and writes lifecycle updates for adopted memory rows.
// PreCheckStore 用于加载最近的混合 session turn，并为被采纳的记忆行写回生命周期更新。
type PreCheckStore interface {
	LoadRecentSessionTurns(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error)
	ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) ([]logicdomain.MemoryRecord, error)
}

// PreCheckMemoryLifecycleVectorStore is the narrow vector-side sync port used after relational adoption extends memory lifetimes.
// PreCheckMemoryLifecycleVectorStore 用于在关系侧采纳延长记忆生命周期后，同步旁路向量库的最小向量端口。
type PreCheckMemoryLifecycleVectorStore interface {
	Upsert(ctx context.Context, record logicdomain.MemoryRecord) error
}

// PreCheckConfig keeps the timeout, turn-window, and recall knobs local to the live pre-check workflow.
// PreCheckConfig 用于保存实时 pre-check 工作流本地使用的超时、turn 窗口和召回参数。
type PreCheckConfig struct {
	IntentTimeout        time.Duration
	TopK                 int
	MinSimilarityScore   float64
	SearchScope          string
	HistoryTurns         int
	MaxInputTokens       int
	ReviewCandidateLimit int
}

// PreCheckUseCase executes the live pre-check workflow on top of resolved session scope, recent turn windows, unified memory recall, and lifecycle write-back.
// PreCheckUseCase 用于在已解析 session 范围、最近 turn 窗口、统一记忆召回和生命周期回写之上执行实时 pre-check 工作流。
type PreCheckUseCase struct {
	memories    PreCheckMemorySearcher
	store       PreCheckStore
	intent      PreCheckIntentExtractor
	reviewer    PreCheckMemoryReviewer
	assembler   PreCheckContextAssembler
	lifecycle   PreCheckMemoryLifecycleVectorStore
	config      PreCheckConfig
	logger      *logx.Logger
	piiScrubber PIIScrubber
}

// NewPreCheckUseCase wires recall, intent, review, and context assembly dependencies while normalizing pre-check limits.
// NewPreCheckUseCase 用于装配召回、意图、评审和上下文组装依赖，并规范化 pre-check 限制项。
func NewPreCheckUseCase(memories PreCheckMemorySearcher, store PreCheckStore, intent PreCheckIntentExtractor, reviewer PreCheckMemoryReviewer, assembler PreCheckContextAssembler, cfg PreCheckConfig, logger *logx.Logger) *PreCheckUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	if cfg.TopK <= 0 {
		cfg.TopK = defaultPreCheckTopK
	}
	if cfg.HistoryTurns <= 0 {
		cfg.HistoryTurns = defaultPreCheckHistoryTurns
	}
	if cfg.MaxInputTokens <= 0 {
		cfg.MaxInputTokens = defaultPreCheckHistoryTokens
	}
	if cfg.ReviewCandidateLimit <= 0 {
		cfg.ReviewCandidateLimit = defaultPreCheckReviewCandidates
	}
	if cfg.MinSimilarityScore <= 0 || cfg.MinSimilarityScore > 1 {
		cfg.MinSimilarityScore = defaultPreCheckSimilarity
	}
	cfg.SearchScope = normalizePreCheckSearchScope(cfg.SearchScope)
	return &PreCheckUseCase{
		memories:  memories,
		store:     store,
		intent:    intent,
		reviewer:  reviewer,
		assembler: assembler,
		config:    cfg,
		logger:    logger,
	}
}

// ConfigurePIIScrubber injects the shared PII scrubber used to redact current requests, recalled history, and reviewer-facing memory candidates before pre-check sends them deeper into LLM-backed stages.
// ConfigurePIIScrubber 用于注入共享 PII 脱敏器，让 pre-check 在把当前请求、召回历史和 reviewer 可见记忆候选继续送入 LLM 阶段前先完成脱敏。
func (u *PreCheckUseCase) ConfigurePIIScrubber(scrubber PIIScrubber) {
	if u == nil {
		return
	}
	u.piiScrubber = scrubber
}

// ConfigureMemoryAdoptionVectorSync wires the optional vector-side lifecycle sync used by split storage after adoption updates durable expiry values.
// ConfigureMemoryAdoptionVectorSync 用于接入可选的向量侧生命周期同步，让 split 存储在采纳更新长期过期时间后同步旁路表。
func (u *PreCheckUseCase) ConfigureMemoryAdoptionVectorSync(lifecycle PreCheckMemoryLifecycleVectorStore) {
	if u == nil {
		return
	}
	u.lifecycle = lifecycle
}

// Execute validates the resolved scope, runs the turn-centric two-stage memory flow, and returns only adopted memory context instead of mixing in the separate profile bundle.
// Execute 用于校验已解析范围、执行基于 turn 的两层记忆流程，并且只返回被采纳的记忆上下文，不再混入独立画像 bundle。
func (u *PreCheckUseCase) Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error) {
	if u == nil {
		return PreCheckResult{}, fmt.Errorf("pre-check use case is nil")
	}
	if err := validatePreCheck(cmd); err != nil {
		return PreCheckResult{}, err
	}
	if !cmd.Session.MemoryStatus.AllowsRuntimeMemory() {
		return PreCheckResult{
			ShouldInject: false,
			TraceID:      trace.IDFromContext(ctx),
		}, nil
	}
	cmd.UserContent = scrubPIIText(u.piiScrubber, cmd.UserContent)
	traceID := trace.IDFromContext(ctx)
	degraded := false

	// Build the recent mixed turn window so stage one can reason over refined details and still see pending raw turns when async extraction has not finished yet.
	// 构建最近混合 turn 窗口，让第一层既能利用已提炼 details，也能在异步提炼尚未完成时看到待处理原文。
	recentTurns, err := u.loadRecentTurnContexts(ctx, cmd.Session)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check recent turns degraded", traceID, cmd.Session, cmd.UserContent, err)
		recentTurns = nil
	} else {
		recentTurns = scrubPreCheckTurnContextsPII(u.piiScrubber, recentTurns)
		u.logPreCheckStage("pre-check recent turns prepared", traceID, cmd.Session, []any{
			"recent_turn_count", len(recentTurns),
			"history_turn_limit", u.config.HistoryTurns,
			"max_input_tokens", u.config.MaxInputTokens,
		}, map[string]any{
			"current_user_input": cmd.UserContent,
			"recent_turns":       recentTurns,
		})
	}

	// Run the first-stage intent extractor against the recent turn window and current request so recall only starts when memory is actually useful.
	// 用最近 turn 窗口和当前请求执行第一层意图提取，只在记忆确实有帮助时才启动召回。
	intent, err := u.extractIntent(ctx, recentTurns, cmd.UserContent)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check intent degraded", traceID, cmd.Session, cmd.UserContent, err)
		return u.finalizePreCheck(ctx, traceID, nil, degraded)
	}
	// Normalize the stage-one output locally so vague deictic queries do not over-trigger long-term retrieval when recent turns already explain the request.
	// 在本地归一第一层输出，避免最近 turn 已经足够解释请求时，模糊指代 query 仍过度触发长期检索。
	intent = normalizePreCheckIntentResult(intent, cmd.UserContent, recentTurns)
	u.logPreCheckStage("pre-check intent analyzed", traceID, cmd.Session, []any{
		"need_memory", intent.NeedMemory,
		"query_count", len(intent.Queries),
		"intent_reason_present", strings.TrimSpace(intent.Reason) != "",
	}, map[string]any{
		"current_user_input": cmd.UserContent,
		"recent_turns":       recentTurns,
		"intent_result":      intent,
	})
	if !intent.NeedMemory {
		result, finalizeErr := u.finalizePreCheck(ctx, traceID, nil, degraded)
		u.logPreCheckFinal(traceID, cmd.Session, result)
		return result, finalizeErr
	}

	// Recall unified memory using the search sentences returned by stage one, then number the final deduplicated candidate list for stage two.
	// 使用第一层返回的检索语句召回统一记忆，并给最终去重后的候选列表编号，交给第二层评审。
	candidates, err := u.searchMemoryCandidates(ctx, cmd, intent)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check memory recall degraded", traceID, cmd.Session, cmd.UserContent, err)
		candidates = nil
	} else {
		u.logPreCheckStage("pre-check memory candidates recalled", traceID, cmd.Session, []any{
			"candidate_count", len(candidates),
		}, map[string]any{
			"current_user_input": cmd.UserContent,
			"intent_queries":     normalizePreCheckMemoryQueries(intent.Queries, cmd.UserContent),
			"candidates":         candidates,
		})
	}
	if len(candidates) == 0 {
		result, finalizeErr := u.finalizePreCheck(ctx, traceID, nil, degraded)
		u.logPreCheckFinal(traceID, cmd.Session, result)
		return result, finalizeErr
	}

	// Let the second-stage reviewer choose candidate numbers in priority order, then map them back to memory ids for lifecycle write-back and final injection.
	// 让第二层评审器按优先顺序选择候选编号，再映射回 memory id，用于生命周期回写和最终注入。
	selectedCandidates, err := u.reviewMemoryCandidates(ctx, cmd, intent, candidates)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check memory adoption degraded", traceID, cmd.Session, cmd.UserContent, err)
		selectedCandidates = nil
	} else {
		selectedIDs := make([]uint64, 0, len(selectedCandidates))
		for _, candidate := range selectedCandidates {
			selectedIDs = append(selectedIDs, candidate.MemoryID)
		}
		u.logPreCheckStage("pre-check memory candidates reviewed", traceID, cmd.Session, []any{
			"selected_candidate_count", len(selectedCandidates),
		}, map[string]any{
			"current_user_input":  cmd.UserContent,
			"intent_queries":      normalizePreCheckMemoryQueries(intent.Queries, cmd.UserContent),
			"candidates":          candidates,
			"selected_memory_ids": selectedIDs,
		})
	}
	if len(selectedCandidates) == 0 {
		result, finalizeErr := u.finalizePreCheck(ctx, traceID, nil, degraded)
		u.logPreCheckFinal(traceID, cmd.Session, result)
		return result, finalizeErr
	}

	// Write back lifecycle updates only for adopted memories so recall alone never refreshes expiry or weight.
	// 只对被采纳的记忆写回生命周期更新，确保单纯召回不会刷新有效期或权重。
	if err := u.writeMemoryAdoption(ctx, cmd.Session, selectedCandidates); err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check lifecycle write-back degraded", traceID, cmd.Session, cmd.UserContent, err)
	} else {
		adoptedIDs := make([]uint64, 0, len(selectedCandidates))
		for _, candidate := range selectedCandidates {
			adoptedIDs = append(adoptedIDs, candidate.MemoryID)
		}
		u.logPreCheckStage("pre-check lifecycle write-back completed", traceID, cmd.Session, []any{
			"adopted_memory_count", len(adoptedIDs),
		}, map[string]any{
			"adopted_memory_ids": adoptedIDs,
		})
	}
	result, finalizeErr := u.finalizePreCheck(ctx, traceID, selectedCandidates, degraded)
	u.logPreCheckFinal(traceID, cmd.Session, result)
	return result, finalizeErr
}

// extractIntent runs the first-stage intent extractor under the configured inner timeout budget.
// extractIntent 用于在配置好的内部超时预算内执行第一层意图提取。
func (u *PreCheckUseCase) extractIntent(ctx context.Context, turns []logicdomain.PreCheckTurnContext, current string) (logicdomain.IntentResult, error) {
	if u.intent == nil {
		return logicdomain.IntentResult{}, fmt.Errorf("pre-check intent extractor is nil")
	}
	intentCtx := ctx
	cancel := func() {}
	if u.config.IntentTimeout > 0 {
		intentCtx, cancel = context.WithTimeout(ctx, u.config.IntentTimeout)
	}
	defer cancel()
	return u.intent.Extract(intentCtx, turns, current)
}

// validatePreCheck checks the resolved identifiers and current user content before the use case enters the live pre-check workflow.
// validatePreCheck 用于在用例进入实时 pre-check 工作流前校验已解析标识和当前用户文本。
func validatePreCheck(cmd PreCheckCommand) error {
	if cmd.Session.SessionID == 0 || strings.TrimSpace(cmd.Session.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if cmd.Session.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if cmd.Session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if strings.TrimSpace(cmd.UserContent) == "" {
		return logicdomain.ValidationError{Field: "user_content", Message: "is required"}
	}
	return nil
}
