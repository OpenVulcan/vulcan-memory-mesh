// noise_cache.go implements the semantic prototype cache used by the noise gate so combined PostgreSQL mode can preload and refresh embeddings.
// noise_cache.go 用于实现噪声门控的语义原型缓存，让组合 PostgreSQL 模式能够预加载和刷新 embedding。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// loadNoiseEmbeddingCache returns one persisted semantic prototype bundle keyed by scope, language, model, dimension, and rules hash.
// loadNoiseEmbeddingCache 用于返回按作用域、语言、模型、维度和规则哈希定位的一组持久化语义原型缓存。
func (r *vectorRepository) loadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, embedding::text AS embedding_text, updated_at
FROM %s
WHERE scope = $1 AND language = $2 AND model = $3 AND dimension = $4 AND rules_hash = $5
ORDER BY category_name ASC, phrase ASC
`, r.noiseEmbeddingsTable())
	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	rows, err := r.shared.pool.Query(callCtx, strings.TrimSpace(sqlText),
		strings.TrimSpace(query.Scope),
		strings.TrimSpace(query.Language),
		strings.TrimSpace(query.Model),
		query.Dimension,
		strings.TrimSpace(query.RulesHash),
	)
	if err != nil {
		return nil, fmt.Errorf("load postgres noise embedding cache: %w", err)
	}
	defer rows.Close()

	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0)
	for rows.Next() {
		var (
			entry         logicdomain.NoiseEmbeddingCacheEntry
			embeddingText string
		)
		if err := rows.Scan(
			&entry.Scope,
			&entry.Language,
			&entry.CategoryName,
			&entry.Phrase,
			&entry.Model,
			&entry.Dimension,
			&entry.RulesHash,
			&embeddingText,
			&entry.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan postgres noise embedding cache row: %w", err)
		}
		entry.Vector = parsePGVectorText(embeddingText)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres noise embedding cache rows: %w", err)
	}
	return entries, nil
}

// replaceNoiseEmbeddingCache replaces one fingerprinted cache bundle atomically so startup semantic preloads never observe half-written prototype rows.
// replaceNoiseEmbeddingCache 用于原子替换某个指纹对应的缓存集合，避免启动期语义预加载看到半写入的原型行。
func (r *vectorRepository) replaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	callCtx, cancel := r.vectorQueryContext(ctx)
	defer cancel()
	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres noise cache replace tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(callCtx)
	}()

	deleteSQL := fmt.Sprintf(`
DELETE FROM %s
WHERE scope = $1 AND language = $2 AND model = $3 AND dimension = $4 AND rules_hash = $5
`, r.noiseEmbeddingsTable())
	if _, err := tx.Exec(callCtx, strings.TrimSpace(deleteSQL),
		strings.TrimSpace(query.Scope),
		strings.TrimSpace(query.Language),
		strings.TrimSpace(query.Model),
		query.Dimension,
		strings.TrimSpace(query.RulesHash),
	); err != nil {
		return fmt.Errorf("clear postgres noise embedding cache: %w", err)
	}
	if len(entries) > 0 {
		insertSQL := fmt.Sprintf(`
INSERT INTO %s (
	scope, language, category_name, phrase, model, dimension, rules_hash, embedding, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector, $9)
`, r.noiseEmbeddingsTable())
		for _, entry := range entries {
			if _, err := tx.Exec(callCtx, strings.TrimSpace(insertSQL),
				strings.TrimSpace(entry.Scope),
				strings.TrimSpace(entry.Language),
				strings.TrimSpace(entry.CategoryName),
				strings.TrimSpace(entry.Phrase),
				strings.TrimSpace(entry.Model),
				entry.Dimension,
				strings.TrimSpace(entry.RulesHash),
				encodePGVectorLiteral(entry.Vector),
				entry.UpdatedAt.UTC(),
			); err != nil {
				return fmt.Errorf("insert postgres noise embedding cache row: %w", err)
			}
		}
	}
	if err := tx.Commit(callCtx); err != nil {
		return postgresNoiseCacheCommitOutcomeUncertainError(err)
	}
	return nil
}

// LoadNoiseEmbeddingCache delegates cache reads to the vector repository that owns the pgvector-backed cache table.
// LoadNoiseEmbeddingCache 用于把缓存读取委托给拥有 pgvector 缓存表的向量仓储。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	if s == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	return s.repos.vector.loadNoiseEmbeddingCache(ctx, query)
}

// ReplaceNoiseEmbeddingCache delegates cache replacement to the vector repository so writes use the same shared runtime state as vector search.
// ReplaceNoiseEmbeddingCache 用于把缓存替换委托给向量仓储，使写入与向量检索共用同一份运行时状态。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	if s == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	return s.repos.vector.replaceNoiseEmbeddingCache(ctx, query, entries)
}

// postgresNoiseCacheCommitOutcomeUncertainError marks cache-replace commit failures after PostgreSQL has accepted the transactional mutation set.
// postgresNoiseCacheCommitOutcomeUncertainError 用于标记 PostgreSQL 已接收缓存替换事务变更后发生的提交失败。
func postgresNoiseCacheCommitOutcomeUncertainError(err error) error {
	return postgresCommitOutcomeUncertainError("replace postgres noise embedding cache", "commit postgres noise embedding cache replace", err)
}
