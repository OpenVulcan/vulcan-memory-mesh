// prompt_bundle.go implements prompt-bundle selection helpers shared by config normalization, loader validation, and runtime prompt resolution.
// prompt_bundle.go 用于实现提示词包选择辅助逻辑，供配置归一化、加载期校验和运行时提示词解析共同复用。
package config

import "strings"

const (
	// defaultPromptBundle keeps the runtime's built-in prompt fallback anchored to the English prompt family.
	// defaultPromptBundle 用于把运行时内建提示词兜底目录锚定到英文提示词族。
	defaultPromptBundle = "default_en"

	// defaultChinesePromptBundle keeps the canonical built-in Chinese prompt family name stable across config aliases and docs.
	// defaultChinesePromptBundle 用于在配置别名与文档之间保持内建中文提示词族名称稳定一致。
	defaultChinesePromptBundle = "default_cn"
)

// normalizePromptBundleName canonicalizes one prompt selector so configs can use built-in language aliases while still allowing direct custom bundle names.
// normalizePromptBundleName 用于规范化单个提示词选择值，让配置既能使用内建语言别名，也能直接指向自定义提示词目录名。
func normalizePromptBundleName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "", "default", "en", "english", "default_en":
		return defaultPromptBundle
	case "cn", "zh", "zh-cn", "zh_hans", "chinese", "default_cn":
		return defaultChinesePromptBundle
	default:
		return trimmed
	}
}

