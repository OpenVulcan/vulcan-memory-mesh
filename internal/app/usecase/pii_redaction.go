// pii_redaction.go implements shared PII redaction helpers used by pre-check, post-action, and direct-memory writes before they hand text to storage- or LLM-facing stages.
// pii_redaction.go 用于实现 pre-check、post-action 与主动写记忆共享的 PII 脱敏辅助能力，确保文本在进入存储侧或 LLM 侧阶段前先完成脱敏。
package usecase

import logicdomain "github.com/openvulcan/vmm/internal/logic/domain"

// PIIScrubber defines the narrow text-only masking capability injected into live use cases so runtime composition can reuse the shared PII engine without leaking engine-specific APIs into the use-case layer.
// PIIScrubber 用于定义注入到运行时用例层的最小文本脱敏能力，让装配层可以复用共享 PII 引擎，同时不把引擎细节 API 泄漏到用例层。
type PIIScrubber interface {
	Scrub(text string) string
}

// scrubPIIText masks one text fragment when the runtime has wired a scrubber, while preserving legacy behavior in tests or partial constructions that do not provide one.
// scrubPIIText 用于在运行时已装配脱敏器时对单段文本执行脱敏；若测试或部分装配场景未提供脱敏器，则保留原有行为。
func scrubPIIText(scrubber PIIScrubber, text string) string {
	if scrubber == nil || text == "" {
		return text
	}
	return scrubber.Scrub(text)
}

// scrubWriteMemoryItemsPII redacts direct-write memory items before soft idempotency, embedding, and relational persistence begin, so tool-authored memories obey the same runtime masking boundary as post-action writes.
// scrubWriteMemoryItemsPII 用于在主动写记忆进入软幂等、embedding 与关系持久化前先完成脱敏，确保工具写入记忆与 post-action 写入共享同一条运行时脱敏边界。
func scrubWriteMemoryItemsPII(scrubber PIIScrubber, items []WriteMemoryItem) []WriteMemoryItem {
	if len(items) == 0 {
		return items
	}
	out := append([]WriteMemoryItem(nil), items...)
	for idx := range out {
		out[idx].Abstract = scrubPIIText(scrubber, out[idx].Abstract)
		out[idx].Details = scrubPIIText(scrubber, out[idx].Details)
	}
	return out
}

// scrubPreCheckTurnContextsPII redacts the recent-turn window exposed to the first-stage pre-check LLM so both current input and historical fragments obey the same masking policy.
// scrubPreCheckTurnContextsPII 用于对暴露给 pre-check 第一层 LLM 的最近 turn 窗口做脱敏，确保当前输入与历史片段遵守同一套掩码策略。
func scrubPreCheckTurnContextsPII(scrubber PIIScrubber, contexts []logicdomain.PreCheckTurnContext) []logicdomain.PreCheckTurnContext {
	if len(contexts) == 0 {
		return contexts
	}
	out := append([]logicdomain.PreCheckTurnContext(nil), contexts...)
	for idx := range out {
		out[idx].Content = scrubPIIText(scrubber, out[idx].Content)
	}
	return out
}

// scrubPreCheckMemoryCandidatesPII redacts recalled memory candidates before the reviewer and final assembler see them, so injected context never re-exposes stored secrets back into the live request path.
// scrubPreCheckMemoryCandidatesPII 用于在评审器和最终组装器看到候选前先完成记忆候选脱敏，避免已存量密文再次沿实时请求链路回流。
func scrubPreCheckMemoryCandidatesPII(scrubber PIIScrubber, candidates []logicdomain.PreCheckMemoryCandidate) []logicdomain.PreCheckMemoryCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	out := append([]logicdomain.PreCheckMemoryCandidate(nil), candidates...)
	for idx := range out {
		out[idx].Abstract = scrubPIIText(scrubber, out[idx].Abstract)
		out[idx].Details = scrubPIIText(scrubber, out[idx].Details)
		if len(out[idx].MatchedContextValues) == 0 {
			continue
		}
		out[idx].MatchedContextValues = append([]string(nil), out[idx].MatchedContextValues...)
		for valueIdx := range out[idx].MatchedContextValues {
			out[idx].MatchedContextValues[valueIdx] = scrubPIIText(scrubber, out[idx].MatchedContextValues[valueIdx])
		}
	}
	return out
}

// preCheckLogPreviewPIICache keeps per-request redacted memory previews for diagnostic logs so repeated query groups do not re-scrub the same durable memory text.
// preCheckLogPreviewPIICache 用于在单次请求内缓存诊断日志用的已脱敏记忆预览，避免重复 query group 对同一条长期记忆正文反复脱敏。
type preCheckLogPreviewPIICache struct {
	// scrubber masks raw preview text before it can enter stage payload logs.
	// scrubber 用于在预览文本进入阶段载荷日志前执行掩码。
	scrubber PIIScrubber
	// previewsByMemoryID stores redacted abstract/details pairs keyed by durable memory id.
	// previewsByMemoryID 用于按长期 memory id 缓存已脱敏的 abstract/details 预览对。
	previewsByMemoryID map[uint64]preCheckLogPreview
}

// preCheckLogPreview stores the redacted memory preview fields shared by raw-recall and threshold-debug log payloads.
// preCheckLogPreview 用于保存 raw-recall 与 threshold-debug 日志载荷共享的已脱敏记忆预览字段。
type preCheckLogPreview struct {
	Abstract       string
	DetailsPreview string
}

// newPreCheckLogPreviewPIICache creates one request-scoped log-preview redaction cache.
// newPreCheckLogPreviewPIICache 用于创建一个请求级日志预览脱敏缓存。
func newPreCheckLogPreviewPIICache(scrubber PIIScrubber) *preCheckLogPreviewPIICache {
	return &preCheckLogPreviewPIICache{
		scrubber:           scrubber,
		previewsByMemoryID: make(map[uint64]preCheckLogPreview),
	}
}

// scrubMemoryPreview returns redacted abstract/details text, reusing cached values when the same durable memory appears in multiple diagnostic payloads.
// scrubMemoryPreview 用于返回已脱敏的 abstract/details 文本，并在同一条长期记忆出现在多个诊断载荷中时复用缓存值。
func (c *preCheckLogPreviewPIICache) scrubMemoryPreview(memoryID uint64, abstract, detailsPreview string) preCheckLogPreview {
	if c == nil {
		return preCheckLogPreview{Abstract: abstract, DetailsPreview: detailsPreview}
	}
	if memoryID > 0 {
		if preview, ok := c.previewsByMemoryID[memoryID]; ok {
			return preview
		}
	}
	preview := preCheckLogPreview{
		Abstract:       scrubPIIText(c.scrubber, abstract),
		DetailsPreview: scrubPIIText(c.scrubber, detailsPreview),
	}
	if memoryID > 0 {
		c.previewsByMemoryID[memoryID] = preview
	}
	return preview
}

// scrubPreCheckThresholdHitLogPII redacts memory previews that only exist for threshold-debug logging, so diagnostic logs stay aligned with the same masking contract as reviewer-facing candidates.
// scrubPreCheckThresholdHitLogPII 用于对仅服务于阈值调试日志的记忆预览做脱敏，确保诊断日志与 reviewer 可见候选保持同一掩码契约。
func scrubPreCheckThresholdHitLogPII(cache *preCheckLogPreviewPIICache, payload *preCheckThresholdHitLogPayload) *preCheckThresholdHitLogPayload {
	if payload == nil {
		return nil
	}
	cloned := *payload
	preview := cache.scrubMemoryPreview(cloned.MemoryID, cloned.Abstract, cloned.DetailsPreview)
	cloned.Abstract = preview.Abstract
	cloned.DetailsPreview = preview.DetailsPreview
	return &cloned
}

// scrubPreCheckRawRecallGroupLogsPII redacts raw recall snapshots before they are written into stage logs, preventing debug payloads from bypassing the same masking boundary enforced for later reviewer inputs.
// scrubPreCheckRawRecallGroupLogsPII 用于在原始召回快照写入阶段日志前完成脱敏，避免调试载荷绕过后续 reviewer 输入所遵守的同一脱敏边界。
func scrubPreCheckRawRecallGroupLogsPII(cache *preCheckLogPreviewPIICache, groups []preCheckRawRecallGroupLogPayload) []preCheckRawRecallGroupLogPayload {
	if len(groups) == 0 {
		return groups
	}
	out := append([]preCheckRawRecallGroupLogPayload(nil), groups...)
	for idx := range out {
		if out[idx].TopRawHit == nil {
			continue
		}
		hit := *out[idx].TopRawHit
		preview := cache.scrubMemoryPreview(hit.MemoryID, hit.Abstract, hit.DetailsPreview)
		hit.Abstract = preview.Abstract
		hit.DetailsPreview = preview.DetailsPreview
		if cache == nil {
			hit.TextPreview = scrubPIIText(nil, hit.TextPreview)
		} else {
			hit.TextPreview = scrubPIIText(cache.scrubber, hit.TextPreview)
		}
		out[idx].TopRawHit = &hit
	}
	return out
}

// scrubPostActionCommandPII redacts both the canonical persistence payload and the raw shadow copy retained for downstream analyzer replay, so post-action never stores or analyzes unsanitized request text after the initial entry boundary.
// scrubPostActionCommandPII 用于同时脱敏 post-action 的标准持久化载荷和保留给后续分析重放的 raw 副本，确保越过入口边界后不会再存储或分析未脱敏文本。
func scrubPostActionCommandPII(scrubber PIIScrubber, cmd PostActionCommand) PostActionCommand {
	out := cmd
	out.UserContent = scrubPIIText(scrubber, cmd.UserContent)
	out.AssistantContent = scrubPIIText(scrubber, cmd.AssistantContent)
	out.RawUserContent = scrubPIIText(scrubber, cmd.RawUserContent)
	out.RawAssistantContent = scrubPIIText(scrubber, cmd.RawAssistantContent)
	if len(cmd.Timeline) > 0 {
		out.Timeline = append([]PostActionTimelineItem(nil), cmd.Timeline...)
		for idx := range out.Timeline {
			out.Timeline[idx].Content = scrubPIIText(scrubber, out.Timeline[idx].Content)
		}
	}
	if len(cmd.RawTimeline) > 0 {
		out.RawTimeline = append([]PostActionTimelineItem(nil), cmd.RawTimeline...)
		for idx := range out.RawTimeline {
			out.RawTimeline[idx].Content = scrubPIIText(scrubber, out.RawTimeline[idx].Content)
		}
	}
	return out
}
