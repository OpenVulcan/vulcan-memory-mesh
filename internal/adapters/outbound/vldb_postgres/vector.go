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
func (s *Store) Upsert(_ context.Context, record logicdomain.MemoryRecord) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	if strings.TrimSpace(record.ID) == "" {
		return logicdomain.ValidationError{Field: "id", Message: "is required"}
	}
	if strings.TrimSpace(record.Text) == "" {
		return logicdomain.ValidationError{Field: "text", Message: "is required"}
	}
	if len(record.Vector) != s.cfg.EmbeddingDimension {
		return logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", s.cfg.EmbeddingDimension)}
	}
	return nil
}

// Search executes one pgvector semantic recall directly against the unified durable memory table and maps the result into the shared MemoryHit shape.
// Search 用于直接在统一长期记忆表上执行一次 pgvector 语义召回，并把结果映射为共享 MemoryHit 结构。
func (s *Store) Search(ctx context.Context, vector []float32, topK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	if len(vector) == 0 {
		return []logicdomain.MemoryHit{}, nil
	}
	if len(vector) != s.cfg.EmbeddingDimension {
		return nil, logicdomain.ValidationError{Field: "vector", Message: fmt.Sprintf("must contain exactly %d dimensions", s.cfg.EmbeddingDimension)}
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
SELECT m.vector_id,
       m.abstract,
       m.team_id,
       m.space_id,
       m.project_id,
       m.origin_session_id,
       m.user_id,
       COALESCE(m.source_turn_id, 0) AS source_turn_id,
       (m.embedding <=> %s::vector) AS distance
FROM %s AS m
WHERE %s
ORDER BY m.embedding <=> %s::vector ASC, m.id ASC
LIMIT %s
`, vectorPlaceholder, s.memoryNodesTable(), strings.Join(whereClauses, " AND "), vectorPlaceholder, limitPlaceholder)

	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres vector search tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	// Apply the ANN probe setting with SET LOCAL inside the same transaction so the recall query observes the operator-configured runtime knob without polluting pooled connections.
	// 在同一事务里通过 SET LOCAL 应用 ANN probe 设置，确保召回查询真正看到运维配置的运行时旋钮，同时不污染连接池中的其他连接。
	probes := s.cfg.VectorProbes
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
			vectorID      string
			abstract      string
			teamID        uint64
			spaceID       uint64
			projectID     uint64
			originSession uint64
			userID        uint64
			sourceTurnID  uint64
			distance      float64
		)
		if err := rows.Scan(&vectorID, &abstract, &teamID, &spaceID, &projectID, &originSession, &userID, &sourceTurnID, &distance); err != nil {
			return nil, fmt.Errorf("scan postgres vector hit: %w", err)
		}
		hits = append(hits, logicdomain.MemoryHit{
			ID:    strings.TrimSpace(vectorID),
			Text:  strings.TrimSpace(abstract),
			Score: distanceToScore(distance),
			Filter: logicdomain.SearchFilter{
				TeamID:    teamID,
				SpaceID:   spaceID,
				ProjectID: projectID,
				SessionID: originSession,
				UserID:    userID,
			},
			Metadata: map[string]string{
				"turn_id": fmt.Sprintf("%d", sourceTurnID),
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres vector hits: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return nil, fmt.Errorf("commit postgres vector search tx: %w", err)
	}
	return hits, nil
}

// DeleteByFilter is a combined-store no-op because durable memory ownership lives in relational mutation flows instead of a detached vector sidecar.
// DeleteByFilter 用于在组合库中保持 no-op，因为长期记忆所有权归属于关系侧变更流，而不是独立向量旁路。
func (s *Store) DeleteByFilter(context.Context, logicdomain.SearchFilter) (uint64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	return 0, nil
}

// DeleteByIDs is a combined-store no-op because supersede/rollback semantics are handled by the owning relational workflow.
// DeleteByIDs 用于在组合库中保持 no-op，因为 supersede 与回滚语义由拥有该记忆的关系流负责处理。
func (s *Store) DeleteByIDs(context.Context, []string) (uint64, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("postgres store is not initialized")
	}
	return 0, nil
}

// distanceToScore converts a pgvector distance into the higher-is-better score contract expected by existing use cases.
// distanceToScore 用于把 pgvector 距离转换成现有用例期望的“越高越好”分数契约。
func distanceToScore(distance float64) float64 {
	if distance <= 0 {
		return 1
	}
	return 1 / (1 + distance)
}
