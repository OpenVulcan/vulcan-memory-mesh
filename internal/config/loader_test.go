// loader_test.go implements configuration and prompt loading.
// loader_test.go 用于实现配置与提示词加载。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvePromptLayoutDefaultsToHomeVMM verifies the TestResolvePromptLayoutDefaultsToHomeVMM behavior.
// TestResolvePromptLayoutDefaultsToHomeVMM 用于验证 TestResolvePromptLayoutDefaultsToHomeVMM 行为。
func TestResolvePromptLayoutDefaultsToHomeVMM(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "system-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "configs", "config.yaml"))

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.UserDir, filepath.Join(home, ".vmm"); got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.BaseConfigPath, filepath.Join(root, "configs", "base.yaml"); got != want {
		t.Fatalf("base config = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, filepath.Join(root, "configs", "config.yaml"); got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
	if got, want := layout.SystemConfigPath, filepath.Join(root, "configs", "config.yaml"); got != want {
		t.Fatalf("system config = %q, want %q", got, want)
	}
	if got := layout.OverrideConfigPath; got != "" {
		t.Fatalf("override config = %q, want empty", got)
	}
	if got, want := layout.ConfigPaths(), []string{
		filepath.Join(root, "configs", "base.yaml"),
		filepath.Join(root, "configs", "config.yaml"),
	}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("config chain = %#v, want %#v", got, want)
	}
}

// TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary verifies the TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary behavior.
// TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary 用于验证 TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary 行为。
func TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "output", "configs", "prompts", "default_en"), "output-default")
	writeConfigStub(t, filepath.Join(root, "output", "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "output", "configs", "config.yaml"))

	layout, err := ResolvePromptLayout(filepath.Join(root, "output", "bin", "vmm-local.exe"), root, "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "output", "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, filepath.Join(root, "output", "configs", "config.yaml"); got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutUsesOutputConfigsWithoutDefaultEnglish verifies layout resolution only requires the packaged config root and does not force a fixed prompt bundle before config loading.
// TestResolvePromptLayoutUsesOutputConfigsWithoutDefaultEnglish 用于验证布局解析在配置加载前只要求打包配置根存在，不会强制某个固定提示词包。
func TestResolvePromptLayoutUsesOutputConfigsWithoutDefaultEnglish(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "output", "configs", "prompts", "custom-bundle"), "output-custom")
	writeConfigStub(t, filepath.Join(root, "output", "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "output", "configs", "config.yaml"))

	layout, err := ResolvePromptLayout(filepath.Join(root, "output", "bin", "vmm-local.exe"), root, "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "output", "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, filepath.Join(root, "output", "configs", "config.yaml"); got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun verifies the TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun behavior.
// TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun 用于验证 TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun 行为。
func TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "project-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), filepath.Join(root, "cmd", "vmm-local"), "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutAcceptsExplicitUserDir verifies the TestResolvePromptLayoutAcceptsExplicitUserDir behavior.
// TestResolvePromptLayoutAcceptsExplicitUserDir 用于验证 TestResolvePromptLayoutAcceptsExplicitUserDir 行为。
func TestResolvePromptLayoutAcceptsExplicitUserDir(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "system-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "configs", "config.yaml"))
	userDir := filepath.Join(root, "custom-user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userConfigPath := filepath.Join(userDir, "config.yaml")
	writeConfigStub(t, userConfigPath)

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, userDir)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.UserDir, userDir; got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.OverrideConfigPath, userConfigPath; got != want {
		t.Fatalf("override config = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, userConfigPath; got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutSupportsExplicitYAMLConfigFilePath verifies callers may still point -config at one explicit YAML file while the loader derives the override root from that file's parent directory.
// TestResolvePromptLayoutSupportsExplicitYAMLConfigFilePath 用于验证调用方仍可把 -config 指向显式 YAML 文件，同时让加载器从该文件的父目录推导覆盖根目录。
func TestResolvePromptLayoutSupportsExplicitYAMLConfigFilePath(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "system-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "configs", "config.yaml"))
	userBundleDir := filepath.Join(root, "bundle")
	if err := os.MkdirAll(filepath.Join(userBundleDir, "prompts", "default_en"), 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(userBundleDir, "custom.override.yaml")
	writeConfigStub(t, configFile)

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, configFile)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.UserDir, userBundleDir; got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, configFile; got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
	if got, want := layout.OverrideConfigPath, configFile; got != want {
		t.Fatalf("override config = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutRejectsExplicitNonYAMLConfigFilePath verifies explicit main-config file arguments no longer accept legacy JSON/TOML-style suffixes, even when the file already exists or has not been created yet.
// TestResolvePromptLayoutRejectsExplicitNonYAMLConfigFilePath 用于验证显式主配置文件参数不再接受旧的 JSON/TOML 后缀，无论文件已经存在还是尚未创建都会被拒绝。
func TestResolvePromptLayoutRejectsExplicitNonYAMLConfigFilePath(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "system-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "configs", "config.yaml"))

	cases := []struct {
		name   string
		path   string
		create bool
	}{
		{name: "existing json file", path: filepath.Join(root, "bundle", "custom.override.json"), create: true},
		{name: "missing toml file", path: filepath.Join(root, "bundle", "custom.override.toml"), create: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.create {
				if err := os.MkdirAll(filepath.Dir(tc.path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tc.path, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			_, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, tc.path)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "must end with .yaml or .yml") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestResolvePromptLayoutUsesUserConfigYAMLAsOverrideWhenPresent verifies the TestResolvePromptLayoutUsesUserConfigYAMLAsOverrideWhenPresent behavior.
// TestResolvePromptLayoutUsesUserConfigYAMLAsOverrideWhenPresent 用于验证 TestResolvePromptLayoutUsesUserConfigYAMLAsOverrideWhenPresent 行为。
func TestResolvePromptLayoutUsesUserConfigYAMLAsOverrideWhenPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "system-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))
	writeConfigStub(t, filepath.Join(root, "configs", "config.yaml"))
	userConfigPath := filepath.Join(home, ".vmm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfigStub(t, userConfigPath)

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.OverrideConfigPath, userConfigPath; got != want {
		t.Fatalf("override config = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, userConfigPath; got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

// TestPromptLayoutExposesFixedPIIRuleDirs verifies the TestPromptLayoutExposesFixedPIIRuleDirs behavior.
// TestPromptLayoutExposesFixedPIIRuleDirs 用于验证 TestPromptLayoutExposesFixedPIIRuleDirs 行为。
func TestPromptLayoutExposesFixedPIIRuleDirs(t *testing.T) {
	layout := PromptLayout{
		SystemDir: filepath.Join("D:", "workspace", "configs"),
		UserDir:   filepath.Join("C:", "Users", "tester", ".vmm"),
	}
	if got, want := layout.SystemPIIRulesDir(), filepath.Join(layout.SystemDir, "pii_rules"); got != want {
		t.Fatalf("system pii_rules dir = %q, want %q", got, want)
	}
	if got, want := layout.UserPIIRulesDir(), filepath.Join(layout.UserDir, "pii_rules"); got != want {
		t.Fatalf("user pii_rules dir = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound verifies the TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound behavior.
// TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound 用于验证 TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound 行为。
func TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound(t *testing.T) {
	_, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "bin", "vmm-local.exe"), t.TempDir(), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs verifies the TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs behavior.
// TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs 用于验证 TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs 行为。
func TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default_en"), "project-default")
	writeConfigStub(t, filepath.Join(root, "configs", "base.yaml"))

	_, err := ResolvePromptLayout(filepath.Join(root, "output", "bin", "vmm-local.exe"), root, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "system config dir is missing or incomplete") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// writeConfigStub writes one minimal YAML config file so layout resolution can assert filename and search-order behavior without depending on config parsing.
// writeConfigStub 用于写入最小 YAML 配置文件，让布局解析测试只关注文件名和搜索顺序，而不依赖完整配置解析。
func writeConfigStub(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("grpc: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
