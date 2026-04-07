// client_test.go verifies SiliconFlow rerank request construction and response parsing without calling the live provider.
// client_test.go 用于在不调用真实 provider 的前提下，验证 SiliconFlow rerank 的请求构造和响应解析。
package siliconflow_rerank

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// TestClientRerankBuildsSiliconFlowRequest verifies the adapter sends the expected authorization header and JSON body to SiliconFlow.
// TestClientRerankBuildsSiliconFlowRequest 用于验证适配器会向 SiliconFlow 发送期望的鉴权头和 JSON 请求体。
func TestClientRerankBuildsSiliconFlowRequest(t *testing.T) {
	var capturedAuth string
	var capturedBody requestPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"trace-id","results":[{"index":1,"relevance_score":0.98},{"index":0,"relevance_score":0.54}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "BAAI/bge-reranker-v2-m3", 0, server.Client())
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
	if capturedBody.Model != "BAAI/bge-reranker-v2-m3" {
		t.Fatalf("model = %q", capturedBody.Model)
	}
	if capturedBody.Query != "什么是文本排序模型" {
		t.Fatalf("query = %q", capturedBody.Query)
	}
	if len(capturedBody.Documents) != 2 || capturedBody.Documents[0] != "第一条文档" || capturedBody.Documents[1] != "第二条文档" {
		t.Fatalf("documents = %#v", capturedBody.Documents)
	}
	if capturedBody.TopN != 2 || capturedBody.ReturnDocuments {
		t.Fatalf("payload flags = %#v", capturedBody)
	}
	if len(results) != 2 || results[0].ID != "doc-2" || results[0].Score != 0.98 || results[1].ID != "doc-1" {
		t.Fatalf("results = %#v", results)
	}
}

// TestClientRerankRejectsOutOfRangeIndex verifies provider bugs cannot silently map scores onto the wrong local document id.
// TestClientRerankRejectsOutOfRangeIndex 用于验证 provider 返回异常索引时不会悄悄把分数映射到错误的本地文档上。
func TestClientRerankRejectsOutOfRangeIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"index":7,"relevance_score":0.98}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "BAAI/bge-reranker-v2-m3", 0, server.Client())
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
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key", "BAAI/bge-reranker-v2-m3", 0, server.Client())
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
	if got := apiErr.Headers.Get("Retry-After"); got != "3" {
		t.Fatalf("retry-after header = %q", got)
	}
}
