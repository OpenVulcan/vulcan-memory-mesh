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

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// inferenceContractVersion is the exact public inference request contract consumed by this adapter.
	// inferenceContractVersion 是当前适配器消费的精确公共推理请求契约版本。
	inferenceContractVersion = 2

	// inferenceServiceAPIVersion is the exact discovery and response-envelope version consumed by this adapter.
	// inferenceServiceAPIVersion 是当前适配器消费的精确发现与响应封装版本。
	inferenceServiceAPIVersion = 2
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

// routeSnapshot contains the non-secret route fields used by VMM diagnostics.
// routeSnapshot 用于保存 VMM 诊断需要的非秘密路由字段。
type routeSnapshot struct {
	ModelID string `json:"model_id"`
}

// usageValue mirrors one known-or-unknown normalized token count.
// usageValue 用于镜像一项已知或未知的规范化 Token 数量。
type usageValue struct {
	State string `json:"state"`
	Value uint64 `json:"value,omitempty"`
}

// requestUsage contains the token fields that VMM can represent in its narrower usage contract.
// requestUsage 用于保存 VMM 较窄用量契约可以表达的 Token 字段。
type requestUsage struct {
	InputTokens  usageValue `json:"input_tokens"`
	OutputTokens usageValue `json:"output_tokens"`
	TotalTokens  usageValue `json:"total_tokens"`
}

// llmResponse mirrors one completed restricted LLM response.
// llmResponse 用于镜像一项已完成的受限 LLM 响应。
type llmResponse struct {
	Route            routeSnapshot   `json:"route"`
	OutputText       string          `json:"output_text"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	Usage            requestUsage    `json:"usage"`
}

// llmStartResult contains the stable host execution identifier.
// llmStartResult 用于保存稳定的宿主执行标识。
type llmStartResult struct {
	ExecutionID string `json:"execution_id"`
}

// llmExecutionSnapshot mirrors one caller-owned execution status.
// llmExecutionSnapshot 用于镜像一项调用方所有的执行状态。
type llmExecutionSnapshot struct {
	ExecutionID string          `json:"execution_id"`
	Phase       string          `json:"phase"`
	Response    *llmResponse    `json:"response"`
	Error       json.RawMessage `json:"error"`
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
	return &Client{
		config: config,
		http: &http.Client{
			Timeout: config.StartupTimeout,
		},
	}, nil
}

// Generate starts one host-owned LLM execution, polls the same execution to terminal state, and cancels it when the VMM context ends.
// Generate 用于启动一项宿主管理的 LLM 执行、轮询同一执行直到终态，并在 VMM 上下文结束时取消执行。
func (c *Client) Generate(ctx context.Context, request appports.LLMRequest) (appports.LLMResponse, error) {
	requestID, err := newRequestID("vmm-llm")
	if err != nil {
		return appports.LLMResponse{}, err
	}
	structuredOutput := map[string]any{"mode": "none"}
	if request.ResponseFormat == appports.LLMResponseFormatJSON {
		if request.StructuredOutput == nil {
			return appports.LLMResponse{}, errors.New("Vulcan inference JSON request requires an explicit structured-output schema")
		}
		var schema map[string]any
		if err := json.Unmarshal(request.StructuredOutput.Schema, &schema); err != nil {
			return appports.LLMResponse{}, fmt.Errorf("decode Vulcan inference structured-output schema: %w", err)
		}
		if strings.TrimSpace(request.StructuredOutput.Name) == "" || len(schema) == 0 {
			return appports.LLMResponse{}, errors.New("Vulcan inference structured-output schema name and object must not be empty")
		}
		structuredOutput = map[string]any{
			"mode":        "json_schema",
			"name":        strings.TrimSpace(request.StructuredOutput.Name),
			"description": strings.TrimSpace(request.StructuredOutput.Description),
			"schema":      schema,
			"strict":      request.StructuredOutput.Strict,
		}
	}
	body := map[string]any{
		"contract_version":    inferenceContractVersion,
		"request_id":          requestID,
		"consumer_profile_id": c.config.ConsumerProfileID,
		"purpose_id":          string(request.RouteSelectionLevel),
		"messages": []any{
			map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": request.SystemPrompt}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": request.UserPrompt}}},
		},
		"structured_output": structuredOutput,
		"prompt_cache":      map[string]any{"mode": "provider_default"},
		"reasoning_output":  "hidden",
	}
	var started llmStartResult
	if err := c.call(ctx, http.MethodPost, "inference/v1/llm/start", body, &started); err != nil {
		return appports.LLMResponse{}, err
	}
	if strings.TrimSpace(started.ExecutionID) == "" {
		return appports.LLMResponse{}, errors.New("Vulcan inference returned an empty LLM execution id")
	}
	return c.waitForLLM(ctx, started.ExecutionID)
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

// waitForLLM polls one immutable execution identity and never creates a replacement execution after dispatch.
// waitForLLM 用于轮询一个不可变执行身份，并保证分派后绝不创建替代执行。
func (c *Client) waitForLLM(ctx context.Context, executionID string) (appports.LLMResponse, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var snapshot llmExecutionSnapshot
		path := "inference/v1/llm/" + url.PathEscape(executionID)
		if err := c.call(ctx, http.MethodGet, path, nil, &snapshot); err != nil {
			if ctx.Err() != nil {
				c.cancelLLM(executionID)
			}
			return appports.LLMResponse{}, err
		}
		switch snapshot.Phase {
		case "completed":
			if snapshot.Response == nil {
				return appports.LLMResponse{}, errors.New("Vulcan inference completed without an LLM response")
			}
			content := snapshot.Response.OutputText
			if len(snapshot.Response.StructuredOutput) > 0 && string(snapshot.Response.StructuredOutput) != "null" {
				content = string(snapshot.Response.StructuredOutput)
			}
			return appports.LLMResponse{
				Content: content,
				Model:   snapshot.Response.Route.ModelID,
				Usage: logicdomain.LLMUsage{
					PromptTokens:     knownUsageInt(snapshot.Response.Usage.InputTokens),
					CompletionTokens: knownUsageInt(snapshot.Response.Usage.OutputTokens),
					TotalTokens:      knownUsageInt(snapshot.Response.Usage.TotalTokens),
				},
			}, nil
		case "failed":
			return appports.LLMResponse{}, controlledExecutionError(snapshot.Error)
		case "cancelled":
			return appports.LLMResponse{}, context.Canceled
		case "running":
		default:
			return appports.LLMResponse{}, fmt.Errorf("Vulcan inference returned unknown LLM phase %q", snapshot.Phase)
		}
		select {
		case <-ctx.Done():
			c.cancelLLM(executionID)
			return appports.LLMResponse{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// cancelLLM best-effort cancels one already-dispatched execution without reusing the cancelled caller context.
// cancelLLM 用于尽力取消一项已分派执行，并避免复用已取消的调用方上下文。
func (c *Client) cancelLLM(executionID string) {
	cancelContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := "inference/v1/llm/" + url.PathEscape(executionID) + "/cancel"
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	_ = c.call(cancelContext, http.MethodPost, path, nil, &result)
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

// knownUsageInt narrows one known token count into the legacy VMM integer range.
// knownUsageInt 用于把一项已知 Token 数量收窄到旧版 VMM 整数范围。
func knownUsageInt(value usageValue) int {
	if value.State != "known" {
		return 0
	}
	maximum := uint64(^uint(0) >> 1)
	if value.Value > maximum {
		return int(maximum)
	}
	return int(value.Value)
}

// controlledExecutionError extracts only the host-controlled message from one terminal execution failure.
// controlledExecutionError 用于仅从一项终态执行失败中提取宿主控制的消息。
func controlledExecutionError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errors.New("Vulcan inference LLM execution failed")
	}
	var value struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	}
	if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value.Message) != "" {
		return fmt.Errorf("Vulcan inference LLM %s: %s", value.Category, value.Message)
	}
	return errors.New("Vulcan inference LLM execution failed with an invalid error payload")
}
