// session_analysis.go declares the session-batch analysis models shared by post-action workers, processors, and DuckDB persistence.
// session_analysis.go 用于声明 post-action 队列工作器、处理器和 DuckDB 持久化共享的 session 批处理分析模型。
package domain

import "time"

// SessionTurnRecord stores one durable turn row loaded back from DuckDB for queued batch analysis.
// SessionTurnRecord 用于保存从 DuckDB 重新加载的一条长期 turn 记录，供队列批处理分析使用。
type SessionTurnRecord struct {
	ID                uint64
	SessionID         uint64
	ProjectID         uint64
	DehydratedContent string
	DehydratedBudget  int
	ExtractedStatus   int
	Details           string
	DetailsBudget     int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TurnDetailTimelineItem stores one parsed middle timeline message extracted from the dehydrated turn payload.
// TurnDetailTimelineItem 用于保存从脱水 turn 载荷中解析出来的一条中间 timeline 消息。
type TurnDetailTimelineItem struct {
	Type    string
	Content string
}

// TurnDetailWindow stores the neighboring turn ids around one anchor turn so callers can continue finer-grained follow-up queries.
// TurnDetailWindow 用于保存某个锚点 turn 周围的相邻 turn id，方便调用方继续发起更细粒度的后续查询。
type TurnDetailWindow struct {
	TurnID          uint64
	PreviousTurnIDs []uint64
	NextTurnIDs     []uint64
}

// SessionMemoryNodeRecord stores one active memory node row that the batch analyzer can reference when deciding what to supersede.
// SessionMemoryNodeRecord 用于保存一条活跃记忆节点记录，让批处理分析器在判断哪些旧记忆需要淘汰时可以引用它。
type SessionMemoryNodeRecord struct {
	ID         uint64
	ProjectID  uint64
	UserID     uint64
	TurnID     uint64
	VectorID   string
	Category   int
	Abstract   string
	Details    string
	NodeStatus int
	CreatedAt  time.Time
}

// SessionAnalysisContext carries the exact session batch window that one queued LLM call should inspect.
// SessionAnalysisContext 用于承载一次排队 LLM 调用需要检查的精确 session 批处理窗口。
type SessionAnalysisContext struct {
	Session           SessionRef
	HistoryTurns      []SessionTurnRecord
	PendingTurns      []SessionTurnRecord
	ActiveMemoryNodes []SessionMemoryNodeRecord
}

// SessionBatchTurnAnalysis stores the extracted result for one pending turn inside a larger queued batch.
// SessionBatchTurnAnalysis 用于保存较大批处理中某一条待处理 turn 的提炼结果。
type SessionBatchTurnAnalysis struct {
	TurnID        uint64
	Details       string
	DetailsBudget int
	MemoryNodes   []MemoryNodeCandidate
	ProfileNodes  []ProfileNodeCandidate
}

// SessionBatchAnalysis stores the full structured output returned by the queued session-batch LLM scene.
// SessionBatchAnalysis 用于保存队列式 session 批处理 LLM 场景返回的完整结构化结果。
type SessionBatchAnalysis struct {
	Turns                 []SessionBatchTurnAnalysis
	ObsoleteMemoryTurnIDs []uint64
	RetiredProfileNodeIDs []uint64
	UserProfileMerged     bool
	MergedUserProfile     string
	ProjectProfileMerged  bool
	MergedProjectProfile  string
}

// SessionAnalysisApplyResult returns the follow-up cleanup coordinates produced by DuckDB persistence.
// SessionAnalysisApplyResult 用于返回 DuckDB 持久化后生成的后续清理坐标信息。
type SessionAnalysisApplyResult struct {
	ObsoleteVectorIDs []string
}
