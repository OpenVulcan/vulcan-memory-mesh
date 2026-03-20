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
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, "", "local")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.UserDir, filepath.Join(home, ".vmm"); got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, filepath.Join(root, "configs", "local.json"); got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
	if got, want := layout.SystemConfigPath, filepath.Join(root, "configs", "local.json"); got != want {
		t.Fatalf("system config = %q, want %q", got, want)
	}
	if got := layout.OverrideConfigPath; got != "" {
		t.Fatalf("override config = %q, want empty", got)
	}
}

// TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary verifies the TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary behavior.
// TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary 用于验证 TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary 行为。
func TestResolvePromptLayoutUsesOutputConfigsForBuiltBinary(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "output", "configs", "prompts", "default"), "output-default")

	layout, err := ResolvePromptLayout(filepath.Join(root, "output", "bin", "vmm-local.exe"), root, "", "local")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.SystemDir, filepath.Join(root, "output", "configs"); got != want {
		t.Fatalf("system dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, filepath.Join(root, "output", "configs", "local.json"); got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

// TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun verifies the TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun behavior.
// TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun 用于验证 TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun 行为。
func TestResolvePromptLayoutFallsBackToProjectConfigsForGoRun(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "project-default")

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), filepath.Join(root, "cmd", "vmm-local"), "", "local")
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
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")
	userDir := filepath.Join(root, "custom-user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userConfigPath := filepath.Join(userDir, "local.json")
	if err := os.WriteFile(userConfigPath, []byte(`{"http":{"listen_addr":":8081"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, userDir, "local")
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

// TestResolvePromptLayoutSupportsLegacyConfigFilePath verifies the TestResolvePromptLayoutSupportsLegacyConfigFilePath behavior.
// TestResolvePromptLayoutSupportsLegacyConfigFilePath 用于验证 TestResolvePromptLayoutSupportsLegacyConfigFilePath 行为。
func TestResolvePromptLayoutSupportsLegacyConfigFilePath(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")
	userBundleDir := filepath.Join(root, "bundle")
	if err := os.MkdirAll(filepath.Join(userBundleDir, "prompts", "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(userBundleDir, "local.json")
	if err := os.WriteFile(configFile, []byte(`{"http":{"listen_addr":":8080"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, configFile, "local")
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

// TestResolvePromptLayoutUsesUserLocalJSONAsOverrideWhenPresent verifies the TestResolvePromptLayoutUsesUserLocalJSONAsOverrideWhenPresent behavior.
// TestResolvePromptLayoutUsesUserLocalJSONAsOverrideWhenPresent 用于验证 TestResolvePromptLayoutUsesUserLocalJSONAsOverrideWhenPresent 行为。
func TestResolvePromptLayoutUsesUserLocalJSONAsOverrideWhenPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")
	userConfigPath := filepath.Join(home, ".vmm", "local.json")
	if err := os.MkdirAll(filepath.Dir(userConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userConfigPath, []byte(`{"http":{"listen_addr":":8082"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, "", "local")
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
	_, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "bin", "vmm-local.exe"), t.TempDir(), "", "local")
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs verifies the TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs behavior.
// TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs 用于验证 TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs 行为。
func TestResolvePromptLayoutFailsForBuiltBinaryWithoutSiblingConfigs(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "project-default")

	_, err := ResolvePromptLayout(filepath.Join(root, "output", "bin", "vmm-local.exe"), root, "", "local")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "system config dir is missing or incomplete") {
		t.Fatalf("unexpected error: %v", err)
	}
}
