// normalize_test.go characterizes the shared cleanup contract used by outbound AI provider adapters.
// normalize_test.go 用于刻画出站 AI provider 适配器共同遵循的请求清洗契约。
package providerinput

import (
	"reflect"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// TestNormalizeEmbeddingTextsTrimsAndDropsBlanks verifies that provider batching receives only meaningful normalized text.
// TestNormalizeEmbeddingTextsTrimsAndDropsBlanks 用于验证 provider 拆批仅接收经过规范化的有效文本。
func TestNormalizeEmbeddingTextsTrimsAndDropsBlanks(t *testing.T) {
	got := NormalizeEmbeddingTexts([]string{" first ", "\t", "second"})
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeEmbeddingTexts() = %#v, want %#v", got, want)
	}
}

// TestNormalizeRerankerDocumentsTrimsAndDropsIncompleteCandidates verifies the shared reranker request boundary.
// TestNormalizeRerankerDocumentsTrimsAndDropsIncompleteCandidates 用于验证共享的重排序请求边界会裁剪并剔除不完整候选。
func TestNormalizeRerankerDocumentsTrimsAndDropsIncompleteCandidates(t *testing.T) {
	got := NormalizeRerankerDocuments([]appports.RerankerDocument{
		{ID: " first ", Text: " content "},
		{ID: "", Text: "missing id"},
		{ID: "missing-text", Text: "  "},
	})
	want := []appports.RerankerDocument{{ID: "first", Text: "content"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeRerankerDocuments() = %#v, want %#v", got, want)
	}
}
