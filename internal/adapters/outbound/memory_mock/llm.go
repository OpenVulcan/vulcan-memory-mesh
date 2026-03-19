package memory_mock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/platform/textutil"
)

type LLMClient struct {
	Delay      time.Duration
	ForceError error
	Override   func(req appports.LLMRequest) string
}

func NewLLMClient() *LLMClient { return &LLMClient{} }

func (c *LLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.Delay > 0 {
		timer := time.NewTimer(c.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return appports.LLMResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	if c.ForceError != nil {
		return appports.LLMResponse{}, c.ForceError
	}
	select {
	case <-ctx.Done():
		return appports.LLMResponse{}, ctx.Err()
	default:
	}
	if c.Override != nil {
		out := strings.TrimSpace(c.Override(req))
		if out == "" {
			return appports.LLMResponse{}, errors.New("override returned empty response")
		}
		return appports.LLMResponse{Content: out}, nil
	}
	if req.ResponseFormat == appports.LLMResponseFormatJSON {
		keywords, needMemory := synthesizeIntent(req.UserPrompt)
		return appports.LLMResponse{Content: fmt.Sprintf(`{"keywords":%s,"need_memory":%t,"reason":"mock generated"}`, marshalStringArray(keywords), needMemory)}, nil
	}
	return appports.LLMResponse{Content: strings.TrimSpace(req.UserPrompt)}, nil
}

func synthesizeIntent(text string) ([]string, bool) {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if containsAny(lowered, "无需记忆", "不用查记忆", "need_memory=false") {
		return nil, false
	}
	tokens := textutil.Tokenize(lowered)
	if containsAny(lowered, "框架", "framework") && containsAny(lowered, "后端", "backend", "api", "服务") {
		return []string{"fastapi", "backend", "framework"}, true
	}
	expansions := make([]string, 0)
	if containsAny(lowered, "框架", "framework") {
		expansions = append(expansions, "fastapi", "gin", "spring", "django", "express")
	}
	if containsAny(lowered, "后端", "backend", "api", "服务") {
		expansions = append(expansions, "backend", "service", "api")
	}
	if containsAny(lowered, "go", "golang") {
		expansions = append(expansions, "go", "golang", "gin")
	}
	if containsAny(lowered, "数据库", "sql", "db") {
		expansions = append(expansions, "sqlite", "mysql", "sql")
	}
	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	for _, token := range append(expansions, tokens...) {
		token = strings.TrimSpace(strings.ToLower(token))
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
func marshalStringArray(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%q", item))
	}
	return "[" + strings.Join(parts, ",") + "]"
}
func containsAny(s string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(s, strings.ToLower(term)) {
			return true
		}
	}
	return false
}
