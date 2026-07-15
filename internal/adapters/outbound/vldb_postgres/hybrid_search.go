// hybrid_search.go implements the PostgreSQL first-stage hybrid recall fast path that fuses vector and lexical candidates inside one SQL query for combined mode.
// hybrid_search.go 用于实现 PostgreSQL 组合模式的一阶段混合召回快速路径，让向量与词法候选在单条 SQL 中完成融合。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// SearchHybridMemory executes one dialect-aware SQL fusion query and returns fully materialized memory rows so combined PostgreSQL mode does not reload the same rows by vector id.
// SearchHybridMemory 用于执行一条带方言感知的 SQL 融合查询，并返回完整物化的记忆行，让 PostgreSQL 组合模式无需再按 vector id 重复回表。
func (r *memoryRepository) SearchHybridMemory(ctx context.Context, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) ([]logicdomain.MemoryHit, error) {
	if r == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []logicdomain.MemoryHit{}, nil
	}
	if len(vector) == 0 {
		return []logicdomain.MemoryHit{}, nil
	}
	if len(vector) != r.shared.cfg.EmbeddingDimension {
		return nil, logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
	}
	if topK <= 0 {
		topK = 10
	}
	if rrfK <= 0 {
		rrfK = 60
	}

	sqlText, args := r.shared.dialect.BuildHybridSearchSQL(r, query, vector, topK, filter, rrfK)
	callCtx, cancel := r.queryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres hybrid search tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(callCtx)
	}()

	// Apply the ANN probe count to the current transaction so the vector candidate CTE respects runtime tuning without leaking settings across pooled connections.
	// 在当前事务里应用 ANN probe 数量，确保向量候选 CTE 真正遵循运行时调优参数，同时不把设置泄露到连接池的其他连接。
	probes := r.shared.cfg.VectorProbes
	if probes < 1 {
		probes = 1
	}
	setProbeSQL := fmt.Sprintf("SET LOCAL ivfflat.probes = %d", probes)
	if _, err := tx.Exec(callCtx, setProbeSQL); err != nil {
		return nil, fmt.Errorf("set postgres hybrid search vector probes: %w", err)
	}

	rows, err := tx.Query(callCtx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("search postgres hybrid memory: %w", err)
	}
	defer rows.Close()

	hits := make([]logicdomain.MemoryHit, 0)
	for rows.Next() {
		var (
			row    memoryNodeScanRow
			score  float64
			origin string
		)
		scanTargets := append(memoryNodeScanDestinations(&row), &score, &origin)
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, fmt.Errorf("scan postgres hybrid memory hit: %w", err)
		}
		hits = append(hits, postgresMemoryHitFromRecord(row.toMemoryNodeRecord(), score, origin))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres hybrid memory hits: %w", err)
	}
	// Let the deferred rollback close the read-only transaction because it exists only to scope SET LOCAL probe tuning, not to commit durable state.
	// 让延迟 rollback 关闭只读事务，因为该事务只用于限定 SET LOCAL 探针参数作用域，而不是提交持久状态。
	return hits, nil
}

// SearchHybridMemory delegates to the memory repository so existing port interfaces continue to compile while ownership moves inward.
// SearchHybridMemory 用于委托给 memory repository，让现有接口保持兼容的同时把职责向内迁移。
func (s *Store) SearchHybridMemory(ctx context.Context, query string, vector []float32, topK int, filter logicdomain.SearchFilter, rrfK int) ([]logicdomain.MemoryHit, error) {
	return s.repos.memory.SearchHybridMemory(ctx, query, vector, topK, filter, rrfK)
}
