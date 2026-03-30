// profile_merge.go declares the profile-merge models shared by post-action use cases, LLM processors, and DuckDB persistence.
// profile_merge.go 用于声明 post-action 用例、LLM 处理器和 DuckDB 持久化共享的画像合并模型。
package domain

// ProfileTargetsSnapshot carries the current durable user/project profile blobs that the merge processor must fold new evidence into.
// ProfileTargetsSnapshot 用于承载当前长期用户/项目画像 Blob，让合并处理器把新的证据节点折叠进去。
type ProfileTargetsSnapshot struct {
	UserProfile    string
	ProjectProfile string
}

// ProfileMergeSection stores one structured merge outcome for one target kind inside the batched profile-merge response.
// ProfileMergeSection 用于保存批量画像合并响应中某一类目标的结构化合并结果。
type ProfileMergeSection struct {
	UpdatedProfile          string
	MergedCandidateIndexes  []int
	InvalidCandidateIndexes []int
	Reason                  string
}

// TurnProfileMergeResult stores the optional user/project merge sections returned by one batched profile-merge call.
// TurnProfileMergeResult 用于保存一次批量画像合并调用返回的可选 user/project 结果块。
type TurnProfileMergeResult struct {
	User    *ProfileMergeSection
	Project *ProfileMergeSection
}
