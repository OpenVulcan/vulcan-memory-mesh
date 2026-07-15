// vector_rebuild.go coordinates one-shot durable/vector-store rebuilds so maintenance tools can refresh embeddings after the configured model changes.
// vector_rebuild.go 用于编排一次性 durable/向量库重建流程，让维护工具在切换配置中的 embedding 模型后刷新向量。
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

const (
	// vectorRebuildBudgetRetryDelay keeps the maintenance loop patient when every configured key is only temporarily blocked by the current RPM/TPM/RPD budget window.
	// vectorRebuildBudgetRetryDelay 用于在所有已配置 Key 只是暂时被 RPM/TPM/RPD 预算窗口卡住时，让维护流程保持耐心等待。
	vectorRebuildBudgetRetryDelay = 30 * time.Second

	// vectorRebuildWriteBatchSize keeps split-mode durable/vector refill writes bounded without coupling storage write chunking to the embedding model's API batch contract.
	// vectorRebuildWriteBatchSize 用于让 split 模式的 durable/向量回填写入保持有界，同时避免把存储写入分批错误绑死到 embedding 模型的 API 批契约上。
	vectorRebuildWriteBatchSize = 10
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

// RunVectorRebuild refreshes active durable memory vectors with the currently configured embedding model and then rebuilds the active vector backend when the runtime uses split storage.
// RunVectorRebuild 用于使用当前配置的 embedding 模型刷新 active 长期记忆向量；若运行时使用分离存储，则继续重建当前向量后端。
func RunVectorRebuild(ctx context.Context, cfg config.Config, deps MaintenanceDependencies, logger *logx.Logger) (VectorRebuildReport, error) {
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
	return runVectorRebuildWithPorts(ctx, cfg, deps.Embedding, workspace, durable, deps.Vector, logger)
}

// runVectorRebuildWithPorts executes the rebuild workflow against the already-resolved narrow ports so tests can verify the orchestration without constructing the full maintenance dependency bundle.
// runVectorRebuildWithPorts 用于针对已经解析好的狭窄端口执行重建流程，让测试无需构造完整维护依赖包也能验证编排行为。
func runVectorRebuildWithPorts(ctx context.Context, cfg config.Config, embedding appports.EmbeddingClient, workspace appports.WorkspaceStore, durable appports.MemoryVectorRebuildStore, vector appports.VectorStore, logger *logx.Logger) (VectorRebuildReport, error) {
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
			if err := migrator.RebuildMemoryVectorDimensions(ctx, nil); err != nil {
				return report, fmt.Errorf("rebuild combined vector dimensions: %w", err)
			}
			logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
			return report, nil
		}

		// Materialize every rebuilt vector first so combined mode can keep the old live embeddings untouched until the final PostgreSQL schema swap is ready to commit atomically.
		// 先把全部重建后的向量载荷准备好，确保组合模式在最终 PostgreSQL schema 交换准备原子提交前，不会提前触碰线上仍在使用的旧 embedding。
		rebuiltRecords, err := materializeVectorRebuildRecords(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger)
		if err != nil {
			return report, err
		}
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
		if err := resetter.RecreateTable(ctx); err != nil {
			return report, fmt.Errorf("recreate split vector table: %w", err)
		}
		logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
		return report, nil
	}

	// Materialize the target vectors before any destructive split reset starts so model-switch failures never leave the current-dimension sidecar empty just because embedding or validation failed midway.
	// 在任何破坏性的 split reset 开始前，先把目标向量全部准备好，避免模型切换时仅因 embedding 或维度校验中途失败，就把当前维度 sidecar 留成空表。
	logger.Info("vector rebuild materialization phase starting", "mode", report.Mode, "project_count", report.ProjectCount, "memory_count", report.MemoryCount)
	rebuiltRecords, err := materializeVectorRebuildRecords(ctx, embedding, records, cfg.Embedding.Dimension, cfg.MaintenanceTool.VectorRebuildBatchSize, logger)
	if err != nil {
		return report, err
	}
	applyResult, err := applySplitVectorRebuildRecords(ctx, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger)
	report.DurableRowsUpdated = applyResult.DurableRowsUpdated
	report.VectorRowsRebuilt = applyResult.VectorRowsRebuilt
	if err != nil {
		if !applyResult.ResetCompleted {
			return report, err
		}
		if recoverErr := recoverSplitVectorRebuildAfterReset(ctx, err, report.Mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger); recoverErr != nil {
			return report, vectorRebuildOutcomeUncertainError("split vector rebuild failed after reset", err, recoverErr)
		}
		report.DurableRowsUpdated = len(rebuiltRecords)
		report.VectorRowsRebuilt = len(rebuiltRecords)
		logger.Warn("split vector rebuild recovered after intermediate failure", "mode", report.Mode, "memory_count", report.MemoryCount, "err", err)
	}
	logger.Info("vector rebuild completed", "mode", report.Mode, "durable_rows_updated", report.DurableRowsUpdated, "vector_rows_rebuilt", report.VectorRowsRebuilt)
	return report, nil
}

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
	if _, err := applySplitVectorRebuildRecords(repairCtx, mode, durable, durableResetter, resetter, vector, rebuiltRecords, logger); err != nil {
		logger.Error("split vector rebuild automatic repair failed", "mode", mode, "memory_count", len(rebuiltRecords), "err", err)
		return err
	}
	logger.Warn("split vector rebuild automatic repair completed", "mode", mode, "memory_count", len(rebuiltRecords))
	return nil
}

// collectVectorRebuildVectorIDs extracts one stable list of durable vector ids so split-mode reset phases can clear SQLite payloads before rebuilding them from scratch.
// collectVectorRebuildVectorIDs 用于提取一份稳定的 durable vector id 列表，让 split 模式的重置阶段可以先清空 SQLite 载荷，再从零开始重建。
func collectVectorRebuildVectorIDs(records []logicdomain.MemoryRecord) []string {
	vectorIDs := make([]string, 0, len(records))
	for _, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			continue
		}
		vectorIDs = append(vectorIDs, vectorID)
	}
	return vectorIDs
}

// loadVectorRebuildRecords enumerates every active project and flattens their active durable memories into one deterministic rebuild list.
// loadVectorRebuildRecords 用于枚举所有活跃项目，并把它们的 active 长期记忆摊平成一个确定性的重建列表。
func loadVectorRebuildRecords(ctx context.Context, workspace appports.WorkspaceStore) ([]logicdomain.ProjectRecord, []logicdomain.MemoryRecord, error) {
	projectLister, hasProjectLister := workspace.(appports.MaintenanceProjectLister)
	var (
		projects []logicdomain.ProjectRecord
		err      error
	)
	if hasProjectLister {
		projects, err = projectLister.ListProjectsForMaintenance(ctx)
	} else {
		projects, err = workspace.ListProjects(ctx)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list projects for vector rebuild: %w", err)
	}
	maintenanceLister, hasMaintenanceLister := workspace.(appports.MaintenanceProjectMemoryLister)
	records := make([]logicdomain.MemoryRecord, 0)
	for _, project := range projects {
		var (
			projectRecords []logicdomain.MemoryRecord
			listErr        error
		)
		if hasMaintenanceLister {
			projectRecords, listErr = maintenanceLister.ListProjectMemoriesForMaintenance(ctx, project.ID)
		} else {
			projectRecords, listErr = workspace.ListProjectMemories(ctx, project.ID)
		}
		if listErr != nil {
			return nil, nil, fmt.Errorf("list project %d memories for vector rebuild: %w", project.ID, listErr)
		}
		records = append(records, projectRecords...)
	}
	return projects, records, nil
}

// materializeVectorRebuildRecords prepares one fully rebuilt in-memory durable snapshot in bounded batches before combined PostgreSQL mode starts its atomic schema swap, so later maintenance writes never expose half-finished live vectors or unbounded temporary vector payloads.
// materializeVectorRebuildRecords 用于在组合 PostgreSQL 模式开始原子 schema 交换前，以有界批次准备完整的内存态 durable 重建快照，既避免后续维护写入暴露“只重建了一半”的线上向量，也避免临时向量载荷无限膨胀。
func materializeVectorRebuildRecords(ctx context.Context, embedding appports.EmbeddingClient, records []logicdomain.MemoryRecord, expectedDimension, batchSize int, logger *logx.Logger) ([]logicdomain.MemoryRecord, error) {
	if batchSize <= 0 {
		batchSize = len(records)
	}
	rebuilt := make([]logicdomain.MemoryRecord, 0, len(records))
	for start := 0; start < len(records); start += batchSize {
		end := start + batchSize
		if end > len(records) {
			end = len(records)
		}
		vectors, err := embedVectorRebuildBatch(ctx, embedding, records[start:end], logger)
		if err != nil {
			return nil, fmt.Errorf("embed rebuild batch %d-%d: %w", start, end, err)
		}
		if err := validateVectorRebuildDimensions(vectors, expectedDimension); err != nil {
			return nil, fmt.Errorf("validate rebuild batch %d-%d dimensions: %w", start, end, err)
		}
		rebuilt = append(rebuilt, cloneVectorRebuildBatch(records[start:end], vectors)...)
	}
	return rebuilt, nil
}

// embedVectorRebuildBatch embeds one durable-memory batch and patiently waits when every configured key is only temporarily blocked by runtime budgets.
// embedVectorRebuildBatch 用于为一批长期记忆生成向量；若所有已配置 Key 只是临时被运行时预算挡住，则耐心等待后继续。
func embedVectorRebuildBatch(ctx context.Context, client appports.EmbeddingClient, records []logicdomain.MemoryRecord, logger *logx.Logger) ([][]float32, error) {
	texts := make([]string, 0, len(records))
	for idx, record := range records {
		text := strings.TrimSpace(record.Text)
		if text == "" {
			return nil, fmt.Errorf("memory record %d (%s) has empty rebuild text", idx, strings.TrimSpace(record.ID))
		}
		texts = append(texts, text)
	}
	for {
		resp, err := client.Embed(ctx, appports.EmbeddingRequest{Texts: texts})
		if err == nil {
			if err := resp.ValidateStrict(len(texts)); err != nil {
				return nil, err
			}
			return resp.Vectors, nil
		}
		if !ai_key_failover.IsBudgetExhaustedCandidatesError(err) {
			return nil, err
		}
		logger.Info("vector rebuild embedding budgets exhausted, waiting before retry", "retry_after", vectorRebuildBudgetRetryDelay.String(), "batch_size", len(records))
		if err := waitVectorRebuildBudgetWindow(ctx, vectorRebuildBudgetRetryDelay); err != nil {
			return nil, err
		}
	}
}

// validateVectorRebuildDimensions rejects unexpected embedding lengths before any durable write starts so rebuilds cannot leave split storage half-updated.
// validateVectorRebuildDimensions 用于在任何 durable 写入之前拒绝异常的 embedding 维度，避免重建把分离存储留在“只更新一半”的状态。
func validateVectorRebuildDimensions(vectors [][]float32, expectedDimension int) error {
	if expectedDimension <= 0 {
		return fmt.Errorf("configured embedding dimension must be > 0")
	}
	for idx, vector := range vectors {
		if len(vector) != expectedDimension {
			return logicdomain.ValidationError{
				Field:   fmt.Sprintf("vectors[%d]", idx),
				Message: fmt.Sprintf("must contain exactly %d dimensions", expectedDimension),
			}
		}
	}
	return nil
}

// cloneVectorRebuildRecords deep-copies one rebuild snapshot so automatic repairs and later batch rewrites never mutate the caller's original in-memory durable image.
// cloneVectorRebuildRecords 用于深拷贝一份重建快照，避免自动修复和后续批次写回意外修改调用方手里的原始 durable 内存镜像。
func cloneVectorRebuildRecords(records []logicdomain.MemoryRecord) []logicdomain.MemoryRecord {
	cloned := make([]logicdomain.MemoryRecord, 0, len(records))
	for _, record := range records {
		cloned = append(cloned, cloneVectorRebuildRecord(record))
	}
	return cloned
}

// cloneVectorRebuildRecord deep-copies one memory record's vector and metadata payload so maintenance snapshots stay isolated from later mutations.
// cloneVectorRebuildRecord 用于深拷贝单条记忆记录的向量和元数据载荷，确保维护快照与后续修改相互隔离。
func cloneVectorRebuildRecord(record logicdomain.MemoryRecord) logicdomain.MemoryRecord {
	current := record
	current.Vector = append([]float32(nil), record.Vector...)
	if record.Metadata != nil {
		current.Metadata = make(map[string]string, len(record.Metadata))
		for key, value := range record.Metadata {
			current.Metadata[key] = value
		}
	}
	return current
}

// cloneVectorRebuildBatch copies one memory batch and injects the freshly generated vectors so durable writes and later sidecar rebuilds observe the same exact payload.
// cloneVectorRebuildBatch 用于复制一批记忆记录并注入新生成的向量，让 durable 写回和后续旁路重建看到完全一致的载荷。
func cloneVectorRebuildBatch(records []logicdomain.MemoryRecord, vectors [][]float32) []logicdomain.MemoryRecord {
	cloned := make([]logicdomain.MemoryRecord, 0, len(records))
	for idx, record := range records {
		current := cloneVectorRebuildRecord(record)
		current.Vector = append([]float32(nil), vectors[idx]...)
		cloned = append(cloned, current)
	}
	return cloned
}

// waitVectorRebuildBudgetWindow blocks until either the retry delay elapses or the caller cancels the maintenance context.
// waitVectorRebuildBudgetWindow 用于阻塞到重试延迟结束，或者调用方取消维护上下文为止。
func waitVectorRebuildBudgetWindow(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
