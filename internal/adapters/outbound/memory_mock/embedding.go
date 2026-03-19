package memory_mock

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/platform/textutil"
)

type EmbeddingClient struct {
	Dim        int
	Delay      time.Duration
	ForceError error
}

func NewEmbeddingClient(dim int) *EmbeddingClient {
	if dim <= 0 { dim = 64 }
	return &EmbeddingClient{Dim: dim}
}

func (c *EmbeddingClient) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if c.Delay > 0 {
		timer := time.NewTimer(c.Delay)
		defer timer.Stop()
		select { case <-ctx.Done(): return nil, ctx.Err(); case <-timer.C: }
	}
	if c.ForceError != nil { return nil, c.ForceError }
	vec := make([]float32, c.Dim)
	tokens := textutil.Tokenize(strings.TrimSpace(text))
	if len(tokens) == 0 { return vec, nil }
	for _, tok := range tokens {
		h := fnv.New64a()
		_, _ = h.Write([]byte(tok))
		idx := int(h.Sum64() % uint64(c.Dim))
		vec[idx] += 1
	}
	normalize(vec)
	return vec, nil
}

func normalize(v []float32) {
	var sum float64
	for _, item := range v { sum += float64(item * item) }
	if sum == 0 { return }
	norm := float32(math.Sqrt(sum))
	for i := range v { v[i] /= norm }
}

func cosine(a, b []float32) (float64, error) {
	if len(a) != len(b) { return 0, errors.New("vector dimensions mismatch") }
	var dot float64
	for i := range a { dot += float64(a[i] * b[i]) }
	return dot, nil
}
