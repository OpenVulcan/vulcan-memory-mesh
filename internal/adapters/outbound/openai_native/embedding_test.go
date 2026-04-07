// embedding_test.go implements the OpenAI-compatible outbound adapters.
// embedding_test.go 用于实现 OpenAI 兼容的出站适配器。
package openai_native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/trace"
)

// TestEmbeddingClientPassesDimension verifies the TestEmbeddingClientPassesDimension behavior.
// TestEmbeddingClientPassesDimension 用于验证 TestEmbeddingClientPassesDimension 行为。
func TestEmbeddingClientPassesDimension(t *testing.T) {
	var got map[string]any
	var gotHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-large","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer ts.Close()

	client := NewEmbeddingClient(
		ts.URL,
		"key",
		"text-embedding-3-large",
		1024,
		"org",
		"proj",
		map[string]any{"encoding_format": "float", "custom_mode": "fast"},
		map[string]map[string]any{"text-embedding-3-large": {"user": "fixture-user"}},
	)
	ctx := trace.WithTraceID(context.Background(), "trc_embed")
	resp, err := client.Embed(ctx, appports.EmbeddingRequest{Texts: []string{"hello"}, ProviderHints: map[string]any{"dimensions": 1024}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("vectors len = %d", len(resp.Vectors))
	}
	if got["dimensions"].(float64) != 1024 {
		t.Fatalf("dimensions = %#v", got["dimensions"])
	}
	if got["encoding_format"] != "float" {
		t.Fatalf("encoding_format = %#v", got["encoding_format"])
	}
	if got["user"] != "fixture-user" {
		t.Fatalf("user = %#v", got["user"])
	}
	if got["custom_mode"] != "fast" {
		t.Fatalf("custom_mode = %#v", got["custom_mode"])
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
	if gotHeaders.Get("X-Trace-ID") != "trc_embed" {
		t.Fatalf("trace header = %q", gotHeaders.Get("X-Trace-ID"))
	}
	if gotHeaders.Get("X-Client-Request-Id") != "trc_embed" {
		t.Fatalf("request id header = %q", gotHeaders.Get("X-Client-Request-Id"))
	}
}
