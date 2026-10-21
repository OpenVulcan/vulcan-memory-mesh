// manager.go implements configuration and prompt loading.
// manager.go 用于实现配置与提示词加载。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PromptManager resolves one preselected prompt bundle and loads scene files for processors at runtime.
// PromptManager 用于在运行时解析一套预先选定的提示词包，并为处理器加载对应场景文件。
type PromptManager struct {
	// Pin the validated bundle so runtime reads cannot mix system and user scenes.
	// 固定已校验的提示词包，避免运行时混用系统与用户场景。
	bundleDir string
}

// NewPromptManager resolves and validates the selected prompt bundle before exposing scene reads to processors.
// NewPromptManager 用于解析并校验选定的提示词包，再向处理器开放场景读取能力。
func NewPromptManager(systemDir, userDir, promptLanguage string) (*PromptManager, error) {
	bundleDir, err := resolveSelectedPromptBundle(systemDir, userDir, normalizePromptBundleName(promptLanguage))
	if err != nil {
		return nil, err
	}
	return &PromptManager{bundleDir: bundleDir}, nil
}

// ValidatePromptBundle checks the selected prompt bundle without opening a runtime prompt manager or writing files.
// ValidatePromptBundle 在不创建运行时提示词管理器且不写入文件的前提下校验选中的提示词包。
func ValidatePromptBundle(systemDir, userDir, promptLanguage string) error {
	_, err := resolveSelectedPromptBundle(systemDir, userDir, normalizePromptBundleName(promptLanguage))
	return err
}

// GetPrompt reads one scene from the selected user-or-system prompt bundle without applying legacy language switching.
// GetPrompt 用于从选定的用户层或系统层提示词包读取场景，不再执行旧式语言切换。
func (m *PromptManager) GetPrompt(scene, _ string) (string, error) {
	// Scene names may omit the markdown extension; the selected bundle remains fixed.
	// 场景名可以省略 markdown 扩展名，但已选提示词包保持固定。
	sceneFile := scene
	if filepath.Ext(sceneFile) == "" {
		sceneFile += ".md"
	}
	body, err := os.ReadFile(filepath.Join(m.bundleDir, sceneFile))
	if err != nil {
		return "", fmt.Errorf("read prompt bundle=%q scene=%q: %w", m.bundleDir, sceneFile, err)
	}
	return string(body), nil
}

// resolveSelectedPromptBundle returns the complete user or system bundle for the selected folder, or its validation error.
// resolveSelectedPromptBundle 返回选定文件夹对应的完整用户包或系统包，失败时返回校验错误。
func resolveSelectedPromptBundle(systemDir, userDir, folder string) (string, error) {
	// Only an absent user bundle selects the system bundle; I/O errors must remain visible.
	// 只有用户包不存在时才选择系统包，输入输出错误必须明确报告。
	bundleDir := filepath.Join(systemDir, "prompts", folder)
	layer := "system"
	if strings.TrimSpace(userDir) != "" {
		userFolder := filepath.Join(userDir, "prompts", folder)
		info, err := os.Stat(userFolder)
		switch {
		case err == nil:
			if !info.IsDir() {
				return "", fmt.Errorf("user prompt bundle %q is not a directory", userFolder)
			}
			bundleDir, layer = userFolder, "user"
		case !errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("inspect user prompt bundle %q: %w", userFolder, err)
		}
	}

	// Validate every required scene against the same chosen directory and preserve filesystem causes.
	// 在同一已选目录内校验全部必需场景，并保留文件系统错误原因。
	validation := &ValidationErrors{}
	for _, scene := range RequiredScenes {
		path := filepath.Join(bundleDir, scene)
		info, err := os.Stat(path)
		if err != nil {
			validation.Add("%s prompt bundle %q is incomplete, cannot access %s: %v", layer, bundleDir, scene, err)
		} else if !info.Mode().IsRegular() {
			validation.Add("%s prompt bundle %q has a non-regular scene %s", layer, bundleDir, scene)
		}
	}
	if validation.HasAny() {
		return "", validation
	}
	return bundleDir, nil
}
