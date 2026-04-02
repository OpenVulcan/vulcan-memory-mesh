// session_analysis.go declares the persisted session-turn records and internal multi-turn analysis carriers shared by queue workers, recall reads, and profile-review helpers.
// session_analysis.go 用于声明队列工作器、记忆读取与画像评审辅助逻辑共享的持久化 session turn 记录和内部多轮分析载体。
package domain

import "time"

// SessionTurnRecord stores one durable turn row loaded back from relational storage for pre-check windows, async extraction, and turn-detail reads.
// SessionTurnRecord 用于保存从关系库存重新加载的一条长期 turn 记录，供 pre-check 窗口、异步提炼和 turn 详情读取使用。
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

// SessionMemoryNodeRecord stores one active memory node row that single-turn extraction can reference when deciding what to supersede.
// SessionMemoryNodeRecord 用于保存一条活跃记忆节点记录，让单轮提炼在判断哪些旧记忆需要淘汰时可以引用它。
type SessionMemoryNodeRecord struct {
	ID              uint64
	TeamID          uint64
	SpaceID         uint64
	ProjectID       uint64
	UserID          uint64
	OriginSessionID uint64
	TurnID          uint64
	VectorID        string
	Category        int
	Abstract        string
	Details         string
	SourceKind      int
	ScopeLevel      int
	Priority        int
	MemoryLevel     int
	RefreshWeight   int
	SupportCount    int
	RebuttalCount   int
	NodeStatus      int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SessionBatchTurnAnalysis stores the extracted result for one turn inside an internal multi-turn review set.
// SessionBatchTurnAnalysis 用于保存内部多轮评审集合中某一条 turn 的提炼结果。
type SessionBatchTurnAnalysis struct {
	TurnID        uint64
	Details       string
	DetailsBudget int
	MemoryNodes   []MemoryNodeCandidate
	ProfileNodes  []ProfileNodeCandidate
}

// SessionBatchAnalysis stores the structured multi-turn result reused by profile-review helpers when they need to map accepted nodes back to per-turn coordinates.
// SessionBatchAnalysis 用于保存画像评审辅助逻辑复用的结构化多轮结果，便于把接受后的节点映射回逐 turn 坐标。
type SessionBatchAnalysis struct {
	Turns                 []SessionBatchTurnAnalysis
	ObsoleteMemoryTurnIDs []uint64
	RetiredProfileNodeIDs []uint64
	UserProfileMerged     bool
	MergedUserProfile     string
	ProjectProfileMerged  bool
	MergedProjectProfile  string
}
