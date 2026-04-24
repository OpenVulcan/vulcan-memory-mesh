// precheck.go declares the structured pre-check review models shared by use cases and LLM-backed processors.
// precheck.go 用于声明用例层和 LLM 处理器共享的结构化 pre-check 评审模型。
package domain

// PreCheckTurnContext stores one recent turn fragment exposed to the first-stage pre-check LLM, using refined details when available and dehydrated raw text otherwise.
// PreCheckTurnContext 用于保存暴露给 pre-check 第一层 LLM 的最近 turn 片段；若已提炼则使用 details，否则使用脱水原文。
type PreCheckTurnContext struct {
	TurnID      uint64 `json:"turn_id"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

// PreCheckMemoryCandidate stores one numbered candidate memory fragment together with retrieval-side evidence so the second-stage reviewer can judge not only what the memory says but also why it matched the current request.
// PreCheckMemoryCandidate 用于保存一条带编号的候选记忆片段及其检索侧证据，让 pre-check 第二层评审器不仅能看见记忆内容，也能知道它为何命中当前请求。
type PreCheckMemoryCandidate struct {
	CandidateNumber             int      `json:"candidate_number"`
	MemoryID                    uint64   `json:"memory_id"`
	SourceTurnID                uint64   `json:"source_turn_id,omitempty"`
	CreatedTimestamp            int64    `json:"created_timestamp,omitempty"`
	CreatedDateTime             string   `json:"created_datetime,omitempty"`
	CreatedDate                 string   `json:"created_date,omitempty"`
	SourceKind                  string   `json:"source_kind,omitempty"`
	ScopeLevel                  string   `json:"scope_level,omitempty"`
	Category                    int      `json:"category"`
	Abstract                    string   `json:"abstract,omitempty"`
	Details                     string   `json:"details,omitempty"`
	Score                       float64  `json:"score"`
	ScoreLabel                  string   `json:"score_label,omitempty"`
	ScoreExplanation            string   `json:"score_explanation,omitempty"`
	Origin                      string   `json:"origin,omitempty"`
	OriginLabel                 string   `json:"origin_label,omitempty"`
	OriginExplanation           string   `json:"origin_explanation,omitempty"`
	SupportCount                int      `json:"support_count,omitempty"`
	RebuttalCount               int      `json:"rebuttal_count,omitempty"`
	MatchedContextValues        []string `json:"matched_context_values,omitempty"`
	MatchedContextSupportCount  int      `json:"matched_context_support_count,omitempty"`
	MatchedContextRebuttalCount int      `json:"matched_context_rebuttal_count,omitempty"`
	MatchedContextScoreDelta    float64  `json:"matched_context_score_delta,omitempty"`
}

// PreCheckMemoryReviewInput carries the current user request, stage-one recall reasoning, and numbered candidates into the second-stage reviewer.
// PreCheckMemoryReviewInput 用于把当前用户请求、第一层“为什么需要召回长期记忆”的推理结果，以及带编号候选送入第二层评审器。
type PreCheckMemoryReviewInput struct {
	CurrentTimestamp int64
	UserContent      string
	SearchQueries    []string
	IntentReason     string
	Candidates       []PreCheckMemoryCandidate
}

// PreCheckMemoryReviewResult stores the chosen candidate numbers returned by the second-stage reviewer together with its brief rationale.
// PreCheckMemoryReviewResult 用于保存第二层评审器返回的候选编号，以及简要理由。
type PreCheckMemoryReviewResult struct {
	SelectedCandidateNumbers []int
	Reason                   string
}
