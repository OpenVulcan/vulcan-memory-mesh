// client.go implements the OpenRouter SDK-backed outbound adapter root shared by chat, embedding, and rerank clients.
// client.go 用于实现由 OpenRouter SDK 驱动的出站适配器根客户端，供 chat、embedding 与 rerank 客户端复用。
package openrouter

import (
	"net/http"
	"strings"
	"time"

	openroutersdk "github.com/OpenRouterTeam/go-sdk"
)

const (
	// defaultEndpoint keeps OpenRouter SDK calls pinned to the public v1 API root when config omits an endpoint.
	// defaultEndpoint 用于在配置未显式提供 endpoint 时，将 OpenRouter SDK 调用固定到公开 v1 API 根地址。
	defaultEndpoint = "https://openrouter.ai/api/v1"

	// defaultTimeout bounds non-rerank OpenRouter calls so startup probes and background processors cannot hang forever on upstream stalls.
	// defaultTimeout 用于限制非 rerank OpenRouter 调用耗时，避免启动探测和后台处理流程在上游卡顿时无限挂起。
	defaultTimeout = 20 * time.Second

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

// NewClient creates one OpenRouter SDK client from the provider root endpoint, API key, timeout, and optional shared HTTP client.
// NewClient 用于根据 provider 根地址、API Key、超时时间和可选共享 HTTP 客户端创建一个 OpenRouter SDK 客户端。
func NewClient(endpoint, apiKey string, timeout time.Duration, httpClient *http.Client) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	trimmedKey := strings.TrimSpace(apiKey)
	opts := []openroutersdk.SDKOption{
		openroutersdk.WithClient(httpClient),
		openroutersdk.WithTimeout(timeout),
		openroutersdk.WithServerURL(normalizeEndpoint(endpoint)),
		openroutersdk.WithHTTPReferer(defaultAppReferer),
		openroutersdk.WithXTitle(defaultAppTitle),
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
