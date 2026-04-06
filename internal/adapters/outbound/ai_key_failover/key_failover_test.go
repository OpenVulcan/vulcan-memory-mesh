// key_failover_test.go verifies fixed-model API-key failover wrappers rotate only on genuine key-level failures and preserve deterministic behavior otherwise.
// key_failover_test.go 用于验证固定模型 API Key 容灾包装器只会在真实 Key 级故障下轮换，并在其他情况下保持确定性行为。
package ai_key_failover

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openvulcan/vmm/internal/adapters/outbound/dashscope_rerank"
	appports "github.com/openvulcan/vmm/internal/app/ports"
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

// TestEmbeddingClientEmbedRejectsDifferentModelOrDimension verifies the fixed-model embedding wrapper rejects cross-model or cross-dimension requests before any key rotation starts.
// TestEmbeddingClientEmbedRejectsDifferentModelOrDimension 用于验证固定模型 embedding 包装器会在 Key 轮换开始前拒绝跨模型或跨维度请求。
func TestEmbeddingClientEmbedRejectsDifferentModelOrDimension(t *testing.T) {
	client, err := NewEmbeddingClient("https://example.com/v1", "fixed-embed", 1024, "", "", []string{"key-a"}, nil, nil, Options{
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

// stubLLMClient records one canned LLM response or error for a specific API key in failover tests.
// stubLLMClient 用于在容灾测试中按指定 API Key 记录一条预置的 LLM 响应或错误。
type stubLLMClient struct {
	response appports.LLMResponse
	err      error
	calls    int
}

// Generate returns the canned response or error while incrementing the call counter.
// Generate 用于在递增调用计数的同时返回预置响应或错误。
func (s *stubLLMClient) Generate(context.Context, appports.LLMRequest) (appports.LLMResponse, error) {
	s.calls++
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
