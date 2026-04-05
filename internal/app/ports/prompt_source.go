// prompt_source.go re-exports the logic-owned prompt lookup port for the application layer.
// prompt_source.go 用于为应用层重导出由 logic 层拥有的提示词查找端口。
package ports

import logicports "github.com/openvulcan/vmm/internal/logic/ports"

// PromptSource abstracts prompt retrieval so processors do not depend on file-system details.
// PromptSource 用于抽象提示词读取过程，让处理器不依赖文件系统细节。
type PromptSource = logicports.PromptSource
