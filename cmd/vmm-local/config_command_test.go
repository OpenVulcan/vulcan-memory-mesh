// config_command_test.go verifies the configuration CLI output, loader integration, and redaction boundary.
// config_command_test.go 用于验证配置 CLI 输出、加载器集成和脱敏边界。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestParseConfigCommand verifies schema and validation flags stay separate from foreground runtime arguments.
// TestParseConfigCommand 用于验证 schema 与 validate 参数和前台运行参数保持隔离。
func TestParseConfigCommand(t *testing.T) {
	command, ok, err := parseConfigCommand([]string{"config", "schema", "--json"})
	if err != nil || !ok || command.action != "schema" || !command.jsonOutput {
		t.Fatalf("parse schema command = %#v, %v, %v", command, ok, err)
	}
	command, ok, err = parseConfigCommand([]string{"config", "validate", "--config", "settings.yaml", "--json"})
	if err != nil || !ok || command.action != "validate" || command.configPath != "settings.yaml" || !command.jsonOutput {
		t.Fatalf("parse validate command = %#v, %v, %v", command, ok, err)
	}
	if _, ok, err = parseConfigCommand([]string{"version"}); err != nil || ok {
		t.Fatalf("non-config command = %v, %v", ok, err)
	}
	if _, ok, err = parseConfigCommand([]string{"config", "schema", "--config", "settings.yaml"}); err == nil || !ok {
		t.Fatal("schema unexpectedly accepted --config")
	}
}

// TestRunConfigSchemaJSON verifies the CLI exposes the authoritative schema without starting the runtime.
// TestRunConfigSchemaJSON 用于验证 CLI 暴露权威 schema 且不会启动运行时。
func TestRunConfigSchemaJSON(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	code := runConfigCommand(configCommand{action: "schema", jsonOutput: true}, "", "", &output, &errors)
	if code != 0 {
		t.Fatalf("schema command code = %d, stderr = %s", code, errors.String())
	}
	var document map[string]any
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode schema JSON: %v", err)
	}
	if document["version"] != "v1" || document["config_type"] != "Config" {
		t.Fatalf("unexpected schema header: %#v", document)
	}
}

// TestRunConfigValidateUsesRealLoader verifies valid overrides, unknown fields, missing files, and required env placeholders.
// TestRunConfigValidateUsesRealLoader 用于验证有效覆盖、未知字段、缺失文件和必填环境变量占位符。
func TestRunConfigValidateUsesRealLoader(t *testing.T) {
	root, exePath := writeConfigCommandFixture(t)
	userDir := filepath.Join(root, "user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("create user dir: %v", err)
	}
	validPath := filepath.Join(userDir, "valid.yaml")
	writeTestFile(t, validPath, "storage:\n  mode: native\nsqlite:\n  native:\n    path: data/sqlite.db\nlancedb:\n  native:\n    path: data/lancedb\n")
	result := runValidationJSON(t, exePath, root, validPath)
	if !result.Valid || len(result.Errors) != 0 {
		t.Fatalf("valid override result = %#v", result)
	}

	unknownPath := filepath.Join(userDir, "unknown.yaml")
	writeTestFile(t, unknownPath, "unknown_field: true\n")
	result = runValidationJSON(t, exePath, root, unknownPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Path != "" || result.Errors[0].Message != "configuration file could not be parsed" {
		t.Fatalf("unknown field result = %#v", result)
	}

	missingPath := filepath.Join(userDir, "missing.yaml")
	result = runValidationJSON(t, exePath, root, missingPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Message != "configuration file could not be read" {
		t.Fatalf("missing config result = %#v", result)
	}

	const requiredKey = "VMMM_CONFIG_COMMAND_REQUIRED_KEY"
	t.Setenv(requiredKey, "")
	envPath := filepath.Join(userDir, "required-env.yaml")
	writeTestFile(t, envPath, "embedding:\n  api_keys:\n    - ${"+requiredKey+"}\n")
	result = runValidationJSON(t, exePath, root, envPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Message != "configuration environment requirements could not be satisfied" {
		t.Fatalf("required env result = %#v", result)
	}

	maliciousPath := filepath.Join(userDir, "malicious.yaml")
	writeTestFile(t, maliciousPath, "unknown_field: 'postgres://user:super-secret@example.invalid/db?token=secret'\n")
	result = runValidationJSON(t, exePath, root, maliciousPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Path != "" || result.Errors[0].Message != "configuration file could not be parsed" {
		t.Fatalf("malicious config result = %#v", result)
	}
	if strings.Contains(result.Errors[0].Message, "super-secret") || strings.Contains(result.Errors[0].Message, "token=secret") {
		t.Fatalf("malicious config value leaked through validation: %#v", result)
	}
}

// TestRunConfigValidateChecksRuleAssets verifies the CLI rejects an invalid selected runtime rule bundle.
// TestRunConfigValidateChecksRuleAssets 验证 CLI 会拒绝无效的已选中运行时规则包。
func TestRunConfigValidateChecksRuleAssets(t *testing.T) {
	root, exePath := writeConfigCommandFixture(t)
	if err := os.WriteFile(filepath.Join(root, "configs", "noise_rules", "common.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runValidationJSON(t, exePath, root, "")
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Path != "noise" || result.Errors[0].Message != "noise rules could not be validated" {
		t.Fatalf("missing noise rule result = %#v", result)
	}
}

// TestRunConfigValidateChecksLocalDataRoot verifies runtime layout failures are caught before installer commit.
// TestRunConfigValidateChecksLocalDataRoot 验证安装器提交前会发现运行时数据布局错误。
func TestRunConfigValidateChecksLocalDataRoot(t *testing.T) {
	root, exePath := writeConfigCommandFixture(t)
	userDir := filepath.Join(root, "user")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(userDir, "config.yaml")
	writeTestFile(t, configPath, "storage:\n  mode: split\n  local_data_root: \""+filepath.ToSlash(filepath.Join(root, "libs"))+"\"\n")
	result := runValidationJSON(t, exePath, root, configPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Path != "storage.local_data_root" {
		t.Fatalf("protected data root result = %#v", result)
	}
	if err := os.MkdirAll(filepath.Join(root, "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "database", "sqlite.db"), "legacy")
	newRoot := filepath.Join(t.TempDir(), "new-data")
	writeTestFile(t, configPath, "storage:\n  mode: split\n  local_data_root: \""+filepath.ToSlash(newRoot)+"\"\n")
	result = runValidationJSON(t, exePath, root, configPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Path != "storage.local_data_root" {
		t.Fatalf("legacy data switch result = %#v", result)
	}
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("validation created data root: %v", err)
	}
}

// TestRunConfigValidateChecksNativeLayout verifies the packaged native database paths cannot overlap.
// TestRunConfigValidateChecksNativeLayout 验证打包环境中的原生数据库路径不能重叠。
func TestRunConfigValidateChecksNativeLayout(t *testing.T) {
	root, exePath := writeConfigCommandFixture(t)
	userDir := filepath.Join(root, "user")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(userDir, "config.yaml")
	writeTestFile(t, configPath, "storage:\n  mode: native\nsqlite:\n  native:\n    path: data/sqlite.db\nlancedb:\n  native:\n    path: data/sqlite.db\n")
	result := runValidationJSON(t, exePath, root, configPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Message != "native storage layout could not be validated" {
		t.Fatalf("overlapping native layout result = %#v", result)
	}
	writeTestFile(t, configPath, "storage:\n  mode: native\nsqlite:\n  native:\n    path: libs/custom.db\nlancedb:\n  native:\n    path: database/native/lancedb\n")
	result = runValidationJSON(t, exePath, root, configPath)
	if result.Valid || len(result.Errors) != 1 || result.Errors[0].Message != "native storage layout could not be validated" {
		t.Fatalf("packaged native path result = %#v", result)
	}
}

// TestSafeConfigDiagnosticRejectsUntrustedText verifies parser and IO text never reaches machine-readable diagnostics.
// TestSafeConfigDiagnosticRejectsUntrustedText 用于验证解析器和 IO 文本不会进入机器可读诊断。
func TestSafeConfigDiagnosticRejectsUntrustedText(t *testing.T) {
	malicious := `postgres://user:super-secret@example.invalid/db access_token="another-secret" endpoint="https://token.invalid/?key=secret"`
	for _, stage := range []config.ConfigLoadStage{config.ConfigLoadStageRead, config.ConfigLoadStageParse, config.ConfigLoadStageEnvironment} {
		err := config.WrapConfigLoadError(stage, errors.New(malicious))
		result := invalidConfigResult(err)
		if strings.Contains(result.Errors[0].Message, "super-secret") || strings.Contains(result.Errors[0].Message, "another-secret") || strings.Contains(result.Errors[0].Message, "token.invalid") {
			t.Fatalf("stage %q leaked untrusted text: %#v", stage, result)
		}
	}
	validation := config.WrapConfigLoadError(config.ConfigLoadStageValidation, errors.New("storage.mode must be one of split, controller, combined, or native"))
	if got := invalidConfigResult(validation).Errors[0].Message; got != "storage.mode must be one of split, controller, combined, or native" {
		t.Fatalf("trusted validator message = %q", got)
	}
}

// runValidationJSON invokes the validation command and decodes its machine-readable result.
// runValidationJSON 调用校验命令并解码其机器可读结果。
func runValidationJSON(t *testing.T, exePath, cwd, configPath string) configValidationResult {
	t.Helper()
	var output bytes.Buffer
	var errors bytes.Buffer
	code := runConfigCommand(configCommand{action: "validate", configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &errors)
	var result configValidationResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode validation JSON (code=%d stderr=%s): %v; output=%s", code, errors.String(), err, output.String())
	}
	return result
}

// writeConfigCommandFixture creates the smallest complete packaged layout accepted by the real runtime loader.
// writeConfigCommandFixture 创建真实运行时加载器能够接受的最小完整打包布局。
func writeConfigCommandFixture(t *testing.T) (root, exePath string) {
	t.Helper()
	root = t.TempDir()
	binDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "configs")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	// Match the host executable name so the fixture is recognized as a packaged runtime on every OS.
	// 按宿主平台命名可执行文件，使测试夹具在每个系统上都被识别为打包运行时。
	executableName := "vmm-local"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	exePath = filepath.Join(binDir, executableName)
	basePath := filepath.Join(configDir, "base.yaml")
	writeTestFile(t, exePath, "")
	writeTestFile(t, filepath.Join(root, "VERSION"), "fixture")
	writeTestFile(t, basePath, `grpc:
  listen_addr: "127.0.0.1:17625"
llm:
  routes:
    - provider: "openai"
      endpoint: "https://example.invalid/v1"
      model: "test-llm"
      api_keys:
        - "llm-key"
      params:
        reasoning_effort: "none"
embedding:
  provider: "openai"
  endpoint: "https://example.invalid/v1"
  api_keys:
    - "embedding-key"
  model: "test-embedding"
  dimension: 3
post_action:
  max_queue_workers: 4
`)
	// Include the runtime's real rule bundles because config validate now checks the same assets as startup.
	// 纳入运行时真实规则包，因为 config validate 现在会检查与启动相同的资产。
	for _, name := range []string{"prompts", "pii_rules", "noise_rules"} {
		copyConfigCommandAssets(t, filepath.Join("..", "..", "configs", name), filepath.Join(configDir, name))
	}
	return root, exePath
}

// copyConfigCommandAssets copies fixture rules so validation uses the real packaged asset shape.
// copyConfigCommandAssets 复制测试规则，让校验使用真实打包资产的目录结构。
func copyConfigCommandAssets(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("test configuration asset is not regular")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
	if err != nil {
		t.Fatalf("copy configuration assets: %v", err)
	}
}

// writeTestFile writes one fixture file with a deterministic permission and error check.
// writeTestFile 写入一个权限确定且带错误检查的测试夹具文件。
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
