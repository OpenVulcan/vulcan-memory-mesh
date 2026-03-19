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

// TestEmbeddingClientRejectsTooManyTexts verifies the TestEmbeddingClientRejectsTooManyTexts behavior.
// TestEmbeddingClientRejectsTooManyTexts 用于验证 TestEmbeddingClientRejectsTooManyTexts 行为。
func TestEmbeddingClientRejectsTooManyTexts(t *testing.T) {
	client := NewEmbeddingClient("http://example.com", "key", "text-embedding-3-large", 1024, "", "")
	texts := make([]string, 11)
	for i := range texts {
		texts[i] = "x"
	}
	_, err := client.Embed(context.Background(), appports.EmbeddingRequest{Texts: texts})
	if err == nil || err.Error() != "exceeds max batch size 10" {
		t.Fatalf("unexpected error: %v", err)
	}
}

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

	client := NewEmbeddingClient(ts.URL, "key", "text-embedding-3-large", 1024, "org", "proj")
	ctx := trace.WithTraceID(context.Background(), "trc_embed")
	resp, err := client.Embed(ctx, appports.EmbeddingRequest{Texts: []string{"hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("vectors len = %d", len(resp.Vectors))
	}
	if got["dimensions"].(float64) != 1024 {
		t.Fatalf("dimensions = %#v", got["dimensions"])
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
