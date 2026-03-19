package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

type ProfileMerger struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

func NewProfileMerger(llm appports.LLMClient, prompts appports.PromptSource, model string) *ProfileMerger {
	return &ProfileMerger{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

func (m *ProfileMerger) Merge(ctx context.Context, existingProfile, newEntry string) (string, error) {
	prompt, err := m.prompts.GetPrompt("merge_profile", m.model)
	if err != nil {
		return "", fmt.Errorf("load merge_profile prompt: %w", err)
	}
	userPrompt := strings.TrimSpace(existingProfile) + "\n\n---\n\n" + strings.TrimSpace(newEntry)
	resp, err := m.llm.Generate(ctx, appports.LLMRequest{Model: m.model, SystemPrompt: prompt, UserPrompt: strings.TrimSpace(userPrompt), ResponseFormat: appports.LLMResponseFormatJSON})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
