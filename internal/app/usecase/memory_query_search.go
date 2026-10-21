// Memory query orchestration, request normalization and recall sizing.
// 应用用例层记忆查询编排、请求归一与召回数量配置。
package usecase

import (
	"context"
	"fmt"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/textutil"
	"strconv"
	"strings"
)

// Search resolves the concrete project/user scope, normalizes the simple query list, runs the configured retrieval stages, and returns enriched unified memory refs.
// Search 用于解析具体的 project/user 范围、规范化简单查询列表、执行已配置的检索阶段，并返回补全后的统一记忆引用。
func (u *MemoryUseCase) Search(ctx context.Context, cmd MemoryQueryCommand) (MemoryQueryResult, error) {
	if u == nil || u.profiles == nil {
		return MemoryQueryResult{}, fmt.Errorf("profile store is nil")
	}
	if u.memories == nil {
		return MemoryQueryResult{}, fmt.Errorf("memory store is nil")
	}
	if u.embedding == nil {
		return MemoryQueryResult{}, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return MemoryQueryResult{}, fmt.Errorf("vector store is nil")
	}
	if err := validateMemoryQueryCommand(cmd); err != nil {
		return MemoryQueryResult{}, err
	}

	// Resolve USER and PROJECT first so the memory search always runs inside one deterministic hierarchy scope.
	// 先解析 USER 和 PROJECT，保证记忆检索始终运行在一个确定性的层级范围内。
	userTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeUser, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return MemoryQueryResult{}, err
	}
	projectTarget, err := u.profiles.ResolveProfileTarget(ctx, logicdomain.ProfileTypeProject, cmd.UserID, cmd.ProjectID)
	if err != nil {
		return MemoryQueryResult{}, err
	}

	// Normalize the query list once, then collapse repeated variants so one request can reuse retrieval work without exposing duplicate pipeline cost to callers.
	// 先一次性规范化查询列表，再折叠重复变体，让单次请求可以复用检索工作，而不会把重复成本暴露给调用方。
	items, err := normalizeMemoryQueries(cmd.Queries)
	if err != nil {
		return MemoryQueryResult{}, err
	}
	keys := make([]string, len(items))
	uniqueItems := make([]MemoryQueryItem, 0, len(items))
	uniqueKeys := make([]string, 0, len(items))
	seenGroups := make(map[string]struct{}, len(items))
	for idx, item := range items {
		key := buildMemoryQueryCacheKey(item)
		keys[idx] = key
		if _, ok := seenGroups[key]; ok {
			continue
		}
		seenGroups[key] = struct{}{}
		uniqueKeys = append(uniqueKeys, key)
		uniqueItems = append(uniqueItems, item)
	}
	texts := make([]string, 0, len(uniqueItems))
	for _, item := range uniqueItems {
		texts = append(texts, buildMemorySearchText(item))
	}
	embedResp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Texts: texts})
	if err != nil {
		return MemoryQueryResult{}, err
	}
	if err := embedResp.ValidateStrict(len(uniqueItems)); err != nil {
		return MemoryQueryResult{}, err
	}

	// Keep vector recall inside the resolved project hierarchy and enrich the returned vector rows with relational memory refs.
	// 将向量召回限制在已解析项目层级内，并使用关系记忆行补全向量结果中的长期引用。
	filter := buildScopedMemorySearchFilter(userTarget, projectTarget, cmd.ScopeOverride)
	if cmd.SessionID > 0 {
		filter.SessionID = cmd.SessionID
	}
	filter.BoundarySessionID = cmd.BoundarySessionID
	filter.BoundaryMaxTurnID = cmd.BoundaryMaxTurnID
	filter.ExcludeBoundaryTurn = cmd.ExcludeBoundaryTurn
	topK := normalizeMemorySearchTopK(cmd.TopK)
	candidatePoolK := topK
	if u.hybridEnabled || u.reranker != nil || u.mmrEnabled {
		lexicalTopK := 0
		if u.hybridEnabled {
			lexicalTopK = u.lexicalTopK
		}
		rerankTopN := 0
		if u.reranker != nil {
			rerankTopN = u.rerankTopN
		}
		candidatePoolK = normalizeMemoryCandidatePoolK(topK, lexicalTopK, rerankTopN, u.mmrEnabled)
	}
	if cmd.EnableHardDedupePool {
		hardDedupeCandidatePoolK := u.configuredHardDedupePoolTopK(topK)
		if hardDedupeCandidatePoolK > candidatePoolK {
			candidatePoolK = hardDedupeCandidatePoolK
		}
	}
	cachedHits := make(map[string][]MemoryQueryHit, len(uniqueItems))
	cachedHardDedupeHits := make(map[string][]MemoryQueryHit, len(uniqueItems))
	cachedQueryVectors := make(map[string][]float32, len(uniqueItems))
	contextEdgeCache := newMemoryContextEdgeCache()
	for idx, item := range uniqueItems {
		queryText := buildMemorySearchText(item)
		hits, usedCombinedHybridSQL, err := u.searchMemoryFirstStage(ctx, item, embedResp.Vectors[idx], candidatePoolK, filter)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		if usedCombinedHybridSQL {
			hits = normalizeCombinedHybridSQLHits(hits)
		}
		mapped, err := u.mapSearchHits(ctx, hits)
		if err != nil {
			return MemoryQueryResult{}, err
		}
		mapped = clampMemoryQueryHitScores(mapped)
		stageMessage := "memory search vector stage completed"
		if usedCombinedHybridSQL {
			stageMessage = "memory search first-stage combined sql completed"
		}
		u.logMemorySearchStage(ctx, stageMessage, queryText, []any{
			"query_index", idx,
			"candidate_pool_k", candidatePoolK,
			"vector_hit_count", len(hits),
			"mapped_hit_count", len(mapped),
			"combined_hybrid_sql_used", usedCombinedHybridSQL,
			"hybrid_enabled", u.hybridEnabled,
			"rerank_enabled", u.reranker != nil,
			"mmr_enabled", u.mmrEnabled,
		}, map[string]any{
			"query_index":    idx,
			"query":          item.Query,
			"top_vector_hit": summarizeRawMemoryHitForLog(firstRawMemoryHit(hits)),
			"top_mapped_hit": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(mapped)),
		})
		if usedCombinedHybridSQL {
			mapped = trimSearchHits(mapped, candidatePoolK)
		} else {
			mapped = u.hybridizeSearchHits(ctx, item, filter, candidatePoolK, mapped)
		}
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.rerankSearchHits(ctx, queryText, mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.applyWeibullDecaySearchHits(mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		mapped = u.applyContextEvidenceScoring(ctx, item, mapped, contextEdgeCache)
		mapped = clampMemoryQueryHitScores(mapped)

		// Capture a dedicated hard-dedupe window before MMR and final top-k trimming so the duplicate shortcut can still inspect near-identical memories that diversity control intentionally pushes out of reviewer-visible results.
		// 在 MMR 和最终 top-k 截断之前截取一份独立的硬排重窗口，这样即便多样性控制故意把近重复旧记忆挤出 reviewer 可见结果，硬排重捷径仍能扫描到它们。
		hardDedupePoolTopK := topK
		if cmd.EnableHardDedupePool {
			hardDedupePoolTopK = u.effectiveHardDedupePoolTopK(topK, candidatePoolK)
		}
		hardDedupeHits := trimSearchHits(mapped, hardDedupePoolTopK)

		mapped = u.applyMMRSearchHits(topK, mapped)
		mapped = clampMemoryQueryHitScores(mapped)
		u.logMemorySearchStage(ctx, "memory search final stage completed", queryText, []any{
			"query_index", idx,
			"final_hit_count", len(mapped),
			"top_k", topK,
			"hard_dedupe_pool_top_k", hardDedupePoolTopK,
			"hard_dedupe_pool_hit_count", len(hardDedupeHits),
		}, map[string]any{
			"query_index":         idx,
			"query":               item.Query,
			"top_final_hit":       summarizeMemoryQueryHitForLog(firstMemoryQueryHit(mapped)),
			"top_hard_dedupe_hit": summarizeMemoryQueryHitForLog(firstMemoryQueryHit(hardDedupeHits)),
		})
		cachedHits[uniqueKeys[idx]] = cloneMemoryQueryHits(trimSearchHits(mapped, topK))
		cachedHardDedupeHits[uniqueKeys[idx]] = cloneMemoryQueryHits(hardDedupeHits)
		cachedQueryVectors[uniqueKeys[idx]] = cloneFloat32Slice(embedResp.Vectors[idx])
	}
	results := make([]MemoryQueryGroupResult, 0, len(items))
	for idx, item := range items {
		results = append(results, MemoryQueryGroupResult{
			QueryIndex:     idx,
			Query:          item.Query,
			QueryVector:    cloneFloat32Slice(cachedQueryVectors[keys[idx]]),
			Hits:           cloneMemoryQueryHits(cachedHits[keys[idx]]),
			HardDedupeHits: cloneMemoryQueryHits(cachedHardDedupeHits[keys[idx]]),
		})
	}
	return MemoryQueryResult{
		UserTarget:    userTarget,
		ProjectTarget: projectTarget,
		Results:       results,
	}, nil
}

// configuredHardDedupePoolTopK returns the configured reviewer-front hard-dedupe window after clamping it into the shared search top-k bounds and keeping it no smaller than the reviewer-visible top-k.
// configuredHardDedupePoolTopK 用于返回配置后的 reviewer 前硬排重窗口；它会被钳制到共享搜索上限内，并保证不会小于 reviewer 可见的 top-k。
func (u *MemoryUseCase) configuredHardDedupePoolTopK(topK int) int {
	poolTopK := u.hardDedupePoolTopK
	if poolTopK <= 0 {
		poolTopK = 16
	}
	if poolTopK < topK {
		poolTopK = topK
	}
	return normalizeMemorySearchTopK(poolTopK)
}

// effectiveHardDedupePoolTopK expands the reviewer top-k into a larger but bounded pre-review scan window so hard dedupe can inspect more durable memories than the reviewer sees, without exceeding the first-stage candidate pool.
// effectiveHardDedupePoolTopK 用于把 reviewer 的 top-k 扩展成一个更大但受限的 reviewer 前扫描窗口，让硬排重能查看比 reviewer 更多的长期记忆，同时又不超过一阶段候选池。
func (u *MemoryUseCase) effectiveHardDedupePoolTopK(topK, candidatePoolK int) int {
	poolTopK := u.configuredHardDedupePoolTopK(topK)
	if candidatePoolK > 0 && poolTopK > candidatePoolK {
		poolTopK = candidatePoolK
	}
	return normalizeMemorySearchTopK(poolTopK)
}

// searchMemoryFirstStage selects the fastest safe first-stage retrieval path, preferring one SQL-level hybrid fusion query when the backing vector store explicitly supports combined PostgreSQL search.
// searchMemoryFirstStage 用于选择最快且安全的一阶段召回路径；当向量存储显式支持 PostgreSQL 组合检索时，优先走单条 SQL 的混合融合查询。
func (u *MemoryUseCase) searchMemoryFirstStage(ctx context.Context, item MemoryQueryItem, vector []float32, poolK int, filter logicdomain.SearchFilter) ([]logicdomain.MemoryHit, bool, error) {
	if u == nil || u.vector == nil {
		return nil, false, fmt.Errorf("vector store is nil")
	}
	query := buildMemoryLexicalQuery(item)
	if u.hybridEnabled && strings.TrimSpace(query) != "" {
		if hybridStore, ok := u.vector.(hybridVectorSearchStore); ok {
			hits, err := hybridStore.SearchHybridMemory(ctx, query, vector, poolK, filter, u.rrfK)
			if err == nil {
				return hits, true, nil
			}
			if u.logger != nil {
				u.logger.Warn("memory combined hybrid search degraded", append(memoryQueryLogFields(u.logger, query), "err", err)...)
			}
		}
	}
	hits, err := u.vector.Search(ctx, vector, poolK, filter)
	return hits, false, err
}

// validateMemoryQueryCommand checks the simple query-list request before any hierarchy, embedding, or vector work begins.
// validateMemoryQueryCommand 用于在任何层级解析、embedding 或向量检索开始前校验简单查询列表请求。
func validateMemoryQueryCommand(cmd MemoryQueryCommand) error {
	if cmd.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if cmd.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if len(cmd.Queries) == 0 {
		return logicdomain.ValidationError{Field: "queries", Message: "must contain at least one query"}
	}
	if len(cmd.Queries) > maxMemoryQueryItems {
		return logicdomain.ValidationError{Field: "queries", Message: fmt.Sprintf("must contain at most %d queries", maxMemoryQueryItems)}
	}
	if cmd.TopK < 0 {
		return logicdomain.ValidationError{Field: "top_k", Message: "must be >= 0"}
	}
	if scope := strings.TrimSpace(cmd.ScopeOverride); scope != "" && !isSupportedMemoryQueryScope(scope) {
		return logicdomain.ValidationError{Field: "scope_override", Message: "must be session, team, space, or project when set"}
	}
	if normalizeConfigToken(cmd.ScopeOverride) == memoryReplaceScopeSession && cmd.SessionID == 0 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must be > 0 when scope_override=session"}
	}
	return nil
}

func normalizeMemoryQueries(queries []string) ([]MemoryQueryItem, error) {
	if len(queries) == 0 {
		return nil, logicdomain.ValidationError{Field: "queries", Message: "must contain at least one query"}
	}
	if len(queries) > maxMemoryQueryItems {
		return nil, logicdomain.ValidationError{Field: "queries", Message: fmt.Sprintf("must contain at most %d queries", maxMemoryQueryItems)}
	}
	items := make([]MemoryQueryItem, 0, len(queries))
	for idx, query := range queries {
		query = textutil.NormalizeWhitespace(query)
		if query == "" {
			return nil, logicdomain.ValidationError{Field: "queries[" + strconv.Itoa(idx) + "]", Message: "is required"}
		}
		items = append(items, MemoryQueryItem{Query: query})
	}
	return items, nil
}

// buildMemorySearchText keeps the embedding input aligned with the simplified AI-facing contract by using the normalized query text directly.
// buildMemorySearchText 用于让 embedding 输入与简化后的 AI 接口契约保持一致，直接使用规范化查询文本。
func buildMemorySearchText(item MemoryQueryItem) string {
	return strings.TrimSpace(item.Query)
}

// buildMemoryQueryCacheKey converts one normalized query into a stable in-request cache key so repeated queries can reuse one retrieval execution without changing the caller-facing result shape.
// buildMemoryQueryCacheKey 用于把一条已归一的 query 转成稳定的单请求缓存键，让重复 query 能复用一次检索执行，同时不改变调用方看到的结果结构。
func buildMemoryQueryCacheKey(item MemoryQueryItem) string {
	return normalizeMemoryQueryCachePart(item.Query)
}

// normalizeMemoryQueryCachePart keeps the in-request dedupe key aligned with pre-check's low-risk query normalization so case-only variants do not fan out into duplicate retrieval work.
// normalizeMemoryQueryCachePart 用于让单请求去重键与 pre-check 的低风险 query 归一保持一致，避免仅有大小写差异的变体再次扩散成重复检索开销。
func normalizeMemoryQueryCachePart(text string) string {
	return strings.ToLower(textutil.NormalizeWhitespace(text))
}

// cloneMemoryQueryHits deep-copies one hit slice before it is shared across repeated groups so later callers cannot accidentally mutate another group's cached result.
// cloneMemoryQueryHits 用于在重复 group 复用结果前深拷贝命中切片，避免后续调用方意外修改到另一组共享的缓存结果。
func cloneMemoryQueryHits(hits []MemoryQueryHit) []MemoryQueryHit {
	if len(hits) == 0 {
		return nil
	}
	cloned := make([]MemoryQueryHit, 0, len(hits))
	for _, hit := range hits {
		copyHit := hit
		copyHit.Vector = append([]float32(nil), hit.Vector...)
		copyHit.MatchedContextValues = append([]string(nil), hit.MatchedContextValues...)
		cloned = append(cloned, copyHit)
	}
	return cloned
}

// cloneFloat32Slice copies one float32 vector so repeated query groups can safely reuse cached embeddings without sharing backing arrays across callers.
// cloneFloat32Slice 用于复制一条 float32 向量，让重复 query group 可以安全复用缓存 embedding，而不会与调用方共享底层数组。
func cloneFloat32Slice(values []float32) []float32 {
	if len(values) == 0 {
		return nil
	}
	return append([]float32(nil), values...)
}

// normalizeMemorySearchTopK applies the RPC defaults and caps so callers can omit top_k safely without creating oversized result sets.
// normalizeMemorySearchTopK 用于施加 RPC 默认值和上限，让调用方即使省略 top_k 也不会生成过大的结果集。
func normalizeMemorySearchTopK(topK int) int {
	if topK <= 0 {
		return defaultMemorySearchTopK
	}
	if topK > MaxMemorySearchTopK {
		return MaxMemorySearchTopK
	}
	return topK
}

// normalizeMemoryCandidatePoolK keeps the mixed-recall candidate pool large enough for lexical fusion, rerank, and optional MMR without breaking the repo-wide RPC cap.
// normalizeMemoryCandidatePoolK 用于让混合召回候选池足够承载 lexical 融合、rerank 和可选 MMR，同时继续遵守仓库级 RPC 上限。
func normalizeMemoryCandidatePoolK(finalTopK, lexicalTopK, rerankTopN int, mmrEnabled bool) int {
	poolK := finalTopK
	if lexicalTopK > poolK {
		poolK = lexicalTopK
	}
	if rerankTopN > poolK {
		poolK = rerankTopN
	}
	if mmrEnabled {
		mmrPoolK := finalTopK * 2
		if mmrPoolK > poolK {
			poolK = mmrPoolK
		}
	}
	return normalizeMemorySearchTopK(poolK)
}
