package domain

type IntentResult struct {
	Keywords   []string
	NeedMemory bool
	Reason     string
}

type LLMUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
