package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
}

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

func TestResolvePromptLayoutAcceptsExplicitUserDir(t *testing.T) {
	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")
	userDir := filepath.Join(root, "custom-user")

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, userDir, "local")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.UserDir, userDir; got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
}

func TestResolvePromptLayoutSupportsLegacyConfigFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	root := t.TempDir()
	writeRequiredScenes(t, filepath.Join(root, "configs", "prompts", "default"), "system-default")
	configFile := filepath.Join(root, "configs", "local.json")
	if err := os.WriteFile(configFile, []byte(`{"http":{"listen_addr":":8080"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	layout, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"), root, configFile, "local")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layout.UserDir, filepath.Join(home, ".vmm"); got != want {
		t.Fatalf("user dir = %q, want %q", got, want)
	}
	if got, want := layout.AppConfigPath, configFile; got != want {
		t.Fatalf("app config = %q, want %q", got, want)
	}
}

func TestResolvePromptLayoutFailsWhenSystemDirCannotBeFound(t *testing.T) {
	_, err := ResolvePromptLayout(filepath.Join(t.TempDir(), "bin", "vmm-local.exe"), t.TempDir(), "", "local")
	if err == nil {
		t.Fatal("expected error")
	}
}

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
