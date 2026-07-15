// embedding.go implements the OpenRouter SDK-backed embedding adapter.
// embedding.go 用于实现基于 OpenRouter SDK 的 embedding 出站适配器。
package openrouter

import (
	"context"
	"fmt"
	"strings"

	"github.com/OpenRouterTeam/go-sdk/models/operations"
	"github.com/openvulcan/vmm/internal/adapters/outbound/providerhint"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// EmbeddingClient adapts the internal embedding port onto OpenRouter's typed embeddings SDK.
// EmbeddingClient 用于把内部 embedding 端口适配到 OpenRouter 的强类型 embeddings SDK。
type EmbeddingClient struct {
	client      *Client
	model       string
	dimension   int
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewEmbeddingClient creates one fixed-route OpenRouter embedding client with route-level and model-level default parameters.
// NewEmbeddingClient 用于创建一个固定路由的 OpenRouter embedding 客户端，并携带路由级与模型级默认参数。
func NewEmbeddingClient(endpoint, apiKey, model string, dimension int, params map[string]any, modelParams map[string]map[string]any) *EmbeddingClient {
	return &EmbeddingClient{
		client:      NewClient(endpoint, apiKey, 0, nil),
		model:       strings.TrimSpace(model),
		dimension:   dimension,
		params:      providerhint.CloneMap(params),
		modelParams: providerhint.CloneNestedMap(modelParams),
	}
}

// Embed sends a sanitized text batch to OpenRouter embeddings and converts numeric vectors into the internal float32 representation.
// Embed 用于把清洗后的文本批次发送到 OpenRouter embeddings，并将数值向量转换成内部 float32 表示。
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	if c == nil || c.client == nil || c.client.sdkClient == nil {
		return appports.EmbeddingResponse{}, fmt.Errorf("openrouter client is nil")
	}
	if strings.TrimSpace(c.client.apiKey) == "" {
		return appports.EmbeddingResponse{}, fmt.Errorf("openrouter api key is required")
	}
	texts := normalizeEmbeddingTexts(req.Texts)
	if len(texts) == 0 {
		return appports.EmbeddingResponse{Vectors: [][]float32{}}, nil
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.EmbeddingResponse{}, fmt.Errorf("openrouter embedding model is required")
	}
	dimension := req.Dimension
	if dimension <= 0 {
		dimension = c.dimension
	}
	params := operations.CreateEmbeddingsRequest{
		Input:          mapEmbeddingInput(texts),
		Model:          model,
		EncodingFormat: operations.EncodingFormatFloat.ToPointer(),
	}
	if dimension > 0 {
		params.Dimensions = int64Pointer(int64(dimension))
	}
	applyEmbeddingHints(&params, providerhint.Merge(c.params, c.modelParams[strings.TrimSpace(model)], req.ProviderHints))

	// Call OpenRouter through its generated SDK and keep vector parsing strict so base64 responses cannot silently corrupt recall.
	// 通过生成式 SDK 调用 OpenRouter，并对向量解析保持严格，避免 base64 响应静默破坏召回链路。
	resp, err := c.client.sdkClient.Embeddings.Generate(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return appports.EmbeddingResponse{}, err
	}
	if resp == nil || resp.CreateEmbeddingsResponseBody == nil || len(resp.CreateEmbeddingsResponseBody.Data) == 0 {
		return appports.EmbeddingResponse{}, fmt.Errorf("openrouter embedding empty response")
	}
	vectors := make([][]float32, 0, len(resp.CreateEmbeddingsResponseBody.Data))
	resultIndices := make([]int, 0, len(resp.CreateEmbeddingsResponseBody.Data))
	for idx, item := range resp.CreateEmbeddingsResponseBody.Data {
		vector, err := mapEmbeddingVector(item.Embedding)
		if err != nil {
			return appports.EmbeddingResponse{}, err
		}
		vectors = append(vectors, vector)
		if item.Index != nil {
			resultIndices = append(resultIndices, int(*item.Index))
		} else {
			resultIndices = append(resultIndices, idx)
		}
	}
	return appports.EmbeddingResponse{
		Vectors:       vectors,
		ResultIndices: resultIndices,
	}, nil
}

// normalizeEmbeddingTexts trims empty embedding inputs before the SDK call so callers can control higher-level invalid-input fallback.
// normalizeEmbeddingTexts 用于在 SDK 调用前裁剪空 embedding 输入，让更高层继续负责非法输入的回退策略。
func normalizeEmbeddingTexts(input []string) []string {
	texts := make([]string, 0, len(input))
	for _, text := range input {
		if text = strings.TrimSpace(text); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// mapEmbeddingInput selects the single-string or string-array union branch expected by OpenRouter embeddings.
// mapEmbeddingInput 用于选择 OpenRouter embeddings 所需的单字符串或字符串数组 union 分支。
func mapEmbeddingInput(texts []string) operations.InputUnion {
	if len(texts) == 1 {
		return operations.CreateInputUnionStr(texts[0])
	}
	return operations.CreateInputUnionArrayOfStr(texts)
}

// applyEmbeddingHints maps stable OpenRouter embedding hints into the generated SDK request struct.
// applyEmbeddingHints 用于把稳定的 OpenRouter embedding 参数映射到生成式 SDK 请求结构中。
func applyEmbeddingHints(params *operations.CreateEmbeddingsRequest, hints map[string]any) {
	if params == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "user":
			if value, ok := providerhint.String(rawValue); ok {
				params.User = &value
			}
		case "encoding_format":
			if value, ok := providerhint.String(rawValue); ok {
				format := operations.EncodingFormat(value)
				params.EncodingFormat = &format
			}
		case "dimensions":
			if value, ok := providerhint.Int64(rawValue); ok && value > 0 {
				params.Dimensions = int64Pointer(value)
			}
		case "input_type":
			if value, ok := providerhint.String(rawValue); ok {
				params.InputType = &value
			}
		case "provider":
			if value, ok := providerPreferencesHint(rawValue); ok {
				params.Provider = optionalValue(value)
			}
		}
	}
}

// mapEmbeddingVector converts one OpenRouter SDK embedding union into the float32 vector shape used by logic ports.
// mapEmbeddingVector 用于把一个 OpenRouter SDK embedding union 转换成 logic 端口使用的 float32 向量形态。
func mapEmbeddingVector(embedding operations.Embedding) ([]float32, error) {
	if embedding.Str != nil {
		return nil, fmt.Errorf("openrouter embedding returned base64 vector; use encoding_format=float")
	}
	vector := make([]float32, 0, len(embedding.ArrayOfNumber))
	for _, dim := range embedding.ArrayOfNumber {
		vector = append(vector, float32(dim))
	}
	return vector, nil
}

// int64Pointer returns an addressable int64 for SDK fields that model optional scalar numbers as pointers.
// int64Pointer 用于为 SDK 中以指针表示的可选数值字段返回可寻址的 int64。
func int64Pointer(value int64) *int64 {
	return &value
}
