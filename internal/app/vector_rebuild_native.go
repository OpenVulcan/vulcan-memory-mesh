// Native vector rebuild publication and interrupted-write recovery.
// 应用层原生向量重建发布及中断写入恢复。
package app

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"strings"
)

// runNativeVectorRebuildWithSnapshot rebuilds live and eligible trash vectors from one captured source image.
// runNativeVectorRebuildWithSnapshot 使用一次捕获的源快照重建 live 与 eligible trash 向量。
func runNativeVectorRebuildWithSnapshot(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if logger == nil {
		logger = logx.Default()
	}
	report := VectorRebuildReport{
		Mode:         "native",
		ProjectCount: snapshot.ProjectCount,
		MemoryCount:  len(snapshot.Active) + len(snapshot.Trash),
	}
	if vector == nil {
		return report, fmt.Errorf("maintenance vector store is not configured for native rebuild")
	}
	resetter, ok := vector.(vectorRebuildResetter)
	if !ok {
		return report, fmt.Errorf("vector store does not support table recreation for native rebuild")
	}
	durableResetter, ok := durable.(vectorRebuildDurableResetter)
	if !ok {
		return report, fmt.Errorf("relational store does not support durable vector reset for native rebuild")
	}
	if len(snapshot.Trash) > 0 {
		if _, ok := durable.(nativeVectorRebuildRestorableTrashWriter); !ok {
			return report, fmt.Errorf("native vector rebuild refuses eligible restorable trash vectors: relational store does not expose exact trash-vector updates")
		}
	}
	rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, snapshot.Unique, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
	if err != nil {
		return report, err
	}
	applyResult, applyErr := applyNativeVectorRebuildSnapshotWithProgress(ctx, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter)
	report.DurableRowsUpdated = applyResult.DurableRowsUpdated
	report.VectorRowsRebuilt = applyResult.VectorRowsRebuilt
	publishCtx := ctx
	cancelPublish := func() {}
	if applyErr != nil {
		if !applyResult.ResetCompleted {
			return report, applyErr
		}
		if recoverErr := recoverNativeVectorRebuildAfterResetWithProgress(ctx, applyErr, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter); recoverErr != nil {
			return report, vectorRebuildOutcomeUncertainError("native vector rebuild failed after reset", applyErr, recoverErr)
		}
		report.DurableRowsUpdated = len(snapshot.Active) + len(snapshot.Trash)
		report.VectorRowsRebuilt = len(snapshot.Unique)
		publishCtx, cancelPublish = nativeVectorRebuildPublishContext(ctx)
		defer cancelPublish()
		logger.Warn("native vector rebuild recovered after intermediate failure", "mode", report.Mode, "memory_count", report.MemoryCount, "err", applyErr)
	}
	if err := recordNativeVectorSchemaVersion(publishCtx, cfg, durable); err != nil {
		return report, err
	}
	logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
	return report, nil
}

// recordNativeVectorSchemaVersion persists the current native LanceDB schema only after the rebuild has converged to its target state.
// recordNativeVectorSchemaVersion 仅在原生向量重建已经收敛到目标状态后持久化当前 LanceDB schema 版本。
func recordNativeVectorSchemaVersion(ctx context.Context, cfg config.Config, durable appports.MemoryVectorRebuildStore) error {
	if !cfg.UsesNative() {
		return nil
	}
	versions, ok := durable.(appports.SchemaVersionStore)
	if !ok {
		return nativeVectorRebuildOutcomeUncertainError("native relational store does not expose schema version persistence")
	}
	if err := versions.SetSchemaComponentVersion(ctx, "lancedb", vldb_lancedb.CurrentSchemaVersion); err != nil {
		return nativeVectorRebuildOutcomeUncertainError("native vector table was rebuilt before schema version persistence failed", err)
	}
	return nil
}

// nativeVectorRebuildOutcomeUncertainError marks native rebuild failures that occur after the vector table or durable vectors changed.
// nativeVectorRebuildOutcomeUncertainError 用于标记原生重建在向量表或 durable 向量已发生变化后出现的失败。
func nativeVectorRebuildOutcomeUncertainError(message string, causes ...error) error {
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
		Operation: "rebuild native vector table",
		Message:   strings.Join(parts, "; "),
	}
}

// applyNativeVectorRebuildSnapshotWithProgress resets native Lance, rewrites live and trash durable rows, and then publishes unique vector ids.
// applyNativeVectorRebuildSnapshotWithProgress 重置 native Lance，重写 live 与 trash durable 行，最后发布唯一向量 ID。
func applyNativeVectorRebuildSnapshotWithProgress(ctx context.Context, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) (splitVectorRebuildApplyResult, error) {
	if logger == nil {
		logger = logx.Default()
	}
	result := splitVectorRebuildApplyResult{}
	logger.Info("native vector rebuild reset phase starting", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash))
	if err := resetter.RecreateTable(ctx); err != nil {
		result.ResetCompleted = logicdomain.IsOutcomeUncertain(err)
		return result, fmt.Errorf("recreate native vector table: %w", err)
	}
	result.ResetCompleted = true
	activeIDs := collectVectorRebuildVectorIDs(snapshot.Active)
	if len(activeIDs) > 0 {
		if err := durableResetter.ClearMemoryVectors(ctx, activeIDs); err != nil {
			return result, fmt.Errorf("clear native durable vectors before rebuild: %w", err)
		}
	}
	rebuiltByID := make(map[string]logicdomain.MemoryRecord, len(rebuiltRecords))
	for _, record := range rebuiltRecords {
		id := strings.TrimSpace(record.ID)
		if id == "" {
			return result, fmt.Errorf("native rebuilt vector has an empty id")
		}
		if _, exists := rebuiltByID[id]; exists {
			return result, fmt.Errorf("native rebuilt vectors contain duplicate id %q", id)
		}
		rebuiltByID[id] = cloneVectorRebuildRecord(record)
	}
	activeRecords := make([]logicdomain.MemoryRecord, 0, len(snapshot.Active))
	for _, source := range snapshot.Active {
		id := strings.TrimSpace(source.ID)
		rebuilt, exists := rebuiltByID[id]
		if !exists {
			return result, fmt.Errorf("native rebuilt vector missing active id %q", id)
		}
		activeRecords = append(activeRecords, rebuilt)
	}
	trashWriter, _ := durable.(nativeVectorRebuildRestorableTrashWriter)
	logger.Info("native vector rebuild durable refill phase starting", "mode", "native", "active_rows", len(activeRecords), "trash_rows", len(snapshot.Trash))
	publishVectorRebuildProgress(reporter, "writing", 0, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	for start := 0; start < len(activeRecords); start += vectorRebuildWriteBatchSize {
		end := start + vectorRebuildWriteBatchSize
		if end > len(activeRecords) {
			end = len(activeRecords)
		}
		batch := cloneVectorRebuildRecords(activeRecords[start:end])
		if err := durable.ReplaceMemoryVectors(ctx, batch); err != nil {
			return result, fmt.Errorf("replace native live vectors for batch %d-%d: %w", start, end, err)
		}
		result.DurableRowsUpdated += len(batch)
	}
	for _, row := range snapshot.Trash {
		rebuilt, exists := rebuiltByID[strings.TrimSpace(row.Record.ID)]
		if !exists {
			return result, fmt.Errorf("native rebuilt vector missing trash id %q", row.Record.ID)
		}
		if trashWriter == nil {
			return result, fmt.Errorf("native vector rebuild cannot update trash row batch=%d memory=%d", row.BatchID, row.MemoryID)
		}
		if err := trashWriter.UpdateNativeRestorableTrashVector(ctx, row.BatchID, row.MemoryID, append([]float32(nil), rebuilt.Vector...)); err != nil {
			return result, fmt.Errorf("update native trash vector batch=%d memory=%d: %w", row.BatchID, row.MemoryID, err)
		}
		result.DurableRowsUpdated++
		publishVectorRebuildProgress(reporter, "writing", result.DurableRowsUpdated, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	}
	for idx, record := range rebuiltRecords {
		if err := vector.Upsert(ctx, record); err != nil {
			return result, fmt.Errorf("upsert rebuilt native vector row %d/%d (%s): %w", idx+1, len(rebuiltRecords), record.ID, err)
		}
		result.VectorRowsRebuilt++
		publishVectorRebuildProgress(reporter, "writing", len(activeRecords)+len(snapshot.Trash)+result.VectorRowsRebuilt, len(activeRecords)+len(snapshot.Trash)+len(rebuiltRecords))
	}
	return result, nil
}

// recoverNativeVectorRebuildAfterResetWithProgress retries the complete native target using a bounded detached context.
// recoverNativeVectorRebuildAfterResetWithProgress 使用有界脱离上下文重试完整 native 目标。
func recoverNativeVectorRebuildAfterResetWithProgress(ctx context.Context, cause error, durable appports.MemoryVectorRebuildStore, durableResetter vectorRebuildDurableResetter, resetter vectorRebuildResetter, vector appports.VectorStore, snapshot nativeVectorRebuildSnapshot, rebuiltRecords []logicdomain.MemoryRecord, logger *logx.Logger, reporter VectorRebuildProgressReporter) error {
	if logger == nil {
		logger = logx.Default()
	}
	logger.Warn("native vector rebuild failed after reset, attempting automatic repair", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash), "err", cause)
	repairBase := ctx
	if repairBase == nil {
		repairBase = context.Background()
	} else {
		repairBase = context.WithoutCancel(repairBase)
	}
	repairCtx, cancel := context.WithTimeout(repairBase, nativeVectorRebuildRecoveryTimeout)
	defer cancel()
	if _, err := applyNativeVectorRebuildSnapshotWithProgress(repairCtx, durable, durableResetter, resetter, vector, snapshot, rebuiltRecords, logger, reporter); err != nil {
		logger.Error("native vector rebuild automatic repair failed", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash), "err", err)
		return err
	}
	logger.Warn("native vector rebuild automatic repair completed", "mode", "native", "memory_count", len(snapshot.Active)+len(snapshot.Trash))
	return nil
}

// nativeVectorRebuildPublishContext keeps post-recovery schema and fact publication bounded after caller cancellation.
// nativeVectorRebuildPublishContext 在调用方取消后为恢复完成的 schema 与事实发布提供有界上下文。
func nativeVectorRebuildPublishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil || ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), nativeVectorRebuildRecoveryTimeout)
}
