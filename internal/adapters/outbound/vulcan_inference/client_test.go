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

// TestClientGeneratesThroughStandardResponses verifies request, output, model, and usage mapping without a private envelope.
// TestClientGeneratesThroughStandardResponses 验证不使用私有信封时的请求、输出、模型与用量映射。
func TestClientGeneratesThroughStandardResponses(t *testing.T) {
	var requestCount int
	// correlationRequestID records the exact VMM-owned identifier observed by the loopback inference endpoint.
	// correlationRequestID 记录回环推理端点实际观察到的 VMM 自有标识。
	var correlationRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPost && request.URL.Path == "/v1/responses" {
			requestCount++
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode Responses request: %v", err)
			}
			if body["model"] != "precheck_l1" || body["instructions"] != "system" || body["input"] != "user" {
				t.Fatalf("Responses body = %#v", body)
			}
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != "none" {
				t.Fatalf("Responses reasoning = %#v, want effort none", body["reasoning"])
			}
			clientMetadata, ok := body["client_metadata"].(map[string]any)
			requestID, requestIDOK := clientMetadata["vmm_request_id"].(string)
			if !ok || !requestIDOK || !strings.HasPrefix(requestID, "vmm-llm-") {
				t.Fatalf("Responses client metadata = %#v", body["client_metadata"])
			}
			correlationRequestID = requestID
			_ = json.NewEncoder(response).Encode(map[string]any{
				"id": "resp_one", "object": "response", "status": "completed", "model": "managed-model",
				"output": []any{map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": "managed output"}},
				}},
				"usage": map[string]any{
					"input_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 6},
					"output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 18,
				},
				"error": nil,
			})
			return
		}
		http.NotFound(response, request)
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
	if requestCount != 1 || result.Content != "managed output" || result.Model != "managed-model" || result.RequestID != correlationRequestID {
		t.Fatalf("Generate() response = %#v, request count = %d", result, requestCount)
	}
	if result.Usage.PromptTokens != 11 || result.Usage.CompletionTokens != 7 || result.Usage.TotalTokens != 18 || result.Usage.CachedInputTokens != 6 || result.Usage.ReasoningTokens != 0 {
		t.Fatalf("Generate() usage = %#v", result.Usage)
	}
}

// TestClientRejectsManagedPhysicalModelDrift verifies a provider response cannot silently replace the frozen managed model identity.
// TestClientRejectsManagedPhysicalModelDrift 用于验证供应商响应不能静默替换冻结的托管模型身份。
func TestClientRejectsManagedPhysicalModelDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id": "resp_drift", "object": "response", "status": "completed", "model": "unexpected-model",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "managed output"}},
			}},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	result, err := client.Generate(context.Background(), appports.LLMRequest{
		Model:               "configured-model",
		SystemPrompt:        "system",
		UserPrompt:          "user",
		ResponseFormat:      appports.LLMResponseFormatText,
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPreCheckL1,
	})
	if err == nil || !strings.Contains(err.Error(), "drifted from configured-model to unexpected-model") {
		t.Fatalf("Generate() error = %v, want model drift failure", err)
	}
	if result.Model != "unexpected-model" || !strings.HasPrefix(result.RequestID, "vmm-llm-") {
		t.Fatalf("drift failure lost response identity: %#v", result)
	}
}

// TestClientLoadsAuthoritativePurposeRoutes verifies managed processor identities come from the protected capability snapshot instead of standalone defaults.
// TestClientLoadsAuthoritativePurposeRoutes 用于验证托管处理器身份来自受保护能力快照，而不是独立模式默认值。
func TestClientLoadsAuthoritativePurposeRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/inference/v1/capabilities/vmm" {
			http.NotFound(response, request)
			return
		}
		writeInferenceResult(t, response, map[string]any{
			"contract_version":    1,
			"consumer_profile_id": "vmm",
			"operations": []any{
				map[string]any{"operation": "conversation_respond", "purpose_id": "precheck_l1", "available": true, "route": map[string]any{
					"provider_id": "deepseek", "service_entry_id": "official-api", "connection_id": "deepseek-account", "model_id": "deepseek-v4-flash", "protocol_binding_id": "openai.responses",
				}},
				map[string]any{"operation": "conversation_respond", "purpose_id": "postaction_l1", "available": false, "route": map[string]any{"model_id": "disabled-model"}},
				map[string]any{"operation": "embedding", "purpose_id": "", "available": true, "route": map[string]any{"model_id": "embedding-model"}},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	routes, err := client.PurposeRoutes(context.Background())
	if err != nil {
		t.Fatalf("PurposeRoutes() error = %v", err)
	}
	want := PurposeRoute{
		ProviderID:        "deepseek",
		ServiceEntryID:    "official-api",
		ConnectionID:      "deepseek-account",
		ModelID:           "deepseek-v4-flash",
		ProtocolBindingID: "openai.responses",
	}
	if len(routes) != 1 || routes["precheck_l1"] != want {
		t.Fatalf("PurposeRoutes() = %#v", routes)
	}
}

// TestClientRejectsIncompletePurposeRouteIdentity verifies managed startup cannot accept a model-only capability snapshot.
// TestClientRejectsIncompletePurposeRouteIdentity 用于验证托管启动不能接受只有模型名称的能力快照。
func TestClientRejectsIncompletePurposeRouteIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/inference/v1/capabilities/vmm" {
			http.NotFound(response, request)
			return
		}
		writeInferenceResult(t, response, map[string]any{
			"contract_version":    1,
			"consumer_profile_id": "vmm",
			"operations": []any{
				map[string]any{"operation": "conversation_respond", "purpose_id": "precheck_l1", "available": true, "route": map[string]any{"model_id": "deepseek-v4-flash"}},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	if _, err := client.PurposeRoutes(context.Background()); err == nil || !strings.Contains(err.Error(), "incomplete physical route identity") {
		t.Fatalf("PurposeRoutes() error = %v, want incomplete route identity", err)
	}
}

// TestClientCancelsResponsesRequestWithCallerContext verifies context cancellation closes the standard HTTP request.
// TestClientCancelsResponsesRequestWithCallerContext 验证上下文取消会关闭标准 HTTP 请求。
func TestClientCancelsResponsesRequestWithCallerContext(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	requestCancelled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPost && request.URL.Path == "/v1/responses" {
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode structured Responses request: %v", err)
			}
			text, ok := body["text"].(map[string]any)
			format, formatOK := text["format"].(map[string]any)
			if !ok || !formatOK || format["type"] != "json_schema" || format["name"] != "cancellation_test" {
				t.Fatalf("text = %#v", body["text"])
			}
			requestSeen <- struct{}{}
			<-request.Context().Done()
			requestCancelled <- struct{}{}
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, "token-one")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Generate(ctx, appports.LLMRequest{
			SystemPrompt:   "system",
			UserPrompt:     "user",
			ResponseFormat: appports.LLMResponseFormatJSON,
			StructuredOutput: &appports.LLMStructuredOutput{
				Name:   "cancellation_test",
				Schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Strict: true,
			},
			RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL1,
		})
		result <- err
	}()

	select {
	case <-requestSeen:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the Responses request")
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
	case <-requestCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the Responses request cancellation")
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

// TestClientRejectsNonV1InferenceServiceAPIVersion verifies that the unpublished v1 discovery contract rejects every other version.
// TestClientRejectsNonV1InferenceServiceAPIVersion 验证未发布 v1 发现契约会拒绝其他所有版本。
func TestClientRejectsNonV1InferenceServiceAPIVersion(t *testing.T) {
	t.Helper()
	client := &Client{}
	for _, apiVersion := range []int{2, 3} {
		err := client.validateDiscovery(discoverySnapshot{
			APIVersion: apiVersion,
			Status:     "ready",
		}, time.Now().UnixMilli())
		if err == nil {
			t.Fatalf("validateDiscovery() accepted non-v1 inference service API %d", apiVersion)
		}
		if got, want := err.Error(), "Vulcan inference discovery is not a supported ready document"; got != want {
			t.Fatalf("validateDiscovery(%d) error = %q, want %q", apiVersion, got, want)
		}
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
