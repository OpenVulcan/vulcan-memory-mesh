// server_scratchpad.go keeps isolated scratchpad RPC handlers for the inbound gRPC adapter.
// server_scratchpad.go 用于承载入站 gRPC 适配层中的隔离 scratchpad RPC 处理器。
package grpcapi

import (
	"context"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ScratchpadUpsert persists one deterministic DWM batch into the isolated scratchpad chain without touching the main session auto-create flow.
// ScratchpadUpsert 用于把一批确定性 DWM 数据写入隔离 scratchpad 链路，同时不触碰主 session 自动创建流程。
func (s *Server) ScratchpadUpsert(ctx context.Context, req *vmmv1.ScratchpadUpsertRequest) (*vmmv1.ScratchpadUpsertResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.scratchpad == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeScratchpadUpsertRequest(req)
	if err := s.validator().ValidateScratchpadUpsert(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.scratchpad.Upsert(ctx, usecase.ScratchpadUpsertCommand{
		Scope: logicdomain.ScratchpadScope{
			ProjectID:  req.GetProjectId(),
			UserID:     req.GetUserId(),
			SessionKey: req.GetSessionId(),
		},
		PlanName: req.GetPlanName(),
		Items:    collectScratchpadUpsertItems(req),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ScratchpadUpsertResponse{
		Status:        toProtoScratchpadStatus(result.Status),
		Msg:           result.Message,
		AffectedCount: uint32(maxInt(result.AffectedCount, 0)),
		InsertedCount: uint32(maxInt(result.InsertedCount, 0)),
		UpdatedCount:  uint32(maxInt(result.UpdatedCount, 0)),
	}, nil
}

// ScratchpadDelete removes one or more deterministic DWM keys while keeping empty-session deletes non-locking and idempotent.
// ScratchpadDelete 用于删除一个或多个确定性 DWM key，同时保持空 session 删除行为不锁定且幂等。
func (s *Server) ScratchpadDelete(ctx context.Context, req *vmmv1.ScratchpadDeleteRequest) (*vmmv1.ScratchpadDeleteResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.scratchpad == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeScratchpadDeleteRequest(req)
	if err := s.validator().ValidateScratchpadDelete(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.scratchpad.Delete(ctx, usecase.ScratchpadDeleteCommand{
		Scope: logicdomain.ScratchpadScope{
			ProjectID:  req.GetProjectId(),
			UserID:     req.GetUserId(),
			SessionKey: req.GetSessionId(),
		},
		PlanName: req.GetPlanName(),
		Keys:     collectScratchpadDeleteKeys(req),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ScratchpadDeleteResponse{
		Status:        toProtoScratchpadStatus(result.Status),
		Msg:           result.Message,
		AffectedCount: uint32(maxInt(result.AffectedCount, 0)),
	}, nil
}

// ScratchpadGet reloads either one deterministic DWM key batch or the whole isolated scratchpad payload for the current scope.
// ScratchpadGet 用于为当前范围重新加载一批确定性 DWM key 或整个隔离 scratchpad 载荷。
func (s *Server) ScratchpadGet(ctx context.Context, req *vmmv1.ScratchpadGetRequest) (*vmmv1.ScratchpadGetResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.scratchpad == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeScratchpadGetRequest(req)
	if err := s.validator().ValidateScratchpadGet(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.scratchpad.Get(ctx, usecase.ScratchpadGetQuery{
		Scope: logicdomain.ScratchpadScope{
			ProjectID:  req.GetProjectId(),
			UserID:     req.GetUserId(),
			SessionKey: req.GetSessionId(),
		},
		Keys: append([]string(nil), req.GetKeys()...),
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ScratchpadGetResponse{
		Status:           toProtoScratchpadStatus(result.Status),
		Msg:              result.Message,
		Items:            toProtoScratchpadItems(result.Items),
		PlanName:         result.PlanName,
		ItemCount:        uint32(maxInt(result.ItemCount, 0)),
		UpdatedTimestamp: toUnixMillis(result.UpdatedAt),
	}, nil
}

// ScratchpadListKeys reloads the canonical plan name plus the full ordered scratchpad key list for the current scope without fetching any values.
// ScratchpadListKeys 用于在不拉取任何 value 的前提下，为当前范围重新加载 canonical 计划名和完整有序的 scratchpad key 列表。
func (s *Server) ScratchpadListKeys(ctx context.Context, req *vmmv1.ScratchpadListKeysRequest) (*vmmv1.ScratchpadListKeysResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.scratchpad == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeScratchpadListKeysRequest(req)
	if err := s.validator().ValidateScratchpadListKeys(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.scratchpad.ListKeys(ctx, usecase.ScratchpadListKeysQuery{
		Scope: logicdomain.ScratchpadScope{
			ProjectID:  req.GetProjectId(),
			UserID:     req.GetUserId(),
			SessionKey: req.GetSessionId(),
		},
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ScratchpadListKeysResponse{
		Status:           toProtoScratchpadStatus(result.Status),
		Msg:              result.Message,
		PlanName:         result.PlanName,
		Keys:             append([]string(nil), result.Keys...),
		KeyCount:         uint32(maxInt(result.KeyCount, 0)),
		UpdatedTimestamp: toUnixMillis(result.UpdatedAt),
	}, nil
}

// ScratchpadClean clears the whole isolated DWM payload for the current scope without touching the main session chain.
// ScratchpadClean 用于清空当前范围下的整个隔离 DWM 载荷，同时不触碰主 session 链路。
func (s *Server) ScratchpadClean(ctx context.Context, req *vmmv1.ScratchpadCleanRequest) (*vmmv1.ScratchpadCleanResponse, error) {
	if err := s.requireReceiver(); err != nil {
		return nil, err
	}
	if s.scratchpad == nil {
		return nil, toStatus(errRouteDisabled)
	}
	NormalizeScratchpadCleanRequest(req)
	if err := s.validator().ValidateScratchpadClean(req); err != nil {
		return nil, toStatus(describeError(err))
	}
	ctx, cancel := withTimeout(ctx, s.workspaceTimeout)
	defer cancel()
	result, err := s.scratchpad.Clean(ctx, usecase.ScratchpadCleanCommand{
		Scope: logicdomain.ScratchpadScope{
			ProjectID:  req.GetProjectId(),
			UserID:     req.GetUserId(),
			SessionKey: req.GetSessionId(),
		},
	})
	if err != nil {
		return nil, toStatus(describeError(err))
	}
	return &vmmv1.ScratchpadCleanResponse{
		Status: toProtoScratchpadStatus(result.Status),
		Msg:    result.Message,
	}, nil
}
