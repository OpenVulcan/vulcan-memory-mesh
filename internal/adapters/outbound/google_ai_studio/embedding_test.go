// embedding_test.go verifies the Google AI Studio native embedding adapter request mapping and response handling.
// embedding_test.go 用于验证 Google AI Studio 原生 embedding 适配器的请求映射与响应处理行为。
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

// TestEmbeddingClientEmbedMapsRequestToGeminiAPI verifies the adapter translates batched embedding requests into one Gemini batchEmbedContents call with trace headers preserved.
// TestEmbeddingClientEmbedMapsRequestToGeminiAPI 用于验证适配器会把批量 embedding 请求翻译成一次 Gemini batchEmbedContents 调用，并保留 trace 请求头。
func TestEmbeddingClientEmbedMapsRequestToGeminiAPI(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.URL.Path, ":batchEmbedContents") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"embeddings":[
				{"values":[0.1,0.2]},
				{"values":[0.3,0.4]}
			]
		}`))
	}))
	defer ts.Close()

	client := NewEmbeddingClient(
		ts.URL,
		"google-key",
		"gemini-embedding-001",
		1024,
		nil,
		map[string]map[string]any{
			"gemini-embedding-001": {
				"task_type": "RETRIEVAL_DOCUMENT",
			},
		},
	)
	ctx := trace.WithTraceID(context.Background(), "trc_google_embed")
	resp, err := client.Embed(ctx, appports.EmbeddingRequest{
		Texts: []string{"hello", "world"},
		ProviderHints: map[string]any{
			"title": "Knowledge Doc",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(resp.Vectors), 2; got != want {
		t.Fatalf("vector count = %d, want %d", got, want)
	}
	if gotHeaders.Get("x-goog-api-key") != "google-key" {
		t.Fatalf("api key header = %q", gotHeaders.Get("x-goog-api-key"))
	}
	if gotHeaders.Get("X-Trace-ID") != "trc_google_embed" {
		t.Fatalf("trace header = %q", gotHeaders.Get("X-Trace-ID"))
	}
	requests, ok := gotBody["requests"].([]any)
	if !ok || len(requests) != 2 {
		t.Fatalf("requests = %#v", gotBody["requests"])
	}
	firstRequest, ok := requests[0].(map[string]any)
	if !ok {
		t.Fatalf("first request = %#v", requests[0])
	}
	if firstRequest["model"] != "models/gemini-embedding-001" {
		t.Fatalf("model = %#v", firstRequest["model"])
	}
	if firstRequest["taskType"] != "RETRIEVAL_DOCUMENT" {
		t.Fatalf("taskType = %#v", firstRequest["taskType"])
	}
	if firstRequest["title"] != "Knowledge Doc" {
		t.Fatalf("title = %#v", firstRequest["title"])
	}
	if firstRequest["outputDimensionality"].(float64) != 1024 {
		t.Fatalf("outputDimensionality = %#v", firstRequest["outputDimensionality"])
	}
}
