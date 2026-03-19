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

type EmbeddingClient struct {
	Dim        int
	Delay      time.Duration
	ForceError error
}

func NewEmbeddingClient(dim int) *EmbeddingClient {
	if dim <= 0 {
		dim = 64
	}
	return &EmbeddingClient{Dim: dim}
}

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
