// prompt_source.go declares the prompt lookup port owned by the logic layer.
// prompt_source.go 用于声明由 logic 层拥有的提示词查找端口。
package ports

// PromptSource abstracts prompt retrieval so processors do not depend on file-system details.
// PromptSource 用于抽象提示词读取过程，让处理器不依赖文件系统细节。
type PromptSource interface {
	GetPrompt(scene, modelName string) (string, error)
}
