package openai_native

import (
	"context"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type LLMClient struct {
	client *Client
	model  string
}

func NewLLMClient(endpoint, apiKey, model, organization, project string) *LLMClient {
	return &LLMClient{
		client: NewClient(endpoint, apiKey, organization, project, nil),
		model:  strings.TrimSpace(model),
	}
}

func (c *LLMClient) ExtractIntent(ctx context.Context, history []domain.HistorySnippet, current string) (string, error) {
	if c == nil || c.client == nil {
		return "", fmt.Errorf("openai client is nil")
	}
	opts := c.client.requestOptions(ctx)
	if c.client.shouldDisableThinking() {
		opts = append(opts, option.WithJSONSet("enable_thinking", false))
	}
	completion, err := c.client.sdk.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(c.model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("你是语义提炼器，只输出中心意图关键词，不要解释。"),
			openai.UserMessage(buildIntentPrompt(history, current)),
		},
	}, opts...)
	if err != nil {
		return "", err
	}
	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("openai chat completion empty choices")
	}
	out := strings.TrimSpace(completion.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("openai chat completion empty content")
	}
	return out, nil
}

func buildIntentPrompt(history []domain.HistorySnippet, current string) string {
	var b strings.Builder
	b.WriteString("请结合最近对话与当前问题，提炼中心语句或意图关键词，输出 1 行，不超过 20 个词。\n\n")
	if len(history) > 0 {
		b.WriteString("最近对话：\n")
		for _, item := range history {
			b.WriteString("-")
			b.WriteString(item.Role)
			b.WriteString(": ")
			b.WriteString(item.Content)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("当前问题：\n")
	b.WriteString(strings.TrimSpace(current))
	return b.String()
}
