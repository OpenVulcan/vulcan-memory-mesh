// client.go implements the closed loopback inference client used only by Vulcan Code managed runtimes.
// client.go 用于实现仅供 Vulcan Code 托管运行时使用的封闭回环推理客户端。
package vulcan_inference

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// inferenceContractVersion is the exact public inference request contract consumed by this adapter.
	// inferenceContractVersion 是当前适配器消费的精确公共推理请求契约版本；未发布内部契约统一为 v1。
	inferenceContractVersion = 1

	// inferenceServiceAPIVersion is the exact discovery and response-envelope API published by Vulcan Code.
	// inferenceServiceAPIVersion 是 Vulcan Code 当前未发布内部发现文档与响应封装 API 版本；统一为 v1。
	inferenceServiceAPIVersion = 1
)

// Config binds the adapter to one protected discovery file and exact caller identity.
// Config 用于把适配器绑定到一份受保护发现文件与精确调用方身份。
type Config struct {
	DiscoveryFile     string
	ExpectedProcessID int
	ExpectedStartedAt int64
	ExpectedCallerID  string
	ConsumerProfileID string
	StartupTimeout    time.Duration
	// MaxConnectionsPerHost is the host-authored ceiling shared by this managed inference client's generation, embedding, and rerank calls.
	// MaxConnectionsPerHost 是该托管推理客户端的生成、向量与重排调用共同遵守的宿主配置每主机连接上限。
	MaxConnectionsPerHost int
}

// Client implements VMM LLM, embedding, and rerank ports over one restricted inference service.
// Client 通过一项受限推理服务实现 VMM 的 LLM、向量与重排端口。
type Client struct {
	config Config
	http   *http.Client
	mu     sync.Mutex
	cached cachedDiscovery
}

// cachedDiscovery retains one validated grant until its file generation or expiration changes.
// cachedDiscovery 用于保留一份已校验授权，直到文件代次或过期状态变化。
type cachedDiscovery struct {
	snapshot discoverySnapshot
	modTime  time.Time
}

// discoverySnapshot mirrors the protected Vulcan Code inference discovery document.
// discoverySnapshot 用于镜像受保护的 Vulcan Code 推理发现文档。
type discoverySnapshot struct {
	APIVersion        int    `json:"api_version"`
	Status            string `json:"status"`
	BaseURL           string `json:"base_url"`
	ProcessID         int    `json:"process_id"`
	CallerID          string `json:"caller_id"`
	ConsumerProfileID string `json:"consumer_profile_id"`
	AccessToken       string `json:"access_token"`
	ExpiresAtUnixMS   int64  `json:"expires_at_unix_ms"`
	StartedAtUnixMS   string `json:"started_at_unix_ms"`
}

// successEnvelope is the only accepted successful HTTP response shape.
// successEnvelope 是唯一允许的 HTTP 成功响应形态。
type successEnvelope struct {
	APIVersion int             `json:"api_version"`
	Result     json.RawMessage `json:"result"`
}

// errorEnvelope is the controlled service failure shape returned by Vulcan Code.
// errorEnvelope 是 Vulcan Code 返回的受控服务失败形态。
type errorEnvelope struct {
	APIVersion int    `json:"api_version"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// responsesUsage mirrors the standard Responses token dimensions consumed by VMM.
// responsesUsage 镜像 VMM 消费的标准 Responses Token 维度。
type responsesUsage struct {
	InputTokens *uint64 `json:"input_tokens"`
	// InputTokensDetails carries the standard cache-read token breakdown.
	// InputTokensDetails 承载标准缓存读取 token 明细。
	InputTokensDetails responsesInputTokensDetails `json:"input_tokens_details"`
	OutputTokens       *uint64                     `json:"output_tokens"`
	// OutputTokensDetails carries the standard hidden-reasoning token breakdown.
	// OutputTokensDetails 承载标准隐藏推理 token 明细。
	OutputTokensDetails responsesOutputTokensDetails `json:"output_tokens_details"`
	TotalTokens         *uint64                      `json:"total_tokens"`
}

// responsesInputTokensDetails mirrors standard cached-input token accounting.
// responsesInputTokensDetails 用于映射标准缓存输入 token 统计。
type responsesInputTokensDetails struct {
	// CachedTokens is the provider-reported cache-read token count when available.
	// CachedTokens 是供应商在可用时上报的缓存读取 token 数量。
	CachedTokens *uint64 `json:"cached_tokens"`
}

// responsesOutputTokensDetails mirrors standard reasoning-token accounting.
// responsesOutputTokensDetails 用于映射标准推理 token 统计。
type responsesOutputTokensDetails struct {
	// ReasoningTokens is the provider-reported hidden reasoning token count when available.
	// ReasoningTokens 是供应商在可用时上报的隐藏推理 token 数量。
	ReasoningTokens *uint64 `json:"reasoning_tokens"`
}

// capabilitySnapshot mirrors the consumer-scoped configured inference routes published by Vulcan Code.
// capabilitySnapshot 用于映射 Vulcan Code 发布的消费者作用域已配置推理路由。
type capabilitySnapshot struct {
	// ContractVersion identifies the exact configured-inference contract generation.
	// ContractVersion 用于标识精确的已配置推理契约代际。
	ContractVersion int `json:"contract_version"`

	// ConsumerProfileID identifies the profile represented by this snapshot.
	// ConsumerProfileID 用于标识该快照表示的消费者配置档。
	ConsumerProfileID string `json:"consumer_profile_id"`

	// Operations contains every configured operation and purpose route.
	// Operations 包含全部已配置操作与用途路由。
	Operations []capabilityOperation `json:"operations"`
}

// capabilityOperation mirrors one configured operation route needed for managed model synchronization.
// capabilityOperation 用于映射托管模型同步所需的一条已配置操作路由。
type capabilityOperation struct {
	// Operation is the configured semantic operation identifier.
	// Operation 是已配置的语义操作标识。
	Operation string `json:"operation"`

	// PurposeID is the consumer-owned LLM purpose when this is a restricted generation route.
	// PurposeID 是受限生成路由对应的消费者自有 LLM 用途。
	PurposeID string `json:"purpose_id"`

	// Available reports whether the route can execute now.
	// Available 表示该路由当前是否可执行。
	Available bool `json:"available"`

	// Route carries the immutable physical model identity when resolution succeeds.
	// Route 在解析成功时承载不可变的物理模型身份。
	Route *capabilityRoute `json:"route"`
}

// capabilityRoute mirrors the immutable physical route identity exposed by one configured operation.
// capabilityRoute 用于映射一条已配置操作暴露的不可变物理路由身份。
type capabilityRoute struct {
	// ProviderID is the exact provider identifier selected by Vulcan Code.
	// ProviderID 是 Vulcan Code 选择的精确供应商标识。
	ProviderID string `json:"provider_id"`

	// ServiceEntryID is the exact provider service entry selected by Vulcan Code.
	// ServiceEntryID 是 Vulcan Code 选择的精确供应商服务入口。
	ServiceEntryID string `json:"service_entry_id"`

	// ConnectionID is the exact credential connection selected by Vulcan Code.
	// ConnectionID 是 Vulcan Code 选择的精确凭据连接。
	ConnectionID string `json:"connection_id"`

	// ModelID is the exact model identifier selected by Vulcan Code.
	// ModelID 是 Vulcan Code 选择的精确模型标识。
	ModelID string `json:"model_id"`

	// ProtocolBindingID is the exact protocol binding used to project the provider request.
	// ProtocolBindingID 是用于投影供应商请求的精确协议绑定。
	ProtocolBindingID string `json:"protocol_binding_id"`
}

// PurposeRoute is the frozen non-secret route identity synchronized for one managed VMM LLM purpose.
// PurposeRoute 是为一个托管 VMM LLM 用途同步的冻结非秘密路由身份。
type PurposeRoute struct {
	// ProviderID is the exact configured provider.
	// ProviderID 是精确的已配置供应商。
	ProviderID string

	// ServiceEntryID is the exact configured provider service entry.
	// ServiceEntryID 是精确的已配置供应商服务入口。
	ServiceEntryID string

	// ConnectionID is the exact configured credential connection.
	// ConnectionID 是精确的已配置凭据连接。
	ConnectionID string

	// ModelID is the exact configured physical model.
	// ModelID 是精确的已配置物理模型。
	ModelID string

	// ProtocolBindingID is the exact configured provider protocol binding.
	// ProtocolBindingID 是精确的已配置供应商协议绑定。
	ProtocolBindingID string
}

// responsesContent mirrors one standard Responses output content part.
// responsesContent 镜像一项标准 Responses 输出内容分片。
type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesOutputItem mirrors the standard output items needed by VMM.
// responsesOutputItem 镜像 VMM 所需的标准输出项目。
type responsesOutputItem struct {
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []responsesContent `json:"content"`
}

// responsesCreateResponse mirrors one completed standard Responses object.
// responsesCreateResponse 镜像一个已完成的标准 Responses 对象。
type responsesCreateResponse struct {
	ID     string                `json:"id"`
	Object string                `json:"object"`
	Status string                `json:"status"`
	Model  string                `json:"model"`
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage        `json:"usage"`
	Error  json.RawMessage       `json:"error"`
}

// responsesErrorEnvelope mirrors the standard OpenAI-compatible error envelope.
// responsesErrorEnvelope 镜像标准 OpenAI 兼容错误信封。
type responsesErrorEnvelope struct {
	Error struct {
		Type    string  `json:"type"`
		Code    string  `json:"code"`
		Param   *string `json:"param"`
		Message string  `json:"message"`
	} `json:"error"`
}

// embeddingResult mirrors one dense float result with stable source identity.
// embeddingResult 用于镜像一项带稳定来源身份的稠密浮点结果。
type embeddingResult struct {
	InputID    string           `json:"input_id"`
	InputIndex int              `json:"input_index"`
	Embedding  embeddingPayload `json:"embedding"`
}

// embeddingPayload mirrors the tagged dense payload accepted by VMM.
// embeddingPayload 用于镜像 VMM 接受的带标签稠密载荷。
type embeddingPayload struct {
	Type   string    `json:"type"`
	Values []float32 `json:"values"`
}

// embeddingResponse contains ordered results from one complete configured batch.
// embeddingResponse 用于保存一批完整配置化向量请求的有序结果。
type embeddingResponse struct {
	Results []embeddingResult       `json:"results"`
	Dropped []embeddingDroppedInput `json:"dropped"`
}

// embeddingDroppedInput mirrors one provider-confirmed item-scoped omission.
// embeddingDroppedInput 用于镜像一项供应商已确认的单项省略记录。
type embeddingDroppedInput struct {
	InputID           string `json:"input_id"`
	InputIndex        int    `json:"input_index"`
	ReasonCode        string `json:"reason_code"`
	ControlledMessage string `json:"controlled_message"`
}

// rerankResult mirrors one validated ranked candidate.
// rerankResult 用于镜像一项已校验的排序候选。
type rerankResult struct {
	CandidateID   string  `json:"candidate_id"`
	OriginalIndex int     `json:"original_index"`
	Rank          int     `json:"rank"`
	Score         float64 `json:"score"`
}

// rerankResponse contains ranked results returned by one configured route.
// rerankResponse 用于保存一条配置化路由返回的重排结果。
type rerankResponse struct {
	Results []rerankResult `json:"results"`
}

// New creates one managed inference client after validating immutable local configuration.
// New 用于在校验不可变本地配置后创建一项托管推理客户端。
func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.DiscoveryFile) == "" ||
		strings.TrimSpace(config.ExpectedCallerID) == "" ||
		strings.TrimSpace(config.ConsumerProfileID) == "" {
		return nil, errors.New("managed inference config is incomplete")
	}
	if config.ExpectedProcessID <= 0 || config.ExpectedStartedAt <= 0 {
		return nil, errors.New("managed inference parent identity is incomplete")
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 30 * time.Second
	}
	if config.MaxConnectionsPerHost <= 0 {
		config.MaxConnectionsPerHost = 8
	}
	return &Client{
		config: config,
		http:   httpclient.NewBounded(config.MaxConnectionsPerHost),
	}, nil
}

// PurposeRoutes loads the authoritative purpose-to-route mapping from the managed capability endpoint.
// PurposeRoutes 用于从托管能力端点加载权威的用途到路由映射。
func (c *Client) PurposeRoutes(ctx context.Context) (map[string]PurposeRoute, error) {
	if c == nil {
		return nil, errors.New("managed inference client is nil")
	}
	var snapshot capabilitySnapshot
	path := "inference/v1/capabilities/" + url.PathEscape(c.config.ConsumerProfileID)
	if err := c.call(ctx, http.MethodGet, path, nil, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.ContractVersion != inferenceContractVersion || snapshot.ConsumerProfileID != c.config.ConsumerProfileID {
		return nil, errors.New("Vulcan inference capability identity is invalid")
	}
	routes := make(map[string]PurposeRoute)
	for _, operation := range snapshot.Operations {
		purposeID := strings.TrimSpace(operation.PurposeID)
		if operation.Operation != "conversation_respond" || purposeID == "" || !operation.Available || operation.Route == nil {
			continue
		}
		route := PurposeRoute{
			ProviderID:        strings.TrimSpace(operation.Route.ProviderID),
			ServiceEntryID:    strings.TrimSpace(operation.Route.ServiceEntryID),
			ConnectionID:      strings.TrimSpace(operation.Route.ConnectionID),
			ModelID:           strings.TrimSpace(operation.Route.ModelID),
			ProtocolBindingID: strings.TrimSpace(operation.Route.ProtocolBindingID),
		}
		if route.ProviderID == "" || route.ServiceEntryID == "" || route.ConnectionID == "" || route.ModelID == "" || route.ProtocolBindingID == "" {
			return nil, fmt.Errorf("Vulcan inference purpose %s has incomplete physical route identity", purposeID)
		}
		if existing, exists := routes[purposeID]; exists && existing != route {
			return nil, fmt.Errorf("Vulcan inference purpose %s resolves to multiple physical routes", purposeID)
		}
		routes[purposeID] = route
	}
	return routes, nil
}

// Generate executes one standard Responses request through the managed Vulcan endpoint.
// Generate 通过托管 Vulcan 端点执行一项标准 Responses 请求。
func (c *Client) Generate(ctx context.Context, request appports.LLMRequest) (appports.LLMResponse, error) {
	requestID, err := newRequestID("vmm-llm")
	if err != nil {
		return appports.LLMResponse{}, err
	}
	textConfig := map[string]any{"format": map[string]any{"type": "text"}}
	if request.ResponseFormat == appports.LLMResponseFormatJSON {
		if request.StructuredOutput == nil {
			return appports.LLMResponse{}, errors.New("Vulcan Responses JSON request requires an explicit structured-output schema")
		}
		var schema map[string]any
		if err := json.Unmarshal(request.StructuredOutput.Schema, &schema); err != nil {
			return appports.LLMResponse{}, fmt.Errorf("decode Vulcan Responses structured-output schema: %w", err)
		}
		if strings.TrimSpace(request.StructuredOutput.Name) == "" || len(schema) == 0 {
			return appports.LLMResponse{}, errors.New("Vulcan Responses structured-output schema name and object must not be empty")
		}
		format := map[string]any{
			"type":   "json_schema",
			"name":   strings.TrimSpace(request.StructuredOutput.Name),
			"schema": schema,
			"strict": request.StructuredOutput.Strict,
		}
		if description := strings.TrimSpace(request.StructuredOutput.Description); description != "" {
			format["description"] = description
		}
		textConfig = map[string]any{"format": format}
	}
	body := map[string]any{
		"model":           string(request.RouteSelectionLevel),
		"instructions":    request.SystemPrompt,
		"input":           request.UserPrompt,
		"reasoning":       map[string]any{"effort": "none"},
		"store":           false,
		"stream":          false,
		"text":            textConfig,
		"client_metadata": map[string]any{"vmm_request_id": requestID},
	}
	var response responsesCreateResponse
	if err := c.callResponses(ctx, body, &response); err != nil {
		return appports.LLMResponse{RequestID: requestID}, err
	}
	if response.Object != "response" || response.Status != "completed" || strings.TrimSpace(response.ID) == "" {
		return appports.LLMResponse{RequestID: requestID}, errors.New("Vulcan Responses returned an invalid or incomplete response object")
	}
	var content strings.Builder
	for _, item := range response.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				content.WriteString(part.Text)
			}
		}
	}
	if content.Len() == 0 {
		return appports.LLMResponse{RequestID: requestID}, errors.New("Vulcan Responses completed without assistant output text")
	}
	result := appports.LLMResponse{
		Content:   content.String(),
		Model:     response.Model,
		RequestID: requestID,
		Usage: logicdomain.LLMUsage{
			PromptTokens:      optionalUsageInt(response.Usage.InputTokens),
			CompletionTokens:  optionalUsageInt(response.Usage.OutputTokens),
			TotalTokens:       optionalUsageInt(response.Usage.TotalTokens),
			CachedInputTokens: optionalUsageInt(response.Usage.InputTokensDetails.CachedTokens),
			ReasoningTokens:   optionalUsageInt(response.Usage.OutputTokensDetails.ReasoningTokens),
		},
	}
	configuredModel := strings.TrimSpace(request.Model)
	responseModel := strings.TrimSpace(response.Model)
	if responseModel == "" {
		return result, errors.New("Vulcan Responses completed without a physical response model")
	}
	if configuredModel != "" && responseModel != configuredModel {
		return result, fmt.Errorf("Vulcan Responses physical model drifted from %s to %s", configuredModel, responseModel)
	}
	return result, nil
}

// Embed creates one dense float vector for every accepted text and restores the exact source order.
// Embed 用于为每条已接受文本创建稠密浮点向量，并恢复精确来源顺序。
func (c *Client) Embed(ctx context.Context, request appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	requestID, err := newRequestID("vmm-embedding")
	if err != nil {
		return appports.EmbeddingResponse{}, err
	}
	inputs := make([]any, 0, len(request.Texts))
	for index, text := range request.Texts {
		inputs = append(inputs, map[string]any{
			"input_id": strconv.Itoa(index),
			"content":  map[string]any{"type": "text", "text": text},
		})
	}
	dimensions := request.Dimension
	if dimensions <= 0 {
		dimensions = 0
	}
	body := map[string]any{
		"contract_version":     inferenceContractVersion,
		"request_id":           requestID,
		"consumer_profile_id":  c.config.ConsumerProfileID,
		"inputs":               inputs,
		"task":                 "provider_default",
		"output_kind":          "dense",
		"encoding":             "float",
		"invalid_input_policy": "fail_batch",
	}
	if request.AllowPartialInvalidTexts {
		body["invalid_input_policy"] = "drop_provider_confirmed_items"
	}
	if dimensions > 0 {
		body["dimensions"] = dimensions
	}
	var response embeddingResponse
	if err := c.call(ctx, http.MethodPost, "inference/v1/embeddings", body, &response); err != nil {
		return appports.EmbeddingResponse{}, err
	}
	if len(response.Results)+len(response.Dropped) != len(request.Texts) {
		return appports.EmbeddingResponse{}, fmt.Errorf("Vulcan inference embedding coverage mismatch: got %d want %d", len(response.Results)+len(response.Dropped), len(request.Texts))
	}
	vectors := make([][]float32, len(request.Texts))
	seen := make([]bool, len(request.Texts))
	for _, result := range response.Results {
		if result.InputIndex < 0 || result.InputIndex >= len(request.Texts) ||
			result.InputID != strconv.Itoa(result.InputIndex) ||
			result.Embedding.Type != "dense" ||
			seen[result.InputIndex] {
			return appports.EmbeddingResponse{}, errors.New("Vulcan inference returned an invalid embedding identity or payload")
		}
		for _, value := range result.Embedding.Values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return appports.EmbeddingResponse{}, errors.New("Vulcan inference returned a non-finite embedding value")
			}
		}
		seen[result.InputIndex] = true
		vectors[result.InputIndex] = append([]float32(nil), result.Embedding.Values...)
	}
	resultVectors := make([][]float32, 0, len(response.Results))
	resultIndices := make([]int, 0, len(response.Results))
	for index, vector := range vectors {
		if seen[index] {
			resultIndices = append(resultIndices, index)
			resultVectors = append(resultVectors, vector)
		}
	}
	dropped := make([]appports.EmbeddingDroppedInput, 0, len(response.Dropped))
	for _, item := range response.Dropped {
		if !request.AllowPartialInvalidTexts ||
			item.InputIndex < 0 || item.InputIndex >= len(request.Texts) ||
			item.InputID != strconv.Itoa(item.InputIndex) ||
			seen[item.InputIndex] ||
			strings.TrimSpace(item.ReasonCode) == "" ||
			strings.TrimSpace(item.ControlledMessage) == "" {
			return appports.EmbeddingResponse{}, errors.New("Vulcan inference returned an invalid provider-confirmed embedding omission")
		}
		seen[item.InputIndex] = true
		dropped = append(dropped, appports.EmbeddingDroppedInput{
			Index:  item.InputIndex,
			Text:   request.Texts[item.InputIndex],
			Reason: item.ReasonCode + ": " + item.ControlledMessage,
		})
	}
	for _, covered := range seen {
		if !covered {
			return appports.EmbeddingResponse{}, errors.New("Vulcan inference embedding response did not cover every input")
		}
	}
	return appports.EmbeddingResponse{
		Vectors:       resultVectors,
		ResultIndices: resultIndices,
		Dropped:       dropped,
	}, nil
}

// Rerank sends one stable candidate set and rejects any unknown, repeated, or non-finite result.
// Rerank 用于发送一组稳定候选，并拒绝任何未知、重复或非有限结果。
func (c *Client) Rerank(ctx context.Context, query string, documents []appports.RerankerDocument, topN int) ([]appports.RerankerResult, error) {
	requestID, err := newRequestID("vmm-rerank")
	if err != nil {
		return nil, err
	}
	candidates := make([]any, 0, len(documents))
	known := make(map[string]int, len(documents))
	for index, document := range documents {
		if _, exists := known[document.ID]; exists {
			return nil, fmt.Errorf("rerank document id %q is repeated", document.ID)
		}
		known[document.ID] = index
		candidates = append(candidates, map[string]any{"id": document.ID, "text": document.Text})
	}
	body := map[string]any{
		"contract_version":    inferenceContractVersion,
		"request_id":          requestID,
		"consumer_profile_id": c.config.ConsumerProfileID,
		"query":               map[string]any{"id": "query", "text": query},
		"candidates":          candidates,
		"top_n":               topN,
		"truncation":          "provider_default",
		"return_content":      false,
	}
	var response rerankResponse
	if err := c.call(ctx, http.MethodPost, "inference/v1/rerank", body, &response); err != nil {
		return nil, err
	}
	results := make([]appports.RerankerResult, 0, len(response.Results))
	seen := make(map[string]struct{}, len(response.Results))
	for responseIndex, result := range response.Results {
		originalIndex, exists := known[result.CandidateID]
		if !exists {
			return nil, fmt.Errorf("Vulcan inference returned unknown rerank candidate %q", result.CandidateID)
		}
		if _, repeated := seen[result.CandidateID]; repeated ||
			result.OriginalIndex != originalIndex ||
			result.Rank != responseIndex ||
			math.IsNaN(result.Score) ||
			math.IsInf(result.Score, 0) {
			return nil, fmt.Errorf("Vulcan inference returned invalid rerank candidate %q", result.CandidateID)
		}
		seen[result.CandidateID] = struct{}{}
		results = append(results, appports.RerankerResult{ID: result.CandidateID, Score: result.Score})
	}
	return results, nil
}

// callResponses sends one authenticated standard Responses Create request without a private envelope.
// callResponses 发送一项不带私有信封的已鉴权标准 Responses Create 请求。
func (c *Client) callResponses(ctx context.Context, body any, output *responsesCreateResponse) error {
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		discovery, err := c.discovery(attempt > 0)
		if err != nil {
			return err
		}
		endpoint, err := url.JoinPath(discovery.BaseURL, "v1/responses")
		if err != nil {
			return fmt.Errorf("build Vulcan Responses endpoint: %w", err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encodedBody))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+discovery.AccessToken)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		response, err := c.http.Do(request)
		if err != nil {
			return fmt.Errorf("call Vulcan Responses: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		closeErr := response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			var serviceError responsesErrorEnvelope
			if json.Unmarshal(responseBody, &serviceError) == nil && strings.TrimSpace(serviceError.Error.Message) != "" {
				return fmt.Errorf("Vulcan Responses %s: %s", serviceError.Error.Code, serviceError.Error.Message)
			}
			return fmt.Errorf("Vulcan Responses returned HTTP %d", response.StatusCode)
		}
		if err := json.Unmarshal(responseBody, output); err != nil {
			return fmt.Errorf("decode Vulcan Responses object: %w", err)
		}
		return nil
	}
	return errors.New("Vulcan Responses authorization refresh did not produce a usable grant")
}

// call sends one closed authenticated operation and strictly decodes its versioned envelope.
// call 用于发送一项封闭鉴权操作，并严格解码其版本化封装。
func (c *Client) call(ctx context.Context, method string, path string, body any, output any) error {
	var encodedBody []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		encodedBody = encoded
	}
	for attempt := 0; attempt < 2; attempt++ {
		discovery, err := c.discovery(attempt > 0)
		if err != nil {
			return err
		}
		endpoint, err := url.JoinPath(discovery.BaseURL, path)
		if err != nil {
			return fmt.Errorf("build Vulcan inference endpoint: %w", err)
		}
		var reader io.Reader
		if encodedBody != nil {
			reader = bytes.NewReader(encodedBody)
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+discovery.AccessToken)
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.http.Do(request)
		if err != nil {
			return fmt.Errorf("call Vulcan inference: %w", err)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		closeErr := response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		// A loopback 401 is emitted before inference dispatch, so refreshing the grant and
		// replaying the exact request once cannot duplicate provider work.
		// 回环 401 在推理分派前产生，因此刷新授权并仅重放一次精确请求不会重复供应商工作。
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			var serviceError errorEnvelope
			if json.Unmarshal(responseBody, &serviceError) == nil && strings.TrimSpace(serviceError.Message) != "" {
				return fmt.Errorf("Vulcan inference %s: %s", serviceError.Code, serviceError.Message)
			}
			return fmt.Errorf("Vulcan inference returned HTTP %d", response.StatusCode)
		}
		var envelope successEnvelope
		decoder := json.NewDecoder(bytes.NewReader(responseBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			return fmt.Errorf("decode Vulcan inference envelope: %w", err)
		}
		if envelope.APIVersion != inferenceServiceAPIVersion {
			return fmt.Errorf("unsupported Vulcan inference API version %d", envelope.APIVersion)
		}
		if output == nil {
			return nil
		}
		// Result types intentionally project only the fields consumed by VMM. The
		// Vulcan service contract may add unrelated response metadata, so rejecting
		// unknown result fields would break forward-compatible reads.
		// 结果类型只投影 VMM 实际消费的字段。Vulcan 服务契约可增加无关响应元数据，
		// 因此若拒绝未知结果字段，会破坏向前兼容的读取能力。
		if err := json.Unmarshal(envelope.Result, output); err != nil {
			return fmt.Errorf("decode Vulcan inference result: %w", err)
		}
		return nil
	}
	return errors.New("Vulcan inference authorization refresh did not produce a usable grant")
}

// discovery loads or reuses one current protected grant after exact process, caller, profile, and loopback validation.
// discovery 用于在完成精确进程、调用方、配置档与回环校验后加载或复用当前受保护授权。
func (c *Client) discovery(forceReload bool) (discoverySnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(c.config.DiscoveryFile)
	if err != nil {
		return discoverySnapshot{}, fmt.Errorf("inspect Vulcan inference discovery: %w", err)
	}
	nowUnixMS := time.Now().UnixMilli()
	if !forceReload &&
		!c.cached.modTime.IsZero() &&
		c.cached.modTime.Equal(info.ModTime()) &&
		c.cached.snapshot.ExpiresAtUnixMS > nowUnixMS+1000 {
		return c.cached.snapshot, nil
	}
	body, err := os.ReadFile(c.config.DiscoveryFile)
	if err != nil {
		return discoverySnapshot{}, fmt.Errorf("read Vulcan inference discovery: %w", err)
	}
	var snapshot discoverySnapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return discoverySnapshot{}, fmt.Errorf("decode Vulcan inference discovery: %w", err)
	}
	if err := c.validateDiscovery(snapshot, nowUnixMS); err != nil {
		return discoverySnapshot{}, err
	}
	c.cached = cachedDiscovery{snapshot: snapshot, modTime: info.ModTime()}
	return snapshot, nil
}

// validateDiscovery enforces the exact immutable grant identity declared by the managed manifest.
// validateDiscovery 用于强制校验托管清单声明的精确不可变授权身份。
func (c *Client) validateDiscovery(snapshot discoverySnapshot, nowUnixMS int64) error {
	if snapshot.APIVersion != inferenceServiceAPIVersion || snapshot.Status != "ready" {
		return errors.New("Vulcan inference discovery is not a supported ready document")
	}
	startedAt, err := strconv.ParseInt(snapshot.StartedAtUnixMS, 10, 64)
	if err != nil || startedAt != c.config.ExpectedStartedAt ||
		snapshot.ProcessID != c.config.ExpectedProcessID {
		return errors.New("Vulcan inference discovery process identity does not match the managed parent")
	}
	if snapshot.CallerID != c.config.ExpectedCallerID ||
		snapshot.ConsumerProfileID != c.config.ConsumerProfileID ||
		strings.TrimSpace(snapshot.AccessToken) == "" ||
		snapshot.ExpiresAtUnixMS <= nowUnixMS {
		return errors.New("Vulcan inference discovery grant identity is invalid or expired")
	}
	parsed, err := url.Parse(snapshot.BaseURL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return errors.New("Vulcan inference base URL must be a root loopback HTTP origin")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return errors.New("Vulcan inference base URL must use a loopback host")
	}
	return nil
}

// newRequestID creates one unpredictable stable request identifier without exposing provider routing.
// newRequestID 用于创建一个不可预测的稳定请求标识，且不暴露供应商路由。
func newRequestID(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create inference request id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(random), nil
}

// optionalUsageInt narrows one optional standard token count into the legacy VMM integer range.
// optionalUsageInt 用于把一个可选标准 Token 数量收窄到旧版 VMM 整数范围。
func optionalUsageInt(value *uint64) int {
	if value == nil {
		return 0
	}
	maximum := uint64(^uint(0) >> 1)
	if *value > maximum {
		return int(maximum)
	}
	return int(*value)
}
