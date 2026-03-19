package domain

import "time"

type SessionRef struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	TeamID    string `json:"team_id"`
	SpaceID   string `json:"space_id,omitempty"`
	ProjectID string `json:"project_id"`
}

func (s SessionRef) SearchFilter() SearchFilter {
	return SearchFilter{UserID: s.UserID, ProjectID: s.ProjectID, SpaceID: s.SpaceID}
}

type SearchFilter struct {
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	SpaceID   string `json:"space_id,omitempty"`
}

type HistorySnippet struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolCall struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

type RawMessage struct {
	Role      string         `json:"role"`
	Content   any            `json:"content"`
	ToolCalls []ToolCall     `json:"tool_calls,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type NormalizedTurn struct {
	TurnIndex      int       `json:"turn_index"`
	UserMessage    string    `json:"user_message"`
	AssistantReply string    `json:"assistant_reply"`
	CreatedAt      time.Time `json:"created_at"`
}

type PersonaContext struct {
	ProjectConstraints []string `json:"project_constraints,omitempty"`
	Profile            []string `json:"profile,omitempty"`
	Preferences        []string `json:"preferences,omitempty"`
}

func (p PersonaContext) Empty() bool {
	return len(p.ProjectConstraints) == 0 && len(p.Profile) == 0 && len(p.Preferences) == 0
}

type MemoryRecord struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Vector    []float32         `json:"vector"`
	Filter    SearchFilter      `json:"filter"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type MemoryHit struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Score    float64           `json:"score"`
	Filter   SearchFilter      `json:"filter"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type ContextItem struct {
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Text   string  `json:"text"`
	Source string  `json:"source"`
	Score  float64 `json:"score,omitempty"`
}
