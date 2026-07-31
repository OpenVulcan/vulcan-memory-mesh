// management_mutation_steps.go contains verified SQLite mutation stages used by human-management operations.
// management_mutation_steps.go 用于保存人工管理操作复用的 SQLite 已校验变更阶段。
package vldb_sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// newSQLiteManagementOperation builds one running operation record.
// newSQLiteManagementOperation 用于构造一条运行中的操作记录。
func newSQLiteManagementOperation(operationID, idempotencyKey string, selection logicdomain.ManagementRemovalSelection, now time.Time) logicdomain.ManagementOperation {
	return logicdomain.ManagementOperation{
		ID: operationID, IdempotencyKey: idempotencyKey, Action: selection.Action,
		TargetType: selection.TargetType, TargetIDs: selection.TargetIDs, Status: "running",
		CreatedAt: now, UpdatedAt: now,
	}
}

// insertSQLiteManagementOperation persists the running idempotency fence.
// insertSQLiteManagementOperation 用于持久化运行中的幂等栅栏。
func (s *Store) insertSQLiteManagementOperation(ctx context.Context, operation logicdomain.ManagementOperation) error {
	targetJSON, _ := json.Marshal(operation.TargetIDs)
	return s.exec(ctx, `
INSERT INTO vmm_management_operations (
  operation_id, idempotency_key, action, target_type, target_ids_json, status,
  batch_id, error_code, error_message, created_timestamp, updated_timestamp, completed_timestamp
) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', ?, ?, 0)
`, operation.ID, operation.IdempotencyKey, operation.Action, operation.TargetType, string(targetJSON),
		operation.Status, operation.CreatedAt.UnixMilli(), operation.UpdatedAt.UnixMilli())
}

// sqliteManagementOperationByIdempotency returns a prior operation when the caller retries the same key.
// sqliteManagementOperationByIdempotency 用于在调用方重试同一幂等键时返回既有操作。
func (s *Store) sqliteManagementOperationByIdempotency(ctx context.Context, key string) (logicdomain.ManagementOperation, bool, error) {
	rows, err := queryRows[managementOperationRow](s, ctx, `
SELECT operation_id, idempotency_key, action, target_type, target_ids_json, status, batch_id,
       error_code, error_message, created_timestamp, updated_timestamp, completed_timestamp
FROM vmm_management_operations WHERE idempotency_key = ? LIMIT 1
`, strings.TrimSpace(key))
	if err != nil {
		return logicdomain.ManagementOperation{}, false, fmt.Errorf("query sqlite management idempotency key: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ManagementOperation{}, false, nil
	}
	operation, conversionErr := rows[0].toDomain()
	return operation, true, conversionErr
}

// completeSQLiteManagementOperation marks one operation terminal and consumes its preview.
// completeSQLiteManagementOperation 用于把操作标记为终态并消费其预览。
func (s *Store) completeSQLiteManagementOperation(ctx context.Context, operation logicdomain.ManagementOperation, batchID uint64, previewToken string, now time.Time) (logicdomain.ManagementOperation, error) {
	if err := s.exec(ctx, `
UPDATE vmm_management_operations
SET status = 'succeeded', batch_id = ?, updated_timestamp = ?, completed_timestamp = ?
WHERE operation_id = ? AND status = 'running'
`, batchID, now.UnixMilli(), now.UnixMilli(), operation.ID); err != nil {
		return operation, logicdomain.OutcomeUncertainError{Operation: operation.Action, Message: err.Error()}
	}
	if strings.TrimSpace(previewToken) != "" {
		if err := s.exec(ctx, `UPDATE vmm_management_previews SET consumed_timestamp = ? WHERE preview_token = ? AND consumed_timestamp = 0`, now.UnixMilli(), previewToken); err != nil {
			return operation, logicdomain.OutcomeUncertainError{Operation: operation.Action, Message: err.Error()}
		}
	}
	operation.Status = "succeeded"
	operation.BatchID = batchID
	operation.UpdatedAt = now
	operation.CompletedAt = now
	return operation, nil
}

// failSQLiteManagementOperation records a failure and returns an outcome-uncertain mutation error.
// failSQLiteManagementOperation 用于记录失败并返回结果不确定的变更错误。
func (s *Store) failSQLiteManagementOperation(ctx context.Context, operation logicdomain.ManagementOperation, code string, cause error) (logicdomain.ManagementOperation, error) {
	now := time.Now().UTC()
	message := strings.TrimSpace(cause.Error())
	_ = s.exec(ctx, `
UPDATE vmm_management_operations
SET status = 'failed', error_code = ?, error_message = ?, updated_timestamp = ?, completed_timestamp = ?
WHERE operation_id = ?
`, code, message, now.UnixMilli(), now.UnixMilli(), operation.ID)
	operation.Status = "failed"
	operation.ErrorCode = code
	operation.ErrorMessage = message
	operation.UpdatedAt = now
	operation.CompletedAt = now
	return operation, logicdomain.OutcomeUncertainError{Operation: operation.Action, Message: message}
}

// recycleSQLiteManagementSelection copies every directly derived row to trash before deleting active rows.
// recycleSQLiteManagementSelection 用于先把全部直接派生行复制到回收站，再删除活动行。
func (s *Store) recycleSQLiteManagementSelection(ctx context.Context, command logicdomain.ManagementMutationCommand) (uint64, error) {
	selection := command.Preview.Selection
	ids := normalizeSQLiteManagementIDs(selection.TargetIDs)
	placeholders := sqlitePlaceholders(len(ids))
	params := sqliteUint64Params(ids)
	turnWhere := fmt.Sprintf("id IN (%s)", placeholders)
	turnParams := params
	sessionWhere := ""
	if selection.TargetType == logicdomain.ManagementTargetSession {
		sessionWhere = fmt.Sprintf("id IN (%s)", placeholders)
		turnWhere = fmt.Sprintf("session_id IN (%s)", placeholders)
	}
	selectedTurnSQL := "SELECT id FROM vmm_turn_records WHERE " + turnWhere
	memoryWhere := "source_turn_id IN (" + selectedTurnSQL + ")"
	memoryParams := append([]any{}, turnParams...)
	if selection.TargetType == logicdomain.ManagementTargetSession {
		memoryWhere += fmt.Sprintf(" OR origin_session_id IN (%s)", placeholders)
		memoryParams = append(memoryParams, params...)
	}
	memoryRows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled,
       dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes WHERE `+memoryWhere, memoryParams...)
	if err != nil {
		return 0, fmt.Errorf("query sqlite management recycle memories: %w", err)
	}
	memoryIDs := make([]uint64, 0, len(memoryRows))
	for _, row := range memoryRows {
		memoryIDs = append(memoryIDs, row.ID)
	}
	batchID, err := s.nextNumericID(ctx, "vmm_recycle_batches")
	if err != nil {
		return 0, fmt.Errorf("allocate sqlite management recycle batch: %w", err)
	}
	targetJSON, _ := json.Marshal(ids)
	nowMillis := command.Now.UnixMilli()
	if err := s.exec(ctx, `
INSERT INTO vmm_recycle_batches (
  id, recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_timestamp, updated_timestamp
) VALUES (?, 'manual_management', 0, 0, 'manual management recycle', ?, 0, ?, ?)
`, batchID, nowMillis, nowMillis, nowMillis); err != nil {
		return 0, fmt.Errorf("insert sqlite management recycle batch: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_management_recycle_batches (
  batch_id, source, target_type, target_ids_json, restorable, state,
  expires_timestamp, restored_timestamp, operation_id
) VALUES (?, ?, ?, ?, 1, 'recycled', ?, 0, ?)
`, batchID, selection.Source, selection.TargetType, string(targetJSON), command.ExpiresAt.UnixMilli(), command.OperationID); err != nil {
		return 0, fmt.Errorf("insert sqlite management recycle metadata: %w", err)
	}
	if sessionWhere != "" {
		if err := s.exec(ctx, fmt.Sprintf(`
INSERT INTO vmm_sessions_trash (
  batch_id, recycled_at, recycle_reason, id, session_key, user_id, team_id, space_id, project_id,
  turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
  created_timestamp, updated_timestamp
)
SELECT ?, ?, 'manual management recycle', id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions WHERE %s
`, sessionWhere), append([]any{batchID, nowMillis}, params...)...); err != nil {
			return 0, fmt.Errorf("copy sqlite sessions to management trash: %w", err)
		}
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_memory_context_edges_trash (
  batch_id, recycled_at, recycle_reason, memory_id, context_key, context_value,
  support_count, rebuttal_count, last_supported_timestamp, last_rebutted_timestamp,
  created_timestamp, updated_timestamp
)
SELECT ?, ?, 'manual management recycle', memory_id, context_key, context_value,
       support_count, rebuttal_count, last_supported_timestamp, last_rebutted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_memory_context_edges WHERE memory_id IN (SELECT id FROM vmm_memory_nodes WHERE `+memoryWhere+")", append([]any{batchID, nowMillis}, memoryParams...)...); err != nil {
		return 0, fmt.Errorf("copy sqlite memory contexts to management trash: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_memory_nodes_trash (
  batch_id, recycled_at, recycle_reason, id, team_id, space_id, project_id, user_id,
  origin_session_id, source_turn_id, vector_id, vector_json, source_kind, scope_level,
  category, abstract, details, memory_status, priority, memory_level, refresh_weight,
  support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp,
  last_adopted_timestamp, last_reinforced_timestamp, recalled_count, adopted_count,
  reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_timestamp, updated_timestamp
)
SELECT ?, ?, 'manual management recycle', id, team_id, space_id, project_id, user_id,
       origin_session_id, source_turn_id, vector_id, vector_json, source_kind, scope_level,
       category, abstract, details, memory_status, priority, memory_level, refresh_weight,
       support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp,
       last_adopted_timestamp, last_reinforced_timestamp, recalled_count, adopted_count,
       reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_timestamp, updated_timestamp
FROM vmm_memory_nodes WHERE `+memoryWhere, append([]any{batchID, nowMillis}, memoryParams...)...); err != nil {
		return 0, fmt.Errorf("copy sqlite memories to management trash: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_profile_nodes_trash (
  batch_id, recycled_at, recycle_reason, id, turn_id, profile_type, bind_id, content,
  profile_status, priority, profile_level, level_reason, refresh_weight, source_kind,
  source_id, status_reason, expires_timestamp, superseded_by_id, profile_date,
  created_timestamp, updated_timestamp
)
SELECT ?, ?, 'manual management recycle', id, turn_id, profile_type, bind_id, content,
       profile_status, priority, profile_level, level_reason, refresh_weight, source_kind,
       source_id, status_reason, expires_timestamp, superseded_by_id, profile_date,
       created_timestamp, updated_timestamp
FROM vmm_profile_nodes WHERE turn_id IN (`+selectedTurnSQL+")", append([]any{batchID, nowMillis}, turnParams...)...); err != nil {
		return 0, fmt.Errorf("copy sqlite profiles to management trash: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_turn_records_trash (
  batch_id, recycled_at, recycle_reason, id, session_id, project_id, dehydrated_content,
  dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp
)
SELECT ?, ?, 'manual management recycle', id, session_id, project_id, dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp
FROM vmm_turn_records WHERE `+turnWhere, append([]any{batchID, nowMillis}, turnParams...)...); err != nil {
		return 0, fmt.Errorf("copy sqlite turns to management trash: %w", err)
	}
	if err := s.exec(ctx, "DELETE FROM vmm_memory_context_edges WHERE memory_id IN (SELECT id FROM vmm_memory_nodes WHERE "+memoryWhere+")", memoryParams...); err != nil {
		return 0, fmt.Errorf("delete sqlite recycled contexts: %w", err)
	}
	if err := s.exec(ctx, "DELETE FROM vmm_memory_nodes WHERE "+memoryWhere, memoryParams...); err != nil {
		return 0, fmt.Errorf("delete sqlite recycled memories: %w", err)
	}
	if err := s.exec(ctx, "DELETE FROM vmm_profile_nodes WHERE turn_id IN ("+selectedTurnSQL+")", turnParams...); err != nil {
		return 0, fmt.Errorf("delete sqlite recycled profiles: %w", err)
	}
	if err := s.exec(ctx, "DELETE FROM vmm_turn_records WHERE "+turnWhere, turnParams...); err != nil {
		return 0, fmt.Errorf("delete sqlite recycled turns: %w", err)
	}
	if sessionWhere != "" {
		stateParams := append([]any{nowMillis}, params...)
		if err := s.exec(ctx, fmt.Sprintf(`
INSERT INTO vmm_management_session_states (session_id, status, revision, updated_timestamp)
SELECT id, 'recycled', 1, ? FROM vmm_sessions WHERE %s
ON CONFLICT(session_id) DO UPDATE SET status = 'recycled', revision = revision + 1, updated_timestamp = excluded.updated_timestamp
`, sessionWhere), append([]any{nowMillis}, params...)...); err != nil {
			return 0, fmt.Errorf("mark sqlite recycled session states: %w", err)
		}
		if err := s.exec(ctx, "UPDATE vmm_sessions SET last_summarized_id = 0, last_compacted_turn_id = 0, summarize_content = '', summarize_budget = 0, last_extract_observed_timestamp = 0, last_extract_completed_timestamp = 0, last_compacted_timestamp = 0, updated_timestamp = ? WHERE "+sessionWhere, stateParams...); err != nil {
			return 0, fmt.Errorf("scrub sqlite recycled session tombstones: %w", err)
		}
	}
	if len(memoryIDs) > 0 && s.database != nil {
		if err := s.syncMemoryFTSAfterWrite(ctx, nil, memoryIDs); err != nil {
			return batchID, fmt.Errorf("sync sqlite FTS after management recycle: %w", err)
		}
	}
	return batchID, nil
}

// restoreSQLiteManagementBatchRows restores all manual trash rows after deterministic conflict checks.
// restoreSQLiteManagementBatchRows 用于在确定性冲突检查后恢复全部人工回收行。
func (s *Store) restoreSQLiteManagementBatchRows(ctx context.Context, batchID uint64) error {
	conflictQueries := []string{
		`SELECT COUNT(*) AS count FROM vmm_sessions active JOIN vmm_sessions_trash trash ON (trash.id = active.id OR (trash.project_id = active.project_id AND trash.session_key = active.session_key)) LEFT JOIN vmm_management_session_states state ON state.session_id = active.id WHERE trash.batch_id = ? AND NOT (active.id = trash.id AND active.project_id = trash.project_id AND active.session_key = trash.session_key AND state.status = 'recycled')`,
		`SELECT COUNT(*) AS count FROM vmm_turn_records active JOIN vmm_turn_records_trash trash ON trash.id = active.id WHERE trash.batch_id = ?`,
		`SELECT COUNT(*) AS count FROM vmm_memory_nodes active JOIN vmm_memory_nodes_trash trash ON trash.id = active.id OR trash.vector_id = active.vector_id WHERE trash.batch_id = ?`,
		`SELECT COUNT(*) AS count FROM vmm_profile_nodes active JOIN vmm_profile_nodes_trash trash ON trash.id = active.id WHERE trash.batch_id = ?`,
	}
	for _, query := range conflictQueries {
		count, err := s.countRows(ctx, query, batchID)
		if err != nil {
			return fmt.Errorf("check sqlite management restore conflicts: %w", err)
		}
		if count > 0 {
			return logicdomain.ConflictError{Resource: "recycle batch", Message: "active data now conflicts with the recycled snapshot"}
		}
	}
	memoryRows, err := queryRows[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled,
       dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes_trash WHERE batch_id = ?
`, batchID)
	if err != nil {
		return fmt.Errorf("query sqlite restore memory rows: %w", err)
	}
	statements := []string{
		`UPDATE vmm_sessions SET user_id = (SELECT user_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), team_id = (SELECT team_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), space_id = (SELECT space_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), project_id = (SELECT project_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), turn_count = (SELECT turn_count FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), last_summarized_id = (SELECT last_summarized_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), last_compacted_turn_id = (SELECT last_compacted_turn_id FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), summarize_content = (SELECT summarize_content FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), summarize_budget = (SELECT summarize_budget FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), last_extract_observed_timestamp = (SELECT last_extract_observed_timestamp FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), last_extract_completed_timestamp = (SELECT last_extract_completed_timestamp FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), last_compacted_timestamp = (SELECT last_compacted_timestamp FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), created_timestamp = (SELECT created_timestamp FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id), updated_timestamp = (SELECT updated_timestamp FROM vmm_sessions_trash WHERE batch_id = ? AND id = vmm_sessions.id) WHERE id IN (SELECT id FROM vmm_sessions_trash WHERE batch_id = ?)`,
		`INSERT INTO vmm_turn_records SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp FROM vmm_turn_records_trash WHERE batch_id = ?`,
		`INSERT INTO vmm_memory_nodes SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, vector_json, source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason, expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash, created_timestamp, updated_timestamp FROM vmm_memory_nodes_trash WHERE batch_id = ?`,
		`INSERT INTO vmm_memory_context_edges SELECT memory_id, context_key, context_value, support_count, rebuttal_count, last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp FROM vmm_memory_context_edges_trash WHERE batch_id = ?`,
		`INSERT INTO vmm_profile_nodes SELECT id, turn_id, profile_type, bind_id, content, profile_status, priority, profile_level, level_reason, refresh_weight, source_kind, source_id, status_reason, expires_timestamp, superseded_by_id, profile_date, created_timestamp, updated_timestamp FROM vmm_profile_nodes_trash WHERE batch_id = ?`,
	}
	for index, statement := range statements {
		arguments := []any{batchID}
		if index == 0 {
			arguments = make([]any, 15)
			for argumentIndex := range arguments {
				arguments[argumentIndex] = batchID
			}
		}
		if err := s.exec(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("restore sqlite management batch rows: %w", err)
		}
	}
	if err := s.exec(ctx, `
UPDATE vmm_management_session_states
SET status = 'active', revision = revision + 1, updated_timestamp = ?
WHERE session_id IN (SELECT id FROM vmm_sessions_trash WHERE batch_id = ?)
`, time.Now().UTC().UnixMilli(), batchID); err != nil {
		return fmt.Errorf("reactivate sqlite restored session states: %w", err)
	}
	if s.database != nil && len(memoryRows) > 0 {
		records := make([]logicdomain.MemoryNodeRecord, 0, len(memoryRows))
		for _, row := range memoryRows {
			records = append(records, row.toMemoryNodeRecord())
		}
		if err := s.syncMemoryFTSAfterWrite(ctx, records, nil); err != nil {
			return fmt.Errorf("sync sqlite FTS after management restore: %w", err)
		}
	}
	for _, table := range []string{"vmm_memory_context_edges_trash", "vmm_memory_nodes_trash", "vmm_profile_nodes_trash", "vmm_turn_records_trash", "vmm_sessions_trash"} {
		if err := s.exec(ctx, "DELETE FROM "+table+" WHERE batch_id = ?", batchID); err != nil {
			return fmt.Errorf("delete restored sqlite management trash rows: %w", err)
		}
	}
	return nil
}

// getSQLiteManagementRecycleBatch loads one batch with manual metadata when present.
// getSQLiteManagementRecycleBatch 用于加载一个批次及其可用的人工元数据。
func (s *Store) getSQLiteManagementRecycleBatch(ctx context.Context, batchID uint64) (logicdomain.ManagementRecycleBatch, error) {
	rows, err := queryRows[managementRecycleBatchRow](s, ctx, `
SELECT b.id AS batch_id, COALESCE(m.source, 'system') AS source,
       COALESCE(m.target_type, '') AS target_type, COALESCE(m.target_ids_json, '[]') AS target_ids_json,
       COALESCE(m.restorable, 0) AS restorable,
       COALESCE(m.state, CASE WHEN b.purged_at > 0 THEN 'purged' ELSE 'system' END) AS state,
       b.reason, b.recycled_at, COALESCE(m.expires_timestamp, 0) AS expires_timestamp,
       COALESCE(m.restored_timestamp, 0) AS restored_timestamp, b.purged_at,
       (SELECT COUNT(*) FROM vmm_sessions_trash st WHERE st.batch_id = b.id) AS session_count,
       (SELECT COUNT(*) FROM vmm_turn_records_trash tt WHERE tt.batch_id = b.id) AS turn_count,
       (SELECT COUNT(*) FROM vmm_memory_nodes_trash mt WHERE mt.batch_id = b.id) AS memory_count,
       (SELECT COUNT(*) FROM vmm_profile_nodes_trash pt WHERE pt.batch_id = b.id) AS profile_count
FROM vmm_recycle_batches b
LEFT JOIN vmm_management_recycle_batches m ON m.batch_id = b.id
WHERE b.id = ? LIMIT 1
`, batchID)
	if err != nil {
		return logicdomain.ManagementRecycleBatch{}, fmt.Errorf("get sqlite management recycle batch: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ManagementRecycleBatch{}, logicdomain.NotFoundError{Resource: "recycle batch", Message: "batch does not exist"}
	}
	return rows[0].toDomain()
}
