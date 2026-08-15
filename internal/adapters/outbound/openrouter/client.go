// client.go implements the OpenRouter SDK-backed outbound adapter root shared by chat, embedding, and rerank clients.
// client.go 用于实现由 OpenRouter SDK 驱动的出站适配器根客户端，供 chat、embedding 与 rerank 客户端复用。
package openrouter

import (
	"net/http"
	"strings"
	"time"

	openroutersdk "github.com/OpenRouterTeam/go-sdk"
	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
)

const (
	// defaultEndpoint keeps OpenRouter SDK calls pinned to the public v1 API root when config omits an endpoint.
	// defaultEndpoint 用于在配置未显式提供 endpoint 时，将 OpenRouter SDK 调用固定到公开 v1 API 根地址。
	defaultEndpoint = "https://openrouter.ai/api/v1"

	// defaultAppReferer identifies VulcanMemoryMesh as the fixed OpenRouter traffic source for provider-side app attribution.
	// defaultAppReferer 用于将 VulcanMemoryMesh 固定标识为 OpenRouter 流量来源，满足 provider 侧应用归因要求。
	defaultAppReferer = "https://openvulcan.com"

	// defaultAppTitle names this application in OpenRouter attribution headers so request traffic is grouped consistently.
	// defaultAppTitle 用于在 OpenRouter 归因 header 中声明本应用名称，确保请求流量被稳定归类。
	defaultAppTitle = "VulcanMemoryMesh"

	// defaultAppCategories pins the OpenRouter app taxonomy categories used by the CLI, cloud agent, and IDE extension entry points.
	// defaultAppCategories 用于固定 CLI、云端 agent 与 IDE 扩展入口共享的 OpenRouter 应用分类标签。
	defaultAppCategories = "cli-agent,cloud-agent,ide-extension"
)

// Client holds one configured OpenRouter SDK instance plus the normalized API key used for local preflight validation.
// Client 用于持有一个配置完成的 OpenRouter SDK 实例，以及本地预检校验所需的规范化 API Key。
type Client struct {
	sdkClient *openroutersdk.OpenRouter
	apiKey    string
}

// NewClient creates one OpenRouter SDK client from the provider root endpoint, API key, optional operation timeout, and optional shared HTTP client.
// NewClient 用于根据 provider 根地址、API Key、可选操作超时和可选共享 HTTP 客户端创建一个 OpenRouter SDK 客户端。
//
// Parameters:
// 参数：
//   - endpoint: provider API root or one legacy operation-specific URL.
//   - endpoint：供应商 API 根地址或旧式具体操作 URL。
//   - apiKey: credential projected by the SDK security handler.
//   - apiKey：由 SDK 安全处理器投影的凭据。
//   - timeout: positive explicit operation ceiling; non-positive values preserve the caller context deadline without adding an SDK-global timeout.
//   - timeout：正数表示显式操作上限；非正值保留调用方 Context 截止时间且不增加 SDK 全局超时。
//   - httpClient: optional bounded shared transport client.
//   - httpClient：可选的有界共享 Transport 客户端。
//
// Returns:
// 返回值：
//   - *Client: configured OpenRouter SDK adapter root.
//   - *Client：配置完成的 OpenRouter SDK 适配器根客户端。
func NewClient(endpoint, apiKey string, timeout time.Duration, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpclient.SharedDefault()
	}
	trimmedKey := strings.TrimSpace(apiKey)
	opts := []openroutersdk.SDKOption{
		openroutersdk.WithClient(httpClient),
		openroutersdk.WithServerURL(normalizeEndpoint(endpoint)),
		openroutersdk.WithHTTPReferer(defaultAppReferer),
		openroutersdk.WithXTitle(defaultAppTitle),
	}
	if timeout > 0 {
		// Only an explicitly owned operation budget may add an SDK deadline; LLM and embedding calls inherit their stage-wide parent context.
		// 只有显式拥有的操作预算才能增加 SDK 截止时间；LLM 与向量调用继承其阶段级父 Context。
		opts = append(opts, openroutersdk.WithTimeout(timeout))
	}
	if trimmedKey != "" {
		opts = append(opts, openroutersdk.WithSecurity(trimmedKey))
	}
	return &Client{
		sdkClient: openroutersdk.New(opts...),
		apiKey:    trimmedKey,
	}
}

// normalizeEndpoint converts either an OpenRouter API root or a legacy operation-specific URL into the SDK server root expected by generated operations.
// normalizeEndpoint 用于把 OpenRouter API 根地址或旧式具体操作 URL 归一成生成式 SDK 操作所需的 server root。
func normalizeEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return defaultEndpoint
	}
	lower := strings.ToLower(trimmed)
	for _, suffix := range []string{"/chat/completions", "/embeddings", "/rerank"} {
		if strings.HasSuffix(lower, suffix) {
			return trimmed[:len(trimmed)-len(suffix)]
		}
	}
	return trimmed
}
