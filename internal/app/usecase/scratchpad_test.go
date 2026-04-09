// scratchpad_test.go verifies the isolated deterministic working-memory use case keeps plan guards, no-data semantics, and format-drift handling aligned with the external DWM contract.
// scratchpad_test.go 用于验证隔离确定性工作记忆用例会把计划守卫、无数据语义和格式漂移处理保持在外部 DWM 契约要求之内。
package usecase

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	appports "github.com/openvulcan/vmm/internal/app/ports"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// fakeScratchpadStore keeps one tiny in-memory scratchpad image so use-case tests can assert plan-guard behavior without touching a real database.
// fakeScratchpadStore 用于维护一个极小的内存 scratchpad 视图，让用例测试能断言计划守卫行为，而无需触碰真实数据库。
type fakeScratchpadStore struct {
	scopeErr error
	plan     logicdomain.ScratchpadPlanRecord
	hasPlan  bool
	items    map[string]string

	createdPlanName string
	upsertPlanID    uint64
	upsertItems     []logicdomain.ScratchpadItem
	deletePlanID    uint64
	deleteKeys      []string
	cleanCalled     bool
}

// EnsureScratchpadScope records scope validation and returns the configured fake error when needed.
// EnsureScratchpadScope 用于记录范围校验，并在需要时返回预设的 fake 错误。
func (f *fakeScratchpadStore) EnsureScratchpadScope(context.Context, logicdomain.ScratchpadScope) error {
	return f.scopeErr
}

// LoadScratchpadPlan returns the configured canonical plan lock.
// LoadScratchpadPlan 用于返回预设的 canonical 计划锁。
func (f *fakeScratchpadStore) LoadScratchpadPlan(context.Context, logicdomain.ScratchpadScope) (logicdomain.ScratchpadPlanRecord, bool, error) {
	return f.plan, f.hasPlan, nil
}

// CreateScratchpadPlan creates one fake canonical plan row for first-write tests.
// CreateScratchpadPlan 用于为首写场景创建一条 fake canonical 计划行。
func (f *fakeScratchpadStore) CreateScratchpadPlan(_ context.Context, scope logicdomain.ScratchpadScope, planName string, createdAt time.Time) (logicdomain.ScratchpadPlanRecord, error) {
	f.createdPlanName = strings.TrimSpace(planName)
	f.hasPlan = true
	f.plan = logicdomain.ScratchpadPlanRecord{
		ID:           101,
		ProjectID:    scope.ProjectID,
		UserID:       scope.UserID,
		SessionKey:   scope.SessionKey,
		PlanName:     strings.TrimSpace(planName),
		PlanNameNorm: strings.ToLower(strings.TrimSpace(planName)),
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	return f.plan, nil
}

// UpsertScratchpadItems records the write payload and mirrors it into the fake in-memory item map.
// UpsertScratchpadItems 用于记录写入载荷，并把它镜像进 fake 内存 item map。
func (f *fakeScratchpadStore) UpsertScratchpadItems(_ context.Context, planID uint64, items []logicdomain.ScratchpadItem, _ time.Time) (logicdomain.ScratchpadUpsertPersistResult, error) {
	f.upsertPlanID = planID
	f.upsertItems = append([]logicdomain.ScratchpadItem(nil), items...)
	if f.items == nil {
		f.items = make(map[string]string, len(items))
	}
	for _, item := range items {
		f.items[item.Key] = item.Value
	}
	return logicdomain.ScratchpadUpsertPersistResult{
		InsertedCount: len(items),
	}, nil
}

// DeleteScratchpadItems records the delete payload and removes any matching keys from the fake in-memory item map.
// DeleteScratchpadItems 用于记录删除载荷，并从 fake 内存 item map 中移除命中的 key。
func (f *fakeScratchpadStore) DeleteScratchpadItems(_ context.Context, planID uint64, keys []string, _ time.Time) (logicdomain.ScratchpadDeletePersistResult, error) {
	f.deletePlanID = planID
	f.deleteKeys = append([]string(nil), keys...)
	deleted := 0
	for _, key := range keys {
		if _, ok := f.items[key]; ok {
			delete(f.items, key)
			deleted++
		}
	}
	return logicdomain.ScratchpadDeletePersistResult{
		DeletedCount:   deleted,
		RemainingCount: len(f.items),
	}, nil
}

// ListScratchpadItems returns either all fake items or one filtered subset in stable key order.
// ListScratchpadItems 用于返回全部 fake items，或一个按 key 过滤后的稳定子集。
func (f *fakeScratchpadStore) ListScratchpadItems(_ context.Context, _ uint64, keys []string) ([]logicdomain.ScratchpadItem, error) {
	if len(f.items) == 0 {
		return nil, nil
	}
	out := make([]logicdomain.ScratchpadItem, 0, len(f.items))
	if len(keys) == 0 {
		sortedKeys := make([]string, 0, len(f.items))
		for key := range f.items {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		for _, key := range sortedKeys {
			value := f.items[key]
			out = append(out, logicdomain.ScratchpadItem{Key: key, Value: value})
		}
		return out, nil
	}
	sortedKeys := append([]string(nil), keys...)
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		if value, ok := f.items[key]; ok {
			out = append(out, logicdomain.ScratchpadItem{Key: key, Value: value})
		}
	}
	return out, nil
}

// ListScratchpadKeys returns the fake key set in stable sorted order so list-keys callers can rebuild the host-visible anchor catalog without touching values.
// ListScratchpadKeys 用于按稳定排序返回 fake key 集合，让 list-keys 调用方无需触碰 value 也能重建宿主可见锚点目录。
func (f *fakeScratchpadStore) ListScratchpadKeys(_ context.Context, _ uint64) ([]string, error) {
	if len(f.items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(f.items))
	for key := range f.items {
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

// CleanScratchpad records one full-scope clear and drops the fake plan plus all items.
// CleanScratchpad 用于记录一次整范围清空，并删除 fake 计划与全部 items。
func (f *fakeScratchpadStore) CleanScratchpad(context.Context, logicdomain.ScratchpadScope) (logicdomain.ScratchpadCleanPersistResult, error) {
	f.cleanCalled = true
	if !f.hasPlan {
		return logicdomain.ScratchpadCleanPersistResult{}, nil
	}
	deletedNodeCount := len(f.items)
	f.items = map[string]string{}
	f.hasPlan = false
	return logicdomain.ScratchpadCleanPersistResult{
		HadPlan:          true,
		DeletedPlanCount: 1,
		DeletedNodeCount: deletedNodeCount,
	}, nil
}

var _ appports.ScratchpadStore = (*fakeScratchpadStore)(nil)

// TestScratchpadUseCaseDeleteWithoutPlanReturnsGuidance verifies delete never fabricates a new plan lock and instead returns the guided success message when the scope is still empty.
// TestScratchpadUseCaseDeleteWithoutPlanReturnsGuidance 用于验证 delete 在范围为空时绝不会伪造新的计划锁，而是直接返回带引导语的成功消息。
func TestScratchpadUseCaseDeleteWithoutPlanReturnsGuidance(t *testing.T) {
	store := &fakeScratchpadStore{}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Delete(context.Background(), ScratchpadDeleteCommand{
		Scope:    logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
		PlanName: "USER_AUTH_PLAN",
		Keys:     []string{"方案"},
	})
	if err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if result.Status != logicdomain.ScratchpadStatusSuccess {
		t.Fatalf("status = %v, want success", result.Status)
	}
	if result.Message != scratchpadNoPlanMessage {
		t.Fatalf("message = %q, want %q", result.Message, scratchpadNoPlanMessage)
	}
	if result.AffectedCount != 0 {
		t.Fatalf("affected count = %d, want 0", result.AffectedCount)
	}
	if store.createdPlanName != "" {
		t.Fatalf("delete unexpectedly created plan %q", store.createdPlanName)
	}
}

// TestScratchpadUseCaseGetWithoutRecordsReturnsSuccess verifies get treats an empty scope as a successful no-data response instead of surfacing an error.
// TestScratchpadUseCaseGetWithoutRecordsReturnsSuccess 用于验证 get 会把空范围视为成功的无数据响应，而不是抛出错误。
func TestScratchpadUseCaseGetWithoutRecordsReturnsSuccess(t *testing.T) {
	store := &fakeScratchpadStore{}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Get(context.Background(), ScratchpadGetQuery{
		Scope: logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
	})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if result.Status != logicdomain.ScratchpadStatusSuccess {
		t.Fatalf("status = %v, want success", result.Status)
	}
	if result.Message != scratchpadNoRecordsMessage {
		t.Fatalf("message = %q, want %q", result.Message, scratchpadNoRecordsMessage)
	}
	if result.PlanName != "" {
		t.Fatalf("plan name = %q, want empty", result.PlanName)
	}
	if result.ItemCount != 0 {
		t.Fatalf("item count = %d, want 0", result.ItemCount)
	}
	if len(result.Items) != 0 {
		t.Fatalf("items = %+v, want empty", result.Items)
	}
}

// TestScratchpadUseCaseUpsertBlocksCrossPlanMismatch verifies a plan-name mismatch is blocked before any write reaches the isolated scratchpad store.
// TestScratchpadUseCaseUpsertBlocksCrossPlanMismatch 用于验证计划名不匹配会在任何写入进入隔离 scratchpad 存储前被直接拦截。
func TestScratchpadUseCaseUpsertBlocksCrossPlanMismatch(t *testing.T) {
	store := &fakeScratchpadStore{
		hasPlan: true,
		plan: logicdomain.ScratchpadPlanRecord{
			ID:       77,
			PlanName: "USER_AUTH_PLAN",
		},
	}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Upsert(context.Background(), ScratchpadUpsertCommand{
		Scope:    logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
		PlanName: "PAYMENT_PLAN",
		Items:    []logicdomain.ScratchpadItem{{Key: "方案", Value: "补充支付链路"}},
	})
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	if result.Status != logicdomain.ScratchpadStatusFailed {
		t.Fatalf("status = %v, want failed", result.Status)
	}
	if !strings.Contains(result.Message, "Current plan: USER_AUTH_PLAN.") {
		t.Fatalf("unexpected mismatch message: %q", result.Message)
	}
	if store.upsertPlanID != 0 || len(store.upsertItems) != 0 {
		t.Fatalf("upsert unexpectedly reached store: planID=%d items=%+v", store.upsertPlanID, store.upsertItems)
	}
}

// TestScratchpadUseCaseUpsertAllowsCaseDriftWithWarning verifies case-only plan drift is forgiven, persisted, and returned with the canonical warning prefix required by DWM.
// TestScratchpadUseCaseUpsertAllowsCaseDriftWithWarning 用于验证仅大小写差异的计划漂移会被放行、持久化，并携带 DWM 要求的 canonical 警告前缀。
func TestScratchpadUseCaseUpsertAllowsCaseDriftWithWarning(t *testing.T) {
	store := &fakeScratchpadStore{
		hasPlan: true,
		plan: logicdomain.ScratchpadPlanRecord{
			ID:       77,
			PlanName: "USER_AUTH_PLAN",
		},
	}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Upsert(context.Background(), ScratchpadUpsertCommand{
		Scope:    logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
		PlanName: "user_auth_plan",
		Items:    []logicdomain.ScratchpadItem{{Key: "方案", Value: "补充登录改造步骤"}},
	})
	if err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}
	if result.Status != logicdomain.ScratchpadStatusSuccess {
		t.Fatalf("status = %v, want success", result.Status)
	}
	if !strings.HasPrefix(result.Message, "[FORMAT DRIFT WARNING]") {
		t.Fatalf("message = %q, want format-drift warning prefix", result.Message)
	}
	if result.PlanName != "USER_AUTH_PLAN" {
		t.Fatalf("plan name = %q, want canonical USER_AUTH_PLAN", result.PlanName)
	}
	if result.AffectedCount != 1 || result.InsertedCount != 1 || result.UpdatedCount != 0 {
		t.Fatalf("unexpected counters: %+v", result)
	}
	if store.upsertPlanID != 77 || len(store.upsertItems) != 1 {
		t.Fatalf("unexpected upsert store state: planID=%d items=%+v", store.upsertPlanID, store.upsertItems)
	}
}

// TestScratchpadUseCaseGetReturnsCanonicalMetadata verifies get returns the canonical plan metadata together with the ordered item slice so compact recovery can rebuild the exact active task anchor.
// TestScratchpadUseCaseGetReturnsCanonicalMetadata 用于验证 get 会连同有序 item 一起返回 canonical 计划 metadata，让 compact 恢复可以重建准确的任务锚点。
func TestScratchpadUseCaseGetReturnsCanonicalMetadata(t *testing.T) {
	now := time.Unix(1712300000, 0).UTC()
	store := &fakeScratchpadStore{
		hasPlan: true,
		plan: logicdomain.ScratchpadPlanRecord{
			ID:        77,
			PlanName:  "USER_AUTH_PLAN",
			UpdatedAt: now,
		},
		items: map[string]string{
			"关键文件": "auth/service.go",
			"方案":   "先收敛鉴权链路，再补测试",
		},
	}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Get(context.Background(), ScratchpadGetQuery{
		Scope: logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
	})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if result.PlanName != "USER_AUTH_PLAN" {
		t.Fatalf("plan name = %q, want canonical USER_AUTH_PLAN", result.PlanName)
	}
	if !result.UpdatedAt.Equal(now) {
		t.Fatalf("updated at = %v, want %v", result.UpdatedAt, now)
	}
	if result.ItemCount != 2 {
		t.Fatalf("item count = %d, want 2", result.ItemCount)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(result.Items))
	}
}

// TestScratchpadUseCaseGetSupportsMultiKeyFilters verifies get can reload a deterministic subset of anchors when the caller specifies multiple keys.
// TestScratchpadUseCaseGetSupportsMultiKeyFilters 用于验证当调用方指定多个 key 时，get 可以重载一个确定性的锚点子集。
func TestScratchpadUseCaseGetSupportsMultiKeyFilters(t *testing.T) {
	now := time.Unix(1712300000, 0).UTC()
	store := &fakeScratchpadStore{
		hasPlan: true,
		plan: logicdomain.ScratchpadPlanRecord{
			ID:        77,
			PlanName:  "USER_AUTH_PLAN",
			UpdatedAt: now,
		},
		items: map[string]string{
			"关键文件": "auth/service.go",
			"方案":   "先收敛鉴权链路，再补测试",
			"备注":   "这条不会被返回",
		},
	}
	uc := NewScratchpadUseCase(store)

	result, err := uc.Get(context.Background(), ScratchpadGetQuery{
		Scope: logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
		Keys:  []string{"方案", "关键文件"},
	})
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if result.ItemCount != 2 {
		t.Fatalf("item count = %d, want 2", result.ItemCount)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(result.Items))
	}
	if result.Items[0].Key != "关键文件" || result.Items[1].Key != "方案" {
		t.Fatalf("unexpected filtered items: %+v", result.Items)
	}
}

// TestScratchpadUseCaseListKeysWithoutRecordsReturnsSuccess verifies list-keys treats an empty scope as a successful no-data response instead of surfacing an error.
// TestScratchpadUseCaseListKeysWithoutRecordsReturnsSuccess 用于验证 list-keys 会把空范围视为成功的无数据响应，而不是抛出错误。
func TestScratchpadUseCaseListKeysWithoutRecordsReturnsSuccess(t *testing.T) {
	store := &fakeScratchpadStore{}
	uc := NewScratchpadUseCase(store)

	result, err := uc.ListKeys(context.Background(), ScratchpadListKeysQuery{
		Scope: logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
	})
	if err != nil {
		t.Fatalf("ListKeys returned error: %v", err)
	}
	if result.Status != logicdomain.ScratchpadStatusSuccess {
		t.Fatalf("status = %v, want success", result.Status)
	}
	if result.Message != scratchpadNoRecordsMessage {
		t.Fatalf("message = %q, want %q", result.Message, scratchpadNoRecordsMessage)
	}
	if result.PlanName != "" {
		t.Fatalf("plan name = %q, want empty", result.PlanName)
	}
	if result.KeyCount != 0 {
		t.Fatalf("key count = %d, want 0", result.KeyCount)
	}
	if len(result.Keys) != 0 {
		t.Fatalf("keys = %+v, want empty", result.Keys)
	}
}

// TestScratchpadUseCaseListKeysReturnsCanonicalMetadata verifies list-keys returns the canonical plan metadata together with a stable ordered key slice.
// TestScratchpadUseCaseListKeysReturnsCanonicalMetadata 用于验证 list-keys 会连同稳定有序的 key 切片一起返回 canonical 计划 metadata。
func TestScratchpadUseCaseListKeysReturnsCanonicalMetadata(t *testing.T) {
	now := time.Unix(1712300000, 0).UTC()
	store := &fakeScratchpadStore{
		hasPlan: true,
		plan: logicdomain.ScratchpadPlanRecord{
			ID:        77,
			PlanName:  "USER_AUTH_PLAN",
			UpdatedAt: now,
		},
		items: map[string]string{
			"关键文件": "auth/service.go",
			"方案":   "先收敛鉴权链路，再补测试",
		},
	}
	uc := NewScratchpadUseCase(store)

	result, err := uc.ListKeys(context.Background(), ScratchpadListKeysQuery{
		Scope: logicdomain.ScratchpadScope{ProjectID: 9, UserID: 7, SessionKey: "sess-1"},
	})
	if err != nil {
		t.Fatalf("ListKeys returned error: %v", err)
	}
	if result.PlanName != "USER_AUTH_PLAN" {
		t.Fatalf("plan name = %q, want canonical USER_AUTH_PLAN", result.PlanName)
	}
	if !result.UpdatedAt.Equal(now) {
		t.Fatalf("updated at = %v, want %v", result.UpdatedAt, now)
	}
	if result.KeyCount != 2 {
		t.Fatalf("key count = %d, want 2", result.KeyCount)
	}
	if len(result.Keys) != 2 {
		t.Fatalf("keys len = %d, want 2", len(result.Keys))
	}
	if result.Keys[0] != "关键文件" || result.Keys[1] != "方案" {
		t.Fatalf("unexpected key list: %+v", result.Keys)
	}
}
