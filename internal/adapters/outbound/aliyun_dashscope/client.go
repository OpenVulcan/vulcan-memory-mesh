package aliyun_dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	endpoint   string
	apiKey     string
	httpClient *http.Client
}

func NewClient(endpoint, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil { httpClient = &http.Client{Timeout: 15 * time.Second} }
	return &Client{endpoint: strings.TrimSpace(endpoint), apiKey: strings.TrimSpace(apiKey), httpClient: httpClient}
}

func (c *Client) PostJSON(ctx context.Context, endpoint string, reqBody any, out any) error {
	body, err := json.Marshal(reqBody)
	if err != nil { return fmt.Errorf("marshal request: %w", err) }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil { return fmt.Errorf("build request: %w", err) }
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" { req.Header.Set("Authorization", "Bearer "+c.apiKey) }
	resp, err := c.httpClient.Do(req)
	if err != nil { return fmt.Errorf("do request: %w", err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil { return fmt.Errorf("read response: %w", err) }
	if resp.StatusCode >= 300 { return fmt.Errorf("dashscope http status %d: %s", resp.StatusCode, string(raw)) }
	if out == nil { return nil }
	if err := json.Unmarshal(raw, out); err != nil { return fmt.Errorf("decode response: %w", err) }
	return nil
}
