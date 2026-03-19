// profile_merger.go implements reusable business processors.
// profile_merger.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// ProfileMerger is the processor reserved for folding new profile evidence into an existing user profile.
// ProfileMerger 用于作为处理器，把新的画像证据合并进已有用户画像。
type ProfileMerger struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewProfileMerger creates a ProfileMerger instance.
// NewProfileMerger 用于创建 ProfileMerger 实例。
func NewProfileMerger(llm appports.LLMClient, prompts appports.PromptSource, model string) *ProfileMerger {
	return &ProfileMerger{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Merge executes the Merge logic.
// Merge 用于执行 Merge 逻辑。
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
