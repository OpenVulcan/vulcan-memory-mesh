// Pre-check final context assembly and deterministic degraded rendering.
// 应用用例层 pre-check 最终上下文组装及降级文本渲染。
package usecase

import (
	"context"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"strconv"
	"strings"
	"time"
)

// finalizePreCheck assembles the final context text and item list from adopted memories only, and short-circuits to an empty result when no memory survives the pipeline.
// finalizePreCheck 用于仅基于被采纳记忆组装最终上下文；如果没有任何记忆穿过整条链路，则直接返回空结果。
func (u *PreCheckUseCase) finalizePreCheck(ctx context.Context, traceID string, memories []logicdomain.PreCheckMemoryCandidate, degraded bool) (PreCheckResult, error) {
	// Keep lifecycle write-back aligned with the full reviewer selection, then apply same-turn display compaction only when building the final injected context.
	// 先让生命周期写回忠实覆盖 reviewer 的完整选择，再仅在构建最终注入上下文时应用同 turn 展示压缩。
	memories = deduplicateSelectedCandidatesByTurnID(memories)
	memoryHits := make([]logicdomain.MemoryHit, 0, len(memories))
	for _, candidate := range memories {
		text := buildPreCheckMemoryText(candidate)
		if strings.TrimSpace(text) == "" {
			continue
		}
		var metadata map[string]string
		if candidate.SourceTurnID > 0 {
			metadata = map[string]string{
				"turn_id": strconv.FormatUint(candidate.SourceTurnID, 10),
			}
		}
		var createdAt time.Time
		if candidate.CreatedTimestamp > 0 {
			createdAt = logicdomain.MemoryHitCreatedAtFromUnixMillis(candidate.CreatedTimestamp)
		}
		memoryHits = appendUniquePreCheckMemoryHit(memoryHits, logicdomain.MemoryHit{
			ID:        fmt.Sprintf("%d", candidate.MemoryID),
			Text:      text,
			Score:     candidate.Score,
			CreatedAt: createdAt,
			Metadata:  metadata,
		})
	}
	if len(memoryHits) == 0 {
		return PreCheckResult{
			ShouldInject: false,
			ContextText:  "",
			ContextItems: nil,
			Degraded:     degraded,
			TraceID:      traceID,
		}, nil
	}
	contextText, items, assembleDegraded := u.assemblePreCheckContext(ctx, memoryHits)
	return PreCheckResult{
		ShouldInject: len(items) > 0,
		ContextText:  contextText,
		ContextItems: items,
		Degraded:     degraded || assembleDegraded,
		TraceID:      traceID,
	}, nil
}

// appendUniquePreCheckMemoryHit keeps the final injected memory list free of equivalent duplicate text so repeated durable rows cannot waste prompt budget or amplify the same hint twice after reviewer selection has already completed.
// appendUniquePreCheckMemoryHit 用于让最终注入的记忆列表剔除文本等价的重复项，避免 reviewer 选择完成后，多条长期记忆行再次浪费 prompt 预算或把同一提示放大两次。
func appendUniquePreCheckMemoryHit(hits []logicdomain.MemoryHit, incoming logicdomain.MemoryHit) []logicdomain.MemoryHit {
	incoming.Text = strings.TrimSpace(incoming.Text)
	if incoming.Text == "" {
		return hits
	}
	for _, existing := range hits {
		if equivalentPreCheckMemoryText(existing.Text, incoming.Text) {
			return hits
		}
	}
	return append(hits, incoming)
}

// assemblePreCheckContext prefers the shared assembler but falls back to a local deterministic renderer if prompt loading degrades.
// assemblePreCheckContext 用于优先使用共享 assembler；若提示词加载降级，则回退到本地确定性渲染。
func (u *PreCheckUseCase) assemblePreCheckContext(ctx context.Context, hits []logicdomain.MemoryHit) (string, []logicdomain.ContextItem, bool) {
	if u.assembler != nil {
		contextText, items, err := u.assembler.Assemble(ctx, logicdomain.PersonaContext{}, hits)
		if err == nil {
			return contextText, items, false
		}
		if u.logger != nil {
			u.logger.Warn("pre-check assembler degraded to fallback", "err", err)
		}
		// Surface fallback activation to the caller so the RPC degraded bit stays honest even when local rendering succeeds.
		// 把 fallback 激活信号向上传递，确保即便本地渲染成功，RPC 的 degraded 标志仍能如实反映本次降级。
		fallbackItems := buildFallbackContextItems(hits)
		return buildFallbackContextSummary(fallbackItems), fallbackItems, true
	}
	items := buildFallbackContextItems(hits)
	return buildFallbackContextSummary(items), items, false
}

// buildPreCheckMemoryText turns one adopted candidate into the compact text fragment injected back to the upstream caller.
// buildPreCheckMemoryText 用于把一条已采纳候选转成回注给上游调用方的紧凑文本片段。
func buildPreCheckMemoryText(candidate logicdomain.PreCheckMemoryCandidate) string {
	abstract := strings.TrimSpace(candidate.Abstract)
	details := strings.TrimSpace(candidate.Details)
	switch {
	case abstract == "" && details == "":
		return ""
	case details == "" || equivalentPreCheckMemoryText(abstract, details):
		return abstract
	case abstract == "":
		return details
	default:
		return abstract + "\n" + details
	}
}

// equivalentPreCheckMemoryText treats whitespace-only and case-only formatting differences as the same injected memory text so pre-check does not repeat one statement twice in the final context.
// equivalentPreCheckMemoryText 用于把仅有空白或大小写差异的文本视为同一条注入记忆，避免 pre-check 在最终上下文里把同一句话重复注入两次。
func equivalentPreCheckMemoryText(left, right string) bool {
	left = strings.ToLower(textutil.NormalizeWhitespace(left))
	right = strings.ToLower(textutil.NormalizeWhitespace(right))
	if left == "" || right == "" {
		return left == right
	}
	return left == right
}

// buildFallbackContextItems reproduces the stable memory-item grouping used by the shared assembler so pre-check can still answer when prompt loading fails.
// buildFallbackContextItems 用于复刻共享 assembler 的稳定记忆分组逻辑，确保提示词加载失败时 pre-check 仍能回答。
func buildFallbackContextItems(hits []logicdomain.MemoryHit) []logicdomain.ContextItem {
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

// buildFallbackContextSummary renders the final fallback context text with the same deterministic section order used by the shared assembler.
// buildFallbackContextSummary 用于按与共享 assembler 相同的确定性 section 顺序渲染最终回退上下文文本。
func buildFallbackContextSummary(items []logicdomain.ContextItem) string {
	if len(items) == 0 {
		return ""
	}
	order := []struct{ Kind, Title string }{
		{Kind: "memory", Title: "混合召回记忆"},
	}
	var b strings.Builder
	firstSection := true
	for _, group := range order {
		section := make([]logicdomain.ContextItem, 0)
		for _, item := range items {
			if item.Kind == group.Kind {
				section = append(section, item)
			}
		}
		if len(section) == 0 {
			continue
		}
		if !firstSection {
			b.WriteString("\n\n")
		}
		firstSection = false
		title := fallbackContextSectionTitle(group.Title, section)
		b.WriteString("[")
		b.WriteString(title)
		b.WriteString("]\n")
		for idx, item := range section {
			if item.Kind == "memory" {
				b.WriteString(fmt.Sprintf("%d. %s (score=%.3f)\n", idx+1, item.Text, item.Score))
				continue
			}
			b.WriteString("- ")
			b.WriteString(item.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// fallbackContextSectionTitle keeps fallback text and structured items aligned by preferring the section title already carried by the first item in that section.
// fallbackContextSectionTitle 用于优先复用分组里第一条 item 自带的标题，保证 fallback 文本和结构化条目在 section 命名上保持一致。
func fallbackContextSectionTitle(defaultTitle string, section []logicdomain.ContextItem) string {
	if len(section) == 0 {
		return defaultTitle
	}
	title := strings.TrimSpace(section[0].Title)
	if title == "" {
		return defaultTitle
	}
	return title
}
