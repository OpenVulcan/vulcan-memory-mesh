// postaction_review.go declares the joint memory/profile review models used by the current post-action candidate reviewer.
// postaction_review.go 用于声明当前 post-action 联合候选评审器使用的记忆/画像联合评审模型。
package domain

// PostActionSimilarMemoryCandidate stores one high-similarity durable memory that should be shown to the post-action reviewer before a new candidate is admitted.
// PostActionSimilarMemoryCandidate 用于保存一条需要在新候选入库前展示给 post-action 评审器的高相似长期记忆。
type PostActionSimilarMemoryCandidate struct {
	MemoryID     uint64
	SourceTurnID uint64
	ScopeLevel   string
	Category     int
	Score        float64
	Origin       string
	Abstract     string
	Details      string
}

// PostActionMemoryReviewCandidate stores one new memory candidate plus its first-pass admission metadata and the strongest similar durable memories recalled for dedupe review.
// PostActionMemoryReviewCandidate 用于保存一条新记忆候选，以及其首轮准入元数据和用于去重评审的高相似长期记忆。
type PostActionMemoryReviewCandidate struct {
	CandidateIndex  int
	Category        int
	Abstract        string
	Details         string
	EvidenceSource  string
	AdmissionReason string
	SimilarMemories []PostActionSimilarMemoryCandidate
}

// PostActionCandidateReviewInput bundles the current turn summary, the post-first-pass memory candidates, and the profile review snapshot for one unified reviewer call.
// PostActionCandidateReviewInput 用于打包当前轮摘要、首轮过滤后的记忆候选以及画像评审快照，供一次统一 reviewer 调用使用。
type PostActionCandidateReviewInput struct {
	UserInputKind     string
	UserContent       string
	AssistantContent  string
	MemoryCandidates  []PostActionMemoryReviewCandidate
	ProfileTargets    ProfileReviewTargetsSnapshot
	ProfileCandidates []ProfileNodeCandidate
}

// PostActionAcceptedMemoryCandidate stores one accepted memory candidate together with the old durable memory ids it is allowed to supersede inside the reviewed scope.
// PostActionAcceptedMemoryCandidate 用于保存一条被接纳的记忆候选，以及它在当前评审作用域内允许替代的旧长期记忆 id。
type PostActionAcceptedMemoryCandidate struct {
	CandidateIndex     int
	SupersedeMemoryIDs []uint64
}

// PostActionDroppedMemoryCandidate stores one dropped memory candidate together with the optional durable memory id that the reviewer explicitly selected as the dedupe target.
// PostActionDroppedMemoryCandidate 用于保存一条被丢弃的记忆候选，以及 reviewer 显式选中的可复用长期记忆 id（如果存在）。
type PostActionDroppedMemoryCandidate struct {
	CandidateIndex int
	DedupeMemoryID uint64
}

// PostActionMemoryReviewSection stores the keep/drop decision for all memory candidates participating in one unified post-action review call.
// PostActionMemoryReviewSection 用于保存一次统一 post-action 评审里全部记忆候选的保留/丢弃决策。
type PostActionMemoryReviewSection struct {
	AcceptedCandidates       []PostActionAcceptedMemoryCandidate
	DroppedCandidates        []PostActionDroppedMemoryCandidate
	AcceptedCandidateIndexes []int
	DroppedCandidateIndexes  []int
	Reason                   string
}

// PostActionCandidateReviewResult stores the memory keep/drop section plus the optional user/project profile sections returned by one unified reviewer call.
// PostActionCandidateReviewResult 用于保存一次统一 reviewer 调用返回的记忆保留结果，以及可选的 user/project 画像评审结果。
type PostActionCandidateReviewResult struct {
	Memory  *PostActionMemoryReviewSection
	User    *ProfileReviewSection
	Project *ProfileReviewSection
}
