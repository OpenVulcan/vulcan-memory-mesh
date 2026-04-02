// intent.go declares structured intent-extraction models shared between processors and use cases.
// intent.go 用于声明处理器和用例层共享的结构化意图提取模型。
package domain

// IntentResult carries the parsed stage-one pre-check result, including whether memory is needed and which search sentences should drive vector recall.
// IntentResult 用于承载 pre-check 第一层的结构化结果，包括是否需要记忆，以及哪些检索语句应驱动向量召回。
type IntentResult struct {
	Queries    []string
	NeedMemory bool
	Reason     string
}

// LLMUsage stores token accounting returned by model providers in the internal response contract.
// LLMUsage 用于保存模型提供方返回的 token 用量统计。
type LLMUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
