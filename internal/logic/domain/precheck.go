// precheck.go declares the structured pre-check review models shared by use cases and LLM-backed processors.
// precheck.go 用于声明用例层和 LLM 处理器共享的结构化 pre-check 评审模型。
package domain

// PreCheckTurnContext stores one recent turn fragment exposed to the first-stage pre-check LLM, using refined details when available and dehydrated raw text otherwise.
// PreCheckTurnContext 用于保存暴露给 pre-check 第一层 LLM 的最近 turn 片段；若已提炼则使用 details，否则使用脱水原文。
type PreCheckTurnContext struct {
	TurnID      uint64
	ContentType string
	Content     string
}

// PreCheckMemoryCandidate stores one numbered candidate memory fragment that the second-stage pre-check reviewer may adopt for injection.
// PreCheckMemoryCandidate 用于保存一条带编号的候选记忆片段，供 pre-check 第二层评审器采纳并注入。
type PreCheckMemoryCandidate struct {
	CandidateNumber int
	MemoryID        uint64
	SourceTurnID    uint64
	SourceKind      string
	ScopeLevel      string
	Category        int
	Abstract        string
	Details         string
	Score           float64
	Origin          string
}

// PreCheckMemoryReviewInput carries the current user request, stage-one search reasoning, and numbered candidates into the second-stage reviewer.
// PreCheckMemoryReviewInput 用于把当前用户请求、第一层检索推理结果以及带编号候选送入第二层评审器。
type PreCheckMemoryReviewInput struct {
	UserContent   string
	SearchQueries []string
	IntentReason  string
	Candidates    []PreCheckMemoryCandidate
}

// PreCheckMemoryReviewResult stores the chosen candidate numbers returned by the second-stage reviewer together with its brief rationale.
// PreCheckMemoryReviewResult 用于保存第二层评审器返回的候选编号，以及简要理由。
type PreCheckMemoryReviewResult struct {
	SelectedCandidateNumbers []int
	Reason                   string
}
