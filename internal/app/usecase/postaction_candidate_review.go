// postaction_candidate_review.go implements the post-action first-pass admission filter, shared-scope memory dedupe recall, and unified candidate review flow.
// postaction_candidate_review.go 用于实现 post-action 的首轮准入过滤、共享范围记忆去重召回以及统一候选评审流程。
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postActionCompactionStats stores the raw/final candidate counts plus the main drop reasons so post-action can expose one stable compaction metric in logs.
// postActionCompactionStats 用于保存原始/最终候选计数以及主要丢弃原因，让 post-action 可以在日志里输出稳定的压缩指标。
type postActionCompactionStats struct {
	RawMemoryCandidates       int
	RawProfileCandidates      int
	FinalMemoryNodes          int
	FinalProfileNodes         int
	AdmissionDroppedCount     int
	ReviewDroppedCount        int
	ExternalResearchKeptCount int
}

// newPostActionCompactionStats captures the raw analyzer candidate counts before any first-pass or reviewer-side filtering starts.
// newPostActionCompactionStats 用于在任何首轮过滤或 reviewer 过滤开始前，记录分析器原始候选数量。
func newPostActionCompactionStats(analysis logicdomain.TurnAnalysis) postActionCompactionStats {
	return postActionCompactionStats{
		RawMemoryCandidates:  len(analysis.MemoryNodes),
		RawProfileCandidates: len(analysis.ProfileNodes),
	}
}

// RawCandidates returns the total number of raw memory plus profile candidates produced by the first analyzer pass.
// RawCandidates 用于返回首轮分析器产出的原始记忆与画像候选总数。
func (s postActionCompactionStats) RawCandidates() int {
	return s.RawMemoryCandidates + s.RawProfileCandidates
}

// FinalStoredNodes returns the total number of final memory plus profile nodes that survived every review stage and are ready for persistence.
// FinalStoredNodes 用于返回经过全部评审后仍准备落库的最终记忆与画像节点总数。
func (s postActionCompactionStats) FinalStoredNodes() int {
	return s.FinalMemoryNodes + s.FinalProfileNodes
}

// CompactionRate returns the fraction of raw candidates removed before persistence; empty candidate sets report zero to avoid divide-by-zero noise.
// CompactionRate 用于返回持久化前被移除的原始候选占比；当原始候选为空时返回零，避免无意义的除零噪声。
func (s postActionCompactionStats) CompactionRate() float64 {
	raw := s.RawCandidates()
	if raw <= 0 {
		return 0
	}
	return float64(raw-s.FinalStoredNodes()) / float64(raw)
}

// applyPostActionAdmissionFilter removes first-pass rejected QA echoes or non-durable candidates before later duplicate review and profile merge steps begin.
// applyPostActionAdmissionFilter 用于在后续去重与画像合并开始前，移除首轮已经拒绝的问答回显或非持久候选。
func applyPostActionAdmissionFilter(analysis *logicdomain.TurnAnalysis, stats *postActionCompactionStats) {
	if analysis == nil {
		return
	}

	// Filter memory candidates first so later duplicate review only sees nodes that already passed the analyzer's admission gate.
	// 先过滤记忆候选，确保后续去重评审只看到已经通过分析器准入闸门的节点。
	filteredMemory := make([]logicdomain.MemoryNodeCandidate, 0, len(analysis.MemoryNodes))
	for _, node := range analysis.MemoryNodes {
		if strings.TrimSpace(node.Admission) == logicdomain.TurnAnalysisAdmissionDrop {
			if stats != nil {
				stats.AdmissionDroppedCount++
			}
			continue
		}
		filteredMemory = append(filteredMemory, node)
	}
	analysis.MemoryNodes = filteredMemory

	// Filter profile candidates with the same admission rule so only durable, user-confirmed, or otherwise allowed evidence reaches later profile review.
	// 按同样的准入规则过滤画像候选，确保只有持久、被用户确认或其他允许的证据会进入后续画像评审。
	filteredProfiles := make([]logicdomain.ProfileNodeCandidate, 0, len(analysis.ProfileNodes))
	for _, node := range analysis.ProfileNodes {
		if strings.TrimSpace(node.Admission) == logicdomain.TurnAnalysisAdmissionDrop {
			if stats != nil {
				stats.AdmissionDroppedCount++
			}
			continue
		}
		filteredProfiles = append(filteredProfiles, node)
	}
	analysis.ProfileNodes = filteredProfiles
}

// reviewTurnCandidates runs the main unified candidate-review path so memory dedupe and profile acceptance can reuse one LLM call and one turn-level reasoning context.
// reviewTurnCandidates 用于执行统一候选评审主路径，让记忆去重与画像接纳共享一次 LLM 调用和同一轮语义上下文。
func (u *PostActionUseCase) reviewTurnCandidates(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord, analysis *logicdomain.TurnAnalysis, stats *postActionCompactionStats) error {
	if u == nil || analysis == nil {
		return nil
	}
	if len(analysis.MemoryNodes) == 0 && len(analysis.ProfileNodes) == 0 {
		if stats != nil {
			stats.ExternalResearchKeptCount = 0
		}
		return nil
	}
	if u.candidateReviewer == nil {
		return fmt.Errorf("post-action candidate reviewer is nil")
	}
	if len(analysis.MemoryNodes) > 0 && u.memorySearcher == nil {
		return fmt.Errorf("post-action memory searcher is nil")
	}

	batch := logicdomain.SessionBatchAnalysis{
		Turns: []logicdomain.SessionBatchTurnAnalysis{{
			TurnID:        turn.ID,
			Details:       analysis.Details,
			DetailsBudget: analysis.DetailsBudget,
			ProfileNodes:  append([]logicdomain.ProfileNodeCandidate(nil), analysis.ProfileNodes...),
		}},
	}
	turnRows := []logicdomain.SessionTurnRecord{{
		ID:        turn.ID,
		SessionID: turn.SessionID,
		ProjectID: turn.ProjectID,
		CreatedAt: choosePostActionCreatedAt(turn),
		UpdatedAt: turn.UpdatedAt,
	}}
	turnByID, stampedProfiles, userRefs, projectRefs := prepareSessionBatchProfileNodes(turnRows, &batch)

	snapshot := logicdomain.ProfileReviewTargetsSnapshot{}
	if len(stampedProfiles) > 0 {
		if u.store == nil {
			return fmt.Errorf("post-action relational store is nil")
		}
		var err error
		snapshot, err = u.store.LoadProfileReviewTargets(ctx, session)
		if err != nil {
			return fmt.Errorf("load profile review targets: %w", err)
		}
	}

	memoryCandidates, err := u.buildPostActionMemoryReviewCandidates(ctx, session, analysis.MemoryNodes)
	if err != nil {
		return err
	}
	reviewed, err := u.candidateReviewer.Review(ctx, logicdomain.PostActionCandidateReviewInput{
		UserInputKind:     strings.TrimSpace(analysis.UserInputKind),
		UserContent:       strings.TrimSpace(rawTurn.UserContent),
		AssistantContent:  strings.TrimSpace(rawTurn.AssistantContent),
		MemoryCandidates:  memoryCandidates,
		ProfileTargets:    snapshot,
		ProfileCandidates: stampedProfiles,
	})
	if err != nil {
		return err
	}

	keptMemoryNodes, reviewDroppedCount, err := applyPostActionMemoryReviewResult(analysis.MemoryNodes, reviewed.Memory)
	if err != nil {
		return err
	}
	analysis.MemoryNodes = keptMemoryNodes
	if stats != nil {
		stats.ReviewDroppedCount += reviewDroppedCount
	}

	if len(stampedProfiles) > 0 {
		if err := applyReviewedSessionBatchProfiles(&batch, turnByID, snapshot, userRefs, projectRefs, logicdomain.TurnProfileReviewResult{
			User:    reviewed.User,
			Project: reviewed.Project,
		}); err != nil {
			return err
		}
		analysis.ProfileNodes = clonePostActionProfileNodes(batch.Turns[0].ProfileNodes)
		analysis.UserProfileMerged = batch.UserProfileMerged
		analysis.MergedUserProfile = batch.MergedUserProfile
		analysis.ProjectProfileMerged = batch.ProjectProfileMerged
		analysis.MergedProjectProfile = batch.MergedProjectProfile
	} else {
		analysis.ProfileNodes = nil
		analysis.UserProfileMerged = false
		analysis.MergedUserProfile = ""
		analysis.ProjectProfileMerged = false
		analysis.MergedProjectProfile = ""
	}
	if stats != nil {
		stats.ExternalResearchKeptCount = countPostActionExternalResearchCandidates(*analysis)
	}
	return nil
}

// buildPostActionMemoryReviewCandidates recalls similar durable memories for each candidate inside the configured shared scope so the unified reviewer can judge semantic duplication.
// buildPostActionMemoryReviewCandidates 用于在配置好的共享作用域里为每条候选召回相似长期记忆，让统一 reviewer 判断语义重复。
func (u *PostActionUseCase) buildPostActionMemoryReviewCandidates(ctx context.Context, session logicdomain.SessionRef, nodes []logicdomain.MemoryNodeCandidate) ([]logicdomain.PostActionMemoryReviewCandidate, error) {
	candidates := make([]logicdomain.PostActionMemoryReviewCandidate, 0, len(nodes))
	if len(nodes) == 0 {
		return candidates, nil
	}
	queries := make([]string, 0, len(nodes))
	queryCandidateIndexes := make([]int, 0, len(nodes))
	for idx, node := range nodes {
		candidate := logicdomain.PostActionMemoryReviewCandidate{
			CandidateIndex:  idx,
			Category:        node.Category,
			Abstract:        strings.TrimSpace(node.Abstract),
			Details:         strings.TrimSpace(node.Details),
			EvidenceSource:  strings.TrimSpace(node.EvidenceSource),
			AdmissionReason: strings.TrimSpace(node.AdmissionReason),
		}
		if candidate.Details == "" {
			candidate.Details = candidate.Abstract
		}
		candidates = append(candidates, candidate)
		query := buildPostActionMemorySearchQuery(node)
		if query == "" || u.memorySearcher == nil {
			continue
		}
		queries = append(queries, query)
		queryCandidateIndexes = append(queryCandidateIndexes, idx)
	}
	if len(queries) == 0 || u.memorySearcher == nil {
		return candidates, nil
	}
	result, err := u.memorySearcher.Search(ctx, MemoryQueryCommand{
		UserID:        session.UserID,
		ProjectID:     session.ProjectID,
		Queries:       queries,
		TopK:          u.analysisCfg.DedupeSearchTopK,
		ScopeOverride: u.analysisCfg.DedupeSearchScope,
	})
	if err != nil {
		return nil, fmt.Errorf("search post-action dedupe candidates: %w", err)
	}

	// Map each grouped search result back onto its original candidate index so the reviewer sees all similar memories grouped with the exact new node it is judging.
	// 把每组搜索结果映射回原始候选索引，确保 reviewer 看到的高相似旧记忆都挂在其对应的新节点下。
	for groupIdx, group := range result.Results {
		if groupIdx < 0 || groupIdx >= len(queryCandidateIndexes) {
			continue
		}
		candidateIndex := queryCandidateIndexes[groupIdx]
		if candidateIndex < 0 || candidateIndex >= len(candidates) {
			continue
		}
		similar := buildPostActionSimilarMemoryCandidates(group.Hits, u.analysisCfg.DedupeMinSimilarity)
		candidates[candidateIndex].SimilarMemories = similar
	}
	return candidates, nil
}

// buildPostActionMemorySearchQuery composes one stable free-text recall query from the abstract plus any materially richer details so dedupe search reuses the same semantic surface the reviewer will later inspect.
// buildPostActionMemorySearchQuery 用于从 abstract 和明显更丰富的 details 组合出稳定的自由文本召回查询，供去重检索复用 reviewer 看到的语义表面。
func buildPostActionMemorySearchQuery(node logicdomain.MemoryNodeCandidate) string {
	abstract := strings.TrimSpace(node.Abstract)
	details := strings.TrimSpace(node.Details)
	switch {
	case abstract == "":
		return details
	case details == "" || strings.EqualFold(abstract, details):
		return abstract
	case strings.Contains(strings.ToLower(details), strings.ToLower(abstract)):
		return details
	default:
		return abstract + "\n" + details
	}
}

// buildPostActionSimilarMemoryCandidates keeps only high-similarity durable memories and de-duplicates them by memory id before they reach the unified reviewer.
// buildPostActionSimilarMemoryCandidates 用于只保留高相似的长期记忆，并按 memory id 去重后再交给统一 reviewer。
func buildPostActionSimilarMemoryCandidates(hits []MemoryQueryHit, minSimilarity float64) []logicdomain.PostActionSimilarMemoryCandidate {
	if len(hits) == 0 {
		return nil
	}
	bestByMemoryID := make(map[uint64]logicdomain.PostActionSimilarMemoryCandidate, len(hits))
	for hitIdx, hit := range hits {
		if hit.MemoryRef.Type != logicdomain.MemoryRefTypeMemory || hit.MemoryRef.ID == 0 {
			continue
		}
		score := normalizePreCheckReviewScore(hit.Score, hit.Origin, hitIdx+1, len(hits))
		if score < minSimilarity {
			continue
		}
		abstract := strings.TrimSpace(hit.Abstract)
		details := strings.TrimSpace(hit.DetailsPreview)
		if abstract == "" && details == "" {
			continue
		}
		if details == "" {
			details = abstract
		}
		candidate := logicdomain.PostActionSimilarMemoryCandidate{
			MemoryID:     hit.MemoryRef.ID,
			SourceTurnID: hit.SourceRef.ID,
			ScopeLevel:   logicdomain.MemoryScopeLevelLabel(hit.ScopeLevel),
			Category:     hit.Category,
			Score:        score,
			Origin:       strings.TrimSpace(hit.Origin),
			Abstract:     abstract,
			Details:      details,
		}
		if existing, ok := bestByMemoryID[candidate.MemoryID]; ok {
			if candidate.Score <= existing.Score {
				continue
			}
		}
		bestByMemoryID[candidate.MemoryID] = candidate
	}
	out := make([]logicdomain.PostActionSimilarMemoryCandidate, 0, len(bestByMemoryID))
	for _, candidate := range bestByMemoryID {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	return out
}

// applyPostActionMemoryReviewResult keeps only accepted memory candidates in original order and returns how many nodes the reviewer dropped during dedupe review.
// applyPostActionMemoryReviewResult 用于按原始顺序保留被接纳的记忆候选，并返回 reviewer 在去重评审阶段额外丢弃了多少节点。
func applyPostActionMemoryReviewResult(nodes []logicdomain.MemoryNodeCandidate, section *logicdomain.PostActionMemoryReviewSection) ([]logicdomain.MemoryNodeCandidate, int, error) {
	if len(nodes) == 0 {
		return nil, 0, nil
	}
	if section == nil {
		return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: "missing memory review result"}
	}
	accepted := make(map[int]struct{}, len(section.AcceptedCandidateIndexes))
	for _, idx := range section.AcceptedCandidateIndexes {
		if idx < 0 || idx >= len(nodes) {
			return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "review_postaction_candidates", Message: fmt.Sprintf("memory accepted index %d is out of range", idx)}
		}
		accepted[idx] = struct{}{}
	}
	kept := make([]logicdomain.MemoryNodeCandidate, 0, len(section.AcceptedCandidateIndexes))
	for idx, node := range nodes {
		if _, ok := accepted[idx]; !ok {
			continue
		}
		kept = append(kept, node)
	}
	return kept, len(nodes) - len(kept), nil
}

// clonePostActionProfileNodes deep-copies the post-review profile node slice so persistence can keep the full status set while active-only views remain free to filter separately.
// clonePostActionProfileNodes 用于深拷贝评审后的画像节点切片，让持久化保留完整状态集合，而活跃视图仍可按需单独过滤。
func clonePostActionProfileNodes(nodes []logicdomain.ProfileNodeCandidate) []logicdomain.ProfileNodeCandidate {
	if len(nodes) == 0 {
		return nil
	}
	cloned := append([]logicdomain.ProfileNodeCandidate(nil), nodes...)
	for idx := range cloned {
		cloned[idx].SupersedeNodeIDs = append([]uint64(nil), nodes[idx].SupersedeNodeIDs...)
	}
	return cloned
}

// filterPostActionActiveProfileNodes keeps only active post-review profile nodes so rejected candidates do not become long-term storage noise.
// filterPostActionActiveProfileNodes 用于只保留评审后为 active 的画像节点；它只能服务于运行态活跃视图，不能裁剪待持久化实体集合。
func filterPostActionActiveProfileNodes(nodes []logicdomain.ProfileNodeCandidate) []logicdomain.ProfileNodeCandidate {
	if len(nodes) == 0 {
		return nil
	}
	filtered := make([]logicdomain.ProfileNodeCandidate, 0, len(nodes))
	for _, node := range nodes {
		if node.Status != logicdomain.ProfileStatusActive {
			continue
		}
		filtered = append(filtered, node)
	}
	return filtered
}

// countPostActionExternalResearchCandidates counts final memory/profile nodes that survived review thanks to external research or tool-discovered evidence.
// countPostActionExternalResearchCandidates 用于统计在最终结果中因外部研究或工具发现而被保留下来的记忆/画像节点数量。
func countPostActionExternalResearchCandidates(analysis logicdomain.TurnAnalysis) int {
	count := 0
	for _, node := range analysis.MemoryNodes {
		if isPostActionExternalResearchSource(node.EvidenceSource) {
			count++
		}
	}
	for _, node := range analysis.ProfileNodes {
		if isPostActionExternalResearchSource(node.EvidenceSource) {
			count++
		}
	}
	return count
}

// isPostActionExternalResearchSource reports whether one evidence source came from an external-research or tool-discovered branch that should be visible in compaction metrics.
// isPostActionExternalResearchSource 用于判断某个证据来源是否属于外部研究或工具发现分支，便于压缩指标观察。
func isPostActionExternalResearchSource(source string) bool {
	switch strings.TrimSpace(source) {
	case logicdomain.TurnAnalysisEvidenceSourceAssistantExternalResearch, logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered:
		return true
	default:
		return false
	}
}
