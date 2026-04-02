// memory_query.go implements the vector-memory search and turn-detail lookup use cases exposed by the gRPC memory query surface.
// memory_query.go 用于实现 gRPC 记忆查询接口暴露的向量记忆检索与 turn 详情读取用例。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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

	// turnDetailContextRadius keeps three turns before and after each anchor so callers can continue finer follow-up lookups without fetching entire sessions.
	// turnDetailContextRadius 用于固定返回每个锚点 turn 前后各三轮编号，让调用方无需拉取整条 session 也能继续做更细的后续查询。
	turnDetailContextRadius = 3
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

// MemoryQueryHit returns one recalled vector-memory candidate plus the turn anchor needed for later detail lookup.
// MemoryQueryHit 用于返回一条召回的向量记忆候选，以及后续详情读取所需的 turn 锚点。
type MemoryQueryHit struct {
	MemoryID  string
	TurnID    uint64
	SessionID uint64
	Content   string
	Details   string
	Category  int
	Score     float64
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

// MemoryExecutor groups the active memory-search and turn-detail lookup flows exposed by the inbound gRPC adapter.
// MemoryExecutor 用于聚合入站 gRPC 适配层对外暴露的主动记忆检索和 turn 详情读取流程。
type MemoryExecutor interface {
	Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error)
	GetTurns(ctx context.Context, cmd TurnDetailCommand) (TurnDetailResult, error)
}

// MemoryUseCase orchestrates grouped memory search on top of profile-target resolution, embeddings, vector recall, and turn detail lookups.
// MemoryUseCase 用于在画像目标解析、embedding、向量召回和 turn 详情读取之上编排分组记忆查询流程。
type MemoryUseCase struct {
	profiles  appports.ProfileStore
	turns     appports.TurnLookupStore
	embedding appports.EmbeddingClient
	vector    appports.VectorStore
	logger    *logx.Logger
}

// NewMemoryUseCase creates a MemoryUseCase instance.
// NewMemoryUseCase 用于创建 MemoryUseCase 实例。
func NewMemoryUseCase(profiles appports.ProfileStore, turns appports.TurnLookupStore, embedding appports.EmbeddingClient, vector appports.VectorStore, logger *logx.Logger) *MemoryUseCase {
	if logger == nil {
		logger = logx.Default()
	}
	return &MemoryUseCase{
		profiles:  profiles,
		turns:     turns,
		embedding: embedding,
		vector:    vector,
		logger:    logger,
	}
}

// Search resolves the concrete project/user scope, parses the grouped JSON payload, embeds each item, and echoes grouped vector hits with turn anchors.
// Search 用于解析具体的 project/user 范围、解析分组 JSON 载荷、对每条输入做 embedding，并返回带 turn 锚点的分组向量命中结果。
func (u *MemoryUseCase) Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	if u == nil || u.profiles == nil {
		return MemoryQueryResult{}, fmt.Errorf("profile store is nil")
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

	// Keep vector recall inside the resolved project hierarchy while allowing user-owned and shared rows to participate.
	// 将向量召回限制在已解析项目层级内，同时允许用户私有和共享行共同参与检索。
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
		results = append(results, MemoryQueryGroupResult{
			QueryIndex: idx,
			Background: item.Background,
			Query:      item.Query,
			Hits:       mapMemoryHits(hits),
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
	if u == nil || u.turns == nil {
		return TurnDetailResult{}, fmt.Errorf("turn lookup store is nil")
	}
	if err := validateTurnDetailCommand(cmd); err != nil {
		return TurnDetailResult{}, err
	}

	// Normalize the id list before querying so relational adapters can use deterministic IN lists without duplicate ids.
	// 在查询前先规范化 id 列表，让关系适配层可以使用确定性的 IN 列表并消除重复 id。
	requested := normalizeTurnIDList(cmd.TurnIDs)
	rows, err := u.turns.LoadTurnsByIDs(ctx, requested)
	if err != nil {
		return TurnDetailResult{}, err
	}
	windows, err := u.turns.LoadTurnWindows(ctx, requested, turnDetailContextRadius)
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

// mapMemoryHits converts vector-store hits into the gRPC-facing query result shape while preserving turn anchors and auxiliary metadata.
// mapMemoryHits 用于把向量存储命中结果转换成 gRPC 查询结果结构，同时保留 turn 锚点和辅助元数据。
func mapMemoryHits(hits []logicdomain.MemoryHit) []MemoryQueryHit {
	mapped := make([]MemoryQueryHit, 0, len(hits))
	for _, hit := range hits {
		mapped = append(mapped, MemoryQueryHit{
			MemoryID:  strings.TrimSpace(hit.ID),
			TurnID:    parseUint64Metadata(hit.Metadata, "turn_id"),
			SessionID: hit.Filter.SessionID,
			Content:   strings.TrimSpace(hit.Text),
			Details:   strings.TrimSpace(hit.Metadata["details"]),
			Category:  parseIntMetadata(hit.Metadata, "category"),
			Score:     hit.Score,
		})
	}
	return mapped
}

// parseUint64Metadata extracts one uint64 metadata value while keeping malformed values from crashing the memory query path.
// parseUint64Metadata 用于提取单个 uint64 元数据值，同时避免格式异常把记忆查询链路打断。
func parseUint64Metadata(metadata map[string]string, key string) uint64 {
	value := strings.TrimSpace(metadata[key])
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

// parseIntMetadata extracts one int metadata value while keeping malformed values from crashing the memory query path.
// parseIntMetadata 用于提取单个 int 元数据值，同时避免格式异常把记忆查询链路打断。
func parseIntMetadata(metadata map[string]string, key string) int {
	value := strings.TrimSpace(metadata[key])
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
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
