// service_command_test.go verifies the portable service-management CLI contract.
// service_command_test.go 用于验证跨平台服务管理 CLI 契约。
package main

import "testing"

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
