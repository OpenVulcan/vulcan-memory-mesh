// llm_output_logging.go wires the optional runtime-only LLM output logger that mirrors model responses into a dedicated prefixed log stream.
// llm_output_logging.go 用于接入可选的运行时专用 LLM 输出日志器，把模型响应镜像到带专属前缀的独立日志流中。
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
)

const (
	// llmOutputLogFilePrefix keeps the dedicated LLM output log file name aligned across runtime creation and tests.
	// llmOutputLogFilePrefix 用于在运行时创建逻辑和测试之间保持 LLM 专用日志文件名前缀一致。
	llmOutputLogFilePrefix = "LLM-"
)

// llmOutputLoggingClient decorates the shared LLM port so every successful generation can be mirrored into the dedicated LLM output log without leaking prompt input text.
// llmOutputLoggingClient 用于包装共享 LLM 端口，让每次成功生成都能镜像到专用 LLM 输出日志中，同时不泄露输入 prompt 文本。
type llmOutputLoggingClient struct {
	upstream appports.LLMClient
	logger   *logx.Logger
}

// buildLLMOutputLogger creates the optional dedicated logger used only for model outputs so runtime startup can fail fast when the prefixed log path is invalid.
// buildLLMOutputLogger 用于创建仅记录模型输出的可选专用日志器，让运行时在带前缀日志路径无效时尽早失败。
func buildLLMOutputLogger(cfg config.Config, logDir string) (*logx.Logger, appports.Shutdowner, error) {
	if !cfg.Logging.LLMOutputEnabled {
		return nil, nil, nil
	}
	writer, err := logx.NewHourlyPrefixedFileWriter(logDir, llmOutputLogFilePrefix)
	if err != nil {
		return nil, nil, err
	}
	logger := logx.New(writer, logx.Config{
		// Keep the dedicated stream readable regardless of the main runtime log level so enabling the feature always produces diagnostic output.
		// 让专用日志流在主运行时日志级别较高时仍保持可读，这样用户一旦开启功能就一定能拿到诊断输出。
		Level:  "info",
		Format: cfg.Logging.Format,
	}).With("component", "llm_output")
	return logger, writer, nil
}

// wrapLLMWithOutputLogger adds one optional response-only logging decorator around the shared LLM client so all processor-facing scenes can reuse one consistent output trace format.
// wrapLLMWithOutputLogger 用于给共享 LLM 客户端增加一个可选的“只记响应”日志装饰器，让所有处理器场景复用同一套输出追踪格式。
func wrapLLMWithOutputLogger(upstream appports.LLMClient, logger *logx.Logger) appports.LLMClient {
	if upstream == nil || logger == nil {
		return upstream
	}
	return llmOutputLoggingClient{
		upstream: upstream,
		logger:   logger,
	}
}

// Generate forwards one LLM request, measures the end-to-end latency, and records only the provider output plus metadata needed for malformed-JSON diagnosis.
// Generate 用于转发一次 LLM 请求、测量端到端耗时，并记录排查 JSON 畸形所需的输出内容和统计信息，而不写入输入 prompt。
func (c llmOutputLoggingClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.upstream == nil {
		return appports.LLMResponse{}, fmt.Errorf("llm output logging upstream is nil")
	}
	startedAt := time.Now()
	response, err := c.upstream.Generate(ctx, req)
	elapsed := time.Since(startedAt)
	if err != nil {
		return response, err
	}
	if c.logger != nil {
		c.logger.Info(
			"llm output captured",
			"scene", strings.TrimSpace(string(req.RouteSelectionLevel)),
			"response_format", strings.TrimSpace(string(req.ResponseFormat)),
			"model", resolveLLMOutputLogModel(req, response),
			"elapsed", elapsed.String(),
			"elapsed_ms", elapsed.Milliseconds(),
			"prompt_tokens", response.Usage.PromptTokens,
			"completion_tokens", response.Usage.CompletionTokens,
			"total_tokens", response.Usage.TotalTokens,
			"llm_output", response.Content,
		)
	}
	return response, nil
}

// resolveLLMOutputLogModel prefers the provider-reported model identity and falls back to the request-level model pin when the upstream omitted it.
// resolveLLMOutputLogModel 用于优先返回提供方上报的真实模型名；若上游未返回，则回退到请求级模型标识。
func resolveLLMOutputLogModel(req appports.LLMRequest, response appports.LLMResponse) string {
	if model := strings.TrimSpace(response.Model); model != "" {
		return model
	}
	return strings.TrimSpace(req.Model)
}
