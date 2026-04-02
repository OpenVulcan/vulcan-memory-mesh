// memory_query.go implements unified memory search, detail lookup, and direct-write flows exposed by the gRPC memory surface.
// memory_query.go 用于实现 gRPC 记忆接口暴露的统一记忆检索、详情查询和主动写入流程。
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

	// maxMemoryQueryItems bounds the JSON query group size so one request cannot fan out into an unbounded number of vector searches.
	// maxMemoryQueryItems 用于限制 JSON 查询组的条目数量，避免一次请求扩散成无限制的向量检索。
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

// MemoryQueryCommand carries one grouped JSON search payload together with the resolved user/project selectors.
// MemoryQueryCommand 用于承载一份分组 JSON 搜索载荷，以及解析范围所需的 user/project 选择参数。
type MemoryQueryCommand struct {
	UserID    uint64
	ProjectID uint64
	QueryJSON string
	TopK      int
}

// MemoryQueryItem stores one parsed JSON query-group item before embedding and vector search begin.
// MemoryQueryItem 用于在 embedding 和向量检索开始前保存一条已解析的 JSON 查询组条目。
type MemoryQueryItem struct {
	Background string `json:"background"`
	Query      string `json:"query"`
}

// MemoryQueryHit returns one unified recalled memory candidate together with its durable refs and preview fields.
// MemoryQueryHit 用于返回一条统一召回的记忆候选，以及它的长期引用和预览字段。
type MemoryQueryHit struct {
	MemoryRef      logicdomain.MemoryRef
	SourceRef      logicdomain.MemoryRef
	SourceKind     int
	ScopeLevel     int
	SessionID      uint64
	Abstract       string
	DetailsPreview string
	Category       int
	Score          float64
}

// MemoryQueryGroupResult returns the echoed JSON query item together with the hit list produced for that item.
// MemoryQueryGroupResult 用于返回被原样回显的 JSON 查询条目，以及针对该条目生成的命中结果。
type MemoryQueryGroupResult struct {
	QueryIndex int
	Background string
	Query      string
	Hits       []MemoryQueryHit
}

// MemoryQueryResult returns the resolved user/project targets plus all grouped vector-search results.
// MemoryQueryResult 用于返回已解析的 user/project 目标，以及全部分组向量检索结果。
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

// MemoryUseCase orchestrates grouped memory search, mixed detail lookup, and direct AI memory writes on top of profile-target resolution and unified relational memory rows.
// MemoryUseCase 用于在画像目标解析和统一关系记忆行之上，编排分组记忆检索、混合详情查询以及 AI 主动写记忆流程。
type MemoryUseCase struct {
	profiles   appports.ProfileStore
	memories   appports.MemoryStore
	embedding  appports.EmbeddingClient
	reranker   appports.RerankerClient
	rerankTopN int
	vector     appports.VectorStore
	logger     *logx.Logger
}

// NewMemoryUseCase creates a MemoryUseCase instance.
// NewMemoryUseCase 用于创建 MemoryUseCase 实例。
func NewMemoryUseCase(profiles appports.ProfileStore, memories appports.MemoryStore, embedding appports.EmbeddingClient, vector appports.VectorStore, logger *logx.Logger) *MemoryUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &MemoryUseCase{
		profiles:   profiles,
		memories:   memories,
		embedding:  embedding,
		rerankTopN: defaultMemorySearchTopK,
		vector:     vector,
		logger:     logger,
	}
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

// Search resolves the concrete project/user scope, parses the grouped JSON payload, embeds each item, and returns enriched unified memory refs.
// Search 用于解析具体的 project/user 范围、解析分组 JSON 载荷、对每条输入做 embedding，并返回补全后的统一记忆引用。
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

	// Parse the grouped JSON payload once, then embed each composed search text in order so the result can be echoed back item-by-item.
	// 先一次性解析分组 JSON 载荷，再按顺序对每条组合后的搜索文本做 embedding，方便逐项原样回显结果。
	items, err := parseMemoryQueryJSON(cmd.QueryJSON)
	if err != nil {
		return MemoryQueryResult{}, err
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, buildMemorySearchText(item))
	}
	embedResp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Texts: texts})
	if err != nil {
		return MemoryQueryResult{}, err
	}
	if len(embedResp.Vectors) != len(items) {
		return MemoryQueryResult{}, fmt.Errorf("embedding result count mismatch: got %d want %d", len(embedResp.Vectors), len(items))
	}

	// Keep vector recall inside the resolved project hierarchy and enrich the returned vector rows with relational memory refs.
	// 将向量召回限制在已解析项目层级内，并使用关系记忆行补全向量结果中的长期引用。
	filter := logicdomain.SearchFilter{
		UserID:    userTarget.UserID,
		TeamID:    projectTarget.TeamID,
		SpaceID:   projectTarget.SpaceID,
		ProjectID: projectTarget.ProjectID,
	}
	topK := normalizeMemorySearchTopK(cmd.TopK)
	results := make([]MemoryQueryGroupResult, 0, len(items))
	for idx, item := range items {
		hits, err := u.vector.Search(ctx, embedResp.Vectors[idx], topK, filter)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		mapped, err := u.mapSearchHits(ctx, hits)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		mapped = u.rerankSearchHits(ctx, buildMemorySearchText(item), mapped)
		results = append(results, MemoryQueryGroupResult{
			QueryIndex: idx,
			Background: item.Background,
			Query:      item.Query,
			Hits:       mapped,
		})
	}
	return MemoryQueryResult{
		UserTarget:    userTarget,
		ProjectTarget: projectTarget,
		Results:       results,
	}, nil
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

	results := make([]WriteMemoryResultItem, 0, len(items))
	for _, item := range items {
		dedupeHash := buildDirectMemoryDedupeHash(cmd.Session, item)
		existing, ok, err := u.memories.FindRecentActiveMemoryByDedupe(
			ctx,
			cmd.Session,
			logicdomain.MemorySourceKindGRPCAIWrite,
			item.ScopeLevel,
			dedupeHash,
			now.Add(-directMemoryDedupeWindow),
		)
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		if ok {
			results = append(results, WriteMemoryResultItem{
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

		vectors, err := embedPostActionTexts(ctx, u.embedding, []string{item.Abstract})
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		if len(vectors) != 1 {
			return WriteMemoriesResult{}, fmt.Errorf("embedding result count mismatch: got %d want 1", len(vectors))
		}
		vectorID, err := generatePostActionUUID()
		if err != nil {
			return WriteMemoriesResult{}, err
		}

		record := logicdomain.MemoryRecord{
			ID:     vectorID,
			Text:   item.Abstract,
			Vector: vectors[0],
			Filter: buildDirectMemoryFilter(cmd.Session, item.ScopeLevel),
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
			return WriteMemoriesResult{}, err
		}

		created, err := u.memories.CreateDirectMemoryNode(ctx, cmd.Session, logicdomain.MemoryNodeRecord{
			TeamID:          cmd.Session.TeamID,
			SpaceID:         cmd.Session.SpaceID,
			ProjectID:       cmd.Session.ProjectID,
			UserID:          cmd.Session.UserID,
			OriginSessionID: cmd.Session.SessionID,
			VectorID:        vectorID,
			Vector:          append([]float32(nil), vectors[0]...),
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
		})
		if err != nil {
			if _, rollbackErr := u.vector.DeleteByIDs(ctx, []string{vectorID}); rollbackErr != nil && u.logger != nil {
				u.logger.Error("direct memory vector rollback failed", "vector_id", vectorID, "session_id", cmd.Session.SessionID, "err", rollbackErr)
			}
			return WriteMemoriesResult{}, err
		}
		results = append(results, WriteMemoryResultItem{
			Ref: logicdomain.MemoryRef{
				Type: logicdomain.MemoryRefTypeMemory,
				ID:   created.ID,
			},
			SourceKind: created.SourceKind,
			ScopeLevel: created.ScopeLevel,
			Deduped:    false,
		})
	}
	return WriteMemoriesResult{Items: results}, nil
}

// validateMemoryQueryCommand checks the grouped vector-search request before any hierarchy, embedding, or vector work begins.
// validateMemoryQueryCommand 用于在任何层级解析、embedding 或向量检索开始前校验分组向量搜索请求。
func validateMemoryQueryCommand(cmd MemoryQueryCommand) error {
	if cmd.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if cmd.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if strings.TrimSpace(cmd.QueryJSON) == "" {
		return logicdomain.ValidationError{Field: "query_json", Message: "is required"}
	}
	if cmd.TopK < 0 {
		return logicdomain.ValidationError{Field: "top_k", Message: "must be >= 0"}
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

// parseMemoryQueryJSON decodes the grouped JSON search payload into the strict array-of-items format expected by the RPC.
// parseMemoryQueryJSON 用于把分组 JSON 搜索载荷解码成 RPC 期望的严格数组格式。
func parseMemoryQueryJSON(raw string) ([]MemoryQueryItem, error) {
	var items []MemoryQueryItem
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &items); err != nil {
		return nil, logicdomain.ValidationError{Field: "query_json", Message: "must be a JSON array of {background, query} objects"}
	}
	if len(items) == 0 {
		return nil, logicdomain.ValidationError{Field: "query_json", Message: "must contain at least one query item"}
	}
	if len(items) > maxMemoryQueryItems {
		return nil, logicdomain.ValidationError{Field: "query_json", Message: fmt.Sprintf("must contain at most %d query items", maxMemoryQueryItems)}
	}
	for idx := range items {
		items[idx].Background = strings.TrimSpace(items[idx].Background)
		items[idx].Query = strings.TrimSpace(items[idx].Query)
		if items[idx].Query == "" {
			return nil, logicdomain.ValidationError{Field: "query_json[" + strconv.Itoa(idx) + "].query", Message: "is required"}
		}
	}
	return items, nil
}

// buildMemorySearchText composes the optional background and required key query into one embedding text while keeping the caller-facing echo fields unchanged.
// buildMemorySearchText 用于把可选背景和必填关键语句组合成一段 embedding 文本，同时保持返回给调用方的回显字段不变。
func buildMemorySearchText(item MemoryQueryItem) string {
	if item.Background == "" {
		return item.Query
	}
	return strings.TrimSpace(item.Background) + "\n\n关键语句：\n" + strings.TrimSpace(item.Query)
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
			u.logger.Warn("memory search rerank degraded", "query", strings.TrimSpace(query), "candidate_count", len(primary), "err", err)
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
	return ordered
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
			SourceKind:     row.SourceKind,
			ScopeLevel:     row.ScopeLevel,
			SessionID:      chooseSearchSessionID(hit, row),
			Abstract:       strings.TrimSpace(row.Abstract),
			DetailsPreview: strings.TrimSpace(row.Details),
			Category:       row.Category,
			Score:          hit.Score,
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

// buildDirectMemoryDedupeHash builds one stable hash from the fields that define “same direct write in the same short window”.
// buildDirectMemoryDedupeHash 用于从定义“同一短时间窗口内相同主动写入”的字段构造稳定哈希。
func buildDirectMemoryDedupeHash(session logicdomain.SessionRef, item WriteMemoryItem) string {
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
