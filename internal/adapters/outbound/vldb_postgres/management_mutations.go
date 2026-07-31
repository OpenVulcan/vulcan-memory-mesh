// management_mutations.go implements preview-bound human management writes for PostgreSQL.
// management_mutations.go 用于实现受预览约束的 PostgreSQL 人工管理写入。
package vldb_postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// postgresManagementTables keeps every management mutation on qualified schema-owned tables.
// postgresManagementTables 用于确保所有管理变更都作用于 schema 限定表。
type postgresManagementTables struct {
	sessionStates string
	previews      string
	operations    string
	batchMetadata string
	sessionsTrash string
	profilesTrash string
}

// managementTables resolves every management-only table through the maintenance repository.
// managementTables 用于通过维护仓储解析全部管理专用表。
func (s *Store) managementTables() postgresManagementTables {
	return postgresManagementTables{
		sessionStates: s.repos.maintenance.maintenanceQualifiedTable("vmm_management_session_states"),
		previews:      s.repos.maintenance.maintenanceQualifiedTable("vmm_management_previews"),
		operations:    s.repos.maintenance.maintenanceQualifiedTable("vmm_management_operations"),
		batchMetadata: s.repos.maintenance.maintenanceQualifiedTable("vmm_management_recycle_batches"),
		sessionsTrash: s.repos.maintenance.maintenanceQualifiedTable("vmm_sessions_trash"),
		profilesTrash: s.repos.maintenance.maintenanceQualifiedTable("vmm_profile_nodes_trash"),
	}
}

// InspectManagementSelection computes one deterministic PostgreSQL impact revision.
// InspectManagementSelection 用于计算一个确定性的 PostgreSQL 影响修订。
func (s *Store) InspectManagementSelection(ctx context.Context, selection logicdomain.ManagementRemovalSelection, hotWindowSize int) (logicdomain.ManagementImpact, error) {
	if s == nil || s.shared == nil || s.shared.pool == nil {
		return logicdomain.ManagementImpact{}, fmt.Errorf("postgres store is not initialized")
	}
	ids, err := managementPostgresIDs(selection.TargetIDs)
	if err != nil {
		return logicdomain.ManagementImpact{}, err
	}
	if len(ids) == 0 {
		return logicdomain.ManagementImpact{}, logicdomain.ValidationError{Field: "target_ids", Message: "must not be empty"}
	}
	callCtx, cancel := s.repos.maintenance.maintenanceQueryContext(ctx)
	defer cancel()
	workspace := &s.repos.maintenance
	tables := s.managementTables()
	impact := logicdomain.ManagementImpact{}
	var maxUpdated time.Time
	switch selection.TargetType {
	case logicdomain.ManagementTargetRecycleBatch:
		query := fmt.Sprintf(`
SELECT COUNT(*),
       (SELECT COUNT(*) FROM %s WHERE batch_id = ANY($1)),
       (SELECT COUNT(*) FROM %s WHERE batch_id = ANY($1)),
       (SELECT COUNT(*) FROM %s WHERE batch_id = ANY($1)),
       (SELECT COUNT(*) FROM %s WHERE batch_id = ANY($1)),
       (SELECT COUNT(*) FROM %s WHERE batch_id = ANY($1))
FROM %s WHERE id = ANY($1) AND purged_at IS NULL
`, tables.sessionsTrash, workspace.turnsTrashTable(), workspace.memoryNodesTrashTable(),
			workspace.memoryContextEdgesTrashTable(), tables.profilesTrash, workspace.recycleBatchesTable())
		var existing int
		if err := s.shared.pool.QueryRow(callCtx, query, ids).Scan(&existing, &impact.SessionCount, &impact.TurnCount, &impact.MemoryCount, &impact.ContextEdgeCount, &impact.ProfileNodeCount); err != nil {
			return logicdomain.ManagementImpact{}, fmt.Errorf("inspect postgres recycle batches: %w", err)
		}
		if existing != len(ids) {
			return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "recycle batch", Message: "one or more recycle batches do not exist"}
		}
		impact.VectorCount = impact.MemoryCount
		impact.Revision = postgresManagementImpactRevision(selection, impact, time.UnixMilli(int64(existing)))
		return impact, nil
	case logicdomain.ManagementTargetSession, logicdomain.ManagementTargetTurn:
	default:
		return logicdomain.ManagementImpact{}, logicdomain.ValidationError{Field: "target_type", Message: "is unsupported"}
	}
	targetPredicate := "tr.id = ANY($1)"
	sessionPredicate := "s.id IN (SELECT DISTINCT session_id FROM " + workspace.turnsTable() + " tr WHERE tr.id = ANY($1))"
	memoryPredicate := "mn.source_turn_id = ANY($1)"
	if selection.TargetType == logicdomain.ManagementTargetSession {
		targetPredicate = "tr.session_id = ANY($1)"
		sessionPredicate = "s.id = ANY($1)"
		memoryPredicate = "mn.origin_session_id = ANY($1) OR mn.source_turn_id IN (SELECT id FROM " + workspace.turnsTable() + " WHERE session_id = ANY($1))"
	}
	query := fmt.Sprintf(`
SELECT
  (SELECT COUNT(*) FROM %s s WHERE %s),
  (SELECT COUNT(*) FROM %s tr WHERE %s),
  (SELECT COUNT(*) FROM %s tr WHERE %s AND tr.extracted_status = %d),
  (SELECT COUNT(*) FROM %s mn WHERE %s),
  (SELECT COUNT(*) FROM %s ce WHERE ce.memory_id IN (SELECT mn.id FROM %s mn WHERE %s)),
  (SELECT COUNT(*) FROM %s pn WHERE pn.turn_id IN (SELECT tr.id FROM %s tr WHERE %s)),
  (SELECT COALESCE(MAX(tr.updated_at), to_timestamp(0)) FROM %s tr WHERE %s)
`, workspace.sessionsTable(), sessionPredicate, workspace.turnsTable(), targetPredicate,
		workspace.turnsTable(), targetPredicate, logicdomain.TurnExtractedStatusPending,
		workspace.memoryNodesTable(), memoryPredicate, workspace.memoryContextEdgesTable(), workspace.memoryNodesTable(), memoryPredicate,
		workspace.profileNodesTable(), workspace.turnsTable(), targetPredicate, workspace.turnsTable(), targetPredicate)
	if err := s.shared.pool.QueryRow(callCtx, query, ids).Scan(
		&impact.SessionCount, &impact.TurnCount, &impact.PendingTurnCount, &impact.MemoryCount,
		&impact.ContextEdgeCount, &impact.ProfileNodeCount, &maxUpdated,
	); err != nil {
		return logicdomain.ManagementImpact{}, fmt.Errorf("inspect postgres management selection: %w", err)
	}
	if selection.TargetType == logicdomain.ManagementTargetSession && impact.SessionCount != len(ids) {
		return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "session", Message: "one or more sessions do not exist"}
	}
	if selection.TargetType == logicdomain.ManagementTargetTurn && impact.TurnCount != len(ids) {
		return logicdomain.ManagementImpact{}, logicdomain.NotFoundError{Resource: "turn", Message: "one or more turns do not exist"}
	}
	if selection.TargetType == logicdomain.ManagementTargetSession {
		for _, rawID := range ids {
			status, err := s.loadPostgresSessionMemoryStatus(callCtx, uint64(rawID))
			if err != nil {
				return logicdomain.ManagementImpact{}, err
			}
			if err := status.ValidateManagementAction(selection.Action); err != nil {
				return logicdomain.ManagementImpact{}, err
			}
		}
	}
	impact.VectorCount = impact.MemoryCount
	if hotWindowSize > 0 {
		hotQuery := fmt.Sprintf(`
SELECT COUNT(*) FROM %s selected
WHERE %s
  AND (SELECT COUNT(*) FROM %s newer WHERE newer.session_id = selected.session_id AND newer.id > selected.id) < $2
`, workspace.turnsTable(), strings.ReplaceAll(targetPredicate, "tr.", "selected."), workspace.turnsTable())
		if err := s.shared.pool.QueryRow(callCtx, hotQuery, ids, hotWindowSize).Scan(&impact.HotWindowTurnCount); err != nil {
			return logicdomain.ManagementImpact{}, fmt.Errorf("inspect postgres hot-window turns: %w", err)
		}
	}
	protectedQuery := fmt.Sprintf(`
SELECT COUNT(*) FROM %s selected
WHERE %s
  AND (selected.extracted_status = $2 OR ($3 > 0 AND
       (SELECT COUNT(*) FROM %s newer WHERE newer.session_id = selected.session_id AND newer.id > selected.id) < $3))
`, workspace.turnsTable(), strings.ReplaceAll(targetPredicate, "tr.", "selected."), workspace.turnsTable())
	if err := s.shared.pool.QueryRow(callCtx, protectedQuery, ids, logicdomain.TurnExtractedStatusPending, hotWindowSize).Scan(&impact.ProtectedCount); err != nil {
		return logicdomain.ManagementImpact{}, fmt.Errorf("inspect postgres protected turns: %w", err)
	}
	impact.Revision = postgresManagementImpactRevision(selection, impact, maxUpdated)
	return impact, nil
}

// SaveManagementPreview persists one short-lived PostgreSQL preview.
// SaveManagementPreview 用于持久化一条短期 PostgreSQL 预览。
func (s *Store) SaveManagementPreview(ctx context.Context, preview logicdomain.ManagementPreview) error {
	targetJSON, _ := json.Marshal(preview.Selection.TargetIDs)
	impactJSON, _ := json.Marshal(preview.Impact)
	tables := s.managementTables()
	_, err := s.shared.pool.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (
  preview_token, target_type, target_ids_json, action, source, impact_json,
  impact_revision, confirm_text, created_at, expires_at, consumed_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL)
`, tables.previews), preview.Token, preview.Selection.TargetType, string(targetJSON), preview.Selection.Action,
		preview.Selection.Source, string(impactJSON), preview.Impact.Revision, preview.ConfirmText,
		preview.CreatedAt.UTC(), preview.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("save postgres management preview: %w", err)
	}
	return nil
}

// GetManagementPreview returns one exact PostgreSQL preview.
// GetManagementPreview 用于返回一条精确 PostgreSQL 预览。
func (s *Store) GetManagementPreview(ctx context.Context, token string) (logicdomain.ManagementPreview, error) {
	tables := s.managementTables()
	var preview logicdomain.ManagementPreview
	var targetJSON string
	var impactJSON string
	var consumedAt *time.Time
	err := s.shared.pool.QueryRow(ctx, fmt.Sprintf(`
SELECT preview_token, target_type, target_ids_json, action, source, impact_json,
       impact_revision, confirm_text, created_at, expires_at, consumed_at
FROM %s WHERE preview_token = $1
`, tables.previews), strings.TrimSpace(token)).Scan(
		&preview.Token, &preview.Selection.TargetType, &targetJSON, &preview.Selection.Action,
		&preview.Selection.Source, &impactJSON, &preview.Impact.Revision, &preview.ConfirmText,
		&preview.CreatedAt, &preview.ExpiresAt, &consumedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return logicdomain.ManagementPreview{}, logicdomain.NotFoundError{Resource: "preview", Message: "preview does not exist"}
	}
	if err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("get postgres management preview: %w", err)
	}
	if err := json.Unmarshal([]byte(targetJSON), &preview.Selection.TargetIDs); err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("decode postgres preview targets: %w", err)
	}
	if err := json.Unmarshal([]byte(impactJSON), &preview.Impact); err != nil {
		return logicdomain.ManagementPreview{}, fmt.Errorf("decode postgres preview impact: %w", err)
	}
	if consumedAt != nil {
		preview.ConsumedAt = consumedAt.UTC()
	}
	preview.CreatedAt = preview.CreatedAt.UTC()
	preview.ExpiresAt = preview.ExpiresAt.UTC()
	return preview, nil
}

// ApplyManagementRemoval executes archive, unarchive, recycle, or restart in one PostgreSQL transaction.
// ApplyManagementRemoval 用于在一个 PostgreSQL 事务中执行归档、取消归档、回收或重新开始。
func (s *Store) ApplyManagementRemoval(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	return s.withPostgresManagementMutation(ctx, command.OperationID, command.IdempotencyKey, command.Preview.Selection, command.Preview.Token, command.Now, func(tx pgx.Tx, operation *logicdomain.ManagementOperation) error {
		tables := s.managementTables()
		ids, err := managementPostgresIDs(command.Preview.Selection.TargetIDs)
		if err != nil {
			return err
		}
		if command.Preview.Selection.TargetType == logicdomain.ManagementTargetSession {
			for _, sessionID := range ids {
				var rawStatus string
				statusErr := tx.QueryRow(ctx, fmt.Sprintf(`SELECT status FROM %s WHERE session_id = $1 FOR UPDATE`, tables.sessionStates), sessionID).Scan(&rawStatus)
				status := logicdomain.SessionMemoryStatusActive
				if statusErr == nil {
					status = logicdomain.SessionMemoryStatus(strings.TrimSpace(rawStatus))
				} else if !errors.Is(statusErr, pgx.ErrNoRows) {
					return statusErr
				}
				if transitionErr := status.ValidateManagementAction(command.Preview.Selection.Action); transitionErr != nil {
					return transitionErr
				}
			}
		}
		if command.Preview.Selection.Action == logicdomain.ManagementActionArchive || command.Preview.Selection.Action == logicdomain.ManagementActionUnarchive {
			status := "archived"
			if command.Preview.Selection.Action == logicdomain.ManagementActionUnarchive {
				status = "active"
			}
			_, err := tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO %s (session_id, status, revision, updated_at)
SELECT target_id, $2, 1, $3 FROM UNNEST($1::bigint[]) AS target_id
ON CONFLICT(session_id) DO UPDATE SET status = EXCLUDED.status, revision = %s.revision + 1, updated_at = EXCLUDED.updated_at
`, tables.sessionStates, tables.sessionStates), ids, status, command.Now.UTC())
			return err
		}
		if command.Preview.Selection.Action == logicdomain.ManagementActionRestart {
			commandTag, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE %s
SET status = 'active', revision = revision + 1, updated_at = $2
WHERE session_id = ANY($1) AND status = 'forgotten'
`, tables.sessionStates), ids, command.Now.UTC())
			if err != nil {
				return err
			}
			if commandTag.RowsAffected() != int64(len(ids)) {
				return logicdomain.ConflictError{Resource: "session", Message: "only forgotten sessions can restart memory"}
			}
			return nil
		}
		batchID, err := s.recyclePostgresManagementSelection(ctx, tx, command)
		operation.BatchID = batchID
		return err
	})
}

// RestoreManagementBatch restores one manual restorable PostgreSQL batch transactionally.
// RestoreManagementBatch 用于事务化恢复一个人工可恢复 PostgreSQL 批次。
func (s *Store) RestoreManagementBatch(ctx context.Context, command logicdomain.ManagementRestoreCommand) (logicdomain.ManagementOperation, error) {
	selection := logicdomain.ManagementRemovalSelection{TargetType: logicdomain.ManagementTargetRecycleBatch, TargetIDs: []uint64{command.BatchID}, Action: logicdomain.ManagementActionRestore}
	return s.withPostgresManagementMutation(ctx, command.OperationID, command.IdempotencyKey, selection, "", command.Now, func(tx pgx.Tx, operation *logicdomain.ManagementOperation) error {
		tables := s.managementTables()
		workspace := &s.repos.maintenance
		var restorable bool
		var state string
		var expiresAt *time.Time
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT restorable, state, expires_at FROM %s WHERE batch_id = $1 FOR UPDATE`, tables.batchMetadata), int64(command.BatchID)).Scan(&restorable, &state, &expiresAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return logicdomain.NotFoundError{Resource: "recycle batch", Message: "batch does not exist"}
			}
			return err
		}
		if !restorable || state != "recycled" || (expiresAt != nil && !expiresAt.After(command.Now)) {
			return logicdomain.ConflictError{Resource: "recycle batch", Message: "batch is not restorable"}
		}
		conflictSQL := fmt.Sprintf(`
SELECT
  (SELECT COUNT(*) FROM %s active JOIN %s trash ON (trash.id = active.id OR (trash.project_id = active.project_id AND trash.session_key = active.session_key)) LEFT JOIN %s state ON state.session_id = active.id WHERE trash.batch_id = $1 AND NOT (active.id = trash.id AND active.project_id = trash.project_id AND active.session_key = trash.session_key AND state.status = 'recycled')) +
  (SELECT COUNT(*) FROM %s active JOIN %s trash ON trash.id = active.id WHERE trash.batch_id = $1) +
  (SELECT COUNT(*) FROM %s active JOIN %s trash ON trash.id = active.id OR trash.vector_id = active.vector_id WHERE trash.batch_id = $1) +
  (SELECT COUNT(*) FROM %s active JOIN %s trash ON trash.id = active.id WHERE trash.batch_id = $1)
`, workspace.sessionsTable(), tables.sessionsTrash, tables.sessionStates, workspace.turnsTable(), workspace.turnsTrashTable(),
			workspace.memoryNodesTable(), workspace.memoryNodesTrashTable(), workspace.profileNodesTable(), tables.profilesTrash)
		var conflicts int
		if err := tx.QueryRow(ctx, conflictSQL, int64(command.BatchID)).Scan(&conflicts); err != nil {
			return err
		}
		if conflicts > 0 {
			return logicdomain.ConflictError{Resource: "recycle batch", Message: "active data now conflicts with the recycled snapshot"}
		}
		statements := []string{
			fmt.Sprintf(`UPDATE %s active SET user_id = trash.user_id, team_id = trash.team_id, space_id = trash.space_id, project_id = trash.project_id, turn_count = trash.turn_count, last_summarized_id = trash.last_summarized_id, last_compacted_turn_id = trash.last_compacted_turn_id, summarize_content = trash.summarize_content, summarize_budget = trash.summarize_budget, last_extract_observed_at = trash.last_extract_observed_at, last_extract_completed_at = trash.last_extract_completed_at, last_compacted_at = trash.last_compacted_at, created_at = trash.created_at, updated_at = trash.updated_at FROM %s trash WHERE trash.batch_id = $1 AND active.id = trash.id`, workspace.sessionsTable(), tables.sessionsTrash),
			fmt.Sprintf(`INSERT INTO %s SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at FROM %s WHERE batch_id = $1`, workspace.turnsTable(), workspace.turnsTrashTable()),
			fmt.Sprintf(`INSERT INTO %s SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, embedding, source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason, expires_at, last_recalled_at, last_adopted_at, last_reinforced_at, recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash, created_at, updated_at FROM %s WHERE batch_id = $1`, workspace.memoryNodesTable(), workspace.memoryNodesTrashTable()),
			fmt.Sprintf(`INSERT INTO %s SELECT memory_id, context_key, context_value, support_count, rebuttal_count, last_supported_at, last_rebutted_at, created_at, updated_at FROM %s WHERE batch_id = $1`, workspace.memoryContextEdgesTable(), workspace.memoryContextEdgesTrashTable()),
			fmt.Sprintf(`INSERT INTO %s SELECT id, turn_id, profile_type, bind_id, content, profile_status, priority, profile_level, level_reason, refresh_weight, source_kind, source_id, status_reason, expires_at, superseded_by_id, profile_date, created_at, updated_at FROM %s WHERE batch_id = $1`, workspace.profileNodesTable(), tables.profilesTrash),
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement, int64(command.BatchID)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE %s SET status = 'active', revision = revision + 1, updated_at = $2
WHERE session_id IN (SELECT id FROM %s WHERE batch_id = $1)
`, tables.sessionStates, tables.sessionsTrash), int64(command.BatchID), command.Now.UTC()); err != nil {
			return err
		}
		for _, table := range []string{workspace.memoryContextEdgesTrashTable(), workspace.memoryNodesTrashTable(), tables.profilesTrash, workspace.turnsTrashTable(), tables.sessionsTrash} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE batch_id = $1", int64(command.BatchID)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET state = 'restored', restored_at = $2 WHERE batch_id = $1`, tables.batchMetadata), int64(command.BatchID), command.Now.UTC())
		operation.BatchID = command.BatchID
		return err
	})
}

// PurgeManagementBatch permanently removes preview-bound PostgreSQL recycle batches.
// PurgeManagementBatch 用于永久移除受预览约束的 PostgreSQL 回收批次。
func (s *Store) PurgeManagementBatch(ctx context.Context, command logicdomain.ManagementMutationCommand) (logicdomain.ManagementOperation, error) {
	return s.withPostgresManagementMutation(ctx, command.OperationID, command.IdempotencyKey, command.Preview.Selection, command.Preview.Token, command.Now, func(tx pgx.Tx, _ *logicdomain.ManagementOperation) error {
		tables := s.managementTables()
		workspace := &s.repos.maintenance
		ids, err := managementPostgresIDs(command.Preview.Selection.TargetIDs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE %s SET status = 'forgotten', revision = revision + 1, updated_at = $2
WHERE session_id IN (SELECT id FROM %s WHERE batch_id = ANY($1))
`, tables.sessionStates, tables.sessionsTrash), ids, command.Now.UTC()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE %s
SET last_summarized_id = 0, last_compacted_turn_id = 0, summarize_content = '', summarize_budget = 0,
    last_extract_observed_at = NULL, last_extract_completed_at = NULL, last_compacted_at = NULL, updated_at = $2
WHERE id IN (SELECT id FROM %s WHERE batch_id = ANY($1))
`, workspace.sessionsTable(), tables.sessionsTrash), ids, command.Now.UTC()); err != nil {
			return err
		}
		for _, table := range []string{workspace.memoryContextEdgesTrashTable(), workspace.memoryNodesTrashTable(), tables.profilesTrash, workspace.turnsTrashTable(), tables.sessionsTrash, tables.batchMetadata} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE batch_id = ANY($1)", ids); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "DELETE FROM "+workspace.recycleBatchesTable()+" WHERE id = ANY($1)", ids)
		return err
	})
}

// GetManagementOperation returns one persisted PostgreSQL operation.
// GetManagementOperation 用于返回一条持久化 PostgreSQL 操作。
func (s *Store) GetManagementOperation(ctx context.Context, operationID string) (logicdomain.ManagementOperation, error) {
	tables := s.managementTables()
	return scanPostgresManagementOperation(s.shared.pool.QueryRow(ctx, fmt.Sprintf(`
SELECT operation_id, idempotency_key, action, target_type, target_ids_json, status,
       batch_id, error_code, error_message, created_at, updated_at, completed_at
FROM %s WHERE operation_id = $1
`, tables.operations), strings.TrimSpace(operationID)))
}

// GetManagementOperationByIdempotencyKey returns one PostgreSQL operation by its caller-stable key.
// GetManagementOperationByIdempotencyKey 用于按调用方稳定幂等键返回一条 PostgreSQL 操作。
func (s *Store) GetManagementOperationByIdempotencyKey(ctx context.Context, idempotencyKey string) (logicdomain.ManagementOperation, error) {
	return s.postgresManagementOperationByIdempotency(ctx, idempotencyKey)
}

// ListManagementRecycleBatches returns one bounded PostgreSQL recycle-bin page.
// ListManagementRecycleBatches 用于返回一页有界 PostgreSQL 回收站。
func (s *Store) ListManagementRecycleBatches(ctx context.Context, cursorID uint64, limit int) (logicdomain.ManagementRecycleBatchPage, error) {
	rows, err := s.shared.pool.Query(ctx, s.postgresManagementRecycleBatchSQL()+" WHERE ($1 = 0 OR b.id < $1) ORDER BY b.id DESC LIMIT $2", int64(cursorID), limit+1)
	if err != nil {
		return logicdomain.ManagementRecycleBatchPage{}, fmt.Errorf("list postgres management recycle batches: %w", err)
	}
	defer rows.Close()
	items := make([]logicdomain.ManagementRecycleBatch, 0, limit+1)
	for rows.Next() {
		batch, scanErr := scanPostgresManagementRecycleBatch(rows)
		if scanErr != nil {
			return logicdomain.ManagementRecycleBatchPage{}, scanErr
		}
		items = append(items, batch)
	}
	if err := rows.Err(); err != nil {
		return logicdomain.ManagementRecycleBatchPage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	page := logicdomain.ManagementRecycleBatchPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextCursorID = items[len(items)-1].BatchID
	}
	return page, nil
}

// GetManagementRecycleBatch returns one PostgreSQL recycle batch.
// GetManagementRecycleBatch 用于返回一个 PostgreSQL 回收批次。
func (s *Store) GetManagementRecycleBatch(ctx context.Context, batchID uint64) (logicdomain.ManagementRecycleBatch, error) {
	return scanPostgresManagementRecycleBatch(s.shared.pool.QueryRow(ctx, s.postgresManagementRecycleBatchSQL()+" WHERE b.id = $1", int64(batchID)))
}

// postgresManagementImpactRevision hashes one target selection, its counts, and the latest selected update.
// postgresManagementImpactRevision 用于哈希目标集合、影响计数及最新选中更新时间。
func postgresManagementImpactRevision(selection logicdomain.ManagementRemovalSelection, impact logicdomain.ManagementImpact, updated time.Time) string {
	payload, _ := json.Marshal(struct {
		Selection logicdomain.ManagementRemovalSelection
		Impact    logicdomain.ManagementImpact
		Updated   int64
	}{Selection: selection, Impact: impact, Updated: updated.UTC().UnixNano()})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// managementPostgresIDs converts unsigned domain identifiers into checked PostgreSQL bigint values.
// managementPostgresIDs 用于把无符号领域标识转换为已检查的 PostgreSQL bigint 值。
func managementPostgresIDs(ids []uint64) ([]int64, error) {
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || id > math.MaxInt64 {
			return nil, logicdomain.ValidationError{Field: "target_ids", Message: "must contain positive PostgreSQL bigint identifiers"}
		}
		result = append(result, int64(id))
	}
	return result, nil
}
