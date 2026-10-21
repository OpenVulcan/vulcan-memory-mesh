// SQLite durable outcome checks used after uncertain write acknowledgements.
// SQLite 适配层在写入确认不明时核对长期存储结果。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"math"
	"sort"
	"strings"
	"time"
)

// loadTurnRecordByID loads one durable turn row by primary key for write reconciliation paths that cannot rely on retry safety.
// loadTurnRecordByID 用于按主键读取一条长期 turn 行，服务于无法依赖安全重试的写入对账路径。
func (s *Store) loadTurnRecordByID(ctx context.Context, turnID uint64) (logicdomain.SessionTurnRecord, bool, error) {
	rows, err := queryRows[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id,
       dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget,
       created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id = ?
LIMIT 1
`, turnID)
	if err != nil {
		return logicdomain.SessionTurnRecord{}, false, fmt.Errorf("query turn record: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.SessionTurnRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// sameAppendedTurnRecord reports whether a read-back row exactly matches the turn insert currently being reconciled.
// sameAppendedTurnRecord 用于判断回读行是否精确匹配当前正在对账的 turn 插入。
func sameAppendedTurnRecord(row logicdomain.SessionTurnRecord, id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) bool {
	return row.ID == id &&
		row.SessionID == sessionID &&
		row.ProjectID == projectID &&
		row.DehydratedContent == strings.TrimSpace(dehydratedContent) &&
		row.DehydratedBudget == dehydratedBudget &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusPending &&
		row.Details == "" &&
		row.DetailsBudget == 0 &&
		row.CreatedAt.Equal(unixMilliToTime(createdMs)) &&
		row.UpdatedAt.Equal(unixMilliToTime(updatedMs))
}

// sameCorruptedTurnMark reports whether a read-back turn row proves one pending-to-done corrupted mark reached the durable table without changing payload fields.
// sameCorruptedTurnMark 用于判断回读 turn 行是否证明一次损坏 turn 的 pending 到 done 标记已经落入长期表，且没有改写载荷字段。
func sameCorruptedTurnMark(row logicdomain.SessionTurnRecord, before logicdomain.SessionTurnRecord, sessionID, turnID uint64, updatedMs int64) bool {
	return before.ID == turnID &&
		before.SessionID == sessionID &&
		before.ExtractedStatus == logicdomain.TurnExtractedStatusPending &&
		row.ID == before.ID &&
		row.SessionID == before.SessionID &&
		row.ProjectID == before.ProjectID &&
		row.DehydratedContent == before.DehydratedContent &&
		row.DehydratedBudget == before.DehydratedBudget &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusDone &&
		row.Details == before.Details &&
		row.DetailsBudget == before.DetailsBudget &&
		row.CreatedAt.Equal(before.CreatedAt) &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// sameAnalyzedTurnUpdate reports whether a read-back turn row has reached the exact analysis status and details written by the first post-action mutation.
// sameAnalyzedTurnUpdate 用于判断回读 turn 行是否已经达到 post-action 首个写入所要求的分析状态与 details 内容。
func sameAnalyzedTurnUpdate(row logicdomain.SessionTurnRecord, sessionID, projectID, turnID uint64, details string, detailsBudget int, updatedMs int64) bool {
	return row.ID == turnID &&
		row.SessionID == sessionID &&
		row.ProjectID == projectID &&
		row.ExtractedStatus == logicdomain.TurnExtractedStatusDone &&
		row.Details == strings.TrimSpace(details) &&
		row.DetailsBudget == detailsBudget &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// sameSQLiteRecordTime compares a read-back SQLite millisecond timestamp with the time value that was written through UnixMilli.
// sameSQLiteRecordTime 用于比较 SQLite 回读的毫秒时间戳与通过 UnixMilli 写入的原始 time 值。
func sameSQLiteRecordTime(value, expected time.Time) bool {
	if expected.IsZero() {
		return value.IsZero()
	}
	return value.Equal(unixMilliToTime(expected.UTC().UnixMilli()))
}

// sameFloat32Vector reports whether two vectors are bit-identical after SQLite JSON round-trip decoding.
// sameFloat32Vector 用于判断两个向量在 SQLite JSON 往返解码后是否保持 bit 级一致。
func sameFloat32Vector(left, right []float32) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if math.Float32bits(left[idx]) != math.Float32bits(right[idx]) {
			return false
		}
	}
	return true
}

// sameMemoryNodeRecord reports whether a read-back memory row exactly matches one pending durable memory target.
// sameMemoryNodeRecord 用于判断回读 memory 行是否精确匹配一个待落表的长期记忆目标。
func sameMemoryNodeRecord(row logicdomain.MemoryNodeRecord, expected logicdomain.MemoryNodeRecord) bool {
	return row.ID == expected.ID &&
		row.TeamID == expected.TeamID &&
		row.SpaceID == expected.SpaceID &&
		row.ProjectID == expected.ProjectID &&
		row.UserID == expected.UserID &&
		row.OriginSessionID == expected.OriginSessionID &&
		row.SourceTurnID == expected.SourceTurnID &&
		row.VectorID == strings.TrimSpace(expected.VectorID) &&
		sameFloat32Vector(row.Vector, expected.Vector) &&
		row.SourceKind == expected.SourceKind &&
		row.ScopeLevel == expected.ScopeLevel &&
		row.Category == expected.Category &&
		row.Abstract == strings.TrimSpace(expected.Abstract) &&
		row.Details == strings.TrimSpace(expected.Details) &&
		row.Status == expected.Status &&
		row.Priority == expected.Priority &&
		row.MemoryLevel == expected.MemoryLevel &&
		row.RefreshWeight == expected.RefreshWeight &&
		row.SupportCount == expected.SupportCount &&
		row.RebuttalCount == expected.RebuttalCount &&
		row.StatusReason == strings.TrimSpace(expected.StatusReason) &&
		sameSQLiteRecordTime(row.ExpiresAt, expected.ExpiresAt) &&
		sameSQLiteRecordTime(row.LastRecalledAt, expected.LastRecalledAt) &&
		sameSQLiteRecordTime(row.LastAdoptedAt, expected.LastAdoptedAt) &&
		sameSQLiteRecordTime(row.LastReinforcedAt, expected.LastReinforcedAt) &&
		row.RecalledCount == expected.RecalledCount &&
		row.AdoptedCount == expected.AdoptedCount &&
		row.ReinforcementCount == expected.ReinforcementCount &&
		row.CrossSessionAdoptedCount == expected.CrossSessionAdoptedCount &&
		row.DecayDisabled == expected.DecayDisabled &&
		row.DedupeHash == strings.TrimSpace(expected.DedupeHash) &&
		sameSQLiteRecordTime(row.CreatedAt, expected.CreatedAt) &&
		sameSQLiteRecordTime(row.UpdatedAt, expected.UpdatedAt)
}

// sameMemoryContextEdgeSet reports whether a read-back edge set exactly matches the durable edge state expected after one context-edge rewrite statement.
// sameMemoryContextEdgeSet 用于判断回读情境边集合是否精确匹配某条情境边重写语句执行后应达到的长期状态。
func sameMemoryContextEdgeSet(rows []logicdomain.MemoryContextEdge, expected []logicdomain.MemoryContextEdge) bool {
	if len(rows) != len(expected) {
		return false
	}
	orderedRows := append([]logicdomain.MemoryContextEdge(nil), rows...)
	orderedExpected := append([]logicdomain.MemoryContextEdge(nil), expected...)
	sortMemoryContextEdgesByPrimaryKey(orderedRows)
	sortMemoryContextEdgesByPrimaryKey(orderedExpected)
	for index := range orderedExpected {
		if !sameMemoryContextEdge(orderedRows[index], orderedExpected[index]) {
			return false
		}
	}
	return true
}

// sortMemoryContextEdgesByPrimaryKey aligns in-memory comparisons with the SQLite table primary key instead of relying on extraction-order side effects.
// sortMemoryContextEdgesByPrimaryKey 用于让内存比较对齐 SQLite 表主键顺序，而不是依赖提炼顺序的副作用。
func sortMemoryContextEdgesByPrimaryKey(edges []logicdomain.MemoryContextEdge) {
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].MemoryID != edges[right].MemoryID {
			return edges[left].MemoryID < edges[right].MemoryID
		}
		if edges[left].ContextKey != edges[right].ContextKey {
			return edges[left].ContextKey < edges[right].ContextKey
		}
		return edges[left].ContextValue < edges[right].ContextValue
	})
}

// sameMemoryContextEdge reports whether one read-back edge matches the exact primary-key row and counters written for a turn-extracted memory.
// sameMemoryContextEdge 用于判断单条回读情境边是否匹配 turn 提炼记忆写入的精确主键行与计数器。
func sameMemoryContextEdge(row logicdomain.MemoryContextEdge, expected logicdomain.MemoryContextEdge) bool {
	return row.MemoryID == expected.MemoryID &&
		row.ContextKey == strings.TrimSpace(expected.ContextKey) &&
		row.ContextValue == strings.TrimSpace(expected.ContextValue) &&
		row.SupportCount == expected.SupportCount &&
		row.RebuttalCount == expected.RebuttalCount &&
		sameSQLiteRecordTime(row.LastSupportedAt, expected.LastSupportedAt) &&
		sameSQLiteRecordTime(row.LastRebuttedAt, expected.LastRebuttedAt) &&
		sameSQLiteRecordTime(row.CreatedAt, expected.CreatedAt) &&
		sameSQLiteRecordTime(row.UpdatedAt, expected.UpdatedAt)
}

// sameProfileNodeInsert reports whether a read-back profile node matches the post-action insert target that may have committed already.
// sameProfileNodeInsert 用于判断回读画像节点是否匹配一次可能已经提交的 post-action 插入目标。
func sameProfileNodeInsert(row logicdomain.ProfileNodeRecord, expected turnAnalysisProfileInsertExpectation) bool {
	expiresAt := time.Time{}
	if !expected.Node.ExpiresAt.IsZero() {
		expiresAt = expected.Node.ExpiresAt.UTC()
	}
	return row.ID == expected.ID &&
		row.TurnID == expected.TurnID &&
		row.ProfileType == expected.ProfileType &&
		row.BindID == expected.BindID &&
		row.Content == strings.TrimSpace(expected.Node.Content) &&
		row.Status == expected.Node.Status &&
		row.Priority == expected.Node.Priority &&
		row.ProfileLevel == expected.Node.ProfileLevel &&
		row.LevelReason == strings.TrimSpace(expected.Node.LevelReason) &&
		row.RefreshWeight == expected.Node.RefreshWeight &&
		row.ProfileDate == strings.TrimSpace(expected.Node.ProfileDate) &&
		row.SourceKind == expected.Node.SourceKind &&
		row.SourceID == expected.Node.SourceID &&
		row.StatusReason == strings.TrimSpace(expected.Node.StatusReason) &&
		sameSQLiteRecordTime(row.ExpiresAt, expiresAt) &&
		row.SupersededByID == 0 &&
		row.CreatedAt.Equal(unixMilliToTime(expected.CreatedMs)) &&
		row.UpdatedAt.Equal(unixMilliToTime(expected.CreatedMs))
}

// sameAppendedSessionCounters reports whether a read-back session row has reached the absolute counter target for one appended turn.
// sameAppendedSessionCounters 用于判断回读 session 行是否已经达到一次 turn 追加对应的绝对计数目标。
func sameAppendedSessionCounters(row logicdomain.SessionRecord, sessionID, projectID uint64, turnCount, summarizeBudget int, updatedMs int64) bool {
	return row.ID == sessionID &&
		row.ProjectID == projectID &&
		row.TurnCount == turnCount &&
		row.SummarizeBudget == summarizeBudget &&
		!row.UpdatedAt.Before(unixMilliToTime(updatedMs))
}

// sessionTimestampReached reports whether one read-back session timestamp has reached a requested checkpoint while treating zero as "no checkpoint requested".
// sessionTimestampReached 用于判断回读 session 时间戳是否达到请求的检查点，同时把零值视为“未请求该检查点”。
func sessionTimestampReached(value time.Time, targetMs int64) bool {
	if targetMs <= 0 {
		return true
	}
	return !value.Before(unixMilliToTime(targetMs))
}

// sameAdvancedSessionExtractWindow reports whether a read-back session row proves one extract-window checkpoint update has reached every requested timestamp.
// sameAdvancedSessionExtractWindow 用于判断回读 session 行是否能够证明一次提炼窗口检查点更新已经达到所有请求的时间戳。
func sameAdvancedSessionExtractWindow(row logicdomain.SessionRecord, sessionID uint64, observedMs, completedMs, updatedMs int64) bool {
	return row.ID == sessionID &&
		sessionTimestampReached(row.LastExtractObservedAt, observedMs) &&
		sessionTimestampReached(row.LastExtractCompletedAt, completedMs) &&
		sessionTimestampReached(row.UpdatedAt, updatedMs)
}

// reconcileProfileInstructionCreate checks whether one insert that returned a commit-uncertain error has already materialized in SQLite.
// reconcileProfileInstructionCreate 用于检查一条返回“提交结果不确定”错误的插入，是否实际上已经在 SQLite 中落库。
func (s *Store) reconcileProfileInstructionCreate(ctx context.Context, expected logicdomain.ProfileInstructionRecord) (logicdomain.ProfileInstructionRecord, bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, expected.ID)
	if err != nil || !found {
		return logicdomain.ProfileInstructionRecord{}, false, err
	}
	if stored.ProfileType != expected.ProfileType || stored.BindID != expected.BindID || strings.TrimSpace(stored.Instruction) != strings.TrimSpace(expected.Instruction) {
		return logicdomain.ProfileInstructionRecord{}, false, nil
	}
	return stored, true, nil
}

// reconcileManualProfileNodeInsert checks whether one node insert already committed even though the gateway returned an uncertain failure.
// reconcileManualProfileNodeInsert 用于检查某条节点插入是否已经提交，即便网关对这次写入返回了“不确定失败”。
func (s *Store) reconcileManualProfileNodeInsert(ctx context.Context, target logicdomain.ProfileTargetRef, expected logicdomain.ProfileNodeCandidate, expectedID uint64) (logicdomain.ProfileNodeRecord, bool, error) {
	stored, found, err := s.loadProfileNodeByID(ctx, expectedID)
	if err != nil || !found {
		return logicdomain.ProfileNodeRecord{}, false, err
	}
	if stored.ProfileType != target.ProfileType ||
		stored.BindID != target.BindID ||
		strings.TrimSpace(stored.Content) != strings.TrimSpace(expected.Content) ||
		stored.SourceKind != expected.SourceKind ||
		stored.SourceID != expected.SourceID {
		return logicdomain.ProfileNodeRecord{}, false, nil
	}
	return stored, true, nil
}

// reconcileProfileNodesSuperseded verifies whether all target nodes already reached the desired superseded state after one uncertain update error.
// reconcileProfileNodesSuperseded 用于验证在一次不确定更新报错后，目标节点是否已经全部进入期望的 superseded 状态。
func (s *Store) reconcileProfileNodesSuperseded(ctx context.Context, nodeIDs []uint64, supersededByID uint64) (bool, error) {
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, nodeIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(storageutil.NormalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus != logicdomain.ProfileStatusSuperseded || row.SupersededByID != supersededByID {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryNodesSuperseded verifies whether all target memory rows reached this write's superseded state after one uncertain update error.
// reconcileMemoryNodesSuperseded 用于验证在一次不确定更新报错后，目标 memory 行是否已经全部进入本次写入要求的 superseded 状态。
func (s *Store) reconcileMemoryNodesSuperseded(ctx context.Context, memoryIDs []uint64, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(memoryIDs)
	rows, err := s.LoadMemoryNodesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.Status != logicdomain.MemoryStatusSuperseded ||
			!sessionTimestampReached(row.UpdatedAt, updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryNodesDeleted verifies whether all target memory rows reached the manual-delete state after one uncertain status update.
// reconcileMemoryNodesDeleted 用于验证一次不确定状态更新后，目标 memory 行是否已经全部进入手工删除状态。
func (s *Store) reconcileMemoryNodesDeleted(ctx context.Context, memoryIDs []uint64, reason string, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(memoryIDs)
	rows, err := s.LoadMemoryNodesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	expectedReason := strings.TrimSpace(reason)
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.Status != logicdomain.MemoryStatusDeleted ||
			row.StatusReason != expectedReason ||
			!sessionTimestampReached(row.UpdatedAt, updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// reconcileMemoryAdoptionUpdate verifies whether one uncertain adoption update already reached its exact durable memory target.
// reconcileMemoryAdoptionUpdate 用于验证一次不确定的采纳更新是否已经达到精确的长期记忆目标。
func (s *Store) reconcileMemoryAdoptionUpdate(ctx context.Context, expected logicdomain.MemoryNodeRecord) (bool, error) {
	rows, err := s.LoadMemoryNodesByIDs(ctx, []uint64{expected.ID})
	if err != nil {
		return false, err
	}
	return len(rows) == 1 && sameMemoryNodeRecord(rows[0], expected), nil
}

// executeDirectMemoryInsert writes one direct-memory row and reconciles commit-unknown inserts because the vector row was already written before this relation step.
// executeDirectMemoryInsert 用于写入一条主动记忆关系行，并在提交未知时对账，因为进入该关系步骤前向量行已经写入成功。
func (s *Store) executeDirectMemoryInsert(ctx context.Context, operation string, record logicdomain.MemoryNodeRecord) error {
	insertStatement, err := parameterizedMemoryNodeInsertStatement(record)
	if err != nil {
		return err
	}
	insertResult, insertErr := s.execResult(ctx, insertStatement.SQL, insertStatement.Params...)
	if insertErr != nil {
		if isSQLiteOutcomeUncertainError(insertErr) {
			recoveredRows, reconcileErr := s.LoadMemoryNodesByIDs(ctx, []uint64{record.ID})
			if reconcileErr != nil {
				return logicdomain.OutcomeUncertainError{
					Operation:            operation,
					Message:              fmt.Sprintf("insert direct memory node: %v; reconcile failed: %v", insertErr, reconcileErr),
					FreshVectorReference: true,
				}
			}
			if len(recoveredRows) == 1 && sameMemoryNodeRecord(recoveredRows[0], record) {
				return nil
			}
			return logicdomain.OutcomeUncertainError{
				Operation:            operation,
				Message:              fmt.Sprintf("insert direct memory node: %v", insertErr),
				FreshVectorReference: true,
			}
		}
		return fmt.Errorf("insert direct memory node: %w", insertErr)
	}
	if insertResult.RowsChanged != 1 {
		rowDriftErr := fmt.Errorf("insert direct memory node affected %d rows, want 1", insertResult.RowsChanged)
		if insertResult.RowsChanged > 0 {
			return logicdomain.OutcomeUncertainError{
				Operation:            operation,
				Message:              rowDriftErr.Error(),
				FreshVectorReference: true,
			}
		}
		return rowDriftErr
	}
	return nil
}

// reconcileProfileNodesRetired verifies whether all target nodes already left the active set after one uncertain retire update.
// reconcileProfileNodesRetired 用于验证在一次不确定退役更新后，目标节点是否已经全部离开 active 集合。
func (s *Store) reconcileProfileNodesRetired(ctx context.Context, nodeIDs []uint64) (bool, error) {
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, nodeIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(storageutil.NormalizeUint64List(nodeIDs)) {
		return false, nil
	}
	for _, row := range rows {
		if row.ProfileStatus == logicdomain.ProfileStatusActive {
			return false, nil
		}
	}
	return true, nil
}

// reconcileProfileNodesExpired verifies whether all selected lifecycle nodes already reached this expiry write after one uncertain update error.
// reconcileProfileNodesExpired 用于验证一次不确定更新错误后，所有选中的生命周期节点是否已经进入本次过期写入要求的状态。
func (s *Store) reconcileProfileNodesExpired(ctx context.Context, nodeIDs []uint64, reason string, updatedMs int64) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(nodeIDs)
	rows, err := s.loadProfileNodeStatusesByIDs(ctx, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	expectedReason := strings.TrimSpace(reason)
	for index, row := range rows {
		if row.ID != expectedIDs[index] ||
			row.ProfileStatus != logicdomain.ProfileStatusExpired ||
			strings.TrimSpace(row.StatusReason) != expectedReason ||
			!sessionTimestampReached(unixMilliToTime(row.UpdatedTimestamp), updatedMs) {
			return false, nil
		}
	}
	return true, nil
}

// sameProfileInstructionStatusUpdate reports whether a read-back instruction row matches the exact status-update payload that may have committed.
// sameProfileInstructionStatusUpdate 用于判断回读的 instruction 行是否精确匹配一次可能已经提交的状态更新载荷。
func sameProfileInstructionStatusUpdate(stored logicdomain.ProfileInstructionRecord, instructionID uint64, status int, reviewResult, failureReason string, updatedMs int64) bool {
	return stored.ID == instructionID &&
		stored.Status == status &&
		strings.TrimSpace(stored.ReviewResult) == strings.TrimSpace(reviewResult) &&
		strings.TrimSpace(stored.FailureReason) == strings.TrimSpace(failureReason) &&
		sessionTimestampReached(stored.UpdatedAt, updatedMs)
}

// reconcileProfileInstructionApplied verifies whether the instruction row already reached this write's applied payload after one uncertain update.
// reconcileProfileInstructionApplied 用于验证一条 instruction 行在不确定更新后是否已经进入本次写入要求的 applied 载荷。
func (s *Store) reconcileProfileInstructionApplied(ctx context.Context, instructionID uint64, reviewResult string, updatedMs int64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return sameProfileInstructionStatusUpdate(stored, instructionID, logicdomain.ProfileInstructionStatusApplied, reviewResult, "", updatedMs), nil
}

// reconcileProfileInstructionFailed verifies whether the instruction row already reached this write's failed payload after one uncertain failure-mark update.
// reconcileProfileInstructionFailed 用于验证一条 instruction 行在不确定失败回写后是否已经进入本次写入要求的 failed 载荷。
func (s *Store) reconcileProfileInstructionFailed(ctx context.Context, instructionID uint64, failureReason, reviewResult string, updatedMs int64) (bool, error) {
	stored, found, err := s.loadProfileInstructionByID(ctx, instructionID)
	if err != nil || !found {
		return false, err
	}
	return sameProfileInstructionStatusUpdate(stored, instructionID, logicdomain.ProfileInstructionStatusFailed, reviewResult, failureReason, updatedMs), nil
}

// reconcileRenderedProfileTarget verifies whether the durable scope profile blob already contains the final rendered text after one uncertain update.
// reconcileRenderedProfileTarget 用于验证在一次不确定更新后，长期 scope 画像 Blob 是否已经包含最终渲染文本。
func (s *Store) reconcileRenderedProfileTarget(ctx context.Context, profileType int, bindID uint64, renderedProfile string) (bool, error) {
	stored, err := s.loadRenderedProfileByTarget(ctx, profileType, bindID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(stored) == strings.TrimSpace(renderedProfile), nil
}

// reconcileRenderedProfileBatch verifies every row in one uncertain scope batch reached the exact profile and updated_at payload from this write.
// reconcileRenderedProfileBatch 用于验证一次不确定 scope 批量写入中的每一行都达到本次写入的精确 profile 和 updated_at 载荷。
func (s *Store) reconcileRenderedProfileBatch(ctx context.Context, profileType int, ids []uint64, profiles map[uint64]string, updatedAt string) (bool, error) {
	expectedIDs := storageutil.NormalizeUint64List(ids)
	if len(expectedIDs) == 0 {
		return true, nil
	}
	rows, err := s.loadRenderedProfileBatch(ctx, profileType, expectedIDs)
	if err != nil {
		return false, err
	}
	if len(rows) != len(expectedIDs) {
		return false, nil
	}
	for index, expectedID := range expectedIDs {
		expectedProfile, ok := profiles[expectedID]
		if !ok {
			return false, nil
		}
		row := rows[index]
		if row.ID != expectedID || row.Profile != expectedProfile || row.UpdatedAt != updatedAt {
			return false, nil
		}
	}
	return true, nil
}
