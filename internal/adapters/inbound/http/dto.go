// dto.go declares the HTTP transport DTOs that isolate API payloads from use case commands.
// dto.go 用于声明 HTTP 传输层 DTO，把 API 载荷与用例命令隔离开来。
package httpapi

import "encoding/json"

// HistorySnippetDTO carries one text-only history message from the pre-check request body.
// HistorySnippetDTO 用于承载 pre-check 请求体中的单条纯文本历史消息。
type HistorySnippetDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolCallDTO captures tool call metadata before the HTTP layer maps snapshots into domain messages.
// ToolCallDTO 用于在 HTTP 层把快照映射成领域消息之前保存工具调用元数据。
type ToolCallDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// RawMessageDTO accepts raw post-action message payloads so downstream normalizers can decide how to parse content.
// RawMessageDTO 用于接收 post-action 的原始消息载荷，供下游 normalizer 决定如何解析内容。
type RawMessageDTO struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []ToolCallDTO   `json:"tool_calls,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

// PreCheckRequestDTO binds the /v1/chat/pre-check request body before it is converted into a use case command.
// PreCheckRequestDTO 用于绑定 /v1/chat/pre-check 请求体，再转换成用例命令。
type PreCheckRequestDTO struct {
	SessionID      string              `json:"session_id"`
	UserID         string              `json:"user_id"`
	TeamID         string              `json:"team_id"`
	SpaceID        string              `json:"space_id,omitempty"`
	ProjectID      string              `json:"project_id"`
	HistoryContent []HistorySnippetDTO `json:"history_content"`
	CurrentContent string              `json:"current_content"`
	IsFirstTurn    bool                `json:"is_first_turn"`
}

// PostActionRequestDTO binds the /v1/chat/post-action request body before normalization and persistence.
// PostActionRequestDTO 用于绑定 /v1/chat/post-action 请求体，再进入清洗和持久化流程。
type PostActionRequestDTO struct {
	SessionID           string          `json:"session_id"`
	UserID              string          `json:"user_id"`
	TeamID              string          `json:"team_id"`
	SpaceID             string          `json:"space_id,omitempty"`
	ProjectID           string          `json:"project_id"`
	RawMessagesSnapshot []RawMessageDTO `json:"raw_messages_snapshot"`
}

// SeedMemoryRequestDTO binds the admin seed-memory request used to preload local vector memory.
// SeedMemoryRequestDTO 用于绑定管理员 seed-memory 请求，以便预热本地向量记忆。
type SeedMemoryRequestDTO struct {
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	MemoryText string `json:"memory_text"`
	SpaceID    string `json:"space_id,omitempty"`
}

// ContextItemDTO serializes one assembled context item returned by the pre-check flow.
// ContextItemDTO 用于序列化 pre-check 流程返回的单条上下文项。
type ContextItemDTO struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Text   string  `json:"text"`
	Source string  `json:"source"`
	Score  float64 `json:"score,omitempty"`
}

// PreCheckResponseDTO serializes the final payload returned to plugins after pre-check completes.
// PreCheckResponseDTO 用于序列化 pre-check 完成后返回给插件的最终载荷。
type PreCheckResponseDTO struct {
	ShouldInject bool             `json:"should_inject"`
	ContextText  string           `json:"context_text"`
	ContextItems []ContextItemDTO `json:"context_items"`
	Degraded     bool             `json:"degraded"`
}

// PostActionResponseDTO serializes the acknowledgement returned after post-action writes succeed.
// PostActionResponseDTO 用于序列化 post-action 写入成功后的确认结果。
type PostActionResponseDTO struct {
	Accepted bool `json:"accepted"`
}

// SeedMemoryResponseDTO serializes the acknowledgement and memory id returned by the seed-memory endpoint.
// SeedMemoryResponseDTO 用于序列化 seed-memory 接口返回的确认结果和记忆 ID。
type SeedMemoryResponseDTO struct {
	Accepted bool   `json:"accepted"`
	MemoryID string `json:"memory_id"`
}
