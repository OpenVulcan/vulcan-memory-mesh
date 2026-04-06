// client_live_test.go verifies the DashScope rerank adapter against the real provider when credentials are available.
// client_live_test.go 用于在凭证可用时，对 DashScope rerank 适配器执行真实 provider 验证。
package dashscope_rerank

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// TestClientRerankLive verifies the adapter can reach the real DashScope rerank API with the current shared API key.
// TestClientRerankLive 用于验证适配器可以使用当前共享 API key 访问真实 DashScope rerank 接口。
func TestClientRerankLive(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("skip live dashscope rerank test: no OPENAI_API_KEY configured")
	}

	client := NewClient(
		firstNonEmpty(
			strings.TrimSpace(os.Getenv("OPENAI_RERANK_URL")),
			"https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
		),
		apiKey,
		firstNonEmpty(strings.TrimSpace(os.Getenv("OPENAI_RERANK_MODEL")), "qwen3-vl-rerank"),
		12*time.Second,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	results, err := client.Rerank(ctx, "什么是文本排序模型", []appports.RerankerDocument{
		{ID: "doc-1", Text: "文本排序模型广泛用于搜索引擎和推荐系统中，它们根据文本相关性对候选文本进行排序。"},
		{ID: "doc-2", Text: "量子计算是计算科学的一个前沿领域。"},
		{ID: "doc-3", Text: "预训练语言模型的发展给文本排序模型带来了新的进展。"},
	}, 3)
	if err != nil {
		t.Fatalf("live rerank call failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected non-empty live rerank results")
	}
	if results[0].ID == "" {
		t.Fatalf("unexpected live rerank first result: %#v", results[0])
	}
}

// firstNonEmpty returns the first non-empty string so the live test can fall back to the default model cleanly.
// firstNonEmpty 用于返回第一个非空字符串，让 live test 可以干净地回退到默认模型。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
