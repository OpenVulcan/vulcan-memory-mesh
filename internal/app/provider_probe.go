// provider_probe.go implements explicit installer network diagnostics using runtime AI clients without storage initialization.
// provider_probe.go 在应用层使用运行时 AI 客户端执行明确请求的安装诊断，不初始化存储。
package app

import (
	"context"
	"math"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// ProviderProbeResult contains only fixed diagnostic codes and selection metadata, never provider output or credentials.
// ProviderProbeResult 只包含固定诊断码及选择元数据，不包含供应商回复或凭据。
type ProviderProbeResult struct {
	Version string `json:"version"`
	Purpose string `json:"purpose"`
	Route   int    `json:"route"`
	Success bool   `json:"success"`
	Class   string `json:"class"`
}

// ProbeProvider tests one purpose and route from validated cfg; ctx cancellation and a twenty-second bound limit configured failover attempts.
// ProbeProvider 测试已校验 cfg 中指定用途及路由；ctx 取消和二十秒上限约束已配置的故障转移尝试。
func ProbeProvider(ctx context.Context, cfg config.Config, purpose string, routeIndex int) ProviderProbeResult {
	result := ProviderProbeResult{Version: "v1", Purpose: purpose, Route: routeIndex, Class: "configuration"}
	if ctx == nil || routeIndex < 0 {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		result.Class = "cancelled"
		if ctx.Err() == context.DeadlineExceeded {
			result.Class = "timeout"
		}
		return result
	}
	// Construct only the explicitly selected client; diagnostic calls never start database or rule pipelines.
	// 只构建明确选中的客户端；诊断调用不会启动数据库或规则处理管线。
	switch purpose {
	case "llm":
		routes := cfg.LLM.ProviderRoutes()
		if routeIndex >= len(routes) {
			return result
		}
		client, err := buildOneLLMRouteClient(routes[routeIndex], routeIndex)
		if err != nil {
			return result
		}
		response, err := client.Generate(ctx, appports.LLMRequest{Model: routes[routeIndex].Model, UserPrompt: "Reply with OK. This is a connection test.", ResponseFormat: appports.LLMResponseFormatText})
		if err != nil {
			result.Class = "request-failed"
		} else if strings.TrimSpace(response.Content) == "" {
			result.Class = "invalid-response"
		} else {
			result.Success = true
		}
	case "embedding":
		if routeIndex != 0 {
			return result
		}
		client, err := buildEmbedding(cfg)
		if err != nil {
			return result
		}
		response, err := client.Embed(ctx, appports.EmbeddingRequest{Model: cfg.Embedding.Model, Dimension: cfg.Embedding.Dimension, Texts: []string{"VMM connection test"}})
		if err != nil {
			result.Class = "request-failed"
		} else if response.ValidateStrict(1) != nil || len(response.Vectors[0]) != cfg.Embedding.Dimension {
			result.Class = "invalid-response"
		} else {
			result.Success = true
			for _, value := range response.Vectors[0] {
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					result.Success, result.Class = false, "invalid-response"
				}
			}
		}
	case "rerank":
		routes := cfg.Rerank.ProviderRoutes()
		if !cfg.Rerank.Enabled || routeIndex >= len(routes) {
			return result
		}
		client, err := buildOneRerankRouteClient(routes[routeIndex], routeIndex)
		if err != nil {
			return result
		}
		response, err := client.Rerank(ctx, "connection test", []appports.RerankerDocument{{ID: "test", Text: "VMM connection test"}}, 1)
		if err != nil {
			result.Class = "request-failed"
		} else if len(response) != 1 || response[0].ID != "test" || math.IsNaN(response[0].Score) || math.IsInf(response[0].Score, 0) {
			result.Class = "invalid-response"
		} else {
			result.Success = true
		}
	default:
		return result
	}
	// Override transport details with stable cancellation classes and never expose raw upstream errors.
	// 用稳定取消类别替换传输细节，绝不暴露上游原始错误。
	if ctx.Err() == context.DeadlineExceeded {
		result.Success, result.Class = false, "timeout"
	} else if ctx.Err() != nil {
		result.Success, result.Class = false, "cancelled"
	} else if result.Success {
		result.Class = "ok"
	}
	return result
}
