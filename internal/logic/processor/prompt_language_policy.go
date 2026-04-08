// prompt_language_policy.go implements shared prompt augmentation used by the main LLM stages.
// prompt_language_policy.go 用于实现主 LLM 阶段共享的提示词增强规则。
package processor

import "strings"

const mainPromptLanguagePolicy = `

# Unified Output Language Rule / 统一输出语言规则
1. Infer the output language from the user's latest natural-language input first. / 优先根据用户最新一条自然语言输入判断输出语言。
2. If the latest user input is mixed-language, prefer its dominant language; if still unclear, fall back to the dominant language across the current turn and directly related context. / 如果最新用户输入是混合语种，优先使用其中的主导语言；如果仍不明确，则回退到当前 turn 与直接相关上下文里的主导语言。
3. All generated free-text fields must use that chosen language consistently. / 你生成的所有自由文本字段都必须一致地使用该目标语言。
4. Do not mix Chinese and English inside one generated field unless you are preserving code, API names, config keys, file paths, product names, or quoted source text. / 除非是在保留代码、API 名、配置键、文件路径、产品名或原文引用，否则不要在同一个生成字段里混用中英文。
5. Keep JSON keys, enum values, code identifiers, protocol fields, and other machine-readable tokens unchanged. / JSON 键名、枚举值、代码标识符、协议字段和其他机器可读 token 必须保持原样，不要翻译。
`

// withMainPromptLanguagePolicy appends one shared language-alignment rule to the main prompt scenes so every major LLM stage follows the user's current dialogue language consistently.
// withMainPromptLanguagePolicy 用于给主提示词场景追加统一语言对齐规则，确保各个核心 LLM 阶段都稳定跟随用户当前对话语言输出。
func withMainPromptLanguagePolicy(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	return prompt + mainPromptLanguagePolicy
}
