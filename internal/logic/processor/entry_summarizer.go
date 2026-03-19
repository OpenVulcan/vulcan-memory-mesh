package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

type EntrySummarizer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

func NewEntrySummarizer(llm appports.LLMClient, prompts appports.PromptSource, model string) *EntrySummarizer {
	return &EntrySummarizer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

func (s *EntrySummarizer) Summarize(ctx context.Context, transcript string) (string, error) {
	prompt, err := s.prompts.GetPrompt("summarize_entry", s.model)
	if err != nil {
		return "", fmt.Errorf("load summarize_entry prompt: %w", err)
	}
	resp, err := s.llm.Generate(ctx, appports.LLMRequest{Model: s.model, SystemPrompt: prompt, UserPrompt: strings.TrimSpace(transcript), ResponseFormat: appports.LLMResponseFormatJSON})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
