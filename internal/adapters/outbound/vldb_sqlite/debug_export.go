// debug_export.go exports one full managed SQLite snapshot for split-to-combined migration workflows.
// debug_export.go 用于为 split-to-combined 迁移流程导出完整的受管 SQLite 快照。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/storagemigrate"
)

// debugNoiseEmbeddingExportRow adds SQLite rowid paging metadata to one noise-embedding export row so large cache tables can still be exported incrementally.
// debugNoiseEmbeddingExportRow 用于给噪声向量缓存导出行补上 SQLite rowid 分页元数据，确保较大的缓存表也能增量导出。
type debugNoiseEmbeddingExportRow struct {
	RowID int64 `json:"row_id"`
	noiseEmbeddingRow
}

// debugMemoryContextEdgeExportRow adds SQLite rowid paging metadata to one memory-context edge export row so composite-primary-key tables can still export in batches.
// debugMemoryContextEdgeExportRow 用于给记忆情境边导出行补上 SQLite rowid 分页元数据，确保复合主键表也能按批次导出。
type debugMemoryContextEdgeExportRow struct {
	RowID int64 `json:"row_id"`
	memoryContextEdgeRow
}

// DebugExportManagedSnapshot connects to the SQLite local-FFI fact store, pages through all managed VMM tables, and returns one normalized migration snapshot.
// DebugExportManagedSnapshot 用于连接 SQLite 本地 FFI 事实库，分页遍历全部 VMM 受管表，并返回一份标准化迁移快照。
func DebugExportManagedSnapshot(ctx context.Context, libraryPath string, databasePath string, timeout time.Duration, batchSize int) (storagemigrate.Snapshot, error) {
	store, err := NewStore(libraryPath, databasePath, timeout)
	if err != nil {
		return storagemigrate.Snapshot{}, err
	}
	defer func() {
		_ = store.Shutdown(context.Background())
	}()
	return store.buildManagedMigrationSnapshot(ctx, batchSize)
}

// DebugExportManagedSnapshot exports all managed rows through the database handle already owned by this store.
// DebugExportManagedSnapshot 通过当前存储已经持有的数据库句柄导出全部受管数据。
func (s *Store) DebugExportManagedSnapshot(ctx context.Context, batchSize int) (storagemigrate.Snapshot, error) {
	return s.buildManagedMigrationSnapshot(ctx, batchSize)
}

// buildManagedMigrationSnapshot gathers the whole managed dataset from SQLite while keeping larger tables paged by id or rowid to avoid one giant query response.
// buildManagedMigrationSnapshot 用于从 SQLite 汇总整套受管数据，同时对较大表按 id 或 rowid 分页，避免单次查询返回过大的载荷。
func (s *Store) buildManagedMigrationSnapshot(ctx context.Context, batchSize int) (storagemigrate.Snapshot, error) {
	if s == nil || s.database == nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("sqlite store is not initialized")
	}
	if batchSize <= 0 {
		batchSize = 500
	}

	// Export the hierarchy first so downstream session and memory rows can preserve their original foreign-key coordinates.
	// 先导出层级数据，确保后续 session 与记忆行能保留原有外键坐标。
	users, err := loadSQLiteUint64IDBatches[userRow](s, ctx, `
SELECT id, name, profile, delete_confirm_code, created_at, updated_at
FROM vmm_users
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row userRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite users: %w", err)
	}
	teams, err := loadSQLiteUint64IDBatches[teamRow](s, ctx, `
SELECT id, name, profile, created_at, updated_at
FROM vmm_teams
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row teamRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite teams: %w", err)
	}
	spaces, err := loadSQLiteUint64IDBatches[spaceRow](s, ctx, `
SELECT id, team_id, name, profile, created_at, updated_at
FROM vmm_spaces
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row spaceRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite spaces: %w", err)
	}
	projects, err := loadSQLiteUint64IDBatches[projectJoinRow](s, ctx, `
SELECT p.id, p.team_id, p.space_id, p.name, p.profile, t.name AS team_name, sp.name AS space_name, p.created_at, p.updated_at
FROM vmm_projects p
JOIN vmm_teams t ON t.id = p.team_id
JOIN vmm_spaces sp ON sp.id = p.space_id
WHERE p.id > ?
ORDER BY p.id ASC
LIMIT ?
`, batchSize, func(row projectJoinRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite projects: %w", err)
	}
	sessions, err := loadSQLiteUint64IDBatches[sessionRow](s, ctx, `
SELECT id, session_key, user_id, team_id, space_id, project_id,
       turn_count, last_summarized_id, last_compacted_turn_id, summarize_content, summarize_budget,
       last_extract_observed_timestamp, last_extract_completed_timestamp, last_compacted_timestamp,
       created_timestamp, updated_timestamp
FROM vmm_sessions
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row sessionRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite sessions: %w", err)
	}

	// Export the high-volume behavioral tables in deterministic id order so imports can replay them in referential order without re-sorting.
	// 再按稳定 id 顺序导出高体量行为表，让导入阶段无需重新排序即可按引用顺序回放。
	turns, err := loadSQLiteUint64IDBatches[turnRecordRow](s, ctx, `
SELECT id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, details, details_budget, created_timestamp, updated_timestamp
FROM vmm_turn_records
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row turnRecordRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite turns: %w", err)
	}
	memoryNodes, err := loadSQLiteUint64IDBatches[memoryNodeRow](s, ctx, `
SELECT id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
       vector_id, vector_json, source_kind, scope_level, category, abstract, details,
       memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
       expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
       recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled,
       dedupe_hash, created_timestamp, updated_timestamp
FROM vmm_memory_nodes
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row memoryNodeRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite memory nodes: %w", err)
	}
	memoryEdges, err := loadSQLiteRowIDBatches[debugMemoryContextEdgeExportRow](s, ctx, `
SELECT rowid AS row_id, memory_id, context_key, context_value, support_count, rebuttal_count,
       last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
FROM vmm_memory_context_edges
WHERE rowid > ?
ORDER BY rowid ASC
LIMIT ?
`, batchSize, func(row debugMemoryContextEdgeExportRow) int64 { return row.RowID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite memory context edges: %w", err)
	}
	profileNodes, err := loadSQLiteUint64IDBatches[profileNodeRow](s, ctx, `
SELECT id, turn_id, profile_type, bind_id, content, profile_status, priority, profile_level,
       level_reason, refresh_weight, source_kind, source_id, status_reason, expires_timestamp,
       superseded_by_id, profile_date, created_timestamp, updated_timestamp
FROM vmm_profile_nodes
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row profileNodeRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite profile nodes: %w", err)
	}
	profileInstructions, err := loadSQLiteUint64IDBatches[profileInstructionRow](s, ctx, `
SELECT id, profile_type, bind_id, instruction, instruction_status, review_result_json, failure_reason, created_timestamp, updated_timestamp
FROM vmm_profile_instructions
WHERE id > ?
ORDER BY id ASC
LIMIT ?
`, batchSize, func(row profileInstructionRow) uint64 { return row.ID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite profile instructions: %w", err)
	}
	noiseRows, err := loadSQLiteRowIDBatches[debugNoiseEmbeddingExportRow](s, ctx, `
SELECT rowid AS row_id, scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
FROM vmm_noise_embeddings
WHERE rowid > ?
ORDER BY rowid ASC
LIMIT ?
`, batchSize, func(row debugNoiseEmbeddingExportRow) int64 { return row.RowID })
	if err != nil {
		return storagemigrate.Snapshot{}, fmt.Errorf("export sqlite noise embeddings: %w", err)
	}

	snapshot := storagemigrate.Snapshot{
		NoiseEmbeddings:     make([]logicdomain.NoiseEmbeddingCacheEntry, 0, len(noiseRows)),
		Users:               make([]logicdomain.UserRecord, 0, len(users)),
		Teams:               make([]logicdomain.TeamRecord, 0, len(teams)),
		Spaces:              make([]logicdomain.SpaceRecord, 0, len(spaces)),
		Projects:            make([]logicdomain.ProjectRecord, 0, len(projects)),
		Sessions:            make([]logicdomain.SessionRecord, 0, len(sessions)),
		Turns:               make([]logicdomain.SessionTurnRecord, 0, len(turns)),
		MemoryNodes:         make([]logicdomain.MemoryNodeRecord, 0, len(memoryNodes)),
		MemoryContextEdges:  make([]logicdomain.MemoryContextEdge, 0, len(memoryEdges)),
		ProfileNodes:        make([]logicdomain.ProfileNodeRecord, 0, len(profileNodes)),
		ProfileInstructions: make([]logicdomain.ProfileInstructionRecord, 0, len(profileInstructions)),
	}
	for _, row := range noiseRows {
		updatedAt, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(row.UpdatedAt))
		snapshot.NoiseEmbeddings = append(snapshot.NoiseEmbeddings, logicdomain.NoiseEmbeddingCacheEntry{
			Scope:        strings.TrimSpace(row.Scope),
			Language:     strings.TrimSpace(row.Language),
			CategoryName: strings.TrimSpace(row.CategoryName),
			Phrase:       strings.TrimSpace(row.Phrase),
			Model:        strings.TrimSpace(row.Model),
			Dimension:    row.Dimension,
			RulesHash:    strings.TrimSpace(row.RulesHash),
			Vector:       decodeFloat32Slice(row.VectorJSON),
			UpdatedAt:    updatedAt.UTC(),
		})
	}
	for _, row := range users {
		snapshot.Users = append(snapshot.Users, row.toDomain())
	}
	for _, row := range teams {
		snapshot.Teams = append(snapshot.Teams, row.toDomain())
	}
	for _, row := range spaces {
		snapshot.Spaces = append(snapshot.Spaces, row.toDomain())
	}
	for _, row := range projects {
		snapshot.Projects = append(snapshot.Projects, row.toDomain())
	}
	for _, row := range sessions {
		snapshot.Sessions = append(snapshot.Sessions, row.toDomain())
	}
	for _, row := range turns {
		snapshot.Turns = append(snapshot.Turns, row.toDomain())
	}
	for _, row := range memoryNodes {
		snapshot.MemoryNodes = append(snapshot.MemoryNodes, row.toMemoryNodeRecord())
	}
	for _, row := range memoryEdges {
		snapshot.MemoryContextEdges = append(snapshot.MemoryContextEdges, row.toDomain())
	}
	for _, row := range profileNodes {
		snapshot.ProfileNodes = append(snapshot.ProfileNodes, row.toRecord())
	}
	for _, row := range profileInstructions {
		snapshot.ProfileInstructions = append(snapshot.ProfileInstructions, row.toDomain())
	}
	return snapshot, nil
}

// loadSQLiteUint64IDBatches reads one SQLite table in ascending numeric-id order so larger managed tables can be exported without one unbounded JSON response.
// loadSQLiteUint64IDBatches 用于按升序数字 id 分批读取 SQLite 表，避免较大的受管表一次性返回无界 JSON 结果。
func loadSQLiteUint64IDBatches[T any](s *Store, ctx context.Context, sql string, batchSize int, lastID func(T) uint64) ([]T, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	rows := make([]T, 0)
	cursor := uint64(0)
	for {
		batch, err := queryRows[T](s, ctx, sql, cursor, batchSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return rows, nil
		}
		rows = append(rows, batch...)
		cursor = lastID(batch[len(batch)-1])
		if len(batch) < batchSize {
			return rows, nil
		}
	}
}

// loadSQLiteRowIDBatches reads one SQLite table through rowid paging so composite-primary-key tables can still export incrementally in deterministic order.
// loadSQLiteRowIDBatches 用于通过 rowid 分页读取 SQLite 表，让复合主键表也能按确定性顺序增量导出。
func loadSQLiteRowIDBatches[T any](s *Store, ctx context.Context, sql string, batchSize int, lastRowID func(T) int64) ([]T, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	rows := make([]T, 0)
	cursor := int64(0)
	for {
		batch, err := queryRows[T](s, ctx, sql, cursor, batchSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return rows, nil
		}
		rows = append(rows, batch...)
		cursor = lastRowID(batch[len(batch)-1])
		if len(batch) < batchSize {
			return rows, nil
		}
	}
}
