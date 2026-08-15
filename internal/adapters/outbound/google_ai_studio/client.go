// client.go implements the Google AI Studio native outbound adapters.
// client.go 用于实现 Google AI Studio 原生出站适配器。
package google_ai_studio

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"google.golang.org/genai"
)

// Client keeps the Google GenAI SDK client plus the fixed Gemini API connection settings shared by LLM and embedding adapters.
// Client 用于保存 Google GenAI SDK 客户端，以及 LLM 与 embedding 适配器共享的固定 Gemini API 连接配置。
type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client

	mu      sync.Mutex
	sdk     *genai.Client
	initErr error
}

// NewClient creates one lazy Google AI Studio client wrapper so fixed-model key-failover can build per-key adapters without dialing the SDK upfront.
// NewClient 用于创建一个懒初始化的 Google AI Studio 客户端包装器，让固定模型 key-failover 可以先构建每个 key 的适配器，而不必在启动时立即初始化 SDK。
func NewClient(endpoint, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpclient.SharedDefault()
	}
	return &Client{
		endpoint:   strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

// SDK returns the initialized Google GenAI SDK client and caches the result so repeated requests reuse one process-local HTTP client and backend configuration.
// SDK 用于返回已经初始化的 Google GenAI SDK 客户端，并缓存结果，让后续请求复用同一份进程内 HTTP 客户端与后端配置。
func (c *Client) SDK() (*genai.Client, error) {
	if c == nil {
		return nil, fmt.Errorf("google ai studio client is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sdk != nil {
		return c.sdk, nil
	}
	if c.initErr != nil {
		return nil, c.initErr
	}
	cfg := &genai.ClientConfig{
		APIKey:     c.apiKey,
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: c.httpClient,
	}
	if c.endpoint != "" {
		cfg.HTTPOptions = genai.HTTPOptions{BaseURL: c.endpoint}
	}
	sdk, err := genai.NewClient(context.Background(), cfg)
	if err != nil {
		c.initErr = err
		return nil, err
	}
	c.sdk = sdk
	return c.sdk, nil
}

// requestHTTPOptionsFromContext forwards trace identifiers into provider requests so cross-system diagnostics remain consistent with the existing OpenAI-compatible adapters.
// requestHTTPOptionsFromContext 用于把 trace 标识透传给 provider 请求，保证跨系统诊断能力与现有 OpenAI-compatible 适配器保持一致。
func requestHTTPOptionsFromContext(ctx context.Context) *genai.HTTPOptions {
	traceID := strings.TrimSpace(trace.IDFromContext(ctx))
	if traceID == "" {
		return nil
	}
	return &genai.HTTPOptions{
		Headers: http.Header{
			"X-Trace-ID":          []string{traceID},
			"X-Client-Request-Id": []string{traceID},
		},
	}
}
