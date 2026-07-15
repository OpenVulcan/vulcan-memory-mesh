// embedding.go implements the OpenAI-compatible outbound adapters.
// embedding.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// EmbeddingClient adapts the internal embedding port onto the official OpenAI embeddings SDK.
// EmbeddingClient 用于把内部 embedding 端口适配到官方 OpenAI Embeddings SDK。
type EmbeddingClient struct {
	client      *Client
	model       string
	dimension   int
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewEmbeddingClient binds one model and cloned provider hints to the native OpenAI embedding adapter.
// NewEmbeddingClient 用于把单个模型及克隆后的 provider hint 绑定到原生 OpenAI 向量适配器。
func NewEmbeddingClient(endpoint, apiKey, model string, dimension int, organization, project string, params map[string]any, modelParams map[string]map[string]any) *EmbeddingClient {
	return &EmbeddingClient{
		client:      NewClient(endpoint, apiKey, organization, project, nil),
		model:       strings.TrimSpace(model),
		dimension:   dimension,
		params:      cloneHintMap(params),
		modelParams: cloneNestedHintMap(modelParams),
	}
}

// Embed sends a batch embedding request, validates the provider response shape, and restores input ordering.
// Embed 用于发送批量向量请求、校验 provider 响应结构并恢复输入顺序。
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	// Sanitize the input batch first so the raw SDK request only carries the concrete non-empty texts chosen by the caller or by the outer embedding controller.
	// 先清洗输入批次，确保真正送进 SDK 请求的只是不为空的实际文本，并把拆批/回退策略继续留给外层 embedding 控制器。
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
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	if model == "" {
		return appports.EmbeddingResponse{}, fmt.Errorf("openai native embedding model is required")
	}

	// Build the SDK request from the internal embedding contract.
	// 根据内部 embedding 契约组装 SDK 请求。
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
	applyEmbeddingHints(&params, mergeProviderHints(c.params, c.modelParams, model, req.ProviderHints))
	resp, err := c.client.sdkClient.Embeddings.New(ctx, params, requestOptionsFromContext(ctx)...)
	if err != nil {
		return appports.EmbeddingResponse{}, err
	}

	// Convert the SDK response vectors into the internal float32 representation.
	// 将 SDK 返回的向量结果转换为内部使用的 float32 结构。
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

// mapEmbeddingInput maps values into the target shape.
// mapEmbeddingInput 用于将值映射到目标结构。
func mapEmbeddingInput(texts []string) openai.EmbeddingNewParamsInputUnion {
	if len(texts) == 1 {
		return openai.EmbeddingNewParamsInputUnion{OfString: openai.String(texts[0])}
	}
	return openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts}
}

// applyEmbeddingHints applies the target settings.
// applyEmbeddingHints 用于应用目标设置。
func applyEmbeddingHints(params *openai.EmbeddingNewParams, hints map[string]any) {
	// Map portable provider hints first, then forward unsupported extras through the SDK payload.
	// 先映射可移植的 provider 参数，再通过 SDK 扩展载荷转发未内建支持的参数。
	if params == nil {
		return
	}
	extraFields := map[string]any{}
	for rawKey, rawValue := range hints {
		originalKey := strings.TrimSpace(rawKey)
		key := strings.ToLower(originalKey)
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
		default:
			if rawValue != nil {
				extraFields[originalKey] = rawValue
			}
		}
	}
	if len(extraFields) > 0 {
		params.SetExtraFields(extraFields)
	}
}
