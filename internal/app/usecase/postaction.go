// postaction.go keeps the core post-action contracts, runtime wiring, and shared state used by the async post-action pipeline.
// postaction.go 用于承载异步 post-action 流水线共享的核心契约、运行时装配入口与状态定义。
package usecase

import (
	"context"
	"sync"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

// PostActionTimelineItem carries one validated timeline message that sits between the first user prompt and final assistant reply.
// PostActionTimelineItem 用于承载一条已经校验过的 timeline 中间消息，它位于首轮用户提问和最终助手回答之间。
type PostActionTimelineItem struct {
	Type    string
	Content string
}

// PostActionCommand carries the resolved session scope plus the new text-only post-action payload.
// PostActionCommand 用于承载已解析的 session 范围以及新的纯文本 post-action 载荷。
type PostActionCommand struct {
	Session             logicdomain.SessionRef
	UserContent         string
	AssistantContent    string
	Timeline            []PostActionTimelineItem
	RawUserContent      string
	RawAssistantContent string
	RawTimeline         []PostActionTimelineItem
}

// PostActionResult returns the post-action acknowledgement data back to the transport layer.
// PostActionResult 用于把 post-action 的确认结果返回给传输层。
type PostActionResult struct {
	Accepted bool
	TraceID  string
}

// PostActionExecutor is the interface consumed by the gRPC adapter to run the post-action workflow.
// PostActionExecutor 用于让 gRPC 适配层执行 post-action 工作流。
type PostActionExecutor interface {
	Execute(ctx context.Context, cmd PostActionCommand) (PostActionResult, error)
}

// PostActionTurnAnalyzer is the tiny port used by post-action background workers to analyze one persisted turn after it has been durably queued.
// PostActionTurnAnalyzer 用于让 post-action 后台工作器在 turn 稳定落库并入队后，再对这一轮执行提炼分析。
type PostActionTurnAnalyzer interface {
	// Analyze runs one single-turn extraction against the persisted post-action turn input.
	// Analyze 用于针对已持久化的 post-action 单轮输入执行一次提炼分析。
	Analyze(ctx context.Context, input logicdomain.TurnAnalysisInput) (logicdomain.TurnAnalysis, error)
	// AnalyzeModel returns the configured post-action first-stage model label so failure logs can identify which model produced malformed output.
	// AnalyzeModel 用于返回当前 post-action 第一层使用的模型标识，便于失败日志定位是哪一个模型产出了畸形输出。
	AnalyzeModel() string
}

// PostActionMemorySearcher is the narrow search port used by post-action duplicate review to recall existing durable memories inside the configured shared scope.
// PostActionMemorySearcher 用于给 post-action 重复评审提供一个狭窄检索端口，在配置好的共享范围内召回既有长期记忆。
type PostActionMemorySearcher interface {
	Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error)
}

// PostActionCandidateReviewer is the unified reviewer port that jointly decides memory dedupe admission and profile acceptance in one LLM call.
// PostActionCandidateReviewer 用于表示统一 reviewer 端口，在一次 LLM 调用里同时决定记忆去重准入和画像接纳结果。
type PostActionCandidateReviewer interface {
	Review(ctx context.Context, input logicdomain.PostActionCandidateReviewInput) (logicdomain.PostActionCandidateReviewResult, error)
}

// PostActionAnalysisConfig carries the queue and history knobs used by the async single-turn extraction pipeline,
// while keeping a few legacy threshold fields for backward-compatible configuration parsing.
// PostActionAnalysisConfig 用于承载异步单轮提炼流水线的队列与历史窗口参数，
// 并保留少量旧阈值字段以兼容既有配置解析。
type PostActionAnalysisConfig struct {
	TurnThreshold             int
	TokenThreshold            int
	IdleTimeout               time.Duration
	HistoryTurns              int
	MaxInputTokens            int
	QueueScanInterval         time.Duration
	DedupeSearchTopK          int
	MemoryReplaceScope        string
	DedupeSearchScope         string
	DedupeMinSimilarity       float64
	HardDedupeCosineThreshold float64
}

// PostActionUseCase stores one cleaned turn, queues asynchronous single-turn extraction work,
// and coordinates the maintenance tasks that keep long-lived profile state in sync.
// PostActionUseCase 用于存储一条清洗后的 turn、把后续单轮提炼排入异步工作器，
// 并协调长期画像状态保持同步所需的维护任务。
type PostActionUseCase struct {
	noiseGate               appports.NoiseTurnFilter
	store                   appports.RelationalStore
	embedding               appports.EmbeddingClient
	vector                  appports.VectorStore
	turnAnalyzer            PostActionTurnAnalyzer
	memorySearcher          PostActionMemorySearcher
	candidateReviewer       PostActionCandidateReviewer
	analysisCfg             PostActionAnalysisConfig
	logger                  *logx.Logger
	piiScrubber             PIIScrubber
	queueCtx                context.Context
	queueCancel             context.CancelFunc
	queueWG                 sync.WaitGroup
	queueMu                 sync.Mutex
	maintenanceMu           sync.Mutex
	maintenanceBackoffUntil time.Time
	queueCh                 chan uint64
	queueState              map[uint64]*postActionQueueState
	deferredQueueIDs        []uint64
	deferredQueueSet        map[uint64]struct{}
}

// NewPostActionUseCase creates a PostActionUseCase instance for the runtime asynchronous single-turn path.
// NewPostActionUseCase 用于为运行时异步单轮提炼路径创建 PostActionUseCase 实例。
func NewPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, embedding appports.EmbeddingClient, vector appports.VectorStore, turnAnalyzer PostActionTurnAnalyzer, memorySearcher PostActionMemorySearcher, candidateReviewer PostActionCandidateReviewer, analysisCfg PostActionAnalysisConfig, logger *logx.Logger) *PostActionUseCase {
	return newPostActionUseCase(noiseGate, store, embedding, vector, turnAnalyzer, memorySearcher, candidateReviewer, analysisCfg, logger, true)
}

// newPostActionUseCase builds the post-action use case and optionally starts the async queue worker for runtime paths.
// newPostActionUseCase 用于构建 post-action 用例，并按需启动运行时异步队列工作器。
func newPostActionUseCase(noiseGate appports.NoiseTurnFilter, store appports.RelationalStore, embedding appports.EmbeddingClient, vector appports.VectorStore, turnAnalyzer PostActionTurnAnalyzer, memorySearcher PostActionMemorySearcher, candidateReviewer PostActionCandidateReviewer, analysisCfg PostActionAnalysisConfig, logger *logx.Logger, startWorker bool) *PostActionUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	if analysisCfg.TurnThreshold < 0 {
		analysisCfg.TurnThreshold = 0
	}
	if analysisCfg.TokenThreshold < 0 {
		analysisCfg.TokenThreshold = 0
	}
	if analysisCfg.IdleTimeout < 0 {
		analysisCfg.IdleTimeout = 0
	}
	if analysisCfg.HistoryTurns < 0 {
		analysisCfg.HistoryTurns = 0
	}
	if analysisCfg.MaxInputTokens < 0 {
		analysisCfg.MaxInputTokens = 0
	}
	if analysisCfg.QueueScanInterval <= 0 {
		analysisCfg.QueueScanInterval = 30 * time.Second
	}
	if analysisCfg.DedupeSearchTopK <= 0 {
		analysisCfg.DedupeSearchTopK = defaultPreCheckTopK
	}
	if normalizeConfigToken(analysisCfg.MemoryReplaceScope) == "" {
		analysisCfg.MemoryReplaceScope = analysisCfg.DedupeSearchScope
	}
	analysisCfg.MemoryReplaceScope = normalizeMemoryReplaceScope(analysisCfg.MemoryReplaceScope)
	if analysisCfg.DedupeMinSimilarity < 0 || analysisCfg.DedupeMinSimilarity > 1 {
		analysisCfg.DedupeMinSimilarity = 0.80
	}
	if analysisCfg.HardDedupeCosineThreshold < 0 || analysisCfg.HardDedupeCosineThreshold > 1 {
		analysisCfg.HardDedupeCosineThreshold = 0.99
	}
	uc := &PostActionUseCase{
		noiseGate:         noiseGate,
		store:             store,
		embedding:         embedding,
		vector:            vector,
		turnAnalyzer:      turnAnalyzer,
		memorySearcher:    memorySearcher,
		candidateReviewer: candidateReviewer,
		analysisCfg:       analysisCfg,
		logger:            logger,
	}
	if startWorker && store != nil {
		uc.startQueueWorker()
	}
	return uc
}

// ConfigurePIIScrubber injects the shared PII scrubber used to redact canonical post-action payloads, raw replay payloads, and analyzer-facing context before the workflow touches storage or LLM-backed stages.
// ConfigurePIIScrubber 用于注入共享 PII 脱敏器，让 post-action 在访问存储或进入 LLM 阶段前，先对标准载荷、raw 重放载荷以及分析器上下文完成脱敏。
func (u *PostActionUseCase) ConfigurePIIScrubber(scrubber PIIScrubber) {
	if u == nil {
		return
	}
	u.piiScrubber = scrubber
}
