package openai_native

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

type EmbeddingClient struct {
	client    *Client
	model     string
	dimension int
}

func NewEmbeddingClient(endpoint, apiKey, model string, dimension int, organization, project string) *EmbeddingClient {
	return &EmbeddingClient{client: NewClient(endpoint, apiKey, organization, project, nil), model: strings.TrimSpace(model), dimension: dimension}
}
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	if c == nil || c.client == nil || c.client.sdkClient == nil {
		return appports.EmbeddingResponse{}, fmt.Errorf("openai native client is nil")
	}
	texts := make([]string, 0, len(req.Texts))
	for _, text := range req.Texts {
		if text = strings.TrimSpace(text); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return appports.EmbeddingResponse{Vectors: [][]float32{}}, nil
	}
	if len(texts) > 10 {
		return appports.EmbeddingResponse{}, fmt.Errorf("exceeds max batch size 10")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.EmbeddingResponse{}, fmt.Errorf("openai native embedding model is required")
	}
	dimension := req.Dimension
	if dimension <= 0 {
		dimension = c.dimension
	}
	params := openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(model),
		Input: mapEmbeddingInput(texts),
	}
	if dimension > 0 {
		params.Dimensions = openai.Int(int64(dimension))
	}
	applyEmbeddingHints(&params, req.ProviderHints)
	resp, err := c.client.sdkClient.Embeddings.New(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return appports.EmbeddingResponse{}, err
	}
	if resp == nil || len(resp.Data) == 0 {
		return appports.EmbeddingResponse{}, fmt.Errorf("openai native embedding empty response")
	}
	vectors := make([][]float32, 0, len(resp.Data))
	for _, item := range resp.Data {
		vector := make([]float32, 0, len(item.Embedding))
		for _, dim := range item.Embedding {
			vector = append(vector, float32(dim))
		}
		vectors = append(vectors, vector)
	}
	return appports.EmbeddingResponse{Vectors: vectors}, nil
}

func mapEmbeddingInput(texts []string) openai.EmbeddingNewParamsInputUnion {
	if len(texts) == 1 {
		return openai.EmbeddingNewParamsInputUnion{OfString: openai.String(texts[0])}
	}
	return openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts}
}

func applyEmbeddingHints(params *openai.EmbeddingNewParams, hints map[string]any) {
	if params == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "user":
			if value, ok := stringHint(rawValue); ok {
				params.User = openai.String(value)
			}
		case "encoding_format":
			if value, ok := stringHint(rawValue); ok {
				params.EncodingFormat = openai.EmbeddingNewParamsEncodingFormat(value)
			}
		case "dimensions":
			if value, ok := intHint(rawValue); ok && value > 0 {
				params.Dimensions = openai.Int(value)
			}
		}
	}
}
