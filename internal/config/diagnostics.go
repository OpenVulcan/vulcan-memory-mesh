// diagnostics.go carries non-invasive stage metadata for configuration loading diagnostics.
// diagnostics.go 用于承载不改变配置语义的加载阶段诊断元数据。
package config

import "errors"

// ConfigLoadStage identifies the trusted phase that produced a configuration-loading error.
// ConfigLoadStage 用于标识产生配置加载错误的可信阶段。
type ConfigLoadStage string

const (
	// ConfigLoadStageRead marks filesystem reads of configuration layers.
	// ConfigLoadStageRead 表示配置层文件系统读取阶段。
	ConfigLoadStageRead ConfigLoadStage = "read"
	// ConfigLoadStageParse marks YAML/JSON decoding and strict field parsing.
	// ConfigLoadStageParse 表示 YAML/JSON 解码和严格字段解析阶段。
	ConfigLoadStageParse ConfigLoadStage = "parse"
	// ConfigLoadStageEnvironment marks required placeholders and process override parsing.
	// ConfigLoadStageEnvironment 表示必填占位符和进程覆盖解析阶段。
	ConfigLoadStageEnvironment ConfigLoadStage = "environment"
	// ConfigLoadStageValidation marks normalized runtime contract validation.
	// ConfigLoadStageValidation 表示归一化运行时契约校验阶段。
	ConfigLoadStageValidation ConfigLoadStage = "validation"
)

// ConfigLoadError preserves the original error text while attaching trusted stage metadata for safe diagnostics.
// ConfigLoadError 保留原始错误文本，同时附加供安全诊断使用的可信阶段元数据。
type ConfigLoadError struct {
	Stage ConfigLoadStage
	Err   error
}

// Error preserves the historical LoadPaths error text so existing callers and tests keep their behavior.
// Error 保留历史 LoadPaths 错误文本，确保现有调用方和测试行为不变。
func (e *ConfigLoadError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Stage)
	}
	return e.Err.Error()
}

// Unwrap exposes the original error to errors.Is and errors.As without changing its public message.
// Unwrap 向 errors.Is 和 errors.As 暴露原始错误，同时不改变对外错误消息。
func (e *ConfigLoadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// WrapConfigLoadError annotates one configuration error with its trusted loading phase.
// WrapConfigLoadError 为一个配置错误附加可信加载阶段。
func WrapConfigLoadError(stage ConfigLoadStage, err error) error {
	if err == nil {
		return nil
	}
	var existing *ConfigLoadError
	if errors.As(err, &existing) {
		return err
	}
	return &ConfigLoadError{Stage: stage, Err: err}
}

// ConfigLoadErrorStage returns trusted stage metadata when the error originated inside LoadPaths.
// ConfigLoadErrorStage 用于在错误来自 LoadPaths 时返回可信阶段元数据。
func ConfigLoadErrorStage(err error) (ConfigLoadStage, bool) {
	var stageError *ConfigLoadError
	if !errors.As(err, &stageError) || stageError == nil {
		return "", false
	}
	return stageError.Stage, true
}
