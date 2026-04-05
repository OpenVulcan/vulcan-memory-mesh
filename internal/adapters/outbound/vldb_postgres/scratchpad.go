// scratchpad.go implements the isolated DWM relational adapter for the PostgreSQL combined-store runtime path.
// scratchpad.go 用于实现 PostgreSQL 组合库存储路径下的隔离 DWM 关系适配器。
package vldb_postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

var (
	// _ScratchpadStoreContract keeps the PostgreSQL combined store aligned with the isolated DWM CRUD port at compile time.
	// _ScratchpadStoreContract 用于在编译期确保 PostgreSQL 组合库满足隔离 DWM CRUD 端口契约。
	_ appports.ScratchpadStore = (*Store)(nil)

	// _ScratchpadMaintenanceStoreContract keeps the PostgreSQL combined store aligned with the isolated DWM maintenance port at compile time.
	// _ScratchpadMaintenanceStoreContract 用于在编译期确保 PostgreSQL 组合库满足隔离 DWM 维护端口契约。
	_ appports.ScratchpadMaintenanceStore = (*Store)(nil)
)

// scratchpadPlanScanRow mirrors one PostgreSQL scratchpad-plan row so transport decoding stays separate from domain-level plan semantics.
// scratchpadPlanScanRow 用于映射一条 PostgreSQL scratchpad 计划行，让传输层解码与领域层计划语义保持分离。
type scratchpadPlanScanRow struct {
	ID           uint64
	ProjectID    uint64
	UserID       uint64
	SessionKey   string
	PlanName     string
	PlanNameNorm string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// toDomain converts one PostgreSQL scratchpad-plan scan row into the isolated plan model consumed by the use case.
// toDomain 用于把一条 PostgreSQL scratchpad 计划扫描行转换成用例消费的隔离计划模型。
func (r scratchpadPlanScanRow) toDomain() logicdomain.ScratchpadPlanRecord {
	return logicdomain.ScratchpadPlanRecord{
		ID:           r.ID,
		ProjectID:    r.ProjectID,
		UserID:       r.UserID,
		SessionKey:   strings.TrimSpace(r.SessionKey),
		PlanName:     strings.TrimSpace(r.PlanName),
		PlanNameNorm: strings.TrimSpace(r.PlanNameNorm),
		CreatedAt:    r.CreatedAt.UTC(),
		UpdatedAt:    r.UpdatedAt.UTC(),
	}
}

// scratchpadNodeScanRow mirrors one PostgreSQL scratchpad-node row so deterministic key/value anchors can be reconstructed after query scanning.
// scratchpadNodeScanRow 用于映射一条 PostgreSQL scratchpad 节点行，让确定性 key/value 锚点能在查询扫描后统一重建。
type scratchpadNodeScanRow struct {
	ID        uint64
	PlanID    uint64
	ItemKey   string
	ItemValue string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// toItem converts one PostgreSQL scratchpad-node scan row into the AI-facing key/value anchor returned by DWM reads.
// toItem 用于把一条 PostgreSQL scratchpad 节点扫描行转换成 DWM 读取返回的 AI 面向 key/value 锚点。
func (r scratchpadNodeScanRow) toItem() logicdomain.ScratchpadItem {
	return logicdomain.ScratchpadItem{
		Key:   strings.TrimSpace(r.ItemKey),
		Value: strings.TrimSpace(r.ItemValue),
	}
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	row, found, err := s.loadScratchpadPlanWithQueryer(callCtx, s.pool, scope, false)
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, false, err
	}
	if !found {
		return logicdomain.ScratchpadPlanRecord{}, false, nil
	}
	return row.toDomain(), true, nil
}

// CreateScratchpadPlan inserts the canonical plan lock when the current isolated DWM scope does not have one yet, and reuses a same-plan concurrent winner when needed.
// CreateScratchpadPlan 用于在当前隔离 DWM 范围尚无计划锁时插入 canonical 计划锁，并在并发情况下复用同计划的先到者。
func (s *Store) CreateScratchpadPlan(ctx context.Context, scope logicdomain.ScratchpadScope, planName string, createdAt time.Time) (logicdomain.ScratchpadPlanRecord, error) {
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("begin postgres scratchpad-create tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	planName = strings.TrimSpace(planName)
	insertSQL := buildPostgresScratchpadPlanInsertSQL(s.scratchpadPlansTable())
	var created scratchpadPlanScanRow
	at := chooseNonZeroTime(createdAt, time.Now().UTC())
	if err := tx.QueryRow(callCtx, strings.TrimSpace(insertSQL), int64(scope.ProjectID), int64(scope.UserID), strings.TrimSpace(scope.SessionKey), planName, strings.ToLower(planName), at).Scan(
		&created.ID, &created.ProjectID, &created.UserID, &created.SessionKey, &created.PlanName, &created.PlanNameNorm, &created.CreatedAt, &created.UpdatedAt,
	); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("insert postgres scratchpad plan: %w", err)
		}
	} else {
		if err := tx.Commit(callCtx); err != nil {
			return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("commit postgres scratchpad-create tx: %w", err)
		}
		return created.toDomain(), nil
	}

	// When the insert lost a concurrent race, lock and reuse the persisted winner if it belongs to the same canonical plan.
	// 当插入在并发中败给先到者后，重新加锁读取持久化赢家；若仍属于同一 canonical 计划，则直接复用。
	row, found, err := s.loadScratchpadPlanWithQueryer(callCtx, tx, scope, true)
	if err != nil {
		return logicdomain.ScratchpadPlanRecord{}, err
	}
	if found {
		existing := row.toDomain()
		if strings.EqualFold(existing.PlanName, planName) {
			if err := tx.Commit(callCtx); err != nil {
				return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("commit postgres scratchpad-create tx: %w", err)
			}
			return existing, nil
		}
		return logicdomain.ScratchpadPlanRecord{}, logicdomain.ConflictError{
			Resource: "scratchpad_plan",
			Message:  fmt.Sprintf("scratchpad plan already exists for session %s", strings.TrimSpace(scope.SessionKey)),
		}
	}
	return logicdomain.ScratchpadPlanRecord{}, fmt.Errorf("postgres scratchpad plan insert race finished without a visible persisted row")
}

// buildPostgresScratchpadPlanInsertSQL renders the first-writer insert used by scratchpad plan creation, so concurrent creators can converge on one canonical session row.
// buildPostgresScratchpadPlanInsertSQL 用于渲染 scratchpad 计划创建时的先写入 SQL，让并发创建者收敛到同一条 canonical session 行。
func buildPostgresScratchpadPlanInsertSQL(table string) string {
	return fmt.Sprintf(`
INSERT INTO %s (project_id, user_id, session_key, plan_name, plan_name_norm, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (project_id, user_id, session_key) DO NOTHING
RETURNING id, project_id, user_id, session_key, plan_name, plan_name_norm, created_at, updated_at
`, strings.TrimSpace(table))
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("begin postgres scratchpad-upsert tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, strings.TrimSpace(item.Key))
	}
	existingKeys, err := s.loadScratchpadExistingKeysWithQueryer(callCtx, tx, planID, keys)
	if err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, err
	}
	insertedCount := 0
	updatedCount := 0
	upsertSQL := fmt.Sprintf(`
INSERT INTO %s (plan_id, item_key, item_value, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (plan_id, item_key) DO UPDATE SET
  item_value = EXCLUDED.item_value,
  updated_at = EXCLUDED.updated_at
`, s.scratchpadNodesTable())
	at := chooseNonZeroTime(updatedAt, time.Now().UTC())
	for _, item := range items {
		itemKey := strings.TrimSpace(item.Key)
		if _, exists := existingKeys[itemKey]; exists {
			updatedCount++
		} else {
			insertedCount++
		}
		if _, err := tx.Exec(callCtx, strings.TrimSpace(upsertSQL), int64(planID), itemKey, strings.TrimSpace(item.Value), at); err != nil {
			return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("upsert postgres scratchpad item %s: %w", itemKey, err)
		}
	}
	updatePlanSQL := fmt.Sprintf(`UPDATE %s SET updated_at = $1 WHERE id = $2`, s.scratchpadPlansTable())
	if _, err := tx.Exec(callCtx, updatePlanSQL, at, int64(planID)); err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("refresh postgres scratchpad plan timestamp: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ScratchpadUpsertPersistResult{}, fmt.Errorf("commit postgres scratchpad-upsert tx: %w", err)
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("begin postgres scratchpad-delete tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	keyList := normalizeStringList(keys)
	deletedCount, err := s.countScratchpadNodesByKeysWithQueryer(callCtx, tx, planID, keyList)
	if err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, err
	}
	if deletedCount == 0 {
		remainingCount, err := s.countScratchpadNodesWithQueryer(callCtx, tx, planID)
		if err != nil {
			return logicdomain.ScratchpadDeletePersistResult{}, err
		}
		if err := tx.Commit(callCtx); err != nil {
			return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("commit postgres scratchpad-delete no-op tx: %w", err)
		}
		return logicdomain.ScratchpadDeletePersistResult{DeletedCount: 0, RemainingCount: remainingCount}, nil
	}
	deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE plan_id = $1 AND item_key = ANY($2)`, s.scratchpadNodesTable())
	if _, err := tx.Exec(callCtx, deleteSQL, int64(planID), keyList); err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("delete postgres scratchpad items: %w", err)
	}
	remainingCount, err := s.countScratchpadNodesWithQueryer(callCtx, tx, planID)
	if err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, err
	}
	updatePlanSQL := fmt.Sprintf(`UPDATE %s SET updated_at = $1 WHERE id = $2`, s.scratchpadPlansTable())
	if _, err := tx.Exec(callCtx, updatePlanSQL, chooseNonZeroTime(updatedAt, time.Now().UTC()), int64(planID)); err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("refresh postgres scratchpad plan after delete: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ScratchpadDeletePersistResult{}, fmt.Errorf("commit postgres scratchpad-delete tx: %w", err)
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	rows, err := s.listScratchpadItemsWithQueryer(callCtx, s.pool, planID, keys)
	if err != nil {
		return nil, err
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("begin postgres scratchpad-clean tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	row, found, err := s.loadScratchpadPlanWithQueryer(callCtx, tx, scope, true)
	if err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, err
	}
	if !found {
		if err := tx.Commit(callCtx); err != nil {
			return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("commit postgres scratchpad-clean empty tx: %w", err)
		}
		return logicdomain.ScratchpadCleanPersistResult{}, nil
	}
	plan := row.toDomain()
	nodeCount, err := s.countScratchpadNodesWithQueryer(callCtx, tx, plan.ID)
	if err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, err
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE plan_id = $1`, s.scratchpadNodesTable()), int64(plan.ID)); err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("delete postgres scratchpad nodes during clean: %w", err)
	}
	if _, err := tx.Exec(callCtx, fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.scratchpadPlansTable()), int64(plan.ID)); err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("delete postgres scratchpad plan during clean: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ScratchpadCleanPersistResult{}, fmt.Errorf("commit postgres scratchpad-clean tx: %w", err)
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
	callCtx, cancel := s.queryContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(callCtx)
	if err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("begin postgres scratchpad-gc tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	planIDs, err := s.claimExpiredScratchpadPlanIDs(callCtx, tx, before, limit)
	if err != nil {
		return logicdomain.ScratchpadGCResult{}, err
	}
	if len(planIDs) == 0 {
		if err := tx.Commit(callCtx); err != nil {
			return logicdomain.ScratchpadGCResult{}, fmt.Errorf("commit postgres scratchpad-gc empty tx: %w", err)
		}
		return logicdomain.ScratchpadGCResult{}, nil
	}
	nodeCount, err := s.countScratchpadNodesByPlanIDsWithQueryer(callCtx, tx, planIDs)
	if err != nil {
		return logicdomain.ScratchpadGCResult{}, err
	}
	deleteNodesSQL := fmt.Sprintf(`DELETE FROM %s WHERE plan_id = ANY($1)`, s.scratchpadNodesTable())
	if _, err := tx.Exec(callCtx, deleteNodesSQL, toInt64List(planIDs)); err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("delete postgres expired scratchpad nodes: %w", err)
	}
	deletePlansSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, s.scratchpadPlansTable())
	if _, err := tx.Exec(callCtx, deletePlansSQL, toInt64List(planIDs)); err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("delete postgres expired scratchpad plans: %w", err)
	}
	if err := tx.Commit(callCtx); err != nil {
		return logicdomain.ScratchpadGCResult{}, fmt.Errorf("commit postgres scratchpad-gc tx: %w", err)
	}
	return logicdomain.ScratchpadGCResult{
		DeletedPlanCount: len(planIDs),
		DeletedNodeCount: nodeCount,
	}, nil
}

// loadScratchpadPlanWithQueryer loads one scratchpad plan row through the shared pool/transaction query abstraction, optionally locking it for follow-up mutations.
// loadScratchpadPlanWithQueryer 用于通过共享连接池/事务查询抽象加载一条 scratchpad 计划行，并按需为后续变更加锁。
func (s *Store) loadScratchpadPlanWithQueryer(ctx context.Context, q profileQueryer, scope logicdomain.ScratchpadScope, forUpdate bool) (scratchpadPlanScanRow, bool, error) {
	sqlText := fmt.Sprintf(`
SELECT id, project_id, user_id, session_key, plan_name, plan_name_norm, created_at, updated_at
FROM %s
WHERE project_id = $1 AND user_id = $2 AND session_key = $3
LIMIT 1
`, s.scratchpadPlansTable())
	if forUpdate {
		sqlText = strings.TrimSpace(sqlText) + ` FOR UPDATE`
	}
	var row scratchpadPlanScanRow
	err := q.QueryRow(ctx, strings.TrimSpace(sqlText), int64(scope.ProjectID), int64(scope.UserID), strings.TrimSpace(scope.SessionKey)).Scan(
		&row.ID, &row.ProjectID, &row.UserID, &row.SessionKey, &row.PlanName, &row.PlanNameNorm, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return scratchpadPlanScanRow{}, false, nil
		}
		return scratchpadPlanScanRow{}, false, fmt.Errorf("load postgres scratchpad plan: %w", err)
	}
	return row, true, nil
}

// listScratchpadItemsWithQueryer loads all or filtered scratchpad nodes through the shared pool/transaction query abstraction.
// listScratchpadItemsWithQueryer 用于通过共享连接池/事务查询抽象加载全部或过滤后的 scratchpad 节点。
func (s *Store) listScratchpadItemsWithQueryer(ctx context.Context, q profileQueryer, planID uint64, keys []string) ([]scratchpadNodeScanRow, error) {
	sqlText := fmt.Sprintf(`
SELECT id, plan_id, item_key, item_value, created_at, updated_at
FROM %s
WHERE plan_id = $1
`, s.scratchpadNodesTable())
	args := []any{int64(planID)}
	if len(keys) > 0 {
		sqlText += ` AND item_key = ANY($2)`
		args = append(args, normalizeStringList(keys))
	}
	sqlText += ` ORDER BY item_key ASC, id ASC`
	rows, err := q.Query(ctx, strings.TrimSpace(sqlText), args...)
	if err != nil {
		return nil, fmt.Errorf("query postgres scratchpad items: %w", err)
	}
	defer rows.Close()
	out := make([]scratchpadNodeScanRow, 0)
	for rows.Next() {
		var row scratchpadNodeScanRow
		if err := rows.Scan(&row.ID, &row.PlanID, &row.ItemKey, &row.ItemValue, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan postgres scratchpad item row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres scratchpad item rows: %w", err)
	}
	return out, nil
}

// loadScratchpadExistingKeysWithQueryer loads the subset of keys that already exist under one plan so upsert can report inserted vs updated counts deterministically.
// loadScratchpadExistingKeysWithQueryer 用于加载某个计划下已存在的 key 子集，让 upsert 能确定性地区分 inserted 与 updated 计数。
func (s *Store) loadScratchpadExistingKeysWithQueryer(ctx context.Context, q profileQueryer, planID uint64, keys []string) (map[string]struct{}, error) {
	rows, err := s.listScratchpadItemsWithQueryer(ctx, q, planID, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		out[strings.TrimSpace(row.ItemKey)] = struct{}{}
	}
	return out, nil
}

// countScratchpadNodesWithQueryer returns how many nodes currently remain under one plan.
// countScratchpadNodesWithQueryer 用于返回某个计划下当前剩余节点数量。
func (s *Store) countScratchpadNodesWithQueryer(ctx context.Context, q profileQueryer, planID uint64) (int, error) {
	sqlText := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE plan_id = $1`, s.scratchpadNodesTable())
	var count int
	if err := q.QueryRow(ctx, sqlText, int64(planID)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count postgres scratchpad nodes: %w", err)
	}
	return count, nil
}

// countScratchpadNodesByKeysWithQueryer returns how many of the requested keys currently exist under one plan.
// countScratchpadNodesByKeysWithQueryer 用于返回某个计划下当前命中的请求 key 数量。
func (s *Store) countScratchpadNodesByKeysWithQueryer(ctx context.Context, q profileQueryer, planID uint64, keys []string) (int, error) {
	keyList := normalizeStringList(keys)
	if len(keyList) == 0 {
		return 0, nil
	}
	sqlText := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE plan_id = $1 AND item_key = ANY($2)`, s.scratchpadNodesTable())
	var count int
	if err := q.QueryRow(ctx, sqlText, int64(planID), keyList).Scan(&count); err != nil {
		return 0, fmt.Errorf("count postgres scratchpad nodes by keys: %w", err)
	}
	return count, nil
}

// countScratchpadNodesByPlanIDsWithQueryer returns how many nodes belong to the selected expired plan set.
// countScratchpadNodesByPlanIDsWithQueryer 用于返回所选过期计划集合下的节点数量。
func (s *Store) countScratchpadNodesByPlanIDsWithQueryer(ctx context.Context, q profileQueryer, planIDs []uint64) (int, error) {
	ids := toInt64List(planIDs)
	if len(ids) == 0 {
		return 0, nil
	}
	sqlText := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE plan_id = ANY($1)`, s.scratchpadNodesTable())
	var count int
	if err := q.QueryRow(ctx, sqlText, ids).Scan(&count); err != nil {
		return 0, fmt.Errorf("count postgres scratchpad nodes by plan ids: %w", err)
	}
	return count, nil
}

// claimExpiredScratchpadPlanIDs locks a bounded ordered batch of expired plans so concurrent maintenance workers do not hard-delete the same scope twice.
// claimExpiredScratchpadPlanIDs 用于锁定一批有界且有序的过期计划，避免并发维护工作器重复硬删同一范围。
func (s *Store) claimExpiredScratchpadPlanIDs(ctx context.Context, q profileQueryer, before time.Time, limit int) ([]uint64, error) {
	sqlText := fmt.Sprintf(`
SELECT id
FROM %s
WHERE updated_at <= $1
ORDER BY updated_at ASC, id ASC
LIMIT $2
FOR UPDATE SKIP LOCKED
`, s.scratchpadPlansTable())
	rows, err := q.Query(ctx, strings.TrimSpace(sqlText), before.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim postgres expired scratchpad plans: %w", err)
	}
	defer rows.Close()
	out := make([]uint64, 0)
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan postgres expired scratchpad plan id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres expired scratchpad plans: %w", err)
	}
	return out, nil
}
