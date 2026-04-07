// key_failover_test.go verifies fixed-model API-key failover wrappers rotate only on genuine key-level failures and preserve deterministic behavior otherwise.
// key_failover_test.go 用于验证固定模型 API Key 容灾包装器只会在真实 Key 级故障下轮换，并在其他情况下保持确定性行为。
package ai_key_failover

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openvulcan/vmm/internal/adapters/outbound/dashscope_rerank"
	"github.com/openvulcan/vmm/internal/adapters/outbound/siliconflow_rerank"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"google.golang.org/genai"
)

// TestLLMClientGenerateSwitchesKeyOnRateLimit verifies LLM failover skips to the next key when the current key is rate limited.
// TestLLMClientGenerateSwitchesKeyOnRateLimit 用于验证当当前 Key 遭遇限流时，LLM 容灾会切换到下一个 Key。
func TestLLMClientGenerateSwitchesKeyOnRateLimit(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newOpenAIAPIError(http.StatusTooManyRequests, "rpm limit exceeded")},
		"key-b": {response: appports.LLMResponse{Content: "ok"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with failover: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("llm response content = %q", resp.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateStopsOnInvalidRequest verifies deterministic bad requests do not fan out to every key in the pool.
// TestLLMClientGenerateStopsOnInvalidRequest 用于验证确定性的坏请求不会扩散到 Key 池中的全部 Key。
func TestLLMClientGenerateStopsOnInvalidRequest(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newOpenAIAPIError(http.StatusBadRequest, "invalid request")},
		"key-b": {response: appports.LLMResponse{Content: "should-not-run"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	_, err = client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil {
		t.Fatal("expected invalid-request error")
	}
	if fakes["key-a"].calls != 1 {
		t.Fatalf("key-a calls = %d", fakes["key-a"].calls)
	}
	if fakes["key-b"].calls != 0 {
		t.Fatalf("expected key-b to stay unused, got %d calls", fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateStopsOnSharedForbidden verifies one shared 403 permission problem is surfaced immediately instead of quarantining every key in the pool.
// TestLLMClientGenerateStopsOnSharedForbidden 用于验证共享的 403 权限问题会被直接返回，而不是把整个 key 池都错误打入冷却。
func TestLLMClientGenerateStopsOnSharedForbidden(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newOpenAIAPIError(http.StatusForbidden, "project does not have access to model fixed-model")},
		"key-b": {response: appports.LLMResponse{Content: "should-not-run"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	_, err = client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil {
		t.Fatal("expected forbidden permission error")
	}
	if fakes["key-a"].calls != 1 {
		t.Fatalf("key-a calls = %d", fakes["key-a"].calls)
	}
	if fakes["key-b"].calls != 0 {
		t.Fatalf("expected key-b to stay unused, got %d calls", fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateSwitchesKeyOnKeyScopedForbidden verifies explicit invalid-key 403 responses still rotate to the next credential in the pool.
// TestLLMClientGenerateSwitchesKeyOnKeyScopedForbidden 用于验证当 403 明确指向坏掉的 key 时，容灾仍会切换到下一个凭据。
func TestLLMClientGenerateSwitchesKeyOnKeyScopedForbidden(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newOpenAIAPIError(http.StatusForbidden, "invalid api key provided")},
		"key-b": {response: appports.LLMResponse{Content: "ok"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with key-scoped forbidden failover: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("llm response content = %q", resp.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnInvalidKeyBadRequest verifies Gemini bad-request payloads that explicitly describe one invalid API key still rotate to the next credential.
// TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnInvalidKeyBadRequest 用于验证当 Gemini 的 400 载荷明确说明当前 API Key 无效时，容灾仍会切换到下一把凭据。
func TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnInvalidKeyBadRequest(t *testing.T) {
	client, err := NewProviderLLMClient("google_ai_studio", "", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new google ai studio llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newGoogleAIStudioAPIError(http.StatusBadRequest, "API key not valid. Please pass a valid API key.", "INVALID_ARGUMENT", map[string]any{"reason": "API_KEY_INVALID"})},
		"key-b": {response: appports.LLMResponse{Content: "ok"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with google invalid-key bad-request failover: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("llm response content = %q", resp.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestGoogleAIStudioLLMClientGenerateStopsOnSharedForbidden verifies Gemini shared permission failures surface immediately instead of quarantining every key in the pool.
// TestGoogleAIStudioLLMClientGenerateStopsOnSharedForbidden 用于验证 Gemini 的共享权限失败会被直接返回，而不是把整池 Key 都错误打入冷却。
func TestGoogleAIStudioLLMClientGenerateStopsOnSharedForbidden(t *testing.T) {
	client, err := NewProviderLLMClient("google_ai_studio", "", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new google ai studio llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newGoogleAIStudioAPIError(http.StatusForbidden, "Permission denied on resource project because the model is not enabled.", "PERMISSION_DENIED", map[string]any{"reason": "MODEL_ACCESS_DENIED"})},
		"key-b": {response: appports.LLMResponse{Content: "should-not-run"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	_, err = client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil {
		t.Fatal("expected shared forbidden permission error")
	}
	if fakes["key-a"].calls != 1 {
		t.Fatalf("key-a calls = %d", fakes["key-a"].calls)
	}
	if fakes["key-b"].calls != 0 {
		t.Fatalf("expected key-b to stay unused, got %d calls", fakes["key-b"].calls)
	}
}

// TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnKeyScopedForbidden verifies Gemini forbidden payloads that explicitly describe one invalid credential still rotate to the next key.
// TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnKeyScopedForbidden 用于验证当 Gemini 的 403 载荷明确指向单个无效凭据时，容灾仍会切换到下一把 Key。
func TestGoogleAIStudioLLMClientGenerateSwitchesKeyOnKeyScopedForbidden(t *testing.T) {
	client, err := NewProviderLLMClient("google_ai_studio", "", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new google ai studio llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {err: newGoogleAIStudioAPIError(http.StatusForbidden, "Request had invalid authentication credentials.", "PERMISSION_DENIED", map[string]any{"reason": "API_KEY_INVALID"})},
		"key-b": {response: appports.LLMResponse{Content: "ok"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with google key-scoped forbidden failover: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("llm response content = %q", resp.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestEmbeddingClientEmbedRejectsDifferentModelOrDimension verifies the fixed-model embedding wrapper rejects cross-model or cross-dimension requests before any key rotation starts.
// TestEmbeddingClientEmbedRejectsDifferentModelOrDimension 用于验证固定模型 embedding 包装器会在 Key 轮换开始前拒绝跨模型或跨维度请求。
func TestEmbeddingClientEmbedRejectsDifferentModelOrDimension(t *testing.T) {
	client, err := NewEmbeddingClient("https://example.com/v1", "fixed-embed", 1024, 10, 0, "", "", []string{"key-a"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new embedding failover client: %v", err)
	}

	if _, err := client.Embed(context.Background(), appports.EmbeddingRequest{Model: "other-model", Texts: []string{"hello"}}); err == nil {
		t.Fatal("expected fixed-model rejection")
	}
	if _, err := client.Embed(context.Background(), appports.EmbeddingRequest{Dimension: 1536, Texts: []string{"hello"}}); err == nil {
		t.Fatal("expected fixed-dimension rejection")
	}
}

// TestEmbeddingClientEmbedSplitsOversizedBatch verifies the failover wrapper splits one logical embedding request into configured sub-batches so higher-level callers no longer need to duplicate provider batch logic.
// TestEmbeddingClientEmbedSplitsOversizedBatch 用于验证 failover 包装器会按配置把一次逻辑 embedding 请求拆成多个子批次，让上层调用方无需重复维护 provider 拆批逻辑。
func TestEmbeddingClientEmbedSplitsOversizedBatch(t *testing.T) {
	client, err := NewEmbeddingClient("https://example.com/v1", "fixed-embed", 1024, 2, 0, "", "", []string{"key-a"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new embedding failover client: %v", err)
	}
	stub := &stubEmbeddingClient{
		responses: []appports.EmbeddingResponse{
			{Vectors: [][]float32{{1}, {2}}},
			{Vectors: [][]float32{{3}}},
		},
	}
	client.factory = func(apiKey string) appports.EmbeddingClient { return stub }

	resp, err := client.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"one", "two", "three"}})
	if err != nil {
		t.Fatalf("embed with split batches: %v", err)
	}
	if got, want := len(stub.requests), 2; got != want {
		t.Fatalf("embedding chunk count = %d, want %d (%#v)", got, want, stub.requests)
	}
	if got, want := len(stub.requests[0]), 2; got != want {
		t.Fatalf("first embedding chunk size = %d, want %d", got, want)
	}
	if got, want := len(stub.requests[1]), 1; got != want {
		t.Fatalf("second embedding chunk size = %d, want %d", got, want)
	}
	if got, want := len(resp.Vectors), 3; got != want {
		t.Fatalf("embedding result vectors = %d, want %d", got, want)
	}
}

// TestEmbeddingClientEmbedIsolatesAndTruncatesProviderRejectedText verifies the embedding controller can recursively isolate the single oversized text inside one logical batch and retry only that text with a shortened payload.
// TestEmbeddingClientEmbedIsolatesAndTruncatesProviderRejectedText 用于验证 embedding 控制器能够在一整个逻辑批次里递归定位出唯一超长文本，并仅对该条缩短后重试。
func TestEmbeddingClientEmbedIsolatesAndTruncatesProviderRejectedText(t *testing.T) {
	client, err := NewEmbeddingClient("https://example.com/v1", "fixed-embed", 1024, 4, 0, "", "", []string{"key-a"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new embedding failover client: %v", err)
	}
	tooLongText := strings.Repeat("too long payload ", 80)
	normalizedLongText := strings.TrimSpace(tooLongText)
	stub := &stubEmbeddingClient{
		handle: func(req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
			switch len(req.Texts) {
			case 2:
				return appports.EmbeddingResponse{}, newOpenAIAPIError(http.StatusBadRequest, "maximum context length exceeded")
			case 1:
				text := req.Texts[0]
				switch {
				case text == "short note":
					return appports.EmbeddingResponse{Vectors: [][]float32{{1, 1}}}, nil
				case strings.Contains(text, embeddingRetryTruncationMarker):
					return appports.EmbeddingResponse{Vectors: [][]float32{{2, 2}}}, nil
				default:
					return appports.EmbeddingResponse{}, newOpenAIAPIError(http.StatusBadRequest, "maximum context length exceeded")
				}
			default:
				return appports.EmbeddingResponse{}, fmt.Errorf("unexpected request shape: %#v", req.Texts)
			}
		},
	}
	client.factory = func(apiKey string) appports.EmbeddingClient { return stub }

	resp, err := client.Embed(context.Background(), appports.EmbeddingRequest{Texts: []string{"short note", tooLongText}})
	if err != nil {
		t.Fatalf("embed with provider length fallback: %v", err)
	}
	if got, want := len(resp.Vectors), 2; got != want {
		t.Fatalf("vector count = %d, want %d", got, want)
	}
	if got := len(stub.requests); got != 4 {
		t.Fatalf("request count = %d, want 4 (%#v)", got, stub.requests)
	}
	if got := stub.requests[0]; len(got) != 2 {
		t.Fatalf("first request should keep the full logical batch, got %#v", got)
	}
	if got := stub.requests[1]; len(got) != 1 || got[0] != "short note" {
		t.Fatalf("second request should isolate the healthy short text, got %#v", got)
	}
	if got := stub.requests[2]; len(got) != 1 || got[0] != normalizedLongText {
		t.Fatalf("third request should isolate the original long text, got %#v", got)
	}
	if got := stub.requests[3]; len(got) != 1 || !strings.Contains(got[0], embeddingRetryTruncationMarker) {
		t.Fatalf("fourth request should retry the long text with truncation marker, got %#v", got)
	}
}

// TestRerankerClientRerankSwitchesKeyOnQuota verifies rerank failover skips to the next key when the current key reports quota exhaustion.
// TestRerankerClientRerankSwitchesKeyOnQuota 用于验证当当前 Key 报告额度耗尽时，rerank 容灾会切换到下一个 Key。
func TestRerankerClientRerankSwitchesKeyOnQuota(t *testing.T) {
	client, err := NewRerankerClient("https://example.com/rerank", "fixed-rerank", 8*time.Second, []string{"key-a", "key-b"}, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new rerank failover client: %v", err)
	}
	fakes := map[string]*stubRerankerClient{
		"key-a": {err: errors.New("dashscope rerank status 429: insufficient_quota")},
		"key-b": {results: []appports.RerankerResult{{ID: "1", Score: 0.9}}},
	}
	client.factory = func(apiKey string) appports.RerankerClient { return fakes[apiKey] }

	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{{ID: "1", Text: "doc"}}, 1)
	if err != nil {
		t.Fatalf("rerank with failover: %v", err)
	}
	if len(results) != 1 || results[0].ID != "1" {
		t.Fatalf("unexpected rerank results: %#v", results)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected rerank call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestProviderRerankerClientRerankSwitchesKeyOnSiliconFlowQuota verifies the provider-aware rerank wrapper uses SiliconFlow-specific classification so key failover still rotates on quota failures.
// TestProviderRerankerClientRerankSwitchesKeyOnSiliconFlowQuota 用于验证 provider 感知的 rerank 包装器会使用 SiliconFlow 专属分类逻辑，确保额度故障时仍能正常切 Key。
func TestProviderRerankerClientRerankSwitchesKeyOnSiliconFlowQuota(t *testing.T) {
	client, err := NewProviderRerankerClient("siliconflow", "https://api.siliconflow.cn/v1/rerank", "BAAI/bge-reranker-v2-m3", 8*time.Second, []string{"key-a", "key-b"}, Options{
		Enabled:            true,
		Policy:             "ordered_failover",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new siliconflow rerank failover client: %v", err)
	}
	fakes := map[string]*stubRerankerClient{
		"key-a": {err: &siliconflow_rerank.APIError{StatusCode: http.StatusTooManyRequests, Body: `{"message":"insufficient balance"}`}},
		"key-b": {results: []appports.RerankerResult{{ID: "1", Score: 0.9}}},
	}
	client.factory = func(apiKey string) appports.RerankerClient { return fakes[apiKey] }

	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{{ID: "1", Text: "doc"}}, 1)
	if err != nil {
		t.Fatalf("siliconflow rerank with failover: %v", err)
	}
	if len(results) != 1 || results[0].ID != "1" {
		t.Fatalf("unexpected siliconflow rerank results: %#v", results)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected siliconflow rerank call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateRoundRobinRotatesHealthyKeys verifies healthy keys are distributed in round-robin mode without waiting for failures.
// TestLLMClientGenerateRoundRobinRotatesHealthyKeys 用于验证在 round-robin 模式下，健康 Key 会在无故障时正常轮转分配。
func TestLLMClientGenerateRoundRobinRotatesHealthyKeys(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", []string{"key-a", "key-b"}, nil, nil, Options{
		Enabled:            true,
		Policy:             "round_robin",
		RateLimitCooldown:  5 * time.Minute,
		QuotaCooldown:      10 * time.Minute,
		AuthCooldown:       12 * time.Hour,
		ProbeAfterCooldown: true,
	})
	if err != nil {
		t.Fatalf("new llm failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {response: appports.LLMResponse{Content: "from-a"}},
		"key-b": {response: appports.LLMResponse{Content: "from-b"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	first, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "one"})
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	second, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "two"})
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if first.Content == second.Content {
		t.Fatalf("expected round-robin distribution, got first=%q second=%q", first.Content, second.Content)
	}
}

// TestLLMClientGenerateSkipsQuotaExhaustedNodeByRPM verifies node-level RPM prechecks switch to the next routing node before the provider returns a hard limit.
// TestLLMClientGenerateSkipsQuotaExhaustedNodeByRPM 用于验证节点级 RPM 预检查会在 provider 返回硬限前先切到下一个轮询节点。
func TestLLMClientGenerateSkipsQuotaExhaustedNodeByRPM(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", nil, nil, nil, Options{
		Enabled: true,
		Policy:  "ordered_failover",
		Nodes: []NodeOptions{
			{Name: "primary", APIKeys: []string{"key-a"}, RPM: 1},
			{Name: "backup", APIKeys: []string{"key-b"}, RPM: 5},
		},
	})
	if err != nil {
		t.Fatalf("new llm node failover client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {response: appports.LLMResponse{Content: "from-a"}},
		"key-b": {response: appports.LLMResponse{Content: "from-b"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	first, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "first"})
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	second, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "second"})
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if first.Content != "from-a" {
		t.Fatalf("first response = %q", first.Content)
	}
	if second.Content != "from-b" {
		t.Fatalf("second response = %q", second.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateKeepsPerKeyRPMInsideOneNode verifies multiple keys inside one node each keep their own RPM budget instead of consuming one shared cap.
// TestLLMClientGenerateKeepsPerKeyRPMInsideOneNode 用于验证同一节点内的多个 Key 会各自保留独立的 RPM 预算，而不是消耗一份共享上限。
func TestLLMClientGenerateKeepsPerKeyRPMInsideOneNode(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", nil, nil, nil, Options{
		Enabled: true,
		Policy:  "ordered_failover",
		Nodes: []NodeOptions{
			{Name: "tier-a", APIKeys: []string{"key-a1", "key-a2"}, RPM: 1},
			{Name: "backup-node", APIKeys: []string{"key-b"}, RPM: 5},
		},
	})
	if err != nil {
		t.Fatalf("new llm per-key budget client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a1": {response: appports.LLMResponse{Content: "from-a1"}},
		"key-a2": {response: appports.LLMResponse{Content: "from-a2"}},
		"key-b":  {response: appports.LLMResponse{Content: "from-b"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	first, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "first"})
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	second, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "second"})
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	third, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "third"})
	if err != nil {
		t.Fatalf("third generate: %v", err)
	}
	if first.Content != "from-a1" {
		t.Fatalf("first response = %q", first.Content)
	}
	if second.Content != "from-a2" {
		t.Fatalf("second response = %q", second.Content)
	}
	if third.Content != "from-b" {
		t.Fatalf("third response = %q", third.Content)
	}
	if fakes["key-a1"].calls != 1 || fakes["key-a2"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a1=%d key-a2=%d key-b=%d", fakes["key-a1"].calls, fakes["key-a2"].calls, fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateRefundsAuthFailureBudget verifies one invalid key releases its own reserved RPM budget so later retries are not distorted by a deterministic auth failure.
// TestLLMClientGenerateRefundsAuthFailureBudget 用于验证单个无效 Key 会释放自己预留的 RPM 预算，避免确定性鉴权失败扭曲后续重试。
func TestLLMClientGenerateRefundsAuthFailureBudget(t *testing.T) {
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", nil, nil, nil, Options{
		Enabled: true,
		Policy:  "ordered_failover",
		Nodes: []NodeOptions{
			{Name: "shared-node", APIKeys: []string{"key-a1", "key-a2"}, RPM: 1},
		},
	})
	if err != nil {
		t.Fatalf("new llm auth-refund client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a1": {err: newOpenAIAPIError(http.StatusForbidden, "invalid api key provided")},
		"key-a2": {response: appports.LLMResponse{Content: "from-a2"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("generate with auth budget refund: %v", err)
	}
	if resp.Content != "from-a2" {
		t.Fatalf("response content = %q", resp.Content)
	}
	if fakes["key-a1"].calls != 1 || fakes["key-a2"].calls != 1 {
		t.Fatalf("unexpected call counts: key-a1=%d key-a2=%d", fakes["key-a1"].calls, fakes["key-a2"].calls)
	}
}

// TestSelectorCandidateNodeIndexesMarksBudgetExhaustion verifies selector prechecks keep budget-only exhaustion distinct so callers can wait for the next quota window instead of treating the pool as permanently dead.
// TestSelectorCandidateNodeIndexesMarksBudgetExhaustion 用于验证选择器预检查会把“仅预算耗尽”的场景单独标记出来，避免调用方把可恢复的 Key 池误判成永久失效。
func TestSelectorCandidateNodeIndexesMarksBudgetExhaustion(t *testing.T) {
	selector, err := newSelector(Options{
		ServiceName: "embedding",
		Enabled:     true,
		Nodes: []NodeOptions{
			{Name: "primary", APIKeys: []string{"key-a"}, RPM: 1},
		},
	})
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	now := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
	if !selector.reserveKeyBudget(0, 0, requestCost{Requests: 1}, now) {
		t.Fatal("expected first request budget reservation to succeed")
	}

	_, err = selector.candidateNodeIndexes(now, requestCost{Requests: 1})
	if !IsBudgetExhaustedCandidatesError(err) {
		t.Fatalf("expected budget exhaustion, got %v", err)
	}
}

// TestSelectorCandidateKeyIndexesMarksBudgetExhaustion verifies one node-level key pool still reports budget exhaustion after every healthy key in that node has spent its own fixed-window quota.
// TestSelectorCandidateKeyIndexesMarksBudgetExhaustion 用于验证当同一节点内所有健康 Key 都已经花完各自固定窗口预算时，节点内 Key 选择仍会报告预算耗尽而不是永久不可用。
func TestSelectorCandidateKeyIndexesMarksBudgetExhaustion(t *testing.T) {
	selector, err := newSelector(Options{
		ServiceName: "embedding",
		Enabled:     true,
		Nodes: []NodeOptions{
			{Name: "primary", APIKeys: []string{"key-a1", "key-a2"}, RPM: 1},
			{Name: "backup", APIKeys: []string{"key-b"}, RPM: 5},
		},
	})
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	now := time.Date(2026, 4, 7, 12, 30, 0, 0, time.UTC)
	if !selector.reserveKeyBudget(0, 0, requestCost{Requests: 1}, now) {
		t.Fatal("expected first node key-a1 reservation to succeed")
	}
	if !selector.reserveKeyBudget(0, 1, requestCost{Requests: 1}, now) {
		t.Fatal("expected first node key-a2 reservation to succeed")
	}

	_, err = selector.candidateKeyIndexes(0, now, requestCost{Requests: 1})
	if !IsBudgetExhaustedCandidatesError(err) {
		t.Fatalf("expected node-local budget exhaustion, got %v", err)
	}
}

// TestSelectorCandidateNodeIndexesKeepsUnavailableExhaustion verifies selector prechecks still surface the generic exhaustion marker when every key is genuinely unhealthy instead of temporarily quota-blocked.
// TestSelectorCandidateNodeIndexesKeepsUnavailableExhaustion 用于验证当所有 Key 都是真正不健康而不是暂时受配额限制时，选择器仍会返回普通耗尽错误而不会误标成预算等待。
func TestSelectorCandidateNodeIndexesKeepsUnavailableExhaustion(t *testing.T) {
	selector, err := newSelector(Options{
		ServiceName:        "embedding",
		Enabled:            true,
		ProbeAfterCooldown: false,
		Nodes: []NodeOptions{
			{Name: "primary", APIKeys: []string{"key-a"}},
		},
	})
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	now := time.Date(2026, 4, 7, 13, 0, 0, 0, time.UTC)
	selector.markFailure(0, 0, errorClassAuth, now.Add(time.Hour), now)

	_, err = selector.candidateNodeIndexes(now, requestCost{Requests: 1})
	if !IsExhaustedCandidatesError(err) {
		t.Fatalf("expected generic exhaustion error, got %v", err)
	}
	if IsBudgetExhaustedCandidatesError(err) {
		t.Fatalf("expected unavailable exhaustion instead of budget exhaustion, got %v", err)
	}
}

// TestLLMClientGenerateUsesFallbackUsageEstimate verifies missing provider usage is reconciled as 1.3x input tokens so TPM guards still accumulate pressure.
// TestLLMClientGenerateUsesFallbackUsageEstimate 用于验证当 provider 缺失 usage 时，系统会按输入 token 的 1.3 倍回填预算，确保 TPM 守卫仍能持续累计压力。
func TestLLMClientGenerateUsesFallbackUsageEstimate(t *testing.T) {
	longPrompt := strings.Repeat("hello world ", 24)
	inputTokens := estimateTextTokens(longPrompt)
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", nil, nil, nil, Options{
		Enabled: true,
		Policy:  "ordered_failover",
		Nodes: []NodeOptions{
			{Name: "missing-usage", APIKeys: []string{"key-a"}, TPM: (inputTokens * 22) / 10},
			{Name: "backup", APIKeys: []string{"key-b"}, TPM: inputTokens * 10},
		},
	})
	if err != nil {
		t.Fatalf("new llm missing-usage client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {response: appports.LLMResponse{Content: "from-a"}},
		"key-b": {response: appports.LLMResponse{Content: "from-b"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	first, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: longPrompt})
	if err != nil {
		t.Fatalf("first generate with fallback usage: %v", err)
	}
	second, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: longPrompt})
	if err != nil {
		t.Fatalf("second generate with fallback usage: %v", err)
	}
	if first.Content != "from-a" {
		t.Fatalf("first response = %q", first.Content)
	}
	if second.Content != "from-b" {
		t.Fatalf("second response = %q", second.Content)
	}
	if fakes["key-a"].calls != 1 || fakes["key-b"].calls != 1 {
		t.Fatalf("unexpected fallback usage call counts: key-a=%d key-b=%d", fakes["key-a"].calls, fakes["key-b"].calls)
	}
}

// TestLLMClientGenerateSkipsNodeByTPMPrecheck verifies one oversized request can bypass a low-TPM node before any network call is attempted.
// TestLLMClientGenerateSkipsNodeByTPMPrecheck 用于验证当单次请求预计超出节点 TPM 时，系统会在发起网络调用前直接绕过低 TPM 节点。
func TestLLMClientGenerateSkipsNodeByTPMPrecheck(t *testing.T) {
	longPrompt := strings.Repeat("hello world ", 24)
	estimatedTokens := estimateTextTokens(longPrompt)
	client, err := NewLLMClient("https://example.com/v1", "fixed-model", "", "", nil, nil, nil, Options{
		Enabled: true,
		Policy:  "ordered_failover",
		Nodes: []NodeOptions{
			{Name: "small-tpm", APIKeys: []string{"key-a"}, TPM: estimatedTokens - 1},
			{Name: "large-tpm", APIKeys: []string{"key-b"}, TPM: estimatedTokens + 50},
		},
	})
	if err != nil {
		t.Fatalf("new llm tpm client: %v", err)
	}
	fakes := map[string]*stubLLMClient{
		"key-a": {response: appports.LLMResponse{Content: "from-a"}},
		"key-b": {response: appports.LLMResponse{Content: "from-b"}},
	}
	client.factory = func(apiKey string) appports.LLMClient { return fakes[apiKey] }

	resp, err := client.Generate(context.Background(), appports.LLMRequest{UserPrompt: longPrompt})
	if err != nil {
		t.Fatalf("generate with tpm precheck: %v", err)
	}
	if resp.Content != "from-b" {
		t.Fatalf("response content = %q", resp.Content)
	}
	if fakes["key-a"].calls != 0 {
		t.Fatalf("expected low-tpm node to be skipped before dialing, got %d calls", fakes["key-a"].calls)
	}
}

// TestClassifyDashScopeErrorUsesRetryAfterHeader verifies DashScope rerank failover can honor provider Retry-After hints when the adapter preserves response headers.
// TestClassifyDashScopeErrorUsesRetryAfterHeader 用于验证当适配器保留响应头时，DashScope rerank 容灾可以尊重 provider 返回的 Retry-After 提示。
func TestClassifyDashScopeErrorUsesRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	decision := classifyDashScopeError(&dashscope_rerank.APIError{
		StatusCode: http.StatusTooManyRequests,
		Headers:    http.Header{"Retry-After-Ms": []string{"1200"}},
		Body:       "rate limit exceeded",
	}, Options{
		RespectRetryAfter: true,
		RateLimitCooldown: 5 * time.Minute,
	}, now)
	if !decision.SwitchKey {
		t.Fatal("expected dashscope rate limit to switch key")
	}
	if decision.Cooldown != 1200*time.Millisecond {
		t.Fatalf("dashscope retry-after cooldown = %v", decision.Cooldown)
	}
}

// TestClassifySiliconFlowErrorUsesRetryAfterHeader verifies SiliconFlow rerank failover can honor provider Retry-After hints when the adapter preserves response headers.
// TestClassifySiliconFlowErrorUsesRetryAfterHeader 用于验证当适配器保留响应头时，SiliconFlow rerank 容灾可以尊重 provider 返回的 Retry-After 提示。
func TestClassifySiliconFlowErrorUsesRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	decision := classifySiliconFlowError(&siliconflow_rerank.APIError{
		StatusCode: http.StatusTooManyRequests,
		Headers:    http.Header{"Retry-After": []string{"3"}},
		Body:       "rate limit exceeded",
	}, Options{
		RespectRetryAfter: true,
		RateLimitCooldown: 5 * time.Minute,
	}, now)
	if !decision.SwitchKey {
		t.Fatal("expected siliconflow rate limit to switch key")
	}
	if decision.Cooldown != 3*time.Second {
		t.Fatalf("siliconflow retry-after cooldown = %v", decision.Cooldown)
	}
}

// TestLLMMultiRouteClientGenerateSwitchesRouteOnExhaustion verifies route-level failover can continue to the next provider/model route after the current route reports that its internal key pool has no healthy candidates left.
// TestLLMMultiRouteClientGenerateSwitchesRouteOnExhaustion 用于验证当当前路由报告其内部 Key 池已无健康候选时，路由级容灾会继续切换到下一个 provider/model 路由。
func TestLLMMultiRouteClientGenerateSwitchesRouteOnExhaustion(t *testing.T) {
	primary := &stubLLMClient{err: newExhaustedCandidatesError("no healthy llm routing candidates available")}
	backup := &stubLLMClient{response: appports.LLMResponse{Content: "ok"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "primary", selectionWeights: llmSelectionWeightsForTest(20), model: "model-a", client: primary, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
			{name: "backup", selectionWeights: llmSelectionWeightsForTest(10), model: "model-b", client: backup, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with multi-route failover: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("llm multi-route response = %q", resp.Content)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected llm multi-route call counts: primary=%d backup=%d", primary.calls, backup.calls)
	}
	if primary.lastRequest.Model != "model-a" || backup.lastRequest.Model != "model-b" {
		t.Fatalf("unexpected routed llm models: primary=%q backup=%q", primary.lastRequest.Model, backup.lastRequest.Model)
	}
}

// TestLLMMultiRouteClientGeneratePrefersHigherWeight verifies the default reserve-slot ordering still prefers the highest-weight route even when a lower-weight route appears earlier in declaration order.
// TestLLMMultiRouteClientGeneratePrefersHigherWeight 用于验证默认 reserve 槽位排序仍会优先命中更高权重的路由，即使低权重路由写在前面。
func TestLLMMultiRouteClientGeneratePrefersHigherWeight(t *testing.T) {
	low := &stubLLMClient{response: appports.LLMResponse{Content: "low"}}
	high := &stubLLMClient{response: appports.LLMResponse{Content: "high"}}
	client, err := NewLLMMultiRouteClient([]LLMRouteOptions{
		{Name: "low", SelectionWeights: llmSelectionWeightsForTest(10), Provider: "openai", Endpoint: "https://low.example/v1", Model: "model-a", APIKeys: []string{"key-low"}},
		{Name: "high", SelectionWeights: llmSelectionWeightsForTest(100), Provider: "openai", Endpoint: "https://high.example/v1", Model: "model-b", APIKeys: []string{"key-high"}},
	})
	if err != nil {
		t.Fatalf("new llm multi-route client: %v", err)
	}
	client.routes[0].client = low
	client.routes[1].client = high

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with llm route weight: %v", err)
	}
	if resp.Content != "high" {
		t.Fatalf("weight-ranked llm response = %q", resp.Content)
	}
	if high.calls != 1 || low.calls != 0 {
		t.Fatalf("unexpected llm weight call counts: high=%d low=%d", high.calls, low.calls)
	}
}

// TestLLMMultiRouteClientGenerateUsesPerSceneWeights verifies one shared route table can choose different routes for different business call tiers.
// TestLLMMultiRouteClientGenerateUsesPerSceneWeights 用于验证同一份共享路由表可以针对不同业务调用层级选择不同的 route。
func TestLLMMultiRouteClientGenerateUsesPerSceneWeights(t *testing.T) {
	precheck := &stubLLMClient{response: appports.LLMResponse{Content: "precheck"}}
	postaction := &stubLLMClient{response: appports.LLMResponse{Content: "postaction"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{
				name:             "precheck",
				selectionWeights: LLMRouteSelectionWeights{PreCheckL1: 200, PostActionL1: 50, Reserve: 100},
				model:            "model-precheck",
				client:           precheck,
				classify:         func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} },
			},
			{
				name:             "postaction",
				selectionWeights: LLMRouteSelectionWeights{PreCheckL1: 60, PostActionL1: 220, Reserve: 100},
				model:            "model-postaction",
				client:           postaction,
				classify:         func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} },
			},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{
		SystemPrompt:        "system",
		UserPrompt:          "user",
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPreCheckL1,
	})
	if err != nil {
		t.Fatalf("generate with precheck_l1 weights: %v", err)
	}
	if resp.Content != "precheck" {
		t.Fatalf("precheck_l1 llm response = %q", resp.Content)
	}
	if precheck.calls != 1 || postaction.calls != 0 {
		t.Fatalf("unexpected precheck_l1 llm call counts: precheck=%d postaction=%d", precheck.calls, postaction.calls)
	}

	resp, err = client.Generate(context.Background(), appports.LLMRequest{
		SystemPrompt:        "system",
		UserPrompt:          "user",
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL1,
	})
	if err != nil {
		t.Fatalf("generate with postaction_l1 weights: %v", err)
	}
	if resp.Content != "postaction" {
		t.Fatalf("postaction_l1 llm response = %q", resp.Content)
	}
	if precheck.calls != 1 || postaction.calls != 1 {
		t.Fatalf("unexpected postaction_l1 llm call counts: precheck=%d postaction=%d", precheck.calls, postaction.calls)
	}
}

// TestLLMMultiRouteClientGeneratePreservesDeclarationOrderWithinSameWeight verifies routes with the same reserve-slot weight still keep their original declaration order so scheduling stays deterministic.
// TestLLMMultiRouteClientGeneratePreservesDeclarationOrderWithinSameWeight 用于验证当多个路由的 reserve 槽位权重相同时，系统仍保持原始声明顺序，确保调度结果稳定可预期。
func TestLLMMultiRouteClientGeneratePreservesDeclarationOrderWithinSameWeight(t *testing.T) {
	first := &stubLLMClient{response: appports.LLMResponse{Content: "first"}}
	second := &stubLLMClient{response: appports.LLMResponse{Content: "second"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "first", selectionWeights: llmSelectionWeightsForTest(50), model: "model-a", client: first, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
			{name: "second", selectionWeights: llmSelectionWeightsForTest(50), model: "model-b", client: second, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with equal-weight llm routes: %v", err)
	}
	if resp.Content != "first" {
		t.Fatalf("equal-weight llm response = %q", resp.Content)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("unexpected equal-weight llm call counts: first=%d second=%d", first.calls, second.calls)
	}
}

// TestLLMMultiRouteClientGenerateFiltersByRequestedModel verifies route-level failover only touches routes whose configured model exactly matches the caller's pinned model.
// TestLLMMultiRouteClientGenerateFiltersByRequestedModel 用于验证当调用方显式固定模型时，路由级容灾只会访问那些配置模型完全匹配的路由。
func TestLLMMultiRouteClientGenerateFiltersByRequestedModel(t *testing.T) {
	primary := &stubLLMClient{response: appports.LLMResponse{Content: "wrong-route"}}
	backup := &stubLLMClient{response: appports.LLMResponse{Content: "ok"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "primary", selectionWeights: llmSelectionWeightsForTest(100), model: "model-a", client: primary, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
			{name: "backup", selectionWeights: llmSelectionWeightsForTest(100), model: "model-b", client: backup, classify: func(error, time.Time) failureDecision { return failureDecision{Class: errorClassUnknown} }},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{Model: "model-b", SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate with pinned llm route model: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("pinned llm response = %q", resp.Content)
	}
	if primary.calls != 0 || backup.calls != 1 {
		t.Fatalf("unexpected llm pinned-route call counts: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestLLMMultiRouteClientGenerateContinuesAfterRouteLocalInvalidRequest verifies one route-local 400 still falls through to the next route because heterogeneous providers or models may reject different payload contracts.
// TestLLMMultiRouteClientGenerateContinuesAfterRouteLocalInvalidRequest 用于验证当首条路由返回路由级 400 时，系统仍会继续尝试下一条路由，因为异构 provider 或 model 可能接受不同的请求契约。
func TestLLMMultiRouteClientGenerateContinuesAfterRouteLocalInvalidRequest(t *testing.T) {
	primary := &stubLLMClient{err: newOpenAIAPIError(http.StatusBadRequest, "invalid request")}
	backup := &stubLLMClient{response: appports.LLMResponse{Content: "ok"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "primary", selectionWeights: llmSelectionWeightsForTest(100), model: "model-a", client: primary, classify: func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, Options{}, now)
			}},
			{name: "backup", selectionWeights: llmSelectionWeightsForTest(90), model: "model-b", client: backup, classify: func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, Options{}, now)
			}},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate after route-local invalid request: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("route-local invalid-request llm response = %q", resp.Content)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected invalid-request multi-route calls: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestLLMMultiRouteClientGenerateContinuesAfterWrappedRouteTimeout verifies one route-local provider timeout still falls through to the next route when the outer request context itself remains healthy.
// TestLLMMultiRouteClientGenerateContinuesAfterWrappedRouteTimeout 用于验证当外层请求上下文仍然健康时，单条 route 内部 provider 超时仍会继续切换到下一条 route。
func TestLLMMultiRouteClientGenerateContinuesAfterWrappedRouteTimeout(t *testing.T) {
	primary := &stubLLMClient{err: fmt.Errorf("call primary route: %w", context.DeadlineExceeded)}
	backup := &stubLLMClient{response: appports.LLMResponse{Content: "ok"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "primary", selectionWeights: llmSelectionWeightsForTest(100), model: "model-a", client: primary, classify: func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, Options{}, now)
			}},
			{name: "backup", selectionWeights: llmSelectionWeightsForTest(90), model: "model-b", client: backup, classify: func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, Options{}, now)
			}},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate after wrapped route timeout: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("wrapped-timeout llm response = %q", resp.Content)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected wrapped-timeout multi-route calls: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestLLMMultiRouteClientGenerateContinuesAfterGoogleRouteRateLimit verifies Google AI Studio route-local 429 errors are considered switch-worthy so the next route can continue serving traffic.
// TestLLMMultiRouteClientGenerateContinuesAfterGoogleRouteRateLimit 用于验证 Google AI Studio 路由本地的 429 错误会被视为可切换故障，从而允许下一条路由继续提供服务。
func TestLLMMultiRouteClientGenerateContinuesAfterGoogleRouteRateLimit(t *testing.T) {
	primary := &stubLLMClient{err: genai.APIError{Code: http.StatusTooManyRequests, Message: "rate limit exceeded", Status: "RESOURCE_EXHAUSTED"}}
	backup := &stubLLMClient{response: appports.LLMResponse{Content: "ok"}}
	client := &LLMMultiRouteClient{
		routes: []llmMultiRouteEntry{
			{name: "primary-google", selectionWeights: llmSelectionWeightsForTest(100), model: "model-a", client: primary, classify: func(err error, now time.Time) failureDecision {
				return classifyGoogleAIStudioError(err, Options{RateLimitCooldown: 5 * time.Minute}, now)
			}},
			{name: "backup-openai", selectionWeights: llmSelectionWeightsForTest(90), model: "model-b", client: backup, classify: func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, Options{}, now)
			}},
		},
	}

	resp, err := client.Generate(context.Background(), appports.LLMRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatalf("generate after google route-local rate limit: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("google route-local rate-limit llm response = %q", resp.Content)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected google rate-limit multi-route calls: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestRerankMultiRouteClientRerankSwitchesRouteOnQuota verifies rerank route failover can move to the next provider/model route after the current route reports a switch-worthy quota failure.
// TestRerankMultiRouteClientRerankSwitchesRouteOnQuota 用于验证当当前 rerank 路由报告值得切换的额度失败时，路由级容灾会切到下一个 provider/model 路由。
func TestRerankMultiRouteClientRerankSwitchesRouteOnQuota(t *testing.T) {
	primary := &stubRerankerClient{err: &dashscope_rerank.APIError{StatusCode: http.StatusTooManyRequests, Body: "insufficient_quota"}}
	backup := &stubRerankerClient{results: []appports.RerankerResult{{ID: "doc-1", Score: 0.9}}}
	client := &RerankMultiRouteClient{
		routes: []rerankMultiRouteEntry{
			{name: "primary", priority: 20, client: primary, classify: func(err error, now time.Time) failureDecision {
				return classifyDashScopeError(err, Options{QuotaCooldown: 5 * time.Minute}, now)
			}},
			{name: "backup", priority: 10, client: backup, classify: func(err error, now time.Time) failureDecision {
				return classifyDashScopeError(err, Options{QuotaCooldown: 5 * time.Minute}, now)
			}},
		},
	}

	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{{ID: "doc-1", Text: "text"}}, 1)
	if err != nil {
		t.Fatalf("rerank with multi-route failover: %v", err)
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("rerank result count = %d, want %d", got, want)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected rerank multi-route call counts: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestRerankMultiRouteClientRerankContinuesAfterRouteLocalInvalidRequest verifies one rerank route-local 400 still falls through to the next route because heterogeneous rerank routes may accept different request contracts.
// TestRerankMultiRouteClientRerankContinuesAfterRouteLocalInvalidRequest 用于验证当首条 rerank 路由返回路由级 400 时，系统仍会继续尝试下一条路由，因为异构 rerank 路由可能接受不同的请求契约。
func TestRerankMultiRouteClientRerankContinuesAfterRouteLocalInvalidRequest(t *testing.T) {
	primary := &stubRerankerClient{err: &dashscope_rerank.APIError{StatusCode: http.StatusBadRequest, Body: "invalid request"}}
	backup := &stubRerankerClient{results: []appports.RerankerResult{{ID: "doc-1", Score: 0.9}}}
	client := &RerankMultiRouteClient{
		routes: []rerankMultiRouteEntry{
			{name: "primary", client: primary, classify: func(err error, now time.Time) failureDecision {
				return classifyDashScopeError(err, Options{}, now)
			}},
			{name: "backup", client: backup, classify: func(err error, now time.Time) failureDecision {
				return classifyDashScopeError(err, Options{}, now)
			}},
		},
	}

	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{{ID: "doc-1", Text: "text"}}, 1)
	if err != nil {
		t.Fatalf("rerank after route-local invalid request: %v", err)
	}
	if got, want := len(results), 1; got != want || results[0].ID != "doc-1" {
		t.Fatalf("unexpected route-local invalid-request rerank results: %#v", results)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("unexpected rerank invalid-request multi-route calls: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

// TestRerankMultiRouteClientRerankPrefersHigherPriority verifies rerank scheduling prefers the highest-priority available route even when that route is declared after a lower-priority candidate.
// TestRerankMultiRouteClientRerankPrefersHigherPriority 用于验证即使高优先级 rerank 路由写在低优先级候选后面，调度仍会优先命中更高优先级的可用路由。
func TestRerankMultiRouteClientRerankPrefersHigherPriority(t *testing.T) {
	low := &stubRerankerClient{results: []appports.RerankerResult{{ID: "low", Score: 0.1}}}
	high := &stubRerankerClient{results: []appports.RerankerResult{{ID: "high", Score: 0.9}}}
	client, err := NewRerankMultiRouteClient([]RerankRouteOptions{
		{Name: "low", Priority: 10, Provider: "dashscope", Endpoint: "https://low.example/v1", Model: "model-low", Timeout: 5 * time.Second, APIKeys: []string{"key-low"}},
		{Name: "high", Priority: 100, Provider: "dashscope", Endpoint: "https://high.example/v1", Model: "model-high", Timeout: 5 * time.Second, APIKeys: []string{"key-high"}},
	})
	if err != nil {
		t.Fatalf("new rerank multi-route client: %v", err)
	}
	client.routes[0].client = high
	client.routes[1].client = low

	results, err := client.Rerank(context.Background(), "query", []appports.RerankerDocument{{ID: "doc-1", Text: "text"}}, 1)
	if err != nil {
		t.Fatalf("rerank with route priority: %v", err)
	}
	if got, want := len(results), 1; got != want || results[0].ID != "high" {
		t.Fatalf("unexpected rerank priority results: %#v", results)
	}
	if high.calls != 1 || low.calls != 0 {
		t.Fatalf("unexpected rerank priority call counts: high=%d low=%d", high.calls, low.calls)
	}
}

// stubLLMClient records one canned LLM response or error for a specific API key in failover tests.
// stubLLMClient 用于在容灾测试中按指定 API Key 记录一条预置的 LLM 响应或错误。
type stubLLMClient struct {
	response    appports.LLMResponse
	err         error
	calls       int
	lastRequest appports.LLMRequest
}

// Generate returns the canned response or error while incrementing the call counter.
// Generate 用于在递增调用计数的同时返回预置响应或错误。
func (s *stubLLMClient) Generate(_ context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	s.calls++
	s.lastRequest = req
	if s.err != nil {
		return appports.LLMResponse{}, s.err
	}
	return s.response, nil
}

// stubRerankerClient records one canned rerank response or error for a specific API key in failover tests.
// stubRerankerClient 用于在容灾测试中按指定 API Key 记录一条预置的 rerank 响应或错误。
type stubRerankerClient struct {
	results []appports.RerankerResult
	err     error
	calls   int
}

// Rerank returns the canned rerank response or error while incrementing the call counter.
// Rerank 用于在递增调用计数的同时返回预置的 rerank 响应或错误。
func (s *stubRerankerClient) Rerank(context.Context, string, []appports.RerankerDocument, int) ([]appports.RerankerResult, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return append([]appports.RerankerResult(nil), s.results...), nil
}

// stubEmbeddingClient records each embedding batch while replaying preloaded responses so wrapper tests can assert chunking without a live provider.
// stubEmbeddingClient 用于记录每个 embedding 子批次并回放预置响应，让包装器测试无需真实 provider 也能断言拆批行为。
type stubEmbeddingClient struct {
	responses []appports.EmbeddingResponse
	handle    func(appports.EmbeddingRequest) (appports.EmbeddingResponse, error)
	err       error
	calls     int
	requests  [][]string
}

// Embed replays one queued embedding response and records the effective text batch.
// Embed 用于回放一个预置 embedding 响应，并记录实际收到的文本批次。
func (s *stubEmbeddingClient) Embed(_ context.Context, req appports.EmbeddingRequest) (appports.EmbeddingResponse, error) {
	s.calls++
	s.requests = append(s.requests, append([]string(nil), req.Texts...))
	if s.handle != nil {
		return s.handle(req)
	}
	if s.err != nil {
		return appports.EmbeddingResponse{}, s.err
	}
	if s.calls > len(s.responses) {
		return appports.EmbeddingResponse{}, nil
	}
	return s.responses[s.calls-1], nil
}

// llmSelectionWeightsForTest mirrors one legacy shared route weight into all five LLM selection slots so compatibility-focused tests stay concise.
// llmSelectionWeightsForTest 用于把一份旧版共享路由权重镜像到 5 个 LLM 选择槽位，让兼容性测试保持简洁。
func llmSelectionWeightsForTest(weight int) LLMRouteSelectionWeights {
	return LLMRouteSelectionWeights{
		PreCheckL1:   weight,
		PreCheckL2:   weight,
		PostActionL1: weight,
		PostActionL2: weight,
		Reserve:      weight,
	}
}

// newOpenAIAPIError builds one minimal OpenAI-compatible API error for failover classification tests.
// newOpenAIAPIError 用于为容灾分类测试构造一条最小可用的 OpenAI-compatible API 错误。
func newOpenAIAPIError(status int, message string) error {
	return &openai.Error{
		StatusCode: status,
		Message:    message,
		Request:    &http.Request{Method: http.MethodPost, URL: &url.URL{Scheme: "https", Host: "example.com", Path: "/v1/chat/completions"}},
		Response:   &http.Response{StatusCode: status, Header: make(http.Header)},
	}
}

// newGoogleAIStudioAPIError builds one minimal Google AI Studio SDK error for failover classification tests.
// newGoogleAIStudioAPIError 用于为容灾分类测试构造一条最小可用的 Google AI Studio SDK 错误。
func newGoogleAIStudioAPIError(status int, message, statusText string, details ...map[string]any) error {
	return genai.APIError{
		Code:    status,
		Message: message,
		Status:  statusText,
		Details: append([]map[string]any(nil), details...),
	}
}
