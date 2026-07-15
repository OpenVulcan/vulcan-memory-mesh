// vector.go implements the shared PostgreSQL vector-search port while keeping write/delete methods as combined-store compatibility shims.
// vector.go 用于实现 PostgreSQL 共享向量检索端口，并把写入/删除方法保留为组合库兼容层 shim。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Upsert validates the incoming vector payload and then intentionally delegates durable ownership to the relational memory mutation path.
// Upsert 用于校验输入向量载荷，然后有意把长期持久化所有权交给关系侧记忆写入路径。
func (r *vectorRepository) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if strings.TrimSpace(record.ID) == "" {
		return logicdomain.ValidationError{Field: "id", Message: "is required"}
	}
	if strings.TrimSpace(record.Text) == "" {
		return logicdomain.ValidationError{Field: "text", Message: "is required"}
	}
	if len(record.Vector) != r.shared.cfg.EmbeddingDimension {
		return logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", r.shared.cfg.EmbeddingDimension)}
	}
	return nil
}

// Search executes one pgvector semantic recall directly against the unified durable memory table and returns materialized memory rows with each hit to avoid an immediate app-layer reload.
// Search 用于直接在统一长期记忆表上执行一次 pgvector 语义召回，并让每条命中携带已物化记忆行，避免应用层立刻再次回表。
func (r *vectorRepository) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
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

	args := &sqlArgsBuilder{}
	vectorPlaceholder := args.Add(encodePGVectorLiteral(vector))
	limitPlaceholder := args.Add(topK)
	whereClauses := []string{activeUnexpiredMemoryCondition("m")}
	appendScopedMemoryFilter(&whereClauses, args, filter, "m")
	sqlText := fmt.Sprintf(`
SELECT %s,
       (m.embedding <=> %s::vector) AS distance
FROM %s AS m
WHERE %s
ORDER BY m.embedding <=> %s::vector ASC, m.id ASC
LIMIT %s
`, memoryNodeSelectColumns("m"), vectorPlaceholder, r.memoryNodesTable(), strings.Join(whereClauses, " AND "), vectorPlaceholder, limitPlaceholder)

	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres vector search tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(callCtx)
	}()

	// Apply the ANN probe setting with SET LOCAL inside the same transaction so the recall query observes the operator-configured runtime knob without polluting pooled connections.
	// 在同一事务里通过 SET LOCAL 应用 ANN probe 设置，确保召回查询真正看到运维配置的运行时旋钮，同时不污染连接池中的其他连接。
	probes := r.shared.cfg.VectorProbes
	if probes < 1 {
		probes = 1
	}
	setProbeSQL := fmt.Sprintf("SET LOCAL ivfflat.probes = %d", probes)
	if _, err := tx.Exec(callCtx, setProbeSQL); err != nil {
		return nil, fmt.Errorf("set postgres vector probes: %w", err)
	}

	rows, err := tx.Query(callCtx, strings.TrimSpace(sqlText), args.Args()...)
	if err != nil {
		return nil, fmt.Errorf("search postgres memory vectors: %w", err)
	}
	defer rows.Close()

	hits := make([]logicdomain.MemoryHit, 0)
	for rows.Next() {
		var (
			row      memoryNodeScanRow
			distance float64
		)
		scanTargets := append(memoryNodeScanDestinations(&row), &distance)
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, fmt.Errorf("scan postgres vector hit: %w", err)
		}
		// Plain pgvector recall is a single-channel vector path, so stamp its origin at the adapter boundary before the app layer maps durable memory rows.
		// 普通 pgvector 召回是单通道向量路径，因此在适配器边界写入来源，再交给应用层映射长期记忆行。
		hits = append(hits, postgresMemoryHitFromRecord(row.toMemoryNodeRecord(), distanceToScore(distance), "vector_search"))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres vector hits: %w", err)
	}
	// Let the deferred rollback close the read-only transaction because it exists only to scope SET LOCAL probe tuning, not to commit durable state.
	// 让延迟 rollback 关闭只读事务，因为该事务只用于限定 SET LOCAL 探针参数作用域，而不是提交持久状态。
	return hits, nil
}

// DeleteByFilter is a combined-store no-op because durable memory ownership lives in relational mutation flows instead of a detached vector sidecar.
// DeleteByFilter 用于在组合库中保持 no-op，因为长期记忆所有权归属于关系侧变更流，而不是独立向量旁路。
func (r *vectorRepository) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	return 0, nil
}

// DeleteByIDs is a combined-store no-op because supersede/rollback semantics are handled by the owning relational workflow.
// DeleteByIDs 用于在组合库中保持 no-op，因为 supersede 与回滚语义由拥有该记忆的关系流负责处理。
func (r *vectorRepository) DeleteByIDs(context.Context, []string) (uint64, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	return 0, nil
}

// distanceToScore converts a pgvector distance into the higher-is-better score contract expected by existing use cases.
// distanceToScore 用于把 pgvector 距离转换成现有用例期望的"越高越好"分数契约。
func distanceToScore(distance float64) float64 {
	if distance <= 0 {
		return 1
	}
	return 1 / (1 + distance)
}

// postgresMemoryHitFromRecord converts one already-scanned unified memory row into the shared recall hit shape while preserving the materialized row for app-layer mapping.
// postgresMemoryHitFromRecord 用于把已扫描的统一记忆行转换成共享召回命中结构，同时保留已物化行供应用层映射使用。
func postgresMemoryHitFromRecord(record logicdomain.MemoryNodeRecord, score float64, origin string) logicdomain.MemoryHit {
	metadata := map[string]string{
		"turn_id": fmt.Sprintf("%d", record.SourceTurnID),
	}
	if origin = strings.TrimSpace(origin); origin != "" {
		metadata["origin"] = origin
	}
	return logicdomain.MemoryHit{
		ID:        strings.TrimSpace(record.VectorID),
		Text:      strings.TrimSpace(record.Abstract),
		Score:     score,
		Record:    record,
		CreatedAt: record.CreatedAt,
		Filter: logicdomain.SearchFilter{
			TeamID:    record.TeamID,
			SpaceID:   record.SpaceID,
			ProjectID: record.ProjectID,
			SessionID: record.OriginSessionID,
			UserID:    record.UserID,
		},
		Metadata: metadata,
	}
}

// DeleteByFilter delegates to the vector repository.
// DeleteByFilter 用于把按过滤器删除委托给 vector 仓储。
func (s *Store) DeleteByFilter(ctx context.Context, filter logicdomain.SearchFilter) (uint64, error) {
	return s.repos.vector.DeleteByFilter(ctx, filter)
}

// DeleteByIDs delegates to the vector repository.
// DeleteByIDs 用于把按 ID 删除委托给 vector 仓储。
func (s *Store) DeleteByIDs(ctx context.Context, ids []string) (uint64, error) {
	return s.repos.vector.DeleteByIDs(ctx, ids)
}

// Search delegates to the vector repository.
// Search 用于把向量搜索委托给 vector 仓储。
func (s *Store) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	return s.repos.vector.Search(ctx, vector, topK, filter)
}

// Upsert delegates to the vector repository.
// Upsert 用于把向量插入委托给 vector 仓储。
func (s *Store) Upsert(ctx context.Context, record logicdomain.MemoryRecord) error {
	return s.repos.vector.Upsert(ctx, record)
}
