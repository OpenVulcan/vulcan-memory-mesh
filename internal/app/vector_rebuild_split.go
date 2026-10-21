// Split-store vector rebuild writes and recovery after sidecar reset.
// 应用层分离存储向量重建写入与旁路库重置后的恢复。
package app

import (
	"context"
	"fmt"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"strings"
)

// splitVectorRebuildApplyResult records how far one destructive split-mode rebuild attempt progressed before returning.
// splitVectorRebuildApplyResult 用于记录一次破坏性 split 重建尝试在返回前推进到了哪个阶段。
type splitVectorRebuildApplyResult struct {
	// DurableRowsUpdated counts durable vector rows whose replacement write has already succeeded.
	// DurableRowsUpdated 用于统计已经成功完成替换写入的 durable 向量行数。
	DurableRowsUpdated int
	// VectorRowsRebuilt counts detached vector rows successfully written after the reset.
	// VectorRowsRebuilt 用于统计 reset 后已经成功写入的独立向量行数。
	VectorRowsRebuilt int
	// ResetCompleted marks whether this attempt crossed the destructive table-reset boundary.
	// ResetCompleted 用于标记本次尝试是否已经越过破坏性的表重置边界。
	ResetCompleted bool
}

// applySplitVectorRebuildRecords performs the destructive split-mode reset plus refill using already-materialized target vectors so retries never need to call the embedding backend again.
// applySplitVectorRebuildRecords 用于基于已准备好的目标向量执行 split 模式的破坏性 reset 与回填，让重试路径无需再次调用 embedding 后端。
func applySplitVectorRebuildRecords(ctx context.Context, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger) (splitVectorRebuildApplyResult, error) {
	return applySplitVectorRebuildRecordsWithProgress(ctx, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, nil)
}

// applySplitVectorRebuildRecordsWithProgress performs split refill and publishes completed write batches.
// applySplitVectorRebuildRecordsWithProgress 执行 split 回填，并发布已完成的写入批次。
func applySplitVectorRebuildRecordsWithProgress(ctx context.Context, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) (splitVectorRebuildApplyResult, error) {
	if logger == nil {
		logger = logx.Default()
	}
	result := splitVectorRebuildApplyResult{}
	logger.Info("vector rebuild reset phase starting", "mode", mode, "memory_count", len(rebuiltRecords))
	if err := resetter.RecreateTable(ctx); err != nil {
		result.ResetCompleted = logicdomain.IsOutcomeUncertain(err)
		return result, fmt.Errorf("recreate split vector table: %w", err)
	}
	result.ResetCompleted = true
	vectorIDs := collectVectorRebuildVectorIDs(rebuiltRecords)
	if len(vectorIDs) > 0 {
		if err := durableResetter.ClearMemoryVectors(ctx, vectorIDs); err != nil {
			return result, fmt.Errorf("clear durable vectors before split rebuild: %w", err)
		}
	}
	if len(rebuiltRecords) == 0 {
		return result, nil
	}

	logger.Info("vector rebuild refill phase starting", "mode", mode, "memory_count", len(rebuiltRecords))
	publishVectorRebuildProgress(reporter, "writing", 0, len(rebuiltRecords))
	for start := 0; start < len(rebuiltRecords); start += vectorRebuildWriteBatchSize {
		end := start + vectorRebuildWriteBatchSize
		if end > len(rebuiltRecords) {
			end = len(rebuiltRecords)
		}
		batchRecords := cloneVectorRebuildRecords(rebuiltRecords[start:end])
		if err := durable.ReplaceMemoryVectors(ctx, batchRecords); err != nil {
			return result, fmt.Errorf("replace durable vectors for batch %d-%d: %w", start, end, err)
		}

		// Count the durable replacement as soon as it succeeds because later sidecar failures cannot roll that write back.
		// durable 替换一旦成功就立即计数，因为后续 sidecar 失败无法回滚这次写入。
		result.DurableRowsUpdated += len(batchRecords)
		for idx, record := range batchRecords {
			if err := vector.Upsert(ctx, record); err != nil {
				return result, fmt.Errorf("upsert rebuilt vector row %d/%d (%s): %w", start+idx+1, len(rebuiltRecords), record.ID, err)
			}
			result.VectorRowsRebuilt++
		}
		logger.Info("vector rebuild refill phase advanced", "mode", mode, "processed", result.DurableRowsUpdated, "total", len(rebuiltRecords))
		publishVectorRebuildProgress(reporter, "writing", result.DurableRowsUpdated, len(rebuiltRecords))
	}
	return result, nil
}

// vectorRebuildOutcomeUncertainError marks split-mode rebuild failures that may have already changed durable vectors, the detached vector table, or both.
// vectorRebuildOutcomeUncertainError 用于标记 split 模式重建中可能已经改变 durable 向量、独立向量表或二者的失败。
func vectorRebuildOutcomeUncertainError(message string, causes ...error) error {
	parts := make([]string, 0, 1+len(causes))
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, cause := range causes {
		if cause == nil {
			continue
		}
		if trimmed := strings.TrimSpace(cause.Error()); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return logicdomain.OutcomeUncertainError{
		Operation: "rebuild split vector table",
		Message:   strings.Join(parts, "; "),
	}
}

// recoverSplitVectorRebuildAfterReset replays the already-materialized target vectors after one intermediate split-mode failure so the command prefers converging to the new current-dimension state over leaving the runtime sidecar empty.
// recoverSplitVectorRebuildAfterReset 用于在 split 模式中途失败后，重放已经准备好的目标向量，让命令优先收敛到新的当前维度状态，而不是把运行时 sidecar 留成空表。
func recoverSplitVectorRebuildAfterReset(ctx context.Context, cause error, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger) error {
	return recoverSplitVectorRebuildAfterResetWithProgress(ctx, cause, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, nil)
}

// recoverSplitVectorRebuildAfterResetWithProgress retries an interrupted refill while retaining progress reporting.
// recoverSplitVectorRebuildAfterResetWithProgress 在保留进度报告的同时重试中断的回填。
func recoverSplitVectorRebuildAfterResetWithProgress(ctx context.Context, cause error, mode string, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) error {
	if cause == nil {
		return nil
	}
	if len(rebuiltRecords) == 0 {
		return nil
	}
	if logger == nil {
		logger = logx.Default()
	}
	logger.Warn("split vector rebuild failed after reset, attempting automatic repair", "mode", mode, "memory_count", len(rebuiltRecords), "err", cause)
	repairCtx := ctx
	if repairCtx == nil {
		repairCtx = context.Background()
	} else {
		repairCtx = context.WithoutCancel(repairCtx)
	}
	if _, err := applySplitVectorRebuildRecordsWithProgress(repairCtx, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter); err != nil {
		logger.Error("split vector rebuild automatic repair failed", "mode", mode, "memory_count", len(rebuiltRecords), "err", err)
		return err
	}
	logger.Warn("split vector rebuild automatic repair completed", "mode", mode, "memory_count", len(rebuiltRecords))
	return nil
}
