// embedding.go declares the embedding and cache contracts owned by the logic layer.
// embedding.go 用于声明由 logic 层拥有的 embedding 与缓存契约。
package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// EmbeddingRequest carries the normalized embedding call parameters passed from logic to adapters.
// EmbeddingRequest 用于承载从 logic 层传给适配器的标准化 embedding 调用参数。
type EmbeddingRequest struct {
	Model         string
	Texts         []string
	Dimension     int
	ProviderHints map[string]any
}

// EmbeddingResponse returns vectors produced by the configured embedding backend.
// EmbeddingResponse 用于返回当前 embedding 后端生成的向量结果。
type EmbeddingResponse struct {
	Vectors [][]float32
}

// EmbeddingClient is the port used by logic processors to obtain embeddings without depending on a concrete SDK.
// EmbeddingClient 用于让 logic 处理器在不依赖具体 SDK 的前提下获取向量表示。
type EmbeddingClient interface {
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// NoiseEmbeddingCache is the port used by startup processors to reuse previously computed semantic prototype vectors.
// NoiseEmbeddingCache 用于让启动期处理器复用已计算好的语义原型向量缓存。
type NoiseEmbeddingCache interface {
	LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error)
	ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error
}
