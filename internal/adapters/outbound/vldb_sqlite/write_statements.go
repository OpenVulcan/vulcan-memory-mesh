// SQLite bound write statements shared by turn, profile and memory workflows.
// SQLite 适配层轮次、画像与记忆工作流共用的参数化写入语句。
package vldb_sqlite

import (
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"strings"
)

// parameterizedTurnInsertStatement returns one turn INSERT with dehydrated JSON bound as a typed SQLite param.
// parameterizedTurnInsertStatement 用于返回 turn 插入语句，并把脱水 JSON 作为强类型 SQLite 参数绑定。
// The dehydrated payload stays as JSON text on purpose so OSS-local boot no longer depends on SQLite's json extension.
// 脱水载荷会刻意以 JSON 文本形式保存，避免 OSS 本地版启动继续依赖 SQLite 的 json 扩展。
func parameterizedTurnInsertStatement(id, sessionID, projectID uint64, dehydratedContent string, dehydratedBudget int, createdMs, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_turn_records (
  id, session_id, project_id, dehydrated_content, dehydrated_budget, extracted_status, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);
`,
		Params: []any{id, sessionID, projectID, strings.TrimSpace(dehydratedContent), dehydratedBudget, logicdomain.TurnExtractedStatusPending, createdMs, updatedMs},
	}
}

// parameterizedSessionTurnUpdateStatement returns the absolute session counter UPDATE paired after a durable turn append.
// parameterizedSessionTurnUpdateStatement 用于返回跟随 turn 持久化追加之后执行的绝对 session 计数更新语句。
func parameterizedSessionTurnUpdateStatement(sessionID uint64, turnCount, summarizeBudget int, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_sessions
SET turn_count = ?, summarize_budget = ?,
    updated_timestamp = CASE
      WHEN updated_timestamp < ? THEN ? ELSE updated_timestamp
    END
WHERE id = ?;
`,
		Params: []any{turnCount, summarizeBudget, updatedMs, updatedMs, sessionID},
	}
}

// parameterizedSessionExtractWindowUpdateStatement returns the monotonic extract-window checkpoint UPDATE for one resolved session.
// parameterizedSessionExtractWindowUpdateStatement 用于返回单个已解析 session 的单调提炼窗口检查点 UPDATE。
func parameterizedSessionExtractWindowUpdateStatement(sessionID uint64, observedMs, completedMs, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_sessions
SET last_extract_observed_timestamp = CASE
      WHEN last_extract_observed_timestamp < ? THEN ? ELSE last_extract_observed_timestamp
    END,
    last_extract_completed_timestamp = CASE
      WHEN last_extract_completed_timestamp < ? THEN ? ELSE last_extract_completed_timestamp
    END,
    updated_timestamp = CASE
      WHEN updated_timestamp < ? THEN ? ELSE updated_timestamp
    END
WHERE id = ?;
`,
		Params: []any{observedMs, observedMs, completedMs, completedMs, updatedMs, updatedMs, sessionID},
	}
}

// parameterizedTurnAnalysisUpdateStatement returns the pending-turn update guarded by current session ownership with extracted details bound as typed SQLite params.
// parameterizedTurnAnalysisUpdateStatement 用于返回受当前 session 归属保护的 pending turn 更新语句，并把提炼详情作为强类型 SQLite 参数绑定。
func parameterizedTurnAnalysisUpdateStatement(sessionID, turnID uint64, details string, detailsBudget int, updatedMs int64) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_turn_records
SET details = ?, details_budget = ?, extracted_status = ?, updated_timestamp = ?
WHERE id = ? AND session_id = ? AND extracted_status = ?;
`,
		Params: []any{strings.TrimSpace(details), detailsBudget, logicdomain.TurnExtractedStatusDone, updatedMs, turnID, sessionID, logicdomain.TurnExtractedStatusPending},
	}
}

// parameterizedMemoryNodeInsertStatement returns the unified memory INSERT used by post-action and direct-write paths with text fields bound outside the SQL template.
// parameterizedMemoryNodeInsertStatement 用于返回 post-action 与主动写路径使用的统一记忆 INSERT，并把文本字段绑定在 SQL 模板之外。
func parameterizedMemoryNodeInsertStatement(record logicdomain.MemoryNodeRecord) (sqliteWriteStatement, error) {
	vectorJSON, err := encodeFloat32Slice(record.Vector)
	if err != nil {
		return sqliteWriteStatement{}, fmt.Errorf("memory %d vector %q: %w", record.ID, record.VectorID, err)
	}
	expiresMs := int64(0)
	if !record.ExpiresAt.IsZero() {
		expiresMs = record.ExpiresAt.UTC().UnixMilli()
	}
	lastRecalledMs := int64(0)
	if !record.LastRecalledAt.IsZero() {
		lastRecalledMs = record.LastRecalledAt.UTC().UnixMilli()
	}
	lastAdoptedMs := int64(0)
	if !record.LastAdoptedAt.IsZero() {
		lastAdoptedMs = record.LastAdoptedAt.UTC().UnixMilli()
	}
	lastReinforcedMs := int64(0)
	if !record.LastReinforcedAt.IsZero() {
		lastReinforcedMs = record.LastReinforcedAt.UTC().UnixMilli()
	}
	var sourceTurnIDParam any
	if record.SourceTurnID != 0 {
		sourceTurnIDParam = record.SourceTurnID
	}
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_memory_nodes (
  id, team_id, space_id, project_id, user_id, origin_session_id, source_turn_id,
  vector_id, vector_json, source_kind, scope_level, category, abstract, details,
  memory_status, priority, memory_level, refresh_weight, support_count, rebuttal_count, status_reason,
  expires_timestamp, last_recalled_timestamp, last_adopted_timestamp, last_reinforced_timestamp,
  recalled_count, adopted_count, reinforcement_count, cross_session_adopted_count, decay_disabled, dedupe_hash,
  created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`,
		Params: []any{
			record.ID,
			record.TeamID,
			record.SpaceID,
			record.ProjectID,
			record.UserID,
			record.OriginSessionID,
			sourceTurnIDParam,
			strings.TrimSpace(record.VectorID),
			vectorJSON,
			record.SourceKind,
			record.ScopeLevel,
			record.Category,
			strings.TrimSpace(record.Abstract),
			strings.TrimSpace(record.Details),
			record.Status,
			record.Priority,
			record.MemoryLevel,
			record.RefreshWeight,
			record.SupportCount,
			record.RebuttalCount,
			strings.TrimSpace(record.StatusReason),
			expiresMs,
			lastRecalledMs,
			lastAdoptedMs,
			lastReinforcedMs,
			record.RecalledCount,
			record.AdoptedCount,
			record.ReinforcementCount,
			record.CrossSessionAdoptedCount,
			boolToSQLiteInt(record.DecayDisabled),
			strings.TrimSpace(record.DedupeHash),
			record.CreatedAt.UTC().UnixMilli(),
			record.UpdatedAt.UTC().UnixMilli(),
		},
	}, nil
}

// parameterizedMemoryContextEdgesReplaceStatements returns the delete-then-insert edge rewrite used after a memory node write.
// parameterizedMemoryContextEdgesReplaceStatements 用于返回记忆节点写入后的先删后插情境边重写语句。
func parameterizedMemoryContextEdgesReplaceStatements(memoryID uint64, edges []logicdomain.MemoryContextEdge) []sqliteWriteStatement {
	if memoryID == 0 {
		return nil
	}
	statements := []sqliteWriteStatement{{
		SQL: `
DELETE FROM vmm_memory_context_edges
WHERE memory_id = ?;
`,
		Params: []any{memoryID},
	}}
	for _, edge := range edges {
		if strings.TrimSpace(edge.ContextKey) == "" || strings.TrimSpace(edge.ContextValue) == "" {
			continue
		}
		lastSupportedMs := int64(0)
		if !edge.LastSupportedAt.IsZero() {
			lastSupportedMs = edge.LastSupportedAt.UTC().UnixMilli()
		}
		lastRebuttedMs := int64(0)
		if !edge.LastRebuttedAt.IsZero() {
			lastRebuttedMs = edge.LastRebuttedAt.UTC().UnixMilli()
		}
		// Bind the contextual labels because they are extracted text, while the durable owner remains the outer memoryID.
		// 绑定情境标签文本，因为它们来自提炼结果；长期归属仍以外层 memoryID 为准。
		statements = append(statements, sqliteWriteStatement{
			SQL: `
INSERT INTO vmm_memory_context_edges (
  memory_id, context_key, context_value, support_count, rebuttal_count,
  last_supported_timestamp, last_rebutted_timestamp, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
`,
			Params: []any{
				memoryID,
				strings.TrimSpace(edge.ContextKey),
				strings.TrimSpace(edge.ContextValue),
				edge.SupportCount,
				edge.RebuttalCount,
				lastSupportedMs,
				lastRebuttedMs,
				edge.CreatedAt.UTC().UnixMilli(),
				edge.UpdatedAt.UTC().UnixMilli(),
			},
		})
	}
	return statements
}

// parameterizedMemoryAdoptionUpdateStatement returns one typed UPDATE for a memory row that has just been adopted by pre-check.
// parameterizedMemoryAdoptionUpdateStatement 用于返回一条刚被 pre-check 采纳的记忆行强类型更新语句。
func parameterizedMemoryAdoptionUpdateStatement(record logicdomain.MemoryNodeRecord) sqliteWriteStatement {
	expiresMs := int64(0)
	if !record.ExpiresAt.IsZero() {
		expiresMs = record.ExpiresAt.UTC().UnixMilli()
	}
	lastRecalledMs := int64(0)
	if !record.LastRecalledAt.IsZero() {
		lastRecalledMs = record.LastRecalledAt.UTC().UnixMilli()
	}
	lastAdoptedMs := int64(0)
	if !record.LastAdoptedAt.IsZero() {
		lastAdoptedMs = record.LastAdoptedAt.UTC().UnixMilli()
	}
	lastReinforcedMs := int64(0)
	if !record.LastReinforcedAt.IsZero() {
		lastReinforcedMs = record.LastReinforcedAt.UTC().UnixMilli()
	}
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_memory_nodes
SET scope_level = ?,
    memory_status = ?,
    memory_level = ?,
    refresh_weight = ?,
    expires_timestamp = ?,
    last_recalled_timestamp = ?,
    last_adopted_timestamp = ?,
    last_reinforced_timestamp = ?,
    recalled_count = ?,
    adopted_count = ?,
    reinforcement_count = ?,
    cross_session_adopted_count = ?,
    decay_disabled = ?,
    updated_timestamp = ?
WHERE id = ?;
`,
		Params: []any{
			record.ScopeLevel,
			record.Status,
			record.MemoryLevel,
			record.RefreshWeight,
			expiresMs,
			lastRecalledMs,
			lastAdoptedMs,
			lastReinforcedMs,
			record.RecalledCount,
			record.AdoptedCount,
			record.ReinforcementCount,
			record.CrossSessionAdoptedCount,
			boolToSQLiteInt(record.DecayDisabled),
			record.UpdatedAt.UTC().UnixMilli(),
			record.ID,
		},
	}
}

// parameterizedProfileNodeInsertStatement returns the INSERT statement used by profile-node write paths with all profile text bound as typed SQLite params.
// parameterizedProfileNodeInsertStatement 用于返回画像节点写入路径使用的 INSERT 语句，并把全部画像文本作为强类型 SQLite 参数绑定。
func parameterizedProfileNodeInsertStatement(id uint64, turnID *uint64, profileType int, bindID uint64, node logicdomain.ProfileNodeCandidate, createdMs int64) sqliteWriteStatement {
	expiresMs := int64(0)
	if !node.ExpiresAt.IsZero() {
		expiresMs = node.ExpiresAt.UTC().UnixMilli()
	}
	var turnIDParam any
	if turnID != nil {
		turnIDParam = *turnID
	}
	return sqliteWriteStatement{
		SQL: `
INSERT INTO vmm_profile_nodes (
  id, turn_id, profile_type, bind_id, content, profile_status,
  priority, profile_level, level_reason, refresh_weight, source_kind, source_id, status_reason, expires_timestamp,
  superseded_by_id, profile_date, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?);
`,
		Params: []any{
			id,
			turnIDParam,
			profileType,
			bindID,
			strings.TrimSpace(node.Content),
			node.Status,
			node.Priority,
			node.ProfileLevel,
			strings.TrimSpace(node.LevelReason),
			node.RefreshWeight,
			node.SourceKind,
			node.SourceID,
			strings.TrimSpace(node.StatusReason),
			expiresMs,
			strings.TrimSpace(node.ProfileDate),
			createdMs,
			createdMs,
		},
	}
}

// boolToSQLiteInt renders SQLite-friendly boolean literals because the managed schema persists bool-like fields as integer columns.
// boolToSQLiteInt 用于渲染 SQLite 友好的布尔字面量，因为当前受管 schema 会把布尔型字段持久化为整型列。
func boolToSQLiteInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// optionalUint64Value converts one optional numeric column into the internal zero sentinel used by current in-memory helpers.
// optionalUint64Value 用于把可空数字列转换成当前内存辅助逻辑使用的零值哨兵。
func optionalUint64Value(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

// parameterizedUserDeleteConfirmationStatement returns the guarded UPDATE used by the first phase of user deletion so duplicate confirmation requests reuse one stable code instead of racing on the same row.
// parameterizedUserDeleteConfirmationStatement 用于返回用户删除第一阶段的受保护 UPDATE，让重复确认请求复用同一确认码，而不是在同一行上并发竞争。
func parameterizedUserDeleteConfirmationStatement(userID uint64, confirmationCode, updatedAt string) sqliteWriteStatement {
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_users
SET delete_confirm_code = ?, updated_at = ?
WHERE id = ? AND delete_confirm_code = '';
`,
		Params: []any{strings.TrimSpace(confirmationCode), strings.TrimSpace(updatedAt), userID},
	}
}

// parameterizedProfileNodesSupersedeStatement returns the guarded UPDATE used to retire older profile nodes after one accepted replacement node is durable.
// parameterizedProfileNodesSupersedeStatement 用于返回替代节点落库后退役旧画像节点的受保护 UPDATE 语句。
func parameterizedProfileNodesSupersedeStatement(profileType int, bindID uint64, nodeIDs []uint64, supersededByID uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	params := append([]any{
		logicdomain.ProfileStatusSuperseded,
		supersededByID,
		strings.TrimSpace(statusReason),
		updatedMs,
		logicdomain.ProfileStatusActive,
		profileType,
		bindID,
	}, sqliteUint64Params(nodeIDs)...)
	// Bind reviewer-controlled reason text, target ownership, and node ids together so profile replacement cannot cross scope boundaries.
	// 将评审器控制的原因文本、目标归属和节点 id 一起绑定，确保画像替代不会跨范围更新。
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = ?, superseded_by_id = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND profile_type = ? AND bind_id = ? AND id IN (%s);
`, nodeIDPlaceholders),
		Params: params,
	}, true
}

// parameterizedProfileNodesExpireStatement returns the lifecycle UPDATE used to mark due active profile nodes as expired.
// parameterizedProfileNodesExpireStatement 用于返回生命周期收敛中把到期 active 画像节点标为 expired 的 UPDATE 语句。
func parameterizedProfileNodesExpireStatement(nodeIDs []uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	nodeIDs = storageutil.NormalizeUint64List(nodeIDs)
	if len(nodeIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	nodeIDPlaceholders := sqlitePlaceholders(len(nodeIDs))
	params := append([]any{
		logicdomain.ProfileStatusExpired,
		strings.TrimSpace(statusReason),
		updatedMs,
		logicdomain.ProfileStatusActive,
	}, sqliteUint64Params(nodeIDs)...)
	// Bind lifecycle reason text and node ids together so expiry writes share the same dynamic-IN contract as profile replacement writes.
	// 将生命周期原因文本和节点 id 一起绑定，让过期写入与画像替代写入共享同一套动态 IN 契约。
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_profile_nodes
SET profile_status = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND id IN (%s);
`, nodeIDPlaceholders),
		Params: params,
	}, true
}

// parameterizedSingleProfileNodeRetireStatement returns the manual-instruction UPDATE used to retire one active profile node with the reviewer reason.
// parameterizedSingleProfileNodeRetireStatement 用于返回手工画像指令退役单条 active 画像节点的 UPDATE 语句，并绑定评审原因。
func parameterizedSingleProfileNodeRetireStatement(nodeID uint64, statusReason string, updatedMs int64) (sqliteWriteStatement, bool) {
	if nodeID == 0 {
		return sqliteWriteStatement{}, false
	}
	return sqliteWriteStatement{
		SQL: `
UPDATE vmm_profile_nodes
SET profile_status = ?, status_reason = ?, updated_timestamp = ?
WHERE profile_status = ? AND id = ?;
`,
		Params: []any{logicdomain.ProfileStatusSuperseded, strings.TrimSpace(statusReason), updatedMs, logicdomain.ProfileStatusActive, nodeID},
	}, true
}

// parameterizedProfileTargetUpdateSQL returns the controlled table-specific UPDATE template used by manual profile instructions before binding rendered profile text as parameters.
// parameterizedProfileTargetUpdateSQL 用于返回手工画像指令使用的受控目标表 UPDATE 模板，并在外层把渲染画像文本作为参数绑定。
func parameterizedProfileTargetUpdateSQL(profileType int) string {
	switch profileType {
	case logicdomain.ProfileTypeUser:
		return `UPDATE vmm_users SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeTeam:
		return `UPDATE vmm_teams SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeSpace:
		return `UPDATE vmm_spaces SET profile = ?, updated_at = ? WHERE id = ?;`
	case logicdomain.ProfileTypeProject:
		return `UPDATE vmm_projects SET profile = ?, updated_at = ? WHERE id = ?;`
	default:
		return ""
	}
}

// parameterizedMemoryNodesSupersedeStatement returns the memory replacement UPDATE used to mark obsolete active memory rows as superseded by id.
// parameterizedMemoryNodesSupersedeStatement 用于返回记忆替代路径中按记忆 id 把过时 active 记忆行标记为 superseded 的 UPDATE 语句。
func parameterizedMemoryNodesSupersedeStatement(memoryIDs []uint64, updatedMs int64) (sqliteWriteStatement, bool) {
	memoryIDs = storageutil.NormalizeUint64List(memoryIDs)
	if len(memoryIDs) == 0 {
		return sqliteWriteStatement{}, false
	}
	memoryIDPlaceholders := sqlitePlaceholders(len(memoryIDs))
	params := append([]any{
		logicdomain.MemoryStatusSuperseded,
		updatedMs,
		logicdomain.MemoryStatusActive,
	}, sqliteUint64Params(memoryIDs)...)
	return sqliteWriteStatement{
		SQL: fmt.Sprintf(`
UPDATE vmm_memory_nodes
SET memory_status = ?, updated_timestamp = ?
WHERE memory_status = ? AND id IN (%s);
`, memoryIDPlaceholders),
		Params: params,
	}, true
}
