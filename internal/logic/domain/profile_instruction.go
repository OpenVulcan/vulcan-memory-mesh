// profile_instruction.go declares the profile-query and manual-instruction models shared by gRPC handlers, use cases, processors, and SQLite-backed persistence.
// profile_instruction.go 用于声明 gRPC、用例层、处理器和 SQLite 持久化共享的画像查询与手工画像指令模型。
package domain

import "time"

const (
	// ProfileInstructionStatusPending marks one manual profile instruction that has been accepted and is waiting for reviewer output.
	// ProfileInstructionStatusPending 用于表示一条手工画像指令已被接收，但仍在等待评审结果。
	ProfileInstructionStatusPending = 0

	// ProfileInstructionStatusApplied marks one manual profile instruction whose reviewer result has already been persisted.
	// ProfileInstructionStatusApplied 用于表示一条手工画像指令的评审结果已经成功落库。
	ProfileInstructionStatusApplied = 1

	// ProfileInstructionStatusFailed marks one manual profile instruction whose reviewer or persistence stage failed.
	// ProfileInstructionStatusFailed 用于表示一条手工画像指令在评审或持久化阶段执行失败。
	ProfileInstructionStatusFailed = 2
)

// ProfileTargetRef stores one resolved target binding used by profile query and manual instruction flows.
// ProfileTargetRef 用于保存画像查询与手工画像指令流程使用的已解析目标绑定信息。
type ProfileTargetRef struct {
	ProfileType int
	BindID      uint64
	UserID      uint64
	TeamID      uint64
	SpaceID     uint64
	ProjectID   uint64
	UserName    string
	TeamName    string
	SpaceName   string
	ProjectName string
}

// ProfileNodeRecord stores one durable profile node row returned by query APIs and manual instruction writebacks.
// ProfileNodeRecord 用于保存一条长期画像节点记录，并返回给查询接口与手工画像写回流程。
type ProfileNodeRecord struct {
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

// ProfileInstructionRecord stores one explicit manual profile instruction before or after review persistence.
// ProfileInstructionRecord 用于保存一条显式手工画像指令在评审前后对应的长期记录。
type ProfileInstructionRecord struct {
	ID            uint64
	ProfileType   int
	BindID        uint64
	Instruction   string
	Status        int
	ReviewResult  string
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ProfileRetireDecision stores one node-id plus the reason why it should leave the active profile set.
// ProfileRetireDecision 用于保存一条待退役节点及其退出活跃画像集合的原因。
type ProfileRetireDecision struct {
	NodeID uint64
	Reason string
}

// ManualProfileAcceptedNode stores one new node accepted from a manual instruction together with its lifecycle metadata and supersede targets.
// ManualProfileAcceptedNode 用于保存一条从手工画像指令中接纳的新节点，并携带其生命周期元数据和替代目标。
type ManualProfileAcceptedNode struct {
	NormalizedContent string
	Priority          int
	ProfileLevel      int
	LevelReason       string
	SupersedeNodes    []ProfileRetireDecision
}

// ManualProfileInstructionReview stores the structured LLM review result for one explicit manual profile instruction.
// ManualProfileInstructionReview 用于保存单次显式手工画像指令的结构化 LLM 评审结果。
type ManualProfileInstructionReview struct {
	AcceptedNodes []ManualProfileAcceptedNode
	RetiredNodes  []ProfileRetireDecision
	Reason        string
	// LLMExecution retains the physical review response identity outside durable review JSON.
	// LLMExecution 用于在持久化评审 JSON 之外保留物理评审响应身份。
	LLMExecution *LLMExecutionMetadata `json:"-"`
}

// ManualProfileInstructionApplyResult stores the final nodes and retire decisions that were persisted after one manual profile instruction succeeded.
// ManualProfileInstructionApplyResult 用于保存单次手工画像指令成功后真正落库的新节点和退役节点结果。
type ManualProfileInstructionApplyResult struct {
	InstructionID uint64
	AcceptedNodes []ProfileNodeRecord
	RetiredNodes  []ProfileRetireDecision
}

// RenderedProfileSet stores the rendered profile blobs grouped by scope type so callers can update durable profile columns in one call.
// RenderedProfileSet 用于按 scope 类型分组保存渲染后的 profile Blob，让调用方能在一次写入中更新长期 profile 列。
type RenderedProfileSet struct {
	UserProfiles    map[uint64]string
	TeamProfiles    map[uint64]string
	SpaceProfiles   map[uint64]string
	ProjectProfiles map[uint64]string
}

// ValidProfileInstructionStatus reports whether one instruction status belongs to the supported manual-instruction lifecycle enum set.
// ValidProfileInstructionStatus 用于判断某个画像指令状态是否属于当前支持的手工指令生命周期枚举集合。
func ValidProfileInstructionStatus(status int) bool {
	return status >= ProfileInstructionStatusPending && status <= ProfileInstructionStatusFailed
}
