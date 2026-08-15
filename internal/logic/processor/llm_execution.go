// llm_execution.go maps provider response facts into domain metadata retained by multi-stage processors.
// llm_execution.go 用于把供应商响应事实映射为多阶段处理器保留的领域元数据。
package processor

import (
	"errors"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	logicports "github.com/openvulcan/vmm/internal/logic/ports"
)

// llmExecutionMetadata builds one immutable execution fact from the configured purpose/model and the physical provider response.
// llmExecutionMetadata 用于根据配置用途、配置模型与物理供应商响应构建一份不可变执行事实。
//
// Parameters:
// 参数：
//   - purpose: stable VMM processing scene that owns the call.
//   - purpose：持有该调用的稳定 VMM 处理场景。
//   - configuredModel: exact model selected before dispatch.
//   - configuredModel：分派前选定的精确模型。
//   - response: physical response identity and usage returned by the LLM port.
//   - response：LLM 端口返回的物理响应身份与用量。
//
// Returns:
// 返回值：
//   - logicdomain.LLMExecutionMetadata: normalized execution fact suitable for later logging.
//   - logicdomain.LLMExecutionMetadata：适合后续日志记录的规范化执行事实。
func llmExecutionMetadata(purpose, configuredModel string, response logicports.LLMResponse) logicdomain.LLMExecutionMetadata {
	return logicdomain.LLMExecutionMetadata{
		Purpose:         strings.TrimSpace(purpose),
		ConfiguredModel: strings.TrimSpace(configuredModel),
		ResponseModel:   strings.TrimSpace(response.Model),
		RequestID:       strings.TrimSpace(response.RequestID),
		Usage:           response.Usage,
	}
}

// attachLLMExecutionToInvalidOutput adds physical response facts to a structured-output failure without changing its classification or raw payload boundary.
// attachLLMExecutionToInvalidOutput 用于向结构化输出失败附加物理响应事实，同时不改变错误分类或原始载荷边界。
//
// Parameters:
// 参数：
//   - err: parser or validator error that may contain InvalidLLMOutputError.
//   - err：可能包含 InvalidLLMOutputError 的解析或校验错误。
//   - execution: completed physical call metadata to attach.
//   - execution：需要附加的已完成物理调用元数据。
//
// Returns:
// 返回值：
//   - error: enriched InvalidLLMOutputError when classified, otherwise the original error.
//   - error：分类命中时返回增强后的 InvalidLLMOutputError，否则返回原始错误。
func attachLLMExecutionToInvalidOutput(err error, execution logicdomain.LLMExecutionMetadata) error {
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		return err
	}
	invalid.Execution = &execution
	return invalid
}
