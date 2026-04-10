// memory_query.go keeps the unified memory usecase contracts, constructor, and runtime knobs used by the app-layer gRPC memory workflow.
// memory_query.go 用于承载应用层统一记忆用例在 gRPC memory 工作流中使用的契约定义、构造入口与运行时配置旋钮。
package usecase

import (
	"context"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
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
