// provider_factory.go implements provider-aware fixed-model adapter factories shared by the key-failover wrappers.
// provider_factory.go 用于实现固定模型 key-failover 包装器共享的 provider 感知型适配器工厂。
package ai_key_failover

import (
	"fmt"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/google_ai_studio"
	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// newLLMProviderFactory builds one per-key concrete LLM adapter factory plus the matching provider error classifier for fixed-model failover.
// newLLMProviderFactory 用于构建固定模型 LLM 容灾所需的“按 key 创建具体适配器”的工厂，以及与之匹配的 provider 错误分类器。
func newLLMProviderFactory(provider, endpoint, model, organization, project string, params map[string]any, modelParams map[string]map[string]any, options Options) (func(string) appports.LLMClient, func(error, time.Time) failureDecision, error) {
	switch normalizeAIProvider(provider) {
	case "openai", "openai_native", "openai_go":
		return func(apiKey string) appports.LLMClient {
				return openai_native.NewLLMClient(endpoint, apiKey, model, organization, project, params, modelParams)
			}, func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, options, now)
			}, nil
	case "google_ai_studio":
		return func(apiKey string) appports.LLMClient {
				return google_ai_studio.NewLLMClient(endpoint, apiKey, model, params, modelParams)
			}, func(err error, now time.Time) failureDecision {
				return classifyGoogleAIStudioError(err, options, now)
			}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported llm provider: %s", provider)
	}
}

// newEmbeddingProviderFactory builds one per-key concrete embedding adapter factory plus the matching provider error classifier for fixed-model failover.
// newEmbeddingProviderFactory 用于构建固定模型 embedding 容灾所需的“按 key 创建具体适配器”的工厂，以及与之匹配的 provider 错误分类器。
func newEmbeddingProviderFactory(provider, endpoint, model string, dimension int, organization, project string, params map[string]any, modelParams map[string]map[string]any, options Options) (func(string) appports.EmbeddingClient, func(error, time.Time) failureDecision, error) {
	switch normalizeAIProvider(provider) {
	case "openai", "openai_native", "openai_go":
		return func(apiKey string) appports.EmbeddingClient {
				return openai_native.NewEmbeddingClient(endpoint, apiKey, model, dimension, organization, project, params, modelParams)
			}, func(err error, now time.Time) failureDecision {
				return classifyOpenAIError(err, options, now)
			}, nil
	case "google_ai_studio":
		return func(apiKey string) appports.EmbeddingClient {
				return google_ai_studio.NewEmbeddingClient(endpoint, apiKey, model, dimension, params, modelParams)
			}, func(err error, now time.Time) failureDecision {
				return classifyGoogleAIStudioError(err, options, now)
			}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported embedding provider: %s", provider)
	}
}

// normalizeAIProvider canonicalizes one provider alias so config validation and runtime adapter wiring compare the same token.
// normalizeAIProvider 用于规范化 provider 别名，让配置校验与运行时适配器装配比较同一份稳定 token。
func normalizeAIProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
