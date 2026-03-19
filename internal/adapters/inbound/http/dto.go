package httpapi

import "github.com/openvulcan/vmm/internal/core/domain"

type PreCheckRequest struct {
	SessionID      string                  `json:"session_id"`
	UserID         string                  `json:"user_id"`
	TeamID         string                  `json:"team_id"`
	SpaceID        string                  `json:"space_id,omitempty"`
	ProjectID      string                  `json:"project_id"`
	HistoryContent []domain.HistorySnippet `json:"history_content"`
	CurrentContent string                  `json:"current_content"`
	IsFirstTurn    bool                    `json:"is_first_turn"`
}

type PostActionRequest struct {
	SessionID           string              `json:"session_id"`
	UserID              string              `json:"user_id"`
	TeamID              string              `json:"team_id"`
	SpaceID             string              `json:"space_id,omitempty"`
	ProjectID           string              `json:"project_id"`
	RawMessagesSnapshot []domain.RawMessage `json:"raw_messages_snapshot"`
}

type SeedMemoryRequest struct {
	UserID     string `json:"user_id"`
	ProjectID  string `json:"project_id"`
	MemoryText string `json:"memory_text"`
	SpaceID    string `json:"space_id,omitempty"`
}
