// postaction_candidate_review.go implements the post-action first-pass admission filter, shared-scope memory dedupe recall, and unified candidate review flow.
// postaction_candidate_review.go 用于实现 post-action 的首轮准入过滤、共享范围记忆去重召回以及统一候选评审流程。
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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
	HardDedupeDroppedCount    int
	ExternalResearchKeptCount int
}

// scopedMemoryReviewBuildResult stores the full reviewer-facing candidate list plus the candidates that were already proven to be duplicate enough to skip the LLM review.
// scopedMemoryReviewBuildResult 用于保存完整的 reviewer 候选列表，以及那些已经被证明重复程度足够高、可以跳过 LLM 评审的候选。
type scopedMemoryReviewBuildResult struct {
	Candidates  []logicdomain.PostActionMemoryReviewCandidate
	HardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate
}

// partitionedMemoryReviewCandidates stores the dense reviewer subset together with its mapping back to the original candidate indexes after local hard-dedupe pruning.
// partitionedMemoryReviewCandidates 用于保存本地硬排重裁剪后的稠密 reviewer 子集，以及它回映到原始候选索引的映射关系。
type partitionedMemoryReviewCandidates struct {
	ReviewerCandidates []logicdomain.PostActionMemoryReviewCandidate
	ReviewerToOriginal []int
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

// reviewTurnCandidates keeps the historical helper signature used by tests while delegating runtime execution to the resolved-time variant that prevents fallback-anchor drift.
// reviewTurnCandidates 用于保留测试仍在使用的历史辅助函数签名，同时把运行时执行委托给可防止兜底时间漂移的 resolved-time 版本。
func (u *PostActionUseCase) reviewTurnCandidates(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord, analysis *logicdomain.TurnAnalysis, stats *postActionCompactionStats) error {
	return u.reviewTurnCandidatesWithResolvedTime(ctx, session, turn, rawTurn, choosePostActionCreatedAt(turn, time.Now().UTC()), analysis, stats)
}

// reviewTurnCandidatesWithResolvedTime runs the main unified candidate-review path so memory dedupe and profile acceptance can reuse one LLM call and one turn-level reasoning context.
// reviewTurnCandidatesWithResolvedTime 用于执行统一候选评审主路径，让记忆去重与画像接纳共享一次 LLM 调用和同一轮语义上下文。
func (u *PostActionUseCase) reviewTurnCandidatesWithResolvedTime(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord, resolvedTurnCreatedAt time.Time, analysis *logicdomain.TurnAnalysis, stats *postActionCompactionStats) error {
	if u == nil || analysis == nil {
		return nil
	}
	if len(analysis.MemoryNodes) == 0 && len(analysis.ProfileNodes) == 0 {
		if stats != nil {
			stats.ExternalResearchKeptCount = 0
		}
		return nil
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
		CreatedAt: resolvedTurnCreatedAt,
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

	memoryReviewBuild, err := u.buildPostActionMemoryReviewCandidates(ctx, session, analysis.MemoryNodes)
	if err != nil {
		return err
	}
	reviewTimestamp := time.Now().UTC()
	currentTurnDateTime := formatPostActionReviewerDateTime(resolvedTurnCreatedAt)
	currentTurnDate := formatPostActionReviewerDate(resolvedTurnCreatedAt)
	for idx := range memoryReviewBuild.Candidates {
		memoryReviewBuild.Candidates[idx].CandidateDateTime = currentTurnDateTime
		memoryReviewBuild.Candidates[idx].CandidateDate = currentTurnDate
	}
	if stats != nil {
		stats.HardDedupeDroppedCount += len(memoryReviewBuild.HardDropped)
	}
	memoryReviewPartition := partitionMemoryReviewCandidatesForReviewer(memoryReviewBuild.Candidates, memoryReviewBuild.HardDropped)
	needReviewer := len(memoryReviewPartition.ReviewerCandidates) > 0 || len(stampedProfiles) > 0
	reviewed := logicdomain.PostActionCandidateReviewResult{}
	if needReviewer {
		if u.candidateReviewer == nil {
			return fmt.Errorf("post-action candidate reviewer is nil")
		}
		reviewed, err = u.candidateReviewer.Review(ctx, logicdomain.PostActionCandidateReviewInput{
			UserInputKind:       strings.TrimSpace(analysis.UserInputKind),
			CurrentTimestamp:    reviewTimestamp.UnixMilli(),
			CurrentTurnDateTime: currentTurnDateTime,
			CurrentTurnDate:     currentTurnDate,
			UserContent:         strings.TrimSpace(rawTurn.UserContent),
			AssistantContent:    strings.TrimSpace(rawTurn.AssistantContent),
			MemoryCandidates:    memoryReviewPartition.ReviewerCandidates,
			ProfileTargets:      snapshot,
			ProfileCandidates:   stampedProfiles,
		})
		if err != nil {
			return err
		}
	}
	remappedMemorySection, err := remapPostActionMemoryReviewSectionToOriginal(reviewed.Memory, memoryReviewPartition.ReviewerToOriginal)
	if err != nil {
		return err
	}
	fullMemorySection, err := mergePostActionMemoryReviewSectionWithHardDropped(len(analysis.MemoryNodes), remappedMemorySection, memoryReviewBuild.HardDropped)
	if err != nil {
		return err
	}

	originalMemoryNodes := append([]logicdomain.MemoryNodeCandidate(nil), analysis.MemoryNodes...)
	keptMemoryNodes, reviewDroppedCount, err := applyPostActionMemoryReviewResult(originalMemoryNodes, fullMemorySection, memoryReviewBuild.Candidates)
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
func (u *PostActionUseCase) buildPostActionMemoryReviewCandidates(ctx context.Context, session logicdomain.SessionRef, nodes []logicdomain.MemoryNodeCandidate) (scopedMemoryReviewBuildResult, error) {
	return buildScopedMemoryReviewCandidates(
		ctx,
		u.memorySearcher,
		session,
		nodes,
		u.analysisCfg.DedupeSearchTopK,
		u.analysisCfg.MemoryReplaceScope,
		u.analysisCfg.DedupeMinSimilarity,
		u.analysisCfg.HardDedupeCosineThreshold,
	)
}

// partitionMemoryReviewCandidatesForReviewer removes locally hard-deduped candidates from the LLM request and reindexes the remaining memory candidates into one dense 0..N-1 subset.
// partitionMemoryReviewCandidatesForReviewer 用于把本地已硬排重的候选从 LLM 请求中移除，并将剩余记忆候选重新编号为稠密的 0..N-1 子集。
func partitionMemoryReviewCandidatesForReviewer(candidates []logicdomain.PostActionMemoryReviewCandidate, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) partitionedMemoryReviewCandidates {
	partition := partitionedMemoryReviewCandidates{
		ReviewerCandidates: make([]logicdomain.PostActionMemoryReviewCandidate, 0, len(candidates)),
		ReviewerToOriginal: make([]int, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		if _, ok := hardDropped[candidate.CandidateIndex]; ok {
			continue
		}
		copyCandidate := candidate
		copyCandidate.CandidateIndex = len(partition.ReviewerCandidates)
		partition.ReviewerCandidates = append(partition.ReviewerCandidates, copyCandidate)
		partition.ReviewerToOriginal = append(partition.ReviewerToOriginal, candidate.CandidateIndex)
	}
	return partition
}

// remapPostActionMemoryReviewSectionToOriginal restores reviewer decisions from the dense subset indexes back to the original candidate indexes after local hard-dedupe pruning.
// remapPostActionMemoryReviewSectionToOriginal 用于在本地硬排重裁剪后，把 reviewer 对稠密子集索引的决策还原回原始候选索引。
func remapPostActionMemoryReviewSectionToOriginal(section *logicdomain.PostActionMemoryReviewSection, reviewerToOriginal []int) (*logicdomain.PostActionMemoryReviewSection, error) {
	if section == nil {
		return nil, nil
	}
	remapIndex := func(reviewerIndex int) (int, error) {
		if reviewerIndex < 0 || reviewerIndex >= len(reviewerToOriginal) {
			return 0, logicdomain.InvalidLLMOutputError{
				Scene:   "postaction_l2_main",
				Message: fmt.Sprintf("memory reviewer subset index %d is out of range", reviewerIndex),
			}
		}
		return reviewerToOriginal[reviewerIndex], nil
	}
	remapped := &logicdomain.PostActionMemoryReviewSection{
		Reason: strings.TrimSpace(section.Reason),
	}
	for _, idx := range section.AcceptedCandidateIndexes {
		originalIndex, err := remapIndex(idx)
		if err != nil {
			return nil, err
		}
		remapped.AcceptedCandidateIndexes = append(remapped.AcceptedCandidateIndexes, originalIndex)
	}
	for _, idx := range section.DroppedCandidateIndexes {
		originalIndex, err := remapIndex(idx)
		if err != nil {
			return nil, err
		}
		remapped.DroppedCandidateIndexes = append(remapped.DroppedCandidateIndexes, originalIndex)
	}
	for _, accepted := range section.AcceptedCandidates {
		originalIndex, err := remapIndex(accepted.CandidateIndex)
		if err != nil {
			return nil, err
		}
		remapped.AcceptedCandidates = append(remapped.AcceptedCandidates, logicdomain.PostActionAcceptedMemoryCandidate{
			CandidateIndex:     originalIndex,
			SupersedeMemoryIDs: append([]uint64(nil), accepted.SupersedeMemoryIDs...),
		})
	}
	for _, dropped := range section.DroppedCandidates {
		originalIndex, err := remapIndex(dropped.CandidateIndex)
		if err != nil {
			return nil, err
		}
		remapped.DroppedCandidates = append(remapped.DroppedCandidates, logicdomain.PostActionDroppedMemoryCandidate{
			CandidateIndex: originalIndex,
			DedupeMemoryID: dropped.DedupeMemoryID,
		})
	}
	sort.Ints(remapped.AcceptedCandidateIndexes)
	sort.Ints(remapped.DroppedCandidateIndexes)
	sort.Slice(remapped.AcceptedCandidates, func(i, j int) bool {
		return remapped.AcceptedCandidates[i].CandidateIndex < remapped.AcceptedCandidates[j].CandidateIndex
	})
	sort.Slice(remapped.DroppedCandidates, func(i, j int) bool {
		return remapped.DroppedCandidates[i].CandidateIndex < remapped.DroppedCandidates[j].CandidateIndex
	})
	return remapped, nil
}

// mergePostActionMemoryReviewSectionWithHardDropped merges reviewer output with local hard-dedupe drops and rebuilds one full-coverage section over the original candidate index space.
// mergePostActionMemoryReviewSectionWithHardDropped 用于把 reviewer 输出与本地硬排重丢弃结果合并，并在原始候选索引空间上重建一份完整覆盖的评审结果。
func mergePostActionMemoryReviewSectionWithHardDropped(totalCandidates int, reviewed *logicdomain.PostActionMemoryReviewSection, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) (*logicdomain.PostActionMemoryReviewSection, error) {
	if totalCandidates == 0 {
		return nil, nil
	}
	acceptedSet := make(map[int]struct{}, totalCandidates)
	droppedSet := make(map[int]struct{}, totalCandidates)
	acceptedByIndex := make(map[int]logicdomain.PostActionAcceptedMemoryCandidate, totalCandidates)
	droppedByIndex := make(map[int]logicdomain.PostActionDroppedMemoryCandidate, totalCandidates)
	reason := ""
	if reviewed != nil {
		reason = strings.TrimSpace(reviewed.Reason)
		for _, idx := range reviewed.AcceptedCandidateIndexes {
			acceptedSet[idx] = struct{}{}
		}
		for _, idx := range reviewed.DroppedCandidateIndexes {
			droppedSet[idx] = struct{}{}
		}
		for _, accepted := range reviewed.AcceptedCandidates {
			acceptedByIndex[accepted.CandidateIndex] = accepted
			acceptedSet[accepted.CandidateIndex] = struct{}{}
		}
		for _, dropped := range reviewed.DroppedCandidates {
			droppedByIndex[dropped.CandidateIndex] = dropped
			droppedSet[dropped.CandidateIndex] = struct{}{}
		}
	}
	for idx, dropped := range hardDropped {
		if _, ok := acceptedSet[idx]; ok {
			return nil, logicdomain.InvalidLLMOutputError{
				Scene:   "postaction_l2_main",
				Message: fmt.Sprintf("memory candidate %d is both hard-dropped and accepted", idx),
			}
		}
		droppedSet[idx] = struct{}{}
		droppedByIndex[idx] = dropped
	}
	if len(acceptedSet)+len(droppedSet) != totalCandidates {
		return nil, logicdomain.InvalidLLMOutputError{
			Scene:   "postaction_l2_main",
			Message: fmt.Sprintf("memory review coverage mismatch after hard dedupe merge: accepted=%d dropped=%d total=%d", len(acceptedSet), len(droppedSet), totalCandidates),
		}
	}
	out := &logicdomain.PostActionMemoryReviewSection{
		AcceptedCandidates:       make([]logicdomain.PostActionAcceptedMemoryCandidate, 0, len(acceptedByIndex)),
		DroppedCandidates:        make([]logicdomain.PostActionDroppedMemoryCandidate, 0, len(droppedByIndex)),
		AcceptedCandidateIndexes: make([]int, 0, len(acceptedSet)),
		DroppedCandidateIndexes:  make([]int, 0, len(droppedSet)),
		Reason:                   reason,
	}
	for idx := range acceptedSet {
		out.AcceptedCandidateIndexes = append(out.AcceptedCandidateIndexes, idx)
		if accepted, ok := acceptedByIndex[idx]; ok {
			out.AcceptedCandidates = append(out.AcceptedCandidates, accepted)
		}
	}
	for idx := range droppedSet {
		out.DroppedCandidateIndexes = append(out.DroppedCandidateIndexes, idx)
		if dropped, ok := droppedByIndex[idx]; ok {
			out.DroppedCandidates = append(out.DroppedCandidates, dropped)
		}
	}
	sort.Ints(out.AcceptedCandidateIndexes)
	sort.Ints(out.DroppedCandidateIndexes)
	sort.Slice(out.AcceptedCandidates, func(i, j int) bool {
		return out.AcceptedCandidates[i].CandidateIndex < out.AcceptedCandidates[j].CandidateIndex
	})
	sort.Slice(out.DroppedCandidates, func(i, j int) bool {
		return out.DroppedCandidates[i].CandidateIndex < out.DroppedCandidates[j].CandidateIndex
	})
	return out, nil
}

// validatePostActionAcceptedSupersedeMemoryIDs verifies that one accepted candidate only points at similar-memory ids the reviewer actually saw for that candidate.
// validatePostActionAcceptedSupersedeMemoryIDs 用于校验一条已接纳候选只能指向 reviewer 在该候选下真正看到过的 similar memory id。
func validatePostActionAcceptedSupersedeMemoryIDs(scene string, candidateIndex int, similarMemories []logicdomain.PostActionSimilarMemoryCandidate, supersedeMemoryIDs []uint64) ([]uint64, error) {
	if len(supersedeMemoryIDs) == 0 {
		return nil, nil
	}
	allowed := make(map[uint64]struct{}, len(similarMemories))
	for _, similar := range similarMemories {
		if similar.MemoryID == 0 {
			continue
		}
		allowed[similar.MemoryID] = struct{}{}
	}
	out := make([]uint64, 0, len(supersedeMemoryIDs))
	seen := make(map[uint64]struct{}, len(supersedeMemoryIDs))
	for _, memoryID := range supersedeMemoryIDs {
		if memoryID == 0 {
			continue
		}
		if _, ok := allowed[memoryID]; !ok {
			return nil, logicdomain.InvalidLLMOutputError{
				Scene:   scene,
				Message: fmt.Sprintf("memory accepted candidate %d references unavailable supersede_memory_id %d", candidateIndex, memoryID),
			}
		}
		if _, ok := seen[memoryID]; ok {
			continue
		}
		seen[memoryID] = struct{}{}
		out = append(out, memoryID)
	}
	return out, nil
}

// validatePostActionDroppedDedupeMemoryID verifies that one dropped candidate only reuses a durable memory id the reviewer actually saw in that candidate's similar-memory list.
// validatePostActionDroppedDedupeMemoryID 用于校验一条被丢弃候选若要复用旧记忆，只能引用 reviewer 在该候选下真正看到过的 similar memory id。
func validatePostActionDroppedDedupeMemoryID(scene string, candidateIndex int, similarMemories []logicdomain.PostActionSimilarMemoryCandidate, dedupeMemoryID uint64) (uint64, error) {
	if dedupeMemoryID == 0 {
		return 0, nil
	}
	allowed := make(map[uint64]struct{}, len(similarMemories))
	for _, similar := range similarMemories {
		if similar.MemoryID == 0 {
			continue
		}
		allowed[similar.MemoryID] = struct{}{}
	}
	if _, ok := allowed[dedupeMemoryID]; !ok {
		return 0, logicdomain.InvalidLLMOutputError{
			Scene:   scene,
			Message: fmt.Sprintf("memory dropped candidate %d references unavailable dedupe_memory_id %d", candidateIndex, dedupeMemoryID),
		}
	}
	return dedupeMemoryID, nil
}

// buildScopedMemoryReviewCandidates recalls similar durable memories for each new candidate inside the configured replacement scope and attaches them by QueryIndex so later reviewer decisions stay candidate-stable.
// buildScopedMemoryReviewCandidates 用于在配置好的更替作用域内为每条新候选召回相似长期记忆，并按 QueryIndex 回贴，确保后续 reviewer 决策稳定绑定到正确候选。
func buildScopedMemoryReviewCandidates(ctx context.Context, searcher PostActionMemorySearcher, session logicdomain.SessionRef, nodes []logicdomain.MemoryNodeCandidate, topK int, scope string, minSimilarity, hardDedupeCosineThreshold float64) (scopedMemoryReviewBuildResult, error) {
	resultBundle := scopedMemoryReviewBuildResult{
		Candidates:  make([]logicdomain.PostActionMemoryReviewCandidate, 0, len(nodes)),
		HardDropped: make(map[int]logicdomain.PostActionDroppedMemoryCandidate, len(nodes)),
	}
	if len(nodes) == 0 {
		return resultBundle, nil
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
		resultBundle.Candidates = append(resultBundle.Candidates, candidate)
		query := buildPostActionMemorySearchQuery(node)
		if query == "" || searcher == nil {
			continue
		}
		queries = append(queries, query)
		queryCandidateIndexes = append(queryCandidateIndexes, idx)
	}
	if len(queries) == 0 || searcher == nil {
		return resultBundle, nil
	}
	queryCmd := MemoryQueryCommand{
		UserID:               session.UserID,
		ProjectID:            session.ProjectID,
		Queries:              queries,
		TopK:                 topK,
		EnableHardDedupePool: true,
		ScopeOverride:        scope,
	}
	if normalizeConfigToken(scope) == memoryReplaceScopeSession {
		queryCmd.SessionID = session.SessionID
	}
	result, err := searcher.Search(ctx, queryCmd)
	if err != nil {
		return scopedMemoryReviewBuildResult{}, fmt.Errorf("search memory review candidates: %w", err)
	}

	// Bind each grouped result back to the candidate index carried through QueryIndex so future search optimizations cannot silently scramble similar-memory attachments.
	// 通过 QueryIndex 把每组结果绑定回对应候选索引，避免未来检索优化在重排结果时悄悄打乱 similar-memory 的挂接关系。
	for _, group := range result.Results {
		if group.QueryIndex < 0 || group.QueryIndex >= len(queryCandidateIndexes) {
			continue
		}
		candidateIndex := queryCandidateIndexes[group.QueryIndex]
		if candidateIndex < 0 || candidateIndex >= len(resultBundle.Candidates) {
			continue
		}
		resultBundle.Candidates[candidateIndex].SimilarMemories = buildPostActionSimilarMemoryCandidates(group.Hits, minSimilarity)
		if hardDedupe, ok := detectHardDedupeMemoryCandidate(candidateIndex, resultBundle.Candidates[candidateIndex].Category, group.QueryVector, group.HardDedupeHits, hardDedupeCosineThreshold); ok {
			resultBundle.HardDropped[candidateIndex] = hardDedupe
		}
	}
	if len(resultBundle.HardDropped) == 0 {
		resultBundle.HardDropped = nil
	}
	return resultBundle, nil
}

// detectHardDedupeMemoryCandidate scans the dedicated hard-dedupe hit pool with real cosine similarity and only short-circuits when one durable memory of the same category crosses the configured threshold.
// detectHardDedupeMemoryCandidate 用于基于真实 cosine 扫描专用硬排重候选池，并且仅在同 Category 的长期记忆跨过配置阈值时才允许直接短路。
func detectHardDedupeMemoryCandidate(candidateIndex int, candidateCategory int, queryVector []float32, hits []MemoryQueryHit, threshold float64) (logicdomain.PostActionDroppedMemoryCandidate, bool) {
	if threshold <= 0 || len(queryVector) == 0 || len(hits) == 0 {
		return logicdomain.PostActionDroppedMemoryCandidate{}, false
	}
	if !logicdomain.ValidMemoryNodeCategory(candidateCategory) {
		return logicdomain.PostActionDroppedMemoryCandidate{}, false
	}
	bestMemoryID := uint64(0)
	bestCosine := threshold
	for _, hit := range hits {
		if hit.MemoryRef.Type != logicdomain.MemoryRefTypeMemory || hit.MemoryRef.ID == 0 || len(hit.Vector) == 0 {
			continue
		}
		if hit.Category != candidateCategory {
			continue
		}
		cosine := cosineSimilarityFloat32(queryVector, hit.Vector)
		if cosine < threshold {
			continue
		}
		if bestMemoryID == 0 || cosine > bestCosine || (cosine == bestCosine && hit.MemoryRef.ID < bestMemoryID) {
			bestMemoryID = hit.MemoryRef.ID
			bestCosine = cosine
		}
	}
	if bestMemoryID == 0 {
		return logicdomain.PostActionDroppedMemoryCandidate{}, false
	}
	return logicdomain.PostActionDroppedMemoryCandidate{
		CandidateIndex: candidateIndex,
		DedupeMemoryID: bestMemoryID,
	}, true
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
			MemoryID:        hit.MemoryRef.ID,
			SourceTurnID:    hit.SourceRef.ID,
			CreatedDateTime: formatPostActionReviewerDateTime(hit.CreatedAt),
			CreatedDate:     formatPostActionReviewerDate(hit.CreatedAt),
			ScopeLevel:      logicdomain.MemoryScopeLevelLabel(hit.ScopeLevel),
			Category:        hit.Category,
			Score:           score,
			Origin:          strings.TrimSpace(hit.Origin),
			Abstract:        abstract,
			Details:         details,
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

// formatPostActionReviewerDate normalizes one timestamp into the lightweight YYYY-MM-DD string sent to the post-action L2 reviewer so it can reason about recency without paying full timestamp token cost.
// formatPostActionReviewerDate 用于把时间戳归一成发送给 post-action L2 reviewer 的轻量 YYYY-MM-DD 字符串，让模型感知先后关系时无需承担完整时间戳的 token 成本。
func formatPostActionReviewerDateTime(ts time.Time) string {
	return logicdomain.FormatDisplayDateTime(ts)
}

// formatPostActionReviewerDate normalizes one timestamp into the local YYYY-MM-DD string sent to the post-action L2 reviewer so date-only reasoning stays aligned with the shared local-time contract.
// formatPostActionReviewerDate 用于把时间归一成本地 YYYY-MM-DD 字符串，发送给 post-action L2 reviewer，确保日期级推理与共享本地时间契约保持一致。
func formatPostActionReviewerDate(ts time.Time) string {
	return logicdomain.FormatDisplayDate(ts)
}

// applyPostActionMemoryReviewResult keeps only accepted memory candidates in original order and merges reviewer-approved supersede ids back onto each surviving node so later persistence can safely re-derive the final retirement set after any additional filtering.
// applyPostActionMemoryReviewResult 用于按原始顺序保留被接纳的记忆候选，并把 reviewer 批准的 supersede id 回填到存活节点上，确保后续若还有额外过滤，持久化阶段仍能安全重建最终退役集合。
func applyPostActionMemoryReviewResult(nodes []logicdomain.MemoryNodeCandidate, section *logicdomain.PostActionMemoryReviewSection, candidates []logicdomain.PostActionMemoryReviewCandidate) ([]logicdomain.MemoryNodeCandidate, int, error) {
	if len(nodes) == 0 {
		return nil, 0, nil
	}
	if section == nil {
		return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "postaction_l2_main", Message: "missing memory review result"}
	}
	if len(candidates) != len(nodes) {
		return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "postaction_l2_main", Message: "memory candidate count does not match nodes"}
	}
	accepted := make(map[int]struct{}, len(section.AcceptedCandidateIndexes))
	for _, idx := range section.AcceptedCandidateIndexes {
		if idx < 0 || idx >= len(nodes) {
			return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "postaction_l2_main", Message: fmt.Sprintf("memory accepted index %d is out of range", idx)}
		}
		accepted[idx] = struct{}{}
	}
	acceptedByIndex := make(map[int]logicdomain.PostActionAcceptedMemoryCandidate, len(section.AcceptedCandidates))
	for _, acceptedCandidate := range section.AcceptedCandidates {
		if acceptedCandidate.CandidateIndex < 0 || acceptedCandidate.CandidateIndex >= len(nodes) {
			return nil, 0, logicdomain.InvalidLLMOutputError{Scene: "postaction_l2_main", Message: fmt.Sprintf("memory accepted candidate %d is out of range", acceptedCandidate.CandidateIndex)}
		}
		acceptedByIndex[acceptedCandidate.CandidateIndex] = acceptedCandidate
	}
	kept := make([]logicdomain.MemoryNodeCandidate, 0, len(section.AcceptedCandidateIndexes))
	for idx, node := range nodes {
		if _, ok := accepted[idx]; !ok {
			continue
		}
		if acceptedCandidate, ok := acceptedByIndex[idx]; ok {
			supersedeMemoryIDs, err := validatePostActionAcceptedSupersedeMemoryIDs(
				"postaction_l2_main",
				acceptedCandidate.CandidateIndex,
				candidates[idx].SimilarMemories,
				acceptedCandidate.SupersedeMemoryIDs,
			)
			if err != nil {
				return nil, 0, err
			}
			if len(supersedeMemoryIDs) > 0 {
				node.SupersedeMemoryIDs = mergePostActionCandidateSupersedeMemoryIDs(node.SupersedeMemoryIDs, supersedeMemoryIDs)
			}
		}
		kept = append(kept, node)
	}
	return kept, len(nodes) - len(kept), nil
}

// mergePostActionCandidateSupersedeMemoryIDs merges the candidate-local supersede ids with any reviewer-approved replacements and keeps the result stable and deduplicated for later persistence.
// mergePostActionCandidateSupersedeMemoryIDs 用于合并候选自带的 supersede id 与 reviewer 批准的替代目标，并保持结果稳定去重，供后续持久化直接复用。
func mergePostActionCandidateSupersedeMemoryIDs(existing, extra []uint64) []uint64 {
	if len(existing) == 0 && len(extra) == 0 {
		return nil
	}
	merged := make(map[uint64]struct{}, len(existing)+len(extra))
	out := make([]uint64, 0, len(existing)+len(extra))
	for _, memoryID := range existing {
		if memoryID == 0 {
			continue
		}
		if _, ok := merged[memoryID]; ok {
			continue
		}
		merged[memoryID] = struct{}{}
		out = append(out, memoryID)
	}
	for _, memoryID := range extra {
		if memoryID == 0 {
			continue
		}
		if _, ok := merged[memoryID]; ok {
			continue
		}
		merged[memoryID] = struct{}{}
		out = append(out, memoryID)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
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
