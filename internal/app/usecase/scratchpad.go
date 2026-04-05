// scratchpad.go implements the isolated deterministic working-memory flow used by AI agents to preserve short-lived task anchors outside the main memory/session pipeline.
// scratchpad.go 用于实现隔离式确定性工作记忆流程，让 AI Agent 能在主记忆/主 session 管线之外保留短期任务锚点。
package usecase

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	// scratchpadPlanNameMaxLen keeps one canonical plan name concise enough to remain stable inside prompts and tool returns.
	// scratchpadPlanNameMaxLen 用于限制 canonical 计划名长度，让其在提示词和工具返回里保持稳定且紧凑。
	scratchpadPlanNameMaxLen = 128

	// scratchpadItemKeyMaxLen keeps one scratchpad key short enough to act as a deterministic anchor instead of turning into another free-form paragraph.
	// scratchpadItemKeyMaxLen 用于限制 scratchpad key 的长度，避免它从确定性锚点退化成另一段自由文本。
	scratchpadItemKeyMaxLen = 128

	// scratchpadItemValueMaxLen keeps one scratchpad value bounded so temporary working memory remains lightweight and AI-facing.
	// scratchpadItemValueMaxLen 用于限制 scratchpad value 的长度，确保临时工作记忆保持轻量且面向 AI。
	scratchpadItemValueMaxLen = 16000

	// scratchpadBatchItemLimit bounds one upsert batch so the working-memory tool stays deterministic and cheap.
	// scratchpadBatchItemLimit 用于限制一次 upsert 批量大小，保证工作记忆工具保持确定性和低成本。
	scratchpadBatchItemLimit = 32
)

const (
	// scratchpadNoPlanMessage guides the caller to create the first record instead of silently fabricating a plan lock during delete.
	// scratchpadNoPlanMessage 用于在 delete 场景下引导调用方先创建首条记录，而不是静默伪造计划锁。
	scratchpadNoPlanMessage = "No scratchpad plan exists for the current session. Create records first."

	// scratchpadNoRecordsMessage explains that the current session simply has no scratchpad payload yet, which is not an error condition.
	// scratchpadNoRecordsMessage 用于说明当前 session 只是暂时没有 scratchpad 数据，这不是错误场景。
	scratchpadNoRecordsMessage = "No scratchpad records found for the current session."

	// scratchpadClearedMessage confirms that the current session scratchpad history has been removed deterministically.
	// scratchpadClearedMessage 用于确认当前 session 的 scratchpad 历史已经被确定性清空。
	scratchpadClearedMessage = "Scratchpad history has been cleared."

	// scratchpadAlreadyEmptyMessage confirms that a clean call observed no scratchpad payload and therefore had nothing left to delete.
	// scratchpadAlreadyEmptyMessage 用于确认 clean 调用观察到当前没有 scratchpad 数据，因此无需继续删除。
	scratchpadAlreadyEmptyMessage = "The current scratchpad is already empty."
)

// ScratchpadExecutor exposes the isolated DWM CRUD surface consumed by the gRPC transport layer.
// ScratchpadExecutor 用于暴露 gRPC 传输层消费的隔离 DWM CRUD 能力面。
type ScratchpadExecutor interface {
	Upsert(ctx context.Context, cmd ScratchpadUpsertCommand) (logicdomain.ScratchpadMutationResult, error)
	Delete(ctx context.Context, cmd ScratchpadDeleteCommand) (logicdomain.ScratchpadMutationResult, error)
	Get(ctx context.Context, query ScratchpadGetQuery) (logicdomain.ScratchpadQueryResult, error)
	Clean(ctx context.Context, cmd ScratchpadCleanCommand) (logicdomain.ScratchpadMutationResult, error)
}

// ScratchpadUpsertCommand carries the deterministic scope, plan name, and key/value anchors that should be persisted into DWM.
// ScratchpadUpsertCommand 用于承载应被持久化到 DWM 的确定性范围、计划名和 key/value 锚点。
type ScratchpadUpsertCommand struct {
	Scope    logicdomain.ScratchpadScope
	PlanName string
	Items    []logicdomain.ScratchpadItem
}

// ScratchpadDeleteCommand carries the deterministic scope, plan name, and keys that should be removed from the isolated scratchpad.
// ScratchpadDeleteCommand 用于承载应从隔离 scratchpad 中删除的确定性范围、计划名和 key 集合。
type ScratchpadDeleteCommand struct {
	Scope    logicdomain.ScratchpadScope
	PlanName string
	Keys     []string
}

// ScratchpadGetQuery carries the deterministic scope and optional single-key filter used to reload scratchpad anchors after context compaction.
// ScratchpadGetQuery 用于承载上下文压缩后重新加载 scratchpad 锚点时需要的确定性范围与可选单键过滤条件。
type ScratchpadGetQuery struct {
	Scope logicdomain.ScratchpadScope
	Key   string
}

// ScratchpadCleanCommand carries the deterministic scope whose working-memory anchors should be deleted explicitly at task end.
// ScratchpadCleanCommand 用于承载任务结束时需要显式删除其工作记忆锚点的确定性范围。
type ScratchpadCleanCommand struct {
	Scope logicdomain.ScratchpadScope
}

// ScratchpadUseCase orchestrates the isolated DWM plan guard, item normalization, and relational persistence without touching the main memory/session chain.
// ScratchpadUseCase 用于编排隔离 DWM 的计划守卫、item 归一化和关系持久化，同时不触碰主记忆/主 session 链。
type ScratchpadUseCase struct {
	store appports.ScratchpadStore
}

// NewScratchpadUseCase creates one isolated DWM use case.
// NewScratchpadUseCase 用于创建一个隔离 DWM 用例实例。
func NewScratchpadUseCase(store appports.ScratchpadStore) *ScratchpadUseCase {
	return &ScratchpadUseCase{store: store}
}

// Upsert validates the scope, enforces the canonical plan guard, and atomically inserts or updates one batch of deterministic scratchpad anchors.
// Upsert 用于校验范围、执行 canonical 计划守卫，并以整批原子语义插入或更新一批确定性的 scratchpad 锚点。
func (u *ScratchpadUseCase) Upsert(ctx context.Context, cmd ScratchpadUpsertCommand) (logicdomain.ScratchpadMutationResult, error) {
	store, err := u.requireStore()
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := validateScratchpadScope(cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := validatePlanName(cmd.PlanName); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	items, err := normalizeScratchpadItems(cmd.Items)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := store.EnsureScratchpadScope(ctx, cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}

	// Load the current plan lock first so the write path can deterministically block cross-task contamination before any row mutation starts.
	// 先读取当前计划锁，再开始写入，这样写路径就能在任何行变更前确定性拦截跨任务污染。
	plan, found, err := store.LoadScratchpadPlan(ctx, cmd.Scope)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	warningPrefix, guardResult, err := checkScratchpadPlanGuard(found, plan, cmd.PlanName)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if guardResult != nil {
		return *guardResult, nil
	}

	now := time.Now().UTC()
	if !found {
		plan, err = store.CreateScratchpadPlan(ctx, cmd.Scope, strings.TrimSpace(cmd.PlanName), now)
		if err != nil {
			return logicdomain.ScratchpadMutationResult{}, err
		}
	}
	persistResult, err := store.UpsertScratchpadItems(ctx, plan.ID, items, now)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	total := persistResult.InsertedCount + persistResult.UpdatedCount
	return logicdomain.ScratchpadMutationResult{
		Status:        logicdomain.ScratchpadStatusSuccess,
		Message:       withScratchpadWarning(warningPrefix, fmt.Sprintf("Upserted %d scratchpad record(s).", total)),
		PlanName:      plan.PlanName,
		UpdatedAt:     now,
		AffectedCount: total,
		InsertedCount: persistResult.InsertedCount,
		UpdatedCount:  persistResult.UpdatedCount,
	}, nil
}

// Delete validates the scope, enforces the canonical plan guard when a plan exists, and atomically removes one or more keys without fabricating a new plan lock.
// Delete 用于校验范围、在计划存在时执行 canonical 计划守卫，并以整批原子语义删除一个或多个 key，同时不伪造新计划锁。
func (u *ScratchpadUseCase) Delete(ctx context.Context, cmd ScratchpadDeleteCommand) (logicdomain.ScratchpadMutationResult, error) {
	store, err := u.requireStore()
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := validateScratchpadScope(cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := validatePlanName(cmd.PlanName); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	keys, err := normalizeScratchpadKeys(cmd.Keys)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := store.EnsureScratchpadScope(ctx, cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}

	// Delete must never lock a new plan, so the empty-session branch returns one guided success message immediately.
	// Delete 绝不能锁定新计划，因此空 session 分支必须立即返回带引导语的成功消息。
	plan, found, err := store.LoadScratchpadPlan(ctx, cmd.Scope)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if !found {
		return logicdomain.ScratchpadMutationResult{
			Status:        logicdomain.ScratchpadStatusSuccess,
			Message:       scratchpadNoPlanMessage,
			AffectedCount: 0,
		}, nil
	}
	warningPrefix, guardResult, err := checkScratchpadPlanGuard(found, plan, cmd.PlanName)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if guardResult != nil {
		return *guardResult, nil
	}

	now := time.Now().UTC()
	persistResult, err := store.DeleteScratchpadItems(ctx, plan.ID, keys, now)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if persistResult.DeletedCount == 0 {
		return logicdomain.ScratchpadMutationResult{
			Status:        logicdomain.ScratchpadStatusSuccess,
			Message:       withScratchpadWarning(warningPrefix, "No matching scratchpad records were found."),
			PlanName:      plan.PlanName,
			UpdatedAt:     plan.UpdatedAt,
			AffectedCount: 0,
		}, nil
	}
	return logicdomain.ScratchpadMutationResult{
		Status:        logicdomain.ScratchpadStatusSuccess,
		Message:       withScratchpadWarning(warningPrefix, fmt.Sprintf("Deleted %d scratchpad record(s).", persistResult.DeletedCount)),
		PlanName:      plan.PlanName,
		UpdatedAt:     now,
		AffectedCount: persistResult.DeletedCount,
	}, nil
}

// Get reloads either the full isolated scratchpad state or one selected key and treats empty results as a successful no-data response instead of an error.
// Get 用于重新加载完整隔离 scratchpad 状态或单个选定 key，并把空结果视为成功的无数据响应，而不是错误。
func (u *ScratchpadUseCase) Get(ctx context.Context, query ScratchpadGetQuery) (logicdomain.ScratchpadQueryResult, error) {
	store, err := u.requireStore()
	if err != nil {
		return logicdomain.ScratchpadQueryResult{}, err
	}
	if err := validateScratchpadScope(query.Scope); err != nil {
		return logicdomain.ScratchpadQueryResult{}, err
	}
	query.Key = strings.TrimSpace(query.Key)
	if query.Key != "" {
		if err := requireScratchpadKey("key", query.Key); err != nil {
			return logicdomain.ScratchpadQueryResult{}, err
		}
	}
	if err := store.EnsureScratchpadScope(ctx, query.Scope); err != nil {
		return logicdomain.ScratchpadQueryResult{}, err
	}
	plan, found, err := store.LoadScratchpadPlan(ctx, query.Scope)
	if err != nil {
		return logicdomain.ScratchpadQueryResult{}, err
	}
	if !found {
		return logicdomain.ScratchpadQueryResult{
			Status:    logicdomain.ScratchpadStatusSuccess,
			Message:   scratchpadNoRecordsMessage,
			ItemCount: 0,
			Items:     []logicdomain.ScratchpadItem{},
		}, nil
	}

	keys := []string(nil)
	if query.Key != "" {
		keys = []string{query.Key}
	}
	items, err := store.ListScratchpadItems(ctx, plan.ID, keys)
	if err != nil {
		return logicdomain.ScratchpadQueryResult{}, err
	}
	if len(items) == 0 {
		return logicdomain.ScratchpadQueryResult{
			Status:    logicdomain.ScratchpadStatusSuccess,
			Message:   scratchpadNoRecordsMessage,
			PlanName:  plan.PlanName,
			UpdatedAt: plan.UpdatedAt,
			ItemCount: 0,
			Items:     []logicdomain.ScratchpadItem{},
		}, nil
	}
	return logicdomain.ScratchpadQueryResult{
		Status:    logicdomain.ScratchpadStatusSuccess,
		Message:   fmt.Sprintf("Retrieved %d scratchpad record(s).", len(items)),
		PlanName:  plan.PlanName,
		UpdatedAt: plan.UpdatedAt,
		ItemCount: len(items),
		Items:     items,
	}, nil
}

// Clean deletes the whole isolated scratchpad scope explicitly and keeps empty-session calls idempotent.
// Clean 用于显式删除整个隔离 scratchpad 范围，并保持空 session 调用的幂等语义。
func (u *ScratchpadUseCase) Clean(ctx context.Context, cmd ScratchpadCleanCommand) (logicdomain.ScratchpadMutationResult, error) {
	store, err := u.requireStore()
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := validateScratchpadScope(cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if err := store.EnsureScratchpadScope(ctx, cmd.Scope); err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	result, err := store.CleanScratchpad(ctx, cmd.Scope)
	if err != nil {
		return logicdomain.ScratchpadMutationResult{}, err
	}
	if !result.HadPlan {
		return logicdomain.ScratchpadMutationResult{
			Status:        logicdomain.ScratchpadStatusSuccess,
			Message:       scratchpadAlreadyEmptyMessage,
			AffectedCount: 0,
		}, nil
	}
	return logicdomain.ScratchpadMutationResult{
		Status:        logicdomain.ScratchpadStatusSuccess,
		Message:       scratchpadClearedMessage,
		AffectedCount: result.DeletedNodeCount,
	}, nil
}

// requireStore fails fast when the isolated scratchpad use case is called through a nil or partially-wired receiver.
// requireStore 用于在隔离 scratchpad 用例被 nil 或部分装配的接收者调用时快速失败。
func (u *ScratchpadUseCase) requireStore() (appports.ScratchpadStore, error) {
	if u == nil || u.store == nil {
		return nil, fmt.Errorf("scratchpad store is nil")
	}
	return u.store, nil
}

// checkScratchpadPlanGuard compares the stored canonical plan lock with the caller-provided plan name and either returns one drift warning prefix or one deterministic block result.
// checkScratchpadPlanGuard 用于比较持久化 canonical 计划锁与调用方传入的计划名，并返回格式漂移警告前缀或确定性的拦截结果。
func checkScratchpadPlanGuard(found bool, plan logicdomain.ScratchpadPlanRecord, inputPlanName string) (string, *logicdomain.ScratchpadMutationResult, error) {
	if !found {
		return "", nil, nil
	}
	canonical := strings.TrimSpace(plan.PlanName)
	input := strings.TrimSpace(inputPlanName)
	canonicalNorm := strings.ToLower(canonical)
	inputNorm := strings.ToLower(input)
	if canonicalNorm != inputNorm {
		result := logicdomain.ScratchpadMutationResult{
			Status:  logicdomain.ScratchpadStatusFailed,
			Message: fmt.Sprintf("The input plan name does not match the current scratchpad plan. Check whether the plan name is misspelled or call Clean before switching to a new plan. Current plan: %s. Input plan: %s.", canonical, input),
		}
		return "", &result, nil
	}
	if canonical != input {
		return fmt.Sprintf("[FORMAT DRIFT WARNING] The canonical plan name is %s. Align future calls to this exact spelling.", canonical), nil, nil
	}
	return "", nil, nil
}

// validateScratchpadScope rejects incomplete deterministic scope coordinates before the isolated DWM workflow touches the database.
// validateScratchpadScope 用于在隔离 DWM 工作流触碰数据库前，拒绝不完整的确定性范围坐标。
func validateScratchpadScope(scope logicdomain.ScratchpadScope) error {
	if scope.ProjectID == 0 {
		return logicdomain.ValidationError{Field: "project_id", Message: "must be a numeric id"}
	}
	if scope.UserID == 0 {
		return logicdomain.ValidationError{Field: "user_id", Message: "must be a numeric id"}
	}
	sessionKey := strings.TrimSpace(scope.SessionKey)
	if sessionKey == "" {
		return logicdomain.ValidationError{Field: "session_id", Message: "is required"}
	}
	if len(sessionKey) > 128 {
		return logicdomain.ValidationError{Field: "session_id", Message: "must be <= 128 characters"}
	}
	return nil
}

// validatePlanName rejects blank or oversized plan names before plan-guard logic runs.
// validatePlanName 用于在计划守卫逻辑开始前拒绝空白或超长的计划名。
func validatePlanName(planName string) error {
	planName = strings.TrimSpace(planName)
	if planName == "" {
		return logicdomain.ValidationError{Field: "plan_name", Message: "is required"}
	}
	if len(planName) > scratchpadPlanNameMaxLen {
		return logicdomain.ValidationError{Field: "plan_name", Message: fmt.Sprintf("must be <= %d characters", scratchpadPlanNameMaxLen)}
	}
	return nil
}

// normalizeScratchpadItems trims, validates, deduplicates, and stably orders batch items so deterministic writes never depend on request-order accidents.
// normalizeScratchpadItems 用于裁剪、校验、去重并稳定排序批量 item，避免确定性写入依赖请求顺序偶然性。
func normalizeScratchpadItems(items []logicdomain.ScratchpadItem) ([]logicdomain.ScratchpadItem, error) {
	if len(items) == 0 {
		return nil, logicdomain.ValidationError{Field: "items", Message: "must contain at least one item"}
	}
	if len(items) > scratchpadBatchItemLimit {
		return nil, logicdomain.ValidationError{Field: "items", Message: fmt.Sprintf("must contain <= %d items", scratchpadBatchItemLimit)}
	}

	// Keep the last value for the same key inside one batch so callers can send incremental partial updates without needing a prior local dedupe pass.
	// 对同一批次里的重复 key 保留最后一个值，让调用方无需在本地先做一轮额外去重也能表达增量覆盖。
	itemMap := make(map[string]string, len(items))
	for idx, item := range items {
		key := strings.TrimSpace(item.Key)
		value := strings.TrimSpace(item.Value)
		if err := requireScratchpadKey(fmt.Sprintf("items[%d].key", idx), key); err != nil {
			return nil, err
		}
		if value == "" {
			return nil, logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].value", idx), Message: "is required"}
		}
		if len(value) > scratchpadItemValueMaxLen {
			return nil, logicdomain.ValidationError{Field: fmt.Sprintf("items[%d].value", idx), Message: fmt.Sprintf("must be <= %d characters", scratchpadItemValueMaxLen)}
		}
		itemMap[key] = value
	}
	out := make([]logicdomain.ScratchpadItem, 0, len(itemMap))
	for key, value := range itemMap {
		out = append(out, logicdomain.ScratchpadItem{Key: key, Value: value})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// normalizeScratchpadKeys trims, validates, deduplicates, and stably orders delete keys so plan-guarded deletes remain deterministic.
// normalizeScratchpadKeys 用于裁剪、校验、去重并稳定排序删除 key，让受计划守卫保护的删除操作保持确定性。
func normalizeScratchpadKeys(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, logicdomain.ValidationError{Field: "keys", Message: "must contain at least one key"}
	}
	keySet := make(map[string]struct{}, len(keys))
	for idx, key := range keys {
		key = strings.TrimSpace(key)
		if err := requireScratchpadKey(fmt.Sprintf("keys[%d]", idx), key); err != nil {
			return nil, err
		}
		keySet[key] = struct{}{}
	}
	out := make([]string, 0, len(keySet))
	for key := range keySet {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

// requireScratchpadKey rejects blank or oversized item keys before they reach storage.
// requireScratchpadKey 用于在 item key 进入存储层前拒绝空白或超长值。
func requireScratchpadKey(field, key string) error {
	if strings.TrimSpace(key) == "" {
		return logicdomain.ValidationError{Field: field, Message: "is required"}
	}
	if len(key) > scratchpadItemKeyMaxLen {
		return logicdomain.ValidationError{Field: field, Message: fmt.Sprintf("must be <= %d characters", scratchpadItemKeyMaxLen)}
	}
	return nil
}

// withScratchpadWarning prefixes one success-path message with the canonical format-drift warning when the caller was forgiven but must self-correct later.
// withScratchpadWarning 用于在调用方被放行但必须后续自我纠正时，把 canonical 格式漂移警告前置到成功路径消息之前。
func withScratchpadWarning(warning, message string) string {
	warning = strings.TrimSpace(warning)
	message = strings.TrimSpace(message)
	if warning == "" {
		return message
	}
	if message == "" {
		return warning
	}
	return warning + " " + message
}
