// noise_cache.go implements the semantic prototype cache used by the noise gate so combined PostgreSQL mode can preload and refresh embeddings.
// noise_cache.go 用于实现噪声门控的语义原型缓存，让组合 PostgreSQL 模式能够预加载和刷新 embedding。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LoadNoiseEmbeddingCache returns one persisted semantic prototype bundle keyed by scope, language, model, dimension, and rules hash.
// LoadNoiseEmbeddingCache 用于返回按作用域、语言、模型、维度和规则哈希定位的一组持久化语义原型缓存。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("postgres store is not initialized")
	}
	sqlText := fmt.Sprintf(`
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, embedding::text AS embedding_text, updated_at
FROM %s
WHERE scope = $1 AND language = $2 AND model = $3 AND dimension = $4 AND rules_hash = $5
ORDER BY category_name ASC, phrase ASC
`, s.noiseEmbeddingsTable())
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.pool.Query(callCtx, strings.TrimSpace(sqlText),
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

// ReplaceNoiseEmbeddingCache replaces one fingerprinted cache bundle atomically so startup semantic preloads never observe half-written prototype rows.
// ReplaceNoiseEmbeddingCache 用于原子替换某个指纹对应的缓存集合，避免启动期语义预加载看到半写入的原型行。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("postgres store is not initialized")
	}
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return fmt.Errorf("begin postgres noise cache replace tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(callCtx)
	}()

	deleteSQL := fmt.Sprintf(`
DELETE FROM %s
WHERE scope = $1 AND language = $2 AND model = $3 AND dimension = $4 AND rules_hash = $5
`, s.noiseEmbeddingsTable())
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
`, s.noiseEmbeddingsTable())
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
		return fmt.Errorf("commit postgres noise embedding cache replace: %w", err)
	}
	return nil
}
