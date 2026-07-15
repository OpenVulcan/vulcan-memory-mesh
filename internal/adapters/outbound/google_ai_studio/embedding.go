// embedding.go implements the Google AI Studio native embedding adapter.
// embedding.go 用于实现 Google AI Studio 原生 embedding 适配器。
package google_ai_studio

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/adapters/outbound/providerhint"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"google.golang.org/genai"
)

// EmbeddingClient adapts the internal embedding port onto the official Google GenAI EmbedContent API.
// EmbeddingClient 用于把内部 embedding 端口适配到官方 Google GenAI 的 EmbedContent API。
type EmbeddingClient struct {
	client      *Client
	model       string
	dimension   int
	params      map[string]any
	modelParams map[string]map[string]any
}

// NewEmbeddingClient creates one Google AI Studio native embedding adapter bound to one fixed model plus a configured output dimension.
// NewEmbeddingClient 用于创建一个绑定固定模型和指定输出维度的 Google AI Studio 原生 embedding 适配器。
func NewEmbeddingClient(endpoint, apiKey, model string, dimension int, params map[string]any, modelParams map[string]map[string]any) *EmbeddingClient {
	return &EmbeddingClient{
		client:      NewClient(endpoint, apiKey, nil),
		model:       strings.TrimSpace(model),
		dimension:   dimension,
		params:      providerhint.CloneMap(params),
		modelParams: providerhint.CloneNestedMap(modelParams),
	}
}

// Embed executes one provider-neutral embedding request against Google AI Studio and returns one float32 vector per non-empty text input.
// Embed 用于把一次 provider 无关的 embedding 请求发送到 Google AI Studio，并为每条非空文本返回一个 float32 向量。
func (c *EmbeddingClient) Embed(ctx context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	// Sanitize the input batch first so blank texts never turn into provider-side invalid requests while batching and fallback policies stay centralized in the outer embedding controller.
	// 先清洗输入批次，避免空文本变成 provider 侧非法请求，同时把拆批与回退策略继续统一收敛在外层 embedding 控制器中。
	if c == nil || c.client == nil {
		return appports.EmbeddingResponse{}, fmt.Errorf("google ai studio embedding client is nil")
	}
	sdkClient, err := c.client.SDK()
	if err != nil {
		return appports.EmbeddingResponse{}, err
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
		return appports.EmbeddingResponse{}, fmt.Errorf("google ai studio embedding model is required")
	}

	// Build the Gemini-native embedding request so the configured vector dimension keeps matching downstream vector-store schema expectations.
	// 组装 Gemini 原生 embedding 请求，确保配置中的向量维度继续与下游向量存储 schema 保持一致。
	config := &genai.EmbedContentConfig{}
	applyEmbeddingHints(config, providerhint.Merge(c.params, c.modelParams[strings.TrimSpace(model)], req.ProviderHints))
	dimension := req.Dimension
	if dimension <= 0 {
		dimension = c.dimension
	}
	if dimension > 0 {
		config.OutputDimensionality = int32Ptr(dimension)
	}
	if httpOptions := requestHTTPOptionsFromContext(ctx); httpOptions != nil {
		config.HTTPOptions = httpOptions
	}
	contents := make([]*genai.Content, 0, len(texts))
	for _, text := range texts {
		contents = append(contents, genai.NewContentFromText(text, genai.RoleUser))
	}

	// Execute the upstream call and convert Google vectors into the internal float32 contract used by the recall pipeline.
	// 执行上游调用，并把 Google 返回的向量转换成召回流水线使用的内部 float32 契约。
	resp, err := sdkClient.Models.EmbedContent(ctx, model, contents, config)
	if err != nil {
		return appports.EmbeddingResponse{}, err
	}
	if resp == nil || len(resp.Embeddings) == 0 {
		return appports.EmbeddingResponse{}, fmt.Errorf("google ai studio embedding empty response")
	}
	vectors := make([][]float32, 0, len(resp.Embeddings))
	for _, item := range resp.Embeddings {
		if item == nil {
			vectors = append(vectors, []float32{})
			continue
		}
		vector := make([]float32, 0, len(item.Values))
		vector = append(vector, item.Values...)
		vectors = append(vectors, vector)
	}
	return appports.EmbeddingResponse{Vectors: vectors}, nil
}

// applyEmbeddingHints maps portable and Google-specific hint fields onto the EmbedContentConfig used by the Gemini API.
// applyEmbeddingHints 用于把可移植和 Google 专属的 hint 字段映射到 Gemini API 使用的 EmbedContentConfig。
func applyEmbeddingHints(config *genai.EmbedContentConfig, hints map[string]any) {
	if config == nil {
		return
	}
	for rawKey, rawValue := range hints {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch key {
		case "task_type":
			if value, ok := stringHint(rawValue); ok {
				config.TaskType = value
			}
		case "title":
			if value, ok := stringHint(rawValue); ok {
				config.Title = value
			}
		case "output_dimensionality", "dimensions":
			if value, ok := intHint(rawValue); ok && value > 0 {
				config.OutputDimensionality = int32Ptr(value)
			}
		}
	}
}
