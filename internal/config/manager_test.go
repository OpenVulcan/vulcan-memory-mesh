package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()

	writeScene(t, filepath.Join(systemDir, "prompts", "default"), "extract_intent.md", "ok")

	_, err := NewPromptManager(systemDir, userDir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "system default prompt missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeScene(t, filepath.Join(systemDir, "prompts", "qwen-fast"), "extract_intent.md", "only one")
	writeRoutes(t, filepath.Join(systemDir, "prompts-routes.json"), RouteMap{
		"qwen-fast*": "qwen-fast",
		"*":          "default",
	})

	_, err := NewPromptManager(systemDir, userDir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `system prompt folder`) || !strings.Contains(err.Error(), `qwen-fast`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-fast"), "system-fast")
	writeRoutes(t, filepath.Join(systemDir, "prompts-routes.json"), RouteMap{
		"qwen-fast*": "qwen-fast",
		"*":          "default",
	})

	writeScene(t, filepath.Join(userDir, "prompts", "qwen-fast"), "extract_intent.md", "user-only")

	_, err := NewPromptManager(systemDir, userDir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `user prompt folder`) || !strings.Contains(err.Error(), `qwen-fast`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom"), "custom")
	writeRoutes(t, filepath.Join(systemDir, "prompts-routes.json"), RouteMap{
		"*": "custom",
	})

	_, err := NewPromptManager(systemDir, userDir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `route "*" must point to "default"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPromptManagerUsesLongestPrefixAndUserPriority(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()

	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "system-default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-base"), "system-base")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-flash"), "system-flash")
	writeRoutes(t, filepath.Join(systemDir, "prompts-routes.json"), RouteMap{
		"qwen*":          "qwen-base",
		"qwen3.5-flash*": "qwen-flash",
		"*":              "default",
	})

	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "extract_intent.md", "user flash extract")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "assemble_context.md", "user flash assemble")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "summarize_entry.md", "user flash summarize")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "merge_profile.md", "user flash merge")

	manager, err := NewPromptManager(systemDir, userDir)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := manager.MatchFolder("qwen3.5-flash-plus"), "qwen-flash"; got != want {
		t.Fatalf("folder = %q, want %q", got, want)
	}
	if got, want := manager.MatchFolder("qwen2.0"), "qwen-base"; got != want {
		t.Fatalf("folder = %q, want %q", got, want)
	}
	if got, want := manager.MatchFolder("gpt-5"), "default"; got != want {
		t.Fatalf("folder = %q, want %q", got, want)
	}

	body, err := manager.GetPrompt("extract_intent", "qwen3.5-flash-plus")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "user flash extract"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}

	body, err = manager.GetPrompt("merge_profile", "qwen2.0")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "system-base:merge_profile.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestPromptManagerLoadsRoutesAndPromptsFromDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	systemDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "system-default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom-user"), "system-custom")
	writeRoutes(t, filepath.Join(systemDir, "prompts-routes.json"), RouteMap{
		"*": "default",
	})

	userDir := filepath.Join(home, ".vmm")
	writeRequiredScenes(t, filepath.Join(userDir, "prompts", "custom-user"), "user-custom")
	writeRoutes(t, filepath.Join(userDir, "prompts-routes.json"), RouteMap{
		"gpt-4.1*": "custom-user",
	})

	manager, err := NewPromptManager(systemDir, userDir)
	if err != nil {
		t.Fatal(err)
	}

	body, err := manager.GetPrompt("summarize_entry", "gpt-4.1-mini")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "user-custom:summarize_entry.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func writeRequiredScenes(t *testing.T, dir, prefix string) {
	t.Helper()
	for _, scene := range RequiredScenes {
		writeScene(t, dir, scene, prefix+":"+scene)
	}
}

func writeScene(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRoutes(t *testing.T, path string, routes RouteMap) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\n"
	first := true
	for key, folder := range routes {
		if !first {
			body += ",\n"
		}
		first = false
		body += `  "` + key + `": "` + folder + `"`
	}
	body += "\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
