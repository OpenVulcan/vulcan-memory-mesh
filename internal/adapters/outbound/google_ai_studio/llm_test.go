// llm_test.go verifies the Google AI Studio native LLM adapter request mapping and response handling.
// llm_test.go 用于验证 Google AI Studio 原生 LLM 适配器的请求映射与响应处理行为。
package google_ai_studio

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

// TestLLMClientGenerateMapsRequestToGeminiAPI verifies the adapter translates the internal request contract into one Gemini API GenerateContent call with trace headers preserved.
// TestLLMClientGenerateMapsRequestToGeminiAPI 用于验证适配器会把内部请求契约翻译成一次 Gemini API GenerateContent 调用，并保留 trace 请求头。
func TestLLMClientGenerateMapsRequestToGeminiAPI(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.URL.Path, ":generateContent") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"{\"need_memory\":true}"}]}}],
			"modelVersion":"gemini-2.5-flash-001",
			"responseId":"google-response-123",
			"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"cachedContentTokenCount":6,"thoughtsTokenCount":0,"totalTokenCount":18}
		}`))
	}))
	defer ts.Close()

	client := NewLLMClient(
		ts.URL,
		"google-key",
		"gemini-2.5-flash",
		map[string]any{
			"temperature": 0.1,
		},
		map[string]map[string]any{
			"gemini-2.5-flash": {
				"top_p": 0.9,
			},
		},
	)
	ctx := trace.WithTraceID(context.Background(), "trc_google_llm")
	resp, err := client.Generate(ctx, appports.LLMRequest{
		SystemPrompt:   "system prompt",
		UserPrompt:     "user prompt",
		ResponseFormat: appports.LLMResponseFormatJSON,
		ProviderHints: map[string]any{
			"temperature":       0.2,
			"max_output_tokens": 64,
			"top_k":             20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resp.Content) == "" {
		t.Fatal("expected non-empty content")
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 || resp.Usage.CachedInputTokens != 6 || resp.Usage.ReasoningTokens != 0 {
		t.Fatalf("unexpected usage: %#v", resp.Usage)
	}
	if resp.Model != "gemini-2.5-flash-001" {
		t.Fatalf("response model = %q", resp.Model)
	}
	if resp.RequestID != "google-response-123" {
		t.Fatalf("response request id = %q", resp.RequestID)
	}
	if gotHeaders.Get("x-goog-api-key") != "google-key" {
		t.Fatalf("api key header = %q", gotHeaders.Get("x-goog-api-key"))
	}
	if gotHeaders.Get("X-Trace-ID") != "trc_google_llm" {
		t.Fatalf("trace header = %q", gotHeaders.Get("X-Trace-ID"))
	}
	if gotHeaders.Get("X-Client-Request-Id") != "trc_google_llm" {
		t.Fatalf("request id header = %q", gotHeaders.Get("X-Client-Request-Id"))
	}
	if gotSystem, ok := gotBody["systemInstruction"].(map[string]any); !ok {
		t.Fatalf("systemInstruction = %#v", gotBody["systemInstruction"])
	} else if gotParts, ok := gotSystem["parts"].([]any); !ok || len(gotParts) != 1 {
		t.Fatalf("systemInstruction.parts = %#v", gotSystem["parts"])
	}
	if generationConfig, ok := gotBody["generationConfig"].(map[string]any); !ok {
		t.Fatalf("generationConfig = %#v", gotBody["generationConfig"])
	} else {
		if generationConfig["temperature"].(float64) != 0.2 {
			t.Fatalf("temperature = %#v", generationConfig["temperature"])
		}
		if generationConfig["topP"].(float64) != 0.9 {
			t.Fatalf("topP = %#v", generationConfig["topP"])
		}
		if generationConfig["topK"].(float64) != 20 {
			t.Fatalf("topK = %#v", generationConfig["topK"])
		}
		if generationConfig["maxOutputTokens"].(float64) != 64 {
			t.Fatalf("maxOutputTokens = %#v", generationConfig["maxOutputTokens"])
		}
		if generationConfig["responseMimeType"] != "application/json" {
			t.Fatalf("responseMimeType = %#v", generationConfig["responseMimeType"])
		}
	}
	generationConfig, generationConfigOK := gotBody["generationConfig"].(map[string]any)
	thinkingConfig, thinkingConfigOK := generationConfig["thinkingConfig"].(map[string]any)
	if !generationConfigOK || !thinkingConfigOK || thinkingConfig["thinkingBudget"] != float64(0) {
		t.Fatalf("thinkingConfig = %#v, want thinkingBudget 0", thinkingConfig)
	}
	contents, ok := gotBody["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v", gotBody["contents"])
	}
	firstContent, ok := contents[0].(map[string]any)
	if !ok || firstContent["role"] != "user" {
		t.Fatalf("first content = %#v", contents[0])
	}
}
