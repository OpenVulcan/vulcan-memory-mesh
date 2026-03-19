// llm_test.go implements the OpenAI-compatible outbound adapters.
// llm_test.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestLLMClientGenerateMapsRequestToSDK verifies the TestLLMClientGenerateMapsRequestToSDK behavior.
// TestLLMClientGenerateMapsRequestToSDK 用于验证 TestLLMClientGenerateMapsRequestToSDK 行为。
func TestLLMClientGenerateMapsRequestToSDK(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl_test",
			"object":"chat.completion",
			"created":1,
			"model":"gpt-4.1-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"keywords\":[\"fastapi\"],\"need_memory\":true,\"reason\":\"ok\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}
		}`))
	}))
	defer ts.Close()

	client := NewLLMClient(ts.URL+"/compatible-mode/v1", "key", "gpt-4.1-mini", "org", "proj")
	ctx := trace.WithTraceID(context.Background(), "trc_test")
	resp, err := client.Generate(ctx, appports.LLMRequest{
		SystemPrompt:   "system prompt",
		UserPrompt:     "user prompt",
		ResponseFormat: appports.LLMResponseFormatJSON,
		ProviderHints: map[string]any{
			"temperature":           0.2,
			"top_p":                 0.9,
			"max_completion_tokens": 64,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("expected non-empty content")
	}
	if resp.Usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %#v", resp.Usage)
	}
	if gotHeaders.Get("Authorization") != "Bearer key" {
		t.Fatalf("authorization header = %q", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("OpenAI-Organization") != "org" {
		t.Fatalf("organization header = %q", gotHeaders.Get("OpenAI-Organization"))
	}
	if gotHeaders.Get("OpenAI-Project") != "proj" {
		t.Fatalf("project header = %q", gotHeaders.Get("OpenAI-Project"))
	}
	if gotHeaders.Get("X-Trace-ID") != "trc_test" {
		t.Fatalf("trace header = %q", gotHeaders.Get("X-Trace-ID"))
	}
	if gotHeaders.Get("X-Client-Request-Id") != "trc_test" {
		t.Fatalf("request id header = %q", gotHeaders.Get("X-Client-Request-Id"))
	}
	if gotBody["model"] != "gpt-4.1-mini" {
		t.Fatalf("model = %#v", gotBody["model"])
	}
	if gotBody["temperature"].(float64) != 0.2 {
		t.Fatalf("temperature = %#v", gotBody["temperature"])
	}
	if gotBody["top_p"].(float64) != 0.9 {
		t.Fatalf("top_p = %#v", gotBody["top_p"])
	}
	if gotBody["max_completion_tokens"].(float64) != 64 {
		t.Fatalf("max_completion_tokens = %#v", gotBody["max_completion_tokens"])
	}
	if gotBody["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %#v", gotBody["enable_thinking"])
	}
	responseFormat, ok := gotBody["response_format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v", gotBody["response_format"])
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	first, ok := messages[0].(map[string]any)
	if !ok || first["role"] != "system" {
		t.Fatalf("first message = %#v", messages[0])
	}
	second, ok := messages[1].(map[string]any)
	if !ok || second["role"] != "user" {
		t.Fatalf("second message = %#v", messages[1])
	}
}
