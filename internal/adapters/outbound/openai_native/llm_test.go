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

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
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
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"keywords\":[\"fastapi\"],\"need_memory\":true,\"reason\":\"ok\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}
		}`))
	}))
	defer ts.Close()

	client := NewLLMClient(
		ts.URL+"/compatible-mode/v1",
		"key",
		"test-model",
		"org",
		"proj",
		map[string]any{
			"Enable_Thinking":   true,
			"Include_Reasoning": true,
			"Reasoning_Effort":  "high",
			"Reasoning":         map[string]any{"effort": "high"},
			"Thinking":          map[string]any{"type": "enabled"},
			"temperature":       0.1,
		},
		map[string]map[string]any{
			"test-model": {
				"temperature": 0.3,
			},
		},
	)
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
	if resp.Model != "test-model" {
		t.Fatalf("response model = %q", resp.Model)
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
	if gotBody["model"] != "test-model" {
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
	if _, exists := gotBody["include_reasoning"]; exists {
		t.Fatalf("include_reasoning must not be treated as a no-thinking control: %#v", gotBody["include_reasoning"])
	}
	if gotBody["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v", gotBody["reasoning_effort"])
	}
	reasoning, ok := gotBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %#v", gotBody["reasoning"])
	}
	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v", gotBody["thinking"])
	}
	for _, nonCanonicalKey := range []string{"Enable_Thinking", "Include_Reasoning", "Reasoning_Effort", "Reasoning", "Thinking"} {
		if _, exists := gotBody[nonCanonicalKey]; exists {
			t.Fatalf("expected normalized no-thinking projection without %q, got %#v", nonCanonicalKey, gotBody)
		}
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

// TestApplyProviderHintsPreservesDeclaredReasoningObjectDialect verifies enabled-false providers do not receive an unrelated effort field.
// TestApplyProviderHintsPreservesDeclaredReasoningObjectDialect 用于验证 enabled-false 供应商不会收到无关的 effort 字段。
func TestApplyProviderHintsPreservesDeclaredReasoningObjectDialect(t *testing.T) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel("test-model"),
		Messages: buildChatMessages("system", "user"),
	}
	applyProviderHints(&params, map[string]any{"reasoning": map[string]any{"enabled": true}})

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal projected chat request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode projected chat request: %v", err)
	}
	reasoning, ok := decoded["reasoning"].(map[string]any)
	if !ok || reasoning["enabled"] != false {
		t.Fatalf("reasoning = %#v, body=%s", decoded["reasoning"], body)
	}
	if _, exists := reasoning["effort"]; exists {
		t.Fatalf("reasoning effort must remain absent for enabled dialect: %s", body)
	}
}

// TestApplyProviderHintsProjectsOnlyDeclaredNoThinkingField verifies one Chat-compatible provider is not sent an unrelated reasoning dialect when it declares only the Anthropic-style thinking switch.
// TestApplyProviderHintsProjectsOnlyDeclaredNoThinkingField 用于验证仅声明 Anthropic 风格 thinking 开关的 Chat 兼容供应商不会收到无关的 reasoning 方言字段。
func TestApplyProviderHintsProjectsOnlyDeclaredNoThinkingField(t *testing.T) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel("test-model"),
		Messages: buildChatMessages("system", "user"),
	}
	applyProviderHints(&params, map[string]any{" Thinking ": map[string]any{"type": "enabled"}})

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal projected chat request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode projected chat request: %v", err)
	}
	thinking, ok := decoded["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, body=%s", decoded["thinking"], body)
	}
	for _, absentKey := range []string{"reasoning_effort", "reasoning", "enable_thinking", "include_reasoning", " Thinking "} {
		if _, exists := decoded[absentKey]; exists {
			t.Fatalf("expected only declared thinking dialect without %q, body=%s", absentKey, body)
		}
	}
}

// TestApplyProviderHintsDropsUnregisteredReasoningControls verifies unknown extra fields cannot re-enable thinking after the declared switch is forced off.
// TestApplyProviderHintsDropsUnregisteredReasoningControls 用于验证未知扩展字段无法在已声明开关被强制关闭后重新开启思考。
func TestApplyProviderHintsDropsUnregisteredReasoningControls(t *testing.T) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel("test-model"),
		Messages: buildChatMessages("system", "user"),
	}
	applyProviderHints(&params, map[string]any{
		"reasoning_effort": "high",
		"reasoning_budget": 8192,
		"thinking-budget":  4096,
		"vendor_option":    "retained",
	})

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal projected chat request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode projected chat request: %v", err)
	}
	if decoded["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v, body=%s", decoded["reasoning_effort"], body)
	}
	for _, absentKey := range []string{"reasoning_budget", "thinking-budget"} {
		if _, exists := decoded[absentKey]; exists {
			t.Fatalf("unregistered reasoning control %q must be discarded, body=%s", absentKey, body)
		}
	}
	if decoded["vendor_option"] != "retained" {
		t.Fatalf("unrelated provider extra must be retained, body=%s", body)
	}
}
