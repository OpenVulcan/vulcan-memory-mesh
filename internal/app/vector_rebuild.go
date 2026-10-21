// Application-level vector rebuild entry points and shared orchestration.
// 应用层向量重建入口与共享编排。
package app

import (
	"context"
	"fmt"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"time"
)

const (
	// vectorRebuildBudgetRetryDelay keeps the maintenance loop patient when every configured key is only temporarily blocked by the current RPM/TPM/RPD budget window.
	// vectorRebuildBudgetRetryDelay 用于在所有已配置 Key 只是暂时被 RPM/TPM/RPD 预算窗口卡住时，让维护流程保持耐心等待。
	vectorRebuildBudgetRetryDelay = 30 * time.Second

	// vectorRebuildWriteBatchSize keeps split-mode durable/vector refill writes bounded without coupling storage write chunking to the embedding model's API batch contract.
	// vectorRebuildWriteBatchSize 用于让 split 模式的 durable/向量回填写入保持有界，同时避免把存储写入分批错误绑死到 embedding 模型的 API 批契约上。
	vectorRebuildWriteBatchSize = 10

	// nativeVectorRebuildRecoveryTimeout bounds detached publication after a cancelled repair attempt.
	// nativeVectorRebuildRecoveryTimeout 限制取消后的脱离修复发布时长。
	nativeVectorRebuildRecoveryTimeout = 10 * time.Minute
)

// vectorRebuildResetter is the narrow vector-store capability needed by split-mode rebuilds that must recreate the sidecar LanceDB table from the durable SQL source.
// vectorRebuildResetter 用于描述分离模式重建所需的最小向量库能力：先重建旁路 LanceDB 表，再从长期 SQL 事实源回灌。
type vectorRebuildResetter interface {
	RecreateTable(ctx context.Context) error
}

// vectorRebuildDurableResetter is the split-mode durable-store capability used to clear stale SQLite vector payloads before the detached vector store is recreated from the same empty baseline.
// vectorRebuildDurableResetter 用于描述 split 模式 durable 存储的能力：先清空陈旧 SQLite 向量载荷，再让独立向量库从同一个空基线重建。
type vectorRebuildDurableResetter interface {
	ClearMemoryVectors(ctx context.Context, vectorIDs []string) error
}

// VectorRebuildReport stores one concise rebuild summary for CLI operators and tests.
// VectorRebuildReport 用于保存一份简洁的重建汇总，供 CLI 运维输出与测试断言使用。
type VectorRebuildReport struct {
	Mode               string
	ProjectCount       int
	MemoryCount        int
	DurableRowsUpdated int
	VectorRowsRebuilt  int
}

// VectorRebuildProgress describes one bounded progress update emitted by the rebuild workflow.
// VectorRebuildProgress 描述重建流程发出的一次有界进度更新。
type VectorRebuildProgress struct {
	// Stage identifies the current embedding, writing, or commit sub-stage.
	// Stage 标识当前的 embedding、写入或提交子阶段。
	Stage string
	// Processed counts records completed in the current sub-stage.
	// Processed 统计当前子阶段已经完成的记录数。
	Processed int
	// Total counts records in the current sub-stage.
	// Total 统计当前子阶段的记录总数。
	Total int
}

// VectorRebuildProgressReporter receives machine-readable progress without changing rebuild data semantics.
// VectorRebuildProgressReporter 接收机器可读进度，且不改变重建数据语义。
type VectorRebuildProgressReporter func(VectorRebuildProgress)

// RunVectorRebuild refreshes active durable memory vectors with the currently configured embedding model and then rebuilds the active vector backend when the runtime uses split storage.
// RunVectorRebuild 用于使用当前配置的 embedding 模型刷新 active 长期记忆向量；若运行时使用分离存储，则继续重建当前向量后端。
func RunVectorRebuild(ctx context.Context, cfg config.Config, deps MaintenanceDependencies, logger *logx.Logger) (VectorRebuildReport, error) {
	return RunVectorRebuildWithProgress(ctx, cfg, deps, logger, nil)
}

// RunVectorRebuildWithProgress runs the rebuild and reports each completed preparation or refill batch.
// RunVectorRebuildWithProgress 执行重建，并报告每个完成的准备或回填批次。
func RunVectorRebuildWithProgress(ctx context.Context, cfg config.Config, deps MaintenanceDependencies, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = logx.Default()
	}
	if deps.Embedding == nil {
		return VectorRebuildReport{}, fmt.Errorf("maintenance embedding client is not configured")
	}
	if deps.Relational == nil {
		return VectorRebuildReport{}, fmt.Errorf("maintenance relational store is not configured")
	}

	// Resolve the narrow maintenance ports up front so the tool fails fast before it starts any destructive or long-running work.
	// 在真正开始破坏性或长耗时工作前先解析狭窄维护端口，确保缺失能力时尽早失败。
	workspace, ok := deps.Relational.(appports.WorkspaceStore)
	if !ok {
		return VectorRebuildReport{}, fmt.Errorf("relational store does not support workspace listing for vector rebuild")
	}
	durable, ok := deps.Relational.(appports.MemoryVectorRebuildStore)
	if !ok {
		return VectorRebuildReport{}, fmt.Errorf("relational store does not support durable vector replacement")
	}
	var (
		nativeSQLitePath string
		nativeSnapshot   nativeVectorRebuildSnapshot
	)
	if cfg.UsesNative() {
		// Capture the relationship-backed active vector set before publishing the marker so completion can prove the same facts were rebuilt.
		// 在发布标记前记录关系层 active 向量集合，让完成阶段可以证明重建的仍是同一批事实。
		layout, layoutErr := resolveNativeStorageLayout(cfg, config.PromptLayout{})
		if layoutErr != nil {
			return VectorRebuildReport{}, fmt.Errorf("resolve native vector rebuild marker path: %w", layoutErr)
		}
		var snapshotErr error
		nativeSnapshot, snapshotErr = loadNativeVectorRebuildSnapshot(ctx, workspace, deps.Relational)
		if snapshotErr != nil {
			return VectorRebuildReport{}, snapshotErr
		}
		nativeSQLitePath = layout.SQLiteDatabase
		// The marker is deliberately durable before RecreateTable/ClearMemoryVectors; every later error leaves startup fail-closed.
		// 标记会在 RecreateTable/ClearMemoryVectors 前持久化；后续任何错误都会让启动保持失败关闭。
		if err := writeNativeVectorRebuildMarker(nativeSQLitePath); err != nil {
			return VectorRebuildReport{}, fmt.Errorf("mark native vector rebuild incomplete: %w", err)
		}
	}
	var report VectorRebuildReport
	var err error
	if cfg.UsesNative() {
		report, err = runNativeVectorRebuildWithSnapshot(ctx, cfg, deps.Embedding, workspace, durable, deps.Vector, nativeSnapshot, logger, reporter)
	} else {
		report, err = runVectorRebuildWithPortsAndProgress(ctx, cfg, deps.Embedding, workspace, durable, deps.Vector, logger, reporter)
	}
	if err != nil {
		return report, err
	}
	if cfg.UsesNative() {
		publishCtx, cancelPublish := nativeVectorRebuildPublishContext(ctx)
		defer cancelPublish()
		if err := verifyNativeVectorRebuildCompletion(publishCtx, workspace, deps.Relational, deps.Vector, nativeSnapshot, cfg.Embedding.Dimension); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed without a matching relationship/vector count", err)
		}
		if err := updateNativeEmbeddingIdentity(cfg); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed before embedding identity persistence failed", err)
		}
		if err := removeNativeVectorRebuildMarker(nativeSQLitePath); err != nil {
			return report, nativeVectorRebuildOutcomeUncertainError("native vector rebuild completed before incomplete marker removal failed", err)
		}
	}
	return report, nil
}

// runVectorRebuildWithPorts executes the rebuild workflow against the already-resolved narrow ports so tests can verify the orchestration without constructing the full maintenance dependency bundle.
// runVectorRebuildWithPorts 用于针对已经解析好的狭窄端口执行重建流程，让测试无需构造完整维护依赖包也能验证编排行为。
func runVectorRebuildWithPorts(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, logger *logx.Logger) (VectorRebuildReport, error) {
	return runVectorRebuildWithPortsAndProgress(ctx, cfg, embedding, workspace, durable, vector, logger, nil)
}

// runVectorRebuildWithPortsAndProgress keeps the testable narrow-port workflow and adds an optional progress channel.
// runVectorRebuildWithPortsAndProgress 保留可测试的窄端口流程，并增加可选进度通道。
func runVectorRebuildWithPortsAndProgress(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, logger *logx.Logger, reporter VectorRebuildProgressReporter) (VectorRebuildReport, error) {
	if cfg.UsesNative() {
		snapshot, snapshotErr := loadNativeVectorRebuildSnapshot(ctx, workspace, durable)
		if snapshotErr != nil {
			return VectorRebuildReport{Mode: "native"}, snapshotErr
		}
		return runNativeVectorRebuildWithSnapshot(ctx, cfg, embedding, workspace, durable, vector, snapshot, logger, reporter)
	}

	// Load only active/unexpired durable memories because inactive rows and trash data are intentionally outside the current recall surface and do not need rebuilding.
	// 只加载 active 且未过期的长期记忆，因为 inactive 行和垃圾箱数据本就不在当前召回面上，没有重建意义。
	projects, records, err := loadVectorRebuildRecords(ctx, workspace)
	if err != nil {
		return VectorRebuildReport{}, err
	}
	report := VectorRebuildReport{
		ProjectCount: len(projects),
		MemoryCount:  len(records),
	}
	if cfg.UsesCombinedPostgres() {
		report.Mode = "combined-postgres"
	} else {
		report.Mode = "split-lancedb"
	}
	if cfg.UsesCombinedPostgres() {
		migrator, ok := durable.(appports.MemoryVectorDimensionMigrationStore)
		if !ok {
			return report, fmt.Errorf("relational store does not support combined vector-dimension migration")
		}
		if len(records) == 0 {
			// Still migrate the combined-store vector columns even on an empty active dataset, because operators usually invoke this command after changing the configured embedding dimension.
			// 即使当前 active 数据集为空，也仍然要迁移组合库存储的向量列，因为运维通常是在切换 embedding 维度后执行该命令。
			publishVectorRebuildProgress(reporter, "committing", 0, 0)
			if err := migrator.RebuildMemoryVectorDimensions(ctx, nil); err != nil {
				return report, fmt.Errorf("rebuild combined vector dimensions: %w", err)
			}
			logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
			return report, nil
		}

		// Materialize every rebuilt vector first so combined mode can keep the old live embeddings untouched until the final PostgreSQL schema swap is ready to commit atomically.
		// 先把全部重建后的向量载荷准备好，确保组合模式在最终 PostgreSQL schema 交换准备原子提交前，不会提前触碰线上仍在使用的旧 embedding。
		rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
		if err != nil {
			return report, err
		}
		publishVectorRebuildProgress(reporter, "committing", len(rebuiltRecords), len(rebuiltRecords))
		if err := migrator.RebuildMemoryVectorDimensions(ctx, rebuiltRecords); err != nil {
			return report, fmt.Errorf("rebuild combined vector dimensions: %w", err)
		}
		report.DurableRowsUpdated = len(rebuiltRecords)
		report.VectorRowsRebuilt = len(rebuiltRecords)
		logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
		return report, nil
	}

	// Split mode still rebuilds from one shared empty baseline, but it now materializes every target vector first so preparation failures cannot leave the live sidecar half-reset.
	// split 模式仍然会从同一个空基线重建，不过现在会先准备好全部目标向量，避免准备阶段失败时把线上 sidecar 留在“只 reset 了一半”的状态。
	// Even on an empty active dataset, the detached vector table still needs to be recreated so stale sidecar rows are removed before the command exits.
	// 即使当前 active 数据集为空，也仍然要重建独立向量表，确保命令结束前顺手清掉陈旧的旁路向量行。
	if vector == nil {
		return report, fmt.Errorf("maintenance vector store is not configured for split rebuild")
	}
	resetter, ok := vector.(vectorRebuildResetter)
	if !ok {
		return report, fmt.Errorf("vector store does not support table recreation for split rebuild")
	}
	durableResetter, ok := durable.(vectorRebuildDurableResetter)
	if !ok {
		return report, fmt.Errorf("relational store does not support durable vector reset for split rebuild")
	}
	if len(records) == 0 {
		logger.Info("vector rebuild reset phase starting", "mode", report.Mode, "memory_count", report.MemoryCount)
		publishVectorRebuildProgress(reporter, "writing", 0, 0)
		if err := resetter.RecreateTable(ctx); err != nil {
			return report, fmt.Errorf("recreate split vector table: %w", err)
		}
		if err := recordNativeVectorSchemaVersion(ctx, cfg, durable); err != nil {
			return report, err
		}
		logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
		return report, nil
	}

	// Materialize the target vectors before any destructive split reset starts so model-switch failures never leave the current-dimension sidecar empty just because embedding or validation failed midway.
	// 在任何破坏性的 split reset 开始前，先把目标向量全部准备好，避免模型切换时仅因 embedding 或维度校验中途失败，就把当前维度 sidecar 留成空表。
	logger.Info("vector rebuild materialization phase starting", "mode", report.Mode, "project_count", report.ProjectCount, "memory_count", report.MemoryCount)
	rebuiltRecords, err := materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger, reporter)
	if err != nil {
		return report, err
	}
	applyResult, err := applySplitVectorRebuildRecordsWithProgress(ctx, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter)
	report.DurableRowsUpdated = applyResult.DurableRowsUpdated
	report.VectorRowsRebuilt = applyResult.VectorRowsRebuilt
	if err != nil {
		if !applyResult.ResetCompleted {
			return report, err
		}
		if recoverErr := recoverSplitVectorRebuildAfterResetWithProgress(ctx, err, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger, reporter); recoverErr != nil {
			return report, vectorRebuildOutcomeUncertainError("split vector rebuild failed after reset", err, recoverErr)
		}
		report.DurableRowsUpdated = len(rebuiltRecords)
		report.VectorRowsRebuilt = len(rebuiltRecords)
		logger.Warn("split vector rebuild recovered after intermediate failure", "mode", report.Mode, "memory_count", report.MemoryCount, "err", err)
	}
	if err := recordNativeVectorSchemaVersion(ctx, cfg, durable); err != nil {
		return report, err
	}
	logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
	return report, nil
}
