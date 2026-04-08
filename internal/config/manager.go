// manager.go implements configuration and prompt loading.
// manager.go 用于实现配置与提示词加载。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PromptManager resolves one preselected prompt bundle and loads scene files for processors at runtime.
// PromptManager 用于在运行时解析一套预先选定的提示词包，并为处理器加载对应场景文件。
type PromptManager struct {
	systemDir      string
	userDir        string
	selectedFolder string
}

// NewPromptManager creates a PromptManager instance from the selected prompt-bundle token.
// NewPromptManager 用于基于选中的提示词包标识创建 PromptManager 实例。
func NewPromptManager(systemDir, userDir, promptLanguage string) (*PromptManager, error) {
	// Validate the built-in default prompt bundle first so every runtime keeps one safe fallback family available.
	// 先校验内建默认提示词包，确保每个运行时都拥有一套安全可用的默认提示词族。
	validation := &ValidationErrors{}
	validateSystemPromptBundle(systemDir, defaultPromptBundle, validation)

	// Resolve the runtime-selected prompt bundle and verify the active directory is complete before the manager becomes usable.
	// 解析运行时选中的提示词包，并在管理器可用前校验当前生效目录完整无缺。
	selectedFolder := normalizePromptBundleName(promptLanguage)
	validateSelectedPromptBundle(systemDir, userDir, selectedFolder, validation)
	if validation.HasAny() {
		return nil, validation
	}

	return &PromptManager{
		systemDir:      systemDir,
		userDir:        userDir,
		selectedFolder: selectedFolder,
	}, nil
}

// GetPrompt executes the GetPrompt logic.
// GetPrompt 用于执行 GetPrompt 逻辑。
func (m *PromptManager) GetPrompt(scene, _ string) (string, error) {
	// Normalize the requested scene name into an on-disk markdown filename.
	// 将请求的场景名归一成磁盘上的 markdown 文件名。
	sceneFile := scene
	if filepath.Ext(sceneFile) == "" {
		sceneFile += ".md"
	}

	// Prefer the selected user prompt bundle when it exists, otherwise fall back to the selected system prompt bundle.
	// 优先读取已选中的用户提示词包；若不存在，则回退到已选中的系统提示词包。
	userPath := filepath.Join(m.userDir, "prompts", m.selectedFolder, sceneFile)
	if body, err := os.ReadFile(userPath); err == nil {
		return string(body), nil
	}

	systemPath := filepath.Join(m.systemDir, "prompts", m.selectedFolder, sceneFile)
	body, err := os.ReadFile(systemPath)
	if err != nil {
		return "", fmt.Errorf("read prompt folder=%q scene=%q: %w", m.selectedFolder, sceneFile, err)
	}
	return string(body), nil
}

// validateSystemPromptBundle validates one required built-in prompt bundle shipped with the system config root.
// validateSystemPromptBundle 用于校验系统配置根目录中必须随包分发的一套内建提示词包。
func validateSystemPromptBundle(systemDir, folder string, validation *ValidationErrors) {
	dir := filepath.Join(systemDir, "prompts", folder)
	missing := missingScenes(dir)
	if len(missing) == 0 {
		return
	}
	for _, scene := range missing {
		validation.Add("system prompt bundle missing: %s", filepath.Join(dir, scene))
	}
}

// validateSelectedPromptBundle validates the active prompt bundle chosen by config so startup fails when the selected user/system directory is missing or incomplete.
// validateSelectedPromptBundle 用于校验配置选中的生效提示词包，确保启动时若用户/系统目录缺失或不完整会直接失败。
func validateSelectedPromptBundle(systemDir, userDir, folder string, validation *ValidationErrors) {
	if validation == nil {
		return
	}
	folder = strings.TrimSpace(folder)
	if folder == "" {
		validation.Add("prompts.prompt_language resolved to an empty prompt bundle")
		return
	}

	userFolder := filepath.Join(userDir, "prompts", folder)
	systemFolder := filepath.Join(systemDir, "prompts", folder)
	if dirExists(userFolder) {
		for _, scene := range missingScenes(userFolder) {
			validation.Add("user prompt bundle %q is incomplete, missing %s", userFolder, scene)
		}
		return
	}
	for _, scene := range missingScenes(systemFolder) {
		validation.Add("system prompt bundle %q is incomplete, missing %s", systemFolder, scene)
	}
}
