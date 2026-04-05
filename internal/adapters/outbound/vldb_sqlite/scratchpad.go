// scratchpad.go implements the isolated DWM relational adapter for the sqlite-first runtime path.
// scratchpad.go 用于实现 sqlite-first 运行路径下的隔离 DWM 关系适配器。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

var (
	// _ScratchpadStoreContract keeps the sqlite store aligned with the isolated DWM CRUD port at compile time.
	// _ScratchpadStoreContract 用于在编译期确保 sqlite store 满足隔离 DWM CRUD 端口契约。
	_ appports.ScratchpadStore = (*Store)(nil)

	// _ScratchpadMaintenanceStoreContract keeps the sqlite store aligned with the isolated DWM maintenance port at compile time.
	// _ScratchpadMaintenanceStoreContract 用于在编译期确保 sqlite store 满足隔离 DWM 维护端口契约。
	_ appports.ScratchpadMaintenanceStore = (*Store)(nil)
)

// scratchpadPlanRow mirrors one SQLite scratchpad-plan row returned by the gateway so the adapter can keep transport decoding separate from domain semantics.
// scratchpadPlanRow 用于映射 SQLite 网关返回的一条 scratchpad 计划行，让适配器把传输解码与领域语义转换明确分离。
type scratchpadPlanRow struct {
	ID               uint64 `json:"id"`
	ProjectID        uint64 `json:"project_id"`
	UserID           uint64 `json:"user_id"`
	SessionKey       string `json:"session_key"`
	PlanName         string `json:"plan_name"`
	PlanNameNorm     string `json:"plan_name_norm"`
	CreatedTimestamp int64  `json:"created_timestamp"`
	UpdatedTimestamp int64  `json:"updated_timestamp"`
}

// toDomain converts one SQLite transport row into the isolated scratchpad plan model consumed by the use case.
// toDomain 用于把一条 SQLite 传输行转换成用例消费的隔离 scratchpad 计划模型。
func (r scratchpadPlanRow) toDomain() logicdomain.ScratchpadPlanRecord {
	return logicdomain.ScratchpadPlanRecord{
		ID:           r.ID,
		ProjectID:    r.ProjectID,
		UserID:       r.UserID,
		SessionKey:   strings.TrimSpace(r.SessionKey),
		PlanName:     strings.TrimSpace(r.PlanName),
		PlanNameNorm: strings.TrimSpace(r.PlanNameNorm),
		CreatedAt:    unixMilliToTime(r.CreatedTimestamp),
		UpdatedAt:    unixMilliToTime(r.UpdatedTimestamp),
	}
}

// scratchpadNodeRow mirrors one SQLite scratchpad-node row so deterministic key/value payloads can be converted after transport decoding.
// scratchpadNodeRow 用于映射一条 SQLite scratchpad 节点行，让确定性 key/value 载荷在传输解码后再统一转换。
type scratchpadNodeRow struct {
	ID               uint64 `json:"id"`
	PlanID           uint64 `json:"plan_id"`
	ItemKey          string `json:"item_key"`
	ItemValue        string `json:"item_value"`
	CreatedTimestamp int64  `json:"created_timestamp"`
	UpdatedTimestamp int64  `json:"updated_timestamp"`
}

// toItem converts one SQLite scratchpad-node transport row into the AI-facing key/value anchor returned by DWM reads.
// toItem 用于把一条 SQLite scratchpad 节点传输行转换成 DWM 读取返回的 AI 面向 key/value 锚点。
func (r scratchpadNodeRow) toItem() logicdomain.ScratchpadItem {
	return logicdomain.ScratchpadItem{
		Key:   strings.TrimSpace(r.ItemKey),
		Value: strings.TrimSpace(r.ItemValue),
	}
}

// scratchpadIDRow mirrors one simple scratchpad id lookup row so helper queries do not overload the MAX(id) allocator payload.
// scratchpadIDRow 用于映射简单的 scratchpad id 查询结果，避免辅助查询复用 MAX(id) 分配器的返回结构。
type scratchpadIDRow struct {
	ID uint64 `json:"id"`
}

// scratchpadCountRow mirrors one COUNT(*) query result used by deterministic scratchpad maintenance helpers.
// scratchpadCountRow 用于映射 scratchpad 维护辅助逻辑里使用的 COUNT(*) 查询结果。
type scratchpadCountRow struct {
	Count int `json:"count"`
}

// EnsureScratchpadScope validates that the referenced user and project already exist without creating any durable session rows.
// EnsureScratchpadScope 用于校验引用的 user 和 project 已存在，同时不会创建任何长期 session 行。
func (s *Store) EnsureScratchpadScope(ctx context.Context, scope logicdomain.ScratchpadScope) error {
	if strings.TrimSpace(scope.SessionKey) == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if scope.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	if scope.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if _, err := s.loadUserByID(ctx, scope.UserID); err != nil {
		return err
	}
	if _, err := s.loadProjectByID(ctx, scope.ProjectID); err != nil {
		return err
	}
	return nil
}

// LoadScratchpadPlan loads the current canonical plan lock for one isolated DWM scope.
// LoadScratchpadPlan 用于加载某个隔离 DWM 范围下当前生效的 canonical 计划锁。
func (s *Store) LoadScratchpadPlan(ctx context.Context, scope logicdomain.ScratchpadScope) (logicdomain.ScratchpadPlanRecord, bool, error) {
	rows, err := queryRows[scratchpadPlanRow](s, ctx, `
SELECT id, project_id, user_id, session_key, plan_name, plan_name_norm, created_timestamp, updated_timestamp
FROM vmm_scratchpad_plans
WHERE project_id = ? AND user_id = ? AND session_key = ?
LIMIT 1
`, scope.ProjectID, scope.UserID, strings.TrimSpace(scope.SessionKey))
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, false, fmt.Errorf("load scratchpad plan: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ScratchpadPlanRecord{}, false, nil
	}
	return rows[0].toDomain(), true, nil
}

// CreateScratchpadPlan inserts the canonical plan lock when the current isolated DWM scope does not have one yet, and reuses a same-plan concurrent winner when needed.
// CreateScratchpadPlan 用于在当前隔离 DWM 范围尚无计划锁时插入 canonical 计划锁，并在并发情况下复用同计划的先到者。
func (s *Store) CreateScratchpadPlan(ctx context.Context, scope logicdomain.ScratchpadScope, planName string, createdAt time.Time) (logicdomain.ScratchpadPlanRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Re-check under the sqlite write lock so concurrent callers never create two plan locks or silently cross-wire different plan names.
	// 在 sqlite 写锁内再次检查，避免并发调用创建两条计划锁，或把不同计划名静默串线。
	existing, found, err := s.LoadScratchpadPlan(ctx, scope)
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, err
	}
	planName = strings.TrimSpace(planName)
	if found {
		if strings.EqualFold(strings.TrimSpace(existing.PlanName), planName) {
			return existing, nil
		}
		return logicdomain.ScratchpadPlanRecord{}, logicdomain.ConflictError{
			Resource: "scratchpad_plan",
			Message:  fmt.Sprintf("scratchpad plan already exists for session %s", strings.TrimSpace(scope.SessionKey)),
		}
	}

	nowMs := chooseScratchpadUnixMilli(createdAt)
	nextID, err := s.nextNumericID(ctx, "vmm_scratchpad_plans")
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("allocate scratchpad plan id: %w", err)
	}
	if err := s.exec(ctx, `
INSERT INTO vmm_scratchpad_plans (
  id, project_id, user_id, session_key, plan_name, plan_name_norm, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, nextID, scope.ProjectID, scope.UserID, strings.TrimSpace(scope.SessionKey), planName, strings.ToLower(planName), nowMs, nowMs); err != nil {
		return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("insert scratchpad plan: %w", err)
	}
	return logicdomain.ScratchpadPlanRecord{
		ID:           nextID,
		ProjectID:    scope.ProjectID,
		UserID:       scope.UserID,
		SessionKey:   strings.TrimSpace(scope.SessionKey),
		PlanName:     planName,
		PlanNameNorm: strings.ToLower(planName),
		CreatedAt:    unixMilliToTime(nowMs),
		UpdatedAt:    unixMilliToTime(nowMs),
	}, nil
}

// UpsertScratchpadItems inserts or overwrites one deterministic DWM item batch and refreshes the parent plan timestamp only when real writes occurred.
// UpsertScratchpadItems 用于插入或覆盖一批确定性的 DWM item，并且只在真实写入发生时刷新父级计划时间戳。
func (s *Store) UpsertScratchpadItems(ctx context.Context, planID uint64, items []logicdomain.ScratchpadItem, updatedAt time.Time) (logicdomain.ScratchpadUpsertPersistResult, error) {
	if planID == 0 {
		return logicdomain.ScratchpadUpsertPersistResult{}, logicdomain.ValidationError{Field: "plan_id", Message: "must be a numeric id"}
	}
	if len(items) == 0 {
		return logicdomain.ScratchpadUpsertPersistResult{}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, strings.TrimSpace(item.Key))
	}
	existingKeys, err := s.loadScratchpadExistingKeys(ctx, planID, keys)
	if err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, err
	}
	insertedCount := 0
	updatedCount := 0
	batchItems := make([][]any, 0, len(items))
	nowMs := chooseScratchpadUnixMilli(updatedAt)
	for _, item := range items {
		itemKey := strings.TrimSpace(item.Key)
		if _, exists := existingKeys[itemKey]; exists {
			updatedCount++
		} else {
			insertedCount++
		}
		nodeID, err := s.ensureScratchpadNodeID(ctx, planID, itemKey)
		if err != nil {
			return logicdomain.ScratchpadUpsertPersistResult{}, err
		}
		batchItems = append(batchItems, []any{
			nodeID,
			planID,
			itemKey,
			strings.TrimSpace(item.Value),
			nowMs,
			nowMs,
		})
	}
	if err := s.execBatch(ctx, `
INSERT INTO vmm_scratchpad_nodes (
  id, plan_id, item_key, item_value, created_timestamp, updated_timestamp
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(plan_id, item_key) DO UPDATE SET
  item_value = excluded.item_value,
  updated_timestamp = excluded.updated_timestamp
`, batchItems); err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("upsert scratchpad items: %w", err)
	}
	if err := s.exec(ctx, `UPDATE vmm_scratchpad_plans SET updated_timestamp = ? WHERE id = ?`, nowMs, planID); err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("refresh scratchpad plan timestamp: %w", err)
	}
	return logicdomain.ScratchpadUpsertPersistResult{
		InsertedCount: insertedCount,
		UpdatedCount:  updatedCount,
	}, nil
}

// DeleteScratchpadItems deletes one deterministic key batch and keeps the plan row intact even when the last node disappears.
// DeleteScratchpadItems 用于删除一批确定性 key，并且即使删掉最后一个节点也保留计划行。
func (s *Store) DeleteScratchpadItems(ctx context.Context, planID uint64, keys []string, updatedAt time.Time) (logicdomain.ScratchpadDeletePersistResult, error) {
	if planID == 0 {
		return logicdomain.ScratchpadDeletePersistResult{}, logicdomain.ValidationError{Field: "plan_id", Message: "must be a numeric id"}
	}
	if len(keys) == 0 {
		return logicdomain.ScratchpadDeletePersistResult{}, nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	deletedCount, err := s.countScratchpadNodesByKeys(ctx, planID, keys)
	if err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, err
	}
	if deletedCount == 0 {
		remainingCount, err := s.countScratchpadNodes(ctx, planID)
		if err != nil {
			return logicdomain.ScratchpadDeletePersistResult{}, err
		}
		return logicdomain.ScratchpadDeletePersistResult{DeletedCount: 0, RemainingCount: remainingCount}, nil
	}

	deleteSQL, params := buildSQLiteScratchpadNodeKeyFilterSQL(`
DELETE FROM vmm_scratchpad_nodes
WHERE plan_id = ? AND `, planID, keys)
	if err := s.exec(ctx, deleteSQL, params...); err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("delete scratchpad items: %w", err)
	}
	remainingCount, err := s.countScratchpadNodes(ctx, planID)
	if err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, err
	}
	if err := s.exec(ctx, `UPDATE vmm_scratchpad_plans SET updated_timestamp = ? WHERE id = ?`, chooseScratchpadUnixMilli(updatedAt), planID); err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("refresh scratchpad plan after delete: %w", err)
	}
	return logicdomain.ScratchpadDeletePersistResult{
		DeletedCount:   deletedCount,
		RemainingCount: remainingCount,
	}, nil
}

// ListScratchpadItems returns either all deterministic DWM items or one filtered key subset ordered by key for stable AI consumption.
// ListScratchpadItems 用于返回全部确定性 DWM item，或按 key 过滤后的子集，并按 key 排序以保证 AI 消费稳定。
func (s *Store) ListScratchpadItems(ctx context.Context, planID uint64, keys []string) ([]logicdomain.ScratchpadItem, error) {
	if planID == 0 {
		return nil, logicdomain.ValidationError{Field: "plan_id", Message: "must be a numeric id"}
	}
	var (
		rows []scratchpadNodeRow
		err  error
	)
	if len(keys) == 0 {
		rows, err = queryRows[scratchpadNodeRow](s, ctx, `
SELECT id, plan_id, item_key, item_value, created_timestamp, updated_timestamp
FROM vmm_scratchpad_nodes
WHERE plan_id = ?
ORDER BY item_key ASC, id ASC
`, planID)
	} else {
		sqlText, params := buildSQLiteScratchpadNodeKeyFilterSQL(`
SELECT id, plan_id, item_key, item_value, created_timestamp, updated_timestamp
FROM vmm_scratchpad_nodes
WHERE plan_id = ? AND `, planID, keys)
		sqlText += ` ORDER BY item_key ASC, id ASC`
		rows, err = queryRows[scratchpadNodeRow](s, ctx, sqlText, params...)
	}
	if err != nil {
		return nil, fmt.Errorf("list scratchpad items: %w", err)
	}
	items := make([]logicdomain.ScratchpadItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toItem())
	}
	return items, nil
}

// CleanScratchpad deletes all DWM nodes and the parent plan row for one deterministic scope, while keeping empty sessions idempotent.
// CleanScratchpad 用于删除某个确定性范围下的全部 DWM 节点和父级计划行，同时保持空 session 场景幂等。
func (s *Store) CleanScratchpad(ctx context.Context, scope logicdomain.ScratchpadScope) (logicdomain.ScratchpadCleanPersistResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	plan, found, err := s.LoadScratchpadPlan(ctx, scope)
	if err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, err
	}
	if !found {
		return logicdomain.ScratchpadCleanPersistResult{}, nil
	}
	nodeCount, err := s.countScratchpadNodes(ctx, plan.ID)
	if err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, err
	}
	if err := s.exec(ctx, `DELETE FROM vmm_scratchpad_nodes WHERE plan_id = ?`, plan.ID); err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("delete scratchpad nodes during clean: %w", err)
	}
	if err := s.exec(ctx, `DELETE FROM vmm_scratchpad_plans WHERE id = ?`, plan.ID); err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("delete scratchpad plan during clean: %w", err)
	}
	return logicdomain.ScratchpadCleanPersistResult{
		HadPlan:          true,
		DeletedPlanCount: 1,
		DeletedNodeCount: nodeCount,
	}, nil
}

// DeleteExpiredScratchpadSessions hard-deletes expired DWM sessions by plan updated timestamp without using any trash or soft-backup tables.
// DeleteExpiredScratchpadSessions 用于按计划更新时间硬删除过期 DWM session，并且不会使用任何回收站或软备份表。
func (s *Store) DeleteExpiredScratchpadSessions(ctx context.Context, before time.Time, limit int) (logicdomain.ScratchpadGCResult, error) {
	if limit <= 0 {
		return logicdomain.ScratchpadGCResult{}, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	rows, err := queryRows[scratchpadPlanRow](s, ctx, `
SELECT id, project_id, user_id, session_key, plan_name, plan_name_norm, created_timestamp, updated_timestamp
FROM vmm_scratchpad_plans
WHERE updated_timestamp <= ?
ORDER BY updated_timestamp ASC, id ASC
LIMIT ?
`, before.UTC().UnixMilli(), limit)
	if err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("load expired scratchpad plans: %w", err)
	}
	if len(rows) == 0 {
		return logicdomain.ScratchpadGCResult{}, nil
	}
	planIDs := make([]uint64, 0, len(rows))
	for _, row := range rows {
		planIDs = append(planIDs, row.ID)
	}
	nodeCount, err := s.countScratchpadNodesByPlanIDs(ctx, planIDs)
	if err != nil {
		return logicdomain.ScratchpadGCResult{}, err
	}
	deleteNodesSQL, nodeParams := buildSQLiteScratchpadIDFilterSQL(`DELETE FROM vmm_scratchpad_nodes WHERE `, "plan_id", planIDs)
	if err := s.exec(ctx, deleteNodesSQL, nodeParams...); err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("delete expired scratchpad nodes: %w", err)
	}
	deletePlansSQL, planParams := buildSQLiteScratchpadIDFilterSQL(`DELETE FROM vmm_scratchpad_plans WHERE `, "id", planIDs)
	if err := s.exec(ctx, deletePlansSQL, planParams...); err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("delete expired scratchpad plans: %w", err)
	}
	return logicdomain.ScratchpadGCResult{
		DeletedPlanCount: len(planIDs),
		DeletedNodeCount: nodeCount,
	}, nil
}

// loadScratchpadExistingKeys loads the subset of keys that already exist under one plan so upsert can report inserted vs updated counts deterministically.
// loadScratchpadExistingKeys 用于加载某个计划下已存在的 key 子集，让 upsert 能确定性地区分 inserted 与 updated 计数。
func (s *Store) loadScratchpadExistingKeys(ctx context.Context, planID uint64, keys []string) (map[string]struct{}, error) {
	if len(keys) == 0 {
		return map[string]struct{}{}, nil
	}
	sqlText, params := buildSQLiteScratchpadNodeKeyFilterSQL(`
SELECT id, plan_id, item_key, item_value, created_timestamp, updated_timestamp
FROM vmm_scratchpad_nodes
WHERE plan_id = ? AND `, planID, keys)
	rows, err := queryRows[scratchpadNodeRow](s, ctx, sqlText, params...)
	if err != nil {
		return nil, fmt.Errorf("load existing scratchpad keys: %w", err)
	}
	out := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		out[strings.TrimSpace(row.ItemKey)] = struct{}{}
	}
	return out, nil
}

// ensureScratchpadNodeID resolves one existing node id by logical key or allocates a fresh deterministic id for new rows.
// ensureScratchpadNodeID 用于按逻辑 key 解析已有节点 id，或为新行分配一个确定性的全新 id。
func (s *Store) ensureScratchpadNodeID(ctx context.Context, planID uint64, key string) (uint64, error) {
	rows, err := queryRows[scratchpadIDRow](s, ctx, `
SELECT id
FROM vmm_scratchpad_nodes
WHERE plan_id = ? AND item_key = ?
LIMIT 1
`, planID, strings.TrimSpace(key))
	if err != nil {
		return 0, fmt.Errorf("load scratchpad node id: %w", err)
	}
	if len(rows) > 0 && rows[0].ID > 0 {
		return rows[0].ID, nil
	}
	nextID, err := s.nextNumericID(ctx, "vmm_scratchpad_nodes")
	if err != nil {
		return 0, fmt.Errorf("allocate scratchpad node id: %w", err)
	}
	return nextID, nil
}

// countScratchpadNodes returns how many nodes currently remain under one plan.
// countScratchpadNodes 用于返回某个计划下当前剩余节点数量。
func (s *Store) countScratchpadNodes(ctx context.Context, planID uint64) (int, error) {
	rows, err := queryRows[scratchpadCountRow](s, ctx, `
SELECT COUNT(*) AS count
FROM vmm_scratchpad_nodes
WHERE plan_id = ?
`, planID)
	if err != nil {
		return 0, fmt.Errorf("count scratchpad nodes: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Count, nil
}

// countScratchpadNodesByKeys returns how many of the requested keys currently exist under one plan.
// countScratchpadNodesByKeys 用于返回某个计划下当前命中的请求 key 数量。
func (s *Store) countScratchpadNodesByKeys(ctx context.Context, planID uint64, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	sqlText, params := buildSQLiteScratchpadNodeKeyFilterSQL(`
SELECT COUNT(*) AS count
FROM vmm_scratchpad_nodes
WHERE plan_id = ? AND `, planID, keys)
	rows, err := queryRows[scratchpadCountRow](s, ctx, sqlText, params...)
	if err != nil {
		return 0, fmt.Errorf("count scratchpad nodes by keys: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Count, nil
}

// countScratchpadNodesByPlanIDs returns how many nodes belong to the selected expired plan set.
// countScratchpadNodesByPlanIDs 用于返回所选过期计划集合下的节点数量。
func (s *Store) countScratchpadNodesByPlanIDs(ctx context.Context, planIDs []uint64) (int, error) {
	if len(planIDs) == 0 {
		return 0, nil
	}
	sqlText, params := buildSQLiteScratchpadIDFilterSQL(`
SELECT COUNT(*) AS count
FROM vmm_scratchpad_nodes
WHERE `, "plan_id", planIDs)
	rows, err := queryRows[scratchpadCountRow](s, ctx, sqlText, params...)
	if err != nil {
		return 0, fmt.Errorf("count scratchpad nodes by plan ids: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Count, nil
}

// buildSQLiteScratchpadNodeKeyFilterSQL appends one deterministic item-key IN filter while preserving the plan_id placeholder at position one.
// buildSQLiteScratchpadNodeKeyFilterSQL 用于追加一个确定性的 item_key IN 过滤条件，同时保留 plan_id 作为第一参数。
func buildSQLiteScratchpadNodeKeyFilterSQL(prefix string, planID uint64, keys []string) (string, []any) {
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+1)
	args = append(args, planID)
	for _, key := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, strings.TrimSpace(key))
	}
	return prefix + `item_key IN (` + strings.Join(placeholders, ", ") + `)`, args
}

// buildSQLiteScratchpadIDFilterSQL appends one deterministic numeric-id IN filter for scratchpad helpers, so node and plan deletes can reuse the same placeholder builder without accidentally addressing the wrong column.
// buildSQLiteScratchpadIDFilterSQL 用于给 scratchpad 辅助逻辑追加确定性的数字 id IN 过滤条件，让节点和计划删除复用同一占位符构造器，同时避免误用错误列名。
func buildSQLiteScratchpadIDFilterSQL(prefix, column string, ids []uint64) (string, []any) {
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	return prefix + strings.TrimSpace(column) + ` IN (` + strings.Join(placeholders, ", ") + `)`, args
}

// chooseScratchpadUnixMilli preserves one explicit timestamp when present and otherwise uses the current UTC time for deterministic scratchpad writes.
// chooseScratchpadUnixMilli 用于在时间已显式提供时保留原值，否则使用当前 UTC 时间进行确定性 scratchpad 写入。
func chooseScratchpadUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return time.Now().UTC().UnixMilli()
	}
	return value.UTC().UnixMilli()
}
