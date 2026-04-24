// postaction_analysis.go contains the queued single-turn analysis application, vector persistence, and redacted analysis logging used by the async post-action pipeline.
// postaction_analysis.go 用于承载异步 post-action 流水线中的单轮分析落地、向量持久化以及脱敏分析日志逻辑。
package usecase

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// normalizeTurnProfileNodes resets freshly extracted profile nodes to pending so a later merge step can deterministically flip only the chosen indexes.
// normalizeTurnProfileNodes 用于把新提取出的画像节点统一重置为 pending，便于后续合并步骤只修改被选中的索引。
func normalizeTurnProfileNodes(analysis *logicdomain.TurnAnalysis) {
	if analysis == nil {
		return
	}
	for idx := range analysis.ProfileNodes {
		analysis.ProfileNodes[idx].Status = logicdomain.ProfileStatusPending
	}
}

// clonePostActionTurnAnalysis deep-copies one analyzer result before the unified reviewer runs so degraded paths can safely fall back to the pre-review persistence payload.
// clonePostActionTurnAnalysis 用于在统一 reviewer 执行前深拷贝一份分析器结果，确保降级路径可以安全回退到评审前的持久化载荷。
func clonePostActionTurnAnalysis(analysis logicdomain.TurnAnalysis) logicdomain.TurnAnalysis {
	cloned := analysis
	if len(analysis.MemoryNodes) > 0 {
		cloned.MemoryNodes = append([]logicdomain.MemoryNodeCandidate(nil), analysis.MemoryNodes...)
		for idx := range cloned.MemoryNodes {
			cloned.MemoryNodes[idx].Vector = append([]float32(nil), analysis.MemoryNodes[idx].Vector...)
			cloned.MemoryNodes[idx].SupersedeMemoryIDs = append([]uint64(nil), analysis.MemoryNodes[idx].SupersedeMemoryIDs...)
			cloned.MemoryNodes[idx].ContextEdges = append([]logicdomain.MemoryContextEdgeCandidate(nil), analysis.MemoryNodes[idx].ContextEdges...)
		}
	}
	cloned.ProfileNodes = clonePostActionProfileNodes(analysis.ProfileNodes)
	return cloned
}

// applyImmediateTurnAnalysis assembles the reference-aware single-turn request, runs the LLM, reviews fresh profile candidates, persists vectors, and writes the final result back onto the new turn.
// applyImmediateTurnAnalysis 用于组装参考感知的单轮请求、执行 LLM、评审新的画像候选、持久化向量，并把最终结果回写到新 turn 上。
func (u *PostActionUseCase) applyImmediateTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord) error {
	if u == nil {
		return nil
	}
	input, analysisCutoff, resolvedTurnCreatedAt, err := u.buildTurnAnalysisInputWithResolvedTime(ctx, session, turn, rawTurn)
	if err != nil {
		return err
	}
	analysis, err := u.turnAnalyzer.Analyze(ctx, input)
	if err != nil {
		return err
	}
	if err := validateTurnAnalysis(input, analysis); err != nil {
		return err
	}
	compaction := newPostActionCompactionStats(analysis)

	// Reset fresh profile nodes to pending before any later review so both the normal path and the degraded path persist a deterministic pre-review lifecycle state.
	// 在后续评审开始前先把新画像节点重置为 pending，确保正常路径与降级路径都能持久化一致的评审前生命周期状态。
	normalizeTurnProfileNodes(&analysis)

	// Run the first-pass admission filter before any later review so obvious QA echoes and non-durable external states never enter dedupe or profile-merge flows.
	// 在进入后续评审前先执行首轮准入过滤，确保明显的问答回显和不具持久性的外部状态不会进入去重或画像合并流程。
	applyPostActionAdmissionFilter(&analysis, &compaction)
	reviewFallback := clonePostActionTurnAnalysis(analysis)
	fallbackCompaction := compaction

	// Apply one unified post-action review so memory dedupe and profile acceptance can share the same turn-level reasoning context.
	// 执行一次统一的 post-action 评审，让记忆去重与画像接纳共享同一轮语义上下文。
	if err := u.reviewTurnCandidatesWithResolvedTime(ctx, session, turn, rawTurn, resolvedTurnCreatedAt, &analysis, &compaction); err != nil {
		if u.logger != nil {
			u.logger.Warn(
				"post-action candidate review degraded to analyzer output",
				"session_key", session.SessionKey,
				"session_id", session.SessionID,
				"turn_id", turn.ID,
				"err", err,
			)
		}
		analysis = reviewFallback
		compaction = fallbackCompaction
		compaction.ExternalResearchKeptCount = countPostActionExternalResearchCandidates(analysis)
	}
	compaction.FinalMemoryNodes = len(analysis.MemoryNodes)
	compaction.FinalProfileNodes = len(analysis.ProfileNodes)

	vectorIDs, err := u.persistMemoryNodeVectors(ctx, session, turn, resolvedTurnCreatedAt, &analysis)
	if err != nil {
		return err
	}
	applyResult, err := u.store.ApplyTurnAnalysis(ctx, session, turn, analysis)
	if err != nil {
		if len(vectorIDs) > 0 && u.vector != nil {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer rollbackCancel()
			if _, rollbackErr := u.vector.DeleteByIDs(rollbackCtx, vectorIDs); rollbackErr != nil {
				if u.logger != nil {
					u.logger.Error("post-action turn vector rollback failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", rollbackErr)
				}
				enqueueVectorGCCompensation(rollbackCtx, u.store, u.logger, logicdomain.VectorGCJobTypeTurnAnalysisRollback, vectorIDs, time.Now().UTC(),
					"session_key", session.SessionKey,
					"session_id", session.SessionID,
					"turn_id", turn.ID,
				)
			}
		}
		return err
	}
	if err := u.store.AdvanceSessionExtractWindow(ctx, session.SessionID, analysisCutoff, analysisCutoff); err != nil {
		return err
	}
	if len(applyResult.SupersededVectorIDs) > 0 && u.vector != nil {
		if _, deleteErr := u.vector.DeleteByIDs(ctx, applyResult.SupersededVectorIDs); deleteErr != nil {
			if u.logger != nil {
				u.logger.Error("post-action superseded vector cleanup failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", deleteErr)
			}
			enqueueVectorGCCompensation(ctx, u.store, u.logger, logicdomain.VectorGCJobTypeTurnAnalysisSupersedeCleanup, applyResult.SupersededVectorIDs, time.Now().UTC(),
				"session_key", session.SessionKey,
				"session_id", session.SessionID,
				"turn_id", turn.ID,
			)
		}
	}
	u.logPostActionAnalysisResult(session, turn, input, analysis, vectorIDs, compaction)
	return nil
}

// logPostActionAnalysisResult records one redacted debug-level summary of the persisted analysis so operators can diagnose extraction throughput without writing derived user text into runtime logs, while also stripping high-dimensional vectors from debug JSON output.
// logPostActionAnalysisResult 用于在 debug 级别记录一份脱敏后的分析结果摘要，让排障仍能观察提炼吞吐，同时会从调试 JSON 输出中剥离高维向量。
func (u *PostActionUseCase) logPostActionAnalysisResult(session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, input logicdomain.TurnAnalysisInput, analysis logicdomain.TurnAnalysis, vectorIDs []string, compaction postActionCompactionStats) {
	if u == nil || u.logger == nil {
		return
	}
	redactedAnalysis, redactedVectorNodes := redactTurnAnalysisVectorsForLog(analysis)
	fields := []any{
		"session_key", session.SessionKey,
		"session_id", session.SessionID,
		"turn_id", turn.ID,
		"reference_turn_count", len(input.ReferenceTurns),
		"recent_direct_write_count", len(input.RecentGRPCMemoryWrites),
		"vector_count", len(vectorIDs),
		"details_len", len(strings.TrimSpace(analysis.Details)),
		"memory_node_count", len(analysis.MemoryNodes),
		"profile_node_count", len(analysis.ProfileNodes),
		"raw_candidates", compaction.RawCandidates(),
		"final_stored_nodes", compaction.FinalStoredNodes(),
		"compaction_rate", compaction.CompactionRate(),
		"admission_drop_count", compaction.AdmissionDroppedCount,
		"review_drop_count", compaction.ReviewDroppedCount,
		"hard_dedupe_drop_count", compaction.HardDedupeDroppedCount,
		"external_research_kept_count", compaction.ExternalResearchKeptCount,
		"user_profile_merged", analysis.UserProfileMerged,
		"project_profile_merged", analysis.ProjectProfileMerged,
	}
	if redactedVectorNodes > 0 {
		fields = append(fields,
			"vector_payload_notice", "embedding vectors omitted from analysis_json",
			"vector_payload_redacted_nodes", redactedVectorNodes,
		)
	}
	if analysisJSON, err := marshalTurnAnalysisForLog(redactedAnalysis); err == nil {
		if u.logger.PayloadDebugEnabled() {
			fields = append(fields, "analysis_json", string(analysisJSON))
		} else {
			fields = append(fields,
				"analysis_len", len(analysisJSON),
				"analysis_sha256", shortLogDigest(string(analysisJSON)),
			)
		}
	}
	u.logger.Info("post-action turn analysis result", fields...)
}

// redactTurnAnalysisVectorsForLog clones one turn-analysis payload and clears the in-memory vector arrays so debug JSON remains readable and avoids dumping 1024-dimensional embeddings into logs.
// redactTurnAnalysisVectorsForLog 用于复制一份 turn analysis 载荷，并清空其中的内存向量数组，避免调试 JSON 把 1024 维 embedding 整块写入日志。
func redactTurnAnalysisVectorsForLog(analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysis, int) {
	if len(analysis.MemoryNodes) == 0 {
		return analysis, 0
	}
	redacted := analysis
	redacted.MemoryNodes = append([]logicdomain.MemoryNodeCandidate(nil), analysis.MemoryNodes...)
	redactedNodes := 0
	for idx := range redacted.MemoryNodes {
		if len(redacted.MemoryNodes[idx].Vector) == 0 {
			continue
		}
		node := redacted.MemoryNodes[idx]
		node.Vector = nil
		redacted.MemoryNodes[idx] = node
		redactedNodes++
	}
	return redacted, redactedNodes
}

// marshalTurnAnalysisForLog serializes one redacted turn-analysis payload into log JSON while omitting the high-dimensional Vector field entirely.
// marshalTurnAnalysisForLog 用于把一份已脱敏的 turn analysis 载荷序列化成日志 JSON，并彻底省略高维 Vector 字段。
func marshalTurnAnalysisForLog(analysis logicdomain.TurnAnalysis) ([]byte, error) {
	type memoryContextEdgeLogPayload struct {
		ContextKey   string `json:"ContextKey"`
		ContextValue string `json:"ContextValue"`
		Relation     string `json:"Relation"`
	}
	type memoryNodeLogPayload struct {
		Category        int                           `json:"Category"`
		VectorID        string                        `json:"VectorID"`
		Abstract        string                        `json:"Abstract"`
		Details         string                        `json:"Details"`
		EvidenceSource  string                        `json:"EvidenceSource"`
		Admission       string                        `json:"Admission"`
		AdmissionReason string                        `json:"AdmissionReason"`
		ContextEdges    []memoryContextEdgeLogPayload `json:"ContextEdges"`
		SourceKind      int                           `json:"SourceKind"`
		ScopeLevel      int                           `json:"ScopeLevel"`
		Priority        int                           `json:"Priority"`
		MemoryLevel     int                           `json:"MemoryLevel"`
		RefreshWeight   int                           `json:"RefreshWeight"`
		ExpiresAt       time.Time                     `json:"ExpiresAt"`
		DedupeHash      string                        `json:"DedupeHash"`
	}
	type profileNodeLogPayload struct {
		ProfileType      int       `json:"ProfileType"`
		Content          string    `json:"Content"`
		EvidenceSource   string    `json:"EvidenceSource"`
		Admission        string    `json:"Admission"`
		AdmissionReason  string    `json:"AdmissionReason"`
		Status           int       `json:"Status"`
		Priority         int       `json:"Priority"`
		ProfileLevel     int       `json:"ProfileLevel"`
		LevelReason      string    `json:"LevelReason"`
		RefreshWeight    int       `json:"RefreshWeight"`
		ProfileDate      string    `json:"ProfileDate"`
		SourceTurnID     uint64    `json:"SourceTurnID"`
		SourceKind       int       `json:"SourceKind"`
		SourceID         uint64    `json:"SourceID"`
		StatusReason     string    `json:"StatusReason"`
		ExpiresAt        time.Time `json:"ExpiresAt"`
		SupersedeNodeIDs []uint64  `json:"SupersedeNodeIDs"`
	}
	type turnAnalysisLogPayload struct {
		UserInputKind        string                  `json:"UserInputKind"`
		TurnID               uint64                  `json:"TurnID"`
		Details              string                  `json:"Details"`
		DetailsBudget        int                     `json:"DetailsBudget"`
		MemoryNodes          []memoryNodeLogPayload  `json:"MemoryNodes"`
		ProfileNodes         []profileNodeLogPayload `json:"ProfileNodes"`
		UserProfileMerged    bool                    `json:"UserProfileMerged"`
		MergedUserProfile    string                  `json:"MergedUserProfile"`
		ProjectProfileMerged bool                    `json:"ProjectProfileMerged"`
		MergedProjectProfile string                  `json:"MergedProjectProfile"`
	}

	memoryNodes := make([]memoryNodeLogPayload, 0, len(analysis.MemoryNodes))
	for _, node := range analysis.MemoryNodes {
		edges := make([]memoryContextEdgeLogPayload, 0, len(node.ContextEdges))
		for _, edge := range node.ContextEdges {
			edges = append(edges, memoryContextEdgeLogPayload{
				ContextKey:   edge.ContextKey,
				ContextValue: edge.ContextValue,
				Relation:     edge.Relation,
			})
		}
		memoryNodes = append(memoryNodes, memoryNodeLogPayload{
			Category:        node.Category,
			VectorID:        node.VectorID,
			Abstract:        node.Abstract,
			Details:         node.Details,
			EvidenceSource:  node.EvidenceSource,
			Admission:       node.Admission,
			AdmissionReason: node.AdmissionReason,
			ContextEdges:    edges,
			SourceKind:      node.SourceKind,
			ScopeLevel:      node.ScopeLevel,
			Priority:        node.Priority,
			MemoryLevel:     node.MemoryLevel,
			RefreshWeight:   node.RefreshWeight,
			ExpiresAt:       node.ExpiresAt,
			DedupeHash:      node.DedupeHash,
		})
	}

	profileNodes := make([]profileNodeLogPayload, 0, len(analysis.ProfileNodes))
	for _, node := range analysis.ProfileNodes {
		profileNodes = append(profileNodes, profileNodeLogPayload{
			ProfileType:      node.ProfileType,
			Content:          node.Content,
			EvidenceSource:   node.EvidenceSource,
			Admission:        node.Admission,
			AdmissionReason:  node.AdmissionReason,
			Status:           node.Status,
			Priority:         node.Priority,
			ProfileLevel:     node.ProfileLevel,
			LevelReason:      node.LevelReason,
			RefreshWeight:    node.RefreshWeight,
			ProfileDate:      node.ProfileDate,
			SourceTurnID:     node.SourceTurnID,
			SourceKind:       node.SourceKind,
			SourceID:         node.SourceID,
			StatusReason:     node.StatusReason,
			ExpiresAt:        node.ExpiresAt,
			SupersedeNodeIDs: append([]uint64(nil), node.SupersedeNodeIDs...),
		})
	}

	return json.Marshal(turnAnalysisLogPayload{
		UserInputKind:        analysis.UserInputKind,
		TurnID:               analysis.TurnID,
		Details:              analysis.Details,
		DetailsBudget:        analysis.DetailsBudget,
		MemoryNodes:          memoryNodes,
		ProfileNodes:         profileNodes,
		UserProfileMerged:    analysis.UserProfileMerged,
		MergedUserProfile:    analysis.MergedUserProfile,
		ProjectProfileMerged: analysis.ProjectProfileMerged,
		MergedProjectProfile: analysis.MergedProjectProfile,
	})
}

// buildTurnAnalysisInput loads refined reference turns and recent direct-write exclusions, then builds the structured request consumed by the reference-aware single-turn analyzer.
// buildTurnAnalysisInput 用于加载已提炼的参考 turn 与最近直写排斥项，并构建参考感知型单轮分析器需要的结构化请求。
// buildTurnAnalysisInput keeps the historical test-facing signature while delegating the actual time resolution to the resolved-time helper used by the runtime path.
// buildTurnAnalysisInput 用于保留历史测试入口的函数签名，同时把真实时间解析逻辑委托给运行时主路径使用的 resolved-time helper。
func (u *PostActionUseCase) buildTurnAnalysisInput(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord) (logicdomain.TurnAnalysisInput, time.Time, error) {
	input, analysisCutoff, _, err := u.buildTurnAnalysisInputWithResolvedTime(ctx, session, turn, rawTurn)
	return input, analysisCutoff, err
}

// buildTurnAnalysisInputWithResolvedTime builds the analyzer input together with the single resolved turn-created timestamp reused across the whole immediate post-action pipeline.
// buildTurnAnalysisInputWithResolvedTime 用于构建分析器输入，并同时返回整条即时 post-action 流水线复用的唯一 turn 创建时间。
func (u *PostActionUseCase) buildTurnAnalysisInputWithResolvedTime(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, rawTurn logicdomain.TurnRecord) (logicdomain.TurnAnalysisInput, time.Time, time.Time, error) {
	if u == nil || u.store == nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, time.Time{}, fmt.Errorf("post-action relational store is nil")
	}
	analysisCutoff := time.Now().UTC()
	resolvedTurnCreatedAt := choosePostActionCreatedAt(turn, analysisCutoff)

	// Serialize the raw turn into the compact JSON payload used by the analyzer and reuse the same token-budget heuristic to trim reference history.
	// 先把原始 turn 序列化成分析器使用的紧凑 JSON 载荷，并复用同一套 token 预算规则裁剪参考历史。
	targetBody, targetBudget, err := buildPostActionTurnPayload(rawTurn)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, time.Time{}, err
	}
	historyTurns, err := u.store.LoadRecentSessionHistory(ctx, session, u.analysisCfg.HistoryTurns)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, time.Time{}, fmt.Errorf("load recent session history: %w", err)
	}
	selectedHistory := historyTurns
	if u.analysisCfg.MaxInputTokens > 0 {
		remainingBudget := u.analysisCfg.MaxInputTokens - targetBudget
		if remainingBudget < 0 {
			remainingBudget = 0
		}
		selectedHistory = trimHistoryTurnsByBudget(historyTurns, remainingBudget)
	}
	recentDirectWrites, err := u.store.LoadRecentDirectMemoryWrites(ctx, session, session.LastExtractObservedAt, analysisCutoff)
	if err != nil {
		return logicdomain.TurnAnalysisInput{}, time.Time{}, time.Time{}, fmt.Errorf("load recent direct memory writes: %w", err)
	}

	// Convert storage rows into the narrower analyzer input model so the prompt only sees the fields relevant to single-turn context and direct-write exclusion.
	// 把存储行转换成更窄的分析器输入模型，让提示词只看到单轮上下文与直写排斥真正需要的字段。
	input := logicdomain.TurnAnalysisInput{
		CurrentTimestamp:       analysisCutoff.UnixMilli(),
		ReferenceTurns:         make([]logicdomain.TurnAnalysisReferenceTurn, 0, len(selectedHistory)),
		TargetTurn:             logicdomain.TurnAnalysisTargetTurn{TurnID: turn.ID, CreatedTimestamp: resolvedTurnCreatedAt.UnixMilli(), RawTurn: targetBody},
		RecentGRPCMemoryWrites: make([]logicdomain.TurnAnalysisDirectWrite, 0, len(recentDirectWrites)),
	}
	for _, historyTurn := range selectedHistory {
		input.ReferenceTurns = append(input.ReferenceTurns, logicdomain.TurnAnalysisReferenceTurn{
			TurnID:  historyTurn.ID,
			Details: strings.TrimSpace(historyTurn.Details),
		})
	}
	for _, item := range recentDirectWrites {
		input.RecentGRPCMemoryWrites = append(input.RecentGRPCMemoryWrites, logicdomain.TurnAnalysisDirectWrite{
			MemoryID:         item.MemoryID,
			ScopeLevel:       item.ScopeLevel,
			Abstract:         strings.TrimSpace(item.Abstract),
			Details:          strings.TrimSpace(item.Details),
			CreatedTimestamp: item.CreatedTimestamp,
		})
	}
	return input, analysisCutoff, resolvedTurnCreatedAt, nil
}

// validateTurnAnalysis validates the analyzer metadata contract before later review and persistence start.
// validateTurnAnalysis 用于在后续评审与持久化开始前，校验分析器输出的元数据契约。
func validateTurnAnalysis(input logicdomain.TurnAnalysisInput, analysis logicdomain.TurnAnalysis) error {
	if input.TargetTurn.TurnID == 0 {
		return logicdomain.ValidationError{Field: "target_turn.turn_id", Message: "is required"}
	}
	if analysis.TurnID != input.TargetTurn.TurnID {
		return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("unexpected turn_id %d", analysis.TurnID)}
	}
	if !logicdomain.ValidTurnAnalysisUserInputKind(strings.TrimSpace(analysis.UserInputKind)) {
		return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("unexpected user_input_kind %q", analysis.UserInputKind)}
	}
	for idx, node := range analysis.MemoryNodes {
		if !logicdomain.ValidTurnAnalysisEvidenceSource(strings.TrimSpace(node.EvidenceSource)) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("memory_nodes[%d].evidence_source is invalid", idx)}
		}
		if !logicdomain.ValidTurnAnalysisAdmission(strings.TrimSpace(node.Admission)) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("memory_nodes[%d].admission is invalid", idx)}
		}
		reason := strings.TrimSpace(node.AdmissionReason)
		if reason != "" && !logicdomain.ValidTurnAnalysisAdmissionReason(reason) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("memory_nodes[%d].admission_reason is invalid", idx)}
		}
	}
	for idx, node := range analysis.ProfileNodes {
		if !logicdomain.ValidTurnAnalysisEvidenceSource(strings.TrimSpace(node.EvidenceSource)) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("profile_nodes[%d].evidence_source is invalid", idx)}
		}
		if !logicdomain.ValidTurnAnalysisAdmission(strings.TrimSpace(node.Admission)) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("profile_nodes[%d].admission is invalid", idx)}
		}
		reason := strings.TrimSpace(node.AdmissionReason)
		if reason != "" && !logicdomain.ValidTurnAnalysisAdmissionReason(reason) {
			return logicdomain.InvalidLLMOutputError{Scene: "postaction_l1_main", Message: fmt.Sprintf("profile_nodes[%d].admission_reason is invalid", idx)}
		}
	}
	return nil
}

// persistMemoryNodeVectors embeds the extracted memory-node abstracts, writes them to LanceDB, and attaches the resulting vector ids back onto the analysis payload.
// persistMemoryNodeVectors 用于对提炼出的记忆节点摘要生成向量、写入 LanceDB，并把得到的 vector_id 回填到分析结果中。
func (u *PostActionUseCase) persistMemoryNodeVectors(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, createdAt time.Time, analysis *logicdomain.TurnAnalysis) ([]string, error) {
	if analysis == nil || len(analysis.MemoryNodes) == 0 {
		return nil, nil
	}
	if u.embedding == nil {
		return nil, fmt.Errorf("embedding client is nil")
	}
	if u.vector == nil {
		return nil, fmt.Errorf("vector store is nil")
	}

	// Submit the full logical memory-node batch and let the embedding controller isolate only the invalid single texts instead of forcing the use-case layer to reimplement provider batching details.
	// 直接提交完整的逻辑记忆节点批次，并让 embedding 控制器只隔离那些确实无效的单条文本，而不是让用例层重新实现 provider 拆批细节。
	texts := make([]string, 0, len(analysis.MemoryNodes))
	for idx, node := range analysis.MemoryNodes {
		text := strings.TrimSpace(node.Abstract)
		if text == "" {
			return nil, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].abstract", Message: "is required for vector persistence"}
		}
		texts = append(texts, text)
	}
	vectorResults, dropped, err := embedPostActionTexts(ctx, u.embedding, texts)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 && u.logger != nil {
		indexes := make([]int, 0, len(dropped))
		reasons := make([]string, 0, len(dropped))
		for _, item := range dropped {
			indexes = append(indexes, item.Index)
			reasons = append(reasons, item.Reason)
		}
		u.logger.Warn(
			"post-action dropped invalid memory nodes during embedding",
			"session_key", session.SessionKey,
			"session_id", session.SessionID,
			"turn_id", turn.ID,
			"dropped_indexes", indexes,
			"dropped_reasons", reasons,
		)
	}

	// Upsert LanceDB rows first so SQLite only flips extracted_status after the corresponding vectors already exist.
	// 先 upsert LanceDB 行，确保 SQLite 只有在对应向量已存在时才会把 extracted_status 置为完成。
	insertedIDs := make([]string, 0, len(vectorResults))
	rollbackInsertedVectors := func() {
		if len(insertedIDs) == 0 || u.vector == nil {
			return
		}
		// Use a fresh context for rollback to avoid failure when caller's ctx is already cancelled.
		// 回滚使用独立的超时 context，避免调用方 ctx 已取消导致回滚失败。
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rollbackCancel()
		if _, rollbackErr := u.vector.DeleteByIDs(rollbackCtx, insertedIDs); rollbackErr != nil {
			if u.logger != nil {
				u.logger.Error("post-action partial vector rollback failed", "session_key", session.SessionKey, "session_id", session.SessionID, "turn_id", turn.ID, "err", rollbackErr)
			}
			enqueueVectorGCCompensation(rollbackCtx, u.store, u.logger, logicdomain.VectorGCJobTypeTurnAnalysisRollback, insertedIDs, time.Now().UTC(),
				"session_key", session.SessionKey,
				"session_id", session.SessionID,
				"turn_id", turn.ID,
			)
		}
	}
	keptNodes := make([]logicdomain.MemoryNodeCandidate, 0, len(vectorResults))
	memoryDropped := false
	for _, item := range vectorResults {
		if item.Index < 0 || item.Index >= len(analysis.MemoryNodes) {
			rollbackInsertedVectors()
			return nil, fmt.Errorf("post-action embedding result index %d out of range", item.Index)
		}
		node := analysis.MemoryNodes[item.Index]
		vectorID, err := generatePostActionUUID()
		if err != nil {
			rollbackInsertedVectors()
			return nil, err
		}
		node.VectorID = vectorID
		node.Vector = append([]float32(nil), item.Vector...)
		record := logicdomain.MemoryRecord{
			ID:           vectorID,
			Text:         strings.TrimSpace(node.Abstract),
			Vector:       append([]float32(nil), item.Vector...),
			Filter:       buildPostActionMemoryFilter(session),
			SourceTurnID: turn.ID,
			Metadata: map[string]string{
				"turn_id":  strconv.FormatUint(turn.ID, 10),
				"category": strconv.Itoa(node.Category),
				"details":  strings.TrimSpace(node.Details),
			},
			CreatedAt: createdAt,
		}
		if err := u.vector.Upsert(ctx, record); err != nil {
			rollbackInsertedVectors()
			return nil, err
		}
		insertedIDs = append(insertedIDs, vectorID)
		keptNodes = append(keptNodes, node)
	}
	if len(keptNodes) != len(analysis.MemoryNodes) {
		memoryDropped = true
	}
	analysis.MemoryNodes = keptNodes
	if memoryDropped {
		for idx := range analysis.MemoryNodes {
			analysis.MemoryNodes[idx].SupersedeMemoryIDs = normalizePostActionUint64List(analysis.MemoryNodes[idx].SupersedeMemoryIDs)
		}
	}
	return insertedIDs, nil
}

// normalizePostActionUint64List keeps post-action supersede targets stable, unique, and free of zero ids after partial candidate drops.
// normalizePostActionUint64List 用于在 post-action 部分候选被丢弃后，保持 supersede 目标稳定去重，并去掉无效的零值 id。
func normalizePostActionUint64List(values []uint64) []uint64 {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(values))
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// embedPostActionTexts submits one full logical embedding request and returns both successful vectors and intentionally dropped invalid inputs so post-action can skip only the bad memory-node candidates.
// embedPostActionTexts 用于提交一次完整的逻辑 embedding 请求，并同时返回成功向量与被主动丢弃的无效输入，让 post-action 只跳过坏掉的记忆节点候选。
func embedPostActionTexts(ctx context.Context, client appports.EmbeddingClient, texts []string) ([]appports.EmbeddingVectorResult, []appports.EmbeddingDroppedInput, error) {
	if client == nil {
		return nil, nil, fmt.Errorf("embedding client is nil")
	}
	if len(texts) == 0 {
		return []appports.EmbeddingVectorResult{}, []appports.EmbeddingDroppedInput{}, nil
	}
	resp, err := client.Embed(ctx, appports.EmbeddingRequest{
		Texts:                    texts,
		AllowPartialInvalidTexts: true,
	})
	if err != nil {
		return nil, nil, err
	}
	items, err := resp.IndexedVectors(len(texts))
	if err != nil {
		return nil, nil, err
	}
	return items, append([]appports.EmbeddingDroppedInput(nil), resp.Dropped...), nil
}

// buildPostActionMemoryFilter derives the flattened hierarchy scope stored on vector rows for post-action memory nodes.
// buildPostActionMemoryFilter 用于推导 post-action 记忆节点写入向量行时携带的扁平层级范围。
func buildPostActionMemoryFilter(session logicdomain.SessionRef) logicdomain.SearchFilter {
	return logicdomain.SearchFilter{
		UserID:    session.UserID,
		TeamID:    session.TeamID,
		SpaceID:   session.SpaceID,
		ProjectID: session.ProjectID,
		SessionID: session.SessionID,
	}
}

// choosePostActionCreatedAt prefers the persisted turn timestamp so vector rows and SQLite memory nodes share the same approximate origin time.
// choosePostActionCreatedAt 用于优先复用已落库 turn 的时间戳，让向量行和 SQLite 记忆节点共享接近的产生时间。
func choosePostActionCreatedAt(turn logicdomain.PersistedTurnRecord, fallback time.Time) time.Time {
	if !turn.CreatedAt.IsZero() {
		return turn.CreatedAt
	}
	return fallback
}

// generatePostActionUUID creates one random UUID string for the LanceDB row id and SQLite memory-node vector_id link.
// generatePostActionUUID 用于生成随机 UUID 字符串，同时作为 LanceDB 行 id 和 SQLite 记忆节点的 vector_id 关联键。
func generatePostActionUUID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate post-action uuid: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
