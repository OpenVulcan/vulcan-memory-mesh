package openai_native

import (
	"context"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/openvulcan/vmm/internal/platform/trace"
)

type Client struct {
	sdk           openai.Client
	endpoint      string
	compatibleAPI bool
}

func NewClient(endpoint, apiKey, organization, project string, httpClient *http.Client) *Client {
	opts := make([]option.RequestOption, 0, 5)
	if strings.TrimSpace(apiKey) != "" {
		opts = append(opts, option.WithAPIKey(strings.TrimSpace(apiKey)))
	}
	if strings.TrimSpace(endpoint) != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimSpace(endpoint)))
	}
	if strings.TrimSpace(organization) != "" {
		opts = append(opts, option.WithOrganization(strings.TrimSpace(organization)))
	}
	if strings.TrimSpace(project) != "" {
		opts = append(opts, option.WithProject(strings.TrimSpace(project)))
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	trimmedEndpoint := strings.TrimSpace(endpoint)
	return &Client{
		sdk:           openai.NewClient(opts...),
		endpoint:      trimmedEndpoint,
		compatibleAPI: strings.Contains(strings.ToLower(trimmedEndpoint), "compatible-mode"),
	}
}

func (c *Client) requestOptions(ctx context.Context) []option.RequestOption {
	if c == nil {
		return nil
	}
	traceID := strings.TrimSpace(trace.IDFromContext(ctx))
	if traceID == "" {
		return nil
	}
	return []option.RequestOption{
		option.WithHeader("X-Trace-ID", traceID),
		option.WithHeader("X-Client-Request-Id", traceID),
	}
}

func (c *Client) shouldDisableThinking() bool {
	return c != nil && c.compatibleAPI
}
