// SQLite direct memory writes, adoption and lifecycle normalization.
// SQLite 适配层主动记忆写入、采纳与生命周期归一化。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
	"time"
)

// CreateDirectMemoryNode inserts one unified direct-write memory row after the vector row has already been persisted successfully.
// CreateDirectMemoryNode 用于在向量行已经成功持久化后，插入一条统一的主动写记忆行。
func (s *Store) CreateDirectMemoryNode(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	if session.SessionID == 0 {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.MemoryNodeRecord{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_memory_nodes")
	if err != nil {
		return logicdomain.MemoryNodeRecord{}, fmt.Errorf("allocate direct memory node id: %w", err)
	}
	now := time.Now().UTC()
	record = normalizeDirectMemoryNodeRecord(session, record, nextID, now)
	if err := s.executeDirectMemoryInsert(ctx, "create direct memory node", record); err != nil {
		return logicdomain.MemoryNodeRecord{}, err
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, []logicdomain.MemoryNodeRecord{record}, nil); err != nil {
		return logicdomain.MemoryNodeRecord{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "create direct memory node", fmt.Errorf("sync memory fts after direct insert: %w", err))
	}
	return record, nil
}

// ApplyDirectMemoryWrite serializes insertion of the session record and retirement of supersededMemoryIDs; success returns inserted and retired identifiers, while partial autocommit writes report an uncertain outcome with fresh-vector ownership.
// ApplyDirectMemoryWrite 串行插入会话记忆并退役 supersededMemoryIDs；成功返回插入及退役标识，逐语句提交发生部分写入时显式报告结果不明及新向量引用状态。
func (s *Store) ApplyDirectMemoryWrite(ctx context.Context, session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, supersededMemoryIDs []uint64) (logicdomain.DirectMemoryWriteApplyResult, error) {
	if session.SessionID == 0 {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if strings.TrimSpace(record.VectorID) == "" {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "vector_id", Message: "is required"}
	}
	if !logicdomain.ValidMemoryNodeCategory(record.Category) {
		return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.ValidationError{Field: "category", Message: "must be one supported memory category"}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	nextID, err := s.nextNumericID(ctx, "vmm_memory_nodes")
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("allocate direct memory node id: %w", err)
	}
	now := time.Now().UTC()
	nowMs := now.UnixMilli()
	record = normalizeDirectMemoryNodeRecord(session, record, nextID, now)
	supersededMemoryIDs = storageutil.NormalizeUint64List(supersededMemoryIDs)
	supersededVectorIDs, err := s.loadActiveMemoryVectorIDs(ctx, supersededMemoryIDs)
	if err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, fmt.Errorf("load superseded direct-write vector ids: %w", err)
	}

	// Execute the insert before the old-row status flip so later failures are reported as partial relational writes instead of clean failures.
	// 先执行插入再切换旧行状态，让后续失败按关系侧部分写入上报，而不是被误认为干净失败。
	if err := s.executeDirectMemoryInsert(ctx, "apply direct memory write", record); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, err
	}
	if len(supersededMemoryIDs) > 0 {
		if supersedeStatement, ok := parameterizedMemoryNodesSupersedeStatement(supersededMemoryIDs, nowMs); ok {
			supersedeResult, supersedeErr := s.execResult(ctx, supersedeStatement.SQL, supersedeStatement.Params...)
			if supersedeErr != nil {
				if isSQLiteOutcomeUncertainError(supersedeErr) {
					recovered, reconcileErr := s.reconcileMemoryNodesSuperseded(ctx, supersededMemoryIDs, nowMs)
					if reconcileErr != nil {
						return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply direct memory write",
							Message:              fmt.Sprintf("supersede direct memory nodes: %v; reconcile failed: %v", supersedeErr, reconcileErr),
							FreshVectorReference: true,
						}
					}
					if !recovered {
						return logicdomain.DirectMemoryWriteApplyResult{}, logicdomain.OutcomeUncertainError{
							Operation:            "apply direct memory write",
							Message:              fmt.Sprintf("supersede direct memory nodes: %v", supersedeErr),
							FreshVectorReference: true,
						}
					}
					// Continue to FTS deletion only after the direct-write replacement rows prove this supersede reached SQLite.
					// 只有在主动写替代行证明本次 supersede 已进入 SQLite 后，才继续执行 FTS 删除。
				} else {
					return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("supersede direct memory nodes: %w", supersedeErr))
				}
			} else if supersedeResult.RowsChanged != int64(len(supersededMemoryIDs)) {
				rowDriftErr := fmt.Errorf("affected %d rows, want %d", supersedeResult.RowsChanged, len(supersededMemoryIDs))
				return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("supersede direct memory nodes: %w", rowDriftErr))
			}
		}
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, []logicdomain.MemoryNodeRecord{record}, supersededMemoryIDs); err != nil {
		return logicdomain.DirectMemoryWriteApplyResult{}, sqlitePartialMutationErrorWithFreshVectorReference(true, true, "apply direct memory write", fmt.Errorf("sync memory fts after direct write: %w", err))
	}
	return logicdomain.DirectMemoryWriteApplyResult{
		InsertedMemoryNode:  record,
		SupersededVectorIDs: supersededVectorIDs,
	}, nil
}

// DeleteMemoryNodes marks active memory rows as deleted inside the provided hierarchy filter while leaving their source turn/detail rows untouched.
// DeleteMemoryNodes 用于在给定层级过滤范围内把 active 记忆行标记为 deleted，同时保留其来源 turn/detail 行不变。
func (s *Store) DeleteMemoryNodes(ctx context.Context, memoryIDs []uint64, filter logicdomain.SearchFilter, deletedAt time.Time, reason string) (logicdomain.MemoryDeleteResult, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return logicdomain.MemoryDeleteResult{}, nil
	}
	if s == nil || s.database == nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("sqlite memory delete requires one local ffi database")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Load the candidate rows under the write lock so the subsequent status flip uses the same active-row snapshot and cannot race with lifecycle writes.
	// 在写锁内加载候选行，让后续状态切换使用同一份 active 行快照，避免与生命周期写入发生竞态。
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	queryParams := append([]any{logicdomain.MemoryStatusActive}, memoryIDParams...)
	rows, err := queryRows[memoryNodeRow](s, ctx, fmt.Sprintf(`
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE memory_status = ?
  AND id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), queryParams...)
	if err != nil {
		return logicdomain.MemoryDeleteResult{}, fmt.Errorf("query sqlite memory nodes for delete: %w", err)
	}

	eligible := make(map[uint64]logicdomain.MemoryNodeRecord, len(rows))
	for _, row := range rows {
		record, err := row.toMemoryNodeRecord()
		if err != nil {
			return logicdomain.MemoryDeleteResult{}, err
		}
		if matchesLexicalMemoryFilter(record, filter) {
			eligible[record.ID] = record
		}
	}
	deletedMemoryIDs := make([]uint64, 0, len(eligible))
	notFoundMemoryIDs := make([]uint64, 0, len(memoryIDs))
	deletedVectorIDs := make([]string, 0, len(eligible))
	for _, memoryID := range memoryIDs {
		record, ok := eligible[memoryID]
		if !ok {
			notFoundMemoryIDs = append(notFoundMemoryIDs, memoryID)
			continue
		}
		deletedMemoryIDs = append(deletedMemoryIDs, memoryID)
		if strings.TrimSpace(record.VectorID) != "" {
			deletedVectorIDs = append(deletedVectorIDs, strings.TrimSpace(record.VectorID))
		}
	}
	if len(deletedMemoryIDs) == 0 {
		return logicdomain.MemoryDeleteResult{
			NotFoundMemoryIDs: notFoundMemoryIDs,
		}, nil
	}

	// Mark only the scoped active rows as deleted so future recall/detail filters skip them while source turns remain available for audit.
	// 只把范围内的 active 行标记为 deleted，让后续召回/详情过滤跳过这些记忆，同时保留来源轮次供审计查看。
	deletedAt = chooseNonZeroTime(deletedAt, time.Now().UTC()).UTC()
	deletedAtMs := deletedAt.UnixMilli()
	deletedMemoryIDPlaceholders := sqlitePlaceholders(len(deletedMemoryIDs))
	deletedMemoryIDParams := sqliteUint64Params(deletedMemoryIDs)
	updateParams := append([]any{
		logicdomain.MemoryStatusDeleted,
		strings.TrimSpace(reason),
		deletedAtMs,
		logicdomain.MemoryStatusActive,
	}, deletedMemoryIDParams...)
	updateResult, err := s.execResult(ctx, fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET memory_status = ?,
    status_reason = ?,
    updated_timestamp = ?
WHERE memory_status = ?
  AND id IN (%s)
`, deletedMemoryIDPlaceholders), updateParams...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			recovered, reconcileErr := s.reconcileMemoryNodesDeleted(ctx, deletedMemoryIDs, reason, deletedAtMs)
			if reconcileErr != nil {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   fmt.Sprintf("delete sqlite memory nodes: %v; reconcile failed: %v", err, reconcileErr),
				}
			}
			if !recovered {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   fmt.Sprintf("delete sqlite memory nodes: %v", err),
				}
			}
			// Continue only after every scoped memory row proves it has reached the deleted state, so vector cleanup coordinates are safe to expose.
			// 只有在范围内的每条 memory 行都证明已经进入 deleted 状态后，才继续暴露 vector 清理坐标。
		} else {
			return logicdomain.MemoryDeleteResult{}, fmt.Errorf("delete sqlite memory nodes: %w", err)
		}
	} else {
		// Do not hand vector ids back to the use case unless every loaded active row was actually marked deleted.
		// 只有所有已加载的 active 行都确实标记为 deleted 后，才把 vector id 返回给用例层清理。
		expectedRowsChanged := int64(len(deletedMemoryIDs))
		if updateResult.RowsChanged != expectedRowsChanged {
			err := fmt.Errorf("delete sqlite memory nodes affected %d rows, want %d", updateResult.RowsChanged, expectedRowsChanged)
			if updateResult.RowsChanged > 0 {
				return logicdomain.MemoryDeleteResult{}, logicdomain.OutcomeUncertainError{
					Operation: "delete memory nodes",
					Message:   err.Error(),
				}
			}
			return logicdomain.MemoryDeleteResult{}, err
		}
	}
	// Build the sidecar cleanup coordinates only after the relational status flip is fully verified, so post-delete index failures can still be compensated safely.
	// 只有在关系状态切换已完整验收后才构造旁路清理坐标，让删除后的索引失败仍可被安全补偿。
	result := logicdomain.MemoryDeleteResult{
		DeletedMemoryIDs:  deletedMemoryIDs,
		NotFoundMemoryIDs: notFoundMemoryIDs,
		DeletedVectorIDs:  storageutil.NormalizeStringList(deletedVectorIDs),
	}
	if err := s.syncMemoryFTSAfterWrite(ctx, nil, deletedMemoryIDs); err != nil {
		return result, sqlitePartialMutationError(true, "delete memory nodes", fmt.Errorf("sync memory fts after manual delete: %w", err))
	}
	return result, nil
}

// LoadRecentDirectMemoryWrites returns the direct AI-written memory rows created inside one exclusion window so the turn analyzer can avoid duplicate extraction.
// LoadRecentDirectMemoryWrites 用于返回某个排斥窗口内新建的 AI 主动写记忆行，让 turn analyzer 避免重复提炼。
func (s *Store) LoadRecentDirectMemoryWrites(ctx context.Context, session logicdomain.SessionRef, observedAfter, observedBefore time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	if observedBefore.IsZero() {
		return []logicdomain.TurnAnalysisDirectWrite{}, nil
	}
	nowMs := time.Now().UTC().UnixMilli()
	rows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE origin_session_id = ?
  AND source_kind = ?
  AND memory_status = ?
  AND (expires_timestamp <= 0 OR expires_timestamp > ?)
  AND created_timestamp > ?
  AND created_timestamp <= ?
ORDER BY created_timestamp ASC, id ASC
`, session.SessionID, logicdomain.MemorySourceKindGRPCAIWrite, logicdomain.MemoryStatusActive, nowMs, observedAfter.UTC().UnixMilli(), observedBefore.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query recent direct memory writes: %w", err)
	}
	items := make([]logicdomain.TurnAnalysisDirectWrite, 0, len(rows))
	for _, row := range rows {
		items = append(items, logicdomain.TurnAnalysisDirectWrite{
			MemoryID:         row.ID,
			ScopeLevel:       logicdomain.MemoryScopeLevelLabel(row.ScopeLevel),
			Abstract:         strings.TrimSpace(row.Abstract),
			Details:          strings.TrimSpace(row.Details),
			CreatedTimestamp: row.CreatedTimestamp,
		})
	}
	return items, nil
}

// ApplyMemoryAdoption increments lifecycle counters for the memory rows selected by pre-check and promotes hot session facts when they prove useful across sessions.
// ApplyMemoryAdoption 用于为被 pre-check 采纳的记忆行递增生命周期计数，并在 session 级事实跨会话多次命中后将其升级。
func (s *Store) ApplyMemoryAdoption(ctx context.Context, session logicdomain.SessionRef, memoryIDs []uint64, adoptedAt time.Time) ([]logicdomain.MemoryRecord, error) {
	if session.SessionID == 0 {
		return nil, logicdomain.ValidationError{Field: "session_id", Message: "must resolve to one persisted session"}
	}
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}
	if adoptedAt.IsZero() {
		adoptedAt = time.Now().UTC()
	} else {
		adoptedAt = adoptedAt.UTC()
	}

	// Serialize the full read-modify-write window so the lifecycle update never snapshots rows before another writer supersedes or reinforces them.
	// 串行化整个读-改-写窗口，避免生命周期回写在其他写入完成 supersede 或强化之前先拍下旧快照。
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Load the current durable rows first so the lifecycle update can respect each row's current scope, counters, and expiry horizon.
	// 先加载当前长期行，确保生命周期更新能够尊重每条记录已有的作用域、计数器和过期时间。
	rows, err := s.LoadMemoryNodesByIDs(ctx, memoryIDs)
	if err != nil {
		return nil, fmt.Errorf("load memory adoption targets: %w", err)
	}
	if len(rows) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}

	// Pair each planned write with the lifecycle record that may be returned only after the durable row is confirmed updated.
	// 将每条计划写入与生命周期记录配对，只有确认长期行被更新后才允许返回该记录。
	pendingWrites := make([]struct {
		statement sqliteWriteStatement
		expected  logicdomain.MemoryNodeRecord
		record    logicdomain.MemoryRecord
	}, 0, len(rows))
	for _, row := range rows {
		if !logicdomain.MemoryNodeRecordIsActiveUnexpiredAt(row, adoptedAt) {
			continue
		}
		evolved := evolveAdoptedMemoryRecord(session, row, adoptedAt)
		pendingWrites = append(pendingWrites, struct {
			statement sqliteWriteStatement
			expected  logicdomain.MemoryNodeRecord
			record    logicdomain.MemoryRecord
		}{
			statement: parameterizedMemoryAdoptionUpdateStatement(evolved),
			expected:  evolved,
			record:    memoryRecordFromNode(evolved),
		})
	}
	if len(pendingWrites) == 0 {
		return []logicdomain.MemoryRecord{}, nil
	}

	// Keep only confirmed lifecycle records for vector-side sync so callers never upsert speculative adoption state.
	// 只保留已确认写入的生命周期记录供向量侧同步使用，避免调用方 upsert 推测性的采纳状态。
	updatedRecords := make([]logicdomain.MemoryRecord, 0, len(pendingWrites))
	// Track whether an earlier SQLite update succeeded so later drift is reported as an uncertain partial mutation.
	// 记录前序 SQLite 更新是否已经成功，让后续漂移按部分突变结果不确定上报。
	mutated := false
	for index, pendingWrite := range pendingWrites {
		updateResult, updateErr := s.execResult(ctx, pendingWrite.statement.SQL, pendingWrite.statement.Params...)
		if updateErr != nil {
			if isSQLiteOutcomeUncertainError(updateErr) {
				recovered, reconcileErr := s.reconcileMemoryAdoptionUpdate(ctx, pendingWrite.expected)
				if reconcileErr != nil {
					return nil, logicdomain.OutcomeUncertainError{
						Operation: "apply memory adoption",
						Message:   fmt.Sprintf("apply memory adoption statement %d: %v; reconcile failed: %v", index+1, updateErr, reconcileErr),
					}
				}
				if !recovered {
					return nil, logicdomain.OutcomeUncertainError{
						Operation: "apply memory adoption",
						Message:   fmt.Sprintf("apply memory adoption statement %d: %v", index+1, updateErr),
					}
				}
				// Continue only after the durable row proves this adoption update reached the exact lifecycle target.
				// 只有在长期行证明本次采纳更新已经达到精确生命周期目标后，才继续处理后续记录。
			} else {
				return nil, sqlitePartialMutationError(mutated, "apply memory adoption", fmt.Errorf("apply memory adoption statement %d: %w", index+1, updateErr))
			}
		} else if updateResult.RowsChanged != 1 {
			rowDriftErr := fmt.Errorf("update adopted sqlite memory node affected %d rows, want 1", updateResult.RowsChanged)
			return nil, sqlitePartialMutationError(mutated, "apply memory adoption", fmt.Errorf("apply memory adoption statement %d: %w", index+1, rowDriftErr))
		}
		mutated = true
		updatedRecords = append(updatedRecords, pendingWrite.record)
	}
	return updatedRecords, nil
}

// loadActiveMemoryVectorIDs loads the vector ids of active unified memory rows by memory id so callers can clean those vector rows after a supersede update commits.
// loadActiveMemoryVectorIDs 用于按记忆 id 读取 active 统一记忆行的 vector_id，供调用方在 supersede 提交后清理对应向量行。
func (s *Store) loadActiveMemoryVectorIDs(ctx context.Context, memoryIDs []uint64) ([]string, error) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil, nil
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	memoryIDParams := sqliteUint64Params(memoryIDs)
	queryParams := append([]any{logicdomain.MemoryStatusActive}, memoryIDParams...)
	rows, err := queryRows[obsoleteVectorRow](s, ctx, fmt.Sprintf(`
SELECT vector_id
FROM vmm_memory_nodes
WHERE memory_status = ? AND id IN (%s)
ORDER BY id ASC
`, memoryIDPlaceholders), queryParams...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.VectorID) == "" {
			continue
		}
		ids = append(ids, strings.TrimSpace(row.VectorID))
	}
	return ids, nil
}

// normalizeTurnMemoryNodeRecord fills unified-memory defaults for one turn-extracted node before it is persisted.
// normalizeTurnMemoryNodeRecord 用于在持久化前，为一条 turn 提炼记忆补齐统一记忆表默认值。
func normalizeTurnMemoryNodeRecord(session logicdomain.SessionRef, turn logicdomain.PersistedTurnRecord, node logicdomain.MemoryNodeCandidate, id uint64, now time.Time) logicdomain.MemoryNodeRecord {
	record := logicdomain.MemoryNodeRecord{
		ID:              id,
		TeamID:          session.TeamID,
		SpaceID:         session.SpaceID,
		ProjectID:       session.ProjectID,
		UserID:          session.UserID,
		OriginSessionID: session.SessionID,
		SourceTurnID:    turn.ID,
		VectorID:        strings.TrimSpace(node.VectorID),
		Vector:          append([]float32(nil), node.Vector...),
		SourceKind:      node.SourceKind,
		ScopeLevel:      node.ScopeLevel,
		Category:        node.Category,
		Abstract:        strings.TrimSpace(node.Abstract),
		Details:         strings.TrimSpace(node.Details),
		Status:          logicdomain.MemoryStatusActive,
		Priority:        node.Priority,
		MemoryLevel:     node.MemoryLevel,
		RefreshWeight:   node.RefreshWeight,
		ExpiresAt:       node.ExpiresAt,
		DedupeHash:      strings.TrimSpace(node.DedupeHash),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindTurnExtract
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	record.LastReinforcedAt = time.Time{}
	record.ReinforcementCount = 0
	return record
}

// normalizeDirectMemoryNodeRecord fills unified-memory defaults for one direct AI-written row before it is persisted.
// normalizeDirectMemoryNodeRecord 用于在持久化前，为一条 AI 主动写记忆补齐统一记忆表默认值。
func normalizeDirectMemoryNodeRecord(session logicdomain.SessionRef, record logicdomain.MemoryNodeRecord, id uint64, now time.Time) logicdomain.MemoryNodeRecord {
	record.ID = id
	record.TeamID = session.TeamID
	record.SpaceID = session.SpaceID
	record.ProjectID = session.ProjectID
	record.UserID = session.UserID
	if record.OriginSessionID == 0 {
		record.OriginSessionID = session.SessionID
	}
	record.VectorID = strings.TrimSpace(record.VectorID)
	record.Abstract = strings.TrimSpace(record.Abstract)
	record.Details = strings.TrimSpace(record.Details)
	record.DedupeHash = strings.TrimSpace(record.DedupeHash)
	record.CreatedAt = chooseNonZeroTime(record.CreatedAt, now)
	record.UpdatedAt = chooseNonZeroTime(record.UpdatedAt, now)
	record.SupportCount = 0
	record.RebuttalCount = 0
	if !logicdomain.ValidMemorySourceKind(record.SourceKind) {
		record.SourceKind = logicdomain.MemorySourceKindGRPCAIWrite
	}
	if !logicdomain.ValidMemoryScopeLevel(record.ScopeLevel) {
		record.ScopeLevel = logicdomain.MemoryScopeLevelProject
	}
	if !logicdomain.ValidMemoryPriority(record.Priority) {
		record.Priority = logicdomain.MemoryPriorityP2
	}
	if !logicdomain.ValidMemoryLevel(record.MemoryLevel) {
		if record.ScopeLevel == logicdomain.MemoryScopeLevelSession {
			record.MemoryLevel = logicdomain.MemoryLevelSession
		} else {
			record.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if !logicdomain.ValidMemoryStatus(record.Status) {
		record.Status = logicdomain.MemoryStatusActive
	}
	if record.RefreshWeight <= 0 {
		record.RefreshWeight = 1
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = defaultUnifiedMemoryExpiry(record.ScopeLevel, now)
	} else {
		record.ExpiresAt = record.ExpiresAt.UTC()
	}
	record.LastReinforcedAt = chooseNonZeroTime(record.LastReinforcedAt, time.Time{})
	if record.ReinforcementCount < 0 {
		record.ReinforcementCount = 0
	}
	return record
}

// defaultUnifiedMemoryExpiry derives the default retention horizon for unified durable memory rows when callers omit an explicit expiry.
// defaultUnifiedMemoryExpiry 用于在调用方省略显式过期时间时，为统一长期记忆推导默认保留时长。
func defaultUnifiedMemoryExpiry(scopeLevel int, now time.Time) time.Time {
	switch scopeLevel {
	case logicdomain.MemoryScopeLevelSession:
		return now.UTC().Add(15 * 24 * time.Hour)
	case logicdomain.MemoryScopeLevelUser:
		return now.UTC().Add(365 * 24 * time.Hour)
	default:
		return now.UTC().Add(180 * 24 * time.Hour)
	}
}

// evolveAdoptedMemoryRecord computes the post-adoption lifecycle state for one active unified memory row.
// evolveAdoptedMemoryRecord 用于计算一条活跃统一记忆在被采纳后的生命周期状态。
func evolveAdoptedMemoryRecord(session logicdomain.SessionRef, row logicdomain.MemoryNodeRecord, adoptedAt time.Time) logicdomain.MemoryNodeRecord {
	updated := row
	updated.LastRecalledAt = adoptedAt.UTC()
	updated.LastAdoptedAt = adoptedAt.UTC()
	updated.LastReinforcedAt = adoptedAt.UTC()
	updated.RecalledCount++
	updated.AdoptedCount++
	updated.ReinforcementCount++
	if updated.RefreshWeight <= 0 {
		updated.RefreshWeight = 1
	}
	updated.RefreshWeight++
	if updated.AdoptedCount >= 2 && updated.MemoryLevel < logicdomain.MemoryLevelPhase {
		updated.MemoryLevel = logicdomain.MemoryLevelPhase
	}
	if row.OriginSessionID > 0 && row.OriginSessionID != session.SessionID {
		updated.CrossSessionAdoptedCount++
	}
	if row.ScopeLevel == logicdomain.MemoryScopeLevelSession && updated.CrossSessionAdoptedCount >= 2 {
		updated.ScopeLevel = logicdomain.MemoryScopeLevelProject
		if updated.MemoryLevel < logicdomain.MemoryLevelStable {
			updated.MemoryLevel = logicdomain.MemoryLevelStable
		}
	}
	if updated.CrossSessionAdoptedCount >= 4 && updated.Priority == logicdomain.MemoryPriorityP0 && updated.MemoryLevel < logicdomain.MemoryLevelPersistent {
		updated.MemoryLevel = logicdomain.MemoryLevelPersistent
	}
	updated.ExpiresAt = chooseLongerMemoryExpiry(updated.ExpiresAt, defaultUnifiedMemoryExpiry(updated.ScopeLevel, adoptedAt))
	updated.UpdatedAt = adoptedAt.UTC()
	return updated
}

// chooseLongerMemoryExpiry keeps the farther expiry horizon so adoption never shortens one record's lifetime by mistake.
// chooseLongerMemoryExpiry 用于保留更远的过期时间，避免采纳操作意外缩短某条记录的寿命。
func chooseLongerMemoryExpiry(current, candidate time.Time) time.Time {
	if current.IsZero() {
		return candidate.UTC()
	}
	if current.After(candidate) {
		return current.UTC()
	}
	return candidate.UTC()
}

// chooseNonZeroTime keeps helper callers concise when they want to preserve a provided timestamp but fall back to now.
// chooseNonZeroTime 用于在调用方希望优先保留已有时间戳、否则回退到 now 时，保持辅助逻辑简洁。
func chooseNonZeroTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback.UTC()
	}
	return value.UTC()
}

// collectTurnAnalysisSupersedeMemoryIDs unions reviewer-approved supersede ids from surviving memory nodes so persistence can retire only the durable memories still replaced after every filter.
// collectTurnAnalysisSupersedeMemoryIDs 用于从存活记忆节点中汇总 reviewer 批准的 supersede id，确保持久化只退役经过全部过滤后仍被新节点替代的旧记忆。
func collectTurnAnalysisSupersedeMemoryIDs(nodes []logicdomain.MemoryNodeCandidate) []uint64 {
	if len(nodes) == 0 {
		return nil
	}
	merged := make([]uint64, 0, len(nodes))
	for _, node := range nodes {
		merged = append(merged, node.SupersedeMemoryIDs...)
	}
	return storageutil.NormalizeUint64List(merged)
}
