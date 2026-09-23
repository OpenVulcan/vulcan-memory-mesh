// config_command.go implements the machine-readable configuration inspection commands.
// config_command.go 用于实现机器可读配置检查命令。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/openvulcan/vmm/internal/app"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/pii"
)

const (
	// configCommandUsage documents the stable configuration subcommand surface consumed by installers.
	// configCommandUsage 用于记录安装器消费的稳定配置子命令入口。
	configCommandUsage = "usage: vmm-local config {schema|validate} [--json] [--config <directory-or-yaml>]"

	// configDiagnosticLayout identifies failures before the layered config loader can attach a trusted stage.
	// configDiagnosticLayout 用于标识分层配置加载器附加可信阶段之前的布局失败。
	configDiagnosticLayout = "layout"
)

// configCommand carries one parsed configuration inspection action and its output mode.
// configCommand 用于承载一个已解析的配置检查动作及其输出模式。
type configCommand struct {
	action     string
	configPath string
	jsonOutput bool
}

// configValidationResult is the stable JSON result returned by the validate command.
// configValidationResult 用于表示 validate 命令返回的稳定 JSON 结果。
type configValidationResult struct {
	Valid  bool                    `json:"valid"`
	Errors []configValidationError `json:"errors"`
}

// configValidationError contains a redacted validation message and an optional schema-confirmed field path.
// configValidationError 用于保存已脱敏的校验消息以及可由 schema 确认的字段路径。
type configValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// parseConfigCommand recognizes config subcommands without changing the foreground runtime flag parser.
// parseConfigCommand 用于识别 config 子命令，同时保持前台运行参数解析器不变。
func parseConfigCommand(args []string) (configCommand, bool, error) {
	if len(args) == 0 || args[0] != "config" {
		return configCommand{}, false, nil
	}
	if len(args) < 2 {
		return configCommand{}, true, fmt.Errorf("%s", configCommandUsage)
	}

	action := strings.ToLower(strings.TrimSpace(args[1]))
	if action != "schema" && action != "validate" {
		return configCommand{}, true, fmt.Errorf("unsupported config action %q; %s", args[1], configCommandUsage)
	}
	flags := flag.NewFlagSet("vmm-local config "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "write machine-readable JSON")
	configPath := flags.String("config", "", "user override root or explicit YAML file")
	if err := flags.Parse(args[2:]); err != nil {
		return configCommand{}, true, fmt.Errorf("%w; %s", err, configCommandUsage)
	}
	if flags.NArg() > 0 {
		return configCommand{}, true, fmt.Errorf("unexpected positional arguments: %v; %s", flags.Args(), configCommandUsage)
	}
	if action == "schema" && strings.TrimSpace(*configPath) != "" {
		return configCommand{}, true, fmt.Errorf("config schema does not accept --config; %s", configCommandUsage)
	}
	return configCommand{
		action:     action,
		configPath: strings.TrimSpace(*configPath),
		jsonOutput: *jsonOutput,
	}, true, nil
}

// runConfigCommand executes schema or validation using the same layout and layered loader as normal runtime startup.
// runConfigCommand 使用与正常运行时启动相同的布局解析和分层加载器执行 schema 或校验。
func runConfigCommand(command configCommand, exePath, cwd string, output, errorOutput io.Writer) int {
	switch command.action {
	case "schema":
		if command.jsonOutput {
			if err := config.WriteConfigSchema(output); err != nil {
				fmt.Fprintf(errorOutput, "write configuration schema: %v\n", err)
				return 1
			}
			return 0
		}
		schema := config.ConfigSchema()
		fmt.Fprintf(output, "configuration schema %s: %d fields\n", schema.Version, len(schema.Fields))
		return 0
	case "validate":
		result := validateRuntimeConfig(exePath, cwd, command.configPath)
		if command.jsonOutput {
			encoder := json.NewEncoder(output)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(result); err != nil {
				fmt.Fprintf(errorOutput, "write configuration validation result: %v\n", err)
				return 1
			}
		} else {
			writeTextValidationResult(output, result)
		}
		if result.Valid {
			return 0
		}
		return 1
	default:
		fmt.Fprintf(errorOutput, "%s\n", configCommandUsage)
		return 1
	}
}

// validateRuntimeConfig resolves the packaged and user layers before invoking the real LoadPaths normalization and validation chain.
// validateRuntimeConfig 先解析打包层与用户层，再调用真实的 LoadPaths 归一化和校验链。
func validateRuntimeConfig(exePath, cwd, configPath string) configValidationResult {
	layout, err := config.ResolvePromptLayout(exePath, cwd, configPath)
	if err != nil {
		return invalidConfigResultForStage(configDiagnosticLayout)
	}
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return invalidConfigResult(err)
	}
	// Reuse the startup rule selection and compilation paths without creating database or provider clients.
	// 复用启动时的规则选择与编译路径，同时避免创建数据库或供应商客户端。
	if err := config.ValidatePromptBundle(layout.SystemDir, layout.UserDir, cfg.Prompts.PromptLanguage); err != nil {
		return invalidConfigAssetResult("prompts")
	}
	if err := pii.ValidateRuleDirs(layout.SystemPIIRulesDir(), layout.UserPIIRulesDir(), cfg.PII.DefaultLanguage); err != nil {
		return invalidConfigAssetResult("pii")
	}
	if err := processor.ValidateNoiseRules(layout.SystemNoiseRulesDir(), layout.UserNoiseRulesDir(), cfg.Noise.DefaultLanguage, cfg.Noise.SemanticThreshold); err != nil {
		return invalidConfigAssetResult("noise")
	}
	// Reject data roots that would fail or hide legacy data before the installer commits configuration.
	// 在安装器提交配置前拒绝无法使用或会隐藏历史数据的数据根目录。
	if err := app.PreflightLocalStorageLayout(cfg, layout); err != nil {
		return configValidationResult{Valid: false, Errors: []configValidationError{{Path: "storage.local_data_root", Message: "local storage layout could not be validated"}}}
	}
	if err := app.PreflightNativeStorageLayout(cfg, layout); err != nil {
		return configValidationResult{Valid: false, Errors: []configValidationError{{Message: "native storage layout could not be validated"}}}
	}
	return configValidationResult{Valid: true, Errors: []configValidationError{}}
}

// invalidConfigAssetResult reports a stable asset category without exposing rule text or filesystem paths.
// invalidConfigAssetResult 报告稳定的规则资产类别，避免暴露规则正文或文件系统路径。
func invalidConfigAssetResult(category string) configValidationResult {
	message := "configuration assets could not be validated"
	switch category {
	case "prompts":
		message = "prompt bundle could not be validated"
	case "pii":
		message = "PII rules could not be validated"
	case "noise":
		message = "noise rules could not be validated"
	}
	return configValidationResult{Valid: false, Errors: []configValidationError{{Path: category, Message: message}}}
}

// invalidConfigResult converts one loader or layout error into the stable redacted result shape.
// invalidConfigResult 将一个加载器或布局错误转换为稳定的脱敏结果结构。
func invalidConfigResult(err error) configValidationResult {
	if err == nil {
		return configValidationResult{Valid: false, Errors: []configValidationError{{Message: "configuration validation failed"}}}
	}
	return configValidationResult{
		Valid: false,
		Errors: []configValidationError{{
			Message: safeConfigDiagnostic(err),
		}},
	}
}

// invalidConfigResultForStage returns a generic result for failures that have no trusted structured field path.
// invalidConfigResultForStage 用于为没有可信结构化字段路径的失败返回泛化结果。
func invalidConfigResultForStage(stage string) configValidationResult {
	message := "configuration validation failed"
	switch stage {
	case configDiagnosticLayout:
		message = "configuration layout could not be resolved"
	}
	return configValidationResult{
		Valid: false,
		Errors: []configValidationError{{
			Message: message,
		}},
	}
}

// safeConfigDiagnostic exposes only fixed validator messages and generic phase messages, never raw parser or filesystem text.
// safeConfigDiagnostic 只暴露固定校验器消息和阶段泛化消息，绝不输出原始解析器或文件系统文本。
func safeConfigDiagnostic(err error) string {
	if err == nil {
		return "configuration validation failed"
	}
	stage, ok := config.ConfigLoadErrorStage(err)
	if !ok {
		return "configuration validation failed"
	}
	switch stage {
	case config.ConfigLoadStageValidation:
		// Validation messages are generated by the runtime validator from fixed templates and schema-owned paths.
		// 校验消息由运行时校验器基于固定模板和 schema 所有的路径生成。
		return strings.TrimSpace(err.Error())
	case config.ConfigLoadStageRead:
		return "configuration file could not be read"
	case config.ConfigLoadStageParse:
		return "configuration file could not be parsed"
	case config.ConfigLoadStageEnvironment:
		return "configuration environment requirements could not be satisfied"
	default:
		return "configuration validation failed"
	}
}

// writeTextValidationResult renders the compact human-readable form used when --json is omitted.
// writeTextValidationResult 用于渲染未指定 --json 时使用的简洁文本结果。
func writeTextValidationResult(output io.Writer, result configValidationResult) {
	if result.Valid {
		fmt.Fprintln(output, "configuration valid")
		return
	}
	fmt.Fprintln(output, "configuration invalid")
	for _, item := range result.Errors {
		if item.Path == "" {
			fmt.Fprintf(output, "- %s\n", item.Message)
			continue
		}
		fmt.Fprintf(output, "- %s: %s\n", item.Path, item.Message)
	}
}
