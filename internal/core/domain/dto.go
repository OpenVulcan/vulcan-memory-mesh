package domain

type PreCheckRequest struct {
	SessionID      string           `json:"session_id"`
	UserID         string           `json:"user_id"`
	TeamID         string           `json:"team_id"`
	SpaceID        string           `json:"space_id,omitempty"`
	ProjectID      string           `json:"project_id"`
	HistoryContent []HistorySnippet `json:"history_content"`
	CurrentContent string           `json:"current_content"`
	IsFirstTurn    bool             `json:"is_first_turn"`
}

func (r PreCheckRequest) SessionRef() SessionRef {
	return SessionRef{SessionID: r.SessionID, UserID: r.UserID, TeamID: r.TeamID, SpaceID: r.SpaceID, ProjectID: r.ProjectID}
}

type PreCheckResponse struct {
	ShouldInject bool          `json:"should_inject"`
	ContextText  string        `json:"context_text"`
	ContextItems []ContextItem `json:"context_items"`
	Degraded     bool          `json:"degraded"`
	TraceID      string        `json:"trace_id"`
}

type PostActionRequest struct {
	SessionID           string       `json:"session_id"`
	UserID              string       `json:"user_id"`
	TeamID              string       `json:"team_id"`
	SpaceID             string       `json:"space_id,omitempty"`
	ProjectID           string       `json:"project_id"`
	RawMessagesSnapshot []RawMessage `json:"raw_messages_snapshot"`
}

func (r PostActionRequest) SessionRef() SessionRef {
	return SessionRef{SessionID: r.SessionID, UserID: r.UserID, TeamID: r.TeamID, SpaceID: r.SpaceID, ProjectID: r.ProjectID}
}

type PostActionResponse struct {
	Accepted bool   `json:"accepted"`
	TraceID  string `json:"trace_id"`
}

type SeedMemoryRequest struct {
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	MemoryText string `json:"memory_text"`
	SpaceID    string `json:"space_id,omitempty"`
}

type SeedMemoryResponse struct {
	Accepted bool   `json:"accepted"`
	MemoryID string `json:"memory_id"`
	TraceID  string `json:"trace_id"`
}
