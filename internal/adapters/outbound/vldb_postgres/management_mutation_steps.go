// management_mutation_steps.go contains transactional PostgreSQL management mutation stages.
// management_mutation_steps.go 用于保存事务化 PostgreSQL 管理变更阶段。
package vldb_postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// withPostgresManagementMutation applies one idempotent management mutation and records its terminal state.
// withPostgresManagementMutation 用于执行一次幂等管理变更并记录其终态。
func (s *Store) withPostgresManagementMutation(
	ctx context.Context,
	operationID string,
	idempotencyKey string,
	selection logicdomain.ManagementRemovalSelection,
	previewToken string,
	now time.Time,
	apply func(pgx.Tx, *logicdomain.ManagementOperation) error,
) (logicdomain.ManagementOperation, error) {
	if existing, err := s.postgresManagementOperationByIdempotency(ctx, idempotencyKey); err == nil {
		if !selection.MatchesOperation(existing) {
			return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
		}
		return existing, nil
	} else if !logicdomain.IsNotFoundError(err) {
		return logicdomain.ManagementOperation{}, err
	}
	callCtx, cancel := s.repos.maintenance.maintenanceWriteContext(ctx)
	defer cancel()
	tx, err := s.shared.pool.BeginTx(callCtx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return logicdomain.ManagementOperation{}, fmt.Errorf("begin postgres management mutation: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback(callCtx)
		}
	}()
	operation := logicdomain.ManagementOperation{
		ID: operationID, IdempotencyKey: idempotencyKey, Action: selection.Action,
		TargetType: selection.TargetType, TargetIDs: selection.TargetIDs, Status: "running",
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	targetJSON, _ := json.Marshal(selection.TargetIDs)
	tables := s.managementTables()
	_, err = tx.Exec(callCtx, fmt.Sprintf(`
INSERT INTO %s (
  operation_id, idempotency_key, action, target_type, target_ids_json, status,
  batch_id, error_code, error_message, created_at, updated_at, completed_at
) VALUES ($1,$2,$3,$4,$5,'running',0,'','',$6,$6,NULL)
`, tables.operations), operation.ID, operation.IdempotencyKey, operation.Action, operation.TargetType, string(targetJSON), now.UTC())
	if err != nil {
		_ = tx.Rollback(callCtx)
		finished = true
		if existing, readErr := s.postgresManagementOperationByIdempotency(ctx, idempotencyKey); readErr == nil {
			if !selection.MatchesOperation(existing) {
				return logicdomain.ManagementOperation{}, logicdomain.ConflictError{Resource: "idempotency key", Message: "key was already used for a different management selection"}
			}
			return existing, nil
		}
		return logicdomain.ManagementOperation{}, fmt.Errorf("insert postgres management operation: %w", err)
	}
	if err := apply(tx, &operation); err != nil {
		_ = tx.Rollback(callCtx)
		finished = true
		s.recordPostgresManagementFailure(ctx, operation, "mutation_failed", err.Error(), now)
		return operation, err
	}
	if strings.TrimSpace(previewToken) != "" {
		result, consumeErr := tx.Exec(callCtx, fmt.Sprintf(`
UPDATE %s SET consumed_at = $2
WHERE preview_token = $1 AND consumed_at IS NULL
`, tables.previews), strings.TrimSpace(previewToken), now.UTC())
		if consumeErr != nil {
			_ = tx.Rollback(callCtx)
			finished = true
			s.recordPostgresManagementFailure(ctx, operation, "preview_consume_failed", consumeErr.Error(), now)
			return operation, fmt.Errorf("consume postgres management preview: %w", consumeErr)
		}
		if result.RowsAffected() != 1 {
			_ = tx.Rollback(callCtx)
			finished = true
			consumeErr = logicdomain.ConflictError{Resource: "preview", Message: "preview was already consumed"}
			s.recordPostgresManagementFailure(ctx, operation, "preview_already_consumed", consumeErr.Error(), now)
			return operation, consumeErr
		}
	}
	_, err = tx.Exec(callCtx, fmt.Sprintf(`
UPDATE %s SET status = 'succeeded', batch_id = $2, updated_at = $3, completed_at = $3
WHERE operation_id = $1 AND status = 'running'
`, tables.operations), operation.ID, int64(operation.BatchID), now.UTC())
	if err != nil {
		return operation, fmt.Errorf("complete postgres management operation: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		finished = true
		return operation, logicdomain.OutcomeUncertainError{Operation: selection.Action, Message: err.Error()}
	}
	finished = true
	operation.Status = "succeeded"
	operation.UpdatedAt = now.UTC()
	operation.CompletedAt = now.UTC()
	return operation, nil
}

// recordPostgresManagementFailure persists a terminal failure after the mutation transaction has rolled back.
// recordPostgresManagementFailure 在变更事务回滚后持久化终态失败记录。
func (s *Store) recordPostgresManagementFailure(ctx context.Context, operation logicdomain.ManagementOperation, code string, message string, now time.Time) {
	targetJSON, _ := json.Marshal(operation.TargetIDs)
	tables := s.managementTables()
	callCtx, cancel := s.repos.maintenance.maintenanceWriteContext(ctx)
	defer cancel()
	_, _ = s.shared.pool.Exec(callCtx, fmt.Sprintf(`
INSERT INTO %s (
  operation_id, idempotency_key, action, target_type, target_ids_json, status,
  batch_id, error_code, error_message, created_at, updated_at, completed_at
) VALUES ($1,$2,$3,$4,$5,'failed',0,$6,$7,$8,$8,$8)
ON CONFLICT(idempotency_key) DO NOTHING
`, tables.operations), operation.ID, operation.IdempotencyKey, operation.Action, operation.TargetType,
		string(targetJSON), strings.TrimSpace(code), strings.TrimSpace(message), now.UTC())
}

// postgresManagementOperationByIdempotency returns one prior operation or a typed missing error.
// postgresManagementOperationByIdempotency 用于返回一条既有操作或强类型缺失错误。
func (s *Store) postgresManagementOperationByIdempotency(ctx context.Context, key string) (logicdomain.ManagementOperation, error) {
	tables := s.managementTables()
	return scanPostgresManagementOperation(s.shared.pool.QueryRow(ctx, fmt.Sprintf(`
SELECT operation_id, idempotency_key, action, target_type, target_ids_json, status,
       batch_id, error_code, error_message, created_at, updated_at, completed_at
FROM %s WHERE idempotency_key = $1
`, tables.operations), strings.TrimSpace(key)))
}

// scanPostgresManagementOperation scans one operation from either QueryRow or Rows.
// scanPostgresManagementOperation 用于从 QueryRow 或 Rows 扫描一条操作。
func scanPostgresManagementOperation(scanner interface{ Scan(...any) error }) (logicdomain.ManagementOperation, error) {
	var operation logicdomain.ManagementOperation
	var targetJSON string
	var batchID int64
	var completedAt *time.Time
	err := scanner.Scan(
		&operation.ID, &operation.IdempotencyKey, &operation.Action, &operation.TargetType,
		&targetJSON, &operation.Status, &batchID, &operation.ErrorCode, &operation.ErrorMessage,
		&operation.CreatedAt, &operation.UpdatedAt, &completedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.ManagementOperation{}, logicdomain.NotFoundError{Resource: "operation", Message: "operation does not exist"}
	}
	if err != nil {
		return logicdomain.ManagementOperation{}, fmt.Errorf("scan postgres management operation: %w", err)
	}
	if err := json.Unmarshal([]byte(targetJSON), &operation.TargetIDs); err != nil {
		return logicdomain.ManagementOperation{}, fmt.Errorf("decode postgres management operation targets: %w", err)
	}
	operation.BatchID = uint64(batchID)
	operation.CreatedAt = operation.CreatedAt.UTC()
	operation.UpdatedAt = operation.UpdatedAt.UTC()
	if completedAt != nil {
		operation.CompletedAt = completedAt.UTC()
	}
	return operation, nil
}

// recyclePostgresManagementSelection moves selected and directly derived rows into one manual batch.
// recyclePostgresManagementSelection 用于把所选数据及其直接派生行移入一个人工批次。
func (s *Store) recyclePostgresManagementSelection(ctx context.Context, tx pgx.Tx, command logicdomain.ManagementMutationCommand) (uint64, error) {
	workspace := &s.repos.maintenance
	tables := s.managementTables()
	selection := command.Preview.Selection
	ids, err := managementPostgresIDs(selection.TargetIDs)
	if err != nil {
		return 0, err
	}
	turnPredicate := "id = ANY($1)"
	sessionPredicate := ""
	memoryPredicate := "source_turn_id = ANY($1)"
	if selection.TargetType == logicdomain.ManagementTargetSession {
		turnPredicate = "session_id = ANY($1)"
		sessionPredicate = "id = ANY($1)"
		memoryPredicate = "origin_session_id = ANY($1) OR source_turn_id IN (SELECT id FROM " + workspace.turnsTable() + " WHERE session_id = ANY($1))"
	}
	var batchID int64
	err = tx.QueryRow(ctx, fmt.Sprintf(`
INSERT INTO %s (recycle_type, session_id, project_id, reason, recycled_at, purged_at, created_at, updated_at)
VALUES ('manual_management',0,0,'manual management recycle',$1,NULL,$1,$1)
RETURNING id
`, workspace.recycleBatchesTable()), command.Now.UTC()).Scan(&batchID)
	if err != nil {
		return 0, fmt.Errorf("insert postgres management recycle batch: %w", err)
	}
	targetJSON, _ := json.Marshal(selection.TargetIDs)
	_, err = tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (batch_id, source, target_type, target_ids_json, restorable, state, expires_at, restored_at, operation_id)
VALUES ($1,$2,$3,$4,TRUE,'recycled',$5,NULL,$6)
`, tables.batchMetadata), batchID, selection.Source, selection.TargetType, string(targetJSON), command.ExpiresAt.UTC(), command.OperationID)
	if err != nil {
		return 0, fmt.Errorf("insert postgres management recycle metadata: %w", err)
	}
	if sessionPredicate != "" {
		_, err = tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason, id, session_key, user_id, team_id, space_id, project_id,
  turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
  last_extract_observed_at, last_extract_completed_at, last_compacted_at, created_at, updated_at
)
SELECT $2,$3,'manual management recycle', id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_at, last_extract_completed_at, last_compacted_at, created_at, updated_at
FROM %s WHERE %s
`, tables.sessionsTrash, workspace.sessionsTable(), sessionPredicate), ids, batchID, command.Now.UTC())
		if err != nil {
			return 0, fmt.Errorf("copy postgres sessions to management trash: %w", err)
		}
	}
	statements := []string{
		fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason, memory_id, context_key, context_value,
  support_count, rebuttal_count, last_supported_at, last_rebutted_at, created_at, updated_at
)
SELECT $2,$3,'manual management recycle', memory_id, context_key, context_value,
       support_count, rebuttal_count, last_supported_at, last_rebutted_at, created_at, updated_at
FROM %s WHERE memory_id IN (SELECT id FROM %s WHERE %s)
`, workspace.memoryContextEdgesTrashTable(), workspace.memoryContextEdgesTable(), workspace.memoryNodesTable(), memoryPredicate),
		fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason, id, team_id, space_id, project_id, user_id,
  origin_session_id, source_turn_id, vector_id, embedding, source_kind, scope_level,
  category, abstract, details, memory_status, priority, memory_level, refresh_weight,
  support_count, rebuttal_count, status_reason, expires_at, last_recalled_at,
  last_adopted_at, last_reinforced_at, recalled_count, adopted_count,
  reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_at, updated_at
)
SELECT $2,$3,'manual management recycle', id, team_id, space_id, project_id, user_id,
       origin_session_id, source_turn_id, vector_id, embedding, source_kind, scope_level,
       category, abstract, details, memory_status, priority, memory_level, refresh_weight,
       support_count, rebuttal_count, status_reason, expires_at, last_recalled_at,
       last_adopted_at, last_reinforced_at, recalled_count, adopted_count,
       reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
       created_at, updated_at
FROM %s WHERE %s
`, workspace.memoryNodesTrashTable(), workspace.memoryNodesTable(), memoryPredicate),
		fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason, id, turn_id, profile_type, bind_id, content,
  profile_status, priority, profile_level, level_reason, refresh_weight, source_kind,
  source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at
)
SELECT $2,$3,'manual management recycle', id, turn_id, profile_type, bind_id, content,
       profile_status, priority, profile_level, level_reason, refresh_weight, source_kind,
       source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at
FROM %s WHERE turn_id IN (SELECT id FROM %s WHERE %s)
`, tables.profilesTrash, workspace.profileNodesTable(), workspace.turnsTable(), turnPredicate),
		fmt.Sprintf(`
INSERT INTO %s (
  batch_id, recycled_at, recycle_reason, id, session_id, project_id, dehydrated_content,
  dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
)
SELECT $2,$3,'manual management recycle', id, session_id, project_id, dehydrated_content,
       dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
FROM %s WHERE %s
`, workspace.turnsTrashTable(), workspace.turnsTable(), turnPredicate),
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, ids, batchID, command.Now.UTC()); err != nil {
			return 0, fmt.Errorf("copy postgres management trash rows: %w", err)
		}
	}
	deletes := []string{
		fmt.Sprintf(`DELETE FROM %s WHERE memory_id IN (SELECT id FROM %s WHERE %s)`, workspace.memoryContextEdgesTable(), workspace.memoryNodesTable(), memoryPredicate),
		fmt.Sprintf(`DELETE FROM %s WHERE %s`, workspace.memoryNodesTable(), memoryPredicate),
		fmt.Sprintf(`DELETE FROM %s WHERE turn_id IN (SELECT id FROM %s WHERE %s)`, workspace.profileNodesTable(), workspace.turnsTable(), turnPredicate),
		fmt.Sprintf(`DELETE FROM %s WHERE %s`, workspace.turnsTable(), turnPredicate),
	}
	for _, statement := range deletes {
		if _, err := tx.Exec(ctx, statement, ids); err != nil {
			return 0, fmt.Errorf("delete postgres recycled active rows: %w", err)
		}
	}
	if sessionPredicate != "" {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (session_id, status, revision, updated_at)
SELECT id, 'recycled', 1, $2 FROM %s WHERE %s
ON CONFLICT(session_id) DO UPDATE SET status = 'recycled', revision = %s.revision + 1, updated_at = EXCLUDED.updated_at
`, tables.sessionStates, workspace.sessionsTable(), sessionPredicate, tables.sessionStates), ids, command.Now.UTC()); err != nil {
			return 0, fmt.Errorf("mark postgres recycled session states: %w", err)
		}
		if _, err := tx.Exec(ctx, "UPDATE "+workspace.sessionsTable()+" SET last_summarized_id = 0, last_compacted_turn_id = 0, summarize_content = '', summarize_budget = 0, last_extract_observed_at = NULL, last_extract_completed_at = NULL, last_compacted_at = NULL, updated_at = $2 WHERE "+sessionPredicate, ids, command.Now.UTC()); err != nil {
			return 0, fmt.Errorf("scrub postgres recycled session tombstones: %w", err)
		}
	}
	return uint64(batchID), nil
}

// postgresManagementRecycleBatchSQL returns the shared list/detail projection.
// postgresManagementRecycleBatchSQL 用于返回回收批次列表与详情共用投影。
func (s *Store) postgresManagementRecycleBatchSQL() string {
	workspace := &s.repos.maintenance
	tables := s.managementTables()
	return fmt.Sprintf(`
SELECT b.id, COALESCE(m.source,'system'), COALESCE(m.target_type,''), COALESCE(m.target_ids_json,'[]'),
       COALESCE(m.restorable,FALSE), COALESCE(m.state, CASE WHEN b.purged_at IS NOT NULL THEN 'purged' ELSE 'system' END),
       b.reason, b.recycled_at, m.expires_at, m.restored_at, b.purged_at,
       (SELECT COUNT(*) FROM %s st WHERE st.batch_id = b.id),
       (SELECT COUNT(*) FROM %s tt WHERE tt.batch_id = b.id),
       (SELECT COUNT(*) FROM %s mt WHERE mt.batch_id = b.id),
       (SELECT COUNT(*) FROM %s pt WHERE pt.batch_id = b.id)
FROM %s b LEFT JOIN %s m ON m.batch_id = b.id
`, tables.sessionsTrash, workspace.turnsTrashTable(), workspace.memoryNodesTrashTable(), tables.profilesTrash,
		workspace.recycleBatchesTable(), tables.batchMetadata)
}

// scanPostgresManagementRecycleBatch scans one recycle-batch projection.
// scanPostgresManagementRecycleBatch 用于扫描一条回收批次投影。
func scanPostgresManagementRecycleBatch(scanner interface{ Scan(...any) error }) (logicdomain.ManagementRecycleBatch, error) {
	var batch logicdomain.ManagementRecycleBatch
	var targetJSON string
	var batchID int64
	var expiresAt *time.Time
	var restoredAt *time.Time
	var purgedAt *time.Time
	err := scanner.Scan(
		&batchID, &batch.Source, &batch.TargetType, &targetJSON, &batch.Restorable, &batch.State,
		&batch.Reason, &batch.RecycledAt, &expiresAt, &restoredAt, &purgedAt,
		&batch.SessionCount, &batch.TurnCount, &batch.MemoryCount, &batch.ProfileCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.ManagementRecycleBatch{}, logicdomain.NotFoundError{Resource: "recycle batch", Message: "batch does not exist"}
	}
	if err != nil {
		return logicdomain.ManagementRecycleBatch{}, fmt.Errorf("scan postgres management recycle batch: %w", err)
	}
	if err := json.Unmarshal([]byte(targetJSON), &batch.TargetIDs); err != nil {
		return logicdomain.ManagementRecycleBatch{}, fmt.Errorf("decode postgres recycle batch targets: %w", err)
	}
	batch.BatchID = uint64(batchID)
	batch.RecycledAt = batch.RecycledAt.UTC()
	if expiresAt != nil {
		batch.ExpiresAt = expiresAt.UTC()
	}
	if restoredAt != nil {
		batch.RestoredAt = restoredAt.UTC()
	}
	if purgedAt != nil {
		batch.PurgedAt = purgedAt.UTC()
	}
	return batch, nil
}
