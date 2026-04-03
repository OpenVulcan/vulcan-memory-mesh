// precheck.go implements the live pre-check flow that first reasons over recent turns, then recalls unified memory, then lets a second LLM choose numbered candidates.
// precheck.go 用于实现实时 pre-check 流程：先基于最近 turn 做推理，再召回统一记忆，最后由第二层 LLM 从编号候选中做选择。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
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
)

var (
	// defaultPreCheckSimilarity keeps recall aligned with the repo's default vector similarity floor when config does not override it.
	// defaultPreCheckSimilarity 用于在配置未覆盖时，让召回沿用仓库默认的相似度下限。
	defaultPreCheckSimilarity = 0.75
)

// PreCheckCommand carries the resolved session scope plus the current user text into the pre-check workflow.
// PreCheckCommand 用于承载已解析的 session 范围和当前用户文本，进入 pre-check 工作流。
type PreCheckCommand struct {
	Session     logicdomain.SessionRef
	UserContent string
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
}

// PreCheckMemoryReviewer is the second-stage processor used to adopt truly useful numbered memory candidates from the recalled pool.
// PreCheckMemoryReviewer 用于表示第二层处理器，负责从召回池中的编号候选里采纳真正有用的记忆。
type PreCheckMemoryReviewer interface {
	Review(ctx context.Context, input logicdomain.PreCheckMemoryReviewInput) (logicdomain.PreCheckMemoryReviewResult, error)
}

// PreCheckContextAssembler turns persona data plus adopted memories into the final injection payload returned by the RPC.
// PreCheckContextAssembler 用于把画像数据和已采纳记忆组合成 RPC 返回的最终注入载荷。
type PreCheckContextAssembler interface {
	Assemble(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error)
}

// PreCheckProfileBundleLoader resolves the current TEAM/SPACE/PROJECT/USER rendered profiles so pre-check can inject stable environment context.
// PreCheckProfileBundleLoader 用于解析当前 TEAM/SPACE/PROJECT/USER 渲染画像，让 pre-check 可以注入稳定的环境上下文。
type PreCheckProfileBundleLoader interface {
	GetBundle(ctx context.Context, cmd ProfileBundleCommand) (ProfileBundleResult, error)
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
	ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error
}

// PreCheckConfig keeps the timeout, turn-window, and recall knobs local to the live pre-check workflow.
// PreCheckConfig 用于保存实时 pre-check 工作流本地使用的超时、turn 窗口和召回参数。
type PreCheckConfig struct {
	IntentTimeout        time.Duration
	TopK                 int
	MinSimilarityScore   float64
	HistoryTurns         int
	MaxInputTokens       int
	ReviewCandidateLimit int
}

// PreCheckUseCase executes the live pre-check workflow on top of resolved session scope, recent turn windows, unified memory recall, and lifecycle write-back.
// PreCheckUseCase 用于在已解析 session 范围、最近 turn 窗口、统一记忆召回和生命周期回写之上执行实时 pre-check 工作流。
type PreCheckUseCase struct {
	profiles  PreCheckProfileBundleLoader
	memories  PreCheckMemorySearcher
	store     PreCheckStore
	intent    PreCheckIntentExtractor
	reviewer  PreCheckMemoryReviewer
	assembler PreCheckContextAssembler
	config    PreCheckConfig
	logger    *logx.Logger
}

// NewPreCheckUseCase creates a PreCheckUseCase instance.
// NewPreCheckUseCase 用于创建 PreCheckUseCase 实例。
func NewPreCheckUseCase(profiles PreCheckProfileBundleLoader, memories PreCheckMemorySearcher, store PreCheckStore, intent PreCheckIntentExtractor, reviewer PreCheckMemoryReviewer, assembler PreCheckContextAssembler, cfg PreCheckConfig, logger *logx.Logger) *PreCheckUseCase {
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
	return &PreCheckUseCase{
		profiles:  profiles,
		memories:  memories,
		store:     store,
		intent:    intent,
		reviewer:  reviewer,
		assembler: assembler,
		config:    cfg,
		logger:    logger,
	}
}

// Execute validates the resolved scope, runs the turn-centric two-stage memory flow, and returns the final assembled context or a degraded fallback.
// Execute 用于校验已解析范围、执行基于 turn 的两层记忆流程，并返回最终组装后的上下文或降级回退结果。
func (u *PreCheckUseCase) Execute(ctx context.Context, cmd PreCheckCommand) (PreCheckResult, error) {
	if err := validatePreCheck(cmd); err != nil {
		return PreCheckResult{}, err
	}
	traceID := trace.IDFromContext(ctx)
	degraded := false

	// Load stable profile bundle first so the request can still return environment context even if one later memory step degrades.
	// 先加载稳定画像组合，确保即便后续某个记忆步骤降级，请求仍能返回环境上下文。
	persona, err := u.loadPersonaContext(ctx, cmd.Session)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check persona bundle degraded", traceID, cmd.Session, err)
		persona = logicdomain.PersonaContext{}
	}

	// Build the recent mixed turn window so stage one can reason over refined details and still see pending raw turns when async extraction has not finished yet.
	// 构建最近混合 turn 窗口，让第一层既能利用已提炼 details，也能在异步提炼尚未完成时看到待处理原文。
	recentTurns, err := u.loadRecentTurnContexts(ctx, cmd.Session)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check recent turns degraded", traceID, cmd.Session, err)
		recentTurns = nil
	}

	// Run the first-stage intent extractor against the recent turn window and current request so recall only starts when memory is actually useful.
	// 用最近 turn 窗口和当前请求执行第一层意图提取，只在记忆确实有帮助时才启动召回。
	intent, err := u.extractIntent(ctx, recentTurns, cmd.UserContent)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check intent degraded", traceID, cmd.Session, err)
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}
	// Normalize the stage-one output locally so vague deictic queries do not over-trigger long-term retrieval when recent turns already explain the request.
	// 在本地归一第一层输出，避免最近 turn 已经足够解释请求时，模糊指代 query 仍过度触发长期检索。
	intent = normalizePreCheckIntentResult(intent, cmd.UserContent, recentTurns)
	if !intent.NeedMemory {
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}

	// Recall unified memory using the search sentences returned by stage one, then number the final deduplicated candidate list for stage two.
	// 使用第一层返回的检索语句召回统一记忆，并给最终去重后的候选列表编号，交给第二层评审。
	candidates, err := u.searchMemoryCandidates(ctx, cmd, intent)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check memory recall degraded", traceID, cmd.Session, err)
		candidates = nil
	}
	if len(candidates) == 0 {
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}

	// Let the second-stage reviewer choose candidate numbers in priority order, then map them back to memory ids for lifecycle write-back and final injection.
	// 让第二层评审器按优先顺序选择候选编号，再映射回 memory id，用于生命周期回写和最终注入。
	selectedCandidates, err := u.reviewMemoryCandidates(ctx, cmd, intent, candidates)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check memory adoption degraded", traceID, cmd.Session, err)
		selectedCandidates = nil
	}
	if len(selectedCandidates) == 0 {
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}

	// Write back lifecycle updates only for adopted memories so recall alone never refreshes expiry or weight.
	// 只对被采纳的记忆写回生命周期更新，确保单纯召回不会刷新有效期或权重。
	if err := u.writeMemoryAdoption(ctx, cmd.Session, selectedCandidates); err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check lifecycle write-back degraded", traceID, cmd.Session, err)
	}
	return u.finalizePreCheck(ctx, traceID, persona, selectedCandidates, degraded)
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

// loadRecentTurnContexts loads the recent session turns, trims them by budget, and converts them into refined-or-raw turn fragments for stage-one reasoning.
// loadRecentTurnContexts 用于加载最近 session turn、按预算裁剪，并把它们转换成“提炼文或原文”的第一层推理片段。
func (u *PreCheckUseCase) loadRecentTurnContexts(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.PreCheckTurnContext, error) {
	if u.store == nil || u.config.HistoryTurns <= 0 {
		return []logicdomain.PreCheckTurnContext{}, nil
	}
	rows, err := u.store.LoadRecentSessionTurns(ctx, session, u.config.HistoryTurns)
	if err != nil {
		return nil, err
	}
	rows = trimRecentTurnsByBudget(rows, u.config.MaxInputTokens)
	contexts := make([]logicdomain.PreCheckTurnContext, 0, len(rows))
	for _, row := range rows {
		context := logicdomain.PreCheckTurnContext{TurnID: row.ID}
		if row.ExtractedStatus == logicdomain.TurnExtractedStatusDone && strings.TrimSpace(row.Details) != "" {
			context.ContentType = "DETAILS"
			context.Content = strings.TrimSpace(row.Details)
		} else {
			context.ContentType = "RAW_TURN"
			context.Content = strings.TrimSpace(row.DehydratedContent)
		}
		if context.TurnID == 0 || strings.TrimSpace(context.Content) == "" {
			continue
		}
		contexts = append(contexts, context)
	}
	return contexts, nil
}

// trimRecentTurnsByBudget keeps the newest turns under the configured token budget while preserving final chronological order.
// trimRecentTurnsByBudget 用于在配置的 token 预算内保留最新 turn，并在最终结果中维持时间顺序。
func trimRecentTurnsByBudget(rows []logicdomain.SessionTurnRecord, maxInputTokens int) []logicdomain.SessionTurnRecord {
	if len(rows) == 0 || maxInputTokens <= 0 {
		return append([]logicdomain.SessionTurnRecord(nil), rows...)
	}
	selected := make([]logicdomain.SessionTurnRecord, 0, len(rows))
	total := 0
	for idx := len(rows) - 1; idx >= 0; idx-- {
		budget := preCheckTurnBudget(rows[idx])
		if len(selected) > 0 && total+budget > maxInputTokens {
			break
		}
		selected = append(selected, rows[idx])
		total += budget
	}
	if len(selected) == 0 {
		return []logicdomain.SessionTurnRecord{rows[len(rows)-1]}
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

// preCheckTurnBudget chooses the extracted-details budget when available and falls back to dehydrated budget for still-pending turns.
// preCheckTurnBudget 用于在存在 details 时优先使用已提炼预算，并对仍为 pending 的 turn 回退到脱水预算。
func preCheckTurnBudget(row logicdomain.SessionTurnRecord) int {
	if row.ExtractedStatus == logicdomain.TurnExtractedStatusDone && strings.TrimSpace(row.Details) != "" {
		if row.DetailsBudget > 0 {
			return row.DetailsBudget
		}
		return estimatePreCheckBudget(row.Details)
	}
	if row.DehydratedBudget > 0 {
		return row.DehydratedBudget
	}
	return estimatePreCheckBudget(row.DehydratedContent)
}

// estimatePreCheckBudget applies the local token estimator to one turn fragment so recent-turn trimming stays aligned with other text-budget decisions in the repo.
// estimatePreCheckBudget 用于对 turn 片段应用本地 token 估算器，让最近 turn 裁剪与仓库里其他文本预算逻辑保持一致。
func estimatePreCheckBudget(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	estimator := textutil.NewTokenEstimator(textutil.DomesticTokenEstimatorConfig())
	return estimator.Estimate(text)
}

// loadPersonaContext reuses the split profile-bundle output so pre-check can inject stable TEAM/SPACE/PROJECT/USER context without duplicating profile SQL here.
// loadPersonaContext 用于复用 split profile bundle 的输出，让 pre-check 可以注入稳定的 TEAM/SPACE/PROJECT/USER 上下文，而无需在这里重复画像 SQL。
func (u *PreCheckUseCase) loadPersonaContext(ctx context.Context, session logicdomain.SessionRef) (logicdomain.PersonaContext, error) {
	if u.profiles == nil {
		return logicdomain.PersonaContext{}, nil
	}
	bundle, err := u.profiles.GetBundle(ctx, ProfileBundleCommand{
		UserID:             session.UserID,
		ProjectID:          session.ProjectID,
		Mode:               ProfileBundleModeSplit,
		IncludeExplanation: false,
	})
	if err != nil {
		return logicdomain.PersonaContext{}, err
	}
	persona := logicdomain.PersonaContext{
		ProjectConstraints: make([]string, 0, 3),
		Preferences:        make([]string, 0, 1),
	}
	appendSection := func(target *[]string, title, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		*target = append(*target, "["+title+"]\n"+body)
	}
	appendSection(&persona.ProjectConstraints, "TEAM", bundle.TeamProfile)
	appendSection(&persona.ProjectConstraints, "SPACE", bundle.SpaceProfile)
	appendSection(&persona.ProjectConstraints, "PROJECT", bundle.ProjectProfile)
	appendSection(&persona.Preferences, "USER", bundle.UserProfile)
	return persona, nil
}

// searchMemoryCandidates reuses the unified memory-search RPC logic, then keeps only de-duplicated hits above the configured similarity floor and numbers them for stage two.
// searchMemoryCandidates 用于复用统一记忆查询逻辑，然后只保留高于配置相似度下限且去重后的命中结果，并为第二层评审分配候选编号。
func (u *PreCheckUseCase) searchMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.memories == nil {
		return []logicdomain.PreCheckMemoryCandidate{}, nil
	}
	queryJSON, err := buildPreCheckMemoryQueryJSON(intent, cmd.UserContent)
	if err != nil {
		return nil, err
	}
	result, err := u.memories.Search(ctx, MemoryQueryCommand{
		UserID:    cmd.Session.UserID,
		ProjectID: cmd.Session.ProjectID,
		QueryJSON: queryJSON,
		TopK:      u.config.TopK,
	})
	if err != nil {
		return nil, err
	}
	merged := make(map[uint64]logicdomain.PreCheckMemoryCandidate)
	firstSeenOrder := make(map[uint64]int)
	nextSeenOrder := 0
	for _, group := range result.Results {
		for hitIdx, hit := range group.Hits {
			if hit.MemoryRef.Type != logicdomain.MemoryRefTypeMemory || hit.MemoryRef.ID == 0 {
				continue
			}
			candidateScore := normalizePreCheckReviewScore(hit.Score, hit.Origin, hitIdx+1, len(group.Hits))
			if candidateScore < u.config.MinSimilarityScore {
				continue
			}
			candidate := logicdomain.PreCheckMemoryCandidate{
				MemoryID:                    hit.MemoryRef.ID,
				SourceTurnID:                hit.SourceRef.ID,
				SourceKind:                  logicdomain.MemorySourceKindLabel(hit.SourceKind),
				ScopeLevel:                  logicdomain.MemoryScopeLevelLabel(hit.ScopeLevel),
				Category:                    hit.Category,
				Abstract:                    strings.TrimSpace(hit.Abstract),
				Details:                     strings.TrimSpace(hit.DetailsPreview),
				Score:                       candidateScore,
				Origin:                      strings.TrimSpace(hit.Origin),
				SupportCount:                hit.SupportCount,
				RebuttalCount:               hit.RebuttalCount,
				MatchedContextValues:        append([]string(nil), hit.MatchedContextValues...),
				MatchedContextSupportCount:  hit.MatchedContextSupportCount,
				MatchedContextRebuttalCount: hit.MatchedContextRebuttalCount,
				MatchedContextScoreDelta:    hit.MatchedContextScoreDelta,
			}
			if candidate.Abstract == "" && candidate.Details == "" {
				continue
			}
			if candidate.Details == "" {
				candidate.Details = candidate.Abstract
			}
			if _, ok := firstSeenOrder[candidate.MemoryID]; !ok {
				firstSeenOrder[candidate.MemoryID] = nextSeenOrder
				nextSeenOrder++
			}
			if existing, ok := merged[candidate.MemoryID]; ok {
				merged[candidate.MemoryID] = mergePreCheckCandidate(existing, candidate)
				continue
			}
			merged[candidate.MemoryID] = candidate
		}
	}
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(merged))
	for _, candidate := range merged {
		candidate = normalizePreCheckCandidateDerivedFields(candidate, u.config.MinSimilarityScore)
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		leftOrder, leftOK := firstSeenOrder[out[i].MemoryID]
		rightOrder, rightOK := firstSeenOrder[out[j].MemoryID]
		if leftOK && rightOK && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	if len(out) > u.config.ReviewCandidateLimit {
		out = out[:u.config.ReviewCandidateLimit]
	}
	for idx := range out {
		out[idx].CandidateNumber = idx + 1
	}
	return out, nil
}

// normalizePreCheckCandidateDerivedFields recomputes reviewer-facing derived explanations from the final merged candidate values so score/origin labels never lag behind later merge decisions.
// normalizePreCheckCandidateDerivedFields 用于基于最终合并后的候选值重新计算 reviewer 可见的派生说明，避免 score/origin 标签滞后于后续 merge 决策。
func normalizePreCheckCandidateDerivedFields(candidate logicdomain.PreCheckMemoryCandidate, threshold float64) logicdomain.PreCheckMemoryCandidate {
	scoreExplanation := describePreCheckCandidateScore(candidate.Score, threshold, candidate.MatchedContextScoreDelta)
	originExplanation := describePreCheckMemoryOrigin(candidate.Origin)
	candidate.ScoreLabel = scoreExplanation.Label
	candidate.ScoreExplanation = scoreExplanation.Explanation
	candidate.OriginLabel = originExplanation.Label
	candidate.OriginExplanation = originExplanation.Explanation
	return candidate
}

// reviewMemoryCandidates lets the second-stage reviewer choose candidate numbers and restores the selected candidate order for final injection.
// reviewMemoryCandidates 用于让第二层评审器选择候选编号，并恢复最终注入使用的候选顺序。
func (u *PreCheckUseCase) reviewMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult, candidates []logicdomain.PreCheckMemoryCandidate) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.reviewer == nil {
		return nil, fmt.Errorf("pre-check memory reviewer is nil")
	}
	searchQueries := normalizePreCheckMemoryQueries(intent.Queries, cmd.UserContent)
	review, err := u.reviewer.Review(ctx, logicdomain.PreCheckMemoryReviewInput{
		UserContent:   cmd.UserContent,
		SearchQueries: searchQueries,
		IntentReason:  intent.Reason,
		Candidates:    append([]logicdomain.PreCheckMemoryCandidate(nil), candidates...),
	})
	if err != nil {
		return nil, err
	}
	candidatesByNumber := make(map[int]logicdomain.PreCheckMemoryCandidate, len(candidates))
	for _, candidate := range candidates {
		candidatesByNumber[candidate.CandidateNumber] = candidate
	}
	selected := make([]logicdomain.PreCheckMemoryCandidate, 0, len(review.SelectedCandidateNumbers))
	for _, number := range review.SelectedCandidateNumbers {
		if candidate, ok := candidatesByNumber[number]; ok {
			selected = append(selected, candidate)
		}
	}
	return selected, nil
}

// writeMemoryAdoption persists lifecycle updates only for the final adopted memory ids selected by the second-stage reviewer.
// writeMemoryAdoption 用于只为第二层评审器最终采纳的记忆 id 持久化生命周期更新。
func (u *PreCheckUseCase) writeMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, candidates []logicdomain.PreCheckMemoryCandidate) error {
	if u.store == nil || len(candidates) == 0 {
		return nil
	}
	memoryIDs := make([]uint64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.MemoryID > 0 {
			memoryIDs = append(memoryIDs, candidate.MemoryID)
		}
	}
	return u.store.ApplyMemoryAdoption(ctx, session, memoryIDs, time.Now().UTC())
}

// finalizePreCheck assembles the final context text and item list, falling back to a deterministic local renderer when the shared assembler is unavailable.
// finalizePreCheck 用于组装最终上下文文本和条目列表；若共享 assembler 不可用，则回退到确定性的本地渲染器。
func (u *PreCheckUseCase) finalizePreCheck(ctx context.Context, traceID string, persona logicdomain.PersonaContext, memories []logicdomain.PreCheckMemoryCandidate, degraded bool) (PreCheckResult, error) {
	memoryHits := make([]logicdomain.MemoryHit, 0, len(memories))
	for _, candidate := range memories {
		text := buildPreCheckMemoryText(candidate)
		if strings.TrimSpace(text) == "" {
			continue
		}
		memoryHits = append(memoryHits, logicdomain.MemoryHit{
			ID:    fmt.Sprintf("%d", candidate.MemoryID),
			Text:  text,
			Score: candidate.Score,
		})
	}
	contextText, items, assembleDegraded, err := u.assemblePreCheckContext(ctx, persona, memoryHits)
	if err != nil {
		return PreCheckResult{}, err
	}
	return PreCheckResult{
		ShouldInject: len(items) > 0,
		ContextText:  contextText,
		ContextItems: items,
		Degraded:     degraded || assembleDegraded,
		TraceID:      traceID,
	}, nil
}

// assemblePreCheckContext prefers the shared assembler but falls back to a local deterministic renderer if prompt loading degrades.
// assemblePreCheckContext 用于优先使用共享 assembler；若提示词加载降级，则回退到本地确定性渲染。
func (u *PreCheckUseCase) assemblePreCheckContext(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, bool, error) {
	if u.assembler != nil {
		contextText, items, err := u.assembler.Assemble(ctx, persona, hits)
		if err == nil {
			return contextText, items, false, nil
		}
		if u.logger != nil {
			u.logger.Warn("pre-check assembler degraded to fallback", "err", err)
		}
		// Surface fallback activation to the caller so the RPC degraded bit stays honest even when local rendering succeeds.
		// 把 fallback 激活信号向上传递，确保即便本地渲染成功，RPC 的 degraded 标志仍能如实反映本次降级。
		fallbackItems := buildFallbackContextItems(persona, hits)
		return buildFallbackContextSummary(fallbackItems), fallbackItems, true, nil
	}
	items := buildFallbackContextItems(persona, hits)
	return buildFallbackContextSummary(items), items, false, nil
}

// buildPreCheckMemoryQueryJSON converts the stage-one search sentences into the grouped memory-search JSON format already consumed by the unified search surface.
// buildPreCheckMemoryQueryJSON 用于把第一层检索语句转换成统一记忆查询接口已消费的分组 JSON 格式。
func buildPreCheckMemoryQueryJSON(intent logicdomain.IntentResult, userContent string) (string, error) {
	queries := normalizePreCheckMemoryQueries(intent.Queries, userContent)
	items := make([]MemoryQueryItem, 0, len(queries))
	for _, query := range queries {
		items = append(items, MemoryQueryItem{
			Background: strings.TrimSpace(userContent),
			Query:      query,
		})
	}
	body, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("marshal pre-check query json: %w", err)
	}
	return string(body), nil
}

// normalizePreCheckMemoryQueries trims and de-duplicates stage-one queries so pre-check only searches and explains the distinct retrieval prompts that actually matter.
// normalizePreCheckMemoryQueries 用于裁剪并去重第一层 query，让 pre-check 只检索并解释真正有意义的去重检索语句。
func normalizePreCheckMemoryQueries(queries []string, userContent string) []string {
	normalized := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = textutil.NormalizeWhitespace(query)
		if query == "" {
			continue
		}
		key := strings.ToLower(textutil.NormalizeWhitespace(query))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, query)
	}
	if len(normalized) > 0 {
		return normalized
	}
	userContent = textutil.NormalizeWhitespace(userContent)
	if userContent == "" {
		return nil
	}
	return []string{userContent}
}

// buildPreCheckMemoryText turns one adopted candidate into the compact text fragment injected back to the upstream caller.
// buildPreCheckMemoryText 用于把一条已采纳候选转成回注给上游调用方的紧凑文本片段。
func buildPreCheckMemoryText(candidate logicdomain.PreCheckMemoryCandidate) string {
	abstract := strings.TrimSpace(candidate.Abstract)
	details := strings.TrimSpace(candidate.Details)
	switch {
	case abstract == "" && details == "":
		return ""
	case details == "" || equivalentPreCheckMemoryText(abstract, details):
		return abstract
	case abstract == "":
		return details
	default:
		return abstract + "\n" + details
	}
}

// equivalentPreCheckMemoryText treats whitespace-only and case-only formatting differences as the same injected memory text so pre-check does not repeat one statement twice in the final context.
// equivalentPreCheckMemoryText 用于把仅有空白或大小写差异的文本视为同一条注入记忆，避免 pre-check 在最终上下文里把同一句话重复注入两次。
func equivalentPreCheckMemoryText(left, right string) bool {
	left = strings.ToLower(textutil.NormalizeWhitespace(left))
	right = strings.ToLower(textutil.NormalizeWhitespace(right))
	if left == "" || right == "" {
		return left == right
	}
	return left == right
}

// buildFallbackContextItems reproduces the stable grouping used by the shared assembler so pre-check can still answer when prompt loading fails.
// buildFallbackContextItems 用于复刻共享 assembler 的稳定分组逻辑，确保提示词加载失败时 pre-check 仍能回答。
func buildFallbackContextItems(persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) []logicdomain.ContextItem {
	items := make([]logicdomain.ContextItem, 0, len(persona.ProjectConstraints)+len(persona.Profile)+len(persona.Preferences)+len(hits))
	for _, text := range persona.ProjectConstraints {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "project_constraint", Title: "项目约束", Text: text, Source: "persona"})
		}
	}
	for _, text := range persona.Profile {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "persona", Title: "个人画像", Text: text, Source: "persona"})
		}
	}
	for _, text := range persona.Preferences {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "preference", Title: "偏好习惯", Text: text, Source: "persona"})
		}
	}
	for _, hit := range hits {
		if text := strings.TrimSpace(hit.Text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "memory", Title: "混合召回记忆", Text: text, Source: "memory", Score: hit.Score})
		}
	}
	return items
}

// buildFallbackContextSummary renders the final fallback context text with the same deterministic section order used by the shared assembler.
// buildFallbackContextSummary 用于按与共享 assembler 相同的确定性 section 顺序渲染最终回退上下文文本。
func buildFallbackContextSummary(items []logicdomain.ContextItem) string {
	if len(items) == 0 {
		return ""
	}
	order := []struct{ Kind, Title string }{
		{Kind: "project_constraint", Title: "项目约束"},
		{Kind: "persona", Title: "个人画像"},
		{Kind: "preference", Title: "偏好习惯"},
		{Kind: "memory", Title: "向量召回记忆"},
	}
	var b strings.Builder
	firstSection := true
	for _, group := range order {
		section := make([]logicdomain.ContextItem, 0)
		for _, item := range items {
			if item.Kind == group.Kind {
				section = append(section, item)
			}
		}
		if len(section) == 0 {
			continue
		}
		if !firstSection {
			b.WriteString("\n\n")
		}
		firstSection = false
		title := fallbackContextSectionTitle(group.Title, section)
		b.WriteString("[")
		b.WriteString(title)
		b.WriteString("]\n")
		for idx, item := range section {
			if item.Kind == "memory" {
				b.WriteString(fmt.Sprintf("%d. %s (score=%.3f)\n", idx+1, item.Text, item.Score))
				continue
			}
			b.WriteString("- ")
			b.WriteString(item.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// fallbackContextSectionTitle keeps fallback text and structured items aligned by preferring the section title already carried by the first item in that section.
// fallbackContextSectionTitle 用于优先复用分组里第一条 item 自带的标题，保证 fallback 文本和结构化条目在 section 命名上保持一致。
func fallbackContextSectionTitle(defaultTitle string, section []logicdomain.ContextItem) string {
	if len(section) == 0 {
		return defaultTitle
	}
	title := strings.TrimSpace(section[0].Title)
	if title == "" {
		return defaultTitle
	}
	return title
}

// logPreCheckWarn keeps degraded-path logging uniform so field triage remains easy after rollout.
// logPreCheckWarn 用于统一降级路径日志，方便上线后排查问题。
func (u *PreCheckUseCase) logPreCheckWarn(message, traceID string, session logicdomain.SessionRef, err error) {
	if u.logger == nil {
		return
	}
	u.logger.Warn(message,
		"trace_id", traceID,
		"session_key", session.SessionKey,
		"session_id", session.SessionID,
		"project_id", session.ProjectID,
		"user_id", session.UserID,
		"err", err,
	)
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
