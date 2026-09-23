// service_command_test.go verifies the portable service-management CLI contract.
// service_command_test.go 用于验证跨平台服务管理 CLI 契约。
package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestParseServiceCommandDefaultsName verifies service commands use the project default when no explicit service name is supplied.
// TestParseServiceCommandDefaultsName 用于验证服务命令在未显式传入服务名时会使用项目默认值。
func TestParseServiceCommandDefaultsName(t *testing.T) {
	command, ok, err := parseServiceCommand([]string{"service", "install"})
	if err != nil {
		t.Fatalf("parseServiceCommand returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected service command to be recognized")
	}
	if command.action != "install" {
		t.Fatalf("expected install action, got %q", command.action)
	}
	if command.name != defaultServiceName {
		t.Fatalf("expected default service name %q, got %q", defaultServiceName, command.name)
	}
	if !command.autoStart {
		t.Fatal("expected install to default to auto-start")
	}
}

// TestParseServiceCommandAcceptsCustomName verifies the only optional service argument is the service name itself.
// TestParseServiceCommandAcceptsCustomName 用于验证服务命令唯一可选参数就是服务名本身。
func TestParseServiceCommandAcceptsCustomName(t *testing.T) {
	command, ok, err := parseServiceCommand([]string{"service", "start", "VMM.Dev_01"})
	if err != nil {
		t.Fatalf("parseServiceCommand returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected service command to be recognized")
	}
	if command.action != "start" || command.name != "VMM.Dev_01" {
		t.Fatalf("unexpected command: %#v", command)
	}
}

// TestParseServiceCommandRejectsExtraArguments verifies service registration cannot smuggle runtime configuration into the service definition.
// TestParseServiceCommandRejectsExtraArguments 用于验证服务注册不能把运行时配置参数偷偷塞进服务定义。
func TestParseServiceCommandRejectsExtraArguments(t *testing.T) {
	_, ok, err := parseServiceCommand([]string{"service", "install", "VMM", "-config", "custom"})
	if !ok {
		t.Fatalf("expected service command to be recognized")
	}
	if err == nil {
		t.Fatalf("expected extra arguments to be rejected")
	}
}

// TestParseServiceCommandPersistsAbsoluteConfigAndManualStart verifies service registration normalizes the explicit machine-level configuration path and start policy.
// TestParseServiceCommandPersistsAbsoluteConfigAndManualStart 用于验证服务注册会规范化显式机器级配置路径与手动启动策略。
func TestParseServiceCommandPersistsAbsoluteConfigAndManualStart(t *testing.T) {
	configRoot := t.TempDir()
	command, ok, err := parseServiceCommand([]string{"service", "install", "VMM.Dev", "-config", configRoot, "-auto-start=false"})
	if err != nil {
		t.Fatalf("parseServiceCommand returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected service command to be recognized")
	}
	if command.configPath != filepath.Clean(configRoot) {
		t.Fatalf("config path = %q, want %q", command.configPath, filepath.Clean(configRoot))
	}
	if command.autoStart {
		t.Fatal("expected auto-start=false")
	}
}

// TestParseServiceCommandAcceptsNotYetCreatedYAML verifies a manager can register a YAML path before creating its first configuration file.
// TestParseServiceCommandAcceptsNotYetCreatedYAML 用于验证管理器可以先注册 YAML 路径，再创建首份配置文件。
func TestParseServiceCommandAcceptsNotYetCreatedYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	command, ok, err := parseServiceCommand([]string{"service", "run", "-config=" + configPath})
	if err != nil {
		t.Fatalf("parseServiceCommand returned error: %v", err)
	}
	if !ok || command.configPath != filepath.Clean(configPath) {
		t.Fatalf("unexpected command: %#v", command)
	}
}

// TestParseServiceCommandRejectsLifecycleConfig verifies lifecycle operations cannot pretend to change a persisted service command line.
// TestParseServiceCommandRejectsLifecycleConfig 用于验证生命周期操作不能伪装修改已持久化的服务命令行。
func TestParseServiceCommandRejectsLifecycleConfig(t *testing.T) {
	_, ok, err := parseServiceCommand([]string{"service", "restart", "-config", t.TempDir()})
	if !ok {
		t.Fatal("expected service command to be recognized")
	}
	if err == nil || !strings.Contains(err.Error(), "does not accept -config") {
		t.Fatalf("expected explicit lifecycle config error, got %v", err)
	}
}

// TestParseServiceCommandRejectsUnknownAndDuplicateFlags verifies the strict parser never silently ignores registration mistakes.
// TestParseServiceCommandRejectsUnknownAndDuplicateFlags 用于验证严格解析器不会静默忽略注册参数错误。
func TestParseServiceCommandRejectsUnknownAndDuplicateFlags(t *testing.T) {
	configRoot := t.TempDir()
	cases := [][]string{
		{"service", "install", "-unknown"},
		{"service", "install", "-config", configRoot, "-config", configRoot},
		{"service", "install", "-auto-start=maybe"},
		{"service", "install", "-config", configRoot, "-user", "bad/account"},
		{"service", "install", "-user", "root"},
	}
	for _, args := range cases {
		_, ok, err := parseServiceCommand(args)
		if !ok {
			t.Fatalf("expected service command for %v", args)
		}
		if err == nil {
			t.Fatalf("expected parse error for %v", args)
		}
	}
}

// TestParseServiceCommandRejectsUnsafeName verifies generated service files stay path-safe across all supported platforms.
// TestParseServiceCommandRejectsUnsafeName 用于验证生成的服务文件在所有受支持平台上都保持路径安全。
func TestParseServiceCommandRejectsUnsafeName(t *testing.T) {
	_, ok, err := parseServiceCommand([]string{"service", "install", "../VMM"})
	if !ok {
		t.Fatalf("expected service command to be recognized")
	}
	if err == nil {
		t.Fatalf("expected unsafe service name to be rejected")
	}
}

// TestParseServiceCommandIgnoresRuntimeFlags verifies ordinary runtime flags remain owned by the normal flag parser.
// TestParseServiceCommandIgnoresRuntimeFlags 用于验证普通运行时参数仍由常规 flag 解析器处理。
func TestParseServiceCommandIgnoresRuntimeFlags(t *testing.T) {
	_, ok, err := parseServiceCommand([]string{"-config", "D:\\vmm"})
	if err != nil {
		t.Fatalf("parseServiceCommand returned error: %v", err)
	}
	if ok {
		t.Fatalf("did not expect runtime flags to be treated as service commands")
	}
}
