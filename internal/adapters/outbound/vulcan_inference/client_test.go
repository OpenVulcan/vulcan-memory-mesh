// client_test.go verifies the closed managed inference transport and semantic mappings.
// client_test.go 用于验证封闭托管推理传输与语义映射。
package vulcan_inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// TestClientMapsPartialEmbeddingAndRerank verifies strong partial-drop coverage and stable candidate identities.
// TestClientMapsPartialEmbeddingAndRerank 用于验证强类型部分丢弃覆盖与稳定候选身份。
func TestClientMapsPartialEmbeddingAndRerank(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token-one" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/inference/v1/embeddings":
			// Verify VMM delegates provider-specific embedding task selection to the configured route contract.
			// 验证 VMM 将供应商特定的向量任务选择交给已配置路由契约。
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode embedding request: %v", err)
			}
			if body["task"] != "provider_default" {
				t.Fatalf("embedding task = %#v", body["task"])
			}
			writeInferenceResult(t, response, map[string]any{
				"results": []any{
					map[string]any{
						"input_id": "0", "input_index": 0,
						"embedding": map[string]any{"type": "dense", "values": []float32{1, 2}},
					},
				},
				"dropped": []any{
					map[string]any{
						"input_id": "1", "input_index": 1, "reason_code": "invalid_input",
						"controlled_message": "provider rejected this item",
					},
				},
			})
		case "/inference/v1/rerank":
			writeInferenceResult(t, response, map[string]any{
				"results": []any{
					map[string]any{
						"candidate_id": "b", "original_index": 1, "rank": 0, "score": 0.9,
					},
				},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	embedded, err := client.Embed(context.Background(), appports.EmbeddingRequest{
		Texts:                    []string{"accepted", "rejected"},
		AllowPartialInvalidTexts: true,
		Dimension:                2,
	})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(embedded.Vectors) != 1 || embedded.ResultIndices[0] != 0 ||
		len(embedded.Dropped) != 1 || embedded.Dropped[0].Index != 1 {
		t.Fatalf("Embed() response = %#v", embedded)
	}

	reranked, err := client.Rerank(
		context.Background(),
		"query",
		[]appports.RerankerDocument{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}},
		1,
	)
	if err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}
	if len(reranked) != 1 || reranked[0].ID != "b" || reranked[0].Score != 0.9 {
		t.Fatalf("Rerank() response = %#v", reranked)
	}
}

// TestClientGeneratesThroughOneStableExecution verifies start, poll, output, route, and usage mapping without redispatch.
// TestClientGeneratesThroughOneStableExecution 用于验证启动、轮询、输出、路由与用量映射，且不会重复分派。
func TestClientGeneratesThroughOneStableExecution(t *testing.T) {
	var startCount int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/inference/v1/llm/start":
			startCount++
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode LLM start request: %v", err)
			}
			if body["purpose_id"] != "precheck_l1" || body["reasoning_output"] != "hidden" {
				t.Fatalf("LLM start body = %#v", body)
			}
			writeInferenceResult(t, response, map[string]any{"execution_id": "execution-one"})
		case request.Method == http.MethodGet && request.URL.Path == "/inference/v1/llm/execution-one":
			writeInferenceResult(t, response, map[string]any{
				"execution_id": "execution-one",
				"phase":        "completed",
				"response": map[string]any{
					"route":             map[string]any{"model_id": "managed-model"},
					"output_text":       "managed output",
					"structured_output": nil,
					"usage": map[string]any{
						"input_tokens":  map[string]any{"state": "known", "value": 11},
						"output_tokens": map[string]any{"state": "known", "value": 7},
						"total_tokens":  map[string]any{"state": "known", "value": 18},
					},
				},
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	result, err := client.Generate(context.Background(), appports.LLMRequest{
		SystemPrompt:        "system",
		UserPrompt:          "user",
		ResponseFormat:      appports.LLMResponseFormatText,
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPreCheckL1,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if startCount != 1 || result.Content != "managed output" || result.Model != "managed-model" {
		t.Fatalf("Generate() response = %#v, start count = %d", result, startCount)
	}
	if result.Usage.PromptTokens != 11 || result.Usage.CompletionTokens != 7 || result.Usage.TotalTokens != 18 {
		t.Fatalf("Generate() usage = %#v", result.Usage)
	}
}

// TestClientCancelsDispatchedExecutionWithCallerContext verifies cancellation targets the original execution id.
// TestClientCancelsDispatchedExecutionWithCallerContext 用于验证取消操作会指向原始执行标识。
func TestClientCancelsDispatchedExecutionWithCallerContext(t *testing.T) {
	firstPoll := make(chan struct{}, 1)
	cancelSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/inference/v1/llm/start":
			writeInferenceResult(t, response, map[string]any{"execution_id": "execution-cancel"})
		case request.Method == http.MethodGet && request.URL.Path == "/inference/v1/llm/execution-cancel":
			firstPoll <- struct{}{}
			writeInferenceResult(t, response, map[string]any{
				"execution_id": "execution-cancel",
				"phase":        "running",
			})
		case request.Method == http.MethodPost && request.URL.Path == "/inference/v1/llm/execution-cancel/cancel":
			cancelSeen <- struct{}{}
			writeInferenceResult(t, response, map[string]any{"cancelled": true})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Generate(ctx, appports.LLMRequest{
			SystemPrompt:        "system",
			UserPrompt:          "user",
			ResponseFormat:      appports.LLMResponseFormatJSON,
			RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL1,
		})
		result <- err
	}()

	select {
	case <-firstPoll:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first LLM execution poll")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate() error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled Generate()")
	}
	select {
	case <-cancelSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the exact execution cancellation")
	}
}

// TestClientRejectsMismatchedRerankIdentity verifies candidate ids cannot conceal incorrect source indexes or ranks.
// TestClientRejectsMismatchedRerankIdentity 用于验证候选标识不能掩盖错误的来源索引或排名。
func TestClientRejectsMismatchedRerankIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		writeInferenceResult(t, response, map[string]any{
			"results": []any{
				map[string]any{
					"candidate_id": "b", "original_index": 0, "rank": 0, "score": 0.9,
				},
			},
		})
	}))
	defer server.Close()

	client, _ := newTestClientWithDiscovery(t, server.URL, "token-one")
	_, err := client.Rerank(
		context.Background(),
		"query",
		[]appports.RerankerDocument{{ID: "a", Text: "A"}, {ID: "b", Text: "B"}},
		1,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid rerank candidate") {
		t.Fatalf("Rerank() error = %v", err)
	}
}

// TestClientReloadsRotatedDiscoveryGrant verifies token rotation is consumed without restarting VMM.
// TestClientReloadsRotatedDiscoveryGrant 用于验证 VMM 无需重启即可消费轮换后的发现授权。
func TestClientReloadsRotatedDiscoveryGrant(t *testing.T) {
	var mutex sync.Mutex
	seenTokens := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		seenTokens = append(seenTokens, request.Header.Get("Authorization"))
		mutex.Unlock()
		writeInferenceResult(t, response, map[string]any{"results": []any{}})
	}))
	defer server.Close()

	client, discoveryPath := newTestClientWithDiscovery(t, server.URL, "token-one")
	if _, err := client.Rerank(context.Background(), "query", nil, 0); err != nil {
		t.Fatalf("first Rerank() error = %v", err)
	}
	writeDiscovery(t, discoveryPath, server.URL, "token-two")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(discoveryPath, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Rerank(context.Background(), "query", nil, 0); err != nil {
		t.Fatalf("second Rerank() error = %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(seenTokens) != 2 ||
		seenTokens[0] != "Bearer token-one" ||
		seenTokens[1] != "Bearer token-two" {
		t.Fatalf("seen authorization values = %#v", seenTokens)
	}
}

// TestClientRefreshesGrantAfterPreDispatchUnauthorized verifies same-timestamp rotations are retried exactly once.
// TestClientRefreshesGrantAfterPreDispatchUnauthorized 用于验证同时间戳授权轮换会精确重试一次。
func TestClientRefreshesGrantAfterPreDispatchUnauthorized(t *testing.T) {
	var mutex sync.Mutex
	seenTokens := make([]string, 0, 2)
	var discoveryPath string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		seenTokens = append(seenTokens, request.Header.Get("Authorization"))
		attempt := len(seenTokens)
		mutex.Unlock()
		if attempt == 1 {
			info, err := os.Stat(discoveryPath)
			if err != nil {
				t.Fatalf("stat discovery: %v", err)
			}
			writeDiscovery(t, discoveryPath, "http://"+request.Host, "token-two")
			if err := os.Chtimes(discoveryPath, info.ModTime(), info.ModTime()); err != nil {
				t.Fatalf("preserve discovery timestamp: %v", err)
			}
			http.Error(response, "rotated", http.StatusUnauthorized)
			return
		}
		writeInferenceResult(t, response, map[string]any{"results": []any{}})
	}))
	defer server.Close()

	client, path := newTestClientWithDiscovery(t, server.URL, "token-one")
	discoveryPath = path
	if _, err := client.Rerank(context.Background(), "query", nil, 0); err != nil {
		t.Fatalf("Rerank() error = %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(seenTokens) != 2 ||
		seenTokens[0] != "Bearer token-one" ||
		seenTokens[1] != "Bearer token-two" {
		t.Fatalf("seen authorization values = %#v", seenTokens)
	}
}

// newTestClient creates one client with an isolated protected discovery file.
// newTestClient 用于使用隔离的受保护发现文件创建客户端。
func newTestClient(t *testing.T, baseURL string, token string) *Client {
	t.Helper()
	client, _ := newTestClientWithDiscovery(t, baseURL, token)
	return client
}

// newTestClientWithDiscovery creates one client and returns its writable discovery path for rotation tests.
// newTestClientWithDiscovery 用于创建客户端，并返回轮换测试可写的发现文件路径。
func newTestClientWithDiscovery(t *testing.T, baseURL string, token string) (*Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inference.json")
	writeDiscovery(t, path, baseURL, token)
	client, err := New(Config{
		DiscoveryFile:     path,
		ExpectedProcessID: os.Getpid(),
		ExpectedStartedAt: 12345,
		ExpectedCallerID:  "vmm-local",
		ConsumerProfileID: "vmm",
		StartupTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, path
}

// writeDiscovery writes one exact versioned loopback inference grant.
// writeDiscovery 用于写入一份精确版本化回环推理授权。
func writeDiscovery(t *testing.T, path string, baseURL string, token string) {
	t.Helper()
	body, err := json.Marshal(discoverySnapshot{
		APIVersion:        inferenceServiceAPIVersion,
		Status:            "ready",
		BaseURL:           baseURL,
		ProcessID:         os.Getpid(),
		CallerID:          "vmm-local",
		ConsumerProfileID: "vmm",
		AccessToken:       token,
		ExpiresAtUnixMS:   time.Now().Add(time.Hour).UnixMilli(),
		StartedAtUnixMS:   "12345",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeInferenceResult writes one exact versioned success envelope.
// writeInferenceResult 用于写入一份精确版本化成功封装。
func writeInferenceResult(t *testing.T, response http.ResponseWriter, result any) {
	t.Helper()
	if err := json.NewEncoder(response).Encode(map[string]any{
		"api_version": inferenceServiceAPIVersion,
		"result":      result,
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
