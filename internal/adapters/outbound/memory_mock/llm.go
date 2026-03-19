package memory_mock

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/openvulcan/vmm/internal/core/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

type LLMClient struct {
	Delay      time.Duration
	ForceError error
	Override   func(history []domain.HistorySnippet, current string) string
}

func NewLLMClient() *LLMClient { return &LLMClient{} }

func (c *LLMClient) ExtractIntent(ctx context.Context, history []domain.HistorySnippet, current string) (string, error) {
	if c.Delay > 0 {
		timer := time.NewTimer(c.Delay)
		defer timer.Stop()
		select { case <-ctx.Done(): return "", ctx.Err(); case <-timer.C: }
	}
	if c.ForceError != nil { return "", c.ForceError }
	select { case <-ctx.Done(): return "", ctx.Err(); default: }

	if c.Override != nil {
		if out := strings.TrimSpace(c.Override(history, current)); out != "" {
			return out, nil
		}
		return "", errors.New("override returned empty intent")
	}
	text := strings.ToLower(strings.TrimSpace(current))
	combined := make([]string, 0, len(history)+1)
	for _, item := range history { combined = append(combined, item.Content) }
	combined = append(combined, current)
	tokens := textutil.Tokenize(strings.Join(combined, " "))
	expansions := make([]string, 0)
	if containsAny(text, "框架", "framework") { expansions = append(expansions, "fastapi", "gin", "spring", "django", "express") }
	if containsAny(text, "后端", "backend", "api", "服务") { expansions = append(expansions, "backend", "service", "api") }
	if containsAny(text, "go", "golang") { expansions = append(expansions, "go", "golang", "gin") }
	if containsAny(text, "数据库", "sql", "db") { expansions = append(expansions, "postgres", "mysql", "sql") }
	dedup := map[string]struct{}{}
	out := make([]string, 0, 12)
	for _, token := range append(expansions, tokens...) {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" { continue }
		if _, ok := dedup[token]; ok { continue }
		dedup[token] = struct{}{}
		out = append(out, token)
		if len(out) >= 12 { break }
	}
	if len(out) == 0 { return strings.TrimSpace(current), nil }
	return strings.Join(out, " "), nil
}

func containsAny(s string, terms ...string) bool {
	for _, term := range terms { if strings.Contains(s, strings.ToLower(term)) { return true } }
	return false
}
