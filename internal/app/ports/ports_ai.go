// ports_ai.go keeps AI client aliases and rerank-related ports used by use cases.
// ports_ai.go 用于承载用例层依赖的 AI 客户端别名与重排序相关端口。
package ports

import (
	"context"

	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// EmbeddingRequest carries the normalized embedding call parameters passed from use cases to adapters.
// EmbeddingRequest 用于承载从用例层传给适配器的标准化 embedding 调用参数。
type EmbeddingRequest = logicports.EmbeddingRequest

// EmbeddingResponse returns vectors produced by the configured embedding backend.
// EmbeddingResponse 用于返回当前 embedding 后端生成的向量结果。
type EmbeddingResponse = logicports.EmbeddingResponse

// EmbeddingVectorResult binds one returned vector to the source text index it belongs to inside the caller's effective request batch.
// EmbeddingVectorResult 用于把一条返回向量与其在调用方有效请求批次中的原始文本下标绑定起来。
type EmbeddingVectorResult = logicports.EmbeddingVectorResult

// EmbeddingDroppedInput records one source text that was intentionally dropped after the provider confirmed it was invalid for embedding.
// EmbeddingDroppedInput 用于记录一条在 provider 已确认其不适合做 embedding 后被主动丢弃的源文本。
type EmbeddingDroppedInput = logicports.EmbeddingDroppedInput

// RerankerDocument carries one candidate document sent into an external rerank backend after the first-stage recall finishes.
// RerankerDocument 用于承载首轮召回完成后送入外部重排序后端的一条候选文档。
type RerankerDocument struct {
	ID   string
	Text string
}

// RerankerResult returns one reranked document id together with the provider score produced for the query.
// RerankerResult 用于返回某条重排序文档的 id，以及该 query 下由提供方产生的分数。
type RerankerResult struct {
	ID    string
	Score float64
}

// EmbeddingClient is the port used by use cases to obtain embeddings without depending on a concrete SDK.
// EmbeddingClient 用于让用例层在不依赖具体 SDK 的前提下获取向量表示。
type EmbeddingClient = logicports.EmbeddingClient

// RerankerClient is the port used by use cases to reorder first-stage recall hits with one dedicated rerank model.
// RerankerClient 用于让用例层使用专门的重排序模型对首轮召回结果重新排序。
type RerankerClient interface {
	Rerank(ctx context.Context, query string, docs []RerankerDocument, topN int) ([]RerankerResult, error)
}
