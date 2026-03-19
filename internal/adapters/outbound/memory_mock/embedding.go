// embedding.go implements the in-memory mock outbound adapters.
// embedding.go 用于实现内存版 mock 出站适配器。
package memory_mock

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

// EmbeddingClient is the in-memory embedding adapter used by tests and the zero-dependency local runtime.
// EmbeddingClient 用于作为测试和零依赖本地运行时使用的内存 embedding 适配器。
type EmbeddingClient struct {
	Dim        int
	Delay      time.Duration
	ForceError error
}

// NewEmbeddingClient creates a EmbeddingClient instance.
// NewEmbeddingClient 用于创建 EmbeddingClient 实例。
func NewEmbeddingClient(dim int) *EmbeddingClient {
	if dim <= 0 {
		dim = 64
	}
	return &EmbeddingClient{Dim: dim}
}

// Embed executes the Embed logic.
// Embed 用于执行 Embed 逻辑。
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	if c.Delay > 0 {
		timer := time.NewTimer(c.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return appports.EmbeddingResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	if c.ForceError != nil {
		return appports.EmbeddingResponse{}, c.ForceError
	}
	dim := c.Dim
	if req.Dimension > 0 {
		dim = req.Dimension
	}
	vectors := make([][]float32, 0, len(req.Texts))
	for _, text := range req.Texts {
		vec := make([]float32, dim)
		tokens := textutil.Tokenize(strings.TrimSpace(text))
		if len(tokens) == 0 {
			vectors = append(vectors, vec)
			continue
		}
		for _, tok := range tokens {
			h := fnv.New64a()
			_, _ = h.Write([]byte(tok))
			idx := int(h.Sum64() % uint64(dim))
			vec[idx] += 1
		}
		normalize(vec)
		vectors = append(vectors, vec)
	}
	return appports.EmbeddingResponse{Vectors: vectors}, nil
}

// normalize executes the normalize logic.
// normalize 用于执行 normalize 逻辑。
func normalize(v []float32) {
	var sum float64
	for _, item := range v {
		sum += float64(item * item)
	}
	if sum == 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= norm
	}
}

// cosine executes the cosine logic.
// cosine 用于执行 cosine 逻辑。
func cosine(a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, errors.New("vector dimensions mismatch")
	}
	var dot float64
	for i := range a {
		dot += float64(a[i] * b[i])
	}
	return dot, nil
}
