// precheck.go declares the structured pre-check review models shared by use cases and LLM-backed processors.
// precheck.go 用于声明用例层和 LLM 处理器共享的结构化 pre-check 评审模型。
package domain

// PreCheckMemoryCandidate stores one candidate memory fragment that the second-stage pre-check reviewer may adopt for injection.
// PreCheckMemoryCandidate 用于保存一条可供 pre-check 第二层评审器采纳并注入的候选记忆片段。
type PreCheckMemoryCandidate struct {
	MemoryID     uint64
	SourceTurnID uint64
	SourceKind   string
	ScopeLevel   string
	Category     int
	Abstract     string
	Details      string
	Score        float64
	Origin       string
}

// PreCheckMemoryReviewInput carries the current user request, extracted intent, and candidate memories into the second-stage reviewer.
// PreCheckMemoryReviewInput 用于把当前用户请求、第一层意图结果和候选记忆送入第二层评审器。
type PreCheckMemoryReviewInput struct {
	UserContent           string
	IntentKeywords        []string
	IntentReason          string
	RecentSessionMemories []PreCheckMemoryCandidate
	RetrievedMemories     []PreCheckMemoryCandidate
}

// PreCheckMemoryReviewResult stores the durable memory ids selected by the second-stage reviewer together with its brief rationale.
// PreCheckMemoryReviewResult 用于保存第二层评审器选中的长期记忆 id，以及简要理由。
type PreCheckMemoryReviewResult struct {
	SelectedMemoryIDs []uint64
	Reason            string
}
