// memory_query.go implements unified memory search, detail lookup, and direct-write flows exposed by the gRPC memory surface.
// memory_query.go 用于实现 gRPC 记忆接口暴露的统一记忆检索、详情查询和主动写入流程。
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

const (
	// defaultMemorySearchTopK keeps the memory-query RPC useful out of the box when callers omit an explicit limit.
	// defaultMemorySearchTopK 用于在调用方未显式指定 limit 时，保证记忆查询 RPC 仍然具备可用的默认返回量。
	defaultMemorySearchTopK = 8

	// maxMemorySearchTopK caps one memory-query RPC so accidental oversized requests do not explode embedding or vector work.
	// maxMemorySearchTopK 用于限制单次记忆查询 RPC 的最大返回量，避免误传超大请求时把 embedding 或向量检索放大。
	maxMemorySearchTopK = 32

	// maxMemoryQueryItems bounds the simple query list size so one request cannot fan out into an unbounded number of vector searches.
	// maxMemoryQueryItems 用于限制简单查询列表的条目数量，避免一次请求扩散成无限制的向量检索。
	maxMemoryQueryItems = 16

	// maxTurnDetailLookup limits how many turn ids one detail query can request at once to keep relational reads bounded.
	// maxTurnDetailLookup 用于限制一次详情查询最多请求多少个 turn id，保持关系读取的规模可控。
	maxTurnDetailLookup = 256

	// maxMemoryDetailLookup limits how many unified memory refs one detail query can request at once.
	// maxMemoryDetailLookup 用于限制一次统一记忆详情查询最多请求多少个引用。
	maxMemoryDetailLookup = 256

	// maxWriteMemoryItems limits one direct-write call so a single tools request cannot fan out into unbounded embedding work.
	// maxWriteMemoryItems 用于限制一次主动写入调用最多能写多少条记忆，避免单次工具调用扩散成无限 embedding 工作量。
	maxWriteMemoryItems = 32

	// turnDetailContextRadius keeps three turns before and after each anchor so callers can continue finer follow-up lookups without fetching entire sessions.
	// turnDetailContextRadius 用于固定返回每个锚点 turn 前后各三轮编号，让调用方无需拉取整条 session 也能继续做更细的后续查询。
	turnDetailContextRadius = 3

	// directMemoryDedupeWindow keeps the soft-idempotency window short enough to block accidental duplicate writes without freezing later updates forever.
	// directMemoryDedupeWindow 用于限定主动写记忆的软幂等时间窗，既阻止误重复写入，又避免长期锁死后续更新。
	directMemoryDedupeWindow = 24 * time.Hour
)

var (
	// defaultSessionMemoryTTL keeps session-scoped direct writes alive long enough for short-term workflows while still allowing idle cleanup later.
	// defaultSessionMemoryTTL 用于让 session 级主动记忆在短期工作流中足够持久，同时仍允许后续 idle 清理。
	defaultSessionMemoryTTL = 15 * 24 * time.Hour

	// defaultProjectMemoryTTL keeps project-scoped direct writes much longer than profile memory by default.
	// defaultProjectMemoryTTL 用于让 project 级主动记忆默认寿命明显长于画像记忆。
	defaultProjectMemoryTTL = 180 * 24 * time.Hour

	// defaultUserMemoryTTL keeps user-scoped direct writes longest by default because they should survive across multiple tasks.
	// defaultUserMemoryTTL 用于让 user 级主动记忆默认拥有最长寿命，因为它们应跨多个任务继续存在。
	defaultUserMemoryTTL = 365 * 24 * time.Hour
)

// MemoryQueryCommand carries one simple query list together with the resolved user/project selectors, one optional scope override, and one internal reviewer-side flag that may widen the first-stage recall pool for hard dedupe.
// MemoryQueryCommand 用于承载一个简单查询字符串列表、解析范围所需的 user/project 选择参数、一个可选作用域覆盖，以及一个供内部 reviewer 链路按需放大首轮召回窗口的 hard dedupe 标记。
type MemoryQueryCommand struct {
	UserID               uint64
	ProjectID            uint64
	SessionID            uint64
	Queries              []string
	TopK                 int
	EnableHardDedupePool bool
	ScopeOverride        string
	BoundarySessionID    uint64
	BoundaryMaxTurnID    uint64
	ExcludeBoundaryTurn  bool
}

// MemoryQueryItem stores one normalized query string before embedding and vector search begin.
// MemoryQueryItem 用于在 embedding 和向量检索开始前保存一条已规范化的查询字符串。
type MemoryQueryItem struct {
	Query string
}

// MemoryQueryHit returns one unified recalled memory candidate together with its durable refs and preview fields.
// MemoryQueryHit 用于返回一条统一召回的记忆候选，以及它的长期引用和预览字段。
type MemoryQueryHit struct {
	MemoryRef                   logicdomain.MemoryRef
	SourceRef                   logicdomain.MemoryRef
	SourceKind                  int
	ScopeLevel                  int
	Priority                    int
	MemoryLevel                 int
	RefreshWeight               int
	SessionID                   uint64
	Abstract                    string
	DetailsPreview              string
	Category                    int
	Score                       float64
	Origin                      string
	Vector                      []float32
	CreatedAt                   time.Time
	LastRecalledAt              time.Time
	LastAdoptedAt               time.Time
	LastReinforcedAt            time.Time
	SupportCount                int
	RebuttalCount               int
	MatchedContextValues        []string
	MatchedContextSupportCount  int
	MatchedContextRebuttalCount int
	MatchedContextScoreDelta    float64
	ReinforcementCount          int
	CrossSessionAdoptedCount    int
	DecayDisabled               bool
}

// MemoryQueryGroupResult returns the echoed normalized query together with the hit list produced for that query.
// MemoryQueryGroupResult 用于返回原样回显的规范化查询，以及针对该查询生成的命中结果。
type MemoryQueryGroupResult struct {
	QueryIndex     int
	Query          string
	QueryVector    []float32
	Hits           []MemoryQueryHit
	HardDedupeHits []MemoryQueryHit
}

// MemoryQueryResult returns the resolved user/project targets plus all grouped recall results after fusion, rerank, and optional diversity control.
// MemoryQueryResult 用于返回已解析的 user/project 目标，以及融合、重排和可选多样性控制后的全部分组召回结果。
type MemoryQueryResult struct {
	UserTarget    logicdomain.ProfileTargetRef
	ProjectTarget logicdomain.ProfileTargetRef
	Results       []MemoryQueryGroupResult
}

// TurnDetailCommand carries one list of turn ids whose dehydrated payload should be loaded back from relational storage.
// TurnDetailCommand 用于承载一组需要从关系存储中回读脱水内容的 turn id。
type TurnDetailCommand struct {
	TurnIDs []uint64
}

// TurnDetailRecord returns one durable turn row together with parsed dialogue fields and neighboring turn ids.
// TurnDetailRecord 用于返回一条长期 turn 行，以及解析后的对话字段和相邻 turn 编号。
type TurnDetailRecord struct {
	Turn             logicdomain.SessionTurnRecord
	UserContent      string
	Timeline         []logicdomain.TurnDetailTimelineItem
	AssistantContent string
	PreviousTurnIDs  []uint64
	NextTurnIDs      []uint64
}

// TurnDetailResult returns the requested turns in caller order together with parsed dialogue fields and neighboring turn ids.
// TurnDetailResult 用于按调用方顺序返回请求的 turn，以及解析后的对话字段和相邻 turn 编号。
type TurnDetailResult struct {
	Turns []TurnDetailRecord
}

// MemoryDetailCommand carries one ordered ref list that may mix unified memory refs and turn refs.
// MemoryDetailCommand 用于承载一个有序引用列表，列表中可混合统一记忆引用和 turn 引用。
type MemoryDetailCommand struct {
	Refs []logicdomain.MemoryRef
}

// MemoryDetailItem returns either one unified memory record or one turn detail record according to the requested ref type.
// MemoryDetailItem 用于按请求引用类型返回统一记忆记录或 turn 详情记录中的一种。
type MemoryDetailItem struct {
	Ref    logicdomain.MemoryRef
	Memory *logicdomain.MemoryNodeRecord
	Turn   *TurnDetailRecord
}

// MemoryDetailResult returns the requested mixed refs in caller order.
// MemoryDetailResult 用于按调用方顺序返回请求的混合引用详情。
type MemoryDetailResult struct {
	Items []MemoryDetailItem
}

// WriteMemoryItem stores one direct-write candidate before soft idempotency, embedding, and relational persistence begin.
// WriteMemoryItem 用于保存一条主动写入候选，在软幂等、embedding 和关系持久化开始前使用。
type WriteMemoryItem struct {
	ScopeLevel  int
	Abstract    string
	Details     string
	Category    int
	Priority    int
	MemoryLevel int
	ExpiresAt   time.Time
}

// WriteMemoriesCommand carries one resolved session scope plus the direct memory items that should be persisted immediately.
// WriteMemoriesCommand 用于承载一个已解析 session 范围，以及需要立刻持久化的主动记忆条目。
type WriteMemoriesCommand struct {
	Session logicdomain.SessionRef
	Items   []WriteMemoryItem
}

// WriteMemoryResultItem returns the ref and write mode for one accepted direct memory item.
// WriteMemoryResultItem 用于返回一条已接受主动记忆项的引用和写入模式。
type WriteMemoryResultItem struct {
	Ref        logicdomain.MemoryRef
	SourceKind int
	ScopeLevel int
	Deduped    bool
}

// WriteMemoriesResult returns the ordered direct-write results for the caller.
// WriteMemoriesResult 用于按调用方顺序返回主动写记忆结果。
type WriteMemoriesResult struct {
	Items []WriteMemoryResultItem
}

// MemoryExecutor groups the active memory-search, detail lookup, and direct-write flows exposed by the inbound gRPC adapter.
// MemoryExecutor 用于聚合入站 gRPC 适配层对外暴露的主动记忆检索、详情查询和直接写入流程。
type MemoryExecutor interface {
	Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error)
	GetTurns(ctx context.Context, cmd TurnDetailCommand) (TurnDetailResult, error)
	GetDetails(ctx context.Context, cmd MemoryDetailCommand) (MemoryDetailResult, error)
	Write(ctx context.Context, cmd WriteMemoriesCommand) (WriteMemoriesResult, error)
}

// hybridVectorSearchStore defines the optional combined-store fast path that can fuse vector and lexical candidates inside one SQL query before the rest of the ranking pipeline runs.
// hybridVectorSearchStore 用于定义组合库可选的快速路径，让向量与 lexical 候选在单条 SQL 内先完成融合，再进入后续排序流水线。
type hybridVectorSearchStore interface {
	SearchHybridMemory(ctx context.Context, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) ([]logicdomain.MemoryHit, error)
}

// MemoryUseCase orchestrates grouped memory search, mixed detail lookup, and direct AI memory writes on top of profile-target resolution and unified relational memory rows.
// MemoryUseCase 用于在画像目标解析和统一关系记忆行之上，编排分组记忆检索、混合详情查询以及 AI 主动写记忆流程。
type MemoryUseCase struct {
	profiles                               appports.ProfileStore
	memories                               appports.MemoryStore
	embedding                              appports.EmbeddingClient
	candidateReviewer                      PostActionCandidateReviewer
	memoryReplaceScope                     string
	memoryReplaceTopK                      int
	memoryReplaceMinSimilarity             float64
	memoryReplaceHardDedupeCosineThreshold float64
	hardDedupePoolTopK                     int
	memoryReplaceConfigured                bool
	hybridEnabled                          bool
	lexicalTopK                            int
	rrfK                                   int
	mmrEnabled                             bool
	mmrLambda                              float64
	weibullEnabled                         bool
	weibullShape                           float64
	weibullScaleHours                      float64
	weibullMinMultiplier                   float64
	weibullReinforceWeight                 float64
	weibullCrossSessionBoost               float64
	reranker                               appports.RerankerClient
	rerankTopN                             int
	vector                                 appports.VectorStore
	logger                                 *logx.Logger
	piiScrubber                            PIIScrubber
}

// directMemoryWriteApplier is the optional store fast path that atomically inserts one direct-write memory row and retires any replaced old rows in the same SQL transaction.
// directMemoryWriteApplier 用于描述一个可选的存储快路径，在同一 SQL 事务里原子写入主动记忆，并退役它替代的旧记忆行。
type directMemoryWriteApplier interface {
	ApplyDirectMemoryWrite(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error)
}

// NewMemoryUseCase creates a MemoryUseCase instance.
// NewMemoryUseCase 用于创建 MemoryUseCase 实例。
func NewMemoryUseCase(profiles appports.ProfileStore, memories appports.MemoryStore, embedding appports.EmbeddingClient, vector appports.VectorStore, logger *logx.Logger) *MemoryUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &MemoryUseCase{
		profiles:                               profiles,
		memories:                               memories,
		embedding:                              embedding,
		memoryReplaceScope:                     memoryReplaceScopeProject,
		memoryReplaceTopK:                      defaultMemorySearchTopK,
		memoryReplaceMinSimilarity:             0.80,
		memoryReplaceHardDedupeCosineThreshold: 0.99,
		hardDedupePoolTopK:                     16,
		memoryReplaceConfigured:                false,
		lexicalTopK:                            defaultMemorySearchTopK,
		rrfK:                                   60,
		mmrLambda:                              0.75,
		weibullShape:                           1.35,
		weibullScaleHours:                      2160,
		weibullMinMultiplier:                   0.4,
		weibullReinforceWeight:                 0.18,
		weibullCrossSessionBoost:               0.12,
		rerankTopN:                             defaultMemorySearchTopK,
		vector:                                 vector,
		logger:                                 logger,
	}
}

// ConfigurePIIScrubber injects the shared PII scrubber used to redact direct-write memory payloads before the write flow performs dedupe, embedding, and durable persistence.
// ConfigurePIIScrubber 用于注入共享 PII 脱敏器，让主动写记忆在进入去重、embedding 与长期持久化流程前先完成脱敏。
func (u *MemoryUseCase) ConfigurePIIScrubber(scrubber PIIScrubber) {
	if u == nil {
		return
	}
	u.piiScrubber = scrubber
}

// ConfigureHybrid attaches the lexical-recall and RRF knobs used by the mixed retrieval pipeline.
// ConfigureHybrid 用于挂载混合检索流水线使用的 lexical 召回和 RRF 参数。
func (u *MemoryUseCase) ConfigureHybrid(enabled bool, lexicalTopK, rrfK int) {
	if u == nil {
		return
	}
	u.hybridEnabled = enabled
	if lexicalTopK <= 0 {
		lexicalTopK = defaultMemorySearchTopK
	}
	if lexicalTopK > maxMemorySearchTopK {
		lexicalTopK = maxMemorySearchTopK
	}
	if rrfK <= 0 {
		rrfK = 60
	}
	u.lexicalTopK = lexicalTopK
	u.rrfK = rrfK
}

// ConfigureMMR attaches the optional diversity-control step used after fusion/rerank so near-duplicate candidates stop crowding out broader context.
// ConfigureMMR 用于挂载融合或 rerank 之后的可选多样性控制步骤，避免近重复候选挤占更广的上下文信息。
func (u *MemoryUseCase) ConfigureMMR(enabled bool, lambda float64) {
	if u == nil {
		return
	}
	u.mmrEnabled = enabled
	if lambda <= 0 || lambda > 1 {
		lambda = 0.75
	}
	u.mmrLambda = lambda
}

// ConfigureDecay attaches the optional Weibull read-time decay model so older, weakly reinforced memories lose rank before the final top-k is chosen.
// ConfigureDecay 用于挂载可选的 Weibull 读时衰减模型，让更旧且强化较弱的记忆在最终 top-k 生成前自然降权。
func (u *MemoryUseCase) ConfigureDecay(enabled bool, shape, scaleHours, minMultiplier, reinforceWeight, crossSessionBoost float64) {
	if u == nil {
		return
	}
	u.weibullEnabled = enabled
	if shape <= 0 {
		shape = 1.35
	}
	if scaleHours <= 0 {
		scaleHours = 2160
	}
	if minMultiplier < 0 || minMultiplier > 1 {
		minMultiplier = 0.4
	}
	if reinforceWeight < 0 {
		reinforceWeight = 0.18
	}
	if crossSessionBoost < 0 {
		crossSessionBoost = 0.12
	}
	u.weibullShape = shape
	u.weibullScaleHours = scaleHours
	u.weibullMinMultiplier = minMultiplier
	u.weibullReinforceWeight = reinforceWeight
	u.weibullCrossSessionBoost = crossSessionBoost
}

// ConfigureRerank attaches one optional rerank backend and caps how many first-stage hits each query group may send into it.
// ConfigureRerank 用于挂载可选的重排序后端，并限制每个 query group 最多向其发送多少首轮命中结果。
func (u *MemoryUseCase) ConfigureRerank(reranker appports.RerankerClient, topN int) {
	if u == nil {
		return
	}
	u.reranker = reranker
	if topN <= 0 {
		topN = defaultMemorySearchTopK
	}
	if topN > maxMemorySearchTopK {
		topN = maxMemorySearchTopK
	}
	u.rerankTopN = topN
}

// ConfigureHardDedupePoolTopK sets the dedicated pre-review candidate window used by hard dedupe so it can inspect a slightly larger pre-MMR pool without inflating reviewer prompts.
// ConfigureHardDedupePoolTopK 用于设置硬排重专用的 reviewer 前候选窗口，让它可以扫描更大的 MMR 之前候选池，同时不放大 reviewer 提示词。
func (u *MemoryUseCase) ConfigureHardDedupePoolTopK(topK int) {
	if u == nil {
		return
	}
	if topK <= 0 {
		topK = 16
	}
	if topK > maxMemorySearchTopK {
		topK = maxMemorySearchTopK
	}
	u.hardDedupePoolTopK = topK
}

// ConfigureMemoryReplace attaches the optional semantic replacement reviewer and its recall knobs so direct writes can converge with post-action memory replacement decisions.
// ConfigureMemoryReplace 用于挂载可选的语义替代 reviewer 及其召回参数，让主动写记忆与 post-action 共享同一套更替决策语义。
func (u *MemoryUseCase) ConfigureMemoryReplace(reviewer PostActionCandidateReviewer, topK int, scope string, minSimilarity float64, hardDedupeCosineThreshold float64) {
	if u == nil {
		return
	}
	u.memoryReplaceConfigured = true
	u.candidateReviewer = reviewer
	if topK <= 0 {
		topK = defaultMemorySearchTopK
	}
	if topK > maxMemorySearchTopK {
		topK = maxMemorySearchTopK
	}
	u.memoryReplaceTopK = topK
	u.memoryReplaceScope = normalizeMemoryReplaceScope(scope)
	if minSimilarity < 0 || minSimilarity > 1 {
		minSimilarity = 0.80
	}
	u.memoryReplaceMinSimilarity = minSimilarity
	if hardDedupeCosineThreshold < 0 || hardDedupeCosineThreshold > 1 {
		hardDedupeCosineThreshold = 0.99
	}
	u.memoryReplaceHardDedupeCosineThreshold = hardDedupeCosineThreshold
}

// Search resolves the concrete project/user scope, normalizes the simple query list, runs the configured retrieval stages, and returns enriched unified memory refs.
// Search 用于解析具体的 project/user 范围、规范化简单查询列表、执行已配置的检索阶段，并返回补全后的统一记忆引用。
func (u *MemoryUseCase) Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	if u == nil || u.profiles == nil {
		return MemoryQueryResult{}, fmt.Errorf("profile store is nil")
	}
	if u.memories == nil {
		return MemoryQueryResult{}, fmt.Errorf("memory store is nil")
	}
	if u.embedding == nil {
		return MemoryQueryResult{}, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return MemoryQueryResult{}, fmt.Errorf("vector store is nil")
	}
	if err := validateMemoryQueryCommand(cmd); err != nil {
		return MemoryQueryResult{}, err
	}

	// Resolve USER and PROJECT first so the memory search always runs inside one deterministic hierarchy scope.
	// 先解析 USER 和 PROJECT，保证记忆检索始终运行在一个确定性的层级范围内。
	userTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeUser, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return MemoryQueryResult{}, err
	}
	projectTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeProject, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return MemoryQueryResult{}, err
	}

	// Normalize the query list once, then collapse repeated variants so one request can reuse retrieval work without exposing duplicate pipeline cost to callers.
	// 先一次性规范化查询列表，再折叠重复变体，让单次请求可以复用检索工作，而不会把重复成本暴露给调用方。
	items, err := normalizeMemoryQueries(cmd.Queries)
	if err != nil {
		return MemoryQueryResult{}, err
	}
	keys := make([]string, len(items))
	uniqueItems := make([]MemoryQueryItem, 0, len(items))
	uniqueKeys := make([]string, 0, len(items))
	seenGroups := make(map[string]struct{}, len(items))
	for idx, item := range items {
		key := buildMemoryQueryCacheKey(item)
		keys[idx] = key
		if _, ok := seenGroups[key]; ok {
			continue
		}
		seenGroups[key] = struct{}{}
		uniqueKeys = append(uniqueKeys, key)
		uniqueItems = append(uniqueItems, item)
	}
	texts := make([]string, 0, len(uniqueItems))
	for _, item := range uniqueItems {
		texts = append(texts, buildMemorySearchText(item))
	}
	embedResp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Texts: texts})
	if err != nil {
		return MemoryQueryResult{}, err
	}
	if err := embedResp.ValidateStrict(len(uniqueItems)); err != nil {
		return MemoryQueryResult{}, err
	}

	// Keep vector recall inside the resolved project hierarchy and enrich the returned vector rows with relational memory refs.
	// 将向量召回限制在已解析项目层级内，并使用关系记忆行补全向量结果中的长期引用。
	filter := buildScopedMemorySearchFilter(userTarget, projectTarget, cmd.ScopeOverride)
	if cmd.SessionID > 0 {
		filter.SessionID = cmd.SessionID
	}
	filter.BoundarySessionID = cmd.BoundarySessionID
	filter.BoundaryMaxTurnID = cmd.BoundaryMaxTurnID
	filter.ExcludeBoundaryTurn = cmd.ExcludeBoundaryTurn
	topK := normalizeMemorySearchTopK(cmd.TopK)
	candidatePoolK := topK
	if u.hybridEnabled || u.reranker != nil || u.mmrEnabled {
		lexicalTopK := 0
		if u.hybridEnabled {
			lexicalTopK = u.lexicalTopK
		}
		rerankTopN := 0
		if u.reranker != nil {
			rerankTopN = u.rerankTopN
		}
		candidatePoolK = normalizeMemoryCandidatePoolK(topK, lexicalTopK, rerankTopN, u.mmrEnabled)
	}
	if cmd.EnableHardDedupePool {
		hardDedupeCandidatePoolK := u.configuredHardDedupePoolTopK(topK)
		if hardDedupeCandidatePoolK > candidatePoolK {
			candidatePoolK = hardDedupeCandidatePoolK
		}
	}
	cachedHits := make(map[string][]MemoryQueryHit, len(uniqueItems))
	cachedHardDedupeHits := make(map[string][]MemoryQueryHit, len(uniqueItems))
	cachedQueryVectors := make(map[string][]float32, len(uniqueItems))
	for idx, item := range uniqueItems {
		queryText := buildMemorySearchText(item)
		hits, usedCombinedHybridSQL, err := u.searchMemoryFirstStage(ctx, item, embedResp.Vectors[idx], candidatePoolK, filter)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		if usedCombinedHybridSQL {
			hits = normalizeCombinedHybridSQLHits(hits)
		}
		mapped, err := u.mapSearchHits(ctx, hits)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		mapped = clampMemoryQueryHitScores(mapped)
		stageMessage := "memory search vector stage completed"
		if usedCombinedHybridSQL {
			stageMessage = "memory search first-stage combined sql completed"
		}
		u.logMemorySearchStage(ctx, stageMessage, queryText, []any{
			"query_index", idx,
			"candidate_pool_k", candidatePoolK,
			"vector_hit_count", len(hits),
			"mapped_hit_count", len(mapped),
			"combined_hybrid_sql_used", usedCombinedHybridSQL,
			"hybrid_enabled", u.hybridEnabled,
			"rerank_enabled", u.reranker != nil,
			"mmr_enabled", u.mmrEnabled,
		}, map[string]any{
			"query_index":    idx,
			"query":          item.Query,
			"top_vector_hit": summarizeRawMemoryHitForLog(firstRawMemoryHit(hits)),
			"top_mapped_hit": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(mapped)),
		})
		if usedCombinedHybridSQL {
			mapped = trimSearchHits(mapped, candidatePoolK)
		} else {
			mapped = u.hybridizeSearchHits(ctx, item, filter, candidatePoolK, mapped)
		}
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.rerankSearchHits(ctx, queryText, mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.applyWeibullDecaySearchHits(mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.applyContextEvidenceScoring(ctx, item, mapped)
		mapped = clampMemoryQueryHitScores(mapped)

		// Capture a dedicated hard-dedupe window before MMR and final top-k trimming so the duplicate shortcut can still inspect near-identical memories that diversity control intentionally pushes out of reviewer-visible results.
		// 在 MMR 和最终 top-k 截断之前截取一份独立的硬排重窗口，这样即便多样性控制故意把近重复旧记忆挤出 reviewer 可见结果，硬排重捷径仍能扫描到它们。
		hardDedupePoolTopK := topK
		if cmd.EnableHardDedupePool {
			hardDedupePoolTopK = u.effectiveHardDedupePoolTopK(topK, candidatePoolK)
		}
		hardDedupeHits := trimSearchHits(mapped, hardDedupePoolTopK)

		mapped = u.applyMMRSearchHits(ctx, topK, mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		u.logMemorySearchStage(ctx, "memory search final stage completed", queryText, []any{
			"query_index", idx,
			"final_hit_count", len(mapped),
			"top_k", topK,
			"hard_dedupe_pool_top_k", hardDedupePoolTopK,
			"hard_dedupe_pool_hit_count", len(hardDedupeHits),
		}, map[string]any{
			"query_index":         idx,
			"query":               item.Query,
			"top_final_hit":       summarizeMemoryQueryHitForLog(firstMemoryQueryHit(mapped)),
			"top_hard_dedupe_hit": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(hardDedupeHits)),
		})
		cachedHits[uniqueKeys[idx]] = cloneMemoryQueryHits(trimSearchHits(mapped, topK))
		cachedHardDedupeHits[uniqueKeys[idx]] = cloneMemoryQueryHits(hardDedupeHits)
		cachedQueryVectors[uniqueKeys[idx]] = cloneFloat32Slice(embedResp.Vectors[idx])
	}
	results := make([]MemoryQueryGroupResult, 0, len(items))
	for idx, item := range items {
		results = append(results, MemoryQueryGroupResult{
			QueryIndex:     idx,
			Query:          item.Query,
			QueryVector:    cloneFloat32Slice(cachedQueryVectors[keys[idx]]),
			Hits:           cloneMemoryQueryHits(cachedHits[keys[idx]]),
			HardDedupeHits: cloneMemoryQueryHits(cachedHardDedupeHits[keys[idx]]),
		})
	}
	return MemoryQueryResult{
		UserTarget:    userTarget,
		ProjectTarget: projectTarget,
		Results:       results,
	}, nil
}

// configuredHardDedupePoolTopK returns the configured reviewer-front hard-dedupe window after clamping it into the shared search top-k bounds and keeping it no smaller than the reviewer-visible top-k.
// configuredHardDedupePoolTopK 用于返回配置后的 reviewer 前硬排重窗口；它会被钳制到共享搜索上限内，并保证不会小于 reviewer 可见的 top-k。
func (u *MemoryUseCase) configuredHardDedupePoolTopK(topK int) int {
	poolTopK := u.hardDedupePoolTopK
	if poolTopK <= 0 {
		poolTopK = 16
	}
	if poolTopK < topK {
		poolTopK = topK
	}
	return normalizeMemorySearchTopK(poolTopK)
}

// effectiveHardDedupePoolTopK expands the reviewer top-k into a larger but bounded pre-review scan window so hard dedupe can inspect more durable memories than the reviewer sees, without exceeding the first-stage candidate pool.
// effectiveHardDedupePoolTopK 用于把 reviewer 的 top-k 扩展成一个更大但受限的 reviewer 前扫描窗口，让硬排重能查看比 reviewer 更多的长期记忆，同时又不超过一阶段候选池。
func (u *MemoryUseCase) effectiveHardDedupePoolTopK(topK, candidatePoolK int) int {
	poolTopK := u.configuredHardDedupePoolTopK(topK)
	if candidatePoolK > 0 && poolTopK > candidatePoolK {
		poolTopK = candidatePoolK
	}
	return normalizeMemorySearchTopK(poolTopK)
}

// searchMemoryFirstStage selects the fastest safe first-stage retrieval path, preferring one SQL-level hybrid fusion query when the backing vector store explicitly supports combined PostgreSQL search.
// searchMemoryFirstStage 用于选择最快且安全的一阶段召回路径；当向量存储显式支持 PostgreSQL 组合检索时，优先走单条 SQL 的混合融合查询。
func (u *MemoryUseCase) searchMemoryFirstStage(ctx context.Context, item MemoryQueryItem, vector []float32, poolK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, bool, error) {
	if u == nil || u.vector == nil {
		return nil, false, fmt.Errorf("vector store is nil")
	}
	query := buildMemoryLexicalQuery(item)
	if u.hybridEnabled && strings.TrimSpace(query) != "" {
		if hybridStore, ok := u.vector.(hybridVectorSearchStore); ok {
			hits, err := hybridStore.SearchHybridMemory(ctx, query, vector, poolK, filter, u.rrfK)
			if err == nil {
				return hits, true, nil
			}
			if u.logger != nil {
				u.logger.Warn("memory combined hybrid search degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
			}
		}
	}
	hits, err := u.vector.Search(ctx, vector, poolK, filter)
	return hits, false, err
}

// GetTurns loads the requested dehydrated turn rows and reorders them to match the caller-supplied turn id sequence.
// GetTurns 用于读取请求的脱水 turn 行，并按调用方传入的 turn id 顺序重新排序返回。
func (u *MemoryUseCase) GetTurns(ctx context.Context, cmd TurnDetailCommand) (TurnDetailResult, error) {
	if u == nil || u.memories == nil {
		return TurnDetailResult{}, fmt.Errorf("memory store is nil")
	}
	if err := validateTurnDetailCommand(cmd); err != nil {
		return TurnDetailResult{}, err
	}
	return u.loadTurnDetails(ctx, normalizeTurnIDList(cmd.TurnIDs))
}

// GetDetails resolves one mixed ref list into ordered memory-detail and turn-detail payloads.
// GetDetails 用于把一组混合引用解析成有序的记忆详情和 turn 详情载荷。
func (u *MemoryUseCase) GetDetails(ctx context.Context, cmd MemoryDetailCommand) (MemoryDetailResult, error) {
	if u == nil || u.memories == nil {
		return MemoryDetailResult{}, fmt.Errorf("memory store is nil")
	}
	if err := validateMemoryDetailCommand(cmd); err != nil {
		return MemoryDetailResult{}, err
	}

	// Group refs by kind so the relational store can batch load memory rows and turn rows separately, then restore caller order afterward.
	// 按类型把引用分组，让关系存储分别批量读取 memory 行和 turn 行，再在最后恢复调用方顺序。
	refs := normalizeMemoryRefs(cmd.Refs)
	memoryIDs := make([]uint64, 0, len(refs))
	turnIDs := make([]uint64, 0, len(refs))
	for _, ref := range refs {
		switch ref.Type {
		case logicdomain.MemoryRefTypeMemory:
			memoryIDs = append(memoryIDs, ref.ID)
		case logicdomain.MemoryRefTypeTurn:
			turnIDs = append(turnIDs, ref.ID)
		}
	}

	memoryRows := map[uint64]logicdomain.MemoryNodeRecord{}
	if len(memoryIDs) > 0 {
		rows, err := u.memories.LoadMemoryNodesByIDs(ctx, memoryIDs)
		if err != nil {
			return MemoryDetailResult{}, err
		}
		for _, row := range rows {
			memoryRows[row.ID] = row
		}
	}

	turnRows := map[uint64]TurnDetailRecord{}
	if len(turnIDs) > 0 {
		loaded, err := u.loadTurnDetails(ctx, turnIDs)
		if err != nil {
			return MemoryDetailResult{}, err
		}
		for _, row := range loaded.Turns {
			turnRows[row.Turn.ID] = row
		}
	}

	items := make([]MemoryDetailItem, 0, len(refs))
	for _, ref := range refs {
		item := MemoryDetailItem{Ref: ref}
		switch ref.Type {
		case logicdomain.MemoryRefTypeMemory:
			if row, ok := memoryRows[ref.ID]; ok {
				copied := row
				item.Memory = &copied
			}
		case logicdomain.MemoryRefTypeTurn:
			if row, ok := turnRows[ref.ID]; ok {
				copied := row
				item.Turn = &copied
			}
		}
		items = append(items, item)
	}
	return MemoryDetailResult{Items: items}, nil
}

// Write validates direct memory items, applies soft idempotency, embeds new rows, writes vectors, and persists unified memory records.
// Write 用于校验主动记忆条目、执行软幂等、向量化新记录、写入向量，并持久化统一记忆行。
func (u *MemoryUseCase) Write(ctx context.Context, cmd WriteMemoriesCommand) (WriteMemoriesResult, error) {
	if u == nil || u.memories == nil {
		return WriteMemoriesResult{}, fmt.Errorf("memory store is nil")
	}
	if u.embedding == nil {
		return WriteMemoriesResult{}, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return WriteMemoriesResult{}, fmt.Errorf("vector store is nil")
	}
	cmd.Items = scrubWriteMemoryItemsPII(u.piiScrubber, cmd.Items)
	if err := validateWriteMemoriesCommand(cmd); err != nil {
		return WriteMemoriesResult{}, err
	}

	// Normalize defaults item-by-item so direct-write callers can omit optional lifecycle fields without triggering a second reviewer model call.
	// 逐条补齐默认值，让主动写记忆的调用方可以省略可选生命周期字段，而无需再触发第二个评审模型。
	now := time.Now().UTC()
	items := make([]WriteMemoryItem, 0, len(cmd.Items))
	for _, item := range cmd.Items {
		items = append(items, normalizeWriteMemoryItem(item, now))
	}

	results := make([]WriteMemoryResultItem, len(items))
	pending := make([]directWritePendingItem, 0, len(items))
	pendingByCollapseKey := make(map[string]int, len(items))
	for idx, item := range items {
		existing, ok, dedupeHash, err := u.resolveRecentDirectWriteSoftDedupe(ctx, cmd.Session, item, now)
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		if ok {
			results[idx] = WriteMemoryResultItem{
				Ref: logicdomain.MemoryRef{
					Type: logicdomain.MemoryRefTypeMemory,
					ID:   existing.ID,
				},
				SourceKind: existing.SourceKind,
				ScopeLevel: existing.ScopeLevel,
				Deduped:    true,
			}
			continue
		}
		collapseKey := buildDirectMemoryRequestCollapseKey(item)
		if pendingIdx, duplicated := pendingByCollapseKey[collapseKey]; duplicated {
			pending[pendingIdx].OriginalIndexes = append(pending[pendingIdx].OriginalIndexes, idx)
			continue
		}
		pendingByCollapseKey[collapseKey] = len(pending)
		pending = append(pending, directWritePendingItem{
			OriginalIndexes: []int{idx},
			Item:            item,
			DedupeHash:      dedupeHash,
		})
	}
	if len(pending) == 0 {
		return WriteMemoriesResult{Items: results}, nil
	}

	decisions, err := u.reviewDirectWriteMemoryCandidates(ctx, cmd.Session, pending)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	dedupedExistingRows, err := u.loadDirectWriteDedupedExistingRows(ctx, decisions)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	createdVectorsByPendingIndex, err := u.prepareDirectWriteCreateVectors(ctx, cmd.Session, pending, decisions, dedupedExistingRows)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	for idx, pendingItem := range pending {
		decision := decisions[idx]
		if decision.DedupedExistingMemoryID > 0 {
			if existing, ok := dedupedExistingRows[decision.DedupedExistingMemoryID]; ok {
				assignDirectWriteResultItems(results, pendingItem.OriginalIndexes, WriteMemoryResultItem{
					Ref: logicdomain.MemoryRef{
						Type: logicdomain.MemoryRefTypeMemory,
						ID:   existing.ID,
					},
					SourceKind: existing.SourceKind,
					ScopeLevel: existing.ScopeLevel,
					Deduped:    true,
				})
				continue
			}
		}

		created, err := u.persistDirectWriteMemory(ctx, cmd.Session, pendingItem.Item, pendingItem.DedupeHash, decision.SupersedeMemoryIDs, now, createdVectorsByPendingIndex[idx])
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		assignDirectWriteResultItems(results, pendingItem.OriginalIndexes, created)
	}
	return WriteMemoriesResult{Items: results}, nil
}

// resolveRecentDirectWriteSoftDedupe evaluates the short-window soft-idempotency path for one direct-write item, preferring the current semantic hash while keeping a tightly validated legacy-hash fallback during the upgrade window.
// resolveRecentDirectWriteSoftDedupe 用于为单条主动写记忆解析短窗口软幂等路径：优先命中当前语义哈希，同时在升级窗口内保留经过严格校验的旧哈希兼容回退。
func (u *MemoryUseCase) resolveRecentDirectWriteSoftDedupe(ctx context.Context, session logicdomain.SessionRef, item WriteMemoryItem, now time.Time) (logicdomain.MemoryNodeRecord, bool, string, error) {
	currentHash := buildDirectMemoryDedupeHash(session, item, now)
	existing, ok, err := u.memories.FindRecentActiveMemoryByDedupe(
		ctx,
		session,
		logicdomain.MemorySourceKindGRPCAIWrite,
		item.ScopeLevel,
		currentHash,
		now.Add(-directMemoryDedupeWindow),
	)
	if err != nil || ok {
		return existing, ok, currentHash, err
	}

	legacyHash := buildLegacyDirectMemoryDedupeHash(session, item)
	if legacyHash == currentHash {
		return logicdomain.MemoryNodeRecord{}, false, currentHash, nil
	}
	existing, ok, err = u.memories.FindRecentActiveMemoryByDedupe(
		ctx,
		session,
		logicdomain.MemorySourceKindGRPCAIWrite,
		item.ScopeLevel,
		legacyHash,
		now.Add(-directMemoryDedupeWindow),
	)
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, false, currentHash, err
	}
	if !ok || !directWriteSoftDedupeMatchesMemoryRow(existing, item, now) {
		return logicdomain.MemoryNodeRecord{}, false, currentHash, nil
	}
	return existing, true, currentHash, nil
}

// directWritePendingItem stores one post-soft-dedupe direct-write item together with every caller position that collapsed into the same in-request write and its stable short-window dedupe hash.
// directWritePendingItem 用于保存一条经过软幂等筛选后仍待处理的主动写入项，并记录同一请求内被折叠到这次写入上的全部原始位置，以及稳定的短窗口去重哈希。
type directWritePendingItem struct {
	OriginalIndexes []int
	Item            WriteMemoryItem
	DedupeHash      string
}

// directWriteMemoryDecision stores the final semantic-replacement decision for one pending direct-write candidate after reviewer judgment plus safe fallback normalization.
// directWriteMemoryDecision 用于保存一条待写主动记忆在 reviewer 判断和安全回退规范化之后的最终语义替代决策。
type directWriteMemoryDecision struct {
	DedupedExistingMemoryID uint64
	SupersedeMemoryIDs      []uint64
}

// reviewDirectWriteMemoryCandidates optionally runs the unified reviewer over pending direct-write items so explicit tool writes can converge with post-action semantic replacement rules.
// reviewDirectWriteMemoryCandidates 用于按需对待写主动记忆执行统一 reviewer，让工具显式写入也能与 post-action 共享同一套语义替代规则。
func (u *MemoryUseCase) reviewDirectWriteMemoryCandidates(ctx context.Context, session logicdomain.SessionRef, pending []directWritePendingItem) ([]directWriteMemoryDecision, error) {
	if len(pending) == 0 {
		return nil, nil
	}
	if u == nil {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}
	// Only the app/runtime wiring should opt direct-write flows into shared semantic replacement; bare unit tests and lightweight callers keep the historical no-extra-search behavior until ConfigureMemoryReplace is invoked.
	// 只有应用装配层显式启用后，主动写记忆才进入共享语义替代链路；裸构造的单测和轻量调用方在 ConfigureMemoryReplace 被调用前继续保持历史上的“无额外检索”行为。
	if !u.memoryReplaceConfigured {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}
	if u.candidateReviewer == nil && u.memoryReplaceHardDedupeCosineThreshold <= 0 {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}

	// Reuse the same review-candidate shape as post-action so explicit tool writes and asynchronous turn extraction do not drift onto competing dedupe semantics.
	// 复用与 post-action 相同的评审候选结构，避免显式工具写入和异步 turn 提炼各自演化出两套不同的去重语义。
	nodes := make([]logicdomain.MemoryNodeCandidate, 0, len(pending))
	for _, item := range pending {
		nodes = append(nodes, logicdomain.MemoryNodeCandidate{
			Category:       item.Item.Category,
			Abstract:       item.Item.Abstract,
			Details:        item.Item.Details,
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		})
	}
	reviewBuild, err := buildScopedMemoryReviewCandidates(
		ctx,
		u,
		session,
		nodes,
		u.memoryReplaceTopK,
		u.memoryReplaceScope,
		u.memoryReplaceMinSimilarity,
		u.memoryReplaceHardDedupeCosineThreshold,
	)
	if err != nil {
		return nil, err
	}
	if u.candidateReviewer == nil {
		return buildDirectWriteFallbackDecisionsWithHardDedupe(len(pending), reviewBuild.HardDropped), nil
	}
	reviewPartition := partitionMemoryReviewCandidatesForReviewer(reviewBuild.Candidates, reviewBuild.HardDropped)
	if len(reviewPartition.ReviewerCandidates) == 0 {
		return buildDirectWriteFallbackDecisionsWithHardDedupe(len(pending), reviewBuild.HardDropped), nil
	}
	reviewed, err := u.candidateReviewer.Review(ctx, logicdomain.PostActionCandidateReviewInput{
		UserInputKind:    logicdomain.TurnAnalysisUserInputStatement,
		MemoryCandidates: reviewPartition.ReviewerCandidates,
	})
	if err != nil {
		return nil, err
	}
	remappedSection, err := remapPostActionMemoryReviewSectionToOriginal(reviewed.Memory, reviewPartition.ReviewerToOriginal)
	if err != nil {
		return nil, err
	}
	fullSection, err := mergePostActionMemoryReviewSectionWithHardDropped(len(reviewBuild.Candidates), remappedSection, reviewBuild.HardDropped)
	if err != nil {
		return nil, err
	}
	return buildDirectWriteMemoryDecisions(reviewBuild.Candidates, fullSection, reviewBuild.HardDropped)
}

// buildDirectWriteFallbackDecisions degrades pending direct writes into unconditional persistence when semantic replacement review is unavailable.
// buildDirectWriteFallbackDecisions 用于在语义替代评审不可用时，把待写主动记忆退化为全部直接持久化。
func buildDirectWriteFallbackDecisions(count int) []directWriteMemoryDecision {
	if count <= 0 {
		return nil
	}
	return make([]directWriteMemoryDecision, count)
}

// buildDirectWriteFallbackDecisionsWithHardDedupe keeps direct-write fallback behavior conservative while still honoring any local hard-dedupe matches discovered before reviewer execution.
// buildDirectWriteFallbackDecisionsWithHardDedupe 用于在 direct-write 走回退路径时保持保守新建策略，同时保留 reviewer 前本地硬排重已发现的 dedupe 命中。
func buildDirectWriteFallbackDecisionsWithHardDedupe(count int, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) []directWriteMemoryDecision {
	decisions := buildDirectWriteFallbackDecisions(count)
	for idx, dropped := range hardDropped {
		if idx < 0 || idx >= len(decisions) {
			continue
		}
		decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dropped.DedupeMemoryID}
	}
	return decisions
}

// buildDirectWriteMemoryDecisions converts the shared reviewer result into direct-write actions, while allowing locally proven hard-dedupe drops to bypass the reviewer-only visibility validation that exists solely for LLM-originated dedupe ids.
// buildDirectWriteMemoryDecisions 用于把共享 reviewer 结果转换成主动写入动作；同时允许本地已证实的 hard dedupe 丢弃结果跳过 reviewer 专属的可见性校验，因为该校验只适用于 LLM 产出的 dedupe id。
func buildDirectWriteMemoryDecisions(candidates []logicdomain.PostActionMemoryReviewCandidate, section *logicdomain.PostActionMemoryReviewSection, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) ([]directWriteMemoryDecision, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if section == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: "missing memory review result"}
	}
	acceptedByIndex := make(map[int]logicdomain.PostActionAcceptedMemoryCandidate, len(section.AcceptedCandidates)+len(section.AcceptedCandidateIndexes))
	for _, accepted := range section.AcceptedCandidates {
		acceptedByIndex[accepted.CandidateIndex] = accepted
	}
	for _, idx := range section.AcceptedCandidateIndexes {
		if _, ok := acceptedByIndex[idx]; ok {
			continue
		}
		acceptedByIndex[idx] = logicdomain.PostActionAcceptedMemoryCandidate{CandidateIndex: idx}
	}
	droppedByIndex := make(map[int]logicdomain.PostActionDroppedMemoryCandidate, len(section.DroppedCandidates)+len(section.DroppedCandidateIndexes))
	for _, dropped := range section.DroppedCandidates {
		droppedByIndex[dropped.CandidateIndex] = dropped
	}
	for _, idx := range section.DroppedCandidateIndexes {
		if _, ok := droppedByIndex[idx]; ok {
			continue
		}
		droppedByIndex[idx] = logicdomain.PostActionDroppedMemoryCandidate{CandidateIndex: idx}
	}
	decisions := make([]directWriteMemoryDecision, len(candidates))
	for idx, candidate := range candidates {
		// Local hard-dedupe targets come from the full recalled hit set and real-cosine comparison rather than the reviewer-visible SimilarMemories subset,
		// so they must not be rejected by the LLM-output validation that only exists to constrain reviewer-authored dedupe ids.
		// 本地 hard dedupe 目标来自完整召回命中集与真实 cosine 比较，而不是 reviewer 可见的 SimilarMemories 子集，
		// 因此不能再被只用于约束 reviewer 输出 dedupe id 的那层校验误伤。
		if dropped, ok := hardDropped[idx]; ok {
			decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dropped.DedupeMemoryID}
			continue
		}
		if accepted, ok := acceptedByIndex[idx]; ok {
			supersedeMemoryIDs, err := validatePostActionAcceptedSupersedeMemoryIDs(
				"review_postaction_candidates",
				accepted.CandidateIndex,
				candidate.SimilarMemories,
				accepted.SupersedeMemoryIDs,
			)
			if err != nil {
				return nil, err
			}
			decisions[idx] = directWriteMemoryDecision{SupersedeMemoryIDs: supersedeMemoryIDs}
			continue
		}
		if dropped, ok := droppedByIndex[idx]; ok {
			dedupeMemoryID, err := validatePostActionDroppedDedupeMemoryID(
				"review_postaction_candidates",
				dropped.CandidateIndex,
				candidate.SimilarMemories,
				dropped.DedupeMemoryID,
			)
			if err != nil {
				return nil, err
			}
			decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dedupeMemoryID}
			continue
		}
		decisions[idx] = directWriteMemoryDecision{}
	}
	return decisions, nil
}

// loadDirectWriteDedupedExistingRows batches the semantic-dedupe targets chosen by reviewer drops and keeps only still-active hot-path rows so direct-write results never point at retired memories.
// loadDirectWriteDedupedExistingRows 用于批量加载 reviewer 丢弃后选择复用的语义去重目标，并只保留仍处于热路径的 active 行，避免主动写入结果指向已退役记忆。
func (u *MemoryUseCase) loadDirectWriteDedupedExistingRows(ctx context.Context, decisions []directWriteMemoryDecision) (map[uint64]logicdomain.MemoryNodeRecord, error) {
	if len(decisions) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}
	memoryIDs := make([]uint64, 0, len(decisions))
	seen := make(map[uint64]struct{}, len(decisions))
	for _, decision := range decisions {
		if decision.DedupedExistingMemoryID == 0 {
			continue
		}
		if _, ok := seen[decision.DedupedExistingMemoryID]; ok {
			continue
		}
		seen[decision.DedupedExistingMemoryID] = struct{}{}
		memoryIDs = append(memoryIDs, decision.DedupedExistingMemoryID)
	}
	if len(memoryIDs) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}
	rows, err := u.memories.LoadMemoryNodesByIDs(ctx, memoryIDs)
	if err != nil {
		return nil, err
	}
	return indexActiveUnexpiredMemoryRowsByID(rows, time.Now().UTC()), nil
}

// memoryNodeRecordIsActiveUnexpiredAt keeps direct-write semantic dedupe aligned with the runtime hot-path contract so stale or retired rows are never returned as reusable targets.
// memoryNodeRecordIsActiveUnexpiredAt 用于让主动写语义去重与运行时热路径契约保持一致，避免把陈旧或已退役的记忆行当成可复用目标返回。
func memoryNodeRecordIsActiveUnexpiredAt(row logicdomain.MemoryNodeRecord, now time.Time) bool {
	return logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, now)
}

// indexActiveUnexpiredMemoryRowsByID builds one id-indexed lookup for hot-path memory rows while dropping any record that has already left the active+unexpired window before materialization finishes.
// indexActiveUnexpiredMemoryRowsByID 用于为热路径记忆行构建按 id 索引的查找表，并在回表完成前丢弃已经退出 active+unexpired 窗口的记录。
func indexActiveUnexpiredMemoryRowsByID(rows []logicdomain.MemoryNodeRecord, now time.Time) map[uint64]logicdomain.MemoryNodeRecord {
	if len(rows) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}
	}
	indexed := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		if !memoryNodeRecordIsActiveUnexpiredAt(row, now) {
			continue
		}
		indexed[row.ID] = row
	}
	return indexed
}

// assignDirectWriteResultItems fans one canonical direct-write outcome back to every original caller position that collapsed into the same in-request dedupe bucket.
// assignDirectWriteResultItems 用于把一条规范化后的主动写入结果回填到同一请求内折叠到同一去重桶的所有原始位置。
func assignDirectWriteResultItems(results []WriteMemoryResultItem, originalIndexes []int, canonical WriteMemoryResultItem) {
	if len(results) == 0 || len(originalIndexes) == 0 {
		return
	}
	for copyIdx, originalIndex := range originalIndexes {
		if originalIndex < 0 || originalIndex >= len(results) {
			continue
		}
		item := canonical
		if copyIdx > 0 && !item.Deduped {
			item.Deduped = true
		}
		results[originalIndex] = item
	}
}

// prepareDirectWriteCreateVectors batches embeddings for all direct-write candidates that still need a fresh durable row after semantic dedupe resolution, so one request pays at most one embedding round-trip for its create set.
// prepareDirectWriteCreateVectors 用于为语义去重判定后仍需新建的主动写入候选批量生成向量，让单次请求对“新建集合”最多只支付一次 embedding 往返。
func (u *MemoryUseCase) prepareDirectWriteCreateVectors(ctx context.Context, session logicdomain.SessionRef, pending []directWritePendingItem, decisions []directWriteMemoryDecision, dedupedExistingRows map[uint64]logicdomain.MemoryNodeRecord) (map[int][]float32, error) {
	out := make(map[int][]float32, len(pending))
	if len(pending) == 0 {
		return out, nil
	}
	createIndexes := make([]int, 0, len(pending))
	createTexts := make([]string, 0, len(pending))
	for idx, pendingItem := range pending {
		decision := decisions[idx]
		if decision.DedupedExistingMemoryID > 0 {
			if _, ok := dedupedExistingRows[decision.DedupedExistingMemoryID]; ok {
				continue
			}
			// Degrade stale dedupe targets into a fresh write so a concurrent retirement window cannot turn one explicit tool write into a hard failure or a stale memory ref.
			// 当 dedupe 目标在并发窗口内失效时，退化成新建，避免显式工具写入因为竞争时序直接失败或返回陈旧记忆引用。
			if u != nil && u.logger != nil {
				u.logger.Warn("direct memory semantic dedupe target became unavailable; falling back to create",
					"session_id", session.SessionID,
					"dedupe_memory_id", decision.DedupedExistingMemoryID,
				)
			}
		}
		createIndexes = append(createIndexes, idx)
		createTexts = append(createTexts, pendingItem.Item.Abstract)
	}
	if len(createTexts) == 0 {
		return out, nil
	}
	resp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Texts: createTexts})
	if err != nil {
		return nil, err
	}
	if err := resp.ValidateStrict(len(createTexts)); err != nil {
		return nil, err
	}
	for idx, pendingIndex := range createIndexes {
		out[pendingIndex] = append([]float32(nil), resp.Vectors[idx]...)
	}
	return out, nil
}

// persistDirectWriteMemory writes one accepted direct-write item using a precomputed embedding vector, then persists the unified memory row together with any same-scope replacements.
// persistDirectWriteMemory 用于使用预先生成好的 embedding 向量写入一条已接纳的主动记忆，并连同同作用域替代结果一起持久化统一记忆行。
func (u *MemoryUseCase) persistDirectWriteMemory(ctx context.Context, session logicdomain.SessionRef, item WriteMemoryItem, dedupeHash string, supersedeMemoryIDs []uint64, now time.Time, vectorPayload []float32) (WriteMemoryResultItem, error) {
	applier, canApplyDirectWrite := u.memories.(directMemoryWriteApplier)
	if len(supersedeMemoryIDs) > 0 && !canApplyDirectWrite {
		return WriteMemoryResultItem{}, fmt.Errorf("memory store does not support atomic direct-memory replacement writes")
	}

	if len(vectorPayload) == 0 {
		return WriteMemoryResultItem{}, fmt.Errorf("direct memory vector payload is required")
	}
	vectorID, err := generatePostActionUUID()
	if err != nil {
		return WriteMemoryResultItem{}, err
	}

	record := logicdomain.MemoryRecord{
		ID:           vectorID,
		Text:         item.Abstract,
		Vector:       append([]float32(nil), vectorPayload...),
		Filter:       buildDirectMemoryFilter(session, item.ScopeLevel),
		SourceTurnID: 0,
		Metadata: map[string]string{
			"category":     strconv.Itoa(item.Category),
			"details":      item.Details,
			"source_kind":  logicdomain.MemorySourceKindLabel(logicdomain.MemorySourceKindGRPCAIWrite),
			"scope_level":  logicdomain.MemoryScopeLevelLabel(item.ScopeLevel),
			"priority":     strconv.Itoa(item.Priority),
			"memory_level": strconv.Itoa(item.MemoryLevel),
		},
		CreatedAt: now,
	}
	if err := u.vector.Upsert(ctx, record); err != nil {
		return WriteMemoryResultItem{}, err
	}

	memoryRecord := logicdomain.MemoryNodeRecord{
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.SessionID,
		VectorID:        vectorID,
		Vector:          append([]float32(nil), vectorPayload...),
		SourceKind:      logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:      item.ScopeLevel,
		Category:        item.Category,
		Abstract:        item.Abstract,
		Details:         item.Details,
		Status:          logicdomain.MemoryStatusActive,
		Priority:        item.Priority,
		MemoryLevel:     item.MemoryLevel,
		RefreshWeight:   1,
		ExpiresAt:       item.ExpiresAt,
		DedupeHash:      dedupeHash,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	var created logicdomain.MemoryNodeRecord
	var supersededVectorIDs []string
	if canApplyDirectWrite {
		applyResult, err := applier.ApplyDirectMemoryWrite(ctx, session, memoryRecord, supersedeMemoryIDs)
		if err != nil {
			u.rollbackDirectWriteVector(ctx, vectorID, session.SessionID)
			return WriteMemoryResultItem{}, err
		}
		created = applyResult.InsertedMemoryNode
		supersededVectorIDs = applyResult.SupersededVectorIDs
	} else {
		created, err = u.memories.CreateDirectMemoryNode(ctx, session, memoryRecord)
		if err != nil {
			u.rollbackDirectWriteVector(ctx, vectorID, session.SessionID)
			return WriteMemoryResultItem{}, err
		}
	}
	if len(supersededVectorIDs) > 0 {
		if _, deleteErr := u.vector.DeleteByIDs(ctx, supersededVectorIDs); deleteErr != nil {
			if u.logger != nil {
				u.logger.Error("direct memory superseded vector cleanup failed", "session_id", session.SessionID, "err", deleteErr)
			}
			enqueueVectorGCCompensation(ctx, u.memories, u.logger, logicdomain.VectorGCJobTypeDirectWriteSupersedeCleanup, supersededVectorIDs, now,
				"session_id", session.SessionID,
			)
		}
	}
	return WriteMemoryResultItem{
		Ref: logicdomain.MemoryRef{
			Type: logicdomain.MemoryRefTypeMemory,
			ID:   created.ID,
		},
		SourceKind: created.SourceKind,
		ScopeLevel: created.ScopeLevel,
		Deduped:    false,
	}, nil
}

// rollbackDirectWriteVector removes one freshly inserted vector row when the relational write failed after vector persistence succeeded.
// rollbackDirectWriteVector 用于在向量已落库但关系写入失败时，回滚刚插入的新向量行。
func (u *MemoryUseCase) rollbackDirectWriteVector(ctx context.Context, vectorID string, sessionID uint64) {
	if u == nil || u.vector == nil || strings.TrimSpace(vectorID) == "" {
		return
	}
	if _, rollbackErr := u.vector.DeleteByIDs(ctx, []string{vectorID}); rollbackErr != nil {
		if u.logger != nil {
			u.logger.Error("direct memory vector rollback failed", "vector_id", vectorID, "session_id", sessionID, "err", rollbackErr)
		}
		enqueueVectorGCCompensation(ctx, u.memories, u.logger, logicdomain.VectorGCJobTypeDirectWriteRollback, []string{vectorID}, time.Now().UTC(),
			"vector_id", vectorID,
			"session_id", sessionID,
		)
	}
}

// validateMemoryQueryCommand checks the simple query-list request before any hierarchy, embedding, or vector work begins.
// validateMemoryQueryCommand 用于在任何层级解析、embedding 或向量检索开始前校验简单查询列表请求。
func validateMemoryQueryCommand(cmd MemoryQueryCommand) error {
	if cmd.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if cmd.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(cmd.Queries) == 0 {
		return logicdomain.ValidationError{Field: "queries", Message: "must contain at least one query"}
	}
	if len(cmd.Queries) > maxMemoryQueryItems {
		return logicdomain.ValidationError{Field: "queries", Message: fmt.Sprintf("must contain at most %d queries", maxMemoryQueryItems)}
	}
	if cmd.TopK < 0 {
		return logicdomain.ValidationError{Field: "top_k", Message: "must be >= 0"}
	}
	if scope := strings.TrimSpace(cmd.ScopeOverride); scope != "" && !isSupportedMemoryQueryScope(scope) {
		return logicdomain.ValidationError{Field: "scope_override", Message: "must be session, team, space, or project when set"}
	}
	if normalizeConfigToken(cmd.ScopeOverride) == memoryReplaceScopeSession && cmd.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must be > 0 when scope_override=session"}
	}
	return nil
}

// validateTurnDetailCommand checks the turn-detail lookup request before relational reads begin.
// validateTurnDetailCommand 用于在关系读取开始前校验 turn 详情查询请求。
func validateTurnDetailCommand(cmd TurnDetailCommand) error {
	if len(cmd.TurnIDs) == 0 {
		return logicdomain.ValidationError{Field: "turn_ids", Message: "must contain at least one id"}
	}
	if len(cmd.TurnIDs) > maxTurnDetailLookup {
		return logicdomain.ValidationError{Field: "turn_ids", Message: fmt.Sprintf("must contain at most %d ids", maxTurnDetailLookup)}
	}
	for idx, turnID := range cmd.TurnIDs {
		if turnID == 0 {
			return logicdomain.ValidationError{Field: "turn_ids[" + strconv.Itoa(idx) + "]", Message: "must be a numeric id"}
		}
	}
	return nil
}

// validateMemoryDetailCommand checks the mixed detail lookup request before relational reads begin.
// validateMemoryDetailCommand 用于在关系读取开始前校验混合详情查询请求。
func validateMemoryDetailCommand(cmd MemoryDetailCommand) error {
	if len(cmd.Refs) == 0 {
		return logicdomain.ValidationError{Field: "refs", Message: "must contain at least one ref"}
	}
	if len(cmd.Refs) > maxMemoryDetailLookup {
		return logicdomain.ValidationError{Field: "refs", Message: fmt.Sprintf("must contain at most %d refs", maxMemoryDetailLookup)}
	}
	for idx, ref := range cmd.Refs {
		if !logicdomain.ValidMemoryRefType(ref.Type) {
			return logicdomain.ValidationError{Field: "refs[" + strconv.Itoa(idx) + "].type", Message: "must be one supported ref type"}
		}
		if ref.ID == 0 {
			return logicdomain.ValidationError{Field: "refs[" + strconv.Itoa(idx) + "].id", Message: "must be a numeric id"}
		}
	}
	return nil
}

// validateWriteMemoriesCommand checks the resolved scope and direct-write candidates before soft idempotency and embedding begin.
// validateWriteMemoriesCommand 用于在软幂等和 embedding 开始前校验已解析范围与主动写入候选。
func validateWriteMemoriesCommand(cmd WriteMemoriesCommand) error {
	if cmd.Session.SessionID == 0 || strings.TrimSpace(cmd.Session.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if cmd.Session.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if cmd.Session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if len(cmd.Items) == 0 {
		return logicdomain.ValidationError{Field: "items", Message: "must contain at least one item"}
	}
	if len(cmd.Items) > maxWriteMemoryItems {
		return logicdomain.ValidationError{Field: "items", Message: fmt.Sprintf("must contain at most %d items", maxWriteMemoryItems)}
	}
	for idx, item := range cmd.Items {
		if strings.TrimSpace(item.Abstract) == "" {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].abstract", Message: "is required"}
		}
		if strings.TrimSpace(item.Details) == "" {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].details", Message: "is required"}
		}
		if !logicdomain.ValidMemoryNodeCategory(item.Category) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].category", Message: "must be one supported memory category"}
		}
		if item.ScopeLevel >= 0 && !logicdomain.ValidMemoryScopeLevel(item.ScopeLevel) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].scope_level", Message: "must be one supported memory scope"}
		}
		if item.Priority >= 0 && !logicdomain.ValidMemoryPriority(item.Priority) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].priority", Message: "must be one supported memory priority"}
		}
		if item.MemoryLevel >= 0 && !logicdomain.ValidMemoryLevel(item.MemoryLevel) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].memory_level", Message: "must be one supported memory level"}
		}
	}
	return nil
}

// normalizeMemoryQueries converts the caller-supplied query list into the strict normalized item format expected by the retrieval pipeline.
// normalizeMemoryQueries 用于把调用方提供的查询列表转换成检索链路期望的严格规范化条目格式。
func normalizeMemoryQueries(queries []string) ([]MemoryQueryItem, error) {
	if len(queries) == 0 {
		return nil, logicdomain.ValidationError{Field: "queries", Message: "must contain at least one query"}
	}
	if len(queries) > maxMemoryQueryItems {
		return nil, logicdomain.ValidationError{Field: "queries", Message: fmt.Sprintf("must contain at most %d queries", maxMemoryQueryItems)}
	}
	items := make([]MemoryQueryItem, 0, len(queries))
	for idx, query := range queries {
		query = textutil.NormalizeWhitespace(query)
		if query == "" {
			return nil, logicdomain.ValidationError{Field: "queries[" + strconv.Itoa(idx) + "]", Message: "is required"}
		}
		items = append(items, MemoryQueryItem{Query: query})
	}
	return items, nil
}

// buildMemorySearchText keeps the embedding input aligned with the simplified AI-facing contract by using the normalized query text directly.
// buildMemorySearchText 用于让 embedding 输入与简化后的 AI 接口契约保持一致，直接使用规范化查询文本。
func buildMemorySearchText(item MemoryQueryItem) string {
	return strings.TrimSpace(item.Query)
}

// buildMemoryQueryCacheKey converts one normalized query into a stable in-request cache key so repeated queries can reuse one retrieval execution without changing the caller-facing result shape.
// buildMemoryQueryCacheKey 用于把一条已归一的 query 转成稳定的单请求缓存键，让重复 query 能复用一次检索执行，同时不改变调用方看到的结果结构。
func buildMemoryQueryCacheKey(item MemoryQueryItem) string {
	return normalizeMemoryQueryCachePart(item.Query)
}

// normalizeMemoryQueryCachePart keeps the in-request dedupe key aligned with pre-check's low-risk query normalization so case-only variants do not fan out into duplicate retrieval work.
// normalizeMemoryQueryCachePart 用于让单请求去重键与 pre-check 的低风险 query 归一保持一致，避免仅有大小写差异的变体再次扩散成重复检索开销。
func normalizeMemoryQueryCachePart(text string) string {
	return strings.ToLower(textutil.NormalizeWhitespace(text))
}

// cloneMemoryQueryHits deep-copies one hit slice before it is shared across repeated groups so later callers cannot accidentally mutate another group's cached result.
// cloneMemoryQueryHits 用于在重复 group 复用结果前深拷贝命中切片，避免后续调用方意外修改到另一组共享的缓存结果。
func cloneMemoryQueryHits(hits []MemoryQueryHit) []MemoryQueryHit {
	if len(hits) == 0 {
		return nil
	}
	cloned := make([]MemoryQueryHit, 0, len(hits))
	for _, hit := range hits {
		copyHit := hit
		copyHit.Vector = append([]float32(nil), hit.Vector...)
		copyHit.MatchedContextValues = append([]string(nil), hit.MatchedContextValues...)
		cloned = append(cloned, copyHit)
	}
	return cloned
}

// cloneFloat32Slice copies one float32 vector so repeated query groups can safely reuse cached embeddings without sharing backing arrays across callers.
// cloneFloat32Slice 用于复制一条 float32 向量，让重复 query group 可以安全复用缓存 embedding，而不会与调用方共享底层数组。
func cloneFloat32Slice(values []float32) []float32 {
	if len(values) == 0 {
		return nil
	}
	return append([]float32(nil), values...)
}

// normalizeMemorySearchTopK applies the RPC defaults and caps so callers can omit top_k safely without creating oversized result sets.
// normalizeMemorySearchTopK 用于施加 RPC 默认值和上限，让调用方即使省略 top_k 也不会生成过大的结果集。
func normalizeMemorySearchTopK(topK int) int {
	if topK <= 0 {
		return defaultMemorySearchTopK
	}
	if topK > maxMemorySearchTopK {
		return maxMemorySearchTopK
	}
	return topK
}

// normalizeMemoryCandidatePoolK keeps the mixed-recall candidate pool large enough for lexical fusion, rerank, and optional MMR without breaking the repo-wide RPC cap.
// normalizeMemoryCandidatePoolK 用于让混合召回候选池足够承载 lexical 融合、rerank 和可选 MMR，同时继续遵守仓库级 RPC 上限。
func normalizeMemoryCandidatePoolK(finalTopK, lexicalTopK, rerankTopN int, mmrEnabled bool) int {
	poolK := finalTopK
	if lexicalTopK > poolK {
		poolK = lexicalTopK
	}
	if rerankTopN > poolK {
		poolK = rerankTopN
	}
	if mmrEnabled {
		mmrPoolK := finalTopK * 2
		if mmrPoolK > poolK {
			poolK = mmrPoolK
		}
	}
	return normalizeMemorySearchTopK(poolK)
}

// hybridizeSearchHits optionally augments vector hits with lexical recall and fuses both channels through RRF.
// hybridizeSearchHits 用于按需使用 lexical 召回增强向量命中，并通过 RRF 融合两个通道。
func (u *MemoryUseCase) hybridizeSearchHits(ctx context.Context, item MemoryQueryItem, filter logicdomain.SearchFilter, poolK int, vectorHits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.hybridEnabled || u.memories == nil {
		return trimSearchHits(vectorHits, poolK)
	}
	query := buildMemoryLexicalQuery(item)
	if strings.TrimSpace(query) == "" {
		return trimSearchHits(vectorHits, poolK)
	}
	lexicalHits, err := u.memories.SearchLexicalMemory(ctx, query, poolK, filter)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory lexical search degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
		}
		return trimSearchHits(vectorHits, poolK)
	}
	if len(lexicalHits) == 0 {
		u.logMemorySearchStage(ctx, "memory search hybrid stage completed", buildMemorySearchText(item), []any{
			"vector_candidate_count", len(vectorHits),
			"lexical_hit_count", 0,
			"lexical_materialized_count", 0,
			"hybrid_candidate_count", len(trimSearchHits(vectorHits, poolK)),
		}, map[string]any{
			"query":                 item.Query,
			"top_vector_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(vectorHits)),
			"top_lexical_candidate": nil,
			"top_hybrid_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(trimSearchHits(vectorHits, poolK))),
		})
		return trimSearchHits(vectorHits, poolK)
	}
	materialized, err := u.materializeLexicalHits(ctx, lexicalHits)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory lexical materialization degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
		}
		return trimSearchHits(vectorHits, poolK)
	}
	fused := fuseSearchHitsByRRF(vectorHits, materialized, poolK, u.rrfK)
	u.logMemorySearchStage(ctx, "memory search hybrid stage completed", buildMemorySearchText(item), []any{
		"vector_candidate_count", len(vectorHits),
		"lexical_hit_count", len(lexicalHits),
		"lexical_materialized_count", len(materialized),
		"hybrid_candidate_count", len(fused),
	}, map[string]any{
		"query":                 item.Query,
		"top_vector_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(vectorHits)),
		"top_lexical_candidate": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(materialized)),
		"top_hybrid_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(fused)),
	})
	return fused
}

// buildMemoryLexicalQuery keeps lexical recall focused on the normalized query text so the simplified AI-facing contract does not need a second background field.
// buildMemoryLexicalQuery 用于让 lexical 召回聚焦规范化后的 query 文本，避免简化后的 AI 接口再引入第二个 background 字段。
func buildMemoryLexicalQuery(item MemoryQueryItem) string {
	return strings.TrimSpace(item.Query)
}

// materializeLexicalHits loads the durable rows for lexical hits and converts them into the same public hit shape used by vector recall.
// materializeLexicalHits 用于回表加载 lexical 命中的长期行，并把它们转换成与向量召回一致的公开命中结构。
func (u *MemoryUseCase) materializeLexicalHits(ctx context.Context, hits []logicdomain.MemoryLexicalHit) ([]MemoryQueryHit, error) {
	memoryIDs := collectLexicalMemoryIDs(hits)
	if len(memoryIDs) == 0 {
		return []MemoryQueryHit{}, nil
	}
	rows, err := u.memories.LoadMemoryNodesByIDs(ctx, memoryIDs)
	if err != nil {
		return nil, err
	}
	byID := indexActiveUnexpiredMemoryRowsByID(rows, time.Now().UTC())
	mapped := make([]MemoryQueryHit, 0, len(hits))
	total := len(hits)
	for idx, hit := range hits {
		row, ok := byID[hit.MemoryID]
		if !ok {
			continue
		}
		mappedHit := MemoryQueryHit{
			MemoryRef: logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeMemory,
				ID:   row.ID,
			},
			SourceKind:               row.SourceKind,
			ScopeLevel:               row.ScopeLevel,
			Priority:                 row.Priority,
			MemoryLevel:              row.MemoryLevel,
			RefreshWeight:            row.RefreshWeight,
			SessionID:                row.OriginSessionID,
			Abstract:                 strings.TrimSpace(row.Abstract),
			DetailsPreview:           strings.TrimSpace(row.Details),
			Category:                 row.Category,
			Score:                    normalizedRankScore(idx+1, total),
			Origin:                   "lexical_search",
			Vector:                   append([]float32(nil), row.Vector...),
			CreatedAt:                row.CreatedAt,
			LastRecalledAt:           row.LastRecalledAt,
			LastAdoptedAt:            row.LastAdoptedAt,
			LastReinforcedAt:         row.LastReinforcedAt,
			SupportCount:             row.SupportCount,
			RebuttalCount:            row.RebuttalCount,
			ReinforcementCount:       row.ReinforcementCount,
			CrossSessionAdoptedCount: row.CrossSessionAdoptedCount,
			DecayDisabled:            row.DecayDisabled,
		}
		if row.SourceTurnID > 0 {
			mappedHit.SourceRef = logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeTurn,
				ID:   row.SourceTurnID,
			}
		}
		mapped = append(mapped, mappedHit)
	}
	return mapped, nil
}

// collectLexicalMemoryIDs removes empty and duplicate lexical hit ids before the relational materialization query starts.
// collectLexicalMemoryIDs 用于在关系层回表开始前去掉空值和重复的 lexical 命中 id。
func collectLexicalMemoryIDs(hits []logicdomain.MemoryLexicalHit) []uint64 {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(hits))
	ids := make([]uint64, 0, len(hits))
	for _, hit := range hits {
		if hit.MemoryID == 0 {
			continue
		}
		if _, ok := seen[hit.MemoryID]; ok {
			continue
		}
		seen[hit.MemoryID] = struct{}{}
		ids = append(ids, hit.MemoryID)
	}
	return ids
}

// fuseSearchHitsByRRF merges vector and lexical hits by reciprocal-rank fusion while preserving one caller-friendly score in the 0..1 range.
// fuseSearchHitsByRRF 用于通过 reciprocal-rank fusion 融合向量和 lexical 命中，并保留一个 0..1 区间内的调用方友好分数。
func fuseSearchHitsByRRF(vectorHits, lexicalHits []MemoryQueryHit, topK, rrfK int) []MemoryQueryHit {
	type fusedCandidate struct {
		Hit      MemoryQueryHit
		RRFScore float64
		Channels int
	}
	if rrfK <= 0 {
		rrfK = 60
	}
	candidates := make(map[uint64]*fusedCandidate, len(vectorHits)+len(lexicalHits))
	merge := func(hits []MemoryQueryHit, channel string) {
		total := len(hits)
		for idx, hit := range hits {
			if hit.MemoryRef.ID == 0 {
				continue
			}
			candidate, ok := candidates[hit.MemoryRef.ID]
			if !ok {
				copyHit := hit
				candidate = &fusedCandidate{Hit: copyHit}
				candidates[hit.MemoryRef.ID] = candidate
			}
			candidate.RRFScore += 1.0 / float64(rrfK+idx+1)
			candidate.Channels++
			if hit.Score > candidate.Hit.Score {
				candidate.Hit.Score = hit.Score
			}
			switch {
			case candidate.Hit.Origin == "":
				candidate.Hit.Origin = channel
			case candidate.Hit.Origin != channel:
				candidate.Hit.Origin = "hybrid_rrf"
			}
			if strings.TrimSpace(candidate.Hit.Abstract) == "" {
				candidate.Hit.Abstract = hit.Abstract
			}
			if strings.TrimSpace(candidate.Hit.DetailsPreview) == "" {
				candidate.Hit.DetailsPreview = hit.DetailsPreview
			}
			if candidate.Hit.SourceRef.Empty() && !hit.SourceRef.Empty() {
				candidate.Hit.SourceRef = hit.SourceRef
			}
			if candidate.Hit.SessionID == 0 {
				candidate.Hit.SessionID = hit.SessionID
			}
			if total > 0 {
				rankScore := normalizedRankScore(idx+1, total)
				if rankScore > candidate.Hit.Score {
					candidate.Hit.Score = rankScore
				}
			}
		}
	}
	merge(vectorHits, "vector_search")
	merge(lexicalHits, "lexical_search")

	fused := make([]fusedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		fused = append(fused, *candidate)
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].RRFScore == fused[j].RRFScore {
			if fused[i].Hit.Score == fused[j].Hit.Score {
				return fused[i].Hit.MemoryRef.ID < fused[j].Hit.MemoryRef.ID
			}
			return fused[i].Hit.Score > fused[j].Hit.Score
		}
		return fused[i].RRFScore > fused[j].RRFScore
	})
	if topK > 0 && len(fused) > topK {
		fused = fused[:topK]
	}
	out := make([]MemoryQueryHit, 0, len(fused))
	for idx, candidate := range fused {
		hit := candidate.Hit
		rankScore := normalizedRankScore(idx+1, len(fused))
		if rankScore > hit.Score {
			hit.Score = rankScore
		}
		if candidate.Channels > 1 && hit.Score < 1 {
			hit.Score = math.Min(1, hit.Score+0.03)
		}
		out = append(out, hit)
	}
	return out
}

// normalizedRankScore converts one 1-based rank into a stable 0..1 score so lexical-only or fused hits can still pass the existing pre-check threshold gate.
// normalizedRankScore 用于把 1-based 排名转换成稳定的 0..1 分数，让 lexical-only 或融合命中仍能通过现有 pre-check 阈值门槛。
func normalizedRankScore(rank, total int) float64 {
	if rank <= 0 || total <= 0 {
		return 0
	}
	score := 1 - (float64(rank-1) / float64(total*2))
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// trimSearchHits applies the final top-k cap while preserving the already established hit order.
// trimSearchHits 用于在保持既有命中顺序的前提下施加最终 top-k 截断。
func trimSearchHits(hits []MemoryQueryHit, topK int) []MemoryQueryHit {
	if topK <= 0 || len(hits) <= topK {
		return hits
	}
	return append([]MemoryQueryHit(nil), hits[:topK]...)
}

// clampMemoryQueryHitScores enforces the shared 0..1 caller-facing score contract after each optional retrieval post-processing stage, so arbitrary provider scores or contextual boosts cannot leak unstable score ranges to downstream ranking and RPC responses.
// clampMemoryQueryHitScores 用于在每个可选检索后处理阶段之后强制执行统一的 0..1 对外分数契约，避免 provider 自定义分数或情境加减分把不稳定范围泄露到后续排序和 RPC 响应中。
func clampMemoryQueryHitScores(hits []MemoryQueryHit) []MemoryQueryHit {
	for idx := range hits {
		hits[idx].Score = clampUnitScore(hits[idx].Score)
	}
	return hits
}

// applyWeibullDecaySearchHits reapplies one read-time memory-lifecycle prior after fusion and rerank so stale but still unexpired rows stop dominating recall.
// applyWeibullDecaySearchHits 用于在融合和 rerank 之后重新施加一次“读时记忆生命周期先验”，让陈旧但尚未过期的记录不再长期垄断召回。
func (u *MemoryUseCase) applyWeibullDecaySearchHits(hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.weibullEnabled || len(hits) <= 1 {
		return hits
	}
	now := time.Now().UTC()
	scored := append([]MemoryQueryHit(nil), hits...)
	changed := false
	for idx := range scored {
		nextScore := clampUnitScore(scored[idx].Score * u.weibullDecayMultiplier(scored[idx], now))
		if math.Abs(nextScore-scored[idx].Score) > 1e-9 {
			changed = true
		}
		scored[idx].Score = nextScore
	}
	if !changed {
		return scored
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].MemoryRef.ID < scored[j].MemoryRef.ID
		}
		return scored[i].Score > scored[j].Score
	})
	return scored
}

// weibullDecayMultiplier converts lifecycle evidence on one memory hit into a stable read-time score multiplier without changing hard expiry semantics.
// weibullDecayMultiplier 用于把单条记忆的生命周期证据转换成稳定的读时分数乘子，同时不改变现有硬过期语义。
func (u *MemoryUseCase) weibullDecayMultiplier(hit MemoryQueryHit, now time.Time) float64 {
	if u == nil || hit.DecayDisabled {
		return 1
	}
	anchor := latestMemoryReinforcementTime(hit)
	if anchor.IsZero() || !now.After(anchor) {
		return 1
	}
	ageHours := now.Sub(anchor).Hours()
	if ageHours <= 0 {
		return 1
	}

	scaleHours := u.weibullScaleHours
	scaleHours *= memoryLevelDecayScale(hit.MemoryLevel)
	scaleHours *= memoryPriorityDecayScale(hit.Priority)
	scaleHours *= memoryScopeDecayScale(hit.ScopeLevel)
	scaleHours *= 1 + 0.08*float64(maxInt(hit.RefreshWeight, 0))
	scaleHours *= 1 + u.weibullReinforceWeight*math.Log1p(float64(maxInt(hit.ReinforcementCount, 0)))
	scaleHours *= 1 + u.weibullCrossSessionBoost*float64(maxInt(hit.CrossSessionAdoptedCount, 0))
	if scaleHours <= 0 {
		return 1
	}

	survival := math.Exp(-math.Pow(ageHours/scaleHours, u.weibullShape))
	if math.IsNaN(survival) || math.IsInf(survival, 0) {
		return 1
	}
	if survival < u.weibullMinMultiplier {
		survival = u.weibullMinMultiplier
	}
	return clampUnitScore(survival)
}

// latestMemoryReinforcementTime chooses the freshest lifecycle timestamp that should anchor Weibull age calculation for one durable memory row.
// latestMemoryReinforcementTime 用于挑出一条长期记忆最“新鲜”的生命周期时间点，作为 Weibull 年龄计算的锚点。
func latestMemoryReinforcementTime(hit MemoryQueryHit) time.Time {
	latest := zeroOrUTC(hit.CreatedAt)
	candidates := []time.Time{
		hit.LastReinforcedAt,
		hit.LastAdoptedAt,
		hit.LastRecalledAt,
	}
	for _, candidate := range candidates {
		candidate = zeroOrUTC(candidate)
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest
}

// applyContextEvidenceScoring loads contextual evidence edges for the current candidate pool and applies a soft boost or demotion when the normalized query explicitly matches those situations.
// applyContextEvidenceScoring 用于为当前候选池加载情境证据边，并在规范化 query 明确命中这些场景时做软提升或软降权。
func (u *MemoryUseCase) applyContextEvidenceScoring(ctx context.Context, item MemoryQueryItem, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || u.memories == nil || len(hits) <= 1 {
		return hits
	}
	signals := buildMemoryQueryContextSignals(item)
	if len(signals.Phrases) == 0 {
		return hits
	}
	memoryIDs := collectMemoryQueryHitIDs(hits)
	if len(memoryIDs) == 0 {
		return hits
	}
	edges, err := u.memories.LoadMemoryContextEdgesByMemoryIDs(ctx, memoryIDs)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory context evidence scoring degraded", "candidate_count", len(memoryIDs), "err", err)
		}
		return hits
	}
	if len(edges) == 0 {
		return hits
	}

	// Aggregate only the edges whose context values explicitly match the normalized query phrases so unrelated support stats do not leak into the current ranking.
	// 只聚合那些与规范化 query 短语明确匹配的 edge，避免无关情境的支持统计污染当前排序。
	matchedEvidence := make(map[uint64]memoryContextEvidenceScore, len(memoryIDs))
	for _, edge := range edges {
		if !signals.Match(edge.ContextValue) {
			continue
		}
		score := matchedEvidence[edge.MemoryID]
		score.SupportCount += edge.SupportCount
		score.RebuttalCount += edge.RebuttalCount
		score.MatchedContextValues = appendUniqueMemoryContextEvidenceValue(score.MatchedContextValues, edge.ContextKey, edge.ContextValue)
		matchedEvidence[edge.MemoryID] = score
	}
	if len(matchedEvidence) == 0 {
		return hits
	}

	scored := append([]MemoryQueryHit(nil), hits...)
	evidenceByID := make(map[uint64]memoryContextEvidenceScore, len(matchedEvidence))
	for idx := range scored {
		evidence := matchedEvidence[scored[idx].MemoryRef.ID]
		if evidence.SupportCount == 0 && evidence.RebuttalCount == 0 {
			continue
		}
		evidence.Delta = computeMemoryContextEvidenceDelta(evidence)
		sort.Strings(evidence.MatchedContextValues)
		scored[idx].Score += evidence.Delta
		scored[idx].MatchedContextValues = append([]string(nil), evidence.MatchedContextValues...)
		scored[idx].MatchedContextSupportCount = evidence.SupportCount
		scored[idx].MatchedContextRebuttalCount = evidence.RebuttalCount
		scored[idx].MatchedContextScoreDelta = evidence.Delta
		evidenceByID[scored[idx].MemoryRef.ID] = evidence
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		left := evidenceByID[scored[i].MemoryRef.ID]
		right := evidenceByID[scored[j].MemoryRef.ID]
		leftNet := left.SupportCount - left.RebuttalCount
		rightNet := right.SupportCount - right.RebuttalCount
		if leftNet != rightNet {
			return leftNet > rightNet
		}
		if left.SupportCount != right.SupportCount {
			return left.SupportCount > right.SupportCount
		}
		if left.RebuttalCount != right.RebuttalCount {
			return left.RebuttalCount < right.RebuttalCount
		}
		return false
	})
	return scored
}

// memoryContextEvidenceScore stores the matched support/rebuttal totals for one candidate memory under the current query situation.
// memoryContextEvidenceScore 用于保存当前 query 场景下某个候选记忆匹配到的支持/反驳总量。
type memoryContextEvidenceScore struct {
	SupportCount         int
	RebuttalCount        int
	MatchedContextValues []string
	Delta                float64
}

// memoryQueryContextSignals stores the normalized phrases extracted from the query so context edges can do deterministic lexical matching without another model call.
// memoryQueryContextSignals 用于保存从 query 提取出的规范化短语，让 context edge 可以在不增加额外模型调用的情况下做确定性匹配。
type memoryQueryContextSignals struct {
	Phrases map[string]struct{}
}

// Match reports whether one normalized contextual value appears in the extracted phrase set for the current query item.
// Match 用于判断某个规范化情境值是否出现在当前 query item 提取出的短语集合中。
func (s memoryQueryContextSignals) Match(value string) bool {
	if len(s.Phrases) == 0 {
		return false
	}
	_, ok := s.Phrases[normalizeMemoryContextMatchText(value)]
	return ok
}

// buildMemoryQueryContextSignals extracts deterministic phrases and short n-grams from the current query item so contextual retrieval can align memory edges with user intent.
// buildMemoryQueryContextSignals 用于从当前 query item 提取确定性的短语和短 n-gram，让情境检索可以把记忆边与用户意图对齐。
func buildMemoryQueryContextSignals(item MemoryQueryItem) memoryQueryContextSignals {
	phrases := make(map[string]struct{})
	appendPhrase := func(value string) {
		value = normalizeMemoryContextMatchText(value)
		if value == "" {
			return
		}
		phrases[value] = struct{}{}
	}
	appendText := func(text string) {
		appendPhrase(text)
		tokens := textutil.Tokenize(text)
		for _, token := range tokens {
			appendPhrase(token)
		}
		for windowSize := 2; windowSize <= 4; windowSize++ {
			for start := 0; start+windowSize <= len(tokens); start++ {
				appendPhrase(strings.Join(tokens[start:start+windowSize], " "))
			}
		}
	}
	appendText(item.Query)
	return memoryQueryContextSignals{Phrases: phrases}
}

// normalizeMemoryContextMatchText normalizes a contextual phrase into the same lexical surface used by query-time matching and edge values.
// normalizeMemoryContextMatchText 用于把情境短语归一成查询期匹配和 edge 值共享的词法表面形式。
func normalizeMemoryContextMatchText(raw string) string {
	return logicdomain.NormalizeMemoryContextValue(raw)
}

// computeMemoryContextEvidenceDelta converts matched support/rebuttal counts into one bounded score delta so context evidence influences ranking without dominating the whole retrieval pipeline.
// computeMemoryContextEvidenceDelta 用于把匹配到的支持/反驳计数转换成一个有界分数增量，让情境证据影响排序但不垄断整条检索链。
func computeMemoryContextEvidenceDelta(evidence memoryContextEvidenceScore) float64 {
	supportBoost := math.Min(0.12, 0.025*float64(evidence.SupportCount))
	rebuttalPenalty := math.Min(0.14, 0.03*float64(evidence.RebuttalCount))
	return supportBoost - rebuttalPenalty
}

// appendUniqueMemoryContextEvidenceValue keeps the matched context summary deterministic and de-duplicated so upper layers can surface a compact explanation of why a memory matched.
// appendUniqueMemoryContextEvidenceValue 用于保持命中的 context 摘要确定且去重，方便上层输出“这条记忆为什么命中”的紧凑说明。
func appendUniqueMemoryContextEvidenceValue(values []string, key, value string) []string {
	key = logicdomain.NormalizeMemoryContextKey(key)
	value = logicdomain.NormalizeMemoryContextValue(value)
	if value == "" {
		return values
	}
	label := value
	if key != "" {
		label = key + "=" + value
	}
	for _, existing := range values {
		if logicdomain.NormalizeMemoryContextEvidenceLabel(existing) == label {
			return values
		}
	}
	return append(values, label)
}

// collectMemoryQueryHitIDs returns the distinct durable memory ids present in one candidate slice so query-time enrichment can batch-load relational evidence once.
// collectMemoryQueryHitIDs 用于返回候选切片中的去重长期 memory id，方便查询期补全一次性批量加载关系证据。
func collectMemoryQueryHitIDs(hits []MemoryQueryHit) []uint64 {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(hits))
	ids := make([]uint64, 0, len(hits))
	for _, hit := range hits {
		if hit.MemoryRef.ID == 0 {
			continue
		}
		if _, ok := seen[hit.MemoryRef.ID]; ok {
			continue
		}
		seen[hit.MemoryRef.ID] = struct{}{}
		ids = append(ids, hit.MemoryRef.ID)
	}
	return ids
}

// memoryLevelDecayScale stretches the Weibull scale for higher-level memories so stable or core knowledge decays more slowly than short-term facts.
// memoryLevelDecayScale 用于为更高等级的记忆拉长 Weibull 尺度，让稳定或核心知识比短期事实衰减得更慢。
func memoryLevelDecayScale(level int) float64 {
	switch level {
	case logicdomain.MemoryLevelSession:
		return 0.8
	case logicdomain.MemoryLevelPhase:
		return 1.0
	case logicdomain.MemoryLevelStable:
		return 1.5
	case logicdomain.MemoryLevelPersistent:
		return 2.4
	default:
		return 1.0
	}
}

// memoryPriorityDecayScale slightly protects high-priority memories during read-time decay so hard rules do not disappear behind fresher trivia.
// memoryPriorityDecayScale 用于在读时衰减里轻微保护高优先级记忆，避免硬规则被更新鲜但无关紧要的细节压下去。
func memoryPriorityDecayScale(priority int) float64 {
	switch priority {
	case logicdomain.MemoryPriorityP0:
		return 1.35
	case logicdomain.MemoryPriorityP1:
		return 1.15
	default:
		return 1.0
	}
}

// memoryScopeDecayScale gives broader-scope memories a slightly longer decay horizon because they are more likely to remain useful across requests.
// memoryScopeDecayScale 用于给更大作用域的记忆略长的衰减视窗，因为它们跨请求继续有用的概率更高。
func memoryScopeDecayScale(scopeLevel int) float64 {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return 0.9
	case logicdomain.MemoryScopeLevelUser:
		return 1.25
	default:
		return 1.1
	}
}

// zeroOrUTC normalizes lifecycle timestamps used by read-time ranking so helper callers can compare zero values without extra branching.
// zeroOrUTC 用于规范化读时排序使用的生命周期时间戳，让调用方在比较零值时不必反复写分支。
func zeroOrUTC(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}

// clampUnitScore keeps ranking multipliers and final scores inside the stable 0..1 range expected by pre-check filtering, while also collapsing NaN and infinity so one unstable upstream provider value cannot poison later sorting or RPC responses.
// clampUnitScore 用于把排序乘子和最终分数都钳制在 pre-check 过滤所期望的稳定 0..1 区间，并额外吸收 NaN 与无穷值，避免上游不稳定分数污染后续排序或 RPC 响应。
func clampUnitScore(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if math.IsInf(value, 1) {
		return 1
	}
	if math.IsInf(value, -1) {
		return 0
	}
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// maxInt keeps small lifecycle-weight helpers readable without repeatedly inlining zero-floor arithmetic.
// maxInt 用于让小型生命周期权重辅助逻辑保持可读，避免反复内联零值下限计算。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// applyMMRSearchHits diversifies the already-ranked candidate pool so highly similar memories do not monopolize the final top-k.
// applyMMRSearchHits 用于对已经排好序的候选池做多样性重排，避免高度相似的记忆垄断最终 top-k。
func (u *MemoryUseCase) applyMMRSearchHits(ctx context.Context, topK int, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || !u.mmrEnabled || len(hits) <= 1 {
		return trimSearchHits(hits, topK)
	}
	limit := topK
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	working := append([]MemoryQueryHit(nil), hits...)
	working = u.ensureMMRVectors(ctx, working)
	if len(working) <= 1 {
		return trimSearchHits(working, limit)
	}

	selected := make([]MemoryQueryHit, 0, limit)
	used := make(map[uint64]struct{}, limit)
	for len(selected) < limit {
		bestIdx := -1
		bestScore := math.Inf(-1)
		for idx, hit := range working {
			if _, ok := used[hit.MemoryRef.ID]; ok {
				continue
			}
			score := hit.Score
			if len(selected) > 0 {
				score = u.mmrLambda*hit.Score - (1-u.mmrLambda)*maxMMRSimilarity(hit, selected)
			}
			if score > bestScore {
				bestScore = score
				bestIdx = idx
				continue
			}
			if score == bestScore {
				current := working[bestIdx]
				if hit.Score > current.Score || (hit.Score == current.Score && hit.MemoryRef.ID < current.MemoryRef.ID) {
					bestIdx = idx
				}
			}
		}
		if bestIdx < 0 {
			break
		}
		chosen := working[bestIdx]
		chosen.Origin = normalizeMMROrigin(chosen.Origin)
		selected = append(selected, chosen)
		used[chosen.MemoryRef.ID] = struct{}{}
	}
	return selected
}

// ensureMMRVectors backfills missing vectors from durable memory rows so the diversity pass can still run on hits produced by multiple retrieval channels.
// ensureMMRVectors 用于从长期记忆行回填缺失向量，让多通道召回后的命中仍能执行多样性重排。
func (u *MemoryUseCase) ensureMMRVectors(ctx context.Context, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || u.memories == nil || len(hits) == 0 {
		return hits
	}
	missingIDs := make([]uint64, 0, len(hits))
	for _, hit := range hits {
		if hit.MemoryRef.ID == 0 || len(hit.Vector) > 0 {
			continue
		}
		missingIDs = append(missingIDs, hit.MemoryRef.ID)
	}
	if len(missingIDs) == 0 {
		return hits
	}
	rows, err := u.memories.LoadMemoryNodesByIDs(ctx, missingIDs)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory mmr vector backfill degraded", "candidate_count", len(missingIDs), "err", err)
		}
		return hits
	}
	now := time.Now().UTC()
	byID := make(map[uint64][]float32, len(rows))
	for _, row := range rows {
		if !memoryNodeRecordIsActiveUnexpiredAt(row, now) {
			continue
		}
		if len(row.Vector) == 0 {
			continue
		}
		byID[row.ID] = append([]float32(nil), row.Vector...)
	}
	for idx := range hits {
		if len(hits[idx].Vector) > 0 {
			continue
		}
		if vector, ok := byID[hits[idx].MemoryRef.ID]; ok {
			hits[idx].Vector = vector
		}
	}
	return hits
}

// maxMMRSimilarity returns the largest cosine similarity between one candidate and the already selected set.
// maxMMRSimilarity 用于返回某个候选与已选择集合之间的最大余弦相似度。
func maxMMRSimilarity(candidate MemoryQueryHit, selected []MemoryQueryHit) float64 {
	if len(candidate.Vector) == 0 || len(selected) == 0 {
		return 0
	}
	best := 0.0
	for _, chosen := range selected {
		score := cosineSimilarityFloat32(candidate.Vector, chosen.Vector)
		if score > best {
			best = score
		}
	}
	return best
}

// cosineSimilarityFloat32 computes a safe cosine similarity for two candidate vectors so MMR can compare near-duplicate memories from different retrieval channels.
// cosineSimilarityFloat32 用于为两条候选向量计算安全的余弦相似度，让 MMR 可以比较不同检索通道中的近重复记忆。
func cosineSimilarityFloat32(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	dot := 0.0
	normA := 0.0
	normB := 0.0
	for idx := range a {
		av := float64(a[idx])
		bv := float64(b[idx])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA <= 0 || normB <= 0 {
		return 0
	}
	return dot / math.Sqrt(normA*normB)
}

// rerankSearchHits reorders the first-stage vector hits with the configured rerank backend and degrades to the original ordering on provider failures.
// rerankSearchHits 用于使用已配置的 rerank 后端重排首轮向量命中，并在 provider 失败时降级回原始顺序。
func (u *MemoryUseCase) rerankSearchHits(ctx context.Context, query string, hits []MemoryQueryHit) []MemoryQueryHit {
	if u == nil || u.reranker == nil || len(hits) <= 1 {
		return hits
	}
	limit := u.rerankTopN
	if limit <= 0 || limit > len(hits) {
		limit = len(hits)
	}
	primary := append([]MemoryQueryHit(nil), hits[:limit]...)
	results, err := u.reranker.Rerank(ctx, strings.TrimSpace(query), buildRerankDocuments(primary), len(primary))
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory search rerank degraded to rerank-disabled fallback", append(memoryQueryLogFields(u.logger, query), "candidate_count", len(primary), "fallback_mode", "rerank_disabled", "err", err)...)
		}
		return hits
	}
	if len(results) == 0 {
		return hits
	}

	// Rewrite the provider-ranked subset with rerank scores first, then append any untouched tail candidates so the caller still receives a stable result count.
	// 先按 provider 重排并写回被 rerank 的子集分数，再追加未触及的尾部候选，保证调用方仍拿到稳定数量的结果。
	byID := make(map[uint64]MemoryQueryHit, len(primary))
	for _, hit := range primary {
		byID[hit.MemoryRef.ID] = hit
	}
	ordered := make([]MemoryQueryHit, 0, len(hits))
	seen := make(map[uint64]struct{}, len(results))
	for _, result := range results {
		memoryID, err := strconv.ParseUint(strings.TrimSpace(result.ID), 10, 64)
		if err != nil {
			continue
		}
		hit, ok := byID[memoryID]
		if !ok {
			continue
		}
		hit.Score = result.Score
		hit.Origin = normalizeRerankedOrigin(hit.Origin)
		ordered = append(ordered, hit)
		seen[memoryID] = struct{}{}
	}
	for _, hit := range primary {
		if _, ok := seen[hit.MemoryRef.ID]; ok {
			continue
		}
		ordered = append(ordered, hit)
	}
	if limit < len(hits) {
		ordered = append(ordered, hits[limit:]...)
	}
	u.logMemorySearchStage(ctx, "memory search rerank stage completed", query, []any{
		"rerank_input_count", len(primary),
		"rerank_output_count", len(ordered),
	}, map[string]any{
		"top_input_candidate":  summarizeMemoryQueryHitForLog(firstMemoryQueryHit(primary)),
		"top_output_candidate": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(ordered)),
	})
	return ordered
}

// normalizeRerankedOrigin upgrades the hit origin label once a later rerank model has re-evaluated the candidate order.
// normalizeRerankedOrigin 用于在后续 rerank 模型重新评估候选顺序后，升级命中来源标签。
func normalizeRerankedOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	switch origin {
	case "hybrid_rrf":
		return "hybrid_rrf_rerank"
	case "lexical_search":
		return "lexical_rerank"
	case "vector_search":
		return "vector_rerank"
	default:
		if origin == "" {
			return "rerank"
		}
		return origin
	}
}

// normalizeMMROrigin upgrades the hit origin label once the diversity pass has re-ordered the candidate list.
// normalizeMMROrigin 用于在多样性重排改写候选顺序后，升级命中来源标签。
func normalizeMMROrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	switch origin {
	case "hybrid_rrf_rerank":
		return "hybrid_rrf_rerank_mmr"
	case "hybrid_rrf":
		return "hybrid_rrf_mmr"
	case "lexical_rerank":
		return "lexical_rerank_mmr"
	case "lexical_search":
		return "lexical_mmr"
	case "vector_rerank":
		return "vector_rerank_mmr"
	case "vector_search":
		return "vector_mmr"
	default:
		if origin == "" {
			return "mmr"
		}
		if strings.HasSuffix(origin, "_mmr") {
			return origin
		}
		return origin + "_mmr"
	}
}

// buildRerankDocuments converts one mapped-hit slice into the compact text fragments expected by the rerank provider.
// buildRerankDocuments 用于把补全后的命中切片转换成 rerank provider 期望的紧凑文本片段。
func buildRerankDocuments(hits []MemoryQueryHit) []appports.RerankerDocument {
	if len(hits) == 0 {
		return nil
	}
	docs := make([]appports.RerankerDocument, 0, len(hits))
	for _, hit := range hits {
		text := buildMemorySearchCandidateText(hit)
		if hit.MemoryRef.ID == 0 || strings.TrimSpace(text) == "" {
			continue
		}
		docs = append(docs, appports.RerankerDocument{
			ID:   strconv.FormatUint(hit.MemoryRef.ID, 10),
			Text: text,
		})
	}
	return docs
}

// buildMemorySearchCandidateText merges abstract and details into one rerank document while avoiding empty duplicated text.
// buildMemorySearchCandidateText 用于把 abstract 和 details 合并成一条 rerank 文档，同时避免空文本和重复文本。
func buildMemorySearchCandidateText(hit MemoryQueryHit) string {
	abstract := strings.TrimSpace(hit.Abstract)
	details := strings.TrimSpace(hit.DetailsPreview)
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

// memoryQueryLogFields converts one live search query into either a redacted diagnostic summary or a full debug payload according to the shared payload-debug logger switch.
// memoryQueryLogFields 用于根据共享 payload 调试开关，把实时检索 query 转成脱敏诊断字段或完整调试正文。
func memoryQueryLogFields(logger *logx.Logger, query string) []any {
	normalized := strings.TrimSpace(query)
	if logger != nil && logger.PayloadDebugEnabled() {
		return []any{"query", normalized}
	}
	return []any{
		"query_len", len(normalized),
		"query_sha256", shortLogDigest(normalized),
	}
}

// memoryQueryHitLogPayload stores one compact search-hit snapshot for diagnostics so operators can see the best candidate without dumping the full hit list every time.
// memoryQueryHitLogPayload 用于保存一条紧凑的检索命中快照，便于运维看到最佳候选，而不用每次都把整份命中列表全部打进日志。
type memoryQueryHitLogPayload struct {
	VectorID       string   `json:"vector_id,omitempty"`
	MemoryID       uint64   `json:"memory_id,omitempty"`
	SourceTurnID   uint64   `json:"source_turn_id,omitempty"`
	Score          float64  `json:"score"`
	RawScore       *float64 `json:"raw_score,omitempty"`
	Origin         string   `json:"origin,omitempty"`
	Abstract       string   `json:"abstract,omitempty"`
	DetailsPreview string   `json:"details_preview,omitempty"`
	TextPreview    string   `json:"text_preview,omitempty"`
}

// logMemorySearchStage records one searchable retrieval checkpoint so operators can tell whether a miss happened in vector recall, hybrid fusion, rerank, or later pruning.
// logMemorySearchStage 用于记录一条可检索的检索阶段检查点，让运维可以分辨未命中是发生在向量召回、混合融合、rerank 还是更后面的裁剪步骤。
func (u *MemoryUseCase) logMemorySearchStage(ctx context.Context, message, query string, fields []any, payload any) {
	if u == nil || u.logger == nil {
		return
	}
	baseFields := []any{
		"trace_id", trace.IDFromContext(ctx),
	}
	baseFields = append(baseFields, memoryQueryLogFields(u.logger, query)...)
	baseFields = append(baseFields, fields...)
	baseFields = u.logger.AppendPayloadFields(baseFields, "stage_payload", payload)
	u.logger.Info(message, baseFields...)
}

// firstRawMemoryHit returns the first raw vector-store hit when one exists so the caller can summarize the top ANN candidate safely.
// firstRawMemoryHit 用于在存在结果时返回第一条原始向量命中，让调用方可以安全地概括最靠前的 ANN 候选。
func firstRawMemoryHit(hits []logicdomain.MemoryHit) *logicdomain.MemoryHit {
	if len(hits) == 0 {
		return nil
	}
	return &hits[0]
}

// firstMemoryQueryHit returns the first mapped public hit when one exists so stage logs can surface the current top candidate after each ranking step.
// firstMemoryQueryHit 用于在存在结果时返回第一条公开命中，让阶段日志能在每个排序步骤后展示当前 top candidate。
func firstMemoryQueryHit(hits []MemoryQueryHit) *MemoryQueryHit {
	if len(hits) == 0 {
		return nil
	}
	return &hits[0]
}

// summarizeRawMemoryHitForLog converts one raw vector-store hit into a compact log payload that keeps only the fields needed to diagnose recall quality.
// summarizeRawMemoryHitForLog 用于把一条原始向量命中转换成紧凑日志载荷，只保留排查召回质量所需的字段。
func summarizeRawMemoryHitForLog(hit *logicdomain.MemoryHit) *memoryQueryHitLogPayload {
	if hit == nil {
		return nil
	}
	return &memoryQueryHitLogPayload{
		VectorID:    strings.TrimSpace(hit.ID),
		Score:       hit.Score,
		RawScore:    memoryHitRawScoreForLog(hit.Metadata),
		TextPreview: strings.TrimSpace(hit.Text),
	}
}

// summarizeMemoryQueryHitForLog converts one mapped hit into a compact log payload so later filters can still report “the best available memory” even when nothing survives.
// summarizeMemoryQueryHitForLog 用于把一条映射后的命中转换成紧凑日志载荷，使后续过滤即便把所有候选都裁掉，日志里仍能报告“最接近的那条记忆”。
func summarizeMemoryQueryHitForLog(hit *MemoryQueryHit) *memoryQueryHitLogPayload {
	if hit == nil {
		return nil
	}
	return &memoryQueryHitLogPayload{
		MemoryID:       hit.MemoryRef.ID,
		SourceTurnID:   hit.SourceRef.ID,
		Score:          hit.Score,
		Origin:         strings.TrimSpace(hit.Origin),
		Abstract:       strings.TrimSpace(hit.Abstract),
		DetailsPreview: strings.TrimSpace(hit.DetailsPreview),
	}
}

// memoryHitRawScoreForLog restores the original SQL-side fused score from metadata so operators can distinguish the stable caller-facing score from the raw internal RRF value.
// memoryHitRawScoreForLog 用于从 metadata 里恢复 SQL 侧原始融合分，让运维可以区分稳定的调用方分数与内部 RRF 原始值。
func memoryHitRawScoreForLog(metadata map[string]string) *float64 {
	raw := strings.TrimSpace(metadata["raw_score"])
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &value
}

// shortLogDigest produces one short stable digest for sensitive runtime strings so logs can correlate repeated failures without leaking the underlying text.
// shortLogDigest 用于为敏感运行时字符串生成短且稳定的摘要，让日志能够关联重复故障，同时不泄露底层文本。
func shortLogDigest(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// mapSearchHits enriches vector hits with relational unified-memory rows so the search response can return durable memory refs instead of bare turn anchors.
// mapSearchHits 用于用关系层统一记忆行补全向量命中，从而让搜索响应返回长期 memory ref，而不是裸 turn 锚点。
func (u *MemoryUseCase) mapSearchHits(ctx context.Context, hits []logicdomain.MemoryHit) ([]MemoryQueryHit, error) {
	vectorIDs := collectVectorIDs(hits)
	if len(vectorIDs) == 0 {
		return []MemoryQueryHit{}, nil
	}
	rows, err := u.memories.LoadMemoryNodesByVectorIDs(ctx, vectorIDs)
	if err != nil {
		return nil, err
	}
	byVectorID := make(map[string]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		byVectorID[strings.TrimSpace(row.VectorID)] = row
	}
	mapped := make([]MemoryQueryHit, 0, len(hits))
	for _, hit := range hits {
		row, ok := byVectorID[strings.TrimSpace(hit.ID)]
		if !ok {
			continue
		}
		mappedHit := MemoryQueryHit{
			MemoryRef: logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeMemory,
				ID:   row.ID,
			},
			SourceKind:               row.SourceKind,
			ScopeLevel:               row.ScopeLevel,
			Priority:                 row.Priority,
			MemoryLevel:              row.MemoryLevel,
			RefreshWeight:            row.RefreshWeight,
			SessionID:                chooseSearchSessionID(hit, row),
			Abstract:                 strings.TrimSpace(row.Abstract),
			DetailsPreview:           strings.TrimSpace(row.Details),
			Category:                 row.Category,
			Score:                    hit.Score,
			Origin:                   rawMemoryHitOrigin(hit),
			Vector:                   append([]float32(nil), row.Vector...),
			CreatedAt:                row.CreatedAt,
			LastRecalledAt:           row.LastRecalledAt,
			LastAdoptedAt:            row.LastAdoptedAt,
			LastReinforcedAt:         row.LastReinforcedAt,
			SupportCount:             row.SupportCount,
			RebuttalCount:            row.RebuttalCount,
			ReinforcementCount:       row.ReinforcementCount,
			CrossSessionAdoptedCount: row.CrossSessionAdoptedCount,
			DecayDisabled:            row.DecayDisabled,
		}
		if row.SourceTurnID > 0 {
			mappedHit.SourceRef = logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeTurn,
				ID:   row.SourceTurnID,
			}
		}
		mapped = append(mapped, mappedHit)
	}
	sort.SliceStable(mapped, func(i, j int) bool {
		if mapped[i].Score == mapped[j].Score {
			return mapped[i].MemoryRef.ID < mapped[j].MemoryRef.ID
		}
		return mapped[i].Score > mapped[j].Score
	})
	return mapped, nil
}

// normalizeCombinedHybridSQLHits rewrites SQL-level raw RRF scores into the stable 0..1 score contract expected by downstream thresholding, while preserving the original fused score for diagnostics.
// normalizeCombinedHybridSQLHits 用于把 SQL 层原始 RRF 分数改写成下游阈值链路期望的稳定 0..1 分数契约，同时保留原始融合分供诊断使用。
func normalizeCombinedHybridSQLHits(hits []logicdomain.MemoryHit) []logicdomain.MemoryHit {
	if len(hits) == 0 {
		return []logicdomain.MemoryHit{}
	}
	normalized := append([]logicdomain.MemoryHit(nil), hits...)
	total := len(normalized)
	for idx := range normalized {
		rawScore := normalized[idx].Score
		if normalized[idx].Metadata == nil {
			normalized[idx].Metadata = make(map[string]string, 1)
		}
		normalized[idx].Metadata["raw_score"] = strconv.FormatFloat(rawScore, 'f', -1, 64)

		// Convert the SQL-fused rank order into the same stable reviewer-facing score semantics used by the app-side fusion path,
		// so combined PostgreSQL search does not leak tiny reciprocal-rank values into logs, thresholds, or final responses.
		// 把 SQL 融合后的排序顺序转换成与应用层融合路径一致的稳定 reviewer 分数语义，
		// 避免 PostgreSQL 组合检索把极小的 reciprocal-rank 原始值泄露到日志、阈值判断或最终响应中。
		normalized[idx].Score = normalizedRankScore(idx+1, total)
	}
	return normalized
}

// rawMemoryHitOrigin extracts the preferred ranking-origin label carried by one raw hit and falls back to the legacy vector label when older backends do not populate metadata.
// rawMemoryHitOrigin 用于提取原始命中携带的排序来源标签；当旧后端未填充 metadata 时，则回退到传统的 vector 标签。
func rawMemoryHitOrigin(hit logicdomain.MemoryHit) string {
	if origin := strings.TrimSpace(hit.Metadata["origin"]); origin != "" {
		return origin
	}
	return "vector_search"
}

// loadTurnDetails batches one turn-id list into ordered turn-detail records plus neighboring turn ids.
// loadTurnDetails 用于把一组 turn id 批量读取成有序的 turn 详情记录，并补齐相邻 turn 编号。
func (u *MemoryUseCase) loadTurnDetails(ctx context.Context, requested []uint64) (TurnDetailResult, error) {
	rows, err := u.memories.LoadTurnsByIDs(ctx, requested)
	if err != nil {
		return TurnDetailResult{}, err
	}
	windows, err := u.memories.LoadTurnWindows(ctx, requested, turnDetailContextRadius)
	if err != nil {
		return TurnDetailResult{}, err
	}
	byID := make(map[uint64]logicdomain.SessionTurnRecord, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]TurnDetailRecord, 0, len(rows))
	for _, turnID := range requested {
		if row, ok := byID[turnID]; ok {
			userContent, timeline, assistantContent := parseDehydratedTurnContent(row.DehydratedContent)
			window := windows[turnID]
			ordered = append(ordered, TurnDetailRecord{
				Turn:             row,
				UserContent:      userContent,
				Timeline:         timeline,
				AssistantContent: assistantContent,
				PreviousTurnIDs:  append([]uint64(nil), window.PreviousTurnIDs...),
				NextTurnIDs:      append([]uint64(nil), window.NextTurnIDs...),
			})
		}
	}
	return TurnDetailResult{Turns: ordered}, nil
}

// normalizeMemoryRefs removes duplicate refs while preserving caller order so mixed detail lookups stay deterministic.
// normalizeMemoryRefs 用于在保持调用方顺序的同时去掉重复引用，确保混合详情查询保持确定性。
func normalizeMemoryRefs(refs []logicdomain.MemoryRef) []logicdomain.MemoryRef {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(refs))
	normalized := make([]logicdomain.MemoryRef, 0, len(refs))
	for _, ref := range refs {
		if ref.ID == 0 || !logicdomain.ValidMemoryRefType(ref.Type) {
			continue
		}
		key := strconv.Itoa(ref.Type) + ":" + strconv.FormatUint(ref.ID, 10)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, ref)
	}
	return normalized
}

// collectVectorIDs removes empty and duplicate vector ids before the relational enrichment query starts.
// collectVectorIDs 用于在关系补全查询开始前去掉空值和重复的 vector id。
func collectVectorIDs(hits []logicdomain.MemoryHit) []string {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hits))
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		id := strings.TrimSpace(hit.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// chooseSearchSessionID prefers the vector hit session id and falls back to the relational origin session when needed.
// chooseSearchSessionID 用于优先返回向量命中的 session id，并在缺失时回退到关系层 origin session。
func chooseSearchSessionID(hit logicdomain.MemoryHit, row logicdomain.MemoryNodeRecord) uint64 {
	if hit.Filter.SessionID > 0 {
		return hit.Filter.SessionID
	}
	return row.OriginSessionID
}

// normalizeTurnIDList removes duplicates while preserving input order so turn-detail lookups remain deterministic for callers.
// normalizeTurnIDList 用于在保持输入顺序的同时去掉重复项，确保 turn 详情查询对调用方保持确定性。
func normalizeTurnIDList(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(values))
	normalized := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

// parseDehydratedTurnContent expands the stored dehydrated JSON into direct user/assistant/timeline fields so callers do not have to decode it client-side.
// parseDehydratedTurnContent 用于把存储中的脱水 JSON 展开成直接可用的 user/assistant/timeline 字段，避免调用方再自行解码。
func parseDehydratedTurnContent(raw string) (string, []logicdomain.TurnDetailTimelineItem, string) {
	type dehydratedTurnTimelineItem struct {
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	type dehydratedTurnPayload struct {
		User      string                       `json:"user"`
		Timeline  []dehydratedTurnTimelineItem `json:"timeline"`
		Assistant string                       `json:"assistant"`
	}

	payload := dehydratedTurnPayload{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err != nil {
		return "", nil, ""
	}
	timeline := make([]logicdomain.TurnDetailTimelineItem, 0, len(payload.Timeline))
	for _, item := range payload.Timeline {
		timeline = append(timeline, logicdomain.TurnDetailTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: item.Content,
		})
	}
	return payload.User, timeline, payload.Assistant
}

// normalizeWriteMemoryItem fills direct-write defaults so tool callers can omit optional lifecycle controls without losing deterministic persistence behavior.
// normalizeWriteMemoryItem 用于补齐主动写记忆默认值，让工具调用方即使省略可选生命周期字段，也不会丢失确定性的持久化行为。
func normalizeWriteMemoryItem(item WriteMemoryItem, now time.Time) WriteMemoryItem {
	item.Abstract = strings.TrimSpace(item.Abstract)
	item.Details = strings.TrimSpace(item.Details)
	if !logicdomain.ValidMemoryScopeLevel(item.ScopeLevel) {
		item.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(item.Priority) {
		item.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(item.MemoryLevel) {
		switch item.ScopeLevel {
		case logicdomain.MemoryScopeLevelSession:
			item.MemoryLevel = logicdomain.MemoryLevelSession
		default:
			item.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if item.ExpiresAt.IsZero() {
		item.ExpiresAt = now.Add(defaultMemoryTTL(item.ScopeLevel))
	}
	return item
}

// defaultMemoryTTL returns the default retention window for one direct-write scope.
// defaultMemoryTTL 用于返回某个主动写记忆作用域的默认保留时长。
func defaultMemoryTTL(scopeLevel int) time.Duration {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return defaultSessionMemoryTTL
	case logicdomain.MemoryScopeLevelUser:
		return defaultUserMemoryTTL
	default:
		return defaultProjectMemoryTTL
	}
}

// buildDirectMemoryDedupeHash builds the current short-window soft-idempotency hash from the fields that define one semantically identical direct write.
// buildDirectMemoryDedupeHash 用于基于定义“同一短窗口内语义等价主动写入”的字段，构造当前版本的软幂等哈希。
func buildDirectMemoryDedupeHash(session logicdomain.SessionRef, item WriteMemoryItem, now time.Time) string {
	body := strings.Join([]string{
		strconv.Itoa(logicdomain.MemorySourceKindGRPCAIWrite),
		strconv.Itoa(item.ScopeLevel),
		strconv.FormatUint(session.SessionID, 10),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
		strconv.Itoa(item.Category),
		strconv.Itoa(item.Priority),
		strconv.Itoa(item.MemoryLevel),
		buildDirectMemoryExpiryDedupeSignature(item.ScopeLevel, item.ExpiresAt, now),
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// buildLegacyDirectMemoryDedupeHash preserves the pre-hardening hash layout so one short migration window can still dedupe rows written by older binaries when the semantic fields also match.
// buildLegacyDirectMemoryDedupeHash 用于保留加固前的旧哈希布局，确保升级后的短迁移窗口内，历史版本写出的行在语义字段一致时仍能继续命中幂等。
func buildLegacyDirectMemoryDedupeHash(session logicdomain.SessionRef, item WriteMemoryItem) string {
	body := strings.Join([]string{
		strconv.Itoa(logicdomain.MemorySourceKindGRPCAIWrite),
		strconv.Itoa(item.ScopeLevel),
		strconv.FormatUint(session.SessionID, 10),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// buildDirectMemoryRequestCollapseKey keeps one-RPC duplicate collapse slightly stricter than the storage dedupe hash by preserving the exact expiry timestamp inside the current batch.
// buildDirectMemoryRequestCollapseKey 用于让单个 RPC 内的重复折叠比存储层软幂等再严格一点：它会保留当前批次中的精确过期时间戳，避免同批显式不同过期点被误并。
func buildDirectMemoryRequestCollapseKey(item WriteMemoryItem) string {
	expiresAt := ""
	if !item.ExpiresAt.IsZero() {
		expiresAt = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	body := strings.Join([]string{
		strconv.Itoa(item.ScopeLevel),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
		strconv.Itoa(item.Category),
		strconv.Itoa(item.Priority),
		strconv.Itoa(item.MemoryLevel),
		expiresAt,
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// directWriteSoftDedupeMatchesMemoryRow validates whether one hot-path memory row is semantically equivalent to the incoming direct-write item before the legacy hash fallback is allowed to reuse it.
// directWriteSoftDedupeMatchesMemoryRow 用于在启用旧哈希兼容回退前，校验热路径记忆行是否与当前主动写入条目语义等价，避免继续复用旧版过粗的幂等键。
func directWriteSoftDedupeMatchesMemoryRow(row logicdomain.MemoryNodeRecord, item WriteMemoryItem, now time.Time) bool {
	if row.ID == 0 || row.SourceKind != logicdomain.MemorySourceKindGRPCAIWrite {
		return false
	}
	if row.ScopeLevel != item.ScopeLevel ||
		row.Category != item.Category ||
		row.Priority != item.Priority ||
		row.MemoryLevel != item.MemoryLevel {
		return false
	}
	if normalizeHashText(row.Abstract) != normalizeHashText(item.Abstract) ||
		normalizeHashText(row.Details) != normalizeHashText(item.Details) {
		return false
	}
	return buildStoredDirectMemoryExpiryDedupeSignature(row) == buildDirectMemoryExpiryDedupeSignature(item.ScopeLevel, item.ExpiresAt, now)
}

// buildStoredDirectMemoryExpiryDedupeSignature normalizes one persisted direct-write expiry into the same semantic signature used by the request-side soft-idempotency hash.
// buildStoredDirectMemoryExpiryDedupeSignature 用于把已持久化主动记忆的过期信息归一成与请求侧软幂等哈希相同的语义签名。
func buildStoredDirectMemoryExpiryDedupeSignature(row logicdomain.MemoryNodeRecord) string {
	return buildDirectMemoryExpiryDedupeSignature(row.ScopeLevel, row.ExpiresAt, row.CreatedAt)
}

// buildDirectMemoryExpiryDedupeSignature keeps default TTL-based writes stable across separate RPCs while still distinguishing explicit absolute-expiry writes from each other.
// buildDirectMemoryExpiryDedupeSignature 用于让基于默认 TTL 的主动写入在不同 RPC 之间仍保持稳定幂等，同时继续区分显式指定的绝对过期时间。
func buildDirectMemoryExpiryDedupeSignature(scopeLevel int, expiresAt, baseTime time.Time) string {
	if expiresAt.IsZero() {
		return "none"
	}
	defaultTTL := defaultMemoryTTL(scopeLevel)
	if !baseTime.IsZero() && durationWithin(expiresAt.Sub(baseTime), defaultTTL, time.Second) {
		return "ttl:" + strconv.FormatInt(int64(defaultTTL/time.Second), 10)
	}
	return "at:" + expiresAt.UTC().Format(time.RFC3339Nano)
}

// durationWithin provides a tiny tolerance so timestamp serialization or database precision cannot break equality for TTL-derived expiries.
// durationWithin 用于提供一个很小的容差，避免时间序列化或数据库精度差异打破基于 TTL 的过期时间等价判断。
func durationWithin(left, right, tolerance time.Duration) bool {
	delta := left - right
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// normalizeHashText keeps soft-idempotency stable across harmless whitespace drift while still distinguishing materially different content.
// normalizeHashText 用于在软幂等中吸收无害的空白差异，同时保留对实质内容变化的区分能力。
func normalizeHashText(text string) string {
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(text)))
	return strings.Join(fields, " ")
}

// buildDirectMemoryFilter derives the vector filter written onto direct-memory rows according to their declared scope level.
// buildDirectMemoryFilter 用于根据主动记忆声明的作用域等级，推导写入向量行时携带的过滤条件。
func buildDirectMemoryFilter(session logicdomain.SessionRef, scopeLevel int) logicdomain.SearchFilter {
	filter := logicdomain.SearchFilter{
		UserID:    session.UserID,
		TeamID:    session.TeamID,
		SpaceID:   session.SpaceID,
		ProjectID: session.ProjectID,
	}
	if scopeLevel == logicdomain.MemoryScopeLevelSession {
		filter.SessionID = session.SessionID
	}
	return filter
}
