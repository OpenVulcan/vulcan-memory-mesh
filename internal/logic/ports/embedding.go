// embedding.go declares the embedding and cache contracts owned by the logic layer.
// embedding.go 用于声明由 logic 层拥有的 embedding 与缓存契约。
package ports

import (
	"context"
	"fmt"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// EmbeddingRequest carries the normalized embedding call parameters passed from logic to adapters.
// EmbeddingRequest 用于承载从 logic 层传给适配器的标准化 embedding 调用参数。
type EmbeddingRequest struct {
	Model string
	Texts []string
	// AllowPartialInvalidTexts lets wrappers drop only provider-confirmed item-scoped invalid inputs instead of failing the whole logical batch.
	// AllowPartialInvalidTexts 用于允许包装层在 provider 已明确确认“单条输入本身非法”时丢弃该条，而不是让整个逻辑批次失败。
	AllowPartialInvalidTexts bool
	Dimension                int
	ProviderHints            map[string]any
}

// EmbeddingVectorResult binds one returned vector to the source text index it belongs to inside the caller's effective request batch.
// EmbeddingVectorResult 用于把一条返回向量与其在调用方有效请求批次中的原始文本下标绑定起来。
type EmbeddingVectorResult struct {
	Index  int
	Vector []float32
}

// EmbeddingDroppedInput records one source text that was intentionally dropped after the provider confirmed it was invalid for embedding.
// EmbeddingDroppedInput 用于记录一条在 provider 已确认其不适合做 embedding 后被主动丢弃的源文本。
type EmbeddingDroppedInput struct {
	Index  int
	Text   string
	Reason string
}

// EmbeddingResponse returns vectors produced by the configured embedding backend.
// EmbeddingResponse 用于返回当前 embedding 后端生成的向量结果。
type EmbeddingResponse struct {
	Vectors       [][]float32
	ResultIndices []int
	Dropped       []EmbeddingDroppedInput
}

// IndexedVectors resolves the returned vectors back onto source indexes so callers can safely consume partial-success embedding responses.
// IndexedVectors 用于把返回向量重新绑定回源文本下标，让调用方可以安全消费“部分成功”的 embedding 结果。
func (r EmbeddingResponse) IndexedVectors(inputCount int) ([]EmbeddingVectorResult, error) {
	indices, err := r.normalizedResultIndices(inputCount)
	if err != nil {
		return nil, err
	}
	items := make([]EmbeddingVectorResult, 0, len(r.Vectors))
	for idx, vector := range r.Vectors {
		items = append(items, EmbeddingVectorResult{
			Index:  indices[idx],
			Vector: append([]float32(nil), vector...),
		})
	}
	return items, nil
}

// ValidateStrict ensures the response still satisfies the historical one-input-one-vector contract expected by strict callers.
// ValidateStrict 用于确保响应仍满足严格调用方所依赖的“每条输入对应一条向量”历史契约。
func (r EmbeddingResponse) ValidateStrict(inputCount int) error {
	items, err := r.IndexedVectors(inputCount)
	if err != nil {
		return err
	}
	if len(r.Dropped) > 0 {
		return fmt.Errorf("embedding response dropped %d input(s)", len(r.Dropped))
	}
	if len(items) != inputCount {
		return fmt.Errorf("embedding result count mismatch: got %d want %d", len(items), inputCount)
	}
	for idx, item := range items {
		if item.Index != idx {
			return fmt.Errorf("embedding result index mismatch: got %d want %d", item.Index, idx)
		}
	}
	return nil
}

// normalizedResultIndices validates or reconstructs the source-index mapping for the returned vectors.
// normalizedResultIndices 用于校验或重建返回向量的源下标映射。
func (r EmbeddingResponse) normalizedResultIndices(inputCount int) ([]int, error) {
	if len(r.ResultIndices) == 0 {
		if len(r.Dropped) > 0 {
			if len(r.Vectors) > 0 {
				return nil, fmt.Errorf("embedding response missing result indices for partial result")
			}
			seenDropped := make(map[int]struct{}, len(r.Dropped))
			for _, dropped := range r.Dropped {
				if dropped.Index < 0 || dropped.Index >= inputCount {
					return nil, fmt.Errorf("embedding dropped index %d out of range", dropped.Index)
				}
				if _, exists := seenDropped[dropped.Index]; exists {
					return nil, fmt.Errorf("embedding dropped index %d repeated", dropped.Index)
				}
				seenDropped[dropped.Index] = struct{}{}
			}
			if len(seenDropped) != inputCount {
				return nil, fmt.Errorf("embedding dropped count mismatch: got %d want %d", len(seenDropped), inputCount)
			}
			return []int{}, nil
		}
		if len(r.Vectors) != inputCount {
			return nil, fmt.Errorf("embedding result count mismatch: got %d want %d", len(r.Vectors), inputCount)
		}
		indices := make([]int, 0, len(r.Vectors))
		for idx := range r.Vectors {
			indices = append(indices, idx)
		}
		return indices, nil
	}
	if len(r.ResultIndices) != len(r.Vectors) {
		return nil, fmt.Errorf("embedding result index count mismatch: got %d want %d", len(r.ResultIndices), len(r.Vectors))
	}
	seen := make(map[int]struct{}, len(r.ResultIndices)+len(r.Dropped))
	indices := make([]int, 0, len(r.ResultIndices))
	for _, index := range r.ResultIndices {
		if index < 0 || index >= inputCount {
			return nil, fmt.Errorf("embedding result index %d out of range", index)
		}
		if _, exists := seen[index]; exists {
			return nil, fmt.Errorf("embedding result index %d repeated", index)
		}
		seen[index] = struct{}{}
		indices = append(indices, index)
	}
	for _, dropped := range r.Dropped {
		if dropped.Index < 0 || dropped.Index >= inputCount {
			return nil, fmt.Errorf("embedding dropped index %d out of range", dropped.Index)
		}
		if _, exists := seen[dropped.Index]; exists {
			return nil, fmt.Errorf("embedding dropped index %d repeated", dropped.Index)
		}
		seen[dropped.Index] = struct{}{}
	}
	return indices, nil
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
