// scratchpad.go declares the deterministic working-memory models used by the isolated session scratchpad chain.
// scratchpad.go 用于声明隔离式 session scratchpad 链路使用的确定性工作记忆模型。
package domain

import "time"

// ScratchpadStatus reports the final business outcome returned by the isolated scratchpad workflow.
// ScratchpadStatus 用于表示隔离 scratchpad 工作流返回给上层的最终业务结果。
type ScratchpadStatus int

const (
	// ScratchpadStatusUnspecified keeps zero-value structs explicit when one caller forgot to set the business outcome.
	// ScratchpadStatusUnspecified 用于让零值结构保持显式语义，避免调用方忘记设置业务结果时出现含糊状态。
	ScratchpadStatusUnspecified ScratchpadStatus = iota

	// ScratchpadStatusSuccess marks one scratchpad operation that completed deterministically and can be trusted by upstream callers.
	// ScratchpadStatusSuccess 用于标识一次已确定完成的 scratchpad 操作，上游调用方可以直接信任其结果。
	ScratchpadStatusSuccess

	// ScratchpadStatusFailed marks one scratchpad operation that was blocked or failed before a stable result could be persisted.
	// ScratchpadStatusFailed 用于标识一次在稳定结果落库前就被拦截或失败的 scratchpad 操作。
	ScratchpadStatusFailed
)

// ScratchpadScope carries the external deterministic coordinates that isolate one scratchpad workspace from the main memory/session chain.
// ScratchpadScope 用于承载隔离 scratchpad 工作区的外部确定性坐标，并与主记忆/主 session 链分离。
type ScratchpadScope struct {
	ProjectID  uint64
	UserID     uint64
	SessionKey string
}

// ScratchpadItem stores one AI-facing key/value anchor that should survive context compaction without entering the long-term memory system.
// ScratchpadItem 用于保存一条面向 AI 的 key/value 锚点，让它在上下文压缩后仍可恢复，而无需进入长期记忆系统。
type ScratchpadItem struct {
	Key   string
	Value string
}

// ScratchpadPlanRecord stores the unique canonical plan lock for one isolated scratchpad session scope.
// ScratchpadPlanRecord 用于保存某个隔离 scratchpad session 范围下唯一生效的计划锁。
type ScratchpadPlanRecord struct {
	ID           uint64
	ProjectID    uint64
	UserID       uint64
	SessionKey   string
	PlanName     string
	PlanNameNorm string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ScratchpadNodeRecord stores one concrete scratchpad key/value row under the canonical plan lock.
// ScratchpadNodeRecord 用于保存 canonical 计划锁下的一条具体 scratchpad key/value 行。
type ScratchpadNodeRecord struct {
	ID        uint64
	PlanID    uint64
	ItemKey   string
	ItemValue string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScratchpadMutationResult reports the business-visible result of one upsert/delete/clean operation, including canonical counters used by gRPC-facing hosts.
// ScratchpadMutationResult 用于表示一次 upsert/delete/clean 操作对业务层可见的最终结果，并包含 gRPC 宿主消费的 canonical 计数字段。
type ScratchpadMutationResult struct {
	Status        ScratchpadStatus
	Message       string
	PlanName      string
	UpdatedAt     time.Time
	AffectedCount int
	InsertedCount int
	UpdatedCount  int
}

// ScratchpadQueryResult reports one get operation together with canonical plan metadata and the ordered scratchpad items selected from the current scope.
// ScratchpadQueryResult 用于表示一次 get 操作的结果，并返回当前范围的 canonical 计划 metadata 与有序 scratchpad item 集合。
type ScratchpadQueryResult struct {
	Status    ScratchpadStatus
	Message   string
	PlanName  string
	UpdatedAt time.Time
	ItemCount int
	Items     []ScratchpadItem
}

// ScratchpadKeyListResult reports one list-keys operation together with canonical plan metadata and the ordered scratchpad key slice selected from the current scope.
// ScratchpadKeyListResult 用于表示一次 list-keys 操作的结果，并返回当前范围的 canonical 计划 metadata 与有序 scratchpad key 切片。
type ScratchpadKeyListResult struct {
	Status    ScratchpadStatus
	Message   string
	PlanName  string
	UpdatedAt time.Time
	KeyCount  int
	Keys      []string
}

// ScratchpadUpsertPersistResult reports how many concrete node rows were inserted or updated inside one deterministic scratchpad write pass.
// ScratchpadUpsertPersistResult 用于表示一次确定性 scratchpad 写入流程中插入和更新了多少具体节点行。
type ScratchpadUpsertPersistResult struct {
	InsertedCount int
	UpdatedCount  int
}

// ScratchpadDeletePersistResult reports how many concrete node rows were removed and how many remain under the current plan lock.
// ScratchpadDeletePersistResult 用于表示当前计划锁下删除了多少具体节点行，以及删除后还剩多少节点。
type ScratchpadDeletePersistResult struct {
	DeletedCount   int
	RemainingCount int
}

// ScratchpadCleanPersistResult reports whether one scope-level clean removed an existing plan lock and how many nodes were deleted with it.
// ScratchpadCleanPersistResult 用于表示一次 scope 级 clean 是否移除了现有计划锁，以及一并删除了多少节点。
type ScratchpadCleanPersistResult struct {
	HadPlan          bool
	DeletedPlanCount int
	DeletedNodeCount int
}

// ScratchpadGCResult reports how many expired scratchpad plans and nodes were permanently deleted by the background maintenance pass.
// ScratchpadGCResult 用于表示后台维护流程永久删除了多少过期 scratchpad 计划和节点。
type ScratchpadGCResult struct {
	DeletedPlanCount int
	DeletedNodeCount int
}
