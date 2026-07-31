// common.go declares the core hierarchy, conversation, and memory models shared across the logic layer.
// common.go 用于声明逻辑层共享的核心层级、会话和记忆模型。
package domain

import "time"

// SessionMemoryStatus is the authoritative lifecycle state applied to one runtime session scope.
// SessionMemoryStatus 是应用到单个运行时会话范围的权威生命周期状态。
type SessionMemoryStatus string

const (
	// SessionMemoryStatusActive allows normal recall and post-action persistence.
	// SessionMemoryStatusActive 允许正常召回和 PostAction 持久化。
	SessionMemoryStatusActive SessionMemoryStatus = "active"
	// SessionMemoryStatusArchived hides the session from default management lists without changing runtime behavior.
	// SessionMemoryStatusArchived 从默认管理列表隐藏会话，但不改变运行时行为。
	SessionMemoryStatusArchived SessionMemoryStatus = "archived"
	// SessionMemoryStatusRecycled pauses recall and persistence while recoverable data remains in the recycle bin.
	// SessionMemoryStatusRecycled 在可恢复数据仍位于回收站期间暂停召回与持久化。
	SessionMemoryStatusRecycled SessionMemoryStatus = "recycled"
	// SessionMemoryStatusForgotten preserves only a no-content tombstone after permanent purge.
	// SessionMemoryStatusForgotten 在永久清理后仅保留无内容墓碑。
	SessionMemoryStatusForgotten SessionMemoryStatus = "forgotten"
)

// AllowsRuntimeMemory reports whether the status participates in ordinary recall and persistence.
// AllowsRuntimeMemory 用于判断该状态是否参与常规召回与持久化。
func (s SessionMemoryStatus) AllowsRuntimeMemory() bool {
	status := s.Effective()
	return status == SessionMemoryStatusActive || status == SessionMemoryStatusArchived
}

// Effective maps the zero value used by existing in-process callers to the historical active behavior.
// Effective 用于把既有进程内调用方使用的零值映射为历史上的 active 行为。
func (s SessionMemoryStatus) Effective() SessionMemoryStatus {
	if s == "" {
		return SessionMemoryStatusActive
	}
	return s
}

// ValidateManagementAction rejects lifecycle transitions that would bypass restore or explicit restart semantics.
// ValidateManagementAction 用于拒绝会绕过恢复流程或显式重新开始语义的生命周期转换。
func (s SessionMemoryStatus) ValidateManagementAction(action string) error {
	s = s.Effective()
	switch action {
	case ManagementActionArchive:
		if s != SessionMemoryStatusActive {
			return ConflictError{Resource: "session", Message: "only active sessions can be archived"}
		}
	case ManagementActionUnarchive:
		if s != SessionMemoryStatusArchived {
			return ConflictError{Resource: "session", Message: "only archived sessions can be unarchived"}
		}
	case ManagementActionRecycle:
		if s != SessionMemoryStatusActive && s != SessionMemoryStatusArchived {
			return ConflictError{Resource: "session", Message: "only active or archived sessions can be recycled"}
		}
	case ManagementActionRestart:
		if s != SessionMemoryStatusForgotten {
			return ConflictError{Resource: "session", Message: "only forgotten sessions can restart memory"}
		}
	}
	return nil
}

// SessionRef carries the resolved numeric hierarchy identifiers plus the external session key used by business RPCs.
// SessionRef 用于承载业务 RPC 使用的外部 session_key，以及解析后的数字层级标识。
type SessionRef struct {
	SessionID              uint64
	SessionKey             string
	UserID                 uint64
	TeamID                 uint64
	SpaceID                uint64
	ProjectID              uint64
	TurnCount              int
	LastSummarizedID       uint64
	LastCompactedTurnID    uint64
	SummarizeContent       string
	SummarizeBudget        int
	LastExtractObservedAt  time.Time
	LastExtractCompletedAt time.Time
	LastCompactedAt        time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	UserName               string
	TeamName               string
	SpaceName              string
	ProjectName            string
	MemoryStatus           SessionMemoryStatus
}

// SearchFilter derives the concrete vector-scope coordinates used by recall and cleanup flows.
// SearchFilter 用于推导召回和清理流程使用的具体向量范围坐标。
func (s SessionRef) SearchFilter() SearchFilter {
	return SearchFilter{
		UserID:    s.UserID,
		TeamID:    s.TeamID,
		SpaceID:   s.SpaceID,
		ProjectID: s.ProjectID,
		SessionID: s.SessionID,
	}
}

// SearchFilter carries the flattened hierarchy coordinates applied when vector reads or deletes must stay inside one scope.
// SearchFilter 用于承载向量读写或删除时限制在单个层级范围内的扁平坐标字段。
type SearchFilter struct {
	UserID              uint64
	TeamID              uint64
	SpaceID             uint64
	ProjectID           uint64
	SessionID           uint64
	BoundarySessionID   uint64
	BoundaryMaxTurnID   uint64
	ExcludeBoundaryTurn bool
}

// TurnTimelineItem stores one cleaned middle node that still needs to survive dehydration before a turn is persisted.
// TurnTimelineItem 用于保存一条清洗后的中间节点，让它在 turn 落库前仍能参与脱水处理。
type TurnTimelineItem struct {
	Type    string
	Content string
}

// TurnRecord stores one cleaned post-action turn before the relational adapter dehydrates it into JSON for SQLite-backed persistence.
// TurnRecord 用于保存一条清洗后的 post-action 轮次，让关系适配器再把它脱水成 SQLite 持久化使用的 JSON 结构。
type TurnRecord struct {
	UserContent      string
	Timeline         []TurnTimelineItem
	AssistantContent string
	CreatedAt        time.Time
}

// HistorySnippet stores one normalized text-only dialogue snippet used during intent extraction.
// HistorySnippet 用于保存意图提取阶段使用的一条标准化纯文本对话片段。
type HistorySnippet struct {
	Role    string
	Content string
}

// ToolCall stores lightweight tool invocation metadata carried by raw post-action snapshots.
// ToolCall 用于保存 post-action 原始快照里携带的轻量工具调用元数据。
type ToolCall struct {
	ID   string
	Type string
}

// RawMessage represents one unnormalized chat message before the normalizer filters and pairs it.
// RawMessage 用于表示进入 normalizer 前尚未清洗和配对的一条原始消息。
type RawMessage struct {
	Role      string
	Content   any
	ToolCalls []ToolCall
	Meta      map[string]any
}

// NormalizedTurn represents one cleaned user-assistant pair ready for relational persistence.
// NormalizedTurn 用于表示一条已清洗完毕、可直接落关系存储的用户-助手轮次。
type NormalizedTurn struct {
	TurnIndex      int
	UserMessage    string
	AssistantReply string
	CreatedAt      time.Time
}

// PersonaContext holds stable user and project context that can be injected during pre-check.
// PersonaContext 用于保存 pre-check 阶段可注入的稳定用户画像和项目上下文。
type PersonaContext struct {
	ProjectConstraints []string
	Profile            []string
	Preferences        []string
}

// Empty reports whether the persona contributes no project constraints, profile facts, or preferences to context assembly.
// Empty 用于报告画像是否没有为上下文组装提供项目约束、画像事实或偏好。
func (p PersonaContext) Empty() bool {
	return len(p.ProjectConstraints) == 0 && len(p.Profile) == 0 && len(p.Preferences) == 0
}

// MemoryRecord is the vector-store write model used by seed-memory and future memory persistence flows.
// MemoryRecord 用于表示 seed-memory 等流程写入向量库时使用的记忆记录模型。
type MemoryRecord struct {
	ID           string
	Text         string
	Vector       []float32
	Filter       SearchFilter
	SourceTurnID uint64
	// Status mirrors the durable memory lifecycle state so detached vector stores can filter inactive rows before top-k truncation.
	// Status 用于镜像长期记忆生命周期状态，让独立向量库能在 top-k 截断前过滤非活跃行。
	Status int
	// ExpiresAt mirrors the durable expiry timestamp so detached vector stores can filter expired rows before top-k truncation.
	// ExpiresAt 用于镜像长期记忆过期时间，让独立向量库能在 top-k 截断前过滤已过期行。
	ExpiresAt time.Time
	Metadata  map[string]string
	CreatedAt time.Time
}

// MemoryHit represents one recalled memory candidate returned by semantic or SQL-fused retrieval before the app layer maps it into a durable query hit.
// MemoryHit 用于表示语义检索或 SQL 融合检索返回的一条召回候选，随后由应用层映射成长期记忆查询命中。
type MemoryHit struct {
	// ID stores the vector-side identifier that preserves the legacy detached vector-store lookup contract.
	// ID 用于保存向量侧标识，保持历史分离向量库按 vector id 补全的查询契约。
	ID string
	// Text stores the lightweight recall text carried by detached vector backends for diagnostics and ranking traces.
	// Text 用于保存分离向量后端携带的轻量召回文本，供诊断和排序追踪使用。
	Text string
	// Score stores the store-native higher-is-better recall score before app-layer normalization or fusion rewrites it.
	// Score 用于保存存储侧原生的越高越好召回分数，应用层归一化或融合前使用。
	Score float64
	// Record optionally carries the already materialized durable memory row when the retrieval backend queried the unified memory table itself.
	// Record 在检索后端自身已经查询统一记忆表时，可选携带已经物化的长期记忆行。
	Record MemoryNodeRecord
	// CreatedAt carries backend-level creation time when a detached vector backend exposes it directly.
	// CreatedAt 用于在分离向量后端直接暴露创建时间时携带该时间。
	CreatedAt time.Time
	// Filter carries the scoped coordinates observed by recall so app-layer fallbacks can preserve the original search scope.
	// Filter 用于携带召回阶段观察到的范围坐标，使应用层回退逻辑能够保留原始搜索范围。
	Filter SearchFilter
	// Metadata carries backend-specific ranking diagnostics and must include the origin label for recall hits returned through VectorStore.
	// Metadata 用于携带后端特定的排序诊断信息，并且通过 VectorStore 返回的召回命中必须包含来源标签。
	Metadata map[string]string
}

// UserRecord stores one durable user row that can be resolved by numeric id or unique name during admin RPCs.
// UserRecord 用于保存一条可通过数字 ID 或唯一名称解析的长期用户记录，供管理 RPC 使用。
type UserRecord struct {
	ID                uint64
	Name              string
	Profile           string
	DeleteConfirmCode string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TeamRecord stores one top-level hierarchy node that namespaces spaces and projects.
// TeamRecord 用于保存一个顶层层级节点，为 space 和 project 提供命名空间。
type TeamRecord struct {
	ID        uint64
	Name      string
	Profile   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SpaceRecord stores one team-scoped hierarchy node that namespaces projects.
// SpaceRecord 用于保存一个 team 作用域下的层级节点，为 project 提供命名空间。
type SpaceRecord struct {
	ID        uint64
	TeamID    uint64
	Name      string
	Profile   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectRecord stores one canonical project node that clients use as the public hierarchy handle.
// ProjectRecord 用于保存一个规范项目节点，并作为客户端对外使用的层级句柄。
type ProjectRecord struct {
	ID        uint64
	TeamID    uint64
	SpaceID   uint64
	TeamName  string
	SpaceName string
	Name      string
	Profile   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Path returns the canonical Team/Space/Project path shown to clients and docs.
// Path 用于返回展示给客户端和文档的标准 Team/Space/Project 路径。
func (p ProjectRecord) Path() string {
	return p.TeamName + "/" + p.SpaceName + "/" + p.Name
}

// SessionRecord stores one durable session row that binds an external session key to concrete hierarchy coordinates.
// SessionRecord 用于保存一条长期 session 记录，把外部 session_key 绑定到具体层级坐标。
type SessionRecord struct {
	ID                     uint64
	SessionKey             string
	UserID                 uint64
	TeamID                 uint64
	SpaceID                uint64
	ProjectID              uint64
	TurnCount              int
	LastSummarizedID       uint64
	LastCompactedTurnID    uint64
	SummarizeContent       string
	SummarizeBudget        int
	LastExtractObservedAt  time.Time
	LastExtractCompletedAt time.Time
	LastCompactedAt        time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ContextItem represents one final context fragment returned to plugins after assembly.
// ContextItem 用于表示上下文组装完成后返回给插件的一条最终上下文片段。
type ContextItem struct {
	Kind             string
	Title            string
	MemoryID         uint64
	Text             string
	Source           string
	Score            float64
	TurnID           uint64
	CreatedTimestamp int64
	CreatedDateTime  string
}
