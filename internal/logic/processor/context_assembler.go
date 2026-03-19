package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

type ContextAssembler struct {
	prompts appports.PromptSource
	model   string
}

func NewContextAssembler(prompts appports.PromptSource, model string) *ContextAssembler {
	return &ContextAssembler{prompts: prompts, model: strings.TrimSpace(model)}
}

func (a *ContextAssembler) Assemble(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	_ = ctx
	if _, err := a.prompts.GetPrompt("assemble_context", a.model); err != nil {
		return "", nil, fmt.Errorf("load assemble_context prompt: %w", err)
	}
	items := append(personaToItems(persona), memoryHitsToItems(hits)...)
	return renderContextSummary(items), items, nil
}

func personaToItems(persona logicdomain.PersonaContext) []logicdomain.ContextItem {
	items := make([]logicdomain.ContextItem, 0, len(persona.ProjectConstraints)+len(persona.Profile)+len(persona.Preferences))
	for _, text := range persona.ProjectConstraints {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "project_constraint", Title: "项目约束", Text: text, Source: "persona"})
		}
	}
	for _, text := range persona.Profile {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "persona", Title: "个人画像", Text: text, Source: "persona"})
		}
	}
	for _, text := range persona.Preferences {
		if text = strings.TrimSpace(text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "preference", Title: "偏好习惯", Text: text, Source: "persona"})
		}
	}
	return items
}

func memoryHitsToItems(hits []logicdomain.MemoryHit) []logicdomain.ContextItem {
	items := make([]logicdomain.ContextItem, 0, len(hits))
	for _, hit := range hits {
		if text := strings.TrimSpace(hit.Text); text != "" {
			items = append(items, logicdomain.ContextItem{Kind: "memory", Title: "向量召回记忆", Text: text, Source: "vector", Score: hit.Score})
		}
	}
	return items
}
