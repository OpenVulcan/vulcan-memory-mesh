// SQLite atomic turn analysis persistence for the post-action pipeline.
// SQLite 适配层为 post-action 链路提供逐轮分析原子持久化。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strconv"
	"strings"
	"time"
)

// turnAnalysisProfileInsertExpectation stores the exact profile-node insert target needed to reconcile an uncertain post-action profile write.
// turnAnalysisProfileInsertExpectation 用于保存 post-action 画像写入不确定时所需对账的精确画像节点插入目标。
type turnAnalysisProfileInsertExpectation struct {
	ID          uint64
	TurnID      uint64
	ProfileType int
	BindID      uint64
	Node        logicdomain.ProfileNodeCandidate
	CreatedMs   int64
}

// turnAnalysisProfileSupersedeExpectation stores the target retirement state for one post-action profile supersede statement.
// turnAnalysisProfileSupersedeExpectation 用于保存一条 post-action 画像替代语句的目标退役状态。
type turnAnalysisProfileSupersedeExpectation struct {
	NodeIDs        []uint64
	SupersededByID uint64
	ExpectedRows   int64
}

// turnAnalysisMemoryContextEdgeExpectation stores the exact edge-table state expected after one post-action context-edge statement.
// turnAnalysisMemoryContextEdgeExpectation 用于保存一条 post-action 情境边语句执行后应达到的精确边表状态。
type turnAnalysisMemoryContextEdgeExpectation struct {
	// MemoryID stores the durable memory row whose context edges are being rewritten.
	// MemoryID 保存正在重写情境边的长期 memory 行 ID。
	MemoryID uint64
	// ExpectedEdges stores the complete edge set that must be visible for the memory row after this statement.
	// ExpectedEdges 保存该语句完成后，这条 memory 行下必须可见的完整情境边集合。
	ExpectedEdges []logicdomain.MemoryContextEdge
	// RequireRowsChanged reports whether the statement must affect an exact number of rows when SQLite returns normally.
	// RequireRowsChanged 表示 SQLite 正常返回时是否必须校验精确影响行数。
	RequireRowsChanged bool
	// ExpectedRows stores the exact row count required when RequireRowsChanged is true.
	// ExpectedRows 保存 RequireRowsChanged 为 true 时要求的精确影响行数。
	ExpectedRows int64
}

// ApplyTurnAnalysis writes the extracted turn summary back to the turn row, inserts unified memory/profile nodes, and returns follow-up vector cleanup coordinates.
// ApplyTurnAnalysis 用于把提炼出的 turn 总结回写到 turn 行、插入统一记忆和画像节点，并返回后续向量清理坐标。
func (s *Store) ApplyTurnAnalysis(ctx context.Context, session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, analysis logicdomain.TurnAnalysis) (logicdomain.TurnAnalysisApplyResult, error) {
	if turn.ID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "turn_id", Message: "must refer to one persisted turn"}
	}
	if session.SessionID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if session.ProjectID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "project_id", Message: "must resolve to one persisted project"}
	}
	if session.UserID == 0 {
		return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "user_id", Message: "must resolve to one persisted user"}
	}

	// Serialize analysis writes so the turn status flip, unified memory inserts, and profile writes stay aligned under one deterministic id allocation window.
	// 串行化分析结果写入，确保 turn 状态切换、统一记忆插入和画像写入在同一个确定性 ID 分配窗口内保持一致。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if analysis.DetailsBudget <= 0 {
		analysis.DetailsBudget = storageutil.EstimateTokenBudget(analysis.Details)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	nowRFC3339 := now.Format(time.RFC3339Nano)
	memoryStartID := uint64(0)
	profileStartID := uint64(0)
	var err error
	// Resolve merged profile targets before the first turn-status write so missing scope rows remain clean rollback-safe failures.
	// 在首个 turn 状态写入之前解析合并画像目标，让缺失范围行保持为可安全回滚的干净失败。
	if analysis.UserProfileMerged {
		if _, err := s.loadUserByID(ctx, session.UserID); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load merged user profile target: %w", err)
		}
	}
	if analysis.ProjectProfileMerged {
		if _, err := s.loadProjectByID(ctx, session.ProjectID); err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load merged project profile target: %w", err)
		}
	}
	if len(analysis.MemoryNodes) > 0 {
		memoryStartID, err = s.nextNumericID(ctx, "vmm_memory_nodes")
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("allocate memory node id: %w", err)
		}
	}
	if len(analysis.ProfileNodes) > 0 {
		profileStartID, err = s.nextNumericID(ctx, "vmm_profile_nodes")
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("allocate profile node id: %w", err)
		}
	}

	// Resolve the vector ids for reviewer-approved superseded memory rows before the status flip so the caller can delete those vector rows after SQL commits.
	// 在状态切换前先解析 reviewer 批准的被覆盖记忆 vector_id，供调用方在 SQL 提交后删除对应向量行。
	supersededMemoryIDs := collectTurnAnalysisSupersedeMemoryIDs(analysis.MemoryNodes)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("load superseded vector ids: %w", err)
	}

	turnUpdateStatement := parameterizedTurnAnalysisUpdateStatement(session.SessionID, turn.ID, strings.TrimSpace(analysis.Details), analysis.DetailsBudget, nowMs)
	statements := []sqliteWriteStatement{}
	profileTargetStatementIndexes := map[int]struct{}{}
	memoryInsertStatementRecords := map[int]logicdomain.MemoryNodeRecord{}
	memoryContextEdgeStatementExpectations := map[int]turnAnalysisMemoryContextEdgeExpectation{}
	profileInsertStatementExpectations := map[int]turnAnalysisProfileInsertExpectation{}
	profileSupersedeStatementExpectations := map[int]turnAnalysisProfileSupersedeExpectation{}
	if analysis.UserProfileMerged {
		profileTargetStatementIndexes[len(statements)] = struct{}{}
		statements = append(statements, sqliteWriteStatement{
			SQL:    parameterizedProfileTargetUpdateSQL(logicdomain.ProfileTypeUser),
			Params: []any{strings.TrimSpace(analysis.MergedUserProfile), nowRFC3339, session.UserID},
		})
	}
	if analysis.ProjectProfileMerged {
		profileTargetStatementIndexes[len(statements)] = struct{}{}
		statements = append(statements, sqliteWriteStatement{
			SQL:    parameterizedProfileTargetUpdateSQL(logicdomain.ProfileTypeProject),
			Params: []any{strings.TrimSpace(analysis.MergedProjectProfile), nowRFC3339, session.ProjectID},
		})
	}
	insertedMemoryNodes := make([]logicdomain.MemoryNodeRecord, 0, len(analysis.MemoryNodes))
	for idx, node := range analysis.MemoryNodes {
		if strings.TrimSpace(node.VectorID) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].vector_id", Message: "is required after vector persistence"}
		}
		if !logicdomain.ValidMemoryNodeCategory(node.Category) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "memory_nodes[" + strconv.Itoa(idx) + "].category", Message: "must be one supported memory category"}
		}
		record := normalizeTurnMemoryNodeRecord(session, turn, node, memoryStartID+uint64(idx), now)
		contextEdges := logicdomain.NormalizeMemoryContextEdges(record.ID, node.ContextEdges, now)
		record.SupportCount, record.RebuttalCount = logicdomain.SummarizeMemoryContextEdges(contextEdges)
		memoryInsertStatementRecords[len(statements)] = record
		insertStatement, err := parameterizedMemoryNodeInsertStatement(record)
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, err
		}
		statements = append(statements, insertStatement)
		// Track every delete-then-insert boundary because commit-unknown recovery must know the exact edge-table prefix that should be visible before later inserts run.
		// 跟踪每个先删后插边界，因为提交未知恢复必须知道后续插入执行前边表应可见的精确前缀集合。
		contextEdgeStatements := parameterizedMemoryContextEdgesReplaceStatements(record.ID, contextEdges)
		for contextStatementIndex := range contextEdgeStatements {
			expectedEdges := []logicdomain.MemoryContextEdge(nil)
			requireRowsChanged := false
			expectedRows := int64(0)
			if contextStatementIndex > 0 {
				expectedEdges = append(expectedEdges, contextEdges[:contextStatementIndex]...)
				requireRowsChanged = true
				expectedRows = 1
			}
			memoryContextEdgeStatementExpectations[len(statements)+contextStatementIndex] = turnAnalysisMemoryContextEdgeExpectation{
				MemoryID:           record.ID,
				ExpectedEdges:      expectedEdges,
				RequireRowsChanged: requireRowsChanged,
				ExpectedRows:       expectedRows,
			}
		}
		statements = append(statements, contextEdgeStatements...)
		insertedMemoryNodes = append(insertedMemoryNodes, record)
	}
	for idx, node := range analysis.ProfileNodes {
		if !logicdomain.ValidProfileType(node.ProfileType) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].profile_type", Message: "must be one supported profile type"}
		}
		if strings.TrimSpace(node.Content) == "" {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].content", Message: "is required"}
		}
		if !logicdomain.ValidProfileStatus(node.Status) {
			return logicdomain.TurnAnalysisApplyResult{}, logicdomain.ValidationError{Field: "profile_nodes[" + strconv.Itoa(idx) + "].status", Message: "must be one supported profile status"}
		}
		bindID := session.ProjectID
		if node.ProfileType == logicdomain.ProfileTypeUser {
			bindID = session.UserID
		}
		if strings.TrimSpace(node.ProfileDate) == "" {
			node.ProfileDate = logicdomain.FormatDisplayDate(turn.CreatedAt)
		}
		insertedProfileID := profileStartID + uint64(idx)
		profileInsertStatementExpectations[len(statements)] = turnAnalysisProfileInsertExpectation{
			ID:          insertedProfileID,
			TurnID:      turn.ID,
			ProfileType: node.ProfileType,
			BindID:      bindID,
			Node:        node,
			CreatedMs:   nowMs,
		}
		statements = append(statements, parameterizedProfileNodeInsertStatement(insertedProfileID, &turn.ID, node.ProfileType, bindID, node, nowMs))
		supersedeNodeIDs := storageutil.NormalizeUint64List(node.SupersedeNodeIDs)
		// Retire reviewer-approved predecessor nodes right after the replacement insert so rendered profile readers do not see both facts as active.
		// 在替代节点插入后立即退役评审器批准的旧节点，避免画像读取链路同时看到新旧事实均为 active。
		if supersedeStatement, ok := parameterizedProfileNodesSupersedeStatement(node.ProfileType, bindID, supersedeNodeIDs, insertedProfileID, strings.TrimSpace(node.StatusReason), nowMs); ok {
			profileSupersedeStatementExpectations[len(statements)] = turnAnalysisProfileSupersedeExpectation{
				NodeIDs:        supersedeNodeIDs,
				SupersededByID: insertedProfileID,
				ExpectedRows:   int64(len(supersedeNodeIDs)),
			}
			statements = append(statements, supersedeStatement)
		}
	}
	// Track the first durable mutation because later failures must not be treated as clean relational failures by vector rollback paths.
	// 记录首次持久化突变，因为后续失败不能再被向量回滚路径当成干净的关系写入失败处理。
	mutated := false
	// Require the current session's pending turn before any memory row can reference the extracted result, so stale queue items remain clean rollback-safe failures.
	// 在任何 memory 行引用提炼结果之前先要求当前 session 的 pending turn 存在，让过期队列项保持为可安全回滚的干净失败。
	turnUpdateResult, err := s.execResult(ctx, turnUpdateStatement.SQL, turnUpdateStatement.Params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recoveredTurn, found, reconcileErr := s.loadTurnRecordByID(ctx, turn.ID)
			if reconcileErr != nil {
				return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "apply turn analysis",
					Message:   err.Error() + "; reconcile failed: " + reconcileErr.Error(),
				}
			}
			if !found || !sameAnalyzedTurnUpdate(recoveredTurn, session.SessionID, turn.ProjectID, turn.ID, analysis.Details, analysis.DetailsBudget, nowMs) {
				return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
					Operation: "apply turn analysis",
					Message:   err.Error(),
				}
			}
		} else {
			return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update turn analysis: %w", err)
		}
	} else if err := sqliteAutocommitRowsChangedDriftError("apply turn analysis", "update analyzed turn", turnUpdateResult.RowsChanged, 1); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, fmt.Errorf("update turn analysis: %w", err)
	}
	mutated = true
	freshVectorReference := false
	for index, statement := range statements {
		var err error
		if _, ok := profileTargetStatementIndexes[index]; ok {
			err = s.execExpectRowsChanged(ctx, "update turn analysis profile target", 1, statement.SQL, statement.Params...)
		} else if record, ok := memoryInsertStatementRecords[index]; ok {
			insertResult, insertErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if insertErr != nil {
				if isSQLiteOutcomeUncertainError(insertErr) {
					recoveredRows, reconcileErr := s.LoadMemoryNodesByIDs(ctx, []uint64{record.ID})
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, insertErr, reconcileErr),
							FreshVectorReference: true,
						}
					}
					if len(recoveredRows) == 1 && sameMemoryNodeRecord(recoveredRows[0], record) {
						freshVectorReference = true
					} else {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, insertErr),
							FreshVectorReference: true,
						}
					}
				} else {
					err = insertErr
				}
			} else if insertResult.RowsChanged != 1 {
				rowDriftErr := fmt.Errorf("insert turn analysis memory node affected %d rows, want 1", insertResult.RowsChanged)
				if insertResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: true,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := memoryContextEdgeStatementExpectations[index]; ok {
			edgeResult, edgeErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if edgeErr != nil {
				if isSQLiteOutcomeUncertainError(edgeErr) {
					recoveredEdges, reconcileErr := s.LoadMemoryContextEdgesByMemoryIDs(ctx, []uint64{expected.MemoryID})
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, edgeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !sameMemoryContextEdgeSet(recoveredEdges, expected.ExpectedEdges) {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, edgeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue only after the edge table exactly matches the statement boundary, so later inserts do not duplicate or skip contextual evidence.
					// 仅在边表精确匹配当前语句边界后继续，避免后续插入重复或跳过情境证据。
				} else {
					err = edgeErr
				}
			} else if expected.RequireRowsChanged && edgeResult.RowsChanged != expected.ExpectedRows {
				rowDriftErr := fmt.Errorf("write turn analysis memory context edge affected %d rows, want %d", edgeResult.RowsChanged, expected.ExpectedRows)
				if edgeResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := profileInsertStatementExpectations[index]; ok {
			insertResult, insertErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if insertErr != nil {
				if isSQLiteOutcomeUncertainError(insertErr) {
					recovered, found, reconcileErr := s.loadProfileNodeByID(ctx, expected.ID)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, insertErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !found || !sameProfileNodeInsert(recovered, expected) {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, insertErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Keep processing later supersede statements because the profile node insert has reached the durable target.
					// 继续处理后续 supersede 语句，因为画像节点插入已经达到长期目标。
				} else {
					err = insertErr
				}
			} else if insertResult.RowsChanged != 1 {
				rowDriftErr := fmt.Errorf("insert turn analysis profile node affected %d rows, want 1", insertResult.RowsChanged)
				if insertResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else if expected, ok := profileSupersedeStatementExpectations[index]; ok {
			supersedeResult, supersedeErr := s.execResult(ctx, statement.SQL, statement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileProfileNodesSuperseded(ctx, expected.NodeIDs, expected.SupersededByID)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v; reconcile failed: %v", index+1, supersedeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !recovered {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, supersedeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue after confirmed retirement so rendered profile readers do not see both old and replacement nodes as active.
					// 确认退役成功后继续执行，避免画像读取链路同时看到旧节点与替代节点均为 active。
				} else {
					err = supersedeErr
				}
			} else if supersedeResult.RowsChanged != expected.ExpectedRows {
				rowDriftErr := fmt.Errorf("supersede turn analysis profile nodes affected %d rows, want %d", supersedeResult.RowsChanged, expected.ExpectedRows)
				if supersedeResult.RowsChanged > 0 {
					return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
						Operation:            "apply turn analysis",
						Message:              fmt.Sprintf("execute turn analysis write statement %d: %v", index+1, rowDriftErr),
						FreshVectorReference: freshVectorReference,
					}
				}
				err = rowDriftErr
			}
		} else {
			err = s.exec(ctx, statement.SQL, statement.Params...)
		}
		if err != nil {
			return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("execute turn analysis write statement %d: %w", index+1, err))
		}
		if _, ok := memoryInsertStatementRecords[index]; ok {
			// Mark the boundary only after the durable memory row insert succeeds because that is when fresh vectors may become referenced.
			// 仅在长期 memory 行插入成功后标记边界，因为此时新向量才可能被关系行引用。
			freshVectorReference = true
		}
		mutated = true
	}
	if len(supersededMemoryIDs) > 0 {
		if supersedeStatement, ok := parameterizedMemoryNodesSupersedeStatement(supersededMemoryIDs, nowMs); ok {
			supersedeResult, supersedeErr := s.execResult(ctx, supersedeStatement.SQL, supersedeStatement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileMemoryNodesSuperseded(ctx, supersededMemoryIDs, nowMs)
					if reconcileErr != nil {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("supersede turn analysis memory nodes: %v; reconcile failed: %v", supersedeErr, reconcileErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					if !recovered {
						return logicdomain.TurnAnalysisApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply turn analysis",
							Message:              fmt.Sprintf("supersede turn analysis memory nodes: %v", supersedeErr),
							FreshVectorReference: freshVectorReference,
						}
					}
					// Continue to FTS deletion only after the durable memory rows prove this supersede write reached the table.
					// 只有在长期 memory 行证明本次 supersede 已落表后，才继续执行 FTS 删除。
				} else {
					return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("supersede turn analysis memory nodes: %w", supersedeErr))
				}
			} else if supersedeResult.RowsChanged != int64(len(supersededMemoryIDs)) {
				rowDriftErr := fmt.Errorf("affected %d rows, want %d", supersedeResult.RowsChanged, len(supersededMemoryIDs))
				return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("supersede turn analysis memory nodes: %w", rowDriftErr))
			}
			mutated = true
		}
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, insertedMemoryNodes, supersededMemoryIDs); err != nil {
		return logicdomain.TurnAnalysisApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(mutated, freshVectorReference, "apply turn analysis", fmt.Errorf("sync memory fts after turn analysis: %w", err))
	}
	return logicdomain.TurnAnalysisApplyResult{
		InsertedMemoryNodes: insertedMemoryNodes,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}
