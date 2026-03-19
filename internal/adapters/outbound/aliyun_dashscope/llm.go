package aliyun_dashscope

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvulcan/vmm/internal/core/domain"
)

type LLMClient struct {
	client   *Client
	endpoint string
	model    string
}

func NewLLMClient(endpoint, apiKey, model string) *LLMClient {
	return &LLMClient{client: NewClient(endpoint, apiKey, nil), endpoint: endpoint, model: model}
}

func (c *LLMClient) ExtractIntent(ctx context.Context, history []domain.HistorySnippet, current string) (string, error) {
	prompt := buildIntentPrompt(history, current)
	req := map[string]any{
		"model": c.model,
		"input": map[string]any{"messages": []map[string]string{
			{"role": "system", "content": "你是语义提炼器，只输出中心意图关键词，不要解释。"},
			{"role": "user", "content": prompt},
		}},
		"parameters": map[string]any{"temperature": 0, "result_format": "message"},
	}
	var resp struct {
		Output struct {
			Text    string `json:"text"`
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
	}
	if err := c.client.PostJSON(ctx, c.endpoint, req, &resp); err != nil { return "", err }
	if strings.TrimSpace(resp.Output.Text) != "" { return strings.TrimSpace(resp.Output.Text), nil }
	if len(resp.Output.Choices) > 0 { return strings.TrimSpace(resp.Output.Choices[0].Message.Content), nil }
	return "", fmt.Errorf("dashscope llm empty response")
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
