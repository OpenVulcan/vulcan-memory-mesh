// Memory context evidence matching and per-query caching.
// 应用用例层记忆情境证据匹配与请求内缓存。
package usecase

import (
	"context"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"math"
	"sort"
	"strings"
)

// memoryContextEdgeCache stores context edges already loaded during one Search request so overlapping candidate pools across different query groups do not repeatedly hit the relational store.
// memoryContextEdgeCache 用于缓存单次 Search 请求内已经读取过的 context edges，避免不同 query 分组命中重叠候选池时反复访问关系存储。
type memoryContextEdgeCache struct {
	// edgesByMemoryID stores raw context edges keyed by durable memory id for the current Search request.
	// edgesByMemoryID 用于按长期 memory id 缓存当前 Search 请求已读取的原始 context edges。
	edgesByMemoryID map[uint64][]logicdomain.MemoryContextEdge
}

// newMemoryContextEdgeCache creates an empty request-scoped context-edge cache; it takes no parameters and returns a cache ready for Search pipeline reuse.
// newMemoryContextEdgeCache 用于创建空的请求级 context-edge 缓存；它不接收参数，并返回可供 Search 链路复用的缓存实例。
func newMemoryContextEdgeCache() *memoryContextEdgeCache {
	return &memoryContextEdgeCache{
		edgesByMemoryID: make(map[uint64][]logicdomain.MemoryContextEdge),
	}
}

// load returns context edges for memoryIDs by reusing cached rows and loading only unseen ids from store; ctx carries cancellation, store is the relational memory port, and the return value preserves the caller's memory-id order.
// load 会复用缓存行并只从 store 加载尚未见过的 memoryIDs；ctx 负责取消控制，store 是关系记忆端口，返回值按调用方传入的 memory id 顺序汇总。
func (c *memoryContextEdgeCache) load(ctx context.Context, store appports.MemoryStore, memoryIDs []uint64) ([]logicdomain.MemoryContextEdge, error) {
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	missingIDs := make([]uint64, 0, len(memoryIDs))
	missingSet := make(map[uint64]struct{}, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		if _, ok := c.edgesByMemoryID[memoryID]; ok {
			continue
		}
		missingIDs = append(missingIDs, memoryID)
		missingSet[memoryID] = struct{}{}
	}
	if len(missingIDs) > 0 {
		loaded, err := store.LoadMemoryContextEdgesByMemoryIDs(ctx, missingIDs)
		if err != nil {
			return nil, err
		}
		loadedByID := make(map[uint64][]logicdomain.MemoryContextEdge, len(missingIDs))
		for _, memoryID := range missingIDs {
			loadedByID[memoryID] = nil
		}
		for _, edge := range loaded {
			if _, ok := missingSet[edge.MemoryID]; !ok {
				continue
			}
			loadedByID[edge.MemoryID] = append(loadedByID[edge.MemoryID], edge)
		}
		for _, memoryID := range missingIDs {
			c.edgesByMemoryID[memoryID] = loadedByID[memoryID]
		}
	}
	edges := make([]logicdomain.MemoryContextEdge, 0)
	for _, memoryID := range memoryIDs {
		edges = append(edges, c.edgesByMemoryID[memoryID]...)
	}
	return edges, nil
}

// applyContextEvidenceScoring loads contextual evidence edges for the current candidate pool and applies a soft boost or demotion when the normalized query explicitly matches those situations.
// applyContextEvidenceScoring 用于为当前候选池加载情境证据边，并在规范化 query 明确命中这些场景时做软提升或软降权。
func (u *MemoryUseCase) applyContextEvidenceScoring(ctx context.Context, item MemoryQueryItem, hits []MemoryQueryHit, contextEdgeCache *memoryContextEdgeCache) []MemoryQueryHit {
	if u == nil || u.memories == nil || len(hits) == 0 {
		return hits
	}
	signals := buildMemoryQueryContextSignals(item)
	if len(signals.Phrases) == 0 {
		return hits
	}
	memoryIDs := collectMemoryQueryHitIDs(hits)
	if len(memoryIDs) == 0 {
		return hits
	}
	edges, err := contextEdgeCache.load(ctx, u.memories, memoryIDs)
	if err != nil {
		if u.logger != nil {
			u.logger.Warn("memory context evidence scoring degraded", "candidate_count", len(memoryIDs), "err", err)
		}
		return hits
	}
	if len(edges) == 0 {
		return hits
	}

	// Aggregate only the edges whose context values explicitly match the normalized query phrases so unrelated support stats do not leak into the current ranking.
	// 只聚合那些与规范化 query 短语明确匹配的 edge，避免无关情境的支持统计污染当前排序。
	matchedEvidence := make(map[uint64]memoryContextEvidenceScore, len(memoryIDs))
	for _, edge := range edges {
		if !signals.Match(edge.ContextValue) {
			continue
		}
		score := matchedEvidence[edge.MemoryID]
		score.SupportCount += edge.SupportCount
		score.RebuttalCount += edge.RebuttalCount
		score.MatchedContextValues = appendUniqueMemoryContextEvidenceValue(score.MatchedContextValues, edge.ContextKey, edge.ContextValue)
		matchedEvidence[edge.MemoryID] = score
	}
	if len(matchedEvidence) == 0 {
		return hits
	}

	// Apply matched evidence even for a single candidate because the fields feed reviewer explanations, not only multi-hit ordering.
	// 即使只有一条候选也要应用命中证据，因为这些字段不仅用于多候选排序，也会进入 reviewer 解释。
	scored := append([]MemoryQueryHit(nil), hits...)
	evidenceByID := make(map[uint64]memoryContextEvidenceScore, len(matchedEvidence))
	for idx := range scored {
		evidence := matchedEvidence[scored[idx].MemoryRef.ID]
		if evidence.SupportCount == 0 && evidence.RebuttalCount == 0 {
			continue
		}
		evidence.Delta = computeMemoryContextEvidenceDelta(evidence)
		sort.Strings(evidence.MatchedContextValues)
		scored[idx].Score += evidence.Delta
		scored[idx].MatchedContextValues = append([]string(nil), evidence.MatchedContextValues...)
		scored[idx].MatchedContextSupportCount = evidence.SupportCount
		scored[idx].MatchedContextRebuttalCount = evidence.RebuttalCount
		scored[idx].MatchedContextScoreDelta = evidence.Delta
		evidenceByID[scored[idx].MemoryRef.ID] = evidence
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		left := evidenceByID[scored[i].MemoryRef.ID]
		right := evidenceByID[scored[j].MemoryRef.ID]
		leftNet := left.SupportCount - left.RebuttalCount
		rightNet := right.SupportCount - right.RebuttalCount
		if leftNet != rightNet {
			return leftNet > rightNet
		}
		if left.SupportCount != right.SupportCount {
			return left.SupportCount > right.SupportCount
		}
		if left.RebuttalCount != right.RebuttalCount {
			return left.RebuttalCount < right.RebuttalCount
		}
		return false
	})
	return scored
}

// memoryContextEvidenceScore stores the matched support/rebuttal totals for one candidate memory under the current query situation.
// memoryContextEvidenceScore 用于保存当前 query 场景下某个候选记忆匹配到的支持/反驳总量。
type memoryContextEvidenceScore struct {
	SupportCount         int
	RebuttalCount        int
	MatchedContextValues []string
	Delta                float64
}

// memoryQueryContextSignals stores the normalized phrases extracted from the query so context edges can do deterministic lexical matching without another model call.
// memoryQueryContextSignals 用于保存从 query 提取出的规范化短语，让 context edge 可以在不增加额外模型调用的情况下做确定性匹配。
type memoryQueryContextSignals struct {
	// Text stores the normalized full query so long context values can match as bounded phrases instead of being limited by the short n-gram window.
	// Text 用于保存规范化后的完整 query，让较长的 context value 可以按边界短语匹配，而不是受短 n-gram 窗口限制。
	Text string
	// Phrases stores exact normalized tokens and short n-grams for fast context-value matching.
	// Phrases 用于保存规范化后的 token 和短 n-gram，便于快速匹配 context value。
	Phrases map[string]struct{}
}

// Match reports whether one normalized contextual value appears in the extracted phrase set for the current query item.
// Match 用于判断某个规范化情境值是否出现在当前 query item 提取出的短语集合中。
func (s memoryQueryContextSignals) Match(value string) bool {
	normalizedValue := normalizeMemoryContextMatchText(value)
	if normalizedValue == "" {
		return false
	}
	if _, ok := s.Phrases[normalizedValue]; ok {
		return true
	}
	return containsMemoryContextPhrase(s.Text, normalizedValue)
}

// buildMemoryQueryContextSignals extracts deterministic phrases and short n-grams from the current query item so contextual retrieval can align memory edges with user intent.
// buildMemoryQueryContextSignals 用于从当前 query item 提取确定性的短语和短 n-gram，让情境检索可以把记忆边与用户意图对齐。
func buildMemoryQueryContextSignals(item MemoryQueryItem) memoryQueryContextSignals {
	phrases := make(map[string]struct{})
	appendPhrase := func(value string) {
		value = normalizeMemoryContextMatchText(value)
		if value == "" {
			return
		}
		phrases[value] = struct{}{}
	}
	appendText := func(text string) {
		appendPhrase(text)
		tokens := textutil.Tokenize(text)
		for _, token := range tokens {
			appendPhrase(token)
		}
		for windowSize := 2; windowSize <= 4; windowSize++ {
			for start := 0; start+windowSize <= len(tokens); start++ {
				appendPhrase(strings.Join(tokens[start:start+windowSize], " "))
			}
		}
	}
	appendText(item.Query)
	return memoryQueryContextSignals{
		Text:    normalizeMemoryContextMatchText(item.Query),
		Phrases: phrases,
	}
}

// normalizeMemoryContextMatchText normalizes a contextual phrase into the same lexical surface used by query-time matching and edge values.
// normalizeMemoryContextMatchText 用于把情境短语归一成查询期匹配和 edge 值共享的词法表面形式。
func normalizeMemoryContextMatchText(raw string) string {
	return logicdomain.NormalizeMemoryContextValue(raw)
}

// containsMemoryContextPhrase reports whether phrase appears in text with whitespace boundaries after both strings have already been normalized onto the context-value surface.
// containsMemoryContextPhrase 用于判断 phrase 是否以空白边界出现在 text 中；调用方需要先把两者归一到 context-value 词法表面。
func containsMemoryContextPhrase(text, phrase string) bool {
	if text == "" || phrase == "" {
		return false
	}
	if text == phrase {
		return true
	}
	return strings.Contains(" "+text+" ", " "+phrase+" ")
}

// computeMemoryContextEvidenceDelta converts matched support/rebuttal counts into one bounded score delta so context evidence influences ranking without dominating the whole retrieval pipeline.
// computeMemoryContextEvidenceDelta 用于把匹配到的支持/反驳计数转换成一个有界分数增量，让情境证据影响排序但不垄断整条检索链。
func computeMemoryContextEvidenceDelta(evidence memoryContextEvidenceScore) float64 {
	supportBoost := math.Min(0.12, 0.025*float64(evidence.SupportCount))
	rebuttalPenalty := math.Min(0.14, 0.03*float64(evidence.RebuttalCount))
	return supportBoost - rebuttalPenalty
}

// appendUniqueMemoryContextEvidenceValue keeps the matched context summary deterministic and de-duplicated so upper layers can surface a compact explanation of why a memory matched.
// appendUniqueMemoryContextEvidenceValue 用于保持命中的 context 摘要确定且去重，方便上层输出“这条记忆为什么命中”的紧凑说明。
func appendUniqueMemoryContextEvidenceValue(values []string, key, value string) []string {
	key = logicdomain.NormalizeMemoryContextKey(key)
	value = logicdomain.NormalizeMemoryContextValue(value)
	if value == "" {
		return values
	}
	label := value
	if key != "" {
		label = key + "=" + value
	}
	for _, existing := range values {
		if logicdomain.NormalizeMemoryContextEvidenceLabel(existing) == label {
			return values
		}
	}
	return append(values, label)
}

// collectMemoryQueryHitIDs returns the distinct durable memory ids present in one candidate slice so query-time enrichment can batch-load relational evidence once.
// collectMemoryQueryHitIDs 用于返回候选切片中的去重长期 memory id，方便查询期补全一次性批量加载关系证据。
func collectMemoryQueryHitIDs(hits []MemoryQueryHit) []uint64 {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(hits))
	ids := make([]uint64, 0, len(hits))
	for _, hit := range hits {
		if hit.MemoryRef.ID == 0 {
			continue
		}
		if _, ok := seen[hit.MemoryRef.ID]; ok {
			continue
		}
		seen[hit.MemoryRef.ID] = struct{}{}
		ids = append(ids, hit.MemoryRef.ID)
	}
	return ids
}
