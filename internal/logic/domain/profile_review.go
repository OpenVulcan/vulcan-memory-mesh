// profile_review.go declares the atomic profile-node review models shared by post-action workers, reviewer prompts, and SQLite-backed persistence.
// profile_review.go 用于声明 post-action 工作器、画像评审提示词和 SQLite 持久化共享的原子化画像节点评审模型。
package domain

import "time"

// ProfileActiveNodeRecord stores one active profile node row that can be shown to the LLM reviewer before newer evidence is applied.
// ProfileActiveNodeRecord 用于保存一条当前活跃的画像节点记录，供 LLM 在接纳新证据前进行对照评审。
type ProfileActiveNodeRecord struct {
	ID                  uint64
	TurnID              uint64
	ProfileType         int
	BindID              uint64
	Content             string
	Status              int
	Priority            int
	ProfileLevel        int
	LevelReason         string
	RefreshWeight       int
	ProfileDate         string
	SourceKind          int
	SourceID            uint64
	StatusReason        string
	ExpiresAt           time.Time
	SupersededByID      uint64
	ProfileDateAnchorAt time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ProfileReviewTargetsSnapshot carries the active user/project profile-node sets that one review call should inspect.
// ProfileReviewTargetsSnapshot 用于承载一次画像评审调用需要查看的 user/project 活跃画像节点集合。
type ProfileReviewTargetsSnapshot struct {
	UserNodes    []ProfileActiveNodeRecord
	ProjectNodes []ProfileActiveNodeRecord
}

// ProfileRenderTargetSnapshot carries one profile target plus the active nodes that should be rendered into its durable profile blob after lifecycle convergence.
// ProfileRenderTargetSnapshot 用于承载一个画像目标及其当前仍应渲染到长期 profile Blob 中的活跃节点，供生命周期收敛后重建画像文本。
type ProfileRenderTargetSnapshot struct {
	ProfileType int
	BindID      uint64
	Nodes       []ProfileActiveNodeRecord
}

// ProfileReviewAcceptedCandidate stores one accepted new candidate together with its normalized content and supersede targets.
// ProfileReviewAcceptedCandidate 用于保存一条被接纳的新候选，以及其规范化内容和要替代的旧节点集合。
type ProfileReviewAcceptedCandidate struct {
	CandidateIndex    int
	NormalizedContent string
	Priority          int
	ProfileLevel      int
	LevelReason       string
	SupersedeNodeIDs  []uint64
}

// ProfileReviewSection stores one structured review result for one target kind inside the batched profile-review response.
// ProfileReviewSection 用于保存批量画像评审响应中某一类目标的结构化评审结果。
type ProfileReviewSection struct {
	AcceptedCandidates      []ProfileReviewAcceptedCandidate
	InvalidCandidateIndexes []int
	RetireOnlyNodeIDs       []uint64
	Reason                  string
}

// TurnProfileReviewResult stores the optional user/project review sections returned by one batched review call.
// TurnProfileReviewResult 用于保存一次批量画像评审调用返回的可选 user/project 结果块。
type TurnProfileReviewResult struct {
	User    *ProfileReviewSection
	Project *ProfileReviewSection
}
