// common.go declares the core conversation, memory, and persona models shared across the logic layer.
// common.go 用于声明逻辑层共享的核心会话、记忆和画像模型。
package domain

import "time"

// SessionRef carries the identifiers that tie one request to a concrete user, session, and project scope.
// SessionRef 用于承载把一次请求绑定到具体用户、会话和项目范围的标识。
type SessionRef struct {
	SessionID string
	UserID    string
	TeamID    string
	SpaceID   string
	ProjectID string
}

// SearchFilter executes the SearchFilter logic.
// SearchFilter 用于执行 SearchFilter 逻辑。
func (s SessionRef) SearchFilter() SearchFilter {
	return SearchFilter{UserID: s.UserID, ProjectID: s.ProjectID, SpaceID: s.SpaceID}
}

// SearchFilter carries the scope fields applied when vector recall must stay inside the active tenant/project context.
// SearchFilter 用于承载向量召回时限制在当前用户和项目范围内的过滤字段。
type SearchFilter struct {
	UserID    string
	ProjectID string
	SpaceID   string
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

// Empty executes the Empty logic.
// Empty 用于执行 Empty 逻辑。
func (p PersonaContext) Empty() bool {
	return len(p.ProjectConstraints) == 0 && len(p.Profile) == 0 && len(p.Preferences) == 0
}

// MemoryRecord is the vector-store write model used by seed-memory and future memory persistence flows.
// MemoryRecord 用于表示 seed-memory 等流程写入向量库时使用的记忆记录模型。
type MemoryRecord struct {
	ID        string
	Text      string
	Vector    []float32
	Filter    SearchFilter
	Metadata  map[string]string
	CreatedAt time.Time
}

// MemoryHit represents one recalled memory candidate returned by the vector backend.
// MemoryHit 用于表示向量后端返回的一条召回记忆候选项。
type MemoryHit struct {
	ID       string
	Text     string
	Score    float64
	Filter   SearchFilter
	Metadata map[string]string
}

// ContextItem represents one final context fragment returned to plugins after assembly.
// ContextItem 用于表示上下文组装完成后返回给插件的一条最终上下文片段。
type ContextItem struct {
	Kind   string
	Title  string
	Text   string
	Source string
	Score  float64
}
