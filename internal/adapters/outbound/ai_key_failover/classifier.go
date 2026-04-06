// classifier.go implements provider-error classification so fixed-model AI adapters only switch API keys on genuine key-level failures.
// classifier.go 用于实现 provider 错误分类，让固定模型 AI 适配器只在真正的 Key 级故障下切换 API Key。
package ai_key_failover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openvulcan/vmm/internal/adapters/outbound/dashscope_rerank"
	"google.golang.org/genai"
)

// errorClass labels the normalized failure class used by the in-memory key failover state machine.
// errorClass 用于标记内存态 Key 容灾状态机使用的规范化失败类别。
type errorClass string

const (
	// errorClassNone marks successful calls with no failure state to record.
	// errorClassNone 用于标记成功调用，不需要记录失败状态。
	errorClassNone errorClass = "none"
	// errorClassRateLimit marks one temporary RPM/TPM/rate-limit restriction on the current API key.
	// errorClassRateLimit 用于标记当前 API Key 的临时 RPM/TPM/限流限制。
	errorClassRateLimit errorClass = "rate_limit"
	// errorClassQuota marks one quota or balance exhaustion on the current API key.
	// errorClassQuota 用于标记当前 API Key 的额度或余额耗尽。
	errorClassQuota errorClass = "quota"
	// errorClassAuth marks one authentication or authorization failure on the current API key.
	// errorClassAuth 用于标记当前 API Key 的鉴权或授权失败。
	errorClassAuth errorClass = "auth"
	// errorClassInvalidRequest marks one deterministic bad request that should not fan out to other keys.
	// errorClassInvalidRequest 用于标记确定性的坏请求，这类请求不应扩散到其他 Key。
	errorClassInvalidRequest errorClass = "invalid_request"
	// errorClassPublicFault marks one shared upstream/server fault that should not trigger blind key rotation by default.
	// errorClassPublicFault 用于标记共享上游/服务端故障，默认不应触发盲目 Key 轮换。
	errorClassPublicFault errorClass = "public_fault"
	// errorClassUnknown marks any uncategorized error that should conservatively stop failover.
	// errorClassUnknown 用于标记未能归类的错误，默认保守地停止 failover。
	errorClassUnknown errorClass = "unknown"
)

// failureDecision describes whether one provider error should rotate to another key and which cooldown to apply.
// failureDecision 用于描述单次 provider 错误是否应切换到其他 Key，以及应应用哪种冷却时长。
type failureDecision struct {
	Class     errorClass
	SwitchKey bool
	Cooldown  time.Duration
}

// classifyOpenAIError maps the final OpenAI-compatible SDK error into one key-failover decision.
// classifyOpenAIError 用于把最终的 OpenAI-compatible SDK 错误映射成一条 Key 容灾决策。
func classifyOpenAIError(err error, options Options, now time.Time) failureDecision {
	if err == nil {
		return failureDecision{Class: errorClassNone}
	}
	if isNetworkError(err) {
		return failureDecision{Class: errorClassPublicFault}
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return failureDecision{Class: errorClassUnknown}
	}
	body := strings.ToLower(strings.TrimSpace(apiErr.Message + " " + apiErr.Code + " " + apiErr.Type + " " + apiErr.RawJSON()))
	switch apiErr.StatusCode {
	case http.StatusBadRequest:
		return failureDecision{Class: errorClassInvalidRequest}
	case http.StatusUnauthorized:
		return failureDecision{
			Class:     errorClassAuth,
			SwitchKey: true,
			Cooldown:  chooseCooldown(apiErr.Response, now, options.RespectRetryAfter, options.AuthCooldown),
		}
	case http.StatusForbidden:
		if shouldSwitchKeyOnOpenAIForbidden(body) {
			return failureDecision{
				Class:     errorClassAuth,
				SwitchKey: true,
				Cooldown:  chooseCooldown(apiErr.Response, now, options.RespectRetryAfter, options.AuthCooldown),
			}
		}
		return failureDecision{Class: errorClassPublicFault}
	case http.StatusTooManyRequests:
		if containsAny(body, "insufficient_quota", "quota", "balance", "insufficient balance", "insufficient_balance") {
			return failureDecision{
				Class:     errorClassQuota,
				SwitchKey: true,
				Cooldown:  chooseCooldown(apiErr.Response, now, options.RespectRetryAfter, options.QuotaCooldown),
			}
		}
		return failureDecision{
			Class:     errorClassRateLimit,
			SwitchKey: true,
			Cooldown:  chooseCooldown(apiErr.Response, now, options.RespectRetryAfter, options.RateLimitCooldown),
		}
	default:
		if apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode >= http.StatusInternalServerError {
			return failureDecision{Class: errorClassPublicFault}
		}
	}
	return failureDecision{Class: errorClassUnknown}
}

// classifyGoogleAIStudioError maps one Google AI Studio native SDK error into the same key-failover decision contract used by the OpenAI-compatible adapters.
// classifyGoogleAIStudioError 用于把单次 Google AI Studio 原生 SDK 错误映射成与 OpenAI-compatible 适配器一致的 key-failover 决策契约。
func classifyGoogleAIStudioError(err error, options Options, now time.Time) failureDecision {
	if err == nil {
		return failureDecision{Class: errorClassNone}
	}
	if isNetworkError(err) {
		return failureDecision{Class: errorClassPublicFault}
	}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return failureDecision{Class: errorClassUnknown}
	}
	body := strings.ToLower(strings.TrimSpace(apiErr.Message + " " + apiErr.Status + " " + fmt.Sprintf("%v", apiErr.Details)))
	switch apiErr.Code {
	case http.StatusBadRequest:
		if shouldSwitchKeyOnGoogleAIStudioCredentialFailure(body) {
			return failureDecision{
				Class:     errorClassAuth,
				SwitchKey: true,
				Cooldown:  options.AuthCooldown,
			}
		}
		return failureDecision{Class: errorClassInvalidRequest}
	case http.StatusUnauthorized:
		return failureDecision{
			Class:     errorClassAuth,
			SwitchKey: true,
			Cooldown:  options.AuthCooldown,
		}
	case http.StatusForbidden:
		if shouldSwitchKeyOnGoogleAIStudioCredentialFailure(body) {
			return failureDecision{
				Class:     errorClassAuth,
				SwitchKey: true,
				Cooldown:  options.AuthCooldown,
			}
		}
		return failureDecision{Class: errorClassPublicFault}
	case http.StatusTooManyRequests:
		if containsAny(body, "insufficient_quota", "quota", "billing", "resource_exhausted", "exhausted", "budget") {
			return failureDecision{
				Class:     errorClassQuota,
				SwitchKey: true,
				Cooldown:  options.QuotaCooldown,
			}
		}
		return failureDecision{
			Class:     errorClassRateLimit,
			SwitchKey: true,
			Cooldown:  options.RateLimitCooldown,
		}
	default:
		if apiErr.Code == http.StatusRequestTimeout || apiErr.Code == http.StatusConflict || apiErr.Code >= http.StatusInternalServerError {
			return failureDecision{Class: errorClassPublicFault}
		}
	}
	return failureDecision{Class: errorClassUnknown}
}

// shouldSwitchKeyOnOpenAIForbidden only returns true for 403 payloads that explicitly point to one bad or revoked credential instead of one shared permission problem.
// shouldSwitchKeyOnOpenAIForbidden 仅在 403 载荷明确指向单个坏掉或被吊销的凭据时返回 true，而不会把共享权限问题误判为切 Key 场景。
func shouldSwitchKeyOnOpenAIForbidden(body string) bool {
	return containsAny(
		body,
		"invalid_api_key",
		"invalid api key",
		"incorrect_api_key",
		"incorrect api key",
		"bad api key",
		"api key is disabled",
		"key disabled",
		"revoked api key",
		"api key has been revoked",
	)
}

// shouldSwitchKeyOnGoogleAIStudioCredentialFailure only returns true when a Gemini error payload explicitly points to one malformed, revoked, or otherwise key-scoped credential failure.
// shouldSwitchKeyOnGoogleAIStudioCredentialFailure 仅在 Gemini 错误载荷明确指向单个格式错误、已吊销或其他 Key 级凭据失效时返回 true，避免把共享权限问题误判为切 Key 场景。
func shouldSwitchKeyOnGoogleAIStudioCredentialFailure(body string) bool {
	return containsAny(
		body,
		"api_key_invalid",
		"api key invalid",
		"api key not valid",
		"invalid api key",
		"bad api key",
		"malformed api key",
		"credential is malformed",
		"invalid authentication credentials",
		"api key is disabled",
		"key disabled",
		"revoked api key",
		"api key has been revoked",
	)
}

// classifyDashScopeError maps one DashScope rerank error into the same key-failover decision contract used by the OpenAI-compatible adapters.
// classifyDashScopeError 用于把单次 DashScope rerank 错误映射成与 OpenAI-compatible 适配器相同的 Key 容灾决策契约。
func classifyDashScopeError(err error, options Options, now time.Time) failureDecision {
	if err == nil {
		return failureDecision{Class: errorClassNone}
	}
	if isNetworkError(err) {
		return failureDecision{Class: errorClassPublicFault}
	}
	var apiErr *dashscope_rerank.APIError
	if errors.As(err, &apiErr) {
		message := strings.ToLower(strings.TrimSpace(apiErr.Error() + " " + apiErr.Body))
		switch apiErr.StatusCode {
		case http.StatusBadRequest:
			return failureDecision{Class: errorClassInvalidRequest}
		case http.StatusUnauthorized, http.StatusForbidden:
			return failureDecision{
				Class:     errorClassAuth,
				SwitchKey: true,
				Cooldown:  chooseCooldownFromHeader(apiErr.Headers, now, options.RespectRetryAfter, options.AuthCooldown),
			}
		case http.StatusTooManyRequests:
			if containsAny(message, "insufficient_quota", "quota", "balance", "insufficient balance", "insufficient_balance") {
				return failureDecision{
					Class:     errorClassQuota,
					SwitchKey: true,
					Cooldown:  chooseCooldownFromHeader(apiErr.Headers, now, options.RespectRetryAfter, options.QuotaCooldown),
				}
			}
			return failureDecision{
				Class:     errorClassRateLimit,
				SwitchKey: true,
				Cooldown:  chooseCooldownFromHeader(apiErr.Headers, now, options.RespectRetryAfter, options.RateLimitCooldown),
			}
		default:
			if apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode >= http.StatusInternalServerError {
				return failureDecision{Class: errorClassPublicFault}
			}
		}
		return failureDecision{Class: errorClassUnknown}
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	status := parseStatusCodeFromText(message)
	switch status {
	case http.StatusBadRequest:
		return failureDecision{Class: errorClassInvalidRequest}
	case http.StatusUnauthorized, http.StatusForbidden:
		return failureDecision{
			Class:     errorClassAuth,
			SwitchKey: true,
			Cooldown:  options.AuthCooldown,
		}
	case http.StatusTooManyRequests:
		if containsAny(message, "insufficient_quota", "quota", "balance", "insufficient balance", "insufficient_balance") {
			return failureDecision{
				Class:     errorClassQuota,
				SwitchKey: true,
				Cooldown:  options.QuotaCooldown,
			}
		}
		return failureDecision{
			Class:     errorClassRateLimit,
			SwitchKey: true,
			Cooldown:  options.RateLimitCooldown,
		}
	default:
		if status == http.StatusRequestTimeout || status == http.StatusConflict || status >= http.StatusInternalServerError {
			return failureDecision{Class: errorClassPublicFault}
		}
	}
	return failureDecision{Class: errorClassUnknown}
}

// chooseCooldown prefers provider Retry-After hints when enabled and otherwise falls back to one static configured duration.
// chooseCooldown 用于在启用时优先采用 provider 的 Retry-After 提示，否则回退到静态配置时长。
func chooseCooldown(response *http.Response, now time.Time, respectRetryAfter bool, fallback time.Duration) time.Duration {
	if response == nil {
		return chooseCooldownFromHeader(nil, now, respectRetryAfter, fallback)
	}
	return chooseCooldownFromHeader(response.Header, now, respectRetryAfter, fallback)
}

// chooseCooldownFromHeader prefers Retry-After hints from one raw response header map when enabled and otherwise falls back to one static configured duration.
// chooseCooldownFromHeader 用于在启用时优先采用原始响应头中的 Retry-After 提示，否则回退到静态配置时长。
func chooseCooldownFromHeader(header http.Header, now time.Time, respectRetryAfter bool, fallback time.Duration) time.Duration {
	if !respectRetryAfter {
		return fallback
	}
	if duration, ok := retryAfterDuration(header, now); ok && duration > 0 {
		return duration
	}
	return fallback
}

// retryAfterDuration parses Retry-After-Ms or Retry-After headers into one positive cooldown duration.
// retryAfterDuration 用于把 Retry-After-Ms 或 Retry-After 请求头解析成正值冷却时长。
func retryAfterDuration(header http.Header, now time.Time) (time.Duration, bool) {
	if header == nil {
		return 0, false
	}
	if raw := strings.TrimSpace(header.Get("Retry-After-Ms")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond, true
		}
	}
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second)), true
		}
		if deadline, err := http.ParseTime(raw); err == nil && deadline.After(now) {
			return deadline.Sub(now), true
		}
	}
	return 0, false
}

// containsAny reports whether the lowercase haystack includes any lowercase marker.
// containsAny 用于判断已经转为小写的原文中是否包含任意一个同样为小写的标记。
func containsAny(haystack string, markers ...string) bool {
	for _, marker := range markers {
		if marker != "" && strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// isNetworkError reports whether one wrapped error represents a transport or request-deadline fault rather than a key-level fault.
// isNetworkError 用于判断某个被包装的错误是否属于传输层或请求截止时间故障，而不是 Key 级故障。
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

// parseStatusCodeFromText extracts the first HTTP status code from the DashScope adapter error text.
// parseStatusCodeFromText 用于从 DashScope 适配器错误文本中提取第一个 HTTP 状态码。
func parseStatusCodeFromText(message string) int {
	const marker = "status "
	index := strings.Index(message, marker)
	if index < 0 {
		return 0
	}
	start := index + len(marker)
	end := start
	for end < len(message) && message[end] >= '0' && message[end] <= '9' {
		end++
	}
	if end == start {
		return 0
	}
	status, err := strconv.Atoi(message[start:end])
	if err != nil {
		return 0
	}
	return status
}
