// entry_summarizer.go implements reusable business processors.
// entry_summarizer.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
)

// EntrySummarizer is the processor reserved for turning raw transcripts into structured memory summaries.
// EntrySummarizer 用于作为处理器，把原始对话转成结构化记忆摘要。
type EntrySummarizer struct {
	llm     appports.LLMClient
	prompts appports.PromptSource
	model   string
}

// NewEntrySummarizer creates a EntrySummarizer instance.
// NewEntrySummarizer 用于创建 EntrySummarizer 实例。
func NewEntrySummarizer(llm appports.LLMClient, prompts appports.PromptSource, model string) *EntrySummarizer {
	return &EntrySummarizer{llm: llm, prompts: prompts, model: strings.TrimSpace(model)}
}

// Summarize executes the Summarize logic.
// Summarize 用于执行 Summarize 逻辑。
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
