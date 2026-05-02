// realruntime.go provides one shared real-model test fixture backed by output/configs and the configured .env file chain.
// realruntime.go 用于提供一套共享的真实模型测试基座，直接复用 output/configs 及其对应的 .env 配置链。
package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joho/godotenv"
	"github.com/openvulcan/vmm/internal/adapters/outbound/ai_key_failover"
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

// loadRealRuntimeFixture loads the same effective runtime layout used by the local binary and constructs the real OpenAI-compatible clients used by tests.
// loadRealRuntimeFixture 用于加载与本地二进制一致的实际运行时布局，并构建测试使用的真实 OpenAI 兼容客户端。
func loadRealRuntimeFixture() (*RealRuntimeFixture, error) {
	// Discover the repository root first so tests remain stable no matter which package directory go test starts from.
	// 先定位仓库根目录，保证无论 go test 从哪个包目录启动都能稳定找到配置目录。
	repoRoot, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	layout, err := resolveRealRuntimeLayout(repoRoot)
	if err != nil {
		return nil, err
	}
	configPath := layout.AppConfigPath
	if strings.TrimSpace(configPath) == "" {
		configPath = layout.BaseConfigPath
	}

	// Reuse the resolved config chain so tests hit the same endpoint, model, and prompt files as the local runtime.
	// 复用解析出的配置链，让测试和本地运行时使用同一套 endpoint、model 与提示词文件。
	cleanupEnvFallback, err := ensureRealRuntimeFixtureEnvFallback(layout.ConfigPaths(), "OPENROUTER_KEY", "test-openrouter-key")
	if err != nil {
		return nil, err
	}
	defer cleanupEnvFallback()
	cfg, err := config.LoadPaths(layout.ConfigPaths(), config.Config{})
	if err != nil {
		return nil, err
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir, cfg.Prompts.PromptLanguage)
	if err != nil {
		return nil, err
	}
	llmRoute, ok := cfg.LLM.PrimaryRoute()
	llmAPIKey, hasLLMAPIKey := firstRoutingAPIKey(llmRoute.APIKeys, llmRoute.Nodes)
	if !ok || strings.TrimSpace(llmRoute.Endpoint) == "" || !hasLLMAPIKey || strings.TrimSpace(llmRoute.Model) == "" {
		return nil, fmt.Errorf("real llm config is incomplete in %s", configPath)
	}
	_, hasEmbeddingAPIKey := firstRoutingAPIKey(cfg.Embedding.APIKeys, cfg.Embedding.RoutingNodes())
	if strings.TrimSpace(cfg.Embedding.Endpoint) == "" || !hasEmbeddingAPIKey || strings.TrimSpace(cfg.Embedding.Model) == "" {
		return nil, fmt.Errorf("real embedding config is incomplete in %s", configPath)
	}
	embedding, err := ai_key_failover.NewProviderEmbeddingClient(
		cfg.Embedding.Provider,
		cfg.Embedding.Endpoint,
		cfg.Embedding.Model,
		cfg.Embedding.Dimension,
		cfg.Embedding.MaxBatchSize,
		cfg.Embedding.Organization,
		cfg.Embedding.Project,
		cfg.Embedding.APIKeys,
		cfg.Embedding.Params,
		cfg.Embedding.ModelParams,
		buildRealRuntimeEmbeddingFailoverOptions(cfg),
	)
	if err != nil {
		return nil, fmt.Errorf("build real embedding failover client: %w", err)
	}

	// Construct the real OpenAI-compatible clients once and share them across the process to avoid repeated setup noise.
	// 只构建一次真实 OpenAI 兼容客户端并在进程内共享，避免重复初始化带来的噪声。
	return &RealRuntimeFixture{
		RepoRoot:   repoRoot,
		ConfigPath: configPath,
		Config:     cfg,
		Prompts:    prompts,
		LLM:        openai_native.NewLLMClient(llmRoute.Endpoint, llmAPIKey, llmRoute.Model, llmRoute.Organization, llmRoute.Project, llmRoute.Params, llmRoute.ModelParams),
		Embedding:  embedding,
	}, nil
}

// buildRealRuntimeEmbeddingFailoverOptions mirrors the runtime embedding routing configuration so integration-style tests exercise the same batching and key-failover shell as the local binary.
// buildRealRuntimeEmbeddingFailoverOptions 用于镜像运行时的 embedding 路由配置，确保集成测试走与本地二进制相同的拆批和 Key 容灾外壳。
func buildRealRuntimeEmbeddingFailoverOptions(cfg config.Config) ai_key_failover.Options {
	nodes := cfg.Embedding.RoutingNodes()
	routingNodes := make([]ai_key_failover.NodeOptions, 0, len(nodes))
	totalCandidates := 0
	for idx, node := range nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("embedding-node-%d", idx+1)
		}
		apiKeys := append([]string(nil), node.APIKeys...)
		totalCandidates += len(apiKeys)
		routingNodes = append(routingNodes, ai_key_failover.NodeOptions{
			Name:    name,
			APIKeys: apiKeys,
			RPM:     node.RPM,
			TPM:     node.TPM,
			RPD:     node.RPD,
		})
	}
	return ai_key_failover.Options{
		ServiceName:        "embedding",
		Enabled:            cfg.Embedding.KeyFailover.Enabled && totalCandidates > 1,
		Policy:             strings.TrimSpace(cfg.Embedding.KeyFailover.Policy),
		Nodes:              routingNodes,
		RespectRetryAfter:  cfg.Embedding.KeyFailover.RespectRetryAfter,
		RateLimitCooldown:  cfg.Embedding.KeyFailover.RateLimitCooldown.Duration,
		QuotaCooldown:      cfg.Embedding.KeyFailover.QuotaCooldown.Duration,
		AuthCooldown:       cfg.Embedding.KeyFailover.AuthCooldown.Duration,
		ProbeAfterCooldown: cfg.Embedding.KeyFailover.ProbeAfterCooldown,
	}
}

// resolveRealRuntimeLayout mirrors the runtime startup layout resolution so live-model tests follow the same packaged-versus-workspace and user-override search order as the local binary.
// resolveRealRuntimeLayout 用于镜像运行时启动时的布局解析逻辑，确保真实模型测试遵循与本地二进制一致的打包/工作区及用户覆盖搜索顺序。
func resolveRealRuntimeLayout(repoRoot string) (config.PromptLayout, error) {
	executablePath := filepath.Join(repoRoot, "cmd", "vmm-local")
	if fileExists(filepath.Join(repoRoot, "output", "configs", "base.yaml")) {
		executablePath = filepath.Join(repoRoot, "output", "bin", "vmm-local.exe")
	}
	return config.ResolvePromptLayout(executablePath, filepath.Join(repoRoot, "cmd", "vmm-local"), "", "config")
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

// ensureRealRuntimeFixtureEnvFallback supplies a non-secret test fallback when a packaged fixture references an unrelated provider key that local tests do not exercise.
// ensureRealRuntimeFixtureEnvFallback 用于在打包测试夹具引用了本地测试不实际调用的 provider key 时，补入非密钥测试兜底值。
func ensureRealRuntimeFixtureEnvFallback(configPaths []string, key, fallback string) (func(), error) {
	if value, exists := os.LookupEnv(key); exists && strings.TrimSpace(value) != "" {
		return func() {}, nil
	}
	hasDotEnvValue, err := realRuntimeDotEnvHasValue(configPaths, key)
	if err != nil {
		return nil, err
	}
	if hasDotEnvValue {
		return func() {}, nil
	}
	previousValue, previousExists := os.LookupEnv(key)
	if err := os.Setenv(key, fallback); err != nil {
		return nil, fmt.Errorf("set test env fallback %q: %w", key, err)
	}
	return func() {
		if previousExists {
			_ = os.Setenv(key, previousValue)
			return
		}
		_ = os.Unsetenv(key)
	}, nil
}

// realRuntimeDotEnvHasValue checks the same adjacent .env locations used by config loading without mutating the process environment.
// realRuntimeDotEnvHasValue 用于检查配置加载同样会读取的相邻 .env 位置，但不会修改进程环境。
func realRuntimeDotEnvHasValue(configPaths []string, key string) (bool, error) {
	for _, configPath := range configPaths {
		for _, candidate := range realRuntimeDotEnvCandidates(configPath) {
			envMap, err := godotenv.Read(candidate)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return false, fmt.Errorf("read test .env %q: %w", candidate, err)
			}
			if strings.TrimSpace(envMap[key]) != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

// realRuntimeDotEnvCandidates mirrors the config loader's packaged-config .env search order for test-only fixture bootstrapping.
// realRuntimeDotEnvCandidates 用于在测试夹具启动时镜像配置加载器的打包配置 .env 搜索顺序。
func realRuntimeDotEnvCandidates(configPath string) []string {
	candidates := make([]string, 0, 2)
	seen := map[string]struct{}{}
	addCandidate := func(path string) {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "." {
			return
		}
		if _, ok := seen[cleaned]; ok {
			return
		}
		seen[cleaned] = struct{}{}
		candidates = append(candidates, cleaned)
	}
	if strings.TrimSpace(configPath) == "" {
		return candidates
	}
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return candidates
	}
	configDir := filepath.Dir(absPath)
	if strings.EqualFold(filepath.Base(configDir), "configs") {
		addCandidate(filepath.Join(configDir, "..", ".env"))
	}
	addCandidate(filepath.Join(configDir, ".env"))
	return candidates
}

// findRepoRoot walks upward until it finds the repository root that contains go.mod plus either packaged or workspace base YAML configuration roots.
// findRepoRoot 用于逐级向上查找同时包含 go.mod 以及打包态或工作区 base YAML 配置根的仓库根目录。
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
		hasPackagedConfig := fileExists(filepath.Join(dir, "output", "configs", "base.yaml"))
		hasWorkspaceConfig := fileExists(filepath.Join(dir, "configs", "base.yaml"))
		if fileExists(filepath.Join(dir, "go.mod")) && (hasPackagedConfig || hasWorkspaceConfig) {
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
