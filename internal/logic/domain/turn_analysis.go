// turn_analysis.go declares the per-turn extraction models shared by the post-action workflow, LLM processors, and SQLite-backed persistence.
// turn_analysis.go 用于声明 post-action 工作流、LLM 处理器和 SQLite 持久化共享的逐轮提炼模型。
package domain

import "time"

const (
	// MemoryContextRelationSupport marks one contextual edge as evidence that supports the memory inside the given situation.
	// MemoryContextRelationSupport 用于标记某条情境边在给定场景下对该记忆形成支持证据。
	MemoryContextRelationSupport = "support"

	// MemoryContextRelationRebuttal marks one contextual edge as evidence that contradicts or weakens the memory inside the given situation.
	// MemoryContextRelationRebuttal 用于标记某条情境边在给定场景下对该记忆形成反驳或削弱证据。
	MemoryContextRelationRebuttal = "rebuttal"
)

const (
	// TurnAnalysisUserInputQuestion marks one turn whose dominant user act is asking a question instead of stating a durable fact directly.
	// TurnAnalysisUserInputQuestion 用于标记一条以提问为主的用户输入，而不是直接陈述可长期保存事实的轮次。
	TurnAnalysisUserInputQuestion = "question"

	// TurnAnalysisUserInputStatement marks one turn whose dominant user act is making or confirming a factual statement that may deserve long-term storage.
	// TurnAnalysisUserInputStatement 用于标记一条以事实陈述或确认事实为主的用户输入，它可能值得长期保存。
	TurnAnalysisUserInputStatement = "statement"

	// TurnAnalysisUserInputMixed marks one turn that mixes questions, instructions, confirmations, or factual statements together.
	// TurnAnalysisUserInputMixed 用于标记一条同时混合提问、指令、确认或事实陈述的输入轮次。
	TurnAnalysisUserInputMixed = "mixed"
)

const (
	// TurnAnalysisEvidenceSourceUserAsserted marks a candidate that comes directly from a user-side factual statement in the current turn.
	// TurnAnalysisEvidenceSourceUserAsserted 用于标记一个直接来自当前轮用户事实陈述的候选。
	TurnAnalysisEvidenceSourceUserAsserted = "user_asserted"

	// TurnAnalysisEvidenceSourceUserConfirmed marks a candidate that becomes durable because the user explicitly confirmed or corrected it in the current turn.
	// TurnAnalysisEvidenceSourceUserConfirmed 用于标记一个因用户在当前轮明确确认或纠正而变得可长期保存的候选。
	TurnAnalysisEvidenceSourceUserConfirmed = "user_confirmed"

	// TurnAnalysisEvidenceSourceAssistantRecalledMemory marks a candidate that only rephrases memory already available to the assistant before this turn.
	// TurnAnalysisEvidenceSourceAssistantRecalledMemory 用于标记一个仅仅复述助手在本轮前已掌握记忆的候选。
	TurnAnalysisEvidenceSourceAssistantRecalledMemory = "assistant_recalled_memory"

	// TurnAnalysisEvidenceSourceAssistantRecalledProfile marks a candidate that only echoes user or project profile information already available to the assistant.
	// TurnAnalysisEvidenceSourceAssistantRecalledProfile 用于标记一个仅仅回显助手已掌握的用户或项目画像信息的候选。
	TurnAnalysisEvidenceSourceAssistantRecalledProfile = "assistant_recalled_profile"

	// TurnAnalysisEvidenceSourceAssistantGeneralKnowledge marks a candidate derived from the model's own general knowledge instead of durable user or project facts.
	// TurnAnalysisEvidenceSourceAssistantGeneralKnowledge 用于标记一个来自模型通用知识，而不是长期用户/项目事实的候选。
	TurnAnalysisEvidenceSourceAssistantGeneralKnowledge = "assistant_general_knowledge"

	// TurnAnalysisEvidenceSourceAssistantExternalResearch marks a candidate produced after costly external research such as website lookup, document search, or multi-source synthesis.
	// TurnAnalysisEvidenceSourceAssistantExternalResearch 用于标记一个通过网站查询、文档检索或多源归纳等高成本外部研究得到的候选。
	TurnAnalysisEvidenceSourceAssistantExternalResearch = "assistant_external_research"

	// TurnAnalysisEvidenceSourceAssistantToolDiscovered marks a candidate learned from a tool call or structured system lookup that surfaced new durable information.
	// TurnAnalysisEvidenceSourceAssistantToolDiscovered 用于标记一个通过工具调用或结构化系统查询发现的新长期信息候选。
	TurnAnalysisEvidenceSourceAssistantToolDiscovered = "assistant_tool_discovered"

	// TurnAnalysisEvidenceSourceMixed marks a candidate whose evidence blends multiple sources and therefore cannot be reduced to one single origin bucket safely.
	// TurnAnalysisEvidenceSourceMixed 用于标记一个混合了多种证据来源、无法安全归入单一来源桶的候选。
	TurnAnalysisEvidenceSourceMixed = "mixed"
)

const (
	// TurnAnalysisAdmissionKeep marks a candidate that the first-pass analyzer believes may proceed into later review and persistence checks.
	// TurnAnalysisAdmissionKeep 用于标记一条在首轮分析后仍可进入后续评审与持久化检查的候选。
	TurnAnalysisAdmissionKeep = "keep"

	// TurnAnalysisAdmissionDrop marks a candidate that the first-pass analyzer believes should be rejected before later review or persistence happen.
	// TurnAnalysisAdmissionDrop 用于标记一条在首轮分析后就应被拒绝、无需继续进入后续评审与持久化的候选。
	TurnAnalysisAdmissionDrop = "drop"
)

const (
	// TurnAnalysisAdmissionReasonQAAnswerOnly marks a candidate rejected because it is only one answer-shaped echo to a user question.
	// TurnAnalysisAdmissionReasonQAAnswerOnly 用于标记一条因只是“对用户提问的回显式回答”而被拒绝的候选。
	TurnAnalysisAdmissionReasonQAAnswerOnly = "qa_answer_only"

	// TurnAnalysisAdmissionReasonDerivedFromExistingMemory marks a candidate rejected because it only reformulates memory that was already available before the current turn.
	// TurnAnalysisAdmissionReasonDerivedFromExistingMemory 用于标记一条因只是重述既有记忆而被拒绝的候选。
	TurnAnalysisAdmissionReasonDerivedFromExistingMemory = "derived_from_existing_memory"

	// TurnAnalysisAdmissionReasonDerivedFromProfileEcho marks a candidate rejected because it only echoes user or project profile content.
	// TurnAnalysisAdmissionReasonDerivedFromProfileEcho 用于标记一条因只是回显用户或项目画像内容而被拒绝的候选。
	TurnAnalysisAdmissionReasonDerivedFromProfileEcho = "derived_from_profile_echo"

	// TurnAnalysisAdmissionReasonGeneralKnowledgeAnswer marks a candidate rejected because it only comes from the assistant's own general knowledge answer.
	// TurnAnalysisAdmissionReasonGeneralKnowledgeAnswer 用于标记一条因只是助手通用知识回答而被拒绝的候选。
	TurnAnalysisAdmissionReasonGeneralKnowledgeAnswer = "general_knowledge_answer"

	// TurnAnalysisAdmissionReasonNonDurable marks a candidate rejected because it is too ephemeral to deserve long-term storage even if it required external lookup.
	// TurnAnalysisAdmissionReasonNonDurable 用于标记一条因缺乏长期价值、即使经过外部检索也不应长期保存的候选。
	TurnAnalysisAdmissionReasonNonDurable = "non_durable"
)

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

	// ProfileTypeTeam stores team-scoped conventions and defaults that should outlive one isolated project.
	// ProfileTypeTeam 用于表示 team 级的长期约定和默认规则，它们应当跨越单个项目继续生效。
	ProfileTypeTeam = 2

	// ProfileTypeSpace stores space-scoped constraints and conventions shared by the projects under the same space.
	// ProfileTypeSpace 用于表示 space 级共享的约束和工作约定，作用于同一 space 下的多个项目。
	ProfileTypeSpace = 3
)

const (
	// ProfileStatusInvalid marks profile evidence that was judged not worth merging.
	// ProfileStatusInvalid 用于表示一条画像证据已被判定为无效。
	ProfileStatusInvalid = 0

	// ProfileStatusPending marks profile evidence waiting for one reviewer decision or a later retry.
	// ProfileStatusPending 用于表示一条画像证据正在等待评审决定或后续重试。
	ProfileStatusPending = 1

	// ProfileStatusActive marks one profile node that is still considered valid and should participate in profile rendering.
	// ProfileStatusActive 用于表示一条画像节点当前仍然有效，并应参与画像渲染。
	ProfileStatusActive = 2

	// ProfileStatusSuperseded marks one profile node that has been replaced by a fresher node.
	// ProfileStatusSuperseded 用于表示一条画像节点已经被更新的节点替代。
	ProfileStatusSuperseded = 3

	// ProfileStatusExpired marks one profile node that has naturally aged out of the active profile window.
	// ProfileStatusExpired 用于表示一条画像节点已因生命周期到期而失效。
	ProfileStatusExpired = 4
)

const (
	// ProfileStatusMerged keeps backward compatibility with older code paths that still refer to the previous "merged" name.
	// ProfileStatusMerged 用于兼容仍然沿用旧“merged”命名的代码路径，它等价于当前的 active 状态。
	ProfileStatusMerged = ProfileStatusActive
)

const (
	// ProfileSourceKindTurnExtract marks nodes extracted from post-action turn analysis.
	// ProfileSourceKindTurnExtract 用于表示画像节点来源于 post-action 的 turn 提炼流程。
	ProfileSourceKindTurnExtract = 0

	// ProfileSourceKindManualInstruction marks nodes created from one explicit manual profile instruction.
	// ProfileSourceKindManualInstruction 用于表示画像节点来源于一次显式的手工画像指令。
	ProfileSourceKindManualInstruction = 1

	// ProfileSourceKindSystemSeed marks nodes inserted by deterministic system seeding or future admin imports.
	// ProfileSourceKindSystemSeed 用于表示画像节点来源于系统种子数据或未来的管理导入流程。
	ProfileSourceKindSystemSeed = 2

	// ProfileSourceKindRetainedAfterUserDelete marks shared scope nodes whose original turn source disappeared
	// because the source user was deleted, while the shared profile fact itself still had to survive.
	// ProfileSourceKindRetainedAfterUserDelete 用于表示一条共享范围画像节点在源用户被删除后仍需保留，
	// 因而脱离了原始 turn 来源。
	ProfileSourceKindRetainedAfterUserDelete = 3
)

const (
	// ProfilePriorityP0 marks a non-negotiable rule or hard boundary that should be surfaced before any softer preference.
	// ProfilePriorityP0 用于表示硬约束或不可协商边界，应优先于较软的偏好展示。
	ProfilePriorityP0 = 0

	// ProfilePriorityP1 marks an important preference or working rule that should remain highly visible but can still be superseded later.
	// ProfilePriorityP1 用于表示重要偏好或工作规则，需保持较高可见度，但后续仍可能被更新。
	ProfilePriorityP1 = 1

	// ProfilePriorityP2 marks a lower-priority reference item that is still useful but not foundational.
	// ProfilePriorityP2 用于表示较低优先级的参考信息，仍有价值，但不属于基础约束。
	ProfilePriorityP2 = 2
)

const (
	// ProfileLevelTransient marks a short-lived conversational context that should expire quickly unless refreshed.
	// ProfileLevelTransient 用于表示短时会话上下文，除非被刷新，否则应快速过期。
	ProfileLevelTransient = 0

	// ProfileLevelSituational marks a phase-specific preference or working assumption that may remain valid for one bounded period.
	// ProfileLevelSituational 用于表示阶段性偏好或工作假设，通常只在一段有限时期内有效。
	ProfileLevelSituational = 1

	// ProfileLevelStable marks a long-lived preference or habit that should outlast one isolated discussion.
	// ProfileLevelStable 用于表示稳定偏好或习惯，应当跨越单次讨论继续生效。
	ProfileLevelStable = 2

	// ProfileLevelPersistent marks a durable rule, identity trait, or hard requirement that should rarely expire automatically.
	// ProfileLevelPersistent 用于表示长期规则、身份特征或强约束，通常不应自动过期。
	ProfileLevelPersistent = 3
)

// PersistedTurnRecord stores the durable identifiers returned right after one cleaned turn is appended into SQLite.
// PersistedTurnRecord 用于保存一条清洗后 turn 写入 SQLite 后立即返回的持久化标识。
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
	UserInputKind        string
	TurnID               uint64
	Details              string
	DetailsBudget        int
	MemoryNodes          []MemoryNodeCandidate
	ProfileNodes         []ProfileNodeCandidate
	UserProfileMerged    bool
	MergedUserProfile    string
	ProjectProfileMerged bool
	MergedProjectProfile string
}

// TurnAnalysisReferenceTurn stores one already-refined historical turn summary that can help the single-turn analyzer understand the immediate context without re-extracting the old turn itself.
// TurnAnalysisReferenceTurn 用于保存一条已经提炼完成的历史 turn 精要，让单轮分析器理解紧邻上下文，但不能把旧 turn 重新当作本轮提炼对象。
type TurnAnalysisReferenceTurn struct {
	TurnID  uint64
	Details string
}

// TurnAnalysisTargetTurn stores the only raw turn that the single-turn analyzer is allowed to extract into fresh details, memory nodes, and profile nodes.
// TurnAnalysisTargetTurn 用于保存单轮分析器唯一允许重新提炼的目标 turn 原文，让输出始终只绑定这一轮。
type TurnAnalysisTargetTurn struct {
	TurnID  uint64
	RawTurn string
}

// TurnAnalysisDirectWrite stores one recently accepted direct-write memory so the single-turn analyzer can avoid extracting facts that the tool path has already persisted.
// TurnAnalysisDirectWrite 用于保存一条最近已经采纳的主动写入记忆，让单轮分析器避免把工具链路已经入库的事实再次重复提炼。
type TurnAnalysisDirectWrite struct {
	MemoryID         uint64
	ScopeLevel       string
	Abstract         string
	Details          string
	CreatedTimestamp int64
}

// TurnAnalysisInput bundles the contextual inputs required by the reference-aware single-turn analyzer.
// TurnAnalysisInput 用于打包参考感知型单轮分析器所需的上下文输入。
type TurnAnalysisInput struct {
	ReferenceTurns         []TurnAnalysisReferenceTurn
	TargetTurn             TurnAnalysisTargetTurn
	RecentGRPCMemoryWrites []TurnAnalysisDirectWrite
}

// MemoryNodeCandidate stores one memory feature extracted from a turn before it is assigned ids and persisted.
// MemoryNodeCandidate 用于保存一条从 turn 中提炼出的记忆特征，等待分配 ID 后持久化。
type MemoryNodeCandidate struct {
	Category           int
	VectorID           string
	Vector             []float32
	Abstract           string
	Details            string
	EvidenceSource     string
	Admission          string
	AdmissionReason    string
	SupersedeMemoryIDs []uint64
	ContextEdges       []MemoryContextEdgeCandidate
	SourceKind         int
	ScopeLevel         int
	Priority           int
	MemoryLevel        int
	RefreshWeight      int
	ExpiresAt          time.Time
	DedupeHash         string
}

// MemoryContextEdgeCandidate stores one extracted situational label attached to a new memory candidate before it is aggregated into durable edge counters.
// MemoryContextEdgeCandidate 用于保存附着在新记忆候选上的一条提取情境标签，后续会被聚合成长期边和统计计数。
type MemoryContextEdgeCandidate struct {
	ContextKey   string
	ContextValue string
	Relation     string
}

// ProfileNodeCandidate stores one profile feature extracted from a turn before it is bound to a user or project row.
// ProfileNodeCandidate 用于保存一条从 turn 中提炼出的画像特征，等待绑定到用户或项目。
type ProfileNodeCandidate struct {
	ProfileType      int
	Content          string
	EvidenceSource   string
	Admission        string
	AdmissionReason  string
	Status           int
	Priority         int
	ProfileLevel     int
	LevelReason      string
	RefreshWeight    int
	ProfileDate      string
	SourceTurnID     uint64
	SourceKind       int
	SourceID         uint64
	StatusReason     string
	ExpiresAt        time.Time
	SupersedeNodeIDs []uint64
}

// ValidMemoryNodeCategory reports whether one category id belongs to the supported memory-node enum set.
// ValidMemoryNodeCategory 用于判断某个分类 ID 是否属于当前支持的记忆节点枚举集合。
func ValidMemoryNodeCategory(category int) bool {
	return category >= MemoryNodeCategoryGeneral && category <= MemoryNodeCategorySecurityPolicy
}

// ValidTurnAnalysisUserInputKind reports whether one analyzer-classified user-input kind belongs to the supported enum-like set.
// ValidTurnAnalysisUserInputKind 用于判断某个分析器给出的用户输入类型是否属于当前支持的枚举集合。
func ValidTurnAnalysisUserInputKind(kind string) bool {
	switch kind {
	case TurnAnalysisUserInputQuestion, TurnAnalysisUserInputStatement, TurnAnalysisUserInputMixed:
		return true
	default:
		return false
	}
}

// ValidTurnAnalysisEvidenceSource reports whether one analyzer evidence-source label belongs to the supported set.
// ValidTurnAnalysisEvidenceSource 用于判断某个分析器证据来源标签是否属于当前支持的集合。
func ValidTurnAnalysisEvidenceSource(source string) bool {
	switch source {
	case TurnAnalysisEvidenceSourceUserAsserted,
		TurnAnalysisEvidenceSourceUserConfirmed,
		TurnAnalysisEvidenceSourceAssistantRecalledMemory,
		TurnAnalysisEvidenceSourceAssistantRecalledProfile,
		TurnAnalysisEvidenceSourceAssistantGeneralKnowledge,
		TurnAnalysisEvidenceSourceAssistantExternalResearch,
		TurnAnalysisEvidenceSourceAssistantToolDiscovered,
		TurnAnalysisEvidenceSourceMixed:
		return true
	default:
		return false
	}
}

// ValidTurnAnalysisAdmission reports whether one analyzer admission decision belongs to the supported keep/drop set.
// ValidTurnAnalysisAdmission 用于判断某个分析器准入决策是否属于当前支持的 keep/drop 集合。
func ValidTurnAnalysisAdmission(admission string) bool {
	switch admission {
	case TurnAnalysisAdmissionKeep, TurnAnalysisAdmissionDrop:
		return true
	default:
		return false
	}
}

// ValidTurnAnalysisAdmissionReason reports whether one analyzer rejection reason belongs to the supported first-pass rejection set.
// ValidTurnAnalysisAdmissionReason 用于判断某个分析器拒绝原因是否属于当前支持的首轮拒绝原因集合。
func ValidTurnAnalysisAdmissionReason(reason string) bool {
	switch reason {
	case TurnAnalysisAdmissionReasonQAAnswerOnly,
		TurnAnalysisAdmissionReasonDerivedFromExistingMemory,
		TurnAnalysisAdmissionReasonDerivedFromProfileEcho,
		TurnAnalysisAdmissionReasonGeneralKnowledgeAnswer,
		TurnAnalysisAdmissionReasonNonDurable:
		return true
	default:
		return false
	}
}

// ValidProfileType reports whether one profile type id belongs to the supported profile-node enum set.
// ValidProfileType 用于判断某个画像类型 ID 是否属于当前支持的画像节点枚举集合。
func ValidProfileType(profileType int) bool {
	return profileType >= ProfileTypeUser && profileType <= ProfileTypeSpace
}

// ValidProfileStatus reports whether one profile status id belongs to the supported profile-node status enum set.
// ValidProfileStatus 用于判断某个画像状态 ID 是否属于当前支持的画像节点状态枚举集合。
func ValidProfileStatus(status int) bool {
	return status >= ProfileStatusInvalid && status <= ProfileStatusExpired
}

// ValidProfilePriority reports whether one profile priority belongs to the supported priority enum set.
// ValidProfilePriority 用于判断某个画像优先级是否属于当前支持的优先级枚举集合。
func ValidProfilePriority(priority int) bool {
	return priority >= ProfilePriorityP0 && priority <= ProfilePriorityP2
}

// ValidProfileLevel reports whether one profile level belongs to the supported lifecycle enum set.
// ValidProfileLevel 用于判断某个画像等级是否属于当前支持的生命周期枚举集合。
func ValidProfileLevel(level int) bool {
	return level >= ProfileLevelTransient && level <= ProfileLevelPersistent
}

// ValidProfileSourceKind reports whether one profile source kind belongs to the supported source enum set.
// ValidProfileSourceKind 用于判断某个画像来源类型是否属于当前支持的来源枚举集合。
func ValidProfileSourceKind(sourceKind int) bool {
	return sourceKind >= ProfileSourceKindTurnExtract && sourceKind <= ProfileSourceKindRetainedAfterUserDelete
}

// ValidMemoryContextRelation reports whether one contextual evidence relation belongs to the supported support/rebuttal set.
// ValidMemoryContextRelation 用于判断某个情境证据关系是否属于当前支持的 support/rebuttal 集合。
func ValidMemoryContextRelation(relation string) bool {
	switch relation {
	case MemoryContextRelationSupport, MemoryContextRelationRebuttal:
		return true
	default:
		return false
	}
}
