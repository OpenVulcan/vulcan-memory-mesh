package processor

import (
	"context"
	"errors"
	"testing"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type testPromptSource struct{}

func (testPromptSource) GetPrompt(scene, modelName string) (string, error) {
	return "prompt:" + scene + ":" + modelName, nil
}

type testLLMClient struct {
	content string
	err     error
}

func (c testLLMClient) Generate(ctx context.Context, req appports.LLMRequest) (appports.LLMResponse, error) {
	if c.err != nil {
		return appports.LLMResponse{}, c.err
	}
	return appports.LLMResponse{Content: c.content}, nil
}

func TestIntentExtractorParsesVikingStyleMarkdownJSON(t *testing.T) {
	extractor := NewIntentExtractor(testLLMClient{content: "analysis...\n```json\n{\n  \"keywords\": [\"go\", \"memory\", \"go\"],\n  \"need_memory\": true,\n  \"reason\": \"match user question\"\n}\n```\nextra"}, testPromptSource{}, "mock", 5)
	intent, err := extractor.Extract(context.Background(), []logicdomain.HistorySnippet{{Role: "user", Content: "hello"}}, "go memory")
	if err != nil {
		t.Fatal(err)
	}
	if !intent.NeedMemory {
		t.Fatal("expected need_memory=true")
	}
	if len(intent.Keywords) != 2 {
		t.Fatalf("keywords len = %d", len(intent.Keywords))
	}
	if intent.Keywords[0] != "go" || intent.Keywords[1] != "memory" {
		t.Fatalf("keywords = %#v", intent.Keywords)
	}
}

func TestIntentExtractorRejectsInvalidJSON(t *testing.T) {
	extractor := NewIntentExtractor(testLLMClient{content: "```json\n[1,2,3]\n```"}, testPromptSource{}, "mock", 5)
	_, err := extractor.Extract(context.Background(), nil, "hello")
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidLLMOutputError, got %v", err)
	}
}

func TestStripMarkdownFences(t *testing.T) {
	got := stripMarkdownFences("```JSON\n{\"k\":1}\n```")
	if got != "{\"k\":1}" {
		t.Fatalf("got %q", got)
	}
}
