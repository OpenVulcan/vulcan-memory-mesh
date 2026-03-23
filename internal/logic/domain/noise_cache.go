// noise_cache.go declares the cache models used to persist precomputed semantic prototypes for the noise gate.
// noise_cache.go 用于声明噪声门控器持久化预计算语义原型时使用的缓存模型。
package domain

import "time"

// NoiseEmbeddingCacheQuery identifies one semantic prototype bundle by scope, language, model, dimension, and rule fingerprint.
// NoiseEmbeddingCacheQuery 用于通过作用域、语言、模型、维度和规则指纹唯一标识一组语义原型缓存。
type NoiseEmbeddingCacheQuery struct {
	Scope     string
	Language  string
	Model     string
	Dimension int
	RulesHash string
}

// NoiseEmbeddingCacheEntry stores one precomputed vector for a single phrase inside one noise category.
// NoiseEmbeddingCacheEntry 用于保存某个噪声类别里单个短语对应的一条预计算向量缓存记录。
type NoiseEmbeddingCacheEntry struct {
	Scope        string
	Language     string
	CategoryName string
	Phrase       string
	Model        string
	Dimension    int
	RulesHash    string
	Vector       []float32
	UpdatedAt    time.Time
}
