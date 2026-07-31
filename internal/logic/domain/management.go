// management.go declares storage-neutral records used by the isolated human-management API.
// management.go 用于声明独立人工管理 API 使用的存储无关记录。
package domain

import "time"

const (
	// ManagementTargetSession identifies one human-managed session target.
	// ManagementTargetSession 用于标识一个由人工管理的会话目标。
	ManagementTargetSession = "session"
	// ManagementTargetTurn identifies one human-managed turn target.
	// ManagementTargetTurn 用于标识一个由人工管理的回合目标。
	ManagementTargetTurn = "turn"
	// ManagementTargetRecycleBatch identifies one recycle-batch target.
	// ManagementTargetRecycleBatch 用于标识一个回收批次目标。
	ManagementTargetRecycleBatch = "recycle_batch"
	// ManagementActionArchive hides sessions without removing derived data.
	// ManagementActionArchive 用于隐藏会话，但不移除派生数据。
	ManagementActionArchive = "archive"
	// ManagementActionUnarchive restores archived sessions to normal visibility.
	// ManagementActionUnarchive 用于把已归档会话恢复为正常可见状态。
	ManagementActionUnarchive = "unarchive"
	// ManagementActionRecycle moves selected data into a restorable manual batch.
	// ManagementActionRecycle 用于把所选数据移入可恢复的人工回收批次。
	ManagementActionRecycle = "recycle"
	// ManagementActionRestore restores one restorable manual recycle batch.
	// ManagementActionRestore 用于恢复一个可恢复的人工回收批次。
	ManagementActionRestore = "restore"
	// ManagementActionPurge permanently removes one recycle batch.
	// ManagementActionPurge 用于永久清理一个回收批次。
	ManagementActionPurge = "purge"
	// ManagementActionRestart explicitly re-enables one forgotten session without restoring purged content.
	// ManagementActionRestart 用于在不恢复已清理内容的前提下显式重新启用遗忘会话。
	ManagementActionRestart = "restart"
)

// ManagementCursor identifies the last stable row observed by one deterministic cursor page.
// ManagementCursor 用于标识确定性游标分页中上一页最后一条稳定记录。
type ManagementCursor struct {
	UpdatedAt time.Time
	ID        uint64
}

// ManagementSessionQuery contains normalized filters for one session-management page.
// ManagementSessionQuery 用于保存单次会话管理分页使用的规范化筛选条件。
type ManagementSessionQuery struct {
	UserID      uint64
	ProjectID   uint64
	Query       string
	Status      string
	CreatedFrom time.Time
	CreatedTo   time.Time
	UpdatedFrom time.Time
	UpdatedTo   time.Time
	Cursor      ManagementCursor
	Limit       int
	Sort        string
}

// ManagementSessionRecord is the relational projection needed to render one session summary or detail.
// ManagementSessionRecord 是渲染单条会话摘要或详情所需的关系存储投影。
type ManagementSessionRecord struct {
	SessionRef
	FirstTurnContent      string
	VisibleTurnCount      int
	PendingTurnCount      int
	ActiveMemoryCount     int
	ProfileReferenceCount int
	Status                string
}

// ManagementSessionPage returns one stable session page plus a possible continuation cursor.
// ManagementSessionPage 用于返回一页稳定会话以及可选的后续游标。
type ManagementSessionPage struct {
	Items      []ManagementSessionRecord
	NextCursor ManagementCursor
	HasMore    bool
}

// ManagementTurnQuery contains normalized filters for one session-scoped turn page.
// ManagementTurnQuery 用于保存单次会话范围回合分页使用的规范化筛选条件。
type ManagementTurnQuery struct {
	SessionID   uint64
	UserID      uint64
	ProjectID   uint64
	Query       string
	Status      string
	CreatedFrom time.Time
	CreatedTo   time.Time
	HasMemory   *bool
	HasProfile  *bool
	CursorID    uint64
	Limit       int
	Sort        string
}

// ManagementTurnRecord joins one durable turn with human-readable scope and derived-impact counts.
// ManagementTurnRecord 用于把一条持久化回合与人类可读作用域及派生影响计数联结起来。
type ManagementTurnRecord struct {
	SessionTurnRecord
	UserID            uint64
	UserName          string
	ProjectName       string
	TeamName          string
	SpaceName         string
	DerivedMemories   int
	ProfileReferences int
}

// ManagementTurnPage returns one stable turn page plus a possible continuation identifier.
// ManagementTurnPage 用于返回一页稳定回合以及可选的后续标识。
type ManagementTurnPage struct {
	Items        []ManagementTurnRecord
	NextCursorID uint64
	HasMore      bool
}

// ManagementRemovalSelection identifies one normalized target set and requested action.
// ManagementRemovalSelection 用于标识一组规范化目标及请求执行的动作。
type ManagementRemovalSelection struct {
	TargetType string
	TargetIDs  []uint64
	Action     string
	Source     string
}

// MatchesOperation reports whether an operation is bound to this exact mutation target and action.
// MatchesOperation 用于判断一条操作是否绑定到当前完全一致的变更目标与动作。
func (selection ManagementRemovalSelection) MatchesOperation(operation ManagementOperation) bool {
	if operation.Action != selection.Action || operation.TargetType != selection.TargetType || len(operation.TargetIDs) != len(selection.TargetIDs) {
		return false
	}
	for index := range operation.TargetIDs {
		if operation.TargetIDs[index] != selection.TargetIDs[index] {
			return false
		}
	}
	return true
}

// ManagementImpact summarizes every directly affected relational and vector-backed resource.
// ManagementImpact 用于汇总所有直接受影响的关系数据与向量资源。
type ManagementImpact struct {
	SessionCount       int
	TurnCount          int
	MemoryCount        int
	ContextEdgeCount   int
	VectorCount        int
	ProfileNodeCount   int
	PendingTurnCount   int
	HotWindowTurnCount int
	ProtectedCount     int
	Revision           string
}

// ManagementPreview binds a short-lived token to one exact target set and impact revision.
// ManagementPreview 用于把短期预览令牌绑定到精确目标集合和影响修订。
type ManagementPreview struct {
	Token       string
	Selection   ManagementRemovalSelection
	Impact      ManagementImpact
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConsumedAt  time.Time
	ConfirmText string
}

// ManagementOperation records one idempotent management mutation and its terminal outcome.
// ManagementOperation 用于记录一次幂等管理变更及其终态结果。
type ManagementOperation struct {
	ID             string
	IdempotencyKey string
	Action         string
	TargetType     string
	TargetIDs      []uint64
	Status         string
	BatchID        uint64
	ErrorCode      string
	ErrorMessage   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    time.Time
}

// ManagementRecycleBatch describes one system or manually created recycle batch.
// ManagementRecycleBatch 用于描述一个系统或人工创建的回收批次。
type ManagementRecycleBatch struct {
	BatchID      uint64
	Source       string
	TargetType   string
	TargetIDs    []uint64
	Restorable   bool
	State        string
	Reason       string
	SessionCount int
	TurnCount    int
	MemoryCount  int
	ProfileCount int
	RecycledAt   time.Time
	ExpiresAt    time.Time
	RestoredAt   time.Time
	PurgedAt     time.Time
}

// ManagementRecycleBatchPage returns one bounded recycle-batch page.
// ManagementRecycleBatchPage 用于返回一页有界回收批次。
type ManagementRecycleBatchPage struct {
	Items        []ManagementRecycleBatch
	NextCursorID uint64
	HasMore      bool
}

// ManagementMutationCommand carries the server-validated preview and idempotency coordinates.
// ManagementMutationCommand 用于携带服务端已校验的预览及幂等坐标。
type ManagementMutationCommand struct {
	OperationID    string
	IdempotencyKey string
	Preview        ManagementPreview
	ConfirmText    string
	Now            time.Time
	ExpiresAt      time.Time
}

// ManagementRestoreCommand carries one explicit restorable batch request.
// ManagementRestoreCommand 用于携带一个明确的可恢复批次请求。
type ManagementRestoreCommand struct {
	OperationID    string
	IdempotencyKey string
	BatchID        uint64
	Now            time.Time
}
