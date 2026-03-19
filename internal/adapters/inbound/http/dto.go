package httpapi

import "encoding/json"

type HistorySnippetDTO struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolCallDTO struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type RawMessageDTO struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []ToolCallDTO   `json:"tool_calls,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
}

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

type PostActionRequestDTO struct {
	SessionID           string          `json:"session_id"`
	UserID              string          `json:"user_id"`
	TeamID              string          `json:"team_id"`
	SpaceID             string          `json:"space_id,omitempty"`
	ProjectID           string          `json:"project_id"`
	RawMessagesSnapshot []RawMessageDTO `json:"raw_messages_snapshot"`
}

type SeedMemoryRequestDTO struct {
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	MemoryText string `json:"memory_text"`
	SpaceID    string `json:"space_id,omitempty"`
}

type ContextItemDTO struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Text   string  `json:"text"`
	Source string  `json:"source"`
	Score  float64 `json:"score,omitempty"`
}

type PreCheckResponseDTO struct {
	ShouldInject bool             `json:"should_inject"`
	ContextText  string           `json:"context_text"`
	ContextItems []ContextItemDTO `json:"context_items"`
	Degraded     bool             `json:"degraded"`
}

type PostActionResponseDTO struct {
	Accepted bool `json:"accepted"`
}

type SeedMemoryResponseDTO struct {
	Accepted bool   `json:"accepted"`
	MemoryID string `json:"memory_id"`
}
