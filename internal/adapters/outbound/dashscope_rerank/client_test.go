// client_test.go verifies DashScope rerank request construction and response parsing without calling the live provider.
// client_test.go 用于在不调用真实 provider 的前提下，验证 DashScope rerank 的请求构造和响应解析。
package dashscope_rerank

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/httpclient"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// TestNewClientUsesSharedBoundedTransport verifies default DashScope instances cannot multiply connections or regain an HTTP-wide request timeout.
// TestNewClientUsesSharedBoundedTransport 用于验证默认 DashScope 实例不会放大连接数量或重新获得 HTTP 全局请求超时。
func TestNewClientUsesSharedBoundedTransport(t *testing.T) {
	client := NewClient("", "key", "model", 0, nil)
	if client.httpClient != httpclient.SharedDefault() {
		t.Fatal("DashScope rerank must reuse the process-wide bounded HTTP client")
	}
	if client.httpClient.Timeout != 0 {
		t.Fatalf("DashScope shared HTTP timeout = %s, want 0", client.httpClient.Timeout)
	}
	if client.timeout != defaultTimeout {
		t.Fatalf("DashScope operation timeout = %s, want %s", client.timeout, defaultTimeout)
	}
	if defaultTimeout != 8*time.Second {
		t.Fatalf("DashScope default timeout = %s, want 8s", defaultTimeout)
	}
}

// TestClientRerankBuildsDashScopeRequest verifies the adapter sends the expected authorization header and JSON body to DashScope.
// TestClientRerankBuildsDashScopeRequest 用于验证适配器会向 DashScope 发送期望的鉴权头和 JSON 请求体。
func TestClientRerankBuildsDashScopeRequest(t *testing.T) {
	var capturedAuth string
	var capturedBody requestPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"output":{"results":[{"index":1,"relevance_score":0.98},{"index":0,"relevance_score":0.54}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "qwen3-vl-rerank", 0, server.Client())
	results, err := client.Rerank(context.Background(), "什么是文本排序模型", []appports.RerankerDocument{
		{ID: "doc-1", Text: "第一条文档"},
		{ID: "doc-2", Text: "第二条文档"},
	}, 2)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}

	if capturedAuth != "Bearer test-key" {
		t.Fatalf("authorization header = %q", capturedAuth)
	}
	if capturedBody.Model != "qwen3-vl-rerank" {
		t.Fatalf("model = %q", capturedBody.Model)
	}
	if capturedBody.Input.Query != "什么是文本排序模型" {
		t.Fatalf("query = %q", capturedBody.Input.Query)
	}
	if len(capturedBody.Input.Documents) != 2 || capturedBody.Input.Documents[0] != "第一条文档" || capturedBody.Input.Documents[1] != "第二条文档" {
		t.Fatalf("documents = %#v", capturedBody.Input.Documents)
	}
	if !capturedBody.Parameters.ReturnDocuments || capturedBody.Parameters.TopN != 2 {
		t.Fatalf("parameters = %#v", capturedBody.Parameters)
	}
	if len(results) != 2 || results[0].ID != "doc-2" || results[0].Score != 0.98 || results[1].ID != "doc-1" {
		t.Fatalf("results = %#v", results)
	}
}

// TestClientRerankRejectsOutOfRangeIndex verifies provider bugs cannot silently map scores onto the wrong local document id.
// TestClientRerankRejectsOutOfRangeIndex 用于验证 provider 返回异常索引时不会悄悄把分数映射到错误的本地文档上。
func TestClientRerankRejectsOutOfRangeIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"output":{"results":[{"index":7,"relevance_score":0.98}]}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "qwen3-vl-rerank", 0, server.Client())
	_, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{
		{ID: "doc-1", Text: "第一条文档"},
	}, 1)
	if err == nil {
		t.Fatal("expected out-of-range index error")
	}
}

// TestClientRerankReturnsStructuredAPIError verifies non-2xx provider responses preserve status and headers so upper failover layers can classify cooldown behavior precisely.
// TestClientRerankReturnsStructuredAPIError 用于验证非 2xx provider 响应会保留状态码和响应头，便于上层容灾精确分类冷却行为。
func TestClientRerankReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After-Ms", "1500")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "qwen3-vl-rerank", 0, server.Client())
	_, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{
		{ID: "doc-1", Text: "第一条文档"},
	}, 1)
	if err == nil {
		t.Fatal("expected structured api error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status code = %d", apiErr.StatusCode)
	}
	if got := apiErr.Headers.Get("Retry-After-Ms"); got != "1500" {
		t.Fatalf("retry-after-ms header = %q", got)
	}
}
