// SQLite durable vector enumeration and replacement used by rebuild tools.
// SQLite 适配层重建工具使用的长期向量枚举与替换。
package vldb_sqlite

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strconv"
	"strings"
	"time"
)

// ListProjectMemories returns unified durable memory rows under one project so migrations can rebuild vector rows directly from the main memory table.
// ListProjectMemories 用于返回某个项目下的统一长期记忆行，让迁移流程可以直接从主记忆表重建向量行。
func (s *Store) ListProjectMemories(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	nowMs := time.Now().UTC().UnixMilli()
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE project_id = ? AND `+buildActiveUnexpiredMemoryCondition("", nowMs)+`
ORDER BY created_timestamp ASC, id ASC
`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project memories: %w", err)
	}
	out := make([]logicdomain.MemoryRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.toMemoryNodeRecord()
		if err != nil {
			return nil, err
		}
		out = append(out, memoryRecordFromNode(record))
	}
	return out, nil
}

// memoryRecordFromNode converts one active durable SQLite memory row into the sidecar/vector-store record used by rebuild, migration, and lifecycle sync paths.
// memoryRecordFromNode 用于把一条 SQLite 长期记忆行转换成重建、迁移和生命周期同步路径使用的旁路向量记录。
func memoryRecordFromNode(record logicdomain.MemoryNodeRecord) logicdomain.MemoryRecord {
	filter := logicdomain.SearchFilter{
		UserID:    record.UserID,
		TeamID:    record.TeamID,
		SpaceID:   record.SpaceID,
		ProjectID: record.ProjectID,
	}
	if record.SourceKind == logicdomain.MemorySourceKindTurnExtract || record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
		filter.SessionID = record.OriginSessionID
	}
	metadata := map[string]string{
		"category":     strconv.Itoa(record.Category),
		"details":      record.Details,
		"source_kind":  logicdomain.MemorySourceKindLabel(record.SourceKind),
		"scope_level":  logicdomain.MemoryScopeLevelLabel(record.ScopeLevel),
		"priority":     strconv.Itoa(record.Priority),
		"memory_level": strconv.Itoa(record.MemoryLevel),
	}
	if record.SourceTurnID > 0 {
		metadata["turn_id"] = strconv.FormatUint(record.SourceTurnID, 10)
	}
	return logicdomain.MemoryRecord{
		ID:           record.VectorID,
		Text:         record.Abstract,
		Vector:       append([]float32(nil), record.Vector...),
		Filter:       filter,
		SourceTurnID: record.SourceTurnID,
		Status:       record.Status,
		ExpiresAt:    record.ExpiresAt,
		Metadata:     metadata,
		CreatedAt:    record.CreatedAt,
	}
}

// ListProjectMemoriesForMaintenance reuses the normal SQLite project-memory scan for maintenance commands because SQLite already executes the durable read under the same bounded gateway timeout.
// ListProjectMemoriesForMaintenance 用于让维护命令复用常规 SQLite 项目记忆扫描，因为 SQLite durable 读取本就走同一套有界网关超时。
func (s *Store) ListProjectMemoriesForMaintenance(ctx context.Context, projectID uint64) ([]logicdomain.MemoryRecord, error) {
	return s.ListProjectMemories(ctx, projectID)
}

// ReplaceMemoryVectors rewrites the durable SQLite vector_json payload for one active-memory batch so split-mode rebuild tools can keep SQLite and LanceDB strictly synchronized after a model change.
// ReplaceMemoryVectors 用于重写一批长期记忆在 SQLite 中保存的 vector_json，让分离模式重建工具在模型切换后保持 SQLite 与 LanceDB 严格同步。
func (s *Store) ReplaceMemoryVectors(ctx context.Context, records []logicdomain.MemoryRecord) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(records) == 0 {
		return nil
	}

	// Keep the durable vector rewrite under the shared write lock so one maintenance rebuild cannot interleave with ordinary memory mutations.
	// 把 durable 向量重写放进共享写锁内，避免维护重建和普通记忆写入彼此交叉。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	items := make([][]any, 0, len(records))
	for idx, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].id", idx), Message: "is required"}
		}
		if len(record.Vector) == 0 {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].vector", idx), Message: "must contain at least one dimension"}
		}
		vectorJSON, err := encodeFloat32Slice(record.Vector)
		if err != nil {
			return fmt.Errorf("records[%d] vector %q: %w", idx, vectorID, err)
		}
		items = append(items, []any{vectorJSON, vectorID})
	}
	result, err := s.execBatchResult(ctx, `
UPDATE vmm_memory_nodes
SET vector_json = ?
WHERE vector_id = ?
`, items)
	if err != nil {
		return sqliteMaintenanceBatchError("replace sqlite memory vectors", err)
	}
	// Require every selected durable row to be rewritten before the sidecar vector table can be refilled from the same batch.
	// 在用同一批数据回填旁路向量表之前，要求每个已选长期行都完成重写。
	if expectedRows := int64(len(items)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("replace sqlite memory vectors affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "replace sqlite memory vectors", err)
	}
	return nil
}

// ClearMemoryVectors clears one active durable batch's SQLite vector_json payload back to the empty baseline before split-mode maintenance rebuilds repopulate both SQLite and LanceDB from scratch.
// ClearMemoryVectors 用于把一批 active durable 记忆的 SQLite vector_json 清回空基线，再由 split 模式维护重建从零开始同时回填 SQLite 与 LanceDB。
func (s *Store) ClearMemoryVectors(ctx context.Context, vectorIDs []string) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(vectorIDs) == 0 {
		return nil
	}

	// Keep the destructive reset under the shared write lock so no foreground writer can observe or reintroduce stale vectors between the reset and the later rebuild batches.
	// 把破坏性的向量清空动作放进共享写锁内，避免前台写入在清空与后续重建批次之间观察到或重新引入陈旧向量。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	items := make([][]any, 0, len(vectorIDs))
	for idx, vectorID := range vectorIDs {
		cleanedVectorID := strings.TrimSpace(vectorID)
		if cleanedVectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("vector_ids[%d]", idx), Message: "is required"}
		}
		items = append(items, []any{cleanedVectorID})
	}
	result, err := s.execBatchResult(ctx, `
UPDATE vmm_memory_nodes
SET vector_json = '[]'
WHERE vector_id = ?
`, items)
	if err != nil {
		return sqliteMaintenanceBatchError("clear sqlite memory vectors", err)
	}
	// Require every durable row to be cleared so the reset phase cannot leave stale SQLite vectors behind the rebuilt sidecar table.
	// 要求每个长期行都被清空，避免 reset 阶段在旁路表重建后留下陈旧的 SQLite 向量。
	if expectedRows := int64(len(items)); result.RowsChanged != expectedRows {
		err := fmt.Errorf("clear sqlite memory vectors affected %d rows, want %d", result.RowsChanged, expectedRows)
		return sqlitePartialMutationError(result.RowsChanged > 0, "clear sqlite memory vectors", err)
	}
	return nil
}

// sqliteMaintenanceBatchError preserves ordinary batch failures while marking SQLite commit-boundary failures that may already have changed maintenance rows.
// sqliteMaintenanceBatchError 用于保留普通批处理失败，同时标记可能已经改变维护行的 SQLite 提交边界失败。
func sqliteMaintenanceBatchError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if classifiedErr := sqliteWriteCommitBoundaryError(operation, err); logicdomain.IsOutcomeUncertain(classifiedErr) {
		return classifiedErr
	}
	return fmt.Errorf("%s: %w", operation, err)
}
