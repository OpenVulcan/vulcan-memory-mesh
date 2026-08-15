// intent.go declares structured intent-extraction models shared between processors and use cases.
// intent.go 用于声明处理器和用例层共享的结构化意图提取模型。
package domain

// IntentResult carries the parsed stage-one pre-check result, including whether memory is needed and which search sentences should drive vector recall.
// IntentResult 用于承载 pre-check 第一层的结构化结果，包括是否需要记忆，以及哪些检索语句应驱动向量召回。
type IntentResult struct {
	Queries    []string
	NeedMemory bool
	Reason     string
	// LLMExecution retains the physical response identity without exposing it through business JSON.
	// LLMExecution 用于保留物理响应身份，同时不通过业务 JSON 暴露。
	LLMExecution *LLMExecutionMetadata `json:"-"`
}

// LLMUsage stores token accounting returned by model providers in the internal response contract.
// LLMUsage 用于保存模型提供方返回的 token 用量统计。
type LLMUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// CachedInputTokens is the provider-reported cache-read portion of prompt tokens.
	// CachedInputTokens 是供应商上报的提示词 token 中缓存读取部分。
	CachedInputTokens int
	// ReasoningTokens is the provider-reported hidden reasoning portion of completion tokens.
	// ReasoningTokens 是供应商上报的补全 token 中隐藏推理部分。
	ReasoningTokens int
}

// LLMExecutionMetadata preserves the identity and token facts returned by one physical LLM call across later parsing, review, vector, and relational stages.
// LLMExecutionMetadata 用于在后续解析、评审、向量与关系存储阶段保留一次物理 LLM 调用返回的身份与 token 事实。
type LLMExecutionMetadata struct {
	// Purpose is the stable VMM processing scene that owns the call.
	// Purpose 是持有该调用的稳定 VMM 处理场景。
	Purpose string

	// ConfiguredModel is the model identity selected before dispatch.
	// ConfiguredModel 是分派前选定的模型身份。
	ConfiguredModel string

	// ResponseModel is the physical model identity reported by the provider response.
	// ResponseModel 是供应商响应上报的物理模型身份。
	ResponseModel string

	// RequestID is the stable cross-process or provider correlation identifier.
	// RequestID 是稳定的跨进程或供应商关联标识。
	RequestID string

	// Usage preserves complete provider token accounting for this call.
	// Usage 用于保留该调用的完整供应商 token 统计。
	Usage LLMUsage
}
