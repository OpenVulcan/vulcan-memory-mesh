// vector_dimension_migration.go rebuilds PostgreSQL vector-bearing columns when the configured embedding dimension changes, so one-shot maintenance tools can migrate the durable layout before rewriting active vectors.
// vector_dimension_migration.go 用于在配置中的 embedding 维度变化时重建 PostgreSQL 向量列，让一次性维护工具可以先迁移 durable 布局，再回填 active 向量。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// RebuildMemoryVectorDimensions rebuilds the combined-store vector-bearing columns with the current configured dimension and repopulates active rows inside the same transaction, while intentionally resetting trash/cache vector payloads instead of re-embedding them.
// RebuildMemoryVectorDimensions 用于按当前配置维度重建组合库里的向量列，并在同一事务里回填 active 行；同时有意重置垃圾箱/缓存向量载荷而不对其重新做 embedding。
func (r *vectorRepository) RebuildMemoryVectorDimensions(ctx context.Context, records []logicdomain.MemoryRecord) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if r.shared.cfg.EmbeddingDimension <= 0 {
		return fmt.Errorf("postgres embedding dimension must be > 0 for vector dimension rebuild")
	}

	callCtx, cancel := r.maintenanceWriteContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres vector dimension rebuild tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	statements := buildVectorDimensionRebuildStatements(r.shared.cfg.Schema, r.noiseEmbeddingsTable(), r.memoryNodesTable(), r.memoryNodesTrashTable(), r.shared.cfg.EmbeddingDimension)
	for _, statement := range statements {
		if _, err := tx.Exec(callCtx, strings.TrimSpace(statement)); err != nil {
			return fmt.Errorf("execute postgres vector dimension rebuild statement: %w", err)
		}
	}

	// Rewrite every active durable row before commit so outside readers never observe the freshly rebuilt column layout with zero-vector placeholders still in place.
	// 在提交前先回填全部 active durable 行，避免外部读取方观察到“列布局已重建但仍停留在零向量占位符”的中间状态。
	sqlText := buildReplaceMemoryVectorSQL(r.memoryNodesTable())
	for idx, record := range records {
		vectorID := strings.TrimSpace(record.ID)
		if vectorID == "" {
			return logicdomain.ValidationError{Field: fmt.Sprintf("records[%d].id", idx), Message: "is required"}
		}
		if len(record.Vector) != r.shared.cfg.EmbeddingDimension {
			return logicdomain.ValidationError{
				Field:   fmt.Sprintf("records[%d].vector", idx),
				Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension),
			}
		}
		tag, execErr := tx.Exec(callCtx, sqlText, encodePGVectorLiteral(record.Vector), vectorID)
		if execErr != nil {
			return fmt.Errorf("replace postgres memory vector %s inside dimension rebuild: %w", vectorID, execErr)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("replace postgres memory vector %s inside dimension rebuild affected %d rows", vectorID, tag.RowsAffected())
		}
	}

	if _, err := tx.Exec(callCtx, strings.TrimSpace(buildMemoryVectorIndexSQL(r.memoryNodesTable(), r.shared.cfg.VectorLists))); err != nil {
		return fmt.Errorf("recreate postgres vector index inside dimension rebuild tx: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return fmt.Errorf("commit postgres vector dimension rebuild tx: %w", err)
	}
	return nil
}

// buildVectorDimensionRebuildStatements returns the ordered DDL used to rebuild combined-store vector columns to a new fixed dimension while preserving non-vector data.
// buildVectorDimensionRebuildStatements 用于返回按顺序执行的 DDL，让组合库向量列迁移到新的固定维度，同时保留非向量数据。
func buildVectorDimensionRebuildStatements(schema, noiseTable, memoryTable, memoryTrashTable string, dimension int) []string {
	zeroVectorLiteral := "'" + encodePGVectorLiteral(make([]float32, dimension)) + "'"
	return []string{
		fmt.Sprintf(`DROP INDEX IF EXISTS %s.%s`, quoteIdentifier(schema), quoteIdentifier("idx_vmm_memory_nodes_embedding_ivfflat")),
		fmt.Sprintf(`TRUNCATE TABLE %s`, noiseTable),
		fmt.Sprintf(`ALTER TABLE %s DROP COLUMN IF EXISTS embedding`, noiseTable),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN embedding VECTOR(%d) NOT NULL`, noiseTable, dimension),
		fmt.Sprintf(`ALTER TABLE %s DROP COLUMN IF EXISTS embedding`, memoryTable),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN embedding VECTOR(%d) NOT NULL DEFAULT %s`, memoryTable, dimension, zeroVectorLiteral),
		fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding DROP DEFAULT`, memoryTable),
		fmt.Sprintf(`ALTER TABLE %s DROP COLUMN IF EXISTS embedding`, memoryTrashTable),
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN embedding VECTOR(%d) NOT NULL DEFAULT %s`, memoryTrashTable, dimension, zeroVectorLiteral),
		fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding DROP DEFAULT`, memoryTrashTable),
	}
}
