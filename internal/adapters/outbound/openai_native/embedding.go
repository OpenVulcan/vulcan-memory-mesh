package openai_native

import (
	"context"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

type EmbeddingClient struct {
	client *Client
	model  string
}

func NewEmbeddingClient(endpoint, apiKey, model, organization, project string) *EmbeddingClient {
	return &EmbeddingClient{
		client: NewClient(endpoint, apiKey, organization, project, nil),
		model:  strings.TrimSpace(model),
	}
}

func (c *EmbeddingClient) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if c == nil || c.client == nil {
		return nil, fmt.Errorf("openai client is nil")
	}
	resp, err := c.client.sdk.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(c.model),
		Input: openai.EmbeddingNewParamsInputUnion{
			OfString: openai.String(strings.TrimSpace(text)),
		},
	}, c.client.requestOptions(ctx)...)
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("openai embedding empty response")
	}
	out := make([]float32, 0, len(resp.Data[0].Embedding))
	for _, item := range resp.Data[0].Embedding {
		out = append(out, float32(item))
	}
	return out, nil
}
