// memory_query_delete.go contains the explicit memory-node delete workflow used by the gRPC memory administration surface.
// memory_query_delete.go 用于承载 gRPC 记忆管理接口使用的显式记忆条目删除流程。
package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// defaultDeleteMemoryReason is stored on manually removed memory rows when callers do not provide a more specific audit reason.
	// defaultDeleteMemoryReason 用于在调用方没有提供更具体审计原因时，写入被手工移除记忆行的状态原因。
	defaultDeleteMemoryReason = "manual memory delete requested through gRPC"
)

// Delete marks explicit durable memory nodes as deleted inside the resolved user/project scope and then cleans their sidecar vectors best-effort.
// Delete 用于在解析后的 user/project 范围内把明确指定的长期记忆条目标记为 deleted，并随后尽力清理对应旁路向量。
func (u *MemoryUseCase) Delete(ctx context.Context, cmd DeleteMemoriesCommand) (DeleteMemoriesResult, error) {
	if u == nil || u.profiles == nil {
		return DeleteMemoriesResult{}, fmt.Errorf("profile store is nil")
	}
	if u.memories == nil {
		return DeleteMemoriesResult{}, fmt.Errorf("memory store is nil")
	}
	if err := validateDeleteMemoriesCommand(cmd); err != nil {
		return DeleteMemoriesResult{}, err
	}

	// Resolve USER and PROJECT before touching memory rows so the store can reject ids outside the caller-visible hierarchy without leaking which ids exist elsewhere.
	// 先解析 USER 和 PROJECT，再接触记忆行，让存储层可以拒绝调用方可见层级之外的 id，而不泄漏其他范围内的 id 是否存在。
	userTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeUser, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return DeleteMemoriesResult{}, err
	}
	projectTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeProject, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return DeleteMemoriesResult{}, err
	}

	now := time.Now().UTC()
	memoryIDs := normalizeDeleteMemoryIDs(cmd.MemoryIDs)
	reason := normalizeDeleteMemoryReason(cmd.Reason)
	filter := buildScopedMemorySearchFilter(userTarget, projectTarget, "")
	deleteResult, err := u.memories.DeleteMemoryNodes(ctx, memoryIDs, filter, now, reason)
	if err != nil {
		return DeleteMemoriesResult{}, err
	}

	deletedVectorRows := uint64(0)
	if len(deleteResult.DeletedVectorIDs) > 0 {
		if u.vector == nil {
			enqueueVectorGCCompensation(ctx, u.memories, u.logger, logicdomain.VectorGCJobTypeManualMemoryDelete, deleteResult.DeletedVectorIDs, now,
				"memory_ids", deleteResult.DeletedMemoryIDs,
				"err", "vector store is nil",
			)
		} else if rows, deleteErr := u.vector.DeleteByIDs(ctx, deleteResult.DeletedVectorIDs); deleteErr != nil {
			enqueueVectorGCCompensation(ctx, u.memories, u.logger, logicdomain.VectorGCJobTypeManualMemoryDelete, deleteResult.DeletedVectorIDs, now,
				"memory_ids", deleteResult.DeletedMemoryIDs,
				"err", deleteErr,
			)
		} else {
			deletedVectorRows = rows
		}
	}
	return DeleteMemoriesResult{
		DeletedMemoryIDs:  deleteResult.DeletedMemoryIDs,
		NotFoundMemoryIDs: deleteResult.NotFoundMemoryIDs,
		DeletedVectorRows: deletedVectorRows,
	}, nil
}

// validateDeleteMemoriesCommand checks the manual delete request before scope resolution and relational locking begin.
// validateDeleteMemoriesCommand 用于在范围解析和关系锁定开始前校验手工删除请求。
func validateDeleteMemoriesCommand(cmd DeleteMemoriesCommand) error {
	if cmd.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if cmd.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(cmd.MemoryIDs) == 0 {
		return logicdomain.ValidationError{Field: "memory_ids", Message: "must contain at least one id"}
	}
	if len(cmd.MemoryIDs) > maxDeleteMemoryIDs {
		return logicdomain.ValidationError{Field: "memory_ids", Message: fmt.Sprintf("must contain at most %d ids", maxDeleteMemoryIDs)}
	}
	for idx, memoryID := range cmd.MemoryIDs {
		if memoryID == 0 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("memory_ids[%d]", idx), Message: "must be a numeric id"}
		}
	}
	if len(strings.TrimSpace(cmd.Reason)) > 1024 {
		return logicdomain.ValidationError{Field: "reason", Message: "must be <= 1024 chars"}
	}
	return nil
}

// normalizeDeleteMemoryIDs removes duplicate ids while preserving the first-seen order used by API responses.
// normalizeDeleteMemoryIDs 用于在保留首次出现顺序的同时移除重复 id，供 API 响应保持可预测顺序。
func normalizeDeleteMemoryIDs(values []uint64) []uint64 {
	out := make([]uint64, 0, len(values))
	seen := make(map[uint64]struct{}, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// normalizeDeleteMemoryReason trims the optional caller reason and applies a stable default used by retention/audit inspection.
// normalizeDeleteMemoryReason 用于裁剪调用方可选原因，并在缺省时应用供 retention/审计查看的稳定默认值。
func normalizeDeleteMemoryReason(raw string) string {
	reason := strings.TrimSpace(raw)
	if reason == "" {
		return defaultDeleteMemoryReason
	}
	return reason
}
