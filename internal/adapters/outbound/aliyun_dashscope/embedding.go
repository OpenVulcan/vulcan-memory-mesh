package aliyun_dashscope

import (
	"context"
	"fmt"
)

type EmbeddingClient struct {
	client   *Client
	endpoint string
	model    string
}

func NewEmbeddingClient(endpoint, apiKey, model string) *EmbeddingClient {
	return &EmbeddingClient{client: NewClient(endpoint, apiKey, nil), endpoint: endpoint, model: model}
}

func (c *EmbeddingClient) EmbedText(ctx context.Context, text string) ([]float32, error) {
	req := map[string]any{
		"model": c.model,
		"input": map[string]any{"texts": []string{text}},
		"parameters": map[string]any{"text_type": "query"},
	}
	var resp struct {
		Output struct {
			Embeddings []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"embeddings"`
		} `json:"output"`
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.client.PostJSON(ctx, c.endpoint, req, &resp); err != nil { return nil, err }
	if len(resp.Output.Embeddings) > 0 { return resp.Output.Embeddings[0].Embedding, nil }
	if len(resp.Data) > 0 { return resp.Data[0].Embedding, nil }
	return nil, fmt.Errorf("dashscope embedding empty response")
}
