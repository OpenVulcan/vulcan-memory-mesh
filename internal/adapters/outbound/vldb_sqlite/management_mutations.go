// management_mutations.go implements preview-bound human management writes for SQLite.
// management_mutations.go 用于实现受预览约束的 SQLite 人工管理写入。
package vldb_sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// managementInt64Row maps one scalar SQLite aggregate.
// managementInt64Row 用于映射一个 SQLite 标量聚合结果。
type managementInt64Row struct {
	Value int64 `json:"value"`
}

// managementVectorIDRow maps one quarantined sidecar vector identifier.
// managementVectorIDRow 用于映射一个被隔离的旁路向量标识。
type managementVectorIDRow struct {
	VectorID string `json:"vector_id"`
}

// managementPreviewRow maps one persisted preview.
// managementPreviewRow 用于映射一条持久化预览。
type managementPreviewRow struct {
	PreviewToken      string `json:"preview_token"`
	TargetType        string `json:"target_type"`
	TargetIDsJSON     string `json:"target_ids_json"`
	Action            string `json:"action"`
	Source            string `json:"source"`
	ImpactJSON        string `json:"impact_json"`
	ImpactRevision    string `json:"impact_revision"`
	ConfirmText       string `json:"confirm_text"`
	CreatedTimestamp  int64  `json:"created_timestamp"`
	ExpiresTimestamp  int64  `json:"expires_timestamp"`
	ConsumedTimestamp int64  `json:"consumed_timestamp"`
}

// managementOperationRow maps one persisted idempotent operation.
// managementOperationRow 用于映射一条持久化幂等操作。
type managementOperationRow struct {
	OperationID        string `json:"operation_id"`
	IdempotencyKey     string `json:"idempotency_key"`
	Action             string `json:"action"`
	TargetType         string `json:"target_type"`
	TargetIDsJSON      string `json:"target_ids_json"`
	Status             string `json:"status"`
	BatchID            uint64 `json:"batch_id"`
	ErrorCode          string `json:"error_code"`
	ErrorMessage       string `json:"error_message"`
	CreatedTimestamp   int64  `json:"created_timestamp"`
	UpdatedTimestamp   int64  `json:"updated_timestamp"`
	CompletedTimestamp int64  `json:"completed_timestamp"`
}

// managementRecycleBatchRow maps one batch and optional manual-management metadata.
// managementRecycleBatchRow 用于映射一个回收批次及可选人工管理元数据。
type managementRecycleBatchRow struct {
	BatchID           uint64 `json:"batch_id"`
	Source            string `json:"source"`
	TargetType        string `json:"target_type"`
	TargetIDsJSON     string `json:"target_ids_json"`
	Restorable        int    `json:"restorable"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	RecycledAt        int64  `json:"recycled_at"`
	ExpiresTimestamp  int64  `json:"expires_timestamp"`
	RestoredTimestamp int64  `json:"restored_timestamp"`
	PurgedAt          int64  `json:"purged_at"`
	SessionCount      int    `json:"session_count"`
	TurnCount         int    `json:"turn_count"`
	MemoryCount       int    `json:"memory_count"`
	ProfileCount      int    `json:"profile_count"`
}

// InspectManagementSelection computes a deterministic live impact revision for one target set.
// InspectManagementSelection 用于为一组目标计算确定性的实时影响修订。
func (s *Store) InspectManagementSelection(ctx context.Context, selection logicdomain.ManagementRemovalSelection, hotWindowSize int) (logicdomain.ManagementImpact, error) {
	ids := normalizeSQLiteManagementIDs(selection.TargetIDs)
	if len(ids) == 0 {
		return logicdomain.ManagementImpact{}, logicdomain.ValidationError{Field: "target_ids", Message: "must not be empty"}
	}
	placeholders := sqlitePlaceholders(len(ids))
	params := sqliteUint64Params(ids)
	impact := logicdomain.ManagementImpact{}
	counter := sqliteManagementCounter{store: s, ctx: ctx}
	var turnWhere string
	var selectedTurnWhere string
	var turnParams []any
	switch selection.TargetType {
	case logicdomain.ManagementTargetSession:
		impact.SessionCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_sessions WHERE id IN (%s)`, placeholders), params...)
		for _, sessionID := range ids {
			status, err := s.loadSessionMemoryStatus(ctx, sessionID)
			if err != nil {
				return logicdomain.ManagementImpact{}, err
			}
			if err := status.ValidateManagementAction(selection.Action); err != nil {
				return logicdomain.ManagementImpact{}, err
			}
		}
		turnWhere = fmt.Sprintf("session_id IN (%s)", placeholders)
		selectedTurnWhere = fmt.Sprintf("selected.session_id IN (%s)", placeholders)
		turnParams = params
	case logicdomain.ManagementTargetTurn:
		impact.TurnCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_turn_records WHERE id IN (%s)`, placeholders), params...)
		impact.SessionCount = counter.count(fmt.Sprintf(`SELECT COUNT(DISTINCT session_id) AS count FROM vmm_turn_records WHERE id IN (%s)`, placeholders), params...)
		turnWhere = fmt.Sprintf("id IN (%s)", placeholders)
		selectedTurnWhere = fmt.Sprintf("selected.id IN (%s)", placeholders)
		turnParams = params
	case logicdomain.ManagementTargetRecycleBatch:
		batchCount := counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_recycle_batches WHERE id IN (%s) AND purged_at = 0`, placeholders), params...)
		if counter.err != nil {
			return logicdomain.ManagementImpact{}, counter.err
		}
		if batchCount != len(ids) {
			return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "recycle batch", Message: "one or more recycle batches do not exist"}
		}
		impact.SessionCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_sessions_trash WHERE batch_id IN (%s)`, placeholders), params...)
		impact.TurnCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_turn_records_trash WHERE batch_id IN (%s)`, placeholders), params...)
		impact.MemoryCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_nodes_trash WHERE batch_id IN (%s)`, placeholders), params...)
		impact.ContextEdgeCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_memory_context_edges_trash WHERE batch_id IN (%s)`, placeholders), params...)
		impact.ProfileNodeCount = counter.count(fmt.Sprintf(`SELECT COUNT(*) AS count FROM vmm_profile_nodes_trash WHERE batch_id IN (%s)`, placeholders), params...)
		if counter.err != nil {
			return logicdomain.ManagementImpact{}, counter.err
		}
		impact.VectorCount = impact.MemoryCount
		impact.Revision = sqliteManagementImpactRevision(selection, impact, int64(batchCount))
		return impact, nil
	default:
		return logicdomain.ManagementImpact{}, logicdomain.ValidationError{Field: "target_type", Message: "is unsupported"}
	}
	if counter.err != nil {
		return logicdomain.ManagementImpact{}, counter.err
	}
	if selection.TargetType == logicdomain.ManagementTargetSession && impact.SessionCount != len(ids) {
		return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "session", Message: "one or more sessions do not exist"}
	}
	if selection.TargetType == logicdomain.ManagementTargetTurn && impact.TurnCount != len(ids) {
		return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "turn", Message: "one or more turns do not exist"}
	}
	if selection.TargetType == logicdomain.ManagementTargetSession {
		impact.TurnCount = counter.count("SELECT COUNT(*) AS count FROM vmm_turn_records WHERE "+turnWhere, turnParams...)
	}
	impact.PendingTurnCount = counter.count("SELECT COUNT(*) AS count FROM vmm_turn_records WHERE "+turnWhere+" AND extracted_status = ?", append(turnParams, logicdomain.TurnExtractedStatusPending)...)
	selectedTurnSQL := "SELECT id FROM vmm_turn_records WHERE " + turnWhere
	selectedParams := turnParams
	memoryWhere := "source_turn_id IN (" + selectedTurnSQL + ")"
	memoryParams := append([]any{}, selectedParams...)
	if selection.TargetType == logicdomain.ManagementTargetSession {
		memoryWhere += fmt.Sprintf(" OR origin_session_id IN (%s)", placeholders)
		memoryParams = append(memoryParams, params...)
	}
	impact.MemoryCount = counter.count("SELECT COUNT(*) AS count FROM vmm_memory_nodes WHERE "+memoryWhere, memoryParams...)
	impact.ContextEdgeCount = counter.count("SELECT COUNT(*) AS count FROM vmm_memory_context_edges WHERE memory_id IN (SELECT id FROM vmm_memory_nodes WHERE "+memoryWhere+")", memoryParams...)
	impact.ProfileNodeCount = counter.count("SELECT COUNT(*) AS count FROM vmm_profile_nodes WHERE turn_id IN ("+selectedTurnSQL+")", selectedParams...)
	impact.VectorCount = impact.MemoryCount
	if hotWindowSize > 0 {
		hotSQL := "SELECT COUNT(*) AS count FROM vmm_turn_records selected WHERE " + selectedTurnWhere + " AND (SELECT COUNT(*) FROM vmm_turn_records newer WHERE newer.session_id = selected.session_id AND newer.id > selected.id) < ?"
		impact.HotWindowTurnCount = counter.count(hotSQL, append(turnParams, hotWindowSize)...)
	}
	protectedSQL := "SELECT COUNT(*) AS count FROM vmm_turn_records selected WHERE " + selectedTurnWhere + " AND (selected.extracted_status = ?"
	protectedParams := append(append([]any{}, turnParams...), logicdomain.TurnExtractedStatusPending)
	if hotWindowSize > 0 {
		protectedSQL += " OR (SELECT COUNT(*) FROM vmm_turn_records newer WHERE newer.session_id = selected.session_id AND newer.id > selected.id) < ?"
		protectedParams = append(protectedParams, hotWindowSize)
	}
	protectedSQL += ")"
	impact.ProtectedCount = counter.count(protectedSQL, protectedParams...)
	maxUpdated := counter.int64("SELECT COALESCE(MAX(updated_timestamp), 0) AS value FROM vmm_turn_records WHERE "+turnWhere, turnParams...)
	if counter.err != nil {
		return logicdomain.ManagementImpact{}, counter.err
	}
	impact.Revision = sqliteManagementImpactRevision(selection, impact, maxUpdated)
	return impact, nil
}

// SaveManagementPreview persists one short-lived preview before any mutation is allowed.
// SaveManagementPreview 用于在允许任何变更前持久化一条短期预览。
func (s *Store) SaveManagementPreview(ctx context.Context, preview logicdomain.ManagementPreview) error {
	targetJSON, _ := json.Marshal(preview.Selection.TargetIDs)
	impactJSON, _ := json.Marshal(preview.Impact)
	return s.exec(ctx, `
INSERT INTO vmm_management_previews (
  preview_token, target_type, target_ids_json, action, source, impact_json, impact_revision,
  confirm_text, created_timestamp, expires_timestamp, consumed_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
`, preview.Token, preview.Selection.TargetType, string(targetJSON), preview.Selection.Action, preview.Selection.Source,
		string(impactJSON), preview.Impact.Revision, preview.ConfirmText, preview.CreatedAt.UnixMilli(), preview.ExpiresAt.UnixMilli())
}

// GetManagementPreview returns one exact persisted preview.
// GetManagementPreview 用于返回一条精确持久化预览。
func (s *Store) GetManagementPreview(ctx context.Context, token string) (logicdomain.ManagementPreview, error) {
	rows, err := queryRows[managementPreviewRow](s, ctx, `
SELECT preview_token, target_type, target_ids_json, action, source, impact_json, impact_revision,
       confirm_text, created_timestamp, expires_timestamp, consumed_timestamp
FROM vmm_management_previews WHERE preview_token = ? LIMIT 1
`, strings.TrimSpace(token))
	if err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("get sqlite management preview: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ManagementPreview{}, logicdomain.NotFoundError{Resource: "preview", Message: "preview does not exist"}
	}
	return rows[0].toDomain()
}

// ListManagementRecycleBatchVectorIDs returns the exact sidecar identifiers retained until permanent purge.
// ListManagementRecycleBatchVectorIDs 返回在永久清理前持续保留的准确旁路向量标识。
func (s *Store) ListManagementRecycleBatchVectorIDs(ctx context.Context, batchIDs []uint64) ([]string, error) {
	ids := normalizeSQLiteManagementIDs(batchIDs)
	if len(ids) == 0 {
		return []string{}, nil
	}
	rows, err := queryRows[managementVectorIDRow](s, ctx, fmt.Sprintf(`
SELECT DISTINCT vector_id
FROM vmm_memory_nodes_trash
WHERE batch_id IN (%s) AND TRIM(vector_id) <> ''
ORDER BY vector_id
`, sqlitePlaceholders(len(ids))), sqliteUint64Params(ids)...)
	if err != nil {
		return nil, fmt.Errorf("list sqlite management recycle vector ids: %w", err)
	}
	vectorIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if vectorID := strings.TrimSpace(row.VectorID); vectorID != "" {
			vectorIDs = append(vectorIDs, vectorID)
		}
	}
	return vectorIDs, nil
}

// ApplyManagementRemoval executes archive, unarchive, recycle, or restart against an already revalidated preview.
// ApplyManagementRemoval 用于针对已重新校验的预览执行归档、取消归档、回收或重新开始。
func (s *Store) ApplyManagementRemoval(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if operation, ok, err := s.sqliteManagementOperationByIdempotency(ctx, command.IdempotencyKey); err != nil || ok {
		if err == nil && ok && !command.Preview.Selection.MatchesOperation(operation) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return operation, err
	}
	if command.Preview.Selection.TargetType == logicdomain.ManagementTargetSession {
		for _, sessionID := range command.Preview.Selection.TargetIDs {
			status, err := s.loadSessionMemoryStatus(ctx, sessionID)
			if err != nil {
				return logicdomain.ManagementOperation{}, err
			}
			if err := status.ValidateManagementAction(command.Preview.Selection.Action); err != nil {
				return logicdomain.ManagementOperation{}, err
			}
		}
	}
	operation := newSQLiteManagementOperation(command.OperationID, command.IdempotencyKey, command.Preview.Selection, command.Now)
	if err := s.insertSQLiteManagementOperation(ctx, operation); err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if command.Preview.Selection.Action == logicdomain.ManagementActionArchive || command.Preview.Selection.Action == logicdomain.ManagementActionUnarchive {
		status := "archived"
		if command.Preview.Selection.Action == logicdomain.ManagementActionUnarchive {
			status = "active"
		}
		for _, sessionID := range command.Preview.Selection.TargetIDs {
			if err := s.exec(ctx, `
INSERT INTO vmm_management_session_states (session_id, status, revision, updated_timestamp)
VALUES (?, ?, 1, ?)
ON CONFLICT(session_id) DO UPDATE SET status = excluded.status, revision = revision + 1, updated_timestamp = excluded.updated_timestamp
`, sessionID, status, command.Now.UnixMilli()); err != nil {
				return s.failSQLiteManagementOperation(ctx, operation, "archive_failed", err)
			}
		}
		return s.completeSQLiteManagementOperation(ctx, operation, 0, command.Preview.Token, command.Now)
	}
	if command.Preview.Selection.Action == logicdomain.ManagementActionRestart {
		for _, sessionID := range command.Preview.Selection.TargetIDs {
			if err := s.execExpectRowsChanged(ctx, "restart forgotten session", 1, `
UPDATE vmm_management_session_states
SET status = 'active', revision = revision + 1, updated_timestamp = ?
WHERE session_id = ? AND status = 'forgotten'
`, command.Now.UnixMilli(), sessionID); err != nil {
				return s.failSQLiteManagementOperation(ctx, operation, "restart_failed", err)
			}
		}
		return s.completeSQLiteManagementOperation(ctx, operation, 0, command.Preview.Token, command.Now)
	}
	batchID, err := s.recycleSQLiteManagementSelection(ctx, command)
	if err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "recycle_failed", err)
	}
	return s.completeSQLiteManagementOperation(ctx, operation, batchID, command.Preview.Token, command.Now)
}

// RestoreManagementBatch restores one manual restorable batch after checking active-row conflicts.
// RestoreManagementBatch 用于在检查活动行冲突后恢复一个人工可恢复批次。
func (s *Store) RestoreManagementBatch(ctx context.Context, command logicdomain.ManagementRestoreCommand) (logicdomain.ManagementOperation, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if operation, ok, err := s.sqliteManagementOperationByIdempotency(ctx, command.IdempotencyKey); err != nil || ok {
		selection := logicdomain.ManagementRemovalSelection{TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{command.BatchID}, Action: logicdomain.ManagementActionRestore}
		if err == nil && ok && !selection.MatchesOperation(operation) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return operation, err
	}
	batch, err := s.getSQLiteManagementRecycleBatch(ctx, command.BatchID)
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if !batch.Restorable || batch.State != "recycled" {
		return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "recycle batch", Message: "batch is not restorable"}
	}
	if !batch.ExpiresAt.IsZero() && !batch.ExpiresAt.After(command.Now) {
		return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "recycle batch", Message: "batch restore window has expired"}
	}
	selection := logicdomain.ManagementRemovalSelection{TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{command.BatchID}, Action: logicdomain.ManagementActionRestore}
	operation := newSQLiteManagementOperation(command.OperationID, command.IdempotencyKey, selection, command.Now)
	if err := s.insertSQLiteManagementOperation(ctx, operation); err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if err := s.restoreSQLiteManagementBatchRows(ctx, command.BatchID); err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "restore_conflict", err)
	}
	if err := s.exec(ctx, `UPDATE vmm_management_recycle_batches SET state = 'restored', restored_timestamp = ? WHERE batch_id = ?`, command.Now.UnixMilli(), command.BatchID); err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "restore_state_failed", err)
	}
	return s.completeSQLiteManagementOperation(ctx, operation, command.BatchID, "", command.Now)
}

// PurgeManagementBatch permanently removes preview-bound recycle batches.
// PurgeManagementBatch 用于永久移除受预览约束的回收批次。
func (s *Store) PurgeManagementBatch(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if operation, ok, err := s.sqliteManagementOperationByIdempotency(ctx, command.IdempotencyKey); err != nil || ok {
		if err == nil && ok && !command.Preview.Selection.MatchesOperation(operation) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return operation, err
	}
	operation := newSQLiteManagementOperation(command.OperationID, command.IdempotencyKey, command.Preview.Selection, command.Now)
	if err := s.insertSQLiteManagementOperation(ctx, operation); err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	ids := normalizeSQLiteManagementIDs(command.Preview.Selection.TargetIDs)
	placeholders := sqlitePlaceholders(len(ids))
	params := sqliteUint64Params(ids)
	stateParams := append([]any{command.Now.UnixMilli()}, params...)
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_management_session_states
SET status = 'forgotten', revision = revision + 1, updated_timestamp = ?
WHERE session_id IN (SELECT id FROM vmm_sessions_trash WHERE batch_id IN (%s))
`, placeholders), stateParams...); err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "purge_failed", err)
	}
	if err := s.exec(ctx, fmt.Sprintf(`
UPDATE vmm_sessions
SET last_summarized_id = 0, last_compacted_turn_id = 0, summarize_content = '', summarize_budget = 0,
    last_extract_observed_timestamp = 0, last_extract_completed_timestamp = 0,
    last_compacted_timestamp = 0, updated_timestamp = ?
WHERE id IN (SELECT id FROM vmm_sessions_trash WHERE batch_id IN (%s))
`, placeholders), stateParams...); err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "purge_failed", err)
	}
	for _, table := range []string{"vmm_memory_context_edges_trash", "vmm_memory_nodes_trash", "vmm_profile_nodes_trash", "vmm_turn_records_trash", "vmm_sessions_trash", "vmm_management_recycle_batches"} {
		column := "batch_id"
		if err := s.exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)", table, column, placeholders), params...); err != nil {
			return s.failSQLiteManagementOperation(ctx, operation, "purge_failed", err)
		}
	}
	if err := s.exec(ctx, fmt.Sprintf("DELETE FROM vmm_recycle_batches WHERE id IN (%s)", placeholders), params...); err != nil {
		return s.failSQLiteManagementOperation(ctx, operation, "purge_failed", err)
	}
	return s.completeSQLiteManagementOperation(ctx, operation, 0, command.Preview.Token, command.Now)
}

// GetManagementOperation returns one persisted operation by opaque identifier.
// GetManagementOperation 用于按不透明标识返回一条持久化操作。
func (s *Store) GetManagementOperation(ctx context.Context, operationID string) (logicdomain.ManagementOperation, error) {
	rows, err := queryRows[managementOperationRow](s, ctx, `
SELECT operation_id, idempotency_key, action, target_type, target_ids_json, status, batch_id,
       error_code, error_message, created_timestamp, updated_timestamp, completed_timestamp
FROM vmm_management_operations WHERE operation_id = ? LIMIT 1
`, strings.TrimSpace(operationID))
	if err != nil {
		return logicdomain.ManagementOperation{}, fmt.Errorf("get sqlite management operation: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ManagementOperation{}, logicdomain.NotFoundError{Resource: "operation", Message: "operation does not exist"}
	}
	return rows[0].toDomain()
}

// GetManagementOperationByIdempotencyKey returns one SQLite operation by its caller-stable key.
// GetManagementOperationByIdempotencyKey 用于按调用方稳定幂等键返回一条 SQLite 操作。
func (s *Store) GetManagementOperationByIdempotencyKey(ctx context.Context, idempotencyKey string) (logicdomain.ManagementOperation, error) {
	operation, found, err := s.sqliteManagementOperationByIdempotency(ctx, idempotencyKey)
	if err != nil {
		return logicdomain.ManagementOperation{}, err
	}
	if !found {
		return logicdomain.ManagementOperation{}, logicdomain.NotFoundError{Resource: "operation", Message: "operation does not exist"}
	}
	return operation, nil
}

// ListManagementRecycleBatches returns manual and historical system batches without pretending old batches are restorable.
// ListManagementRecycleBatches 用于返回人工与历史系统批次，同时不会把旧批次伪装成可恢复数据。
func (s *Store) ListManagementRecycleBatches(ctx context.Context, cursorID uint64, limit int) (logicdomain.ManagementRecycleBatchPage, error) {
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
WHERE (? = 0 OR b.id < ?)
ORDER BY b.id DESC LIMIT ?
`, cursorID, cursorID, limit+1)
	if err != nil {
		return logicdomain.ManagementRecycleBatchPage{}, fmt.Errorf("list sqlite management recycle batches: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]logicdomain.ManagementRecycleBatch, 0, len(rows))
	for _, row := range rows {
		batch, conversionErr := row.toDomain()
		if conversionErr != nil {
			return logicdomain.ManagementRecycleBatchPage{}, conversionErr
		}
		items = append(items, batch)
	}
	page := logicdomain.ManagementRecycleBatchPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextCursorID = items[len(items)-1].BatchID
	}
	return page, nil
}

// GetManagementRecycleBatch returns one batch by identifier.
// GetManagementRecycleBatch 用于按标识返回一个回收批次。
func (s *Store) GetManagementRecycleBatch(ctx context.Context, batchID uint64) (logicdomain.ManagementRecycleBatch, error) {
	return s.getSQLiteManagementRecycleBatch(ctx, batchID)
}

// sqliteManagementCounter preserves the first aggregate error while allowing compact impact assembly.
// sqliteManagementCounter 用于在紧凑组装影响结果时保留首个聚合错误。
type sqliteManagementCounter struct {
	store *Store
	ctx   context.Context
	err   error
}

// count evaluates one count query unless an earlier aggregate has already failed.
// count 用于在此前聚合未失败时执行一条计数查询。
func (counter *sqliteManagementCounter) count(query string, params ...any) int {
	if counter.err != nil {
		return 0
	}
	value, err := counter.store.countRows(counter.ctx, query, params...)
	if err != nil {
		counter.err = fmt.Errorf("query sqlite management impact: %w", err)
		return 0
	}
	return value
}

// int64 evaluates one scalar int64 query unless an earlier aggregate has already failed.
// int64 用于在此前聚合未失败时执行一条 int64 标量查询。
func (counter *sqliteManagementCounter) int64(query string, params ...any) int64 {
	if counter.err != nil {
		return 0
	}
	rows, err := queryRows[managementInt64Row](counter.store, counter.ctx, query, params...)
	if err != nil || len(rows) == 0 {
		if err == nil {
			err = fmt.Errorf("scalar query returned no rows")
		}
		counter.err = fmt.Errorf("query sqlite management impact scalar: %w", err)
		return 0
	}
	return rows[0].Value
}

// sqliteManagementImpactRevision hashes all impact counters and the latest selected mutation timestamp.
// sqliteManagementImpactRevision 用于哈希全部影响计数与所选数据的最新变更时间。
func sqliteManagementImpactRevision(selection logicdomain.ManagementRemovalSelection, impact logicdomain.ManagementImpact, maxUpdated int64) string {
	payload, _ := json.Marshal(struct {
		Selection logicdomain.ManagementRemovalSelection
		Impact    logicdomain.ManagementImpact
		Updated   int64
	}{Selection: selection, Impact: impact, Updated: maxUpdated})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// normalizeSQLiteManagementIDs returns sorted unique positive identifiers.
// normalizeSQLiteManagementIDs 用于返回排序去重后的正标识。
func normalizeSQLiteManagementIDs(ids []uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(ids))
	result := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	slices.Sort(result)
	return result
}

// toDomain converts one preview row into a typed preview.
// toDomain 用于把一条预览行转换为强类型预览。
func (row managementPreviewRow) toDomain() (logicdomain.ManagementPreview, error) {
	var targetIDs []uint64
	var impact logicdomain.ManagementImpact
	if err := json.Unmarshal([]byte(row.TargetIDsJSON), &targetIDs); err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("decode management preview targets: %w", err)
	}
	if err := json.Unmarshal([]byte(row.ImpactJSON), &impact); err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("decode management preview impact: %w", err)
	}
	impact.Revision = row.ImpactRevision
	return logicdomain.ManagementPreview{
		Token:     row.PreviewToken,
		Selection: logicdomain.ManagementRemovalSelection{TargetType: row.TargetType, TargetIDs: targetIDs, Action: row.Action, Source: row.Source},
		Impact:    impact, ConfirmText: row.ConfirmText, CreatedAt: time.UnixMilli(row.CreatedTimestamp).UTC(),
		ExpiresAt: time.UnixMilli(row.ExpiresTimestamp).UTC(), ConsumedAt: unixMilliToTime(row.ConsumedTimestamp),
	}, nil
}

// toDomain converts one operation row into a typed operation.
// toDomain 用于把一条操作行转换为强类型操作。
func (row managementOperationRow) toDomain() (logicdomain.ManagementOperation, error) {
	var targetIDs []uint64
	if err := json.Unmarshal([]byte(row.TargetIDsJSON), &targetIDs); err != nil {
		return logicdomain.ManagementOperation{}, fmt.Errorf("decode management operation targets: %w", err)
	}
	return logicdomain.ManagementOperation{
		ID: row.OperationID, IdempotencyKey: row.IdempotencyKey, Action: row.Action, TargetType: row.TargetType,
		TargetIDs: targetIDs, Status: row.Status, BatchID: row.BatchID, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
		CreatedAt: unixMilliToTime(row.CreatedTimestamp), UpdatedAt: unixMilliToTime(row.UpdatedTimestamp),
		CompletedAt: unixMilliToTime(row.CompletedTimestamp),
	}, nil
}

// toDomain converts one recycle-batch row into a typed batch.
// toDomain 用于把一条回收批次行转换为强类型批次。
func (row managementRecycleBatchRow) toDomain() (logicdomain.ManagementRecycleBatch, error) {
	var targetIDs []uint64
	if err := json.Unmarshal([]byte(row.TargetIDsJSON), &targetIDs); err != nil {
		return logicdomain.ManagementRecycleBatch{}, fmt.Errorf("decode recycle batch targets: %w", err)
	}
	return logicdomain.ManagementRecycleBatch{
		BatchID: row.BatchID, Source: row.Source, TargetType: row.TargetType, TargetIDs: targetIDs,
		Restorable: row.Restorable != 0, State: row.State, Reason: row.Reason,
		SessionCount: row.SessionCount, TurnCount: row.TurnCount, MemoryCount: row.MemoryCount, ProfileCount: row.ProfileCount,
		RecycledAt: unixMilliToTime(row.RecycledAt), ExpiresAt: unixMilliToTime(row.ExpiresTimestamp),
		RestoredAt: unixMilliToTime(row.RestoredTimestamp), PurgedAt: unixMilliToTime(row.PurgedAt),
	}, nil
}
