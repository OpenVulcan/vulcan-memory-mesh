// server_memory.go keeps memory retrieval and direct-write RPC handlers for the inbound gRPC adapter.
// server_memory.go 用于承载入站 gRPC 适配层中的记忆检索与主动写入 RPC 处理器。
package grpcapi

import (
	"context"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// SearchMemoryEvents runs one simple query list through the unified memory-search pipeline and returns only the AI-facing hit fields needed for follow-up tool calls.
// SearchMemoryEvents 用于把简单查询字符串列表送入统一记忆检索链路，并只返回 AI 后续工具调用真正需要的命中字段。
func (s *Server) SearchMemoryEvents(ctx context.Context, req *vmmv1.SearchMemoryEventsRequest) (*vmmv1.SearchMemoryEventsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeSearchMemoryEventsRequest(req)
	if err := s.validator().ValidateSearchMemoryEvents(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.memory.Search(ctx, usecase.MemoryQueryCommand{
		UserID:    req.GetUserId(),
		ProjectID: req.GetProjectId(),
		Queries:   req.GetQueries(),
		TopK:      int(req.GetTopK()),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	groups := make([]*vmmv1.MemorySearchGroupResult, 0, len(result.Results))
	for _, group := range result.Results {
		hits := make([]*vmmv1.MemorySearchHit, 0, len(group.Hits))
		for _, hit := range group.Hits {
			hits = append(hits, &vmmv1.MemorySearchHit{
				MemoryId:        hit.MemoryRef.ID,
				SourceTurnId:    hit.SourceRef.ID,
				Abstract:        hit.Abstract,
				DetailsPreview:  hit.DetailsPreview,
				Category:        memoryCategoryLabel(hit.Category),
				CreatedDatetime: logicdomain.FormatDisplayDateTime(hit.CreatedAt),
			})
		}
		groups = append(groups, &vmmv1.MemorySearchGroupResult{
			QueryIndex: uint32(group.QueryIndex),
			Query:      group.Query,
			Hits:       hits,
		})
	}
	return &vmmv1.SearchMemoryEventsResponse{
		Results: groups,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// GetTurnDetails loads one or more turn rows by turn id and returns an AI-ready structured dialogue view instead of storage-oriented dehydrated fields.
// GetTurnDetails 用于按 turn id 读取一条或多条 turn 记录，并返回面向 AI 的结构化对话视图，而不是面向存储的脱水字段。
func (s *Server) GetTurnDetails(ctx context.Context, req *vmmv1.GetTurnDetailsRequest) (*vmmv1.GetTurnDetailsResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeGetTurnDetailsRequest(req)
	if err := s.validator().ValidateGetTurnDetails(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.memory.GetTurns(ctx, usecase.TurnDetailCommand{TurnIDs: req.GetTurnIds()})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	turns := make([]*vmmv1.TurnDetailEntry, 0, len(result.Turns))
	for _, turn := range result.Turns {
		if entry := toTurnDetailEntry(turn); entry != nil {
			turns = append(turns, entry)
		}
	}
	return &vmmv1.GetTurnDetailsResponse{
		Turns:   turns,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// WriteMemories persists one batch of direct AI-written memory items inside the resolved session scope and returns only the created or deduplicated memory ids.
// WriteMemories 用于在已解析 session 范围内持久化一批 AI 主动写入的记忆项，并只返回新建或复用的 memory id。
func (s *Server) WriteMemories(ctx context.Context, req *vmmv1.WriteMemoriesRequest) (*vmmv1.WriteMemoriesResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeWriteMemoriesRequest(req)
	if err := s.validator().ValidateWriteMemories(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	session, ok := resolvedSessionRefFromContext(ctx)
	if !ok {
		return nil, toStatus(withMessage(errInternal, "resolved request scope is missing"))
	}
	ctx, cancel := withTimeout(ctx, s.postTimeout)
	defer cancel()

	// Keep the transport layer focused on compact numeric concept mapping so AI-facing writes stay simple while defaults still live in the use case.
	// 让传输层只负责紧凑数字概念值映射，保证 AI 写入面保持简单，而默认值仍由用例层统一决定。
	items := make([]usecase.WriteMemoryItem, 0, len(req.GetItems()))
	for _, item := range req.GetItems() {
		items = append(items, usecase.WriteMemoryItem{
			ScopeLevel:  normalizeTransportMemoryScopeLevel(item.GetScopeLevel()),
			Abstract:    item.GetAbstract(),
			Details:     item.GetDetails(),
			Category:    int(item.GetCategory()),
			Priority:    normalizeTransportMemoryPriority(item.GetPriority()),
			MemoryLevel: normalizeTransportMemoryLevel(item.GetMemoryLevel()),
		})
	}
	result, err := s.memory.Write(ctx, usecase.WriteMemoriesCommand{
		Session: session,
		Items:   items,
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}

	entries := make([]*vmmv1.WriteMemoryResultItem, 0, len(result.Items))
	for _, item := range result.Items {
		entries = append(entries, &vmmv1.WriteMemoryResultItem{
			MemoryId: item.Ref.ID,
			Deduped:  item.Deduped,
		})
	}
	return &vmmv1.WriteMemoriesResponse{
		Items:   entries,
		TraceId: trace.IDFromContext(ctx),
	}, nil
}

// DeleteMemories marks explicit memory rows as deleted inside the caller-visible user/project scope while preserving source turn details.
// DeleteMemories 用于在调用方可见的 user/project 范围内把明确指定的记忆行标记为 deleted，同时保留来源 turn 详情。
func (s *Server) DeleteMemories(ctx context.Context, req *vmmv1.DeleteMemoriesRequest) (*vmmv1.DeleteMemoriesResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.memory == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeDeleteMemoriesRequest(req)
	if err := s.validator().ValidateDeleteMemories(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.memory.Delete(ctx, usecase.DeleteMemoriesCommand{
		UserID:    req.GetUserId(),
		ProjectID: req.GetProjectId(),
		MemoryIDs: req.GetMemoryIds(),
		Reason:    req.GetReason(),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.DeleteMemoriesResponse{
		DeletedMemoryIds:  result.DeletedMemoryIDs,
		NotFoundMemoryIds: result.NotFoundMemoryIDs,
		DeletedVectorRows: result.DeletedVectorRows,
		TraceId:           trace.IDFromContext(ctx),
	}, nil
}
