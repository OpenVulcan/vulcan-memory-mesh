// client_test.go verifies the OpenRouter SDK-backed adapters against local HTTP fixtures without calling the real provider.
// client_test.go 用于通过本地 HTTP 夹具验证基于 OpenRouter SDK 的适配器，而不访问真实 provider。
package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// assertOpenRouterDefaultHeaders verifies every OpenRouter SDK request carries the fixed app attribution headers required by the provider taxonomy.
// assertOpenRouterDefaultHeaders 用于验证每个 OpenRouter SDK 请求都携带 provider 分类协议要求的固定应用归因 header。
func assertOpenRouterDefaultHeaders(t *testing.T, r *http.Request) bool {
	t.Helper()
	ok := true
	expected := map[string]string{
		"HTTP-Referer":            defaultAppReferer,
		"X-Title":                 defaultAppTitle,
		"X-OpenRouter-Title":      defaultAppTitle,
		"X-OpenRouter-Categories": defaultAppCategories,
	}
	for name, want := range expected {
		if got := r.Header.Get(name); got != want {
			t.Errorf("%s header = %q, want %q", name, got, want)
			ok = false
		}
	}
	return ok
}

// TestLLMClientGenerateUsesOpenRouterChatEndpoint verifies chat requests are sent through the OpenRouter SDK endpoint and mapped back into the internal LLM response.
// TestLLMClientGenerateUsesOpenRouterChatEndpoint 用于验证 chat 请求会通过 OpenRouter SDK 端点发出，并映射回内部 LLM 响应。
func TestLLMClientGenerateUsesOpenRouterChatEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("chat path = %q", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if got, want := r.Header.Get("Authorization"), "Bearer test-key"; got != want {
			t.Errorf("authorization header = %q, want %q", got, want)
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		if !assertOpenRouterDefaultHeaders(t, r) {
			http.Error(w, "bad attribution headers", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode chat request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if got, want := request["model"], "openrouter/test-chat"; got != want {
			t.Errorf("chat model = %#v, want %q", got, want)
		}
		if got, want := request["temperature"], 0.3; got != want {
			t.Errorf("chat temperature = %#v, want %v", got, want)
		}
		provider, ok := request["provider"].(map[string]any)
		if !ok || provider["allow_fallbacks"] != true {
			t.Errorf("chat provider preferences = %#v", request["provider"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"openrouter/test-chat","choices":[{"index":0,"message":{"role":"assistant","content":" ok "},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer server.Close()

	client := &LLMClient{
		client: NewClient(server.URL+"/chat/completions", "test-key", time.Second, server.Client()),
		model:  "openrouter/test-chat",
		params: map[string]any{
			"temperature": 0.3,
			"provider":    map[string]any{"allow_fallbacks": true},
		},
	}
	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate openrouter chat: %v", err)
	}
	if got, want := resp.Content, "ok"; got != want {
		t.Fatalf("chat content = %q, want %q", got, want)
	}
	if got, want := resp.Usage.TotalTokens, 5; got != want {
		t.Fatalf("chat total tokens = %d, want %d", got, want)
	}
}

// TestEmbeddingClientEmbedUsesOpenRouterEmbeddingsEndpoint verifies embeddings are requested as float vectors and converted into the internal vector shape.
// TestEmbeddingClientEmbedUsesOpenRouterEmbeddingsEndpoint 用于验证 embeddings 会以 float 向量格式请求，并转换成内部向量结构。
func TestEmbeddingClientEmbedUsesOpenRouterEmbeddingsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("embedding path = %q", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if !assertOpenRouterDefaultHeaders(t, r) {
			http.Error(w, "bad attribution headers", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode embedding request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if got, want := request["model"], "openrouter/test-embedding"; got != want {
			t.Errorf("embedding model = %#v, want %q", got, want)
		}
		if got, want := request["encoding_format"], "float"; got != want {
			t.Errorf("embedding encoding_format = %#v, want %q", got, want)
		}
		if got, want := request["dimensions"], float64(3); got != want {
			t.Errorf("embedding dimensions = %#v, want %v", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]},{"object":"embedding","index":1,"embedding":[0.4,0.5,0.6]}],"model":"openrouter/test-embedding","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer server.Close()

	client := &EmbeddingClient{
		client:    NewClient(server.URL+"/embeddings", "test-key", time.Second, server.Client()),
		model:     "openrouter/test-embedding",
		dimension: 3,
	}
	resp, err := client.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"alpha", "beta"}})
	if err != nil {
		t.Fatalf("embed openrouter texts: %v", err)
	}
	if got, want := len(resp.Vectors), 2; got != want {
		t.Fatalf("embedding vector count = %d, want %d", got, want)
	}
	if got, want := resp.Vectors[0][2], float32(0.3); got != want {
		t.Fatalf("embedding vector value = %v, want %v", got, want)
	}
	if got, want := resp.ResultIndices[1], 1; got != want {
		t.Fatalf("embedding result index = %d, want %d", got, want)
	}
}

// TestRerankerClientRerankUsesOpenRouterRerankEndpoint verifies rerank results are remapped from provider indexes back to internal document ids.
// TestRerankerClientRerankUsesOpenRouterRerankEndpoint 用于验证 rerank 结果会从 provider 索引重新映射回内部文档 id。
func TestRerankerClientRerankUsesOpenRouterRerankEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("rerank path = %q", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if !assertOpenRouterDefaultHeaders(t, r) {
			http.Error(w, "bad attribution headers", http.StatusBadRequest)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode rerank request: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if got, want := request["model"], "cohere/rerank-v3.5"; got != want {
			t.Errorf("rerank model = %#v, want %q", got, want)
		}
		if got, want := request["top_n"], float64(1); got != want {
			t.Errorf("rerank top_n = %#v, want %v", got, want)
		}
		provider, ok := request["provider"].(map[string]any)
		if !ok {
			t.Errorf("rerank provider preferences missing: %#v", request["provider"])
		} else {
			only, _ := provider["only"].([]any)
			if len(only) != 1 || only[0] != "Cohere" {
				t.Errorf("rerank provider.only = %#v", provider["only"])
			}
			if got, want := provider["allow_fallbacks"], false; got != want {
				t.Errorf("rerank provider.allow_fallbacks = %#v, want %v", got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"rerank-test","model":"cohere/rerank-v3.5","results":[{"index":1,"relevance_score":0.91,"document":{"text":"beta"}}]}`))
	}))
	defer server.Close()

	client := NewRerankerClientWithParams(server.URL+"/rerank", "test-key", "cohere/rerank-v3.5", time.Second, server.Client(), map[string]any{
		"provider": map[string]any{
			"only":            []any{"Cohere"},
			"allow_fallbacks": false,
		},
	}, nil)
	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{
		{ID: "doc-a", Text: "alpha"},
		{ID: "doc-b", Text: "beta"},
	}, 1)
	if err != nil {
		t.Fatalf("rerank openrouter docs: %v", err)
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("rerank result count = %d, want %d", got, want)
	}
	if got, want := results[0].ID, "doc-b"; got != want {
		t.Fatalf("rerank result id = %q, want %q", got, want)
	}
	if got, want := results[0].Score, 0.91; got != want {
		t.Fatalf("rerank score = %v, want %v", got, want)
	}
}
