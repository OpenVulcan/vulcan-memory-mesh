// intent.go declares structured intent-extraction models shared between processors and use cases.
// intent.go 用于声明处理器和用例层共享的结构化意图提取模型。
package domain

// IntentResult carries the parsed intent-extraction result consumed by the pre-check recall flow.
// IntentResult 用于承载 pre-check 召回流程消费的结构化意图提取结果。
type IntentResult struct {
	Keywords   []string
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
