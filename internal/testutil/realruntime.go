// realruntime.go provides one shared real-model test fixture backed by output/configs and the configured .env file.
// realruntime.go 用于提供一套共享的真实模型测试基座，直接复用 output/configs 和已配置的 .env。
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openvulcan/vmm/internal/adapters/outbound/openai_native"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
)

// RealRuntimeFixture bundles the real OpenAI-compatible clients and prompt source used by integration-style tests.
// RealRuntimeFixture 用于聚合集成测试所需的真实 OpenAI 兼容客户端和提示词来源。
type RealRuntimeFixture struct {
	RepoRoot   string
	ConfigPath string
	Config     config.Config
	Prompts    appports.PromptSource
	LLM        appports.LLMClient
	Embedding  appports.EmbeddingClient
}

var (
	// realRuntimeOnce ensures the expensive config and prompt loading only runs once per test process.
	// realRuntimeOnce 用于保证昂贵的配置与提示词加载在单个测试进程里只执行一次。
	realRuntimeOnce sync.Once
	// realRuntimeCached stores the shared fixture once it has been resolved successfully.
	// realRuntimeCached 用于保存成功解析后的共享测试基座。
	realRuntimeCached *RealRuntimeFixture
	// realRuntimeErr preserves the first initialization error so every caller sees the same failure.
	// realRuntimeErr 用于保存首次初始化错误，确保所有调用方看到一致的失败原因。
	realRuntimeErr error
	// realRuntimeProbeOnce ensures the live-provider smoke check is executed at most once per test process.
	// realRuntimeProbeOnce 用于保证真实提供方的冒烟探测在单个测试进程里最多只执行一次。
	realRuntimeProbeOnce sync.Once
	// realRuntimeProbeErr stores the first live-provider probe failure so later tests can skip consistently.
	// realRuntimeProbeErr 用于保存首次真实提供方探测失败结果，便于后续测试一致地跳过。
	realRuntimeProbeErr error
)

// MustRealRuntimeFixture resolves the shared real-model fixture or fails the current test immediately.
// MustRealRuntimeFixture 用于解析共享真实模型测试基座，并在失败时立即终止当前测试。
func MustRealRuntimeFixture(tb testing.TB) *RealRuntimeFixture {
	tb.Helper()
	realRuntimeOnce.Do(func() {
		realRuntimeCached, realRuntimeErr = loadRealRuntimeFixture()
	})
	if realRuntimeErr != nil {
		tb.Fatalf("load real runtime fixture: %v", realRuntimeErr)
	}
	return realRuntimeCached
}

// RequireLiveModelAccess verifies that the configured embedding endpoint is actually usable before integration-style tests proceed.
// RequireLiveModelAccess 用于在集成测试继续之前验证当前配置的 embedding 端点确实可用。
func RequireLiveModelAccess(tb testing.TB) *RealRuntimeFixture {
	tb.Helper()
	fixture := MustRealRuntimeFixture(tb)
	realRuntimeProbeOnce.Do(func() {
		_, realRuntimeProbeErr = fixture.Embedding.Embed(context.Background(), appports.EmbeddingRequest{
			Model:     fixture.Config.Embedding.Model,
			Texts:     []string{"vmm live embedding probe"},
			Dimension: fixture.Config.Embedding.Dimension,
			ProviderHints: map[string]any{
				"purpose": "test_probe",
			},
		})
	})
	if realRuntimeProbeErr != nil {
		tb.Skipf("real model fixture is unavailable: %v", realRuntimeProbeErr)
	}
	return fixture
}

// loadRealRuntimeFixture loads the packaged local runtime config and constructs the real OpenAI-compatible clients used by tests.
// loadRealRuntimeFixture 用于加载打包后的本地运行时配置，并构建测试使用的真实 OpenAI 兼容客户端。
func loadRealRuntimeFixture() (*RealRuntimeFixture, error) {
	// Discover the repository root first so tests remain stable no matter which package directory go test starts from.
	// 先定位仓库根目录，保证无论 go test 从哪个包目录启动都能稳定找到标准输出目录。
	repoRoot, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(repoRoot, "output", "configs", "local.json")
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("missing packaged runtime config: %w", err)
	}

	// Reuse the real packaged config chain so tests hit the same endpoint, model, and prompt files as the local runtime.
	// 复用真实打包配置链，让测试和本地运行时使用同一套 endpoint、model 与提示词文件。
	cfg, err := config.Load(configPath, config.DefaultLocal())
	if err != nil {
		return nil, err
	}
	systemDir := filepath.Join(repoRoot, "output", "configs")
	prompts, err := config.NewPromptManager(systemDir, systemDir)
	if err != nil {
		return nil, err
	}
	llmRoute, ok := cfg.LLM.PrimaryRoute()
	llmAPIKey, hasLLMAPIKey := firstRoutingAPIKey(llmRoute.APIKeys, llmRoute.Nodes)
	if !ok || strings.TrimSpace(llmRoute.Endpoint) == "" || !hasLLMAPIKey || strings.TrimSpace(llmRoute.Model) == "" {
		return nil, fmt.Errorf("real llm config is incomplete in %s", configPath)
	}
	embeddingAPIKey, hasEmbeddingAPIKey := firstRoutingAPIKey(cfg.Embedding.APIKeys, cfg.Embedding.RoutingNodes())
	if strings.TrimSpace(cfg.Embedding.Endpoint) == "" || !hasEmbeddingAPIKey || strings.TrimSpace(cfg.Embedding.Model) == "" {
		return nil, fmt.Errorf("real embedding config is incomplete in %s", configPath)
	}

	// Construct the real OpenAI-compatible clients once and share them across the process to avoid repeated setup noise.
	// 只构建一次真实 OpenAI 兼容客户端并在进程内共享，避免重复初始化带来的噪声。
	return &RealRuntimeFixture{
		RepoRoot:   repoRoot,
		ConfigPath: configPath,
		Config:     cfg,
		Prompts:    prompts,
		LLM:        openai_native.NewLLMClient(llmRoute.Endpoint, llmAPIKey, llmRoute.Model, llmRoute.Organization, llmRoute.Project, llmRoute.Params, llmRoute.ModelParams),
		Embedding:  openai_native.NewEmbeddingClient(cfg.Embedding.Endpoint, embeddingAPIKey, cfg.Embedding.Model, cfg.Embedding.Dimension, cfg.Embedding.Organization, cfg.Embedding.Project, cfg.Embedding.Params, cfg.Embedding.ModelParams),
	}, nil
}

// firstRoutingAPIKey returns the first usable API key from either one top-level key pool or one normalized node list so fixtures accept every supported key declaration shape.
// firstRoutingAPIKey 用于从顶层 Key 池或已归一化的节点列表中返回第一个可用 API Key，确保测试基座接受所有受支持的 Key 声明形态。
func firstRoutingAPIKey(apiKeys []string, nodes []config.AIRoutingNodeConfig) (string, bool) {
	for _, apiKey := range apiKeys {
		if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
			return trimmed, true
		}
	}
	for _, node := range nodes {
		for _, apiKey := range node.APIKeys {
			if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
				return trimmed, true
			}
		}
	}
	return "", false
}

// findRepoRoot walks upward until it finds the repository root that contains both go.mod and output/configs/local.json.
// findRepoRoot 用于逐级向上查找同时包含 go.mod 和 output/configs/local.json 的仓库根目录。
func findRepoRoot() (string, error) {
	start, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working dir: %w", err)
	}
	start, err = filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("abs working dir: %w", err)
	}
	for dir := start; ; dir = filepath.Dir(dir) {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "output", "configs", "local.json")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("cannot locate repository root from %s", start)
}

// fileExists reports whether one path points at an existing regular file.
// fileExists 用于判断某个路径是否指向已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
