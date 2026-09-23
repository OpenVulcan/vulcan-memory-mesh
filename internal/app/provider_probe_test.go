// provider_probe_test.go exercises diagnostic calls against loopback providers, never paid external services.
// provider_probe_test.go 使用回环供应商验证诊断调用，绝不访问收费外部服务。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/config"
)

// probeFixtureConfig binds all selected clients to one loopback endpoint with a disposable credential.
// probeFixtureConfig 将所有被选客户端绑定到同一回环端点，并使用一次性测试凭据。
func probeFixtureConfig(endpoint string) config.Config {
	cfg := config.Config{}
	cfg.LLM.Routes = []config.LLMRouteConfig{newLLMRouteForTest("openai", endpoint+"/v1", []string{"probe-secret"}, "probe-model")}
	cfg.Embedding = config.EmbeddingConfig{Provider: "openai", Endpoint: endpoint + "/v1", APIKeys: []string{"probe-secret"}, Model: "probe-model", Dimension: 3}
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{newRerankRouteForTest("siliconflow", endpoint+"/v1/rerank", []string{"probe-secret"}, "probe-model", time.Second)}
	cfg.Normalize()
	return cfg
}

// TestProbeProviderCallsSelectedRuntimeClients verifies real adapter requests, fixed inputs and independent purpose selection.
// TestProbeProviderCallsSelectedRuntimeClients 验证真实适配器请求、固定输入和独立用途选择。
func TestProbeProviderCallsSelectedRuntimeClients(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer probe-secret" {
			http.Error(w, "unexpected diagnostic request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"model":"probe-model"}`)
		case "/v1/embeddings":
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}],"model":"probe-model"}`)
		case "/v1/rerank":
			fmt.Fprint(w, `{"results":[{"index":0,"relevance_score":0.9}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := probeFixtureConfig(server.URL)
	for _, purpose := range []string{"llm", "embedding", "rerank"} {
		t.Run(purpose, func(t *testing.T) {
			before := requests.Load()
			result := ProbeProvider(context.Background(), cfg, purpose, 0)
			if !result.Success || result.Class != "ok" || requests.Load() != before+1 {
				t.Fatalf("selected provider probe failed: %+v", result)
			}
		})
	}
	before := requests.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := ProbeProvider(ctx, cfg, "llm", 0); result.Success || result.Class != "cancelled" {
		t.Fatalf("cancelled probe proceeded: %+v", result)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if result := ProbeProvider(expired, cfg, "llm", 0); result.Success || result.Class != "timeout" {
		t.Fatalf("expired probe proceeded: %+v", result)
	}
	for _, purpose := range []string{"unknown", "llm", "embedding", "rerank"} {
		if result := ProbeProvider(context.Background(), cfg, purpose, 9); result.Success || result.Class != "configuration" {
			t.Fatalf("invalid selection proceeded: %+v", result)
		}
	}
	if requests.Load() != before {
		t.Fatal("invalid or cancelled selections performed network requests")
	}
}

// TestProbeProviderRejectsBadResponsesAndRedactsFailures verifies wrong dimensions and untrusted remote error text.
// TestProbeProviderRejectsBadResponsesAndRedactsFailures 验证向量维度错误以及不可信远端错误文本的处理。
func TestProbeProviderRejectsBadResponsesAndRedactsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}]}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"remote-secret-probe-secret","type":"authentication_error"}}`)
	}))
	defer server.Close()
	cfg := probeFixtureConfig(server.URL)
	for _, purpose := range []string{"llm", "embedding"} {
		result := ProbeProvider(context.Background(), cfg, purpose, 0)
		encoded, err := json.Marshal(result)
		if err != nil || result.Success || result.Class == "ok" || strings.Contains(string(encoded), "secret") {
			t.Fatalf("failed provider response was accepted or leaked: %+v", result)
		}
	}
}
