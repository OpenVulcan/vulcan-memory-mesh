// noise_validate.go exposes read-only validation for the noise rule bundles used by the admission gate.
// noise_validate.go 用于提供准入门控器使用的噪声规则包只读校验能力。
package processor

import (
	"strings"
)

const defaultNoiseSemanticThreshold = 0.88

// ValidateNoiseRules loads and compiles the selected common and language noise bundles without embedding, networking, or database access.
// ValidateNoiseRules 在不执行向量生成、网络访问或数据库访问的前提下加载并编译选中的公共和语言噪声规则包。
func ValidateNoiseRules(systemDir, userDir, defaultLanguage string, defaultThreshold float64) error {
	// Match NewNoiseGate's runtime defaults before compiling so validation and startup accept the same threshold semantics.
	// 在编译前应用与 NewNoiseGate 相同的运行时默认值，确保校验与启动使用一致的阈值语义。
	language := strings.TrimSpace(defaultLanguage)
	if language == "" {
		language = "zh-CN"
	}
	threshold := defaultThreshold
	if threshold <= 0 {
		threshold = defaultNoiseSemanticThreshold
	}

	// Reuse the runtime compiler to preserve user-first selection and system fallback without constructing an embedding client.
	// 复用运行时编译器，在不创建 embedding 客户端的前提下保持用户优先选择和系统回退行为。
	if _, _, err := loadCompiledNoiseCategories(systemDir, userDir, language, threshold); err != nil {
		return err
	}
	return nil
}
