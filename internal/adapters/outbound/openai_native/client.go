// client.go implements the OpenAI-compatible outbound adapters.
// client.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
)

// Client holds the official OpenAI SDK client plus compatibility-mode flags for OpenAI-compatible endpoints.
// Client 用于持有官方 OpenAI SDK 客户端，以及 OpenAI-compatible 端点所需的兼容模式标记。
type Client struct {
	sdkClient *openai.Client
}

// NewClient configures the official SDK with trimmed endpoint credentials and a bounded connection pool.
// NewClient 用于使用清理后的端点凭据配置官方 SDK，并提供连接数有界的连接池。
func NewClient(endpoint, apiKey, organization, project string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = httpclient.SharedDefault()
	}
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	opts := []option.RequestOption{option.WithHTTPClient(httpClient)}
	if trimmed != "" {
		opts = append(opts, option.WithBaseURL(trimmed))
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		opts = append(opts, option.WithAPIKey(key))
		apiKey = key
	}
	if org := strings.TrimSpace(organization); org != "" {
		opts = append(opts, option.WithOrganization(org))
		organization = org
	}
	if proj := strings.TrimSpace(project); proj != "" {
		opts = append(opts, option.WithProject(proj))
		project = proj
	}
	sdkClient := openai.NewClient(opts...)
	return &Client{
		sdkClient: &sdkClient,
	}
}
