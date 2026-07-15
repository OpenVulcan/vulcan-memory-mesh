// vector_dimension_migration_test.go verifies the SQL emitted for combined-store vector rebuild maintenance so dimension migrations do not silently skip required schema steps.
// vector_dimension_migration_test.go 用于验证组合库存储向量重建维护动作生成的 SQL，避免维度迁移时静默漏掉必要的 schema 步骤。
package vldb_postgres

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestBuildReplaceMemoryVectorSQLTargetsEmbeddingByVectorID verifies combined-mode durable rebuilds only rewrite the embedding payload identified by stable vector_id.
// TestBuildReplaceMemoryVectorSQLTargetsEmbeddingByVectorID 用于验证组合模式 durable 重建只会按稳定的 vector_id 重写 embedding 载荷。
func TestBuildReplaceMemoryVectorSQLTargetsEmbeddingByVectorID(t *testing.T) {
	sqlText := buildReplaceMemoryVectorSQL(`"public"."vmm_memory_nodes"`)
	if !strings.Contains(sqlText, `UPDATE "public"."vmm_memory_nodes"`) {
		t.Fatalf("expected qualified table in rebuild sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, "SET embedding = $1::vector") {
		t.Fatalf("expected embedding assignment in rebuild sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, "WHERE vector_id = $2") {
		t.Fatalf("expected vector_id filter in rebuild sql, got %q", sqlText)
	}
}

// TestBuildMemoryVectorIndexSQLTargetsConfiguredTable verifies the ANN index recreation SQL stays identical between startup bootstrap and maintenance rebuilds.
// TestBuildMemoryVectorIndexSQLTargetsConfiguredTable 用于验证 ANN 索引重建 SQL 在启动初始化与维护重建之间保持一致，并且始终指向当前配置表。
func TestBuildMemoryVectorIndexSQLTargetsConfiguredTable(t *testing.T) {
	sqlText := buildMemoryVectorIndexSQL(`"tenant_a"."vmm_memory_nodes"`, 128)
	if !strings.Contains(sqlText, `CREATE INDEX IF NOT EXISTS "idx_vmm_memory_nodes_embedding_ivfflat"`) {
		t.Fatalf("expected vector index name in sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, `ON "tenant_a"."vmm_memory_nodes" USING ivfflat`) {
		t.Fatalf("expected qualified memory table in sql, got %q", sqlText)
	}
	if !strings.Contains(sqlText, `WITH (lists = 128)`) {
		t.Fatalf("expected lists clause in sql, got %q", sqlText)
	}
}

// TestBuildVectorDimensionRebuildStatementsCoversNoiseActiveAndTrashTables verifies dimension migration resets the cache table, rebuilds active/trash embedding columns, and drops the ANN index first.
// TestBuildVectorDimensionRebuildStatementsCoversNoiseActiveAndTrashTables 用于验证维度迁移会先处理缓存表，再重建 active/trash embedding 列，并优先删除 ANN 索引。
func TestBuildVectorDimensionRebuildStatementsCoversNoiseActiveAndTrashTables(t *testing.T) {
	statements := buildVectorDimensionRebuildStatements(
		"tenant_a",
		`"tenant_a"."vmm_noise_embeddings"`,
		`"tenant_a"."vmm_memory_nodes"`,
		`"tenant_a"."vmm_memory_nodes_trash"`,
		4,
	)
	joined := strings.Join(statements, "\n")
	required := []string{
		`DROP INDEX IF EXISTS "tenant_a"."idx_vmm_memory_nodes_embedding_ivfflat"`,
		`TRUNCATE TABLE "tenant_a"."vmm_noise_embeddings"`,
		`ALTER TABLE "tenant_a"."vmm_noise_embeddings" ADD COLUMN embedding VECTOR(4) NOT NULL`,
		`ALTER TABLE "tenant_a"."vmm_memory_nodes" ADD COLUMN embedding VECTOR(4) NOT NULL DEFAULT '[0,0,0,0]'`,
		`ALTER TABLE "tenant_a"."vmm_memory_nodes" ALTER COLUMN embedding DROP DEFAULT`,
		`ALTER TABLE "tenant_a"."vmm_memory_nodes_trash" ADD COLUMN embedding VECTOR(4) NOT NULL DEFAULT '[0,0,0,0]'`,
		`ALTER TABLE "tenant_a"."vmm_memory_nodes_trash" ALTER COLUMN embedding DROP DEFAULT`,
	}
	for _, fragment := range required {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("expected dimension rebuild sql to contain %q, got %q", fragment, joined)
		}
	}
}

// TestPostgresVectorDimensionRebuildCommitErrorMarksOutcomeUncertain verifies schema/data rebuild commit ambiguity reports maintenance uncertainty without fresh-vector retention.
// TestPostgresVectorDimensionRebuildCommitErrorMarksOutcomeUncertain 用于验证 schema/data 重建提交结果不明时会上报维护结果不确定，但不会标记新向量保留。
func TestPostgresVectorDimensionRebuildCommitErrorMarksOutcomeUncertain(t *testing.T) {
	// err represents a failed commit after vector-bearing columns and active memory embeddings may already be rebuilt.
	// err 表示向量列与 active memory embedding 可能已经重建后的提交失败。
	err := postgresCommitOutcomeUncertainError("rebuild vector dimensions", "commit postgres vector dimension rebuild tx", errors.New("commit acknowledgement lost"))
	if !logicdomain.IsOutcomeUncertain(err) {
		t.Fatalf("expected vector dimension rebuild commit to be outcome-uncertain, got %v", err)
	}
	if logicdomain.IsFreshVectorReferenceUncertain(err) {
		t.Fatalf("did not expect vector dimension rebuild commit to mark fresh-vector uncertainty, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit postgres vector dimension rebuild tx: commit acknowledgement lost") {
		t.Fatalf("unexpected vector dimension rebuild commit detail: %v", err)
	}
}
