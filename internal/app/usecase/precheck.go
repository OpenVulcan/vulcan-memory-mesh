// precheck.go implements the live pre-check flow that performs intent extraction, unified memory recall, second-stage adoption, and final context assembly.
// precheck.go 用于实现实时 pre-check 流程，负责执行意图提取、统一记忆召回、第二层采纳和最终上下文组装。
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
	"github.com/openvulcan/vmm/internal/platform/trace"
)

const (
	// defaultPreCheckTopK keeps vector recall bounded when callers omit a config override.
	// defaultPreCheckTopK 用于在调用方省略配置覆盖时，为向量召回提供受控默认值。
	defaultPreCheckTopK = 5

	// defaultPreCheckHistoryTurns keeps the first-stage intent prompt focused on only the most recent extracted turns.
	// defaultPreCheckHistoryTurns 用于让第一层意图提示词只关注最近几条已提炼 turn。
	defaultPreCheckHistoryTurns = 2

	// defaultPreCheckRecentSessionMemories keeps the second-stage reviewer grounded in only the freshest in-session memory nodes.
	// defaultPreCheckRecentSessionMemories 用于让第二层评审器只参考当前 session 内最新的一小批记忆节点。
	defaultPreCheckRecentSessionMemories = 8

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

// PreCheckIntentExtractor is the first-stage processor used to decide whether memory is needed and which keywords should drive recall.
// PreCheckIntentExtractor 用于表示第一层处理器，负责判断是否需要记忆以及该用哪些关键词驱动召回。
type PreCheckIntentExtractor interface {
	Extract(ctx context.Context, history []logicdomain.HistorySnippet, current string) (logicdomain.IntentResult, error)
}

// PreCheckMemoryReviewer is the second-stage processor used to adopt truly useful memory candidates from the recalled pool.
// PreCheckMemoryReviewer 用于表示第二层处理器，负责从召回池中采纳真正有用的记忆候选。
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

// PreCheckStore loads recent extracted session state and writes lifecycle updates for adopted memory rows.
// PreCheckStore 用于加载最近提炼过的 session 状态，并为被采纳的记忆行写回生命周期更新。
type PreCheckStore interface {
	LoadRecentSessionHistory(ctx context.Context, session logicdomain.SessionRef, limit int) ([]logicdomain.SessionTurnRecord, error)
	LoadActiveSessionMemoryNodes(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.SessionMemoryNodeRecord, error)
	ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) error
}

// PreCheckConfig keeps the first-stage timeout and recall fan-out knobs local to the live pre-check workflow.
// PreCheckConfig 用于保存实时 pre-check 工作流本地使用的第一层超时和召回扇出参数。
type PreCheckConfig struct {
	IntentTimeout         time.Duration
	TopK                  int
	MinSimilarityScore    float64
	HistoryTurns          int
	RecentSessionMemories int
	ReviewCandidateLimit  int
}

// PreCheckUseCase executes the live pre-check workflow on top of resolved session scope, rendered profiles, unified memory recall, and lifecycle write-back.
// PreCheckUseCase 用于在已解析 session 范围、渲染画像、统一记忆召回和生命周期回写之上执行实时 pre-check 工作流。
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
	if cfg.RecentSessionMemories <= 0 {
		cfg.RecentSessionMemories = defaultPreCheckRecentSessionMemories
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

// Execute validates the resolved scope, runs the two-stage memory flow, and returns the final assembled context or a degraded fallback.
// Execute 用于校验已解析范围、执行两层记忆流程，并返回最终组装后的上下文或降级回退结果。
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

	// Run the first-stage intent extractor against the latest extracted history so memory recall only starts when the user really needs it.
	// 用最新已提炼历史执行第一层意图提取，只在用户确实需要记忆时才启动召回。
	intent, err := u.extractIntent(ctx, cmd)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check intent degraded", traceID, cmd.Session, err)
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}
	if !intent.NeedMemory {
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}

	// Gather recent in-session memory plus vector-recalled durable memory so the second-stage reviewer can choose only truly useful rows.
	// 汇总当前 session 内最近记忆和向量召回出来的长期记忆，让第二层评审器只采纳真正有用的条目。
	recentSession, err := u.loadRecentSessionMemoryCandidates(ctx, cmd.Session)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check recent session memory degraded", traceID, cmd.Session, err)
		recentSession = nil
	}
	recalled, err := u.searchMemoryCandidates(ctx, cmd, intent, recentSession)
	if err != nil {
		degraded = true
		u.logPreCheckWarn("pre-check memory recall degraded", traceID, cmd.Session, err)
		recalled = nil
	}
	if len(recentSession) == 0 && len(recalled) == 0 {
		return u.finalizePreCheck(ctx, traceID, persona, nil, degraded)
	}

	// Let the second-stage reviewer decide which unified memory ids are worth injecting for this concrete user request.
	// 让第二层评审器根据本次具体用户请求，决定哪些统一记忆 id 值得注入。
	selectedCandidates, err := u.reviewMemoryCandidates(ctx, cmd, intent, recentSession, recalled)
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

// extractIntent loads recent extracted history and runs the first-stage intent extractor under the configured inner timeout budget.
// extractIntent 用于加载最近已提炼历史，并在配置好的内部超时预算内执行第一层意图提取。
func (u *PreCheckUseCase) extractIntent(ctx context.Context, cmd PreCheckCommand) (logicdomain.IntentResult, error) {
	if u.intent == nil {
		return logicdomain.IntentResult{}, fmt.Errorf("pre-check intent extractor is nil")
	}
	history, err := u.loadHistorySnippets(ctx, cmd.Session)
	if err != nil {
		return logicdomain.IntentResult{}, err
	}
	intentCtx := ctx
	cancel := func() {}
	if u.config.IntentTimeout > 0 {
		intentCtx, cancel = context.WithTimeout(ctx, u.config.IntentTimeout)
	}
	defer cancel()
	return u.intent.Extract(intentCtx, history, cmd.UserContent)
}

// loadHistorySnippets converts the latest extracted turns into the compact history snippet shape expected by the intent prompt.
// loadHistorySnippets 用于把最近已提炼过的 turn 转成意图提示词期望的紧凑 history snippet 结构。
func (u *PreCheckUseCase) loadHistorySnippets(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.HistorySnippet, error) {
	if u.store == nil || u.config.HistoryTurns <= 0 {
		return []logicdomain.HistorySnippet{}, nil
	}
	rows, err := u.store.LoadRecentSessionHistory(ctx, session, u.config.HistoryTurns)
	if err != nil {
		return nil, err
	}
	snippets := make([]logicdomain.HistorySnippet, 0, len(rows)*2)
	for _, row := range rows {
		userContent, _, assistantContent := parseDehydratedTurnContent(row.DehydratedContent)
		if text := strings.TrimSpace(userContent); text != "" {
			snippets = append(snippets, logicdomain.HistorySnippet{Role: "user", Content: text})
		}
		if text := strings.TrimSpace(assistantContent); text != "" {
			snippets = append(snippets, logicdomain.HistorySnippet{Role: "assistant", Content: text})
		}
	}
	return snippets, nil
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

// loadRecentSessionMemoryCandidates keeps only the newest active rows from the current session so the second-stage reviewer can see recent short-term state.
// loadRecentSessionMemoryCandidates 用于保留当前 session 内最新的一小批活跃记忆行，让第二层评审器看见最近短期状态。
func (u *PreCheckUseCase) loadRecentSessionMemoryCandidates(ctx context.Context, session logicdomain.SessionRef) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.store == nil {
		return []logicdomain.PreCheckMemoryCandidate{}, nil
	}
	rows, err := u.store.LoadActiveSessionMemoryNodes(ctx, session)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if len(rows) > u.config.RecentSessionMemories {
		rows = rows[:u.config.RecentSessionMemories]
	}
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := logicdomain.PreCheckMemoryCandidate{
			MemoryID:     row.ID,
			SourceTurnID: row.TurnID,
			SourceKind:   logicdomain.MemorySourceKindLabel(row.SourceKind),
			ScopeLevel:   logicdomain.MemoryScopeLevelLabel(row.ScopeLevel),
			Category:     row.Category,
			Abstract:     strings.TrimSpace(row.Abstract),
			Details:      strings.TrimSpace(row.Details),
			Score:        1,
			Origin:       "recent_session",
		}
		if candidate.Abstract == "" && candidate.Details == "" {
			continue
		}
		if candidate.Details == "" {
			candidate.Details = candidate.Abstract
		}
		out = append(out, candidate)
	}
	return out, nil
}

// searchMemoryCandidates reuses the unified memory-search RPC logic, then keeps only de-duplicated hits above the configured similarity floor.
// searchMemoryCandidates 用于复用统一记忆查询逻辑，然后只保留高于配置相似度下限且去重后的命中结果。
func (u *PreCheckUseCase) searchMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult, recentSession []logicdomain.PreCheckMemoryCandidate) ([]logicdomain.PreCheckMemoryCandidate, error) {
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
	recentIDs := make(map[uint64]struct{}, len(recentSession))
	for _, candidate := range recentSession {
		if candidate.MemoryID > 0 {
			recentIDs[candidate.MemoryID] = struct{}{}
		}
	}
	merged := make(map[uint64]logicdomain.PreCheckMemoryCandidate)
	for _, group := range result.Results {
		for _, hit := range group.Hits {
			if hit.MemoryRef.Type != logicdomain.MemoryRefTypeMemory || hit.MemoryRef.ID == 0 {
				continue
			}
			if hit.Score < u.config.MinSimilarityScore {
				continue
			}
			if _, ok := recentIDs[hit.MemoryRef.ID]; ok {
				continue
			}
			candidate := logicdomain.PreCheckMemoryCandidate{
				MemoryID:     hit.MemoryRef.ID,
				SourceTurnID: hit.SourceRef.ID,
				SourceKind:   logicdomain.MemorySourceKindLabel(hit.SourceKind),
				ScopeLevel:   logicdomain.MemoryScopeLevelLabel(hit.ScopeLevel),
				Category:     hit.Category,
				Abstract:     strings.TrimSpace(hit.Abstract),
				Details:      strings.TrimSpace(hit.DetailsPreview),
				Score:        hit.Score,
				Origin:       "vector_search",
			}
			if candidate.Abstract == "" && candidate.Details == "" {
				continue
			}
			if candidate.Details == "" {
				candidate.Details = candidate.Abstract
			}
			if existing, ok := merged[candidate.MemoryID]; ok {
				if candidate.Score > existing.Score {
					merged[candidate.MemoryID] = candidate
				}
				continue
			}
			merged[candidate.MemoryID] = candidate
		}
	}
	out := make([]logicdomain.PreCheckMemoryCandidate, 0, len(merged))
	for _, candidate := range merged {
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].MemoryID < out[j].MemoryID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > u.config.ReviewCandidateLimit {
		out = out[:u.config.ReviewCandidateLimit]
	}
	return out, nil
}

// reviewMemoryCandidates lets the second-stage reviewer choose a small adopted subset and restores the caller-facing order from the merged candidate map.
// reviewMemoryCandidates 用于让第二层评审器挑出一个小型采纳子集，并从合并候选映射中恢复调用方可用的顺序结果。
func (u *PreCheckUseCase) reviewMemoryCandidates(ctx context.Context, cmd PreCheckCommand, intent logicdomain.IntentResult, recentSession, recalled []logicdomain.PreCheckMemoryCandidate) ([]logicdomain.PreCheckMemoryCandidate, error) {
	if u.reviewer == nil {
		return nil, fmt.Errorf("pre-check memory reviewer is nil")
	}
	review, err := u.reviewer.Review(ctx, logicdomain.PreCheckMemoryReviewInput{
		UserContent:           cmd.UserContent,
		IntentKeywords:        append([]string(nil), intent.Keywords...),
		IntentReason:          intent.Reason,
		RecentSessionMemories: append([]logicdomain.PreCheckMemoryCandidate(nil), recentSession...),
		RetrievedMemories:     append([]logicdomain.PreCheckMemoryCandidate(nil), recalled...),
	})
	if err != nil {
		return nil, err
	}
	candidatesByID := make(map[uint64]logicdomain.PreCheckMemoryCandidate, len(recentSession)+len(recalled))
	for _, candidate := range recentSession {
		candidatesByID[candidate.MemoryID] = candidate
	}
	for _, candidate := range recalled {
		if _, ok := candidatesByID[candidate.MemoryID]; !ok {
			candidatesByID[candidate.MemoryID] = candidate
		}
	}
	selected := make([]logicdomain.PreCheckMemoryCandidate, 0, len(review.SelectedMemoryIDs))
	for _, memoryID := range review.SelectedMemoryIDs {
		if candidate, ok := candidatesByID[memoryID]; ok {
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
	contextText, items, err := u.assemblePreCheckContext(ctx, persona, memoryHits)
	if err != nil {
		return PreCheckResult{}, err
	}
	return PreCheckResult{
		ShouldInject: len(items) > 0,
		ContextText:  contextText,
		ContextItems: items,
		Degraded:     degraded,
		TraceID:      traceID,
	}, nil
}

// assemblePreCheckContext prefers the shared assembler but falls back to a local deterministic renderer if prompt loading degrades.
// assemblePreCheckContext 用于优先使用共享 assembler；若提示词加载降级，则回退到本地确定性渲染。
func (u *PreCheckUseCase) assemblePreCheckContext(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	if u.assembler != nil {
		contextText, items, err := u.assembler.Assemble(ctx, persona, hits)
		if err == nil {
			return contextText, items, nil
		}
		if u.logger != nil {
			u.logger.Warn("pre-check assembler degraded to fallback", "err", err)
		}
	}
	items := buildFallbackContextItems(persona, hits)
	return buildFallbackContextSummary(items), items, nil
}

// buildPreCheckMemoryQueryJSON converts the intent keywords into the grouped memory-search JSON format already consumed by the unified search surface.
// buildPreCheckMemoryQueryJSON 用于把意图关键词转换成统一记忆查询接口已消费的分组 JSON 格式。
func buildPreCheckMemoryQueryJSON(intent logicdomain.IntentResult, userContent string) (string, error) {
	items := make([]MemoryQueryItem, 0, len(intent.Keywords))
	for _, keyword := range intent.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		items = append(items, MemoryQueryItem{
			Background: strings.TrimSpace(userContent),
			Query:      keyword,
		})
	}
	if len(items) == 0 {
		items = append(items, MemoryQueryItem{Query: strings.TrimSpace(userContent)})
	}
	body, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("marshal pre-check query json: %w", err)
	}
	return string(body), nil
}

// buildPreCheckMemoryText turns one adopted candidate into the compact text fragment injected back to the upstream caller.
// buildPreCheckMemoryText 用于把一条已采纳候选转成回注给上游调用方的紧凑文本片段。
func buildPreCheckMemoryText(candidate logicdomain.PreCheckMemoryCandidate) string {
	abstract := strings.TrimSpace(candidate.Abstract)
	details := strings.TrimSpace(candidate.Details)
	switch {
	case abstract == "" && details == "":
		return ""
	case details == "" || details == abstract:
		return abstract
	case abstract == "":
		return details
	default:
		return abstract + "\n" + details
	}
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
			items = append(items, logicdomain.ContextItem{Kind: "memory", Title: "向量召回记忆", Text: text, Source: "vector", Score: hit.Score})
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
		b.WriteString("[")
		b.WriteString(group.Title)
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
