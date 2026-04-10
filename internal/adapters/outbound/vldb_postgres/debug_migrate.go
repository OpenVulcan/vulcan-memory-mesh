// debug_migrate.go imports the historical split SQLite snapshot into the unified PostgreSQL combined store.
// debug_migrate.go 用于把历史 split 模式的 SQLite 快照导入统一的 PostgreSQL 组合库。
package vldb_postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
)

// DebugImportManagedSnapshot bootstraps the PostgreSQL combined store, transactionally replaces all managed rows, and restores identity sequences afterward.
// DebugImportManagedSnapshot 用于初始化 PostgreSQL 组合库、以事务方式替换全部受管数据，并在结束后恢复 identity 序列。
func DebugImportManagedSnapshot(ctx context.Context, cfg Config, snapshot storagemigrate.Snapshot) (storagemigrate.Report, error) {
	store, err := NewStore(cfg)
	if err != nil {
		return storagemigrate.Report{}, err
	}
	defer func() {
		_ = store.Shutdown(context.Background())
	}()
	return store.importManagedSnapshot(ctx, snapshot)
}

// importManagedSnapshot truncates the current managed PostgreSQL data and replays the SQLite fact snapshot inside one transaction so failures leave the old target intact.
// importManagedSnapshot 用于清空当前 PostgreSQL 受管数据，并在单个事务中回放 SQLite 事实快照，保证失败时目标库保持旧状态不变。
func (r *maintenanceRepository) importManagedSnapshot(ctx context.Context, snapshot storagemigrate.Snapshot) (storagemigrate.Report, error) {
	if r == nil || r.shared == nil || r.shared.pool == nil {
		return storagemigrate.Report{}, fmt.Errorf("postgres store is not initialized")
	}
	callCtx, cancel := r.maintenanceWriteContext(ctx)
	defer cancel()

	tx, err := r.shared.pool.Begin(callCtx)
	if err != nil {
		return storagemigrate.Report{}, fmt.Errorf("begin postgres debug migration tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(callCtx)
	}()

	// Replace the full managed dataset atomically so operators can re-run the migration without leaving the target in a half-imported state.
	// 以原子方式替换整套受管数据，确保运维人员可重复执行迁移且不会把目标库留在半导入状态。
	if err := r.truncateManagedTables(callCtx, tx); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importNoiseEmbeddings(callCtx, tx, snapshot.NoiseEmbeddings); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importUsers(callCtx, tx, snapshot.Users); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importTeams(callCtx, tx, snapshot.Teams); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importSpaces(callCtx, tx, snapshot.Spaces); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importProjects(callCtx, tx, snapshot.Projects); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importSessions(callCtx, tx, snapshot.Sessions); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importTurns(callCtx, tx, snapshot.Turns); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importMemoryNodes(callCtx, tx, snapshot.MemoryNodes); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importMemoryContextEdges(callCtx, tx, snapshot.MemoryContextEdges); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importProfileNodes(callCtx, tx, snapshot.ProfileNodes); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.importProfileInstructions(callCtx, tx, snapshot.ProfileInstructions); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := tx.Commit(callCtx); err != nil {
		return storagemigrate.Report{}, fmt.Errorf("commit postgres debug migration tx: %w", err)
	}
	if err := r.syncDebugSeedSequences(callCtx); err != nil {
		return storagemigrate.Report{}, err
	}
	if err := r.persistManagedSnapshotVersions(callCtx); err != nil {
		return storagemigrate.Report{}, err
	}
	return snapshot.BuildReport(), nil
}

// truncateManagedTables clears every managed PostgreSQL table inside one transaction so the import can replay explicit ids from SQLite without conflict drift.
// truncateManagedTables 用于在单个事务中清空全部 PostgreSQL 受管表，让后续导入可以无冲突地回放 SQLite 的显式 id。
func (r *maintenanceRepository) truncateManagedTables(ctx context.Context, tx pgx.Tx) error {
	statement := fmt.Sprintf(`
TRUNCATE TABLE
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s,
	%s
RESTART IDENTITY CASCADE
`, r.profileInstructionsTable(), r.profileNodesTable(), r.memoryContextEdgesTable(), r.memoryNodesTable(), r.turnsTable(), r.sessionsTable(), r.projectsTable(), r.spacesTable(), r.teamsTable(), r.usersTable(), r.noiseEmbeddingsTable(), r.maintenanceQualifiedTable("vmm_schema_versions"))
	if _, err := tx.Exec(ctx, strings.TrimSpace(statement)); err != nil {
		return fmt.Errorf("truncate postgres managed tables before debug migration: %w", err)
	}
	return nil
}

// persistManagedSnapshotVersions restores the shared schema-version rows after the migration cleared the managed schema table.
// persistManagedSnapshotVersions 用于在迁移清空组件版本表后恢复共享 schema 版本记录。
func (r *maintenanceRepository) persistManagedSnapshotVersions(ctx context.Context) error {
	return r.writeTrackedSchemaVersions(ctx)
}

// importNoiseEmbeddings replays the semantic prototype cache first because these rows are independent and benefit from early vector-dimension validation.
// importNoiseEmbeddings 用于优先回放语义原型缓存，因为这类行彼此独立，适合尽早执行向量维度校验。
func (r *maintenanceRepository) importNoiseEmbeddings(ctx context.Context, tx pgx.Tx, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (scope, language, category_name, phrase, model, dimension, rules_hash, embedding, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector, $9)
`, r.noiseEmbeddingsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(entries), func(batch *pgx.Batch, start, end int) error {
		for _, entry := range entries[start:end] {
			if err := validateMigrationVectorDimension("noise embedding", entry.Phrase, entry.Vector, r.shared.cfg.EmbeddingDimension); err != nil {
				return err
			}
			batch.Queue(statement,
				strings.TrimSpace(entry.Scope),
				strings.TrimSpace(entry.Language),
				strings.TrimSpace(entry.CategoryName),
				strings.TrimSpace(entry.Phrase),
				strings.TrimSpace(entry.Model),
				entry.Dimension,
				strings.TrimSpace(entry.RulesHash),
				encodePGVectorLiteral(entry.Vector),
				entry.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importUsers restores durable users with their historical ids so every downstream foreign key from sessions and memory rows remains stable.
// importUsers 用于按历史 id 恢复长期用户，确保 session 和记忆表的下游外键保持稳定。
func (r *maintenanceRepository) importUsers(ctx context.Context, tx pgx.Tx, users []logicdomain.UserRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (id, name, profile, delete_confirm_code, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
`, r.usersTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(users), func(batch *pgx.Batch, start, end int) error {
		for _, row := range users[start:end] {
			batch.Queue(statement, int64(row.ID), strings.TrimSpace(row.Name), row.Profile, row.DeleteConfirmCode, row.CreatedAt.UTC(), row.UpdatedAt.UTC())
		}
		return nil
	})
}

// importTeams restores durable teams before spaces and projects so the hierarchy path can preserve historical numeric ids.
// importTeams 用于在导入 space 与 project 前先恢复 team，保证层级路径保留历史数字 id。
func (r *maintenanceRepository) importTeams(ctx context.Context, tx pgx.Tx, teams []logicdomain.TeamRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (id, name, profile, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
`, r.teamsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(teams), func(batch *pgx.Batch, start, end int) error {
		for _, row := range teams[start:end] {
			batch.Queue(statement, int64(row.ID), strings.TrimSpace(row.Name), row.Profile, row.CreatedAt.UTC(), row.UpdatedAt.UTC())
		}
		return nil
	})
}

// importSpaces restores team-scoped spaces after teams so the project path hierarchy can be rebuilt losslessly.
// importSpaces 用于在 team 导入后恢复 team 作用域下的 space，确保项目层级可无损重建。
func (r *maintenanceRepository) importSpaces(ctx context.Context, tx pgx.Tx, spaces []logicdomain.SpaceRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (id, team_id, name, profile, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
`, r.spacesTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(spaces), func(batch *pgx.Batch, start, end int) error {
		for _, row := range spaces[start:end] {
			batch.Queue(statement, int64(row.ID), int64(row.TeamID), strings.TrimSpace(row.Name), row.Profile, row.CreatedAt.UTC(), row.UpdatedAt.UTC())
		}
		return nil
	})
}

// importProjects restores canonical project rows with their original ids so session scopes and profile targets continue to resolve identically after cutover.
// importProjects 用于以原始 id 恢复规范 project 行，保证切换后 session 范围和画像目标解析结果保持一致。
func (r *maintenanceRepository) importProjects(ctx context.Context, tx pgx.Tx, projects []logicdomain.ProjectRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (id, team_id, space_id, name, profile, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`, r.projectsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(projects), func(batch *pgx.Batch, start, end int) error {
		for _, row := range projects[start:end] {
			batch.Queue(statement, int64(row.ID), int64(row.TeamID), int64(row.SpaceID), strings.TrimSpace(row.Name), row.Profile, row.CreatedAt.UTC(), row.UpdatedAt.UTC())
		}
		return nil
	})
}

// importSessions restores session rows before turn replay so later turn imports can keep their original session_id linkage.
// importSessions 用于在回放 turn 前先恢复 session 行，让后续 turn 导入保留原始 session_id 关联。
func (r *maintenanceRepository) importSessions(ctx context.Context, tx pgx.Tx, sessions []logicdomain.SessionRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	id, session_key, user_id, team_id, space_id, project_id,
	turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
	last_extract_observed_at, last_extract_completed_at, last_compacted_at, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6,
	$7, $8, $9, $10, $11,
	$12, $13, $14, $15, $16
)
`, r.sessionsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(sessions), func(batch *pgx.Batch, start, end int) error {
		for _, row := range sessions[start:end] {
			batch.Queue(statement,
				int64(row.ID),
				strings.TrimSpace(row.SessionKey),
				int64(row.UserID),
				int64(row.TeamID),
				int64(row.SpaceID),
				int64(row.ProjectID),
				row.TurnCount,
				int64(row.LastSummarizedID),
				int64(row.LastCompactedTurnID),
				row.SummarizeContent,
				row.SummarizeBudget,
				nullableTime(row.LastExtractObservedAt),
				nullableTime(row.LastExtractCompletedAt),
				nullableTime(row.LastCompactedAt),
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importTurns restores dehydrated turn rows so downstream memory/profile references continue pointing at the same historical source turns.
// importTurns 用于恢复脱水后的 turn 行，确保后续记忆和画像引用继续指向相同的历史源 turn。
func (r *maintenanceRepository) importTurns(ctx context.Context, tx pgx.Tx, turns []logicdomain.SessionTurnRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
`, r.turnsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(turns), func(batch *pgx.Batch, start, end int) error {
		for _, row := range turns[start:end] {
			batch.Queue(statement,
				int64(row.ID),
				int64(row.SessionID),
				int64(row.ProjectID),
				row.DehydratedContent,
				row.DehydratedBudget,
				row.ExtractedStatus,
				row.Details,
				row.DetailsBudget,
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importMemoryNodes parses already-normalized Go vectors directly into pgvector writes and never reintroduces the deprecated vector_json payload.
// importMemoryNodes 用于把已在 Go 内存中标准化的向量直接写入 pgvector 列，绝不重新引入已废弃的 vector_json 载荷。
func (r *maintenanceRepository) importMemoryNodes(ctx context.Context, tx pgx.Tx, rows []logicdomain.MemoryNodeRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id, vector_id, embedding,
	source_kind, scope_level, category, abstract, details, memory_status, priority, memory_level, refresh_weight,
	support_count, rebuttal_count, status_reason, expires_at, last_recalled_at, last_adopted_at, last_reinforced_at,
	recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
	created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9::vector,
	$10, $11, $12, $13, $14, $15, $16, $17, $18,
	$19, $20, $21, $22, $23, $24, $25,
	$26, $27, $28, $29, $30, $31,
	$32, $33
)
`, r.memoryNodesTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(rows), func(batch *pgx.Batch, start, end int) error {
		for _, row := range rows[start:end] {
			if err := validateMigrationVectorDimension("memory node", row.VectorID, row.Vector, r.shared.cfg.EmbeddingDimension); err != nil {
				return err
			}
			batch.Queue(statement,
				int64(row.ID),
				int64(row.TeamID),
				int64(row.SpaceID),
				int64(row.ProjectID),
				int64(row.UserID),
				int64(row.OriginSessionID),
				nullableUint64(row.SourceTurnID),
				strings.TrimSpace(row.VectorID),
				encodePGVectorLiteral(row.Vector),
				row.SourceKind,
				row.ScopeLevel,
				row.Category,
				row.Abstract,
				row.Details,
				row.Status,
				row.Priority,
				row.MemoryLevel,
				row.RefreshWeight,
				row.SupportCount,
				row.RebuttalCount,
				row.StatusReason,
				nullableTime(row.ExpiresAt),
				nullableTime(row.LastRecalledAt),
				nullableTime(row.LastAdoptedAt),
				nullableTime(row.LastReinforcedAt),
				row.RecalledCount,
				row.AdoptedCount,
				row.ReinforcementCount,
				row.CrossSessionAdoptedCount,
				row.DecayDisabled,
				row.DedupeHash,
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importMemoryContextEdges restores the contextual evidence graph after memory nodes so support/rebuttal counters keep their original aggregation state.
// importMemoryContextEdges 用于在记忆节点导入后恢复情境证据图，保持 support/rebuttal 计数的原始聚合状态。
func (r *maintenanceRepository) importMemoryContextEdges(ctx context.Context, tx pgx.Tx, rows []logicdomain.MemoryContextEdge) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	memory_id, context_key, context_value, support_count, rebuttal_count, last_supported_at, last_rebutted_at, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9
)
`, r.memoryContextEdgesTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(rows), func(batch *pgx.Batch, start, end int) error {
		for _, row := range rows[start:end] {
			batch.Queue(statement,
				int64(row.MemoryID),
				strings.TrimSpace(row.ContextKey),
				strings.TrimSpace(row.ContextValue),
				row.SupportCount,
				row.RebuttalCount,
				nullableTime(row.LastSupportedAt),
				nullableTime(row.LastRebuttedAt),
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importProfileNodes restores durable profile evidence after turns so any surviving turn references remain valid.
// importProfileNodes 用于在 turn 导入后恢复长期画像证据，保证仍然存在的 turn 引用保持有效。
func (r *maintenanceRepository) importProfileNodes(ctx context.Context, tx pgx.Tx, rows []logicdomain.ProfileNodeRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	id, turn_id, profile_type, bind_id, content, profile_status, priority, profile_level,
	level_reason, refresh_weight, source_kind, source_id, status_reason, expires_at,
	superseded_by_id, profile_date, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8,
	$9, $10, $11, $12, $13, $14,
	$15, $16, $17, $18
)
`, r.profileNodesTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(rows), func(batch *pgx.Batch, start, end int) error {
		for _, row := range rows[start:end] {
			batch.Queue(statement,
				int64(row.ID),
				nullableUint64(row.TurnID),
				row.ProfileType,
				int64(row.BindID),
				row.Content,
				row.Status,
				row.Priority,
				row.ProfileLevel,
				row.LevelReason,
				row.RefreshWeight,
				row.SourceKind,
				int64(row.SourceID),
				row.StatusReason,
				nullableTime(row.ExpiresAt),
				int64(row.SupersededByID),
				row.ProfileDate,
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// importProfileInstructions restores explicit manual review instructions last because they depend only on already imported hierarchy/profile identifiers.
// importProfileInstructions 用于最后恢复手工评审指令，因为它们只依赖此前已导入完成的层级和画像标识。
func (r *maintenanceRepository) importProfileInstructions(ctx context.Context, tx pgx.Tx, rows []logicdomain.ProfileInstructionRecord) error {
	statement := fmt.Sprintf(`
INSERT INTO %s (
	id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8, $9
)
`, r.profileInstructionsTable())
	return queueBatches(ctx, tx, r.shared.cfg.MigrationBatchSize, len(rows), func(batch *pgx.Batch, start, end int) error {
		for _, row := range rows[start:end] {
			batch.Queue(statement,
				int64(row.ID),
				row.ProfileType,
				int64(row.BindID),
				row.Instruction,
				row.Status,
				row.ReviewResult,
				row.FailureReason,
				row.CreatedAt.UTC(),
				row.UpdatedAt.UTC(),
			)
		}
		return nil
	})
}

// queueBatches groups migration writes into bounded pgx batches so large imports do not degrade into one round trip per row.
// queueBatches 用于把迁移写入整理成有界 pgx 批次，避免大批量导入退化成“一行一次往返”。
func queueBatches(ctx context.Context, tx pgx.Tx, batchSize, total int, queue func(batch *pgx.Batch, start, end int) error) error {
	if total == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	for start := 0; start < total; start += batchSize {
		end := start + batchSize
		if end > total {
			end = total
		}
		batch := &pgx.Batch{}
		if err := queue(batch, start, end); err != nil {
			return err
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
	}
	return nil
}

// validateMigrationVectorDimension fails fast when SQLite carries a malformed vector_json payload so the importer never silently writes a truncated or empty embedding.
// validateMigrationVectorDimension 用于在 SQLite 携带损坏的 vector_json 载荷时快速失败，避免导入器静默写入被截断或为空的 embedding。
func validateMigrationVectorDimension(kind, key string, vector []float32, expected int) error {
	if expected <= 0 {
		return fmt.Errorf("postgres embedding dimension must be > 0 during debug migration")
	}
	if len(vector) != expected {
		return fmt.Errorf("%s %q vector dimension mismatch: got %d want %d", strings.TrimSpace(kind), strings.TrimSpace(key), len(vector), expected)
	}
	return nil
}

// importManagedSnapshot delegates to the maintenance repository for debug snapshot import.
// importManagedSnapshot 用于把调试快照导入委托给 maintenance 仓储。
func (s *Store) importManagedSnapshot(ctx context.Context, snapshot storagemigrate.Snapshot) (storagemigrate.Report, error) {
	return s.repos.maintenance.importManagedSnapshot(ctx, snapshot)
}
