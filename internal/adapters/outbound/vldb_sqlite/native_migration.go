// native_migration.go streams durable memory facts through the shared business mapping for native migration.
// native_migration.go 通过共用业务映射流式提供长期记忆事实，供原生迁移重建旁路向量。
package vldb_sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// nativeMigrationTrashMemoryRow carries the source recycle batch together with the exact durable memory row shape.
// nativeMigrationTrashMemoryRow 将来源回收批次和明确的长期记忆行结构一起携带，避免依赖 trash 表的隐含列顺序。
type nativeMigrationTrashMemoryRow struct {
	memoryNodeRow
	BatchID uint64 `json:"batch_id"`
}

// NativeRestorableTrashMemory exposes the durable identity needed to rewrite one recovered trash vector without relying on a non-unique vector id.
// NativeRestorableTrashMemory 暴露精确 durable 身份，使回收向量写回不依赖非唯一的 vector id。
type NativeRestorableTrashMemory struct {
	// BatchID identifies the retention and management batch owning the trash row.
	// BatchID 标识拥有该回收行的保留及管理批次。
	BatchID uint64
	// MemoryID identifies the original durable memory id within the batch primary key.
	// MemoryID 标识该批次复合主键中的原始长期记忆 ID。
	MemoryID uint64
	// Record carries the strict durable memory and vector payload presented to the embedding workflow.
	// Record 携带严格解析后的长期记忆及向量载荷，供 embedding 流程使用。
	Record logicdomain.MemoryRecord
}

// WalkNativeMigrationMemories visits every durable memory, including inactive rows, in bounded primary-key pages.
// WalkNativeMigrationMemories 按主键有界分页遍历全部长期记忆，包括非活跃行；原始向量无效时返回错误。
func (s *Store) WalkNativeMigrationMemories(ctx context.Context, visit func(logicdomain.MemoryRecord) error) error {
	if visit == nil {
		return fmt.Errorf("native migration memory visitor is required")
	}
	const pageSize = 256
	var lastID uint64
	firstPage := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var rows []memoryNodeRow
		var err error
		if firstPage {
			rows, err = queryRows[memoryNodeRow](s, ctx, "SELECT * FROM vmm_memory_nodes ORDER BY id LIMIT ?", pageSize)
		} else {
			rows, err = queryRows[memoryNodeRow](s, ctx, "SELECT * FROM vmm_memory_nodes WHERE id > ? ORDER BY id LIMIT ?", lastID, pageSize)
		}
		if err != nil {
			return fmt.Errorf("read native migration memory page: %w", err)
		}
		for _, row := range rows {
			vector, err := decodeNativeMigrationVector(row.VectorJSON)
			if err != nil {
				return fmt.Errorf("memory %d vector %q: %w", row.ID, row.VectorID, err)
			}
			// Preserve the existing scope, lifecycle and metadata mapping after strict durable-vector decoding.
			// 严格解析已保存向量后，保留既有范围、生命周期与元数据映射。
			node := row.toMemoryNodeRecord()
			node.Vector = vector
			if err := visit(memoryRecordFromNode(node)); err != nil {
				return err
			}
			lastID = row.ID
		}
		if len(rows) < pageSize {
			return nil
		}
		firstPage = false
	}
}

// WalkNativeMigrationRestorableTrash visits memories retained by a live, restorable management recycle batch.
// WalkNativeMigrationRestorableTrash 遍历仍可恢复的管理回收批次记忆，供迁移流程恢复长期向量载荷。
func (s *Store) WalkNativeMigrationRestorableTrash(ctx context.Context, now time.Time, visit func(logicdomain.MemoryRecord) error) error {
	if visit == nil {
		return fmt.Errorf("native migration trash visitor is required")
	}
	return s.WalkNativeMigrationRestorableTrashRows(ctx, now, func(row NativeRestorableTrashMemory) error {
		return visit(row.Record)
	})
}

// WalkNativeMigrationRestorableTrashRows visits eligible trash rows with their composite identity for safe vector replacement.
// WalkNativeMigrationRestorableTrashRows 遍历带复合身份的可恢复回收行，供安全向量替换使用。
func (s *Store) WalkNativeMigrationRestorableTrashRows(ctx context.Context, now time.Time, visit func(NativeRestorableTrashMemory) error) error {
	if visit == nil {
		return fmt.Errorf("native migration trash row visitor is required")
	}
	const pageSize = 256
	const memoryColumns = `
 t.id AS id, t.team_id AS team_id, t.space_id AS space_id, t.project_id AS project_id,
 t.user_id AS user_id, t.origin_session_id AS origin_session_id, t.source_turn_id AS source_turn_id,
 t.vector_id AS vector_id, t.vector_json AS vector_json, t.source_kind AS source_kind,
 t.scope_level AS scope_level, t.category AS category, t.abstract AS abstract, t.details AS details,
 t.memory_status AS memory_status, t.priority AS priority, t.memory_level AS memory_level,
 t.refresh_weight AS refresh_weight, t.support_count AS support_count, t.rebuttal_count AS rebuttal_count,
 t.status_reason AS status_reason, t.expires_timestamp AS expires_timestamp,
 t.last_recalled_timestamp AS last_recalled_timestamp, t.last_adopted_timestamp AS last_adopted_timestamp,
 t.last_reinforced_timestamp AS last_reinforced_timestamp, t.recalled_count AS recalled_count,
 t.adopted_count AS adopted_count, t.reinforcement_count AS reinforcement_count,
 t.cross_session_adopted_count AS cross_session_adopted_count, t.decay_disabled AS decay_disabled,
 t.dedupe_hash AS dedupe_hash, t.created_timestamp AS created_timestamp, t.updated_timestamp AS updated_timestamp`
	nowMillis := now.UnixMilli()
	var lastBatchID uint64
	var lastID uint64
	firstPage := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := `
SELECT t.batch_id AS batch_id,` + memoryColumns + `
FROM vmm_memory_nodes_trash AS t
JOIN vmm_management_recycle_batches AS m ON m.batch_id = t.batch_id
JOIN vmm_recycle_batches AS b ON b.id = t.batch_id
WHERE m.restorable = 1
  AND m.state = 'recycled'
  AND (m.expires_timestamp = 0 OR m.expires_timestamp > ?)
  AND b.purged_at = 0`
		params := []any{nowMillis}
		if !firstPage {
			query += `
  AND (t.batch_id > ? OR (t.batch_id = ? AND t.id > ?))`
			params = append(params, lastBatchID, lastBatchID, lastID)
		}
		query += `
ORDER BY t.batch_id ASC, t.id ASC
LIMIT ?`
		params = append(params, pageSize)
		rows, err := queryRows[nativeMigrationTrashMemoryRow](s, ctx, query, params...)
		if err != nil {
			return fmt.Errorf("read native migration restorable trash page: %w", err)
		}
		for _, row := range rows {
			vector, err := decodeNativeMigrationVector(row.VectorJSON)
			if err != nil {
				return fmt.Errorf("trash memory batch %d id %d vector %q: %w", row.BatchID, row.ID, row.VectorID, err)
			}
			// Preserve the same lifecycle, scope and metadata mapping as live durable memories after strict vector decoding.
			// 严格解析向量后，沿用长期记忆行的生命周期、范围和元数据映射。
			node := row.toMemoryNodeRecord()
			node.Vector = vector
			if err := visit(NativeRestorableTrashMemory{
				BatchID:  row.BatchID,
				MemoryID: row.ID,
				Record:   memoryRecordFromNode(node),
			}); err != nil {
				return err
			}
			lastBatchID = row.BatchID
			lastID = row.ID
		}
		if len(rows) < pageSize {
			return nil
		}
		firstPage = false
	}
}

// UpdateNativeRestorableTrashVector atomically replaces exactly one eligible trash row selected by its composite identity.
// UpdateNativeRestorableTrashVector 以复合身份原子替换且只允许命中一条可恢复回收行。
func (s *Store) UpdateNativeRestorableTrashVector(ctx context.Context, batchID, memoryID uint64, vector []float32) error {
	if !s.hasSQLiteStore() {
		return fmt.Errorf("sqlite store is not initialized")
	}
	vectorJSON, err := json.Marshal(vector)
	if err != nil {
		return fmt.Errorf("encode restorable trash vector: %w", err)
	}

	// A single parameterized UPDATE is one SQLite transaction, and the exact affected-row check makes a stale or mismatched composite identity fail closed.
	// 单条参数化 UPDATE 本身就是一个 SQLite 事务，并通过精确影响行数检查让过期或错误复合身份安全失败。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.execExpectRowsChanged(ctx, "update native restorable trash vector", 1, `
UPDATE vmm_memory_nodes_trash
SET vector_json = ?
WHERE batch_id = ? AND id = ?
`, string(vectorJSON), batchID, memoryID); err != nil {
		return fmt.Errorf("update native restorable trash vector batch %d id %d: %w", batchID, memoryID, err)
	}
	return nil
}

// decodeNativeMigrationVector distinguishes intentionally absent vectors from malformed stored JSON.
// decodeNativeMigrationVector 区分明确缺失的向量与格式损坏的已保存 JSON，不把解析错误隐藏为空向量。
func decodeNativeMigrationVector(raw string) ([]float32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &elements); err != nil {
		return nil, fmt.Errorf("invalid durable vector JSON: %w", err)
	}
	vector := make([]float32, len(elements))
	for i, element := range elements {
		if strings.TrimSpace(string(element)) == "null" {
			return nil, fmt.Errorf("durable vector element %d is null", i)
		}
		if err := json.Unmarshal(element, &vector[i]); err != nil {
			return nil, fmt.Errorf("invalid durable vector element %d: %w", i, err)
		}
	}
	return vector, nil
}
