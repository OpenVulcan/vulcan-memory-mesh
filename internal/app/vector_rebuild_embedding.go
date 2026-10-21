// Vector rebuild record enumeration, embedding batches and progress updates.
// 应用层向量重建记录枚举、分批嵌入与进度更新。
package app

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"strings"
	"time"
)

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
	return materializeVectorRebuildRecordsWithProgress(ctx, embedding, records, expectedDimension, batchSize, logger, nil)
}

// materializeVectorRebuildRecordsWithProgress embeds bounded batches and reports each completed batch.
// materializeVectorRebuildRecordsWithProgress 按有界批次生成向量，并报告每个已完成批次。
func materializeVectorRebuildRecordsWithProgress(ctx context.Context, embedding appports.EmbeddingClient, records []logicdomain.MemoryRecord, expectedDimension, batchSize int, logger *logx.Logger, reporter VectorRebuildProgressReporter) ([]logicdomain.MemoryRecord, error) {
	if batchSize <= 0 {
		batchSize = len(records)
	}
	rebuilt := make([]logicdomain.MemoryRecord, 0, len(records))
	publishVectorRebuildProgress(reporter, "embedding", 0, len(records))
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
		publishVectorRebuildProgress(reporter, "embedding", end, len(records))
	}
	return rebuilt, nil
}

// publishVectorRebuildProgress forwards one validated workflow update to the optional observer.
// publishVectorRebuildProgress 将一条经过边界控制的流程更新转发给可选观察者。
func publishVectorRebuildProgress(reporter VectorRebuildProgressReporter, stage string, processed, total int) {
	if reporter == nil {
		return
	}
	if total < 0 {
		total = 0
	}
	if processed < 0 {
		processed = 0
	}
	if processed > total {
		processed = total
	}
	reporter(VectorRebuildProgress{Stage: stage, Processed: processed, Total: total})
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
