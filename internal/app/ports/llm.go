package ports

import (
	"context"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type LLMResponseFormat string

const (
	LLMResponseFormatText LLMResponseFormat = "text"
	LLMResponseFormatJSON LLMResponseFormat = "json"
)

type LLMRequest struct {
	Model          string
	SystemPrompt   string
	UserPrompt     string
	ResponseFormat LLMResponseFormat
	ProviderHints  map[string]any
}

type LLMResponse struct {
	Content string
	Usage   logicdomain.LLMUsage
}

type LLMClient interface {
	Generate(ctx context.Context, req LLMRequest) (LLMResponse, error)
}
