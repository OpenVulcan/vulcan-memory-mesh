// turn_analysis.go declares the per-turn extraction models shared by the post-action workflow, LLM processors, and DuckDB persistence.
// turn_analysis.go 用于声明 post-action 工作流、LLM 处理器和 DuckDB 持久化共享的逐轮提炼模型。
package domain

import "time"

const (
	// TurnExtractedStatusPending marks one turn row that has not completed feature extraction yet.
	// TurnExtractedStatusPending 用于标记一条尚未完成特征提取的 turn 记录。
	TurnExtractedStatusPending = 0

	// TurnExtractedStatusDone marks one turn row whose details and extracted nodes have already been persisted.
	// TurnExtractedStatusDone 用于标记一条 details 与提炼节点都已经落库的 turn 记录。
	TurnExtractedStatusDone = 1
)

const (
	// MemoryNodeCategoryGeneral stores background facts that do not fit a more specific class.
	// MemoryNodeCategoryGeneral 用于表示无法归入更具体分类的背景事实。
	MemoryNodeCategoryGeneral = 0

	// MemoryNodeCategoryArchitectureDecision stores architecture decisions together with their rationale.
	// MemoryNodeCategoryArchitectureDecision 用于表示架构决策及其原因。
	MemoryNodeCategoryArchitectureDecision = 1

	// MemoryNodeCategoryTechSpecAPI stores API, library, and technical specification usage details.
	// MemoryNodeCategoryTechSpecAPI 用于表示接口、库定义以及技术规格用法。
	MemoryNodeCategoryTechSpecAPI = 2

	// MemoryNodeCategoryBusinessLogic stores durable business rules, formulas, and logic constraints.
	// MemoryNodeCategoryBusinessLogic 用于表示稳定的业务规则、公式和逻辑约束。
	MemoryNodeCategoryBusinessLogic = 3

	// MemoryNodeCategoryRequirementTODO stores pending work, requirements, and unfinished commitments.
	// MemoryNodeCategoryRequirementTODO 用于表示待办、需求和未完成事项。
	MemoryNodeCategoryRequirementTODO = 4

	// MemoryNodeCategoryProjectContext stores environment, module relationship, and deployment context.
	// MemoryNodeCategoryProjectContext 用于表示环境、模块关系和部署上下文。
	MemoryNodeCategoryProjectContext = 5

	// MemoryNodeCategoryLogicalBugDebt stores known defects, pitfalls, and technical debt.
	// MemoryNodeCategoryLogicalBugDebt 用于表示已知缺陷、坑点和技术债。
	MemoryNodeCategoryLogicalBugDebt = 6

	// MemoryNodeCategorySecurityPolicy stores security boundaries, sensitive handling rules, and policy constraints.
	// MemoryNodeCategorySecurityPolicy 用于表示安全边界、敏感处理规则和策略约束。
	MemoryNodeCategorySecurityPolicy = 7
)

const (
	// MemoryNodeStatusActive keeps one extracted memory node active for future recall and merge decisions.
	// MemoryNodeStatusActive 用于表示一条提炼记忆节点当前仍处于活跃生效状态。
	MemoryNodeStatusActive = 0

	// MemoryNodeStatusSuperseded marks one node that has been covered by a newer consolidated memory.
	// MemoryNodeStatusSuperseded 用于表示一条节点已经被更新的记忆覆盖。
	MemoryNodeStatusSuperseded = 1

	// MemoryNodeStatusDeleted marks one node that has been judged invalid and no longer usable.
	// MemoryNodeStatusDeleted 用于表示一条节点已被判定失效，不再使用。
	MemoryNodeStatusDeleted = 2
)

const (
	// ProfileTypeUser stores stable user-side preferences, habits, and long-lived traits.
	// ProfileTypeUser 用于表示用户侧的稳定偏好、习惯和长期特征。
	ProfileTypeUser = 0

	// ProfileTypeProject stores stable project-side constraints, stack choices, and working conventions.
	// ProfileTypeProject 用于表示项目侧的稳定约束、技术选型和工作约定。
	ProfileTypeProject = 1
)

const (
	// ProfileStatusInvalid marks profile evidence that was judged not worth merging.
	// ProfileStatusInvalid 用于表示一条画像证据已被判定为无效。
	ProfileStatusInvalid = 0

	// ProfileStatusPending marks profile evidence waiting for a future merge into the profile blob.
	// ProfileStatusPending 用于表示一条画像证据等待后续合并进画像 Blob。
	ProfileStatusPending = 1

	// ProfileStatusMerged marks profile evidence that has already been merged into the profile blob.
	// ProfileStatusMerged 用于表示一条画像证据已经合并进画像 Blob。
	ProfileStatusMerged = 2
)

// PersistedTurnRecord stores the durable identifiers returned right after one cleaned turn is appended into DuckDB.
// PersistedTurnRecord 用于保存一条清洗后 turn 写入 DuckDB 后立即返回的持久化标识。
type PersistedTurnRecord struct {
	ID               uint64
	SessionID        uint64
	ProjectID        uint64
	DehydratedBudget int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TurnAnalysis carries the structured LLM extraction output that should be written back onto one turn row and its derived node tables.
// TurnAnalysis 用于承载结构化 LLM 提炼结果，并回写到 turn 行及其衍生节点表。
type TurnAnalysis struct {
	Details       string
	DetailsBudget int
	MemoryNodes   []MemoryNodeCandidate
	ProfileNodes  []ProfileNodeCandidate
}

// MemoryNodeCandidate stores one memory feature extracted from a turn before it is assigned ids and persisted.
// MemoryNodeCandidate 用于保存一条从 turn 中提炼出的记忆特征，等待分配 ID 后持久化。
type MemoryNodeCandidate struct {
	Category int
	VectorID string
	Abstract string
	Details  string
}

// ProfileNodeCandidate stores one profile feature extracted from a turn before it is bound to a user or project row.
// ProfileNodeCandidate 用于保存一条从 turn 中提炼出的画像特征，等待绑定到用户或项目。
type ProfileNodeCandidate struct {
	ProfileType int
	Content     string
}

// ValidMemoryNodeCategory reports whether one category id belongs to the supported memory-node enum set.
// ValidMemoryNodeCategory 用于判断某个分类 ID 是否属于当前支持的记忆节点枚举集合。
func ValidMemoryNodeCategory(category int) bool {
	return category >= MemoryNodeCategoryGeneral && category <= MemoryNodeCategorySecurityPolicy
}

// ValidProfileType reports whether one profile type id belongs to the supported profile-node enum set.
// ValidProfileType 用于判断某个画像类型 ID 是否属于当前支持的画像节点枚举集合。
func ValidProfileType(profileType int) bool {
	return profileType == ProfileTypeUser || profileType == ProfileTypeProject
}
