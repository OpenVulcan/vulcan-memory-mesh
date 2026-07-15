// context_assembler.go implements reusable business processors.
// context_assembler.go 用于实现可复用的业务处理器。
package processor

import (
	"context"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// ContextAssembler merges persona data and recalled memories into the final context payload returned by pre-check using one deterministic local renderer.
// ContextAssembler 用于通过本地确定性渲染器，把画像数据和召回记忆合并成 pre-check 返回的最终上下文载荷。
type ContextAssembler struct{}

// NewContextAssembler creates the deterministic local assembler used by the pre-check context path.
// NewContextAssembler 用于创建 pre-check 上下文链路使用的确定性本地组装器。
func NewContextAssembler() *ContextAssembler { return &ContextAssembler{} }

// Assemble satisfies the cancellable pre-check assembler contract while this deterministic local implementation performs no blocking work.
// Assemble 用于满足可取消的 pre-check 组装器契约；当前确定性本地实现不执行阻塞操作。
func (a *ContextAssembler) Assemble(_ context.Context, persona logicdomain.PersonaContext, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, error) {
	items := append(personaToItems(persona), memoryHitsToItems(hits)...)
	return renderContextSummary(items), items, nil
}

// personaToItems converts each persona section into labeled context items while preserving section order.
// personaToItems 用于把各画像分区转换为带标签的上下文条目，并保持分区顺序。
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

// memoryHitsToItems maps recalled memories into context items with stable memory and source-turn identifiers.
// memoryHitsToItems 用于把召回记忆映射为带稳定记忆 ID 与来源轮次 ID 的上下文条目。
func memoryHitsToItems(hits []logicdomain.MemoryHit) []logicdomain.ContextItem {
	items := make([]logicdomain.ContextItem, 0, len(hits))
	for _, hit := range hits {
		if text := strings.TrimSpace(hit.Text); text != "" {
			items = append(items, logicdomain.ContextItem{
				Kind:             "memory",
				Title:            "混合召回记忆",
				MemoryID:         logicdomain.MemoryHitMemoryID(hit),
				Text:             text,
				Source:           "memory",
				Score:            hit.Score,
				TurnID:           logicdomain.MemoryHitTurnID(hit),
				CreatedTimestamp: logicdomain.MemoryHitCreatedTimestamp(hit),
				CreatedDateTime:  logicdomain.MemoryHitCreatedDateTime(hit),
			})
		}
	}
	return items
}
