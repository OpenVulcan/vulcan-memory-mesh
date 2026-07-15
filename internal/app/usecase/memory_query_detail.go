// memory_query_detail.go contains the mixed memory-detail and turn-detail loading logic used by the app-layer memory query flow.
// memory_query_detail.go 用于承载应用层记忆查询链路中的混合记忆详情与 turn 详情加载逻辑。
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

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

// validateTurnDetailCommand checks the turn-detail lookup request before relational reads begin.
// validateTurnDetailCommand 用于在关系读取开始前校验 turn 详情查询请求。
func validateTurnDetailCommand(cmd TurnDetailCommand) error {
	if len(cmd.TurnIDs) == 0 {
		return logicdomain.ValidationError{Field: "turn_ids", Message: "must contain at least one id"}
	}
	if len(cmd.TurnIDs) > MaxTurnDetailLookup {
		return logicdomain.ValidationError{Field: "turn_ids", Message: fmt.Sprintf("must contain at most %d ids", MaxTurnDetailLookup)}
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
			userContent, timeline, assistantContent, err := parseDehydratedTurnContent(row.DehydratedContent)
			if err != nil {
				return TurnDetailResult{}, err
			}
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
func parseDehydratedTurnContent(raw string) (string, []logicdomain.TurnDetailTimelineItem, string, error) {
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
		return "", nil, "", fmt.Errorf("parse dehydrated turn content: %w", err)
	}
	timeline := make([]logicdomain.TurnDetailTimelineItem, 0, len(payload.Timeline))
	for _, item := range payload.Timeline {
		timeline = append(timeline, logicdomain.TurnDetailTimelineItem{
			Type:    strings.TrimSpace(item.Type),
			Content: item.Content,
		})
	}
	return payload.User, timeline, payload.Assistant, nil
}

// normalizeWriteMemoryItem fills direct-write defaults so tool callers can omit optional lifecycle controls without losing deterministic persistence behavior.
// normalizeWriteMemoryItem 用于补齐主动写记忆默认值，让工具调用方即使省略可选生命周期字段，也不会丢失确定性的持久化行为。
