// SQLite semantic noise cache persistence used by NoiseGate initialization.
// SQLite 适配层为 NoiseGate 初始化提供语义噪声缓存持久化。
package vldb_sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// LoadNoiseEmbeddingCache returns one persisted semantic prototype bundle keyed by scope, language, model, dimension, and rules hash.
// LoadNoiseEmbeddingCache 用于返回按作用域、语言、模型、维度和规则哈希定位的一组语义原型缓存。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	rows, err := queryRows[noiseEmbeddingRow](s, ctx, `
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
ORDER BY category_name ASC, phrase ASC
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash))
	if err != nil {
		return nil, fmt.Errorf("load noise embedding cache: %w", err)
	}
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(rows))
	for _, row := range rows {
		vector, err := decodeFloat32Slice(row.VectorJSON)
		if err != nil {
			return nil, fmt.Errorf("noise embedding %q/%q vector_json: %w", row.CategoryName, row.Phrase, err)
		}
		updatedAt, _ := time.Parse(time.RFC3339Nano, row.UpdatedAt)
		entries = append(entries, logicdomain.NoiseEmbeddingCacheEntry{
			Scope:        row.Scope,
			Language:     row.Language,
			CategoryName: row.CategoryName,
			Phrase:       row.Phrase,
			Model:        row.Model,
			Dimension:    row.Dimension,
			RulesHash:    row.RulesHash,
			Vector:       vector,
			UpdatedAt:    updatedAt,
		})
	}
	return entries, nil
}

// ReplaceNoiseEmbeddingCache rewrites one semantic prototype bundle by deleting old rows and batch-inserting the refreshed set; callers treat failures as cache-refresh degradation.
// ReplaceNoiseEmbeddingCache 用于通过先删旧行再批量插入新行来重写一组语义原型缓存；调用方会把失败视为缓存刷新降级。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.exec(ctx, `
DELETE FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
`, strings.TrimSpace(query.Scope), strings.TrimSpace(query.Language), strings.TrimSpace(query.Model), query.Dimension, strings.TrimSpace(query.RulesHash)); err != nil {
		return sqliteNoiseEmbeddingCacheReplaceError("clear noise embedding cache", err)
	}
	if len(entries) == 0 {
		return nil
	}
	batchItems := make([][]any, 0, len(entries))
	for _, entry := range entries {
		vectorJSON, err := json.Marshal(entry.Vector)
		if err != nil {
			return fmt.Errorf("encode noise embedding cache vector: %w", err)
		}
		batchItems = append(batchItems, []any{
			strings.TrimSpace(entry.Scope),
			strings.TrimSpace(entry.Language),
			strings.TrimSpace(entry.CategoryName),
			strings.TrimSpace(entry.Phrase),
			strings.TrimSpace(entry.Model),
			entry.Dimension,
			strings.TrimSpace(entry.RulesHash),
			string(vectorJSON),
			entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	if err := s.execBatch(ctx, `
INSERT INTO vmm_noise_embeddings (
  scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, batchItems); err != nil {
		return sqliteNoiseEmbeddingCacheReplaceError("insert noise embedding cache rows", err)
	}
	return nil
}

// sqliteNoiseEmbeddingCacheReplaceError keeps cache refresh failures non-fatal to callers while preserving commit-boundary uncertainty in the storage error type.
// sqliteNoiseEmbeddingCacheReplaceError 用于让调用方继续把缓存刷新失败视为非致命降级，同时在存储错误类型中保留提交边界不确定语义。
func sqliteNoiseEmbeddingCacheReplaceError(stage string, err error) error {
	if err == nil {
		return nil
	}
	stage = strings.TrimSpace(stage)
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: "replace sqlite noise embedding cache",
			Message:   fmt.Sprintf("%s: %v", stage, err),
		}
	}
	return fmt.Errorf("%s: %w", stage, err)
}
