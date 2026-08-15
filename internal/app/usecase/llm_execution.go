// llm_execution.go preserves physical LLM facts while use-case validation transforms processor results.
// llm_execution.go 用于在用例校验转换处理器结果时保留物理 LLM 事实。
package usecase

import (
	"errors"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// attachUseCaseLLMExecutionToInvalidOutput enriches one use-case-generated structured-output error with the completed processor call that produced its review payload.
// attachUseCaseLLMExecutionToInvalidOutput 用于给用例层生成的结构化输出错误附加产出该评审载荷的已完成处理器调用。
//
// Parameters:
// 参数：
//   - err: materialization or validation error that may classify as InvalidLLMOutputError.
//   - err：可能分类为 InvalidLLMOutputError 的物化或校验错误。
//   - execution: physical call metadata retained by the successful processor result.
//   - execution：成功处理器结果保留的物理调用元数据。
//
// Returns:
// 返回值：
//   - error: enriched invalid-output error when possible, otherwise the original error.
//   - error：可附加时返回增强后的无效输出错误，否则返回原始错误。
func attachUseCaseLLMExecutionToInvalidOutput(err error, execution *logicdomain.LLMExecutionMetadata) error {
	if err == nil || execution == nil {
		return err
	}
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) || invalid.Execution != nil {
		return err
	}
	retainedExecution := *execution
	invalid.Execution = &retainedExecution
	return invalid
}
