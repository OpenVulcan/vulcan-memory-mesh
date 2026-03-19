package domain

import "time"

type SessionRef struct {
	SessionID string
	UserID    string
	TeamID    string
	SpaceID   string
	ProjectID string
}

func (s SessionRef) SearchFilter() SearchFilter {
	return SearchFilter{UserID: s.UserID, ProjectID: s.ProjectID, SpaceID: s.SpaceID}
}

type SearchFilter struct {
	UserID    string
	ProjectID string
	SpaceID   string
}

type HistorySnippet struct {
	Role    string
	Content string
}

type ToolCall struct {
	ID   string
	Type string
}

type RawMessage struct {
	Role      string
	Content   any
	ToolCalls []ToolCall
	Meta      map[string]any
}

type NormalizedTurn struct {
	TurnIndex      int
	UserMessage    string
	AssistantReply string
	CreatedAt      time.Time
}

type PersonaContext struct {
	ProjectConstraints []string
	Profile            []string
	Preferences        []string
}

func (p PersonaContext) Empty() bool {
	return len(p.ProjectConstraints) == 0 && len(p.Profile) == 0 && len(p.Preferences) == 0
}

type MemoryRecord struct {
	ID        string
	Text      string
	Vector    []float32
	Filter    SearchFilter
	Metadata  map[string]string
	CreatedAt time.Time
}

type MemoryHit struct {
	ID       string
	Text     string
	Score    float64
	Filter   SearchFilter
	Metadata map[string]string
}

type ContextItem struct {
	Kind   string
	Title  string
	Text   string
	Source string
	Score  float64
}
