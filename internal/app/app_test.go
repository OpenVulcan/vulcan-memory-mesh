// app_test.go verifies runtime-only composition details such as gRPC reflection registration without depending on external gateways.
// app_test.go 用于在不依赖外部网关的前提下，验证 gRPC 反射注册等仅运行时装配细节。
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// newRuntimeConfigForTest builds one runtime-ready config fixture that follows the new AI contract:
// LLM uses explicit routes, embedding stays single-provider/single-model with a multi-key pool, and rerank remains opt-in.
// newRuntimeConfigForTest 用于构建一份符合新 AI 契约的运行时测试配置：LLM 使用显式 routes，embedding 保持单 provider/单模型但支持多 key 池，rerank 默认按需启用。
func newRuntimeConfigForTest() config.Config {
	cfg := config.DefaultLocal()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("openai", "https://example.com/v1", []string{"test-llm-key"}, "test-llm"),
	}
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.Endpoint = "https://example.com/v1"
	cfg.Embedding.APIKeys = []string{"test-embedding-key"}
	cfg.Embedding.Model = "test-embedding"
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = false
	return cfg
}

// configurePackagedNativeStorageForTest selects isolated native files when the release job supplies its verified LanceDB library.
// configurePackagedNativeStorageForTest 在发行任务提供已验证 LanceDB 库时选用隔离的原生文件。
func configurePackagedNativeStorageForTest(t *testing.T, cfg *config.Config) {
	t.Helper()
	library := os.Getenv("VMM_NATIVE_LANCEDB_LIBRARY")
	if library == "" {
		return
	}
	storageRoot := t.TempDir()
	cfg.Storage.Mode = "native"
	cfg.SQLite.Native.Path = filepath.Join(storageRoot, "sqlite.db")
	cfg.LanceDB.Native.Path = filepath.Join(storageRoot, "lancedb")
	cfg.LanceDB.Native.LibraryPath = library
}

// TestBuildGRPCKeepaliveConfigurationMapsConfigValues verifies the transport helper preserves the configured keepalive timings and client-ping policy before the runtime builds the gRPC server.
// TestBuildGRPCKeepaliveConfigurationMapsConfigValues 用于验证在运行时构建 gRPC 服务之前，传输层辅助函数会保留配置中的 keepalive 时序和客户端 ping 策略。
func TestBuildGRPCKeepaliveConfigurationMapsConfigValues(t *testing.T) {
	params, policy, ok := buildGRPCKeepaliveConfiguration(config.GRPCKeepaliveConfig{
		Enabled:               true,
		Time:                  config.Duration{Duration: 45 * time.Second},
		Timeout:               config.Duration{Duration: 12 * time.Second},
		MaxConnectionIdle:     config.Duration{Duration: 3 * time.Minute},
		MaxConnectionAge:      config.Duration{Duration: 15 * time.Minute},
		MaxConnectionAgeGrace: config.Duration{Duration: 30 * time.Second},
		MinPingInterval:       config.Duration{Duration: 25 * time.Second},
		PermitWithoutStream:   false,
	})
	if !ok {
		t.Fatal("expected grpc keepalive configuration to be enabled")
	}
	if got, want := params, (keepalive.ServerParameters{
		Time:                  45 * time.Second,
		Timeout:               12 * time.Second,
		MaxConnectionIdle:     3 * time.Minute,
		MaxConnectionAge:      15 * time.Minute,
		MaxConnectionAgeGrace: 30 * time.Second,
	}); got != want {
		t.Fatalf("grpc keepalive params = %#v, want %#v", got, want)
	}
	if got, want := policy, (keepalive.EnforcementPolicy{
		MinTime:             25 * time.Second,
		PermitWithoutStream: false,
	}); got != want {
		t.Fatalf("grpc keepalive enforcement policy = %#v, want %#v", got, want)
	}
}

// TestBuildGRPCKeepaliveConfigurationSupportsDisable verifies the runtime can explicitly skip keepalive server options when operators want to preserve a fully passive transport posture.
// TestBuildGRPCKeepaliveConfigurationSupportsDisable 用于验证当运维方希望保持完全被动的传输层行为时，运行时可以显式跳过 keepalive 服务端选项。
func TestBuildGRPCKeepaliveConfigurationSupportsDisable(t *testing.T) {
	params, policy, ok := buildGRPCKeepaliveConfiguration(config.GRPCKeepaliveConfig{})
	if ok {
		t.Fatal("expected disabled grpc keepalive configuration to be skipped")
	}
	if got, want := params, (keepalive.ServerParameters{}); got != want {
		t.Fatalf("grpc keepalive params = %#v, want %#v", got, want)
	}
	if got, want := policy, (keepalive.EnforcementPolicy{}); got != want {
		t.Fatalf("grpc keepalive enforcement policy = %#v, want %#v", got, want)
	}
}

// newLLMRouteForTest builds one explicit LLM route fixture so runtime tests always exercise the route-only configuration path.
// newLLMRouteForTest 用于构造一条显式 LLM 路由测试夹具，确保运行时测试始终走 route-only 配置路径。
func newLLMRouteForTest(provider string, endpoint string, apiKeys []string, model string) config.LLMRouteConfig {
	return config.LLMRouteConfig{
		Provider: provider,
		Endpoint: endpoint,
		APIKeys:  append([]string(nil), apiKeys...),
		Model:    model,
		Params:   map[string]any{"reasoning_effort": "none"},
	}
}

// intPtrForAppTest returns one stable integer pointer so runtime composition tests can express explicit per-scene route weights.
// intPtrForAppTest 用于返回稳定的整数指针，让运行时装配测试可以表达显式的分场景路由权重。
func intPtrForAppTest(value int) *int {
	return &value
}

// newRerankRouteForTest builds one explicit rerank route fixture so tests never rely on removed top-level rerank fields.
// newRerankRouteForTest 用于构造一条显式 rerank 路由测试夹具，避免测试再依赖已移除的顶层 rerank 字段。
func newRerankRouteForTest(provider string, endpoint string, apiKeys []string, model string, timeout time.Duration) config.RerankRouteConfig {
	return config.RerankRouteConfig{
		Provider: provider,
		Endpoint: endpoint,
		APIKeys:  append([]string(nil), apiKeys...),
		Model:    model,
		Timeout:  config.Duration{Duration: timeout},
	}
}

// recordingLLMClient captures the last forwarded request so composition tests can verify whether runtime wrappers preserve or clear the request-level model pin.
// recordingLLMClient 用于记录最后一次转发请求，让装配测试可以验证运行时包装器是否保留或清空了请求级模型固定值。
type recordingLLMClient struct {
	lastRequest appports.LLMRequest
	response    appports.LLMResponse
	err         error
}

// Generate records one request and then returns the canned response or error.
// Generate 用于记录一次请求，然后返回预置响应或错误。
func (c *recordingLLMClient) Generate(_ context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	c.lastRequest = req
	if c.err != nil {
		return c.response, c.err
	}
	return c.response, nil
}

// matchingPromptSource satisfies the prompt source contract for composition tests that only care about request wiring instead of real prompt-file contents.
// matchingPromptSource 用于满足装配测试的提示词接口；这些测试只关心请求接线，而不关心真实提示词文件内容。
type matchingPromptSource struct{}

// GetPrompt returns one deterministic in-memory prompt body for composition tests.
// GetPrompt 用于为装配测试返回一份稳定的内存提示词内容。
func (s matchingPromptSource) GetPrompt(scene, modelName string) (string, error) {
	return scene + ":" + modelName, nil
}

// TestNewLocalRegistersReflection verifies the local runtime exposes both the main VMM service and gRPC reflection.
// TestNewLocalRegistersReflection 用于验证本地运行时会同时暴露主 VMM 服务和 gRPC reflection。
func TestNewLocalRegistersReflection(t *testing.T) {
	// Build prompt layout from real repository assets so the composition root still uses the shipped prompt tree.
	// 基于仓库里的真实资源构建提示词布局，确保组合根仍然使用随仓库发布的提示词目录。
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"),
		filepath.Join(root, "cmd", "vmm-local"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := newRuntimeConfigForTest()
	configurePackagedNativeStorageForTest(t, &cfg)

	app, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.Shutdown(context.Background())
	})
	info := app.Server.GetServiceInfo()
	if _, ok := info["grpc.reflection.v1alpha.ServerReflection"]; !ok {
		t.Fatalf("expected gRPC reflection to be registered, got services: %#v", info)
	}
	if _, ok := info["vmm.v1.VMMService"]; !ok {
		t.Fatalf("expected VMM service to be registered, got services: %#v", info)
	}
}

// TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs verifies runtime file logs stay next to the resolved system config root so packaged binaries use `output/logs` while go-run development uses the repository `logs` directory.
// TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs 用于验证运行时文件日志会落在系统配置根的同级目录；这样打包二进制走 `output/logs`，而 go run 调试走仓库根 `logs`。
func TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs(t *testing.T) {
	logDir, err := resolveRuntimeLogDir(config.PromptLayout{SystemDir: filepath.Join("D:", "repo", "output", "configs")})
	if err != nil {
		t.Fatalf("resolve runtime log dir for packaged layout: %v", err)
	}
	if want := filepath.Join("D:", "repo", "output", "logs"); logDir != want {
		t.Fatalf("packaged log dir = %q, want %q", logDir, want)
	}

	logDir, err = resolveRuntimeLogDir(config.PromptLayout{SystemDir: filepath.Join("D:", "repo", "configs")})
	if err != nil {
		t.Fatalf("resolve runtime log dir for go-run layout: %v", err)
	}
	if want := filepath.Join("D:", "repo", "logs"); logDir != want {
		t.Fatalf("go-run log dir = %q, want %q", logDir, want)
	}
}

// TestNewLocalCreatesRuntimeLogFile verifies application composition eagerly creates the day/hour log file under the resolved runtime log root so startup fails early on invalid paths instead of silently dropping logs later.
// TestNewLocalCreatesRuntimeLogFile 用于验证应用装配会在解析出的运行时日志根目录下立即创建按天/小时的日志文件，让路径异常在启动阶段就暴露出来，而不是之后静默丢日志。
func TestNewLocalCreatesRuntimeLogFile(t *testing.T) {
	root := t.TempDir()
	writePromptBundleForAppTest(t, filepath.Join(root, "output", "configs", "prompts", "default_en"), "packaged-default")
	writeConfigStubForAppTest(t, filepath.Join(root, "output", "configs", "base.yaml"))
	writeConfigStubForAppTest(t, filepath.Join(root, "output", "configs", "config.yaml"))
	writeRuleStubsForAppTest(t, filepath.Join(root, "output", "configs"))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(root, "output", "bin", "vmm-local.exe"),
		filepath.Join(root, "cmd", "vmm-local"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := newRuntimeConfigForTest()
	configurePackagedNativeStorageForTest(t, &cfg)

	application, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = application.Shutdown(context.Background())
	})

	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		t.Fatal(err)
	}
	dayDir := filepath.Join(logDir, time.Now().Local().Format("20060102"))
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatalf("read runtime log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one hourly log file in %s", dayDir)
	}
}

// TestNewLocalCreatesDedicatedLLMLogFileWhenEnabled verifies the dedicated LLM output log file is created when the LLM output switch is enabled.
// TestNewLocalCreatesDedicatedLLMLogFileWhenEnabled 用于验证专用 LLM 输出日志文件在 LLM 输出开关开启时会创建。
func TestNewLocalCreatesDedicatedLLMLogFileWhenEnabled(t *testing.T) {
	root := t.TempDir()
	writePromptBundleForAppTest(t, filepath.Join(root, "output", "configs", "prompts", "default_en"), "packaged-default")
	writeConfigStubForAppTest(t, filepath.Join(root, "output", "configs", "base.yaml"))
	writeConfigStubForAppTest(t, filepath.Join(root, "output", "configs", "config.yaml"))
	writeRuleStubsForAppTest(t, filepath.Join(root, "output", "configs"))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(root, "output", "bin", "vmm-local.exe"),
		filepath.Join(root, "cmd", "vmm-local"),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, "")
	if err != nil {
		t.Fatal(err)
	}

	cfg := newRuntimeConfigForTest()
	configurePackagedNativeStorageForTest(t, &cfg)
	cfg.Logging.LLMOutputEnabled = true

	application, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = application.Shutdown(context.Background())
	})

	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Local()
	llmLogPath := filepath.Join(logDir, now.Format("20060102"), "LLM-"+now.Format("2006010215")+".log")
	if _, err := os.Stat(llmLogPath); err != nil {
		t.Fatalf("expected dedicated llm log file %s to exist: %v", llmLogPath, err)
	}
}

// TestWrapLLMWithOutputLoggerRecordsResponseMetadata verifies the dedicated LLM output logger writes response content, model identity, elapsed time, and token accounting without logging prompt input text.
// TestWrapLLMWithOutputLoggerRecordsResponseMetadata 用于验证专用 LLM 输出日志会记录响应内容、模型标识、耗时和 token 统计，同时不会写入输入 prompt 文本。
func TestWrapLLMWithOutputLoggerRecordsResponseMetadata(t *testing.T) {
	var logBuf bytes.Buffer
	logger := logx.New(&logBuf, logx.Config{Level: "info", Format: "text"})
	upstream := &recordingLLMClient{
		response: appports.LLMResponse{
			Content: "{\"ok\":true}",
			Model:   "qwen3.5-32b",
			Usage:   appports.LLMResponse{}.Usage,
		},
	}
	upstream.response.Usage.PromptTokens = 11
	upstream.response.Usage.CompletionTokens = 7
	upstream.response.Usage.TotalTokens = 18

	client := wrapLLMWithOutputLogger(upstream, logger)
	if _, err := client.Generate(context.Background(), appports.LLMRequest{
		SystemPrompt:        "system prompt should stay hidden",
		UserPrompt:          "user prompt should stay hidden",
		ResponseFormat:      appports.LLMResponseFormatJSON,
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL1,
	}); err != nil {
		t.Fatalf("generate with llm output logger: %v", err)
	}

	output := logBuf.String()
	for _, fragment := range []string{
		`MSG："llm output captured"`,
		`scene："postaction_l1"`,
		`model："qwen3.5-32b"`,
		`prompt_tokens：11`,
		`completion_tokens：7`,
		`total_tokens：18`,
		`JSON(llm_output)：`,
		`"ok": true`,
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("expected llm output log to contain %s, got %s", fragment, output)
		}
	}
	if strings.Contains(output, "system prompt should stay hidden") || strings.Contains(output, "user prompt should stay hidden") {
		t.Fatalf("expected llm output log to exclude prompt inputs, got %s", output)
	}
}

// TestWrapLLMWithOutputLoggerDoesNotInventResponseModel verifies an omitted provider model remains absent instead of being replaced by the configured request model.
// TestWrapLLMWithOutputLoggerDoesNotInventResponseModel 用于验证供应商未返回模型名时保持缺失状态，而不会被请求中的配置模型冒充。
func TestWrapLLMWithOutputLoggerDoesNotInventResponseModel(t *testing.T) {
	var logBuf bytes.Buffer
	logger := logx.New(&logBuf, logx.Config{Level: "info", Format: "json"})
	upstream := &recordingLLMClient{
		response: appports.LLMResponse{Content: `{"ok":true}`},
	}

	client := wrapLLMWithOutputLogger(upstream, logger)
	if _, err := client.Generate(context.Background(), appports.LLMRequest{
		Model:               "standalone-default-model",
		ResponseFormat:      appports.LLMResponseFormatJSON,
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL1,
	}); err != nil {
		t.Fatalf("generate with llm output logger: %v", err)
	}

	// loggedFields decodes the structured log entry so exact key presence can be asserted without depending on text rendering punctuation.
	// loggedFields 解码结构化日志条目，从而无需依赖文本渲染标点即可精确断言字段是否存在。
	loggedFields := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(logBuf.Bytes()), &loggedFields); err != nil {
		t.Fatalf("decode llm output log: %v; output=%s", err, logBuf.String())
	}
	if got := loggedFields["configured_model"]; got != "standalone-default-model" {
		t.Fatalf("configured_model = %#v, want standalone-default-model", got)
	}
	if got := loggedFields["response_model"]; got != "" {
		t.Fatalf("response_model = %#v, want empty provider value", got)
	}
	if inventedModel, exists := loggedFields["model"]; exists {
		t.Fatalf("model field invented provider identity %#v", inventedModel)
	}
}

// TestWrapLLMWithOutputLoggerPreservesCompletedResponseOnValidationFailure verifies a completed provider response keeps its body, physical identity, and full usage when a later model-drift check returns an error.
// TestWrapLLMWithOutputLoggerPreservesCompletedResponseOnValidationFailure 用于验证供应商已完成响应但后续模型漂移校验返回错误时，日志仍保留正文、物理身份与完整用量。
func TestWrapLLMWithOutputLoggerPreservesCompletedResponseOnValidationFailure(t *testing.T) {
	var logBuf bytes.Buffer
	logger := logx.New(&logBuf, logx.Config{Level: "info", Format: "json"})
	upstream := &recordingLLMClient{
		response: appports.LLMResponse{
			Content:   `{"ok":true}`,
			Model:     "provider-drifted-model",
			RequestID: "req-drift-1",
		},
		err: errors.New("physical model drifted"),
	}
	upstream.response.Usage.PromptTokens = 120
	upstream.response.Usage.CompletionTokens = 30
	upstream.response.Usage.TotalTokens = 150
	upstream.response.Usage.CachedInputTokens = 80
	upstream.response.Usage.ReasoningTokens = 0

	client := wrapLLMWithOutputLogger(upstream, logger)
	_, err := client.Generate(context.Background(), appports.LLMRequest{
		Model:               "configured-model",
		ResponseFormat:      appports.LLMResponseFormatJSON,
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL2,
	})
	if err == nil || !strings.Contains(err.Error(), "physical model drifted") {
		t.Fatalf("expected model drift failure, got %v", err)
	}

	loggedFields := map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(logBuf.Bytes()), &loggedFields); err != nil {
		t.Fatalf("decode failed LLM response log: %v; output=%s", err, logBuf.String())
	}
	for field, expected := range map[string]any{
		"configured_model":    "configured-model",
		"response_model":      "provider-drifted-model",
		"request_id":          "req-drift-1",
		"prompt_tokens":       float64(120),
		"completion_tokens":   float64(30),
		"total_tokens":        float64(150),
		"cached_input_tokens": float64(80),
		"reasoning_tokens":    float64(0),
	} {
		if got := loggedFields[field]; got != expected {
			t.Fatalf("%s = %#v, want %#v; output=%s", field, got, expected, logBuf.String())
		}
	}
	if !strings.Contains(logBuf.String(), `\"ok\":true`) {
		t.Fatalf("expected failed completed response body in log, got %s", logBuf.String())
	}
}

// writePromptBundleForAppTest creates one complete prompt bundle for runtime composition tests that need an isolated packaged config layout.
// writePromptBundleForAppTest 用于为需要隔离打包配置布局的运行时装配测试创建一套完整提示词包。
func writePromptBundleForAppTest(t *testing.T, dir string, prefix string) {
	t.Helper()
	for _, scene := range []string{
		"precheck_l1_main.md",
		"precheck_l2_main.md",
		"postaction_l1_main.md",
		"postaction_l2_main.md",
		"profile_instruction_main.md",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, scene), []byte(prefix+":"+scene), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeConfigStubForAppTest writes one minimal YAML config file so layout resolution can exercise packaged-path behavior without depending on repository build artifacts.
// writeConfigStubForAppTest 用于写入最小 YAML 配置文件，让布局解析测试能够验证打包路径行为，而不依赖仓库现成构建产物。
func writeConfigStubForAppTest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("grpc: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRuleStubsForAppTest writes one minimal noise/pii rule set so isolated packaged-layout tests can boot the local app without relying on repository assets.
// writeRuleStubsForAppTest 用于写入最小 noise/pii 规则集，让隔离打包布局测试能够启动本地应用，而不依赖仓库现成资源。
func writeRuleStubsForAppTest(t *testing.T, configRoot string) {
	t.Helper()
	noiseDir := filepath.Join(configRoot, "noise_rules")
	if err := os.MkdirAll(noiseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noiseDir, "common.json"), []byte(`{
  "language": "common",
  "version": "1.0.0",
  "categories": [
    {
      "name": "stub-common",
      "targets": ["user"],
      "threshold": 0.9,
      "patterns": ["^stub$"],
      "phrases": ["stub"]
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noiseDir, "zh-CN.json"), []byte(`{
  "language": "zh-CN",
  "version": "1.0.0",
  "categories": [
    {
      "name": "stub-zh",
      "targets": ["assistant"],
      "threshold": 0.9,
      "patterns": ["^stub$"],
      "phrases": ["stub"]
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	piiDir := filepath.Join(configRoot, "pii_rules")
	if err := os.MkdirAll(piiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(piiDir, "zh-CN.json"), []byte(`{
  "language": "zh-CN",
  "version": "1.0.0",
  "rules": [],
  "excludes": []
}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNewLocalClosesRuntimeLogFileOnInitFailure verifies startup closes the eagerly opened runtime log file again when later dependency wiring fails, so callers do not inherit leaked file handles on Windows or other strict filesystems.
// TestNewLocalClosesRuntimeLogFileOnInitFailure 用于验证当后续依赖装配失败时，启动流程会把提前打开的运行时日志文件重新关闭，避免调用方在 Windows 等严格文件系统上继承泄露的文件句柄。
func TestNewLocalClosesRuntimeLogFileOnInitFailure(t *testing.T) {
	root := t.TempDir()
	layout := config.PromptLayout{
		SystemDir: filepath.Join(root, "configs"),
	}
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("unsupported", "https://example.com/v1", []string{"test-key"}, "test-llm"),
	}

	_, err := NewLocal(cfg, nil, layout)
	if err == nil {
		t.Fatal("expected startup error")
	}

	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		t.Fatalf("resolve runtime log dir: %v", err)
	}
	if err := os.RemoveAll(logDir); err != nil {
		t.Fatalf("remove runtime log dir after init failure: %v", err)
	}
	if _, statErr := os.Stat(logDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected runtime log dir to be removable after init failure, stat err=%v", statErr)
	}
}

// TestBuildRerankerRejectsMissingDedicatedRerankKey verifies runtime composition no longer reuses LLM keys for DashScope rerank and instead requires one dedicated rerank key pool.
// TestBuildRerankerRejectsMissingDedicatedRerankKey 用于验证运行时装配不再复用 LLM Key 给 DashScope rerank，而是要求显式配置独立 rerank Key 池。
func TestBuildRerankerRejectsMissingDedicatedRerankKey(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("openai", "https://example.com/v1", []string{"shared-llm-key"}, "test-llm"),
	}
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{
		{
			Provider: "dashscope",
			Endpoint: "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
			Model:    "qwen3-vl-rerank",
			Timeout:  config.Duration{Duration: 8 * time.Second},
		},
	}

	reranker, err := buildReranker(cfg)
	if err == nil {
		t.Fatalf("expected reranker build to fail when dedicated rerank keys are missing, got reranker=%T", reranker)
	}
}

// TestBuildLLMUsesKeyFailoverWrapperForMultipleKeys verifies runtime composition upgrades one fixed-model config with multiple keys into the dedicated key-failover wrapper instead of silently discarding extra keys.
// TestBuildLLMUsesKeyFailoverWrapperForMultipleKeys 用于验证当固定模型配置包含多个 Key 时，运行时装配会升级为专用 Key 容灾包装器，而不是静默丢弃额外 Key。
func TestBuildLLMUsesKeyFailoverWrapperForMultipleKeys(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("openai", "https://example.com/v1", []string{"key-a", "key-b"}, "test-llm"),
	}

	client, err := buildLLM(cfg)
	if err != nil {
		t.Fatalf("build llm with key pool: %v", err)
	}
	if _, ok := client.(*ai_key_failover.LLMClient); !ok {
		t.Fatalf("expected key failover llm wrapper, got %T", client)
	}
}

// TestBuildLLMUsesRoutingNodes verifies runtime composition can bootstrap the fixed-model wrapper from explicit routing nodes even when the legacy top-level key fields are empty.
// TestBuildLLMUsesRoutingNodes 用于验证即使旧版顶层 key 字段为空，运行时装配也能从显式轮询节点启动固定模型包装器。
func TestBuildLLMUsesRoutingNodes(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{{
		Provider: "openai",
		Endpoint: "https://example.com/v1",
		Nodes: []config.AIRoutingNodeConfig{
			{Name: "primary", APIKeys: []string{"key-a"}, RPM: 1},
			{Name: "backup", APIKeys: []string{"key-b"}, RPM: 2},
		},
		Model: "test-llm",
	}}

	client, err := buildLLM(cfg)
	if err != nil {
		t.Fatalf("build llm with routing nodes: %v", err)
	}
	if _, ok := client.(*ai_key_failover.LLMClient); !ok {
		t.Fatalf("expected key failover llm wrapper, got %T", client)
	}
}

// TestBuildLLMUsesMultiRouteWrapper verifies runtime composition upgrades explicit llm.routes into the ordered multi-route wrapper instead of collapsing them back into one fixed-model route.
// TestBuildLLMUsesMultiRouteWrapper 用于验证当显式声明 llm.routes 时，运行时装配会升级为有序多路由包装器，而不是把它们重新折叠成单条固定模型路由。
func TestBuildLLMUsesMultiRouteWrapper(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("openai", "https://primary.example/v1", []string{"llm-a"}, "model-a"),
		newLLMRouteForTest("openai_native", "https://backup.example/v1", []string{"llm-b"}, "model-b"),
	}

	client, err := buildLLM(cfg)
	if err != nil {
		t.Fatalf("build llm with provider routes: %v", err)
	}
	if _, ok := client.(*ai_key_failover.LLMMultiRouteClient); !ok {
		t.Fatalf("expected multi-route llm wrapper, got %T", client)
	}
}

// TestAdaptLLMForProcessorRoutesClearsPinnedModel verifies processor-facing runtime wiring drops the fixed request model in multi-route mode so heterogeneous route failover can still occur.
// TestAdaptLLMForProcessorRoutesClearsPinnedModel 用于验证处理器侧运行时装配会在多路由模式下移除固定请求模型，从而允许不同模型名的路由继续参与容灾。
func TestAdaptLLMForProcessorRoutesClearsPinnedModel(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest("openai", "https://primary.example/v1", []string{"llm-a"}, "model-a"),
		newLLMRouteForTest("openai", "https://backup.example/v1", []string{"llm-b"}, "model-b"),
	}
	upstream := &recordingLLMClient{response: appports.LLMResponse{Content: "ok"}}

	client := adaptLLMForProcessorRoutes(cfg, upstream)
	if _, err := client.Generate(context.Background(), appports.LLMRequest{
		Model:               "model-a",
		SystemPrompt:        "system",
		UserPrompt:          "user",
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPreCheckL1,
	}); err != nil {
		t.Fatalf("generate through processor adapter: %v", err)
	}
	if upstream.lastRequest.Model != "" {
		t.Fatalf("expected multi-route processor client to clear request model, got %q", upstream.lastRequest.Model)
	}
	if upstream.lastRequest.RouteSelectionLevel != appports.LLMRouteSelectionLevelPreCheckL1 {
		t.Fatalf("expected processor adapter to preserve route selection level, got %q", upstream.lastRequest.RouteSelectionLevel)
	}
}

// TestAdaptLLMForProcessorRoutesPreservesPinnedModelForSingleRoute verifies single-route runtime wiring keeps the processor-selected model untouched so fixed-route behavior remains stable.
// TestAdaptLLMForProcessorRoutesPreservesPinnedModelForSingleRoute 用于验证单路由运行时装配会保留处理器选择的模型名，确保固定路由行为继续稳定。
func TestAdaptLLMForProcessorRoutesPreservesPinnedModelForSingleRoute(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	upstream := &recordingLLMClient{response: appports.LLMResponse{Content: "ok"}}

	client := adaptLLMForProcessorRoutes(cfg, upstream)
	if _, err := client.Generate(context.Background(), appports.LLMRequest{
		Model:               "test-llm",
		SystemPrompt:        "system",
		UserPrompt:          "user",
		RouteSelectionLevel: appports.LLMRouteSelectionLevelPostActionL2,
	}); err != nil {
		t.Fatalf("generate through single-route processor adapter: %v", err)
	}
	if upstream.lastRequest.Model != "test-llm" {
		t.Fatalf("expected single-route processor client to preserve request model, got %q", upstream.lastRequest.Model)
	}
	if upstream.lastRequest.RouteSelectionLevel != appports.LLMRouteSelectionLevelPostActionL2 {
		t.Fatalf("expected single-route processor client to preserve route selection level, got %q", upstream.lastRequest.RouteSelectionLevel)
	}
}

// TestSelectProcessorLLMModelUsesPrimaryModelForSingleRoute verifies processor-side model selection still returns the concrete single-route model after prompt routing is decoupled from model names.
// TestSelectProcessorLLMModelUsesPrimaryModelForSingleRoute 用于验证在提示词目录已与模型名解耦后，处理器侧模型选择在单路由场景下仍会返回明确的单路由模型。
func TestSelectProcessorLLMModelUsesPrimaryModelForSingleRoute(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{{
		Name:     "primary",
		Provider: "openai",
		Endpoint: "https://primary.example/v1",
		APIKeys:  []string{"llm-a"},
		Model:    "single-route-model",
	}}

	if got, want := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL1), "single-route-model"; got != want {
		t.Fatalf("processor llm model = %q, want %q", got, want)
	}
}

// TestSelectProcessorLLMModelUsesSelectionLevelWeights verifies different business tiers can still resolve different primary models from one shared route table after prompt routing becomes explicit config state.
// TestSelectProcessorLLMModelUsesSelectionLevelWeights 用于验证提示词路由改成显式配置后，不同业务层级仍能从同一份路由表解析出不同主模型。
func TestSelectProcessorLLMModelUsesSelectionLevelWeights(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		{
			Name:     "precheck",
			Provider: "openai",
			Endpoint: "https://precheck.example/v1",
			APIKeys:  []string{"llm-a"},
			Model:    "qwen3.5-flash",
			Weights: config.LLMRouteWeightConfig{
				PreCheckL1:         intPtrForAppTest(200),
				PostActionL1:       intPtrForAppTest(40),
				ProfileInstruction: intPtrForAppTest(50),
			},
		},
		{
			Name:     "postaction",
			Provider: "openai",
			Endpoint: "https://postaction.example/v1",
			APIKeys:  []string{"llm-b"},
			Model:    "qwen3.5-base",
			Weights: config.LLMRouteWeightConfig{
				PreCheckL1:         intPtrForAppTest(50),
				PostActionL1:       intPtrForAppTest(220),
				ProfileInstruction: intPtrForAppTest(60),
			},
		},
		{
			Name:     "profile",
			Provider: "openai",
			Endpoint: "https://profile.example/v1",
			APIKeys:  []string{"llm-c"},
			Model:    "qwen3.5-profile",
			Weights: config.LLMRouteWeightConfig{
				PreCheckL1:         intPtrForAppTest(40),
				PostActionL1:       intPtrForAppTest(50),
				ProfileInstruction: intPtrForAppTest(230),
			},
		},
	}

	if got, want := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPreCheckL1), "qwen3.5-flash"; got != want {
		t.Fatalf("precheck llm model = %q, want %q", got, want)
	}
	if got, want := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelPostActionL1), "qwen3.5-base"; got != want {
		t.Fatalf("postaction llm model = %q, want %q", got, want)
	}
	if got, want := selectProcessorLLMModel(cfg, appports.LLMRouteSelectionLevelProfileInstruction), "qwen3.5-profile"; got != want {
		t.Fatalf("profile instruction llm model = %q, want %q", got, want)
	}
}

// TestBuildRerankerUsesMultiRouteWrapper verifies runtime composition upgrades explicit rerank.routes into the ordered multi-route wrapper while leaving the use-case level top_n configuration untouched.
// TestBuildRerankerUsesMultiRouteWrapper 用于验证当显式声明 rerank.routes 时，运行时装配会升级为有序多路由包装器，同时保持用例层的 top_n 配置不变。
func TestBuildRerankerUsesMultiRouteWrapper(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{
		newRerankRouteForTest("dashscope", "https://rerank-primary.example/v1", []string{"rerank-a"}, "model-a", 5*time.Second),
		newRerankRouteForTest("dashscope", "https://rerank-backup.example/v1", []string{"rerank-b"}, "model-b", 6*time.Second),
	}

	client, err := buildReranker(cfg)
	if err != nil {
		t.Fatalf("build reranker with provider routes: %v", err)
	}
	if _, ok := client.(*ai_key_failover.RerankMultiRouteClient); !ok {
		t.Fatalf("expected multi-route rerank wrapper, got %T", client)
	}
}

// TestBuildRerankerSupportsSiliconFlowProvider verifies runtime composition accepts SiliconFlow rerank routes and can materialize the provider-specific adapter with provider defaults intact.
// TestBuildRerankerSupportsSiliconFlowProvider 用于验证运行时装配接受 SiliconFlow rerank 路由，并能在保留 provider 默认值的前提下实例化对应适配器。
func TestBuildRerankerSupportsSiliconFlowProvider(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{{
		Provider: "siliconflow",
		APIKeys:  []string{"silicon-key"},
		Params: map[string]any{
			"provider": map[string]any{"only": []any{"Cohere"}},
		},
	}}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation should accept siliconflow rerank: %v", err)
	}
	if route := cfg.Rerank.ProviderRoutes()[0]; route.Endpoint != "https://api.siliconflow.cn/v1/rerank" || route.Model != "BAAI/bge-reranker-v2-m3" {
		t.Fatalf("normalized siliconflow rerank route = %#v", route)
	}

	client, err := buildReranker(cfg)
	if err != nil {
		t.Fatalf("build siliconflow reranker: %v", err)
	}
	if _, ok := client.(*ai_key_failover.RerankerClient); !ok {
		t.Fatalf("expected fixed-route siliconflow reranker wrapper, got %T", client)
	}
}

// TestBuildAdaptersAllowTrimmedProviderAliases verifies runtime adapter construction stays aligned with config validation when provider aliases contain surrounding whitespace.
// TestBuildAdaptersAllowTrimmedProviderAliases 用于验证当 provider 别名带有首尾空白时，运行时适配器构建仍与配置校验口径保持一致。
func TestBuildAdaptersAllowTrimmedProviderAliases(t *testing.T) {
	cfg := newRuntimeConfigForTest()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		newLLMRouteForTest(" openai ", "https://example.com/v1", []string{"test-key"}, "test-llm"),
	}
	cfg.Embedding.Provider = " openai "
	cfg.Embedding.Endpoint = "https://example.com/v1"
	cfg.Embedding.APIKeys = []string{"test-key"}
	cfg.Embedding.Model = "test-embedding"
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{
		newRerankRouteForTest(
			" dashscope ",
			"https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank",
			[]string{"test-key"},
			"qwen3-vl-rerank",
			8*time.Second,
		),
	}
	cfg.Vector.Provider = " lancedb "
	cfg.Relational.Provider = " sqlite "
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation should accept trimmed provider aliases: %v", err)
	}
	if _, err := buildLLM(cfg); err != nil {
		t.Fatalf("build llm with trimmed provider alias: %v", err)
	}
	if _, err := buildEmbedding(cfg); err != nil {
		t.Fatalf("build embedding with trimmed provider alias: %v", err)
	}
	if _, err := buildReranker(cfg); err != nil {
		t.Fatalf("build reranker with trimmed provider alias: %v", err)
	}
	if got := normalizeProviderAlias(cfg.Vector.Provider); got != "lancedb" {
		t.Fatalf("normalized vector provider alias = %q", got)
	}
	if got := normalizeProviderAlias(cfg.Relational.Provider); got != "sqlite" {
		t.Fatalf("normalized relational provider alias = %q", got)
	}
}

// TestBuildGoogleAIStudioAdapters verifies runtime composition accepts the native Google AI Studio provider for both LLM routes and embedding while keeping endpoint optional.
// TestBuildGoogleAIStudioAdapters 用于验证运行时装配支持原生 Google AI Studio provider，并允许在 LLM 路由与 embedding 中省略 endpoint。
func TestBuildGoogleAIStudioAdapters(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		{
			Provider: "google_ai_studio",
			APIKeys:  []string{"google-llm-key"},
			Model:    "gemini-2.5-flash",
		},
	}
	cfg.Embedding.Provider = "google_ai_studio"
	cfg.Embedding.APIKeys = []string{"google-embed-key"}
	cfg.Embedding.Model = "gemini-embedding-001"
	cfg.Embedding.Dimension = 1024

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation should accept google ai studio: %v", err)
	}
	if _, err := buildLLM(cfg); err != nil {
		t.Fatalf("build google ai studio llm: %v", err)
	}
	if _, err := buildEmbedding(cfg); err != nil {
		t.Fatalf("build google ai studio embedding: %v", err)
	}
}

// TestBuildOpenRouterAdapters verifies runtime composition accepts OpenRouter for LLM, embedding, and rerank while relying on SDK-default endpoints.
// TestBuildOpenRouterAdapters 用于验证运行时装配支持 OpenRouter 同时作为 LLM、embedding 与 rerank provider，并允许使用 SDK 默认 endpoint。
func TestBuildOpenRouterAdapters(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.LLM.Routes = []config.LLMRouteConfig{
		{
			Provider: "openrouter",
			APIKeys:  []string{"openrouter-llm-key"},
			Model:    "openai/gpt-4.1-mini",
		},
	}
	cfg.Embedding.Provider = "openrouter"
	cfg.Embedding.APIKeys = []string{"openrouter-embed-key"}
	cfg.Embedding.Model = "openai/text-embedding-3-small"
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Routes = []config.RerankRouteConfig{{
		Provider: "openrouter",
		APIKeys:  []string{"openrouter-rerank-key"},
		Params: map[string]any{
			"provider": map[string]any{
				"only":            []any{"Cohere"},
				"allow_fallbacks": false,
			},
		},
	}}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation should accept openrouter: %v", err)
	}
	if _, err := buildLLM(cfg); err != nil {
		t.Fatalf("build openrouter llm: %v", err)
	}
	if _, err := buildEmbedding(cfg); err != nil {
		t.Fatalf("build openrouter embedding: %v", err)
	}
	client, err := buildReranker(cfg)
	if err != nil {
		t.Fatalf("build openrouter reranker: %v", err)
	}
	if _, ok := client.(*ai_key_failover.RerankerClient); !ok {
		t.Fatalf("expected fixed-route openrouter reranker wrapper, got %T", client)
	}
}

// TestApplicationRunRejectsNilReceiver verifies exported startup fails with one deterministic error instead of panicking when callers invoke it on a nil application pointer.
// TestApplicationRunRejectsNilReceiver 用于验证调用方在空应用指针上触发启动时，会收到确定性错误而不是直接 panic。
func TestApplicationRunRejectsNilReceiver(t *testing.T) {
	var app *Application

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("expected run error")
	}
	if !strings.Contains(err.Error(), "application is nil") {
		t.Fatalf("unexpected run error: %v", err)
	}
}

// TestApplicationRunRejectsNilServer verifies exported startup rejects incomplete runtime wiring before a nil grpc.Server can panic inside Serve.
// TestApplicationRunRejectsNilServer 用于验证导出启动入口会在 nil grpc.Server 进入 Serve 之前拒绝不完整装配，避免内部 panic。
func TestApplicationRunRejectsNilServer(t *testing.T) {
	app := &Application{
		Config: config.DefaultLocal(),
	}

	err := app.Run(nil)
	if err == nil {
		t.Fatal("expected run error")
	}
	if !strings.Contains(err.Error(), "grpc server is not initialized") {
		t.Fatalf("unexpected run error: %v", err)
	}
}

// TestApplicationShutdownContinuesAfterDependencyError verifies graceful shutdown keeps draining later dependencies even when an earlier shutdown hook fails.
// TestApplicationShutdownContinuesAfterDependencyError 用于验证优雅停机会在前一个依赖关闭失败后继续释放后续依赖。
func TestApplicationShutdownContinuesAfterDependencyError(t *testing.T) {
	cfg := config.DefaultLocal()
	first := &stubShutdowner{err: errors.New("first failed")}
	second := &stubShutdowner{}
	app := &Application{
		Config:    cfg,
		Logger:    nil,
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{second, first},
	}

	err := app.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected shutdown error")
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("expected both shutdowners to be called once, got first=%d second=%d", first.calls, second.calls)
	}
	if !strings.Contains(err.Error(), "shutdown dependency[1]") {
		t.Fatalf("expected aggregated shutdown error to mention failing dependency, got %v", err)
	}
}

// TestApplicationShutdownAllowsNilServer verifies exported shutdown can still drain dependencies when tests or partial construction leave the gRPC server unset.
// TestApplicationShutdownAllowsNilServer 用于验证当测试替身或部分装配过程未设置 gRPC 服务时，导出的 shutdown 仍能继续释放依赖而不崩溃。
func TestApplicationShutdownAllowsNilServer(t *testing.T) {
	shutdowner := &stubShutdowner{}
	app := &Application{
		Shutdowns: []appports.Shutdowner{shutdowner},
	}

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with nil server: %v", err)
	}
	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationShutdownUsesManagementBudgetAndForceCloses verifies an expired graceful HTTP shutdown is surfaced and followed by a forced close.
// TestApplicationShutdownUsesManagementBudgetAndForceCloses 用于验证 HTTP 优雅关闭超时会被显式报告并继续强制关闭连接。
func TestApplicationShutdownUsesManagementBudgetAndForceCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen management test server: %v", err)
	}
	requestStarted := make(chan struct{})
	requestFinished := make(chan struct{})
	serverFinished := make(chan struct{})
	managementServer := &http.Server{
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(requestStarted)
			<-request.Context().Done()
		}),
	}
	go func() {
		_ = managementServer.Serve(listener)
		close(serverFinished)
	}()
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_ = response.Body.Close()
		}
		close(requestFinished)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("management request did not start")
	}

	cfg := config.DefaultLocal()
	cfg.Management.ShutdownTimeout = config.Duration{Duration: 20 * time.Millisecond}
	application := &Application{Config: cfg, ManagementServer: managementServer}
	shutdownErr := application.Shutdown(context.Background())
	if shutdownErr == nil || !strings.Contains(shutdownErr.Error(), "shutdown management server") {
		t.Fatalf("expected management shutdown timeout, got %v", shutdownErr)
	}
	select {
	case <-requestFinished:
	case <-time.After(time.Second):
		t.Fatal("forced management close did not release the active request")
	}
	select {
	case <-serverFinished:
	case <-time.After(time.Second):
		t.Fatal("forced management close did not stop the server")
	}
}

// TestApplicationShutdownAllowsNilReceiver verifies nil application pointers do not crash shared cleanup paths.
// TestApplicationShutdownAllowsNilReceiver 用于验证空应用指针不会把共享清理路径直接打崩。
func TestApplicationShutdownAllowsNilReceiver(t *testing.T) {
	var app *Application

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown nil application: %v", err)
	}
}

// TestApplicationShutdownSkipsNilShutdowners verifies exported shutdown still drains valid dependencies when a partially assembled runtime leaves nil shutdown slots in the slice.
// TestApplicationShutdownSkipsNilShutdowners 用于验证当部分装配运行时在 Shutdowns 切片中留下 nil 槽位时，导出的 shutdown 仍会跳过空槽并继续释放有效依赖。
func TestApplicationShutdownSkipsNilShutdowners(t *testing.T) {
	shutdowner := &stubShutdowner{}
	app := &Application{
		Shutdowns: []appports.Shutdowner{shutdowner, nil},
	}

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with nil shutdowner: %v", err)
	}
	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationShutdownDeduplicatesRepeatedDependencies verifies graceful shutdown collapses repeated hooks that point at the same runtime dependency, so combined-store modes do not double-close shared resources.
// TestApplicationShutdownDeduplicatesRepeatedDependencies 用于验证优雅停机会折叠指向同一运行时依赖的重复 hook，避免组合库模式对共享资源重复关闭。
func TestApplicationShutdownDeduplicatesRepeatedDependencies(t *testing.T) {
	shutdowner := &stubShutdowner{}
	app := &Application{
		Shutdowns: []appports.Shutdowner{shutdowner, shutdowner},
	}

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with duplicate shutdowners: %v", err)
	}
	if shutdowner.calls != 1 {
		t.Fatalf("expected duplicate shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationRunDrainsShutdownsAfterExternalServerStop verifies exported startup still drains downstream shutdown hooks when another goroutine stops the gRPC server directly.
// TestApplicationRunDrainsShutdownsAfterExternalServerStop 用于验证当其他 goroutine 直接停止 gRPC 服务时，导出启动入口仍会继续释放下游 shutdown 钩子。
func TestApplicationRunDrainsShutdownsAfterExternalServerStop(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	shutdowner := &stubShutdowner{}
	app := &Application{
		Config:    config.DefaultLocal(),
		Logger:    logx.Default(),
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{shutdowner},
	}
	app.Config.GRPC.ListenAddr = addr

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app.Run(context.Background())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	app.Server.Stop()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run after external stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after external stop")
	}

	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationRunWithReadySignalsAfterListen verifies service managers only receive readiness after the TCP listener is actually bound.
// TestApplicationRunWithReadySignalsAfterListen 用于验证服务管理器只会在 TCP 监听器真实绑定后收到就绪信号。
func TestApplicationRunWithReadySignalsAfterListen(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	app := &Application{
		Config: config.DefaultLocal(),
		Logger: logx.Default(),
		Server: grpc.NewServer(),
	}
	app.Config.GRPC.ListenAddr = addr

	readyCh := make(chan struct{}, 1)
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app.RunWithReady(context.Background(), func() {
			readyCh <- struct{}{}
		})
	}()

	select {
	case <-readyCh:
	case err := <-runErrCh:
		t.Fatalf("run returned before readiness: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("ready callback was not invoked")
	}

	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("expected listener to be reachable after ready callback: %v", err)
	}
	_ = conn.Close()

	app.Server.Stop()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run after stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after stop")
	}
}

// TestApplicationRunWithReadyEndpointsStartsAuthenticatedManagementServer verifies readiness includes two live listeners and the management listener enforces its separate token.
// TestApplicationRunWithReadyEndpointsStartsAuthenticatedManagementServer 用于验证就绪状态包含两个存活监听器，且管理监听器会校验独立 Token。
func TestApplicationRunWithReadyEndpointsStartsAuthenticatedManagementServer(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.GRPC.ListenAddr = "127.0.0.1:0"
	cfg.Management.Enabled = true
	cfg.Management.ListenAddr = "127.0.0.1:0"
	cfg.Management.AccessToken = "management-test-token"
	application := &Application{
		Config:           cfg,
		Logger:           logx.Default(),
		Server:           grpc.NewServer(),
		ManagementServer: buildManagementHTTPServer(cfg, nil),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	endpointsCh := make(chan RuntimeEndpoints, 1)
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- application.RunWithReadyEndpoints(ctx, func(endpoints RuntimeEndpoints) error {
			endpointsCh <- endpoints
			return nil
		})
	}()

	var endpoints RuntimeEndpoints
	select {
	case endpoints = <-endpointsCh:
	case err := <-runErrCh:
		t.Fatalf("run returned before readiness: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("endpoint readiness callback was not invoked")
	}
	if endpoints.GRPCListenAddr == "" || endpoints.ManagementListenAddr == "" || endpoints.GRPCListenAddr == endpoints.ManagementListenAddr {
		t.Fatalf("runtime endpoints = %#v", endpoints)
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+endpoints.ManagementListenAddr+"/management/v1/capabilities", nil)
	if err != nil {
		t.Fatalf("build management request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer management-test-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call management endpoint: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("management status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run after cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

// TestApplicationRunAllowsNilLogger verifies exported startup still remains usable when direct tests or partial runtime assembly omit the logger field.
// TestApplicationRunAllowsNilLogger 用于验证当直接测试或部分运行时装配遗漏 logger 字段时，导出的启动入口仍然可用，不会因为记录启动日志而直接 panic。
func TestApplicationRunAllowsNilLogger(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	shutdowner := &stubShutdowner{}
	app := &Application{
		Config:    config.DefaultLocal(),
		Logger:    nil,
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{shutdowner},
	}
	app.Config.GRPC.ListenAddr = addr

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app.Run(context.Background())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	app.Server.Stop()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run with nil logger: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return with nil logger")
	}

	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// stubShutdowner records shutdown attempts for application lifecycle tests.
// stubShutdowner 用于为应用生命周期测试记录 shutdown 调用次数。
type stubShutdowner struct {
	calls int
	err   error
}

// Shutdown increments the call counter and returns the configured error.
// Shutdown 用于增加调用计数，并返回预设错误。
func (s *stubShutdowner) Shutdown(context.Context) error {
	s.calls++
	return s.err
}
