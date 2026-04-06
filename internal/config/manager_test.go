// manager_test.go implements configuration and prompt loading.
// manager_test.go 用于实现配置与提示词加载。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete verifies the TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete behavior.
// TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete 用于验证 TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete 行为。
func TestNewPromptManagerFailsWhenSystemDefaultIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()

	writeScene(t, filepath.Join(systemDir, "prompts", "default"), "extract_intent.md", "ok")

	_, err := NewPromptManager(systemDir, userDir, RouteMap{"*": "default"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "system default prompt missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete verifies the TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete behavior.
// TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete 用于验证 TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete 行为。
func TestNewPromptManagerFailsWhenSystemRouteFolderIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeScene(t, filepath.Join(systemDir, "prompts", "qwen-fast"), "extract_intent.md", "only one")
	routes := RouteMap{
		"qwen-fast*": "qwen-fast",
		"*":          "default",
	}

	_, err := NewPromptManager(systemDir, userDir, routes)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `system prompt folder`) || !strings.Contains(err.Error(), `qwen-fast`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete verifies the TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete behavior.
// TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete 用于验证 TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete 行为。
func TestNewPromptManagerFailsWhenUserOverrideFolderIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-fast"), "system-fast")
	routes := RouteMap{
		"qwen-fast*": "qwen-fast",
		"*":          "default",
	}

	writeScene(t, filepath.Join(userDir, "prompts", "qwen-fast"), "extract_intent.md", "user-only")

	_, err := NewPromptManager(systemDir, userDir, routes)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `user prompt folder`) || !strings.Contains(err.Error(), `qwen-fast`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault verifies the TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault behavior.
// TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault 用于验证 TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault 行为。
func TestNewPromptManagerFailsWhenWildcardRouteDoesNotPointToDefault(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom"), "custom")
	routes := RouteMap{
		"*": "custom",
	}

	_, err := NewPromptManager(systemDir, userDir, routes)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `route "*" must point to "default"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerFailsWhenRouteEntryIsBlank verifies direct prompt-manager construction still rejects malformed route keys or folders instead of silently normalizing them away.
// TestNewPromptManagerFailsWhenRouteEntryIsBlank 用于验证直接构造提示词管理器时，仍会拒绝非法的空路由键或空目录，而不是静默归一化后忽略。
func TestNewPromptManagerFailsWhenRouteEntryIsBlank(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom"), "custom")

	cases := []struct {
		name   string
		routes RouteMap
		want   string
	}{
		{
			name: "empty-key",
			routes: RouteMap{
				"":  "custom",
				"*": "default",
			},
			want: "prompts.routes contains empty model key",
		},
		{
			name: "empty-folder",
			routes: RouteMap{
				"qwen*": "",
				"*":     "default",
			},
			want: `prompts.routes["qwen*"] must not be empty`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPromptManager(systemDir, userDir, tc.routes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestPromptManagerUsesLongestPrefixAndUserPriority verifies the TestPromptManagerUsesLongestPrefixAndUserPriority behavior.
// TestPromptManagerUsesLongestPrefixAndUserPriority 用于验证 TestPromptManagerUsesLongestPrefixAndUserPriority 行为。
func TestPromptManagerUsesLongestPrefixAndUserPriority(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()

	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "system-default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-base"), "system-base")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "qwen-flash"), "system-flash")
	routes := RouteMap{
		"qwen*":          "qwen-base",
		"qwen3.5-flash*": "qwen-flash",
		"*":              "default",
	}

	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "extract_intent.md", "user flash extract")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "assemble_context.md", "user flash assemble")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "analyze_turn.md", "user flash analyze")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "summarize_entry.md", "user flash summarize")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "merge_profile.md", "user flash merge")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "review_postaction_candidates.md", "user flash review post-action candidates")
	writeScene(t, filepath.Join(userDir, "prompts", "qwen-flash"), "review_profile_instruction.md", "user flash review profile instruction")

	manager, err := NewPromptManager(systemDir, userDir, routes)
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

// TestPromptManagerLoadsUserPromptOverridesFromDefaultHome verifies the TestPromptManagerLoadsUserPromptOverridesFromDefaultHome behavior.
// TestPromptManagerLoadsUserPromptOverridesFromDefaultHome 用于验证 TestPromptManagerLoadsUserPromptOverridesFromDefaultHome 行为。
func TestPromptManagerLoadsUserPromptOverridesFromDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	systemDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default"), "system-default")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom-user"), "system-custom")
	routes := RouteMap{
		"gpt-4.1*": "custom-user",
		"*":        "default",
	}

	userDir := filepath.Join(home, ".vmm")
	writeRequiredScenes(t, filepath.Join(userDir, "prompts", "custom-user"), "user-custom")

	manager, err := NewPromptManager(systemDir, userDir, routes)
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

// writeRequiredScenes executes the writeRequiredScenes logic.
// writeRequiredScenes 用于执行 writeRequiredScenes 逻辑。
func writeRequiredScenes(t *testing.T, dir, prefix string) {
	t.Helper()
	for _, scene := range RequiredScenes {
		writeScene(t, dir, scene, prefix+":"+scene)
	}
}

// writeScene executes the writeScene logic.
// writeScene 用于执行 writeScene 逻辑。
func writeScene(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
