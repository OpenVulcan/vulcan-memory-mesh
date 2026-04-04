// context_assembler.go implements reusable business processors.
// context_assembler.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"fmt"
	"strings"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ContextAssembler merges persona data and recalled memories into the final context payload returned by pre-check.
// ContextAssembler 用于把画像数据和召回记忆合并成 pre-check 返回的最终上下文载荷。
type ContextAssembler struct {
	prompts appports.PromptSource
	model   string
}

// NewContextAssembler creates a ContextAssembler instance.
// NewContextAssembler 用于创建 ContextAssembler 实例。
func NewContextAssembler(prompts appports.PromptSource, model string) *ContextAssembler {
	return &ContextAssembler{prompts: prompts, model: strings.TrimSpace(model)}
}

// Assemble executes the Assemble logic.
// Assemble 用于执行 Assemble 逻辑。
func (a *ContextAssembler) Assemble(ctx context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	_ = ctx
	if _, err := a.prompts.GetPrompt("assemble_context", a.model); err != nil {
		return "", nil, fmt.Errorf("load assemble_context prompt: %w", err)
	}
	items := append(personaToItems(persona), memoryHitsToItems(hits)...)
	return renderContextSummary(items), items, nil
}

// personaToItems executes the personaToItems logic.
// personaToItems 用于执行 personaToItems 逻辑。
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

// memoryHitsToItems executes the memoryHitsToItems logic.
// memoryHitsToItems 用于执行 memoryHitsToItems 逻辑。
func memoryHitsToItems(hits []logicdomain.MemoryHit) []logicdomain.ContextItem {
	items := make([]logicdomain.ContextItem, 0, len(hits))
	for _, hit := range hits {
		if text := strings.TrimSpace(hit.Text); text != "" {
			items = append(items, logicdomain.ContextItem{
				Kind:   "memory",
				Title:  "混合召回记忆",
				Text:   text,
				Source: "memory",
				Score:  hit.Score,
				TurnID: logicdomain.MemoryHitTurnID(hit),
			})
		}
	}
	return items
}
