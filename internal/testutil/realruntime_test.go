// realruntime_test.go verifies the real-runtime fixture helpers accept every supported AI key declaration shape without depending on live upstream access.
// realruntime_test.go 用于验证真实运行时测试基座辅助逻辑能够接受所有受支持的 AI Key 声明形态，而不依赖真实上游可用性。
package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openvulcan/vmm/internal/config"
)

// TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes verifies the fixture can extract one usable key from both top-level pools and node-only declarations.
// TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes 用于验证测试基座既能从顶层 Key 池提取可用 Key，也能从仅节点声明里提取可用 Key。
func TestFirstRoutingAPIKeyAcceptsSupportedKeyShapes(t *testing.T) {
	tests := []struct {
		name    string
		apiKeys []string
		nodes   []config.AIRoutingNodeConfig
		want    string
		ok      bool
	}{
		{
			name:    "top-level pool",
			apiKeys: []string{"top-level-key"},
			want:    "top-level-key",
			ok:      true,
		},
		{
			name: "node-only pool",
			nodes: []config.AIRoutingNodeConfig{
				{Name: "primary", APIKeys: []string{"node-key-a", "node-key-b"}},
			},
			want: "node-key-a",
			ok:   true,
		},
		{
			name: "missing key",
			nodes: []config.AIRoutingNodeConfig{
				{Name: "broken"},
			},
			want: "",
			ok:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := firstRoutingAPIKey(tc.apiKeys, tc.nodes)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("firstRoutingAPIKey() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestFindRepoRootAcceptsBaseOnlyPackagedConfig verifies repository-root detection no longer requires a packaged config.yaml when the runtime is expected to pick up user overrides from ~/.vmm.
// TestFindRepoRootAcceptsBaseOnlyPackagedConfig 用于验证仓库根目录探测不再强制要求打包态 config.yaml，确保运行时可从 ~/.vmm 读取用户覆盖配置。
func TestFindRepoRootAcceptsBaseOnlyPackagedConfig(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, "output", "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, "internal", "logic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "output", "configs", "base.yaml"), []byte("grpc: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(repoRoot, "internal", "logic")); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(previousWD)
	}()

	got, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if got != repoRoot {
		t.Fatalf("repo root = %q, want %q", got, repoRoot)
	}
}

// TestResolveRealRuntimeLayoutSupportsBaseOnlyPackagedConfigAndHomeOverride verifies the live-runtime fixture follows the same base-plus-home-override search order as the real binary.
// TestResolveRealRuntimeLayoutSupportsBaseOnlyPackagedConfigAndHomeOverride 用于验证真实运行时测试夹具遵循与正式二进制一致的“打包 base + 用户目录 override”搜索顺序。
func TestResolveRealRuntimeLayoutSupportsBaseOnlyPackagedConfigAndHomeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	repoRoot := t.TempDir()
	systemDir := filepath.Join(repoRoot, "output", "configs")
	writePromptBundleForRealRuntimeTest(t, filepath.Join(systemDir, "prompts", "default"), "packaged-default")
	writeConfigStubForRealRuntimeTest(t, filepath.Join(systemDir, "base.yaml"))
	userConfigPath := filepath.Join(home, ".vmm", "config.yaml")
	writeConfigStubForRealRuntimeTest(t, userConfigPath)

	layout, err := resolveRealRuntimeLayout(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layout.SystemDir, systemDir; got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.UserDir, filepath.Join(home, ".vmm"); got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.BaseConfigPath, filepath.Join(systemDir, "base.yaml"); got != want {
		t.Fatalf("base config = %q, want %q", got, want)
	}
	if got := layout.SystemConfigPath; got != "" {
		t.Fatalf("system config path = %q, want empty", got)
	}
	if got, want := layout.OverrideConfigPath, userConfigPath; got != want {
		t.Fatalf("override config path = %q, want %q", got, want)
	}
	if got, want := layout.ConfigPaths(), []string{filepath.Join(systemDir, "base.yaml"), userConfigPath}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("config paths = %#v, want %#v", got, want)
	}
}

// writePromptBundleForRealRuntimeTest writes one complete default prompt bundle so layout resolution can validate packaged configs in isolation.
// writePromptBundleForRealRuntimeTest 用于写入一套完整的默认提示词包，确保布局解析测试可以独立校验打包态配置目录。
func writePromptBundleForRealRuntimeTest(t *testing.T, dir string, prefix string) {
	t.Helper()
	for _, scene := range config.RequiredScenes {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, scene), []byte(prefix+":"+scene), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeConfigStubForRealRuntimeTest writes one minimal YAML config file so layout tests can focus on path resolution instead of full runtime config shape.
// writeConfigStubForRealRuntimeTest 用于写入最小 YAML 配置文件，让布局测试聚焦于路径解析，而不是完整运行时配置结构。
func writeConfigStubForRealRuntimeTest(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("grpc: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
