// manager_test.go verifies prompt-bundle selection and completeness checks after prompt routing moved away from model-name matching.
// manager_test.go 用于验证取消模型名提示词路由后，提示词包选择与完整性校验行为。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewPromptManagerFailsWhenSystemDefaultEnglishBundleIsIncomplete verifies startup still fails fast when the built-in default English bundle is incomplete.
// TestNewPromptManagerFailsWhenSystemDefaultEnglishBundleIsIncomplete 用于验证当内建英文默认提示词包不完整时，启动仍会快速失败。
func TestNewPromptManagerFailsWhenSystemDefaultEnglishBundleIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()

	writeScene(t, filepath.Join(systemDir, "prompts", "default_en"), "precheck_l1_main.md", "ok")

	_, err := NewPromptManager(systemDir, userDir, "")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "system prompt bundle") || !strings.Contains(err.Error(), "default_en") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerUsesSelectedCustomBundleWithoutDefaultEnglish verifies startup no longer requires default_en when config explicitly selects another complete prompt bundle.
// TestNewPromptManagerUsesSelectedCustomBundleWithoutDefaultEnglish 用于验证当配置显式选择其他完整提示词包时，启动不再强制要求 default_en 存在。
func TestNewPromptManagerUsesSelectedCustomBundleWithoutDefaultEnglish(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom-bundle"), "system-custom")

	manager, err := NewPromptManager(systemDir, userDir, "custom-bundle")
	if err != nil {
		t.Fatal(err)
	}

	body, err := manager.GetPrompt("precheck_l1_main", "ignored-model")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "system-custom:precheck_l1_main.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestNewPromptManagerFailsWhenSelectedSystemBundleIsIncomplete verifies the explicitly selected system prompt bundle must contain every required scene file.
// TestNewPromptManagerFailsWhenSelectedSystemBundleIsIncomplete 用于验证显式选中的系统提示词包必须包含全部必需场景文件。
func TestNewPromptManagerFailsWhenSelectedSystemBundleIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_en"), "default-en")
	writeScene(t, filepath.Join(systemDir, "prompts", "custom-bundle"), "precheck_l1_main.md", "only one")

	_, err := NewPromptManager(systemDir, userDir, "custom-bundle")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `system prompt bundle`) || !strings.Contains(err.Error(), `custom-bundle`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewPromptManagerFailsWhenSelectedUserBundleIsIncomplete verifies a user override bundle cannot partially shadow the selected prompt bundle.
// TestNewPromptManagerFailsWhenSelectedUserBundleIsIncomplete 用于验证用户覆盖提示词包不能以不完整目录的方式部分遮蔽当前选中的提示词包。
func TestNewPromptManagerFailsWhenSelectedUserBundleIsIncomplete(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_en"), "default-en")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_cn"), "default-cn")
	writeScene(t, filepath.Join(userDir, "prompts", "default_cn"), "precheck_l1_main.md", "user-only")

	_, err := NewPromptManager(systemDir, userDir, "default_cn")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `user prompt bundle`) || !strings.Contains(err.Error(), `default_cn`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPromptManagerUsesDefaultEnglishBundle verifies an empty prompt selector falls back to the built-in English prompt bundle.
// TestPromptManagerUsesDefaultEnglishBundle 用于验证当提示词选择为空时，会回退到内建英文默认提示词包。
func TestPromptManagerUsesDefaultEnglishBundle(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_en"), "system-default-en")

	manager, err := NewPromptManager(systemDir, userDir, "")
	if err != nil {
		t.Fatal(err)
	}

	body, err := manager.GetPrompt("precheck_l1_main", "ignored-model")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "system-default-en:precheck_l1_main.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestPromptManagerNormalizesChineseAlias verifies built-in Chinese aliases resolve to the canonical default_cn bundle.
// TestPromptManagerNormalizesChineseAlias 用于验证内建中文别名会解析到规范化的 default_cn 提示词包。
func TestPromptManagerNormalizesChineseAlias(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_en"), "system-default-en")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_cn"), "system-default-cn")

	manager, err := NewPromptManager(systemDir, userDir, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}

	body, err := manager.GetPrompt("profile_instruction_main", "ignored-model")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "system-default-cn:profile_instruction_main.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// TestPromptManagerPrefersSelectedUserBundle verifies the selected user bundle overrides the packaged system bundle only when the user bundle is complete.
// TestPromptManagerPrefersSelectedUserBundle 用于验证当用户提示词包完整时，当前选中的用户包会优先覆盖打包系统包。
func TestPromptManagerPrefersSelectedUserBundle(t *testing.T) {
	systemDir := t.TempDir()
	userDir := t.TempDir()
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "default_en"), "system-default-en")
	writeRequiredScenes(t, filepath.Join(systemDir, "prompts", "custom-bundle"), "system-custom")
	writeRequiredScenes(t, filepath.Join(userDir, "prompts", "custom-bundle"), "user-custom")

	manager, err := NewPromptManager(systemDir, userDir, "custom-bundle")
	if err != nil {
		t.Fatal(err)
	}

	body, err := manager.GetPrompt("postaction_l2_main", "ignored-model")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := body, "user-custom:postaction_l2_main.md"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

// writeRequiredScenes creates one complete prompt bundle for manager tests.
// writeRequiredScenes 用于为 manager 测试创建一套完整提示词包。
func writeRequiredScenes(t *testing.T, dir, prefix string) {
	t.Helper()
	for _, scene := range RequiredScenes {
		writeScene(t, dir, scene, prefix+":"+scene)
	}
}

// writeScene writes one prompt scene file for manager tests.
// writeScene 用于为 manager 测试写入单个提示词场景文件。
func writeScene(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
