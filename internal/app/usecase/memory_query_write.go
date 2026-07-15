// memory_query_write.go contains the direct memory write, semantic dedupe, embedding persistence, and rollback helpers used by the app-layer memory write flow.
// memory_query_write.go 用于承载应用层主动记忆写入链路中的语义去重、向量持久化、直接写入与回滚辅助逻辑。
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// Write validates direct memory items, applies soft idempotency, embeds new rows, writes vectors, and persists unified memory records.
// Write 用于校验主动记忆条目、执行软幂等、向量化新记录、写入向量，并持久化统一记忆行。
func (u *MemoryUseCase) Write(ctx context.Context, cmd WriteMemoriesCommand) (WriteMemoriesResult, error) {
	if u == nil || u.memories == nil {
		return WriteMemoriesResult{}, fmt.Errorf("memory store is nil")
	}
	if u.embedding == nil {
		return WriteMemoriesResult{}, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return WriteMemoriesResult{}, fmt.Errorf("vector store is nil")
	}
	cmd.Items = scrubWriteMemoryItemsPII(u.piiScrubber, cmd.Items)
	if err := validateWriteMemoriesCommand(cmd); err != nil {
		return WriteMemoriesResult{}, err
	}

	// Normalize defaults item-by-item so direct-write callers can omit optional lifecycle fields without triggering a second reviewer model call.
	// 逐条补齐默认值，让主动写记忆的调用方可以省略可选生命周期字段，而无需再触发第二个评审模型。
	now := time.Now().UTC()
	items := make([]WriteMemoryItem, 0, len(cmd.Items))
	for _, item := range cmd.Items {
		items = append(items, normalizeWriteMemoryItem(item, now))
	}

	results := make([]WriteMemoryResultItem, len(items))
	pending := make([]directWritePendingItem, 0, len(items))
	pendingByCollapseKey := make(map[string]int, len(items))
	for idx, item := range items {
		existing, ok, dedupeHash, err := u.resolveRecentDirectWriteSoftDedupe(ctx, cmd.Session, item, now)
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		if ok {
			results[idx] = WriteMemoryResultItem{
				Ref: logicdomain.MemoryRef{
					Type: logicdomain.MemoryRefTypeMemory,
					ID:   existing.ID,
				},
				SourceKind: existing.SourceKind,
				ScopeLevel: existing.ScopeLevel,
				Deduped:    true,
			}
			continue
		}
		collapseKey := buildDirectMemoryRequestCollapseKey(item)
		if pendingIdx, duplicated := pendingByCollapseKey[collapseKey]; duplicated {
			pending[pendingIdx].OriginalIndexes = append(pending[pendingIdx].OriginalIndexes, idx)
			continue
		}
		pendingByCollapseKey[collapseKey] = len(pending)
		pending = append(pending, directWritePendingItem{
			OriginalIndexes: []int{idx},
			Item:            item,
			DedupeHash:      dedupeHash,
		})
	}
	if len(pending) == 0 {
		return WriteMemoriesResult{Items: results}, nil
	}

	decisions, err := u.reviewDirectWriteMemoryCandidates(ctx, cmd.Session, pending, now)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	dedupedExistingRows, err := u.loadDirectWriteDedupedExistingRows(ctx, decisions)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	createdVectorsByPendingIndex, err := u.prepareDirectWriteCreateVectors(ctx, cmd.Session, pending, decisions, dedupedExistingRows)
	if err != nil {
		return WriteMemoriesResult{}, err
	}
	for idx, pendingItem := range pending {
		decision := decisions[idx]
		if decision.DedupedExistingMemoryID > 0 {
			if existing, ok := dedupedExistingRows[decision.DedupedExistingMemoryID]; ok {
				assignDirectWriteResultItems(results, pendingItem.OriginalIndexes, WriteMemoryResultItem{
					Ref: logicdomain.MemoryRef{
						Type: logicdomain.MemoryRefTypeMemory,
						ID:   existing.ID,
					},
					SourceKind: existing.SourceKind,
					ScopeLevel: existing.ScopeLevel,
					Deduped:    true,
				})
				continue
			}
		}

		created, err := u.persistDirectWriteMemory(ctx, cmd.Session, pendingItem.Item, pendingItem.DedupeHash, decision.SupersedeMemoryIDs, now, createdVectorsByPendingIndex[idx])
		if err != nil {
			return WriteMemoriesResult{}, err
		}
		assignDirectWriteResultItems(results, pendingItem.OriginalIndexes, created)
	}
	return WriteMemoriesResult{Items: results}, nil
}

// resolveRecentDirectWriteSoftDedupe evaluates the short-window soft-idempotency path for one direct-write item using the current semantic hash as the only durable dedupe key.
// resolveRecentDirectWriteSoftDedupe 用于为单条主动写记忆解析短窗口软幂等路径，并且只使用当前语义哈希作为长期持久化去重键。
func (u *MemoryUseCase) resolveRecentDirectWriteSoftDedupe(ctx context.Context, session logicdomain.SessionRef, item WriteMemoryItem, now time.Time) (logicdomain.MemoryNodeRecord, bool, string, error) {
	currentHash := buildDirectMemoryDedupeHash(session, item, now)
	existing, ok, err := u.memories.FindRecentActiveMemoryByDedupe(
		ctx,
		session,
		logicdomain.MemorySourceKindGRPCAIWrite,
		item.ScopeLevel,
		currentHash,
		now.Add(-directMemoryDedupeWindow),
	)
	if err != nil || ok {
		return existing, ok, currentHash, err
	}
	return logicdomain.MemoryNodeRecord{}, false, currentHash, nil
}

// directWritePendingItem stores one post-soft-dedupe direct-write item together with every caller position that collapsed into the same in-request write and its stable short-window dedupe hash.
// directWritePendingItem 用于保存一条经过软幂等筛选后仍待处理的主动写入项，并记录同一请求内被折叠到这次写入上的全部原始位置，以及稳定的短窗口去重哈希。
type directWritePendingItem struct {
	OriginalIndexes []int
	Item            WriteMemoryItem
	DedupeHash      string
}

// directWriteMemoryDecision stores the final semantic-replacement decision for one pending direct-write candidate after reviewer judgment plus safe fallback normalization.
// directWriteMemoryDecision 用于保存一条待写主动记忆在 reviewer 判断和安全回退规范化之后的最终语义替代决策。
type directWriteMemoryDecision struct {
	DedupedExistingMemoryID uint64
	SupersedeMemoryIDs      []uint64
}

// reviewDirectWriteMemoryCandidates optionally runs the unified reviewer over pending direct-write items so explicit tool writes can converge with post-action semantic replacement rules.
// reviewDirectWriteMemoryCandidates 用于按需对待写主动记忆执行统一 reviewer，让工具显式写入也能与 post-action 共享同一套语义替代规则。
func (u *MemoryUseCase) reviewDirectWriteMemoryCandidates(ctx context.Context, session logicdomain.SessionRef, pending []directWritePendingItem, reviewTime time.Time) ([]directWriteMemoryDecision, error) {
	if len(pending) == 0 {
		return nil, nil
	}
	if u == nil {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}
	// Only the app/runtime wiring should opt direct-write flows into shared semantic replacement; bare unit tests and lightweight callers keep the historical no-extra-search behavior until ConfigureMemoryReplace is invoked.
	// 只有应用装配层显式启用后，主动写记忆才进入共享语义替代链路；裸构造的单测和轻量调用方在 ConfigureMemoryReplace 被调用前继续保持历史上的“无额外检索”行为。
	if !u.memoryReplaceConfigured {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}
	if u.candidateReviewer == nil && u.memoryReplaceHardDedupeCosineThreshold <= 0 {
		return buildDirectWriteFallbackDecisions(len(pending)), nil
	}

	// Reuse the same review-candidate shape as post-action so explicit tool writes and asynchronous turn extraction do not drift onto competing dedupe semantics.
	// 复用与 post-action 相同的评审候选结构，避免显式工具写入和异步 turn 提炼各自演化出两套不同的去重语义。
	nodes := make([]logicdomain.MemoryNodeCandidate, 0, len(pending))
	for _, item := range pending {
		nodes = append(nodes, logicdomain.MemoryNodeCandidate{
			Category:       item.Item.Category,
			Abstract:       item.Item.Abstract,
			Details:        item.Item.Details,
			EvidenceSource: logicdomain.TurnAnalysisEvidenceSourceAssistantToolDiscovered,
			Admission:      logicdomain.TurnAnalysisAdmissionKeep,
		})
	}
	reviewBuild, err := buildScopedMemoryReviewCandidates(
		ctx,
		u,
		session,
		nodes,
		u.memoryReplaceTopK,
		u.memoryReplaceScope,
		u.memoryReplaceMinSimilarity,
		u.memoryReplaceHardDedupeCosineThreshold,
	)
	if err != nil {
		return nil, err
	}
	currentReviewDate := formatPostActionReviewerDate(reviewTime)
	currentReviewDateTime := formatPostActionReviewerDateTime(reviewTime)
	for idx := range reviewBuild.Candidates {
		reviewBuild.Candidates[idx].CandidateDateTime = currentReviewDateTime
		reviewBuild.Candidates[idx].CandidateDate = currentReviewDate
	}
	if u.candidateReviewer == nil {
		return buildDirectWriteFallbackDecisionsWithHardDedupe(len(pending), reviewBuild.HardDropped), nil
	}
	reviewPartition := partitionMemoryReviewCandidatesForReviewer(reviewBuild.Candidates, reviewBuild.HardDropped)
	if len(reviewPartition.ReviewerCandidates) == 0 {
		return buildDirectWriteFallbackDecisionsWithHardDedupe(len(pending), reviewBuild.HardDropped), nil
	}
	reviewed, err := u.candidateReviewer.Review(ctx, logicdomain.PostActionCandidateReviewInput{
		UserInputKind:       logicdomain.TurnAnalysisUserInputStatement,
		CurrentTimestamp:    reviewTime.UnixMilli(),
		CurrentTurnDateTime: currentReviewDateTime,
		CurrentTurnDate:     currentReviewDate,
		MemoryCandidates:    reviewPartition.ReviewerCandidates,
	})
	if err != nil {
		u.logDirectWriteReviewerInvalidOutput(session, len(reviewPartition.ReviewerCandidates), err)
		return nil, err
	}
	remappedSection, err := remapPostActionMemoryReviewSectionToOriginal(reviewed.Memory, reviewPartition.ReviewerToOriginal)
	if err != nil {
		u.logDirectWriteReviewerInvalidOutput(session, len(reviewPartition.ReviewerCandidates), err)
		return nil, err
	}
	fullSection, err := mergePostActionMemoryReviewSectionWithHardDropped(len(reviewBuild.Candidates), remappedSection, reviewBuild.HardDropped)
	if err != nil {
		u.logDirectWriteReviewerInvalidOutput(session, len(reviewPartition.ReviewerCandidates), err)
		return nil, err
	}
	decisions, err := buildDirectWriteMemoryDecisions(reviewBuild.Candidates, fullSection, reviewBuild.HardDropped)
	if err != nil {
		u.logDirectWriteReviewerInvalidOutput(session, len(reviewPartition.ReviewerCandidates), err)
		return nil, err
	}
	return decisions, nil
}

// logDirectWriteReviewerInvalidOutput records structured reviewer contract failures on the server side so direct-write callers do not need raw model output in their gRPC response.
// logDirectWriteReviewerInvalidOutput 用于在服务端记录结构化 reviewer 契约失败，避免 direct-write 调用方需要在 gRPC 响应中接收原始模型输出。
func (u *MemoryUseCase) logDirectWriteReviewerInvalidOutput(session logicdomain.SessionRef, reviewerCandidateCount int, err error) {
	if u == nil || u.logger == nil || err == nil {
		return
	}
	var invalid logicdomain.InvalidLLMOutputError
	if !errors.As(err, &invalid) {
		return
	}
	fields := []any{
		"session_key", session.SessionKey,
		"session_id", session.SessionID,
		"user_id", session.UserID,
		"project_id", session.ProjectID,
		"reviewer_candidate_count", reviewerCandidateCount,
	}
	if scene := strings.TrimSpace(invalid.Scene); scene != "" {
		fields = append(fields, "llm_scene", scene)
	}
	if u.candidateReviewer != nil {
		if model := strings.TrimSpace(u.candidateReviewer.ReviewModel()); model != "" {
			fields = append(fields, "model", model)
		}
	}
	// Direct-write clients should only receive the stable error summary; operators need the raw provider body in server logs to repair reviewer prompts or parser contracts.
	// direct-write 客户端只应收到稳定错误摘要；运维需要在服务端日志中查看原始 provider 响应，以修复 reviewer 提示词或解析契约。
	if raw := strings.TrimSpace(invalid.Raw); raw != "" {
		fields = append(fields, "llm_raw_output", invalid.Raw)
	}
	fields = append(fields, "err", err)
	u.logger.Error("direct memory candidate reviewer output invalid", fields...)
}

// buildDirectWriteFallbackDecisions degrades pending direct writes into unconditional persistence when semantic replacement review is unavailable.
// buildDirectWriteFallbackDecisions 用于在语义替代评审不可用时，把待写主动记忆退化为全部直接持久化。
func buildDirectWriteFallbackDecisions(count int) []directWriteMemoryDecision {
	if count <= 0 {
		return nil
	}
	return make([]directWriteMemoryDecision, count)
}

// buildDirectWriteFallbackDecisionsWithHardDedupe keeps direct-write fallback behavior conservative while still honoring any local hard-dedupe matches discovered before reviewer execution.
// buildDirectWriteFallbackDecisionsWithHardDedupe 用于在 direct-write 走回退路径时保持保守新建策略，同时保留 reviewer 前本地硬排重已发现的 dedupe 命中。
func buildDirectWriteFallbackDecisionsWithHardDedupe(count int, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) []directWriteMemoryDecision {
	decisions := buildDirectWriteFallbackDecisions(count)
	for idx, dropped := range hardDropped {
		if idx < 0 || idx >= len(decisions) {
			continue
		}
		decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dropped.DedupeMemoryID}
	}
	return decisions
}

// buildDirectWriteMemoryDecisions converts the shared reviewer result into direct-write actions, while allowing locally proven hard-dedupe drops to bypass the reviewer-only visibility validation that exists solely for LLM-originated dedupe ids.
// buildDirectWriteMemoryDecisions 用于把共享 reviewer 结果转换成主动写入动作；同时允许本地已证实的 hard dedupe 丢弃结果跳过 reviewer 专属的可见性校验，因为该校验只适用于 LLM 产出的 dedupe id。
func buildDirectWriteMemoryDecisions(candidates []logicdomain.PostActionMemoryReviewCandidate, section *logicdomain.PostActionMemoryReviewSection, hardDropped map[int]logicdomain.PostActionDroppedMemoryCandidate) ([]directWriteMemoryDecision, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if section == nil {
		return nil, logicdomain.InvalidLLMOutputError{Scene: "postaction_l2_main", Message: "missing memory review result"}
	}
	acceptedByIndex := make(map[int]logicdomain.PostActionAcceptedMemoryCandidate, len(section.AcceptedCandidates)+len(section.AcceptedCandidateIndexes))
	for _, accepted := range section.AcceptedCandidates {
		acceptedByIndex[accepted.CandidateIndex] = accepted
	}
	for _, idx := range section.AcceptedCandidateIndexes {
		if _, ok := acceptedByIndex[idx]; ok {
			continue
		}
		acceptedByIndex[idx] = logicdomain.PostActionAcceptedMemoryCandidate{CandidateIndex: idx}
	}
	droppedByIndex := make(map[int]logicdomain.PostActionDroppedMemoryCandidate, len(section.DroppedCandidates)+len(section.DroppedCandidateIndexes))
	for _, dropped := range section.DroppedCandidates {
		droppedByIndex[dropped.CandidateIndex] = dropped
	}
	for _, idx := range section.DroppedCandidateIndexes {
		if _, ok := droppedByIndex[idx]; ok {
			continue
		}
		droppedByIndex[idx] = logicdomain.PostActionDroppedMemoryCandidate{CandidateIndex: idx}
	}
	decisions := make([]directWriteMemoryDecision, len(candidates))
	for idx, candidate := range candidates {
		// Local hard-dedupe targets come from the full recalled hit set and real-cosine comparison rather than the reviewer-visible SimilarMemories subset,
		// so they must not be rejected by the LLM-output validation that only exists to constrain reviewer-authored dedupe ids.
		// 本地 hard dedupe 目标来自完整召回命中集与真实 cosine 比较，而不是 reviewer 可见的 SimilarMemories 子集，
		// 因此不能再被只用于约束 reviewer 输出 dedupe id 的那层校验误伤。
		if dropped, ok := hardDropped[idx]; ok {
			decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dropped.DedupeMemoryID}
			continue
		}
		if accepted, ok := acceptedByIndex[idx]; ok {
			supersedeMemoryIDs, err := validatePostActionAcceptedSupersedeMemoryIDs(
				"postaction_l2_main",
				accepted.CandidateIndex,
				candidate.SimilarMemories,
				accepted.SupersedeMemoryIDs,
			)
			if err != nil {
				return nil, err
			}
			decisions[idx] = directWriteMemoryDecision{SupersedeMemoryIDs: supersedeMemoryIDs}
			continue
		}
		if dropped, ok := droppedByIndex[idx]; ok {
			dedupeMemoryID, err := validatePostActionDroppedDedupeMemoryID(
				"postaction_l2_main",
				dropped.CandidateIndex,
				candidate.SimilarMemories,
				dropped.DedupeMemoryID,
			)
			if err != nil {
				return nil, err
			}
			decisions[idx] = directWriteMemoryDecision{DedupedExistingMemoryID: dedupeMemoryID}
			continue
		}
		decisions[idx] = directWriteMemoryDecision{}
	}
	return decisions, nil
}

// loadDirectWriteDedupedExistingRows batches the semantic-dedupe targets chosen by reviewer drops and keeps only still-active hot-path rows so direct-write results never point at retired memories.
// loadDirectWriteDedupedExistingRows 用于批量加载 reviewer 丢弃后选择复用的语义去重目标，并只保留仍处于热路径的 active 行，避免主动写入结果指向已退役记忆。
func (u *MemoryUseCase) loadDirectWriteDedupedExistingRows(ctx context.Context, decisions []directWriteMemoryDecision) (map[uint64]logicdomain.MemoryNodeRecord, error) {
	if len(decisions) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}
	memoryIDs := make([]uint64, 0, len(decisions))
	seen := make(map[uint64]struct{}, len(decisions))
	for _, decision := range decisions {
		if decision.DedupedExistingMemoryID == 0 {
			continue
		}
		if _, ok := seen[decision.DedupedExistingMemoryID]; ok {
			continue
		}
		seen[decision.DedupedExistingMemoryID] = struct{}{}
		memoryIDs = append(memoryIDs, decision.DedupedExistingMemoryID)
	}
	if len(memoryIDs) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}, nil
	}
	rows, err := u.memories.LoadMemoryNodesByIDs(ctx, memoryIDs)
	if err != nil {
		return nil, err
	}
	return indexActiveUnexpiredMemoryRowsByID(rows, time.Now().UTC()), nil
}

// memoryNodeRecordIsActiveUnexpiredAt keeps direct-write semantic dedupe aligned with the runtime hot-path contract so stale or retired rows are never returned as reusable targets.
// memoryNodeRecordIsActiveUnexpiredAt 用于让主动写语义去重与运行时热路径契约保持一致，避免把陈旧或已退役的记忆行当成可复用目标返回。
func memoryNodeRecordIsActiveUnexpiredAt(row logicdomain.MemoryNodeRecord, now time.Time) bool {
	return logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, now)
}

// indexActiveUnexpiredMemoryRowsByID builds one id-indexed lookup for hot-path memory rows while dropping any record that has already left the active+unexpired window before materialization finishes.
// indexActiveUnexpiredMemoryRowsByID 用于为热路径记忆行构建按 id 索引的查找表，并在回表完成前丢弃已经退出 active+unexpired 窗口的记录。
func indexActiveUnexpiredMemoryRowsByID(rows []logicdomain.MemoryNodeRecord, now time.Time) map[uint64]logicdomain.MemoryNodeRecord {
	if len(rows) == 0 {
		return map[uint64]logicdomain.MemoryNodeRecord{}
	}
	indexed := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		if !memoryNodeRecordIsActiveUnexpiredAt(row, now) {
			continue
		}
		indexed[row.ID] = row
	}
	return indexed
}

// assignDirectWriteResultItems fans one canonical direct-write outcome back to every original caller position that collapsed into the same in-request dedupe bucket.
// assignDirectWriteResultItems 用于把一条规范化后的主动写入结果回填到同一请求内折叠到同一去重桶的所有原始位置。
func assignDirectWriteResultItems(results []WriteMemoryResultItem, originalIndexes []int, canonical WriteMemoryResultItem) {
	if len(results) == 0 || len(originalIndexes) == 0 {
		return
	}
	for copyIdx, originalIndex := range originalIndexes {
		if originalIndex < 0 || originalIndex >= len(results) {
			continue
		}
		item := canonical
		if copyIdx > 0 && !item.Deduped {
			item.Deduped = true
		}
		results[originalIndex] = item
	}
}

// prepareDirectWriteCreateVectors batches embeddings for all direct-write candidates that still need a fresh durable row after semantic dedupe resolution, so one request pays at most one embedding round-trip for its create set.
// prepareDirectWriteCreateVectors 用于为语义去重判定后仍需新建的主动写入候选批量生成向量，让单次请求对“新建集合”最多只支付一次 embedding 往返。
func (u *MemoryUseCase) prepareDirectWriteCreateVectors(ctx context.Context, session logicdomain.SessionRef, pending []directWritePendingItem, decisions []directWriteMemoryDecision, dedupedExistingRows map[uint64]logicdomain.MemoryNodeRecord) (map[int][]float32, error) {
	out := make(map[int][]float32, len(pending))
	if len(pending) == 0 {
		return out, nil
	}
	createIndexes := make([]int, 0, len(pending))
	createTexts := make([]string, 0, len(pending))
	for idx, pendingItem := range pending {
		decision := decisions[idx]
		if decision.DedupedExistingMemoryID > 0 {
			if _, ok := dedupedExistingRows[decision.DedupedExistingMemoryID]; ok {
				continue
			}
			// Degrade stale dedupe targets into a fresh write so a concurrent retirement window cannot turn one explicit tool write into a hard failure or a stale memory ref.
			// 当 dedupe 目标在并发窗口内失效时，退化成新建，避免显式工具写入因为竞争时序直接失败或返回陈旧记忆引用。
			if u != nil && u.logger != nil {
				u.logger.Warn("direct memory semantic dedupe target became unavailable; falling back to create",
					"session_id", session.SessionID,
					"dedupe_memory_id", decision.DedupedExistingMemoryID,
				)
			}
		}
		createIndexes = append(createIndexes, idx)
		createTexts = append(createTexts, pendingItem.Item.Abstract)
	}
	if len(createTexts) == 0 {
		return out, nil
	}
	resp, err := u.embedding.Embed(ctx, appports.EmbeddingRequest{Texts: createTexts})
	if err != nil {
		return nil, err
	}
	if err := resp.ValidateStrict(len(createTexts)); err != nil {
		return nil, err
	}
	for idx, pendingIndex := range createIndexes {
		out[pendingIndex] = append([]float32(nil), resp.Vectors[idx]...)
	}
	return out, nil
}

// persistDirectWriteMemory writes one accepted direct-write item using a precomputed embedding vector, then persists the unified memory row together with any same-scope replacements.
// persistDirectWriteMemory 用于使用预先生成好的 embedding 向量写入一条已接纳的主动记忆，并连同同作用域替代结果一起持久化统一记忆行。
func (u *MemoryUseCase) persistDirectWriteMemory(ctx context.Context, session logicdomain.SessionRef, item WriteMemoryItem, dedupeHash string, supersedeMemoryIDs []uint64, now time.Time, vectorPayload []float32) (WriteMemoryResultItem, error) {
	applier, canApplyDirectWrite := u.memories.(directMemoryWriteApplier)
	if len(supersedeMemoryIDs) > 0 && !canApplyDirectWrite {
		return WriteMemoryResultItem{}, fmt.Errorf("memory store does not support atomic direct-memory replacement writes")
	}

	if len(vectorPayload) == 0 {
		return WriteMemoryResultItem{}, fmt.Errorf("direct memory vector payload is required")
	}
	vectorID, err := generatePostActionUUID()
	if err != nil {
		return WriteMemoryResultItem{}, err
	}

	record := logicdomain.MemoryRecord{
		ID:           vectorID,
		Text:         item.Abstract,
		Vector:       append([]float32(nil), vectorPayload...),
		Filter:       buildDirectMemoryFilter(session, item.ScopeLevel),
		SourceTurnID: 0,
		Status:       logicdomain.MemoryStatusActive,
		ExpiresAt:    item.ExpiresAt,
		Metadata: map[string]string{
			"category":     strconv.Itoa(item.Category),
			"details":      item.Details,
			"source_kind":  logicdomain.MemorySourceKindLabel(logicdomain.MemorySourceKindGRPCAIWrite),
			"scope_level":  logicdomain.MemoryScopeLevelLabel(item.ScopeLevel),
			"priority":     strconv.Itoa(item.Priority),
			"memory_level": strconv.Itoa(item.MemoryLevel),
		},
		CreatedAt: now,
	}
	if err := u.vector.Upsert(ctx, record); err != nil {
		return WriteMemoryResultItem{}, err
	}

	memoryRecord := logicdomain.MemoryNodeRecord{
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.SessionID,
		VectorID:        vectorID,
		Vector:          append([]float32(nil), vectorPayload...),
		SourceKind:      logicdomain.MemorySourceKindGRPCAIWrite,
		ScopeLevel:      item.ScopeLevel,
		Category:        item.Category,
		Abstract:        item.Abstract,
		Details:         item.Details,
		Status:          logicdomain.MemoryStatusActive,
		Priority:        item.Priority,
		MemoryLevel:     item.MemoryLevel,
		RefreshWeight:   1,
		ExpiresAt:       item.ExpiresAt,
		DedupeHash:      dedupeHash,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	var created logicdomain.MemoryNodeRecord
	var supersededVectorIDs []string
	if canApplyDirectWrite {
		applyResult, err := applier.ApplyDirectMemoryWrite(ctx, session, memoryRecord, supersedeMemoryIDs)
		if err != nil {
			if !logicdomain.IsFreshVectorReferenceUncertain(err) {
				u.rollbackDirectWriteVector(vectorID, session.SessionID)
			} else if u.logger != nil {
				u.logger.Warn("direct memory relational outcome uncertain; keeping fresh vector for possible durable memory row", "session_id", session.SessionID, "vector_id", vectorID, "err", err)
			}
			return WriteMemoryResultItem{}, err
		}
		created = applyResult.InsertedMemoryNode
		supersededVectorIDs = normalizeVectorGCIDs(applyResult.SupersededVectorIDs)
	} else {
		created, err = u.memories.CreateDirectMemoryNode(ctx, session, memoryRecord)
		if err != nil {
			if !logicdomain.IsFreshVectorReferenceUncertain(err) {
				u.rollbackDirectWriteVector(vectorID, session.SessionID)
			} else if u.logger != nil {
				u.logger.Warn("direct memory fallback outcome uncertain; keeping fresh vector for possible durable memory row", "session_id", session.SessionID, "vector_id", vectorID, "err", err)
			}
			return WriteMemoryResultItem{}, err
		}
	}
	if len(supersededVectorIDs) > 0 {
		cleanupCtx, cleanupCancel := newPostCommitVectorCleanupContext()
		defer cleanupCancel()
		if _, deleteErr := u.vector.DeleteByIDs(cleanupCtx, supersededVectorIDs); deleteErr != nil {
			if u.logger != nil {
				u.logger.Error("direct memory superseded vector cleanup failed", "session_id", session.SessionID, "err", deleteErr)
			}
			enqueueVectorGCCompensation(cleanupCtx, u.memories, u.logger, logicdomain.VectorGCJobTypeDirectWriteSupersedeCleanup, supersededVectorIDs, now,
				"session_id", session.SessionID,
			)
		}
	}
	return WriteMemoryResultItem{
		Ref: logicdomain.MemoryRef{
			Type: logicdomain.MemoryRefTypeMemory,
			ID:   created.ID,
		},
		SourceKind: created.SourceKind,
		ScopeLevel: created.ScopeLevel,
		Deduped:    false,
	}, nil
}

// rollbackDirectWriteVector removes one freshly inserted vector row when the relational write failed after vector persistence succeeded.
// rollbackDirectWriteVector 用于在向量已落库但关系写入失败时，回滚刚插入的新向量行。
func (u *MemoryUseCase) rollbackDirectWriteVector(vectorID string, sessionID uint64) {
	if u == nil || u.vector == nil || strings.TrimSpace(vectorID) == "" {
		return
	}
	cleanupCtx, cleanupCancel := newPostCommitVectorCleanupContext()
	defer cleanupCancel()
	if _, rollbackErr := u.vector.DeleteByIDs(cleanupCtx, []string{vectorID}); rollbackErr != nil {
		if u.logger != nil {
			u.logger.Error("direct memory vector rollback failed", "vector_id", vectorID, "session_id", sessionID, "err", rollbackErr)
		}
		enqueueVectorGCCompensation(cleanupCtx, u.memories, u.logger, logicdomain.VectorGCJobTypeDirectWriteRollback, []string{vectorID}, time.Now().UTC(),
			"vector_id", vectorID,
			"session_id", sessionID,
		)
	}
}

// validateWriteMemoriesCommand checks the resolved scope and direct-write candidates before soft idempotency and embedding begin.
// validateWriteMemoriesCommand 用于在软幂等和 embedding 开始前校验已解析范围与主动写入候选。
func validateWriteMemoriesCommand(cmd WriteMemoriesCommand) error {
	if cmd.Session.SessionID == 0 || strings.TrimSpace(cmd.Session.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if cmd.Session.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}
	if cmd.Session.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if len(cmd.Items) == 0 {
		return logicdomain.ValidationError{Field: "items", Message: "must contain at least one item"}
	}
	if len(cmd.Items) > maxWriteMemoryItems {
		return logicdomain.ValidationError{Field: "items", Message: fmt.Sprintf("must contain at most %d items", maxWriteMemoryItems)}
	}
	for idx, item := range cmd.Items {
		if strings.TrimSpace(item.Abstract) == "" {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].abstract", Message: "is required"}
		}
		if strings.TrimSpace(item.Details) == "" {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].details", Message: "is required"}
		}
		if !logicdomain.ValidMemoryNodeCategory(item.Category) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].category", Message: "must be one supported memory category"}
		}
		if item.ScopeLevel >= 0 && !logicdomain.ValidMemoryScopeLevel(item.ScopeLevel) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].scope_level", Message: "must be one supported memory scope"}
		}
		if item.Priority >= 0 && !logicdomain.ValidMemoryPriority(item.Priority) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].priority", Message: "must be one supported memory priority"}
		}
		if item.MemoryLevel >= 0 && !logicdomain.ValidMemoryLevel(item.MemoryLevel) {
			return logicdomain.ValidationError{Field: "items[" + strconv.Itoa(idx) + "].memory_level", Message: "must be one supported memory level"}
		}
	}
	return nil
}

// normalizeMemoryQueries converts the caller-supplied query list into the strict normalized item format expected by the retrieval pipeline.
// normalizeMemoryQueries 用于把调用方提供的查询列表转换成检索链路期望的严格规范化条目格式。

func normalizeWriteMemoryItem(item WriteMemoryItem, now time.Time) WriteMemoryItem {
	item.Abstract = strings.TrimSpace(item.Abstract)
	item.Details = strings.TrimSpace(item.Details)
	if !logicdomain.ValidMemoryScopeLevel(item.ScopeLevel) {
		item.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(item.Priority) {
		item.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(item.MemoryLevel) {
		switch item.ScopeLevel {
		case logicdomain.MemoryScopeLevelSession:
			item.MemoryLevel = logicdomain.MemoryLevelSession
		default:
			item.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if item.ExpiresAt.IsZero() {
		item.ExpiresAt = now.Add(defaultMemoryTTL(item.ScopeLevel))
	}
	return item
}

// defaultMemoryTTL returns the default retention window for one direct-write scope.
// defaultMemoryTTL 用于返回某个主动写记忆作用域的默认保留时长。
func defaultMemoryTTL(scopeLevel int) time.Duration {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return defaultSessionMemoryTTL
	case logicdomain.MemoryScopeLevelUser:
		return defaultUserMemoryTTL
	default:
		return defaultProjectMemoryTTL
	}
}

// buildDirectMemoryDedupeHash builds the current short-window soft-idempotency hash from the fields that define one semantically identical direct write.
// buildDirectMemoryDedupeHash 用于基于定义“同一短窗口内语义等价主动写入”的字段，构造当前版本的软幂等哈希。
func buildDirectMemoryDedupeHash(session logicdomain.SessionRef, item WriteMemoryItem, now time.Time) string {
	body := strings.Join([]string{
		strconv.Itoa(logicdomain.MemorySourceKindGRPCAIWrite),
		strconv.Itoa(item.ScopeLevel),
		strconv.FormatUint(session.SessionID, 10),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
		strconv.Itoa(item.Category),
		strconv.Itoa(item.Priority),
		strconv.Itoa(item.MemoryLevel),
		buildDirectMemoryExpiryDedupeSignature(item.ScopeLevel, item.ExpiresAt, now),
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// buildDirectMemoryRequestCollapseKey keeps one-RPC duplicate collapse slightly stricter than the storage dedupe hash by preserving the exact expiry timestamp inside the current batch.
// buildDirectMemoryRequestCollapseKey 用于让单个 RPC 内的重复折叠比存储层软幂等再严格一点：它会保留当前批次中的精确过期时间戳，避免同批显式不同过期点被误并。
func buildDirectMemoryRequestCollapseKey(item WriteMemoryItem) string {
	expiresAt := ""
	if !item.ExpiresAt.IsZero() {
		expiresAt = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	body := strings.Join([]string{
		strconv.Itoa(item.ScopeLevel),
		normalizeHashText(item.Abstract),
		normalizeHashText(item.Details),
		strconv.Itoa(item.Category),
		strconv.Itoa(item.Priority),
		strconv.Itoa(item.MemoryLevel),
		expiresAt,
	}, "\n")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// buildDirectMemoryExpiryDedupeSignature keeps default TTL-based writes stable across separate RPCs while still distinguishing explicit absolute-expiry writes from each other.
// buildDirectMemoryExpiryDedupeSignature 用于让基于默认 TTL 的主动写入在不同 RPC 之间仍保持稳定幂等，同时继续区分显式指定的绝对过期时间。
func buildDirectMemoryExpiryDedupeSignature(scopeLevel int, expiresAt, baseTime time.Time) string {
	if expiresAt.IsZero() {
		return "none"
	}
	defaultTTL := defaultMemoryTTL(scopeLevel)
	if !baseTime.IsZero() && durationWithin(expiresAt.Sub(baseTime), defaultTTL, time.Second) {
		return "ttl:" + strconv.FormatInt(int64(defaultTTL/time.Second), 10)
	}
	return "at:" + expiresAt.UTC().Format(time.RFC3339Nano)
}

// durationWithin provides a tiny tolerance so timestamp serialization or database precision cannot break equality for TTL-derived expiries.
// durationWithin 用于提供一个很小的容差，避免时间序列化或数据库精度差异打破基于 TTL 的过期时间等价判断。
func durationWithin(left, right, tolerance time.Duration) bool {
	delta := left - right
	if delta < 0 {
		delta = -delta
	}
	return delta <= tolerance
}

// normalizeHashText keeps soft-idempotency stable across harmless whitespace drift while still distinguishing materially different content.
// normalizeHashText 用于在软幂等中吸收无害的空白差异，同时保留对实质内容变化的区分能力。
func normalizeHashText(text string) string {
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(text)))
	return strings.Join(fields, " ")
}

// buildDirectMemoryFilter derives the vector filter written onto direct-memory rows according to their declared scope level.
// buildDirectMemoryFilter 用于根据主动记忆声明的作用域等级，推导写入向量行时携带的过滤条件。
func buildDirectMemoryFilter(session logicdomain.SessionRef, scopeLevel int) logicdomain.SearchFilter {
	filter := logicdomain.SearchFilter{
		UserID:    session.UserID,
		TeamID:    session.TeamID,
		SpaceID:   session.SpaceID,
		ProjectID: session.ProjectID,
	}
	if scopeLevel == logicdomain.MemoryScopeLevelSession {
		filter.SessionID = session.SessionID
	}
	return filter
}
