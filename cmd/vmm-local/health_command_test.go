// health_command_test.go verifies the bounded, loopback-only Healthz probe contract.
// health_command_test.go 用于验证有界、仅回环的 Healthz 探测契约。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	vmmconfig "github.com/openvulcan/vmm/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeHealthServer provides only Healthz so tests exercise the real client and parser path without a storage process.
// fakeHealthServer 只提供 Healthz，使测试在不启动存储进程的情况下覆盖真实客户端和解析路径。
type fakeHealthServer struct {
	vmmv1.UnimplementedVMMServiceServer
	response *vmmv1.HealthzResponse
	err      error
}

// Healthz returns the configured fake response or dependency failure.
// Healthz 返回配置的伪造响应或依赖失败。
func (s *fakeHealthServer) Healthz(context.Context, *emptypb.Empty) (*vmmv1.HealthzResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

// TestParseHealthCommandRequiresAbsoluteConfig verifies the explicit absolute config boundary and flag handling.
// TestParseHealthCommandRequiresAbsoluteConfig 验证显式绝对配置路径边界和参数解析。
func TestParseHealthCommandRequiresAbsoluteConfig(t *testing.T) {
	if _, ok, err := parseHealthCommand([]string{"version"}); err != nil || ok {
		t.Fatalf("non-health args should be ignored, ok=%v err=%v", ok, err)
	}
	if _, ok, err := parseHealthCommand([]string{"health", "--json"}); !ok || err == nil {
		t.Fatalf("missing config should fail, ok=%v err=%v", ok, err)
	}
	if _, ok, err := parseHealthCommand([]string{"health", "--config", "configs/config.yaml"}); !ok || err == nil {
		t.Fatalf("relative config should fail, ok=%v err=%v", ok, err)
	}
	absolute, err := filepath.Abs(filepath.Join("configs", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	command, ok, err := parseHealthCommand([]string{"health", "--config=" + absolute, "--json"})
	if err != nil || !ok {
		t.Fatalf("absolute config should parse, ok=%v err=%v", ok, err)
	}
	if command.configPath != filepath.Clean(absolute) || !command.jsonOutput {
		t.Fatalf("parsed command = %#v", command)
	}
	if _, ok, err := parseHealthCommand([]string{"health", "--config", absolute, "--config", absolute}); !ok || err == nil {
		t.Fatalf("duplicate config should fail, ok=%v err=%v", ok, err)
	}
}

// TestValidateHealthAddressRejectsNonLoopbackTargets verifies that health cannot be redirected to a public or wildcard host.
// TestValidateHealthAddressRejectsNonLoopbackTargets 验证健康探测不会被重定向到公网或通配地址。
func TestValidateHealthAddressRejectsNonLoopbackTargets(t *testing.T) {
	allowed := []struct {
		address string
		want    string
	}{
		{address: "127.0.0.1:1", want: "127.0.0.1:1"},
		{address: "[::1]:1", want: "[::1]:1"},
		{address: "localhost:1", want: "localhost:1"},
		{address: "[::ffff:127.0.0.1]:1", want: "[::ffff:127.0.0.1]:1"},
		{address: ":8080", want: "127.0.0.1:8080"},
		{address: "0.0.0.0:8080", want: "127.0.0.1:8080"},
		{address: "[::]:8080", want: "[::1]:8080"},
	}
	for _, test := range allowed {
		got, err := validateHealthAddress(test.address)
		if err != nil {
			t.Errorf("loopback address %q rejected: %v", test.address, err)
		}
		if got != test.want {
			t.Errorf("validated address %q = %q, want %q", test.address, got, test.want)
		}
	}
	rejected := []string{"", "8.8.8.8:1", "example.com:1", "unix:///tmp/vmm.sock"}
	for _, address := range rejected {
		if _, err := validateHealthAddress(address); err == nil {
			t.Errorf("unsafe address %q was accepted", address)
		}
	}
}

// TestRunHealthCommandMapsWildcardToLoopback verifies a wildcard config reaches a real local fake server through loopback.
// TestRunHealthCommandMapsWildcardToLoopback 验证通配配置会通过回环地址连接真实本地伪服务器。
func TestRunHealthCommandMapsWildcardToLoopback(t *testing.T) {
	listener, server := startFakeHealthServer(t, &fakeHealthServer{response: &vmmv1.HealthzResponse{Status: "ok"}})
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	exePath, cwd, configPath := writeHealthFixture(t, ":"+port, "")
	var output bytes.Buffer
	code := runHealthCommand(healthCommand{configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("wildcard probe exit code = %d, output=%s", code, output.String())
	}
	var result healthResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Class != healthClassOK {
		t.Fatalf("wildcard result = %#v", result)
	}
	server.Stop()
}

// TestRunHealthCommandHealthy uses a fake VMMService endpoint and checks that only stable non-sensitive fields are emitted.
// TestRunHealthCommandHealthy 使用伪造 VMMService endpoint，并检查输出只包含稳定的非敏感字段。
func TestRunHealthCommandHealthy(t *testing.T) {
	listener, server := startFakeHealthServer(t, &fakeHealthServer{response: &vmmv1.HealthzResponse{Status: "ok", TraceId: "secret-trace"}})
	exePath, cwd, configPath := writeHealthFixture(t, listener.Addr().String(), "")
	var output bytes.Buffer
	code := runHealthCommand(healthCommand{configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("healthy probe exit code = %d, output=%s", code, output.String())
	}
	var result healthResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Status != "ok" || result.Class != healthClassOK || result.ElapsedMsec < 0 {
		t.Fatalf("health result = %#v", result)
	}
	if strings.Contains(output.String(), "secret-trace") || strings.Contains(output.String(), listener.Addr().String()) {
		t.Fatalf("health output leaked endpoint or trace: %s", output.String())
	}
	server.Stop()
}

// TestRunHealthCommandStorageFailureDistinguishesDependencyError verifies VMM's Unavailable Healthz contract is classified as storage failure.
// TestRunHealthCommandStorageFailureDistinguishesDependencyError 验证 VMM 的 Unavailable Healthz 契约会归类为存储失败。
func TestRunHealthCommandStorageFailureDistinguishesDependencyError(t *testing.T) {
	listener, server := startFakeHealthServer(t, &fakeHealthServer{err: status.Error(codes.Unavailable, "runtime storage is unavailable: api_key=secret")})
	exePath, cwd, configPath := writeHealthFixture(t, listener.Addr().String(), "")
	var output bytes.Buffer
	code := runHealthCommand(healthCommand{configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &bytes.Buffer{})
	if code == 0 {
		t.Fatalf("storage failure should return non-zero, output=%s", output.String())
	}
	var result healthResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Class != healthClassStorage || result.Error != "storage_health_failed" {
		t.Fatalf("storage result = %#v", result)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "api_key") {
		t.Fatalf("health output leaked server error: %s", output.String())
	}
	server.Stop()
}

// TestRunHealthCommandSeparatesConfigurationFailure verifies invalid configuration is reported before dialing.
// TestRunHealthCommandSeparatesConfigurationFailure 验证无效配置会在连接前被单独报告。
func TestRunHealthCommandSeparatesConfigurationFailure(t *testing.T) {
	exePath, cwd, configPath := writeHealthFixture(t, "127.0.0.1:1", "storage:\n  mode: invalid-mode\n")
	var output bytes.Buffer
	code := runHealthCommand(healthCommand{configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &bytes.Buffer{})
	if code == 0 {
		t.Fatalf("invalid config should return non-zero, output=%s", output.String())
	}
	var result healthResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Class != healthClassConfiguration || result.Error != "configuration_validation" {
		t.Fatalf("configuration result = %#v", result)
	}
}

// TestRunHealthCommandRejectsPublicConfigAddress verifies the endpoint policy is treated as configuration failure.
// TestRunHealthCommandRejectsPublicConfigAddress 验证 endpoint 策略违规会被视为配置失败。
func TestRunHealthCommandRejectsPublicConfigAddress(t *testing.T) {
	exePath, cwd, configPath := writeHealthFixture(t, "203.0.113.10:8080", "")
	var output bytes.Buffer
	code := runHealthCommand(healthCommand{configPath: configPath, jsonOutput: true}, exePath, cwd, &output, &bytes.Buffer{})
	if code == 0 {
		t.Fatalf("public endpoint should return non-zero, output=%s", output.String())
	}
	var result healthResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v; output=%s", err, output.String())
	}
	if result.Class != healthClassConfiguration || result.Error != "grpc_address_invalid" {
		t.Fatalf("public address result = %#v", result)
	}
}

// startFakeHealthServer binds a real loopback TCP listener so grpc.DialContext follows the production transport path.
// startFakeHealthServer 绑定真实回环 TCP listener，使 grpc.DialContext 走生产传输路径。
func startFakeHealthServer(t *testing.T, implementation vmmv1.VMMServiceServer) (net.Listener, *grpc.Server) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	vmmv1.RegisterVMMServiceServer(server, implementation)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener, server
}

// writeHealthFixture creates a minimal packaged config root and an explicit user YAML layer for the real loader.
// writeHealthFixture 创建最小打包配置根和显式用户 YAML 层，供真实加载器使用。
func writeHealthFixture(t *testing.T, address, extra string) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	systemDir := filepath.Join(root, "configs")
	binDir := filepath.Join(root, "bin")
	userDir := filepath.Join(root, "user")
	for _, directory := range []string{systemDir, binDir, userDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	baseBody, err := json.Marshal(vmmconfig.DefaultBase())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "base.yaml"), baseBody, 0o644); err != nil {
		t.Fatal(err)
	}
	body := "grpc:\n  listen_addr: \"" + address + "\"\n" +
		"llm:\n  routes:\n    - provider: openai\n      endpoint: https://example.invalid/v1\n      api_keys: [test-key]\n      model: test-model\n      params:\n        reasoning_effort: none\n" +
		"embedding:\n  provider: openai\n  endpoint: https://example.invalid/v1\n  api_keys: [test-key]\n  model: test-embedding\n  dimension: 3\n" + extra
	configPath := filepath.Join(userDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(binDir, "vmm-local.exe"), root, configPath
}
