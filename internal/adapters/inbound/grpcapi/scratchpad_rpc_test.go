// scratchpad_rpc_test.go verifies the isolated DWM gRPC surface keeps transport validation, shorthand expansion, and response metadata aligned with the public contract.
// scratchpad_rpc_test.go 用于验证隔离 DWM 的 gRPC 接口会把传输层校验、单项简写展开和响应 metadata 保持在公开契约要求之内。
package grpcapi

import (
	"context"
	"testing"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// stubScratchpadExecutor keeps one in-memory record of the latest scratchpad RPC command so transport tests can assert request shaping without touching a real store.
// stubScratchpadExecutor 用于记录最近一次 scratchpad RPC 命令，让传输层测试无需真实存储就能断言请求整形行为。
type stubScratchpadExecutor struct {
	upsertCmd      usecase.ScratchpadUpsertCommand
	deleteCmd      usecase.ScratchpadDeleteCommand
	getQuery       usecase.ScratchpadGetQuery
	listKeysQuery  usecase.ScratchpadListKeysQuery
	cleanCmd       usecase.ScratchpadCleanCommand
	upsertResult   logicdomain.ScratchpadMutationResult
	deleteResult   logicdomain.ScratchpadMutationResult
	getResult      logicdomain.ScratchpadQueryResult
	listKeysResult logicdomain.ScratchpadKeyListResult
	cleanResult    logicdomain.ScratchpadMutationResult
}

// Upsert records the latest upsert command and returns the configured fake result.
// Upsert 用于记录最近一次 upsert 命令，并返回预设的 fake 结果。
func (s *stubScratchpadExecutor) Upsert(_ context.Context, cmd usecase.ScratchpadUpsertCommand) (logicdomain.ScratchpadMutationResult, error) {
	s.upsertCmd = cmd
	return s.upsertResult, nil
}

// Delete records the latest delete command and returns the configured fake result.
// Delete 用于记录最近一次 delete 命令，并返回预设的 fake 结果。
func (s *stubScratchpadExecutor) Delete(_ context.Context, cmd usecase.ScratchpadDeleteCommand) (logicdomain.ScratchpadMutationResult, error) {
	s.deleteCmd = cmd
	return s.deleteResult, nil
}

// Get records the latest get query and returns the configured fake result.
// Get 用于记录最近一次 get 查询，并返回预设的 fake 结果。
func (s *stubScratchpadExecutor) Get(_ context.Context, query usecase.ScratchpadGetQuery) (logicdomain.ScratchpadQueryResult, error) {
	s.getQuery = query
	return s.getResult, nil
}

// ListKeys records the latest list-keys query and returns the configured fake result.
// ListKeys 用于记录最近一次 list-keys 查询，并返回预设的 fake 结果。
func (s *stubScratchpadExecutor) ListKeys(_ context.Context, query usecase.ScratchpadListKeysQuery) (logicdomain.ScratchpadKeyListResult, error) {
	s.listKeysQuery = query
	return s.listKeysResult, nil
}

// Clean records the latest clean command and returns the configured fake result.
// Clean 用于记录最近一次 clean 命令，并返回预设的 fake 结果。
func (s *stubScratchpadExecutor) Clean(_ context.Context, cmd usecase.ScratchpadCleanCommand) (logicdomain.ScratchpadMutationResult, error) {
	s.cleanCmd = cmd
	return s.cleanResult, nil
}

// TestScratchpadUpsertRejectsMixedSingleAndBatchPayload verifies transport validation now rejects simultaneous key/value and items[] usage instead of silently merging them.
// TestScratchpadUpsertRejectsMixedSingleAndBatchPayload 用于验证传输层现在会拒绝同时传入 key/value 与 items[]，而不是像旧实现那样静默合并。
func TestScratchpadUpsertRejectsMixedSingleAndBatchPayload(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: &stubScratchpadExecutor{},
	}, testBufSize)

	_, err := fixture.client.ScratchpadUpsert(context.Background(), &vmmv1.ScratchpadUpsertRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		PlanName:  "USER_AUTH_PLAN",
		Key:       strPtr("方案"),
		Value:     strPtr("单项"),
		Items: []*vmmv1.ScratchpadItem{
			{Key: "关键文件", Value: "auth/service.go"},
		},
	})
	if err == nil {
		t.Fatal("expected mixed scratchpad upsert payload to fail")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("status code = %s, want InvalidArgument", got)
	}
}

// TestScratchpadDeleteRejectsMixedSingleAndBatchPayload verifies transport validation now rejects simultaneous key and keys[] usage instead of silently merging them.
// TestScratchpadDeleteRejectsMixedSingleAndBatchPayload 用于验证传输层现在会拒绝同时传入 key 与 keys[]，而不是像旧实现那样静默合并。
func TestScratchpadDeleteRejectsMixedSingleAndBatchPayload(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: &stubScratchpadExecutor{},
	}, testBufSize)

	_, err := fixture.client.ScratchpadDelete(context.Background(), &vmmv1.ScratchpadDeleteRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		PlanName:  "USER_AUTH_PLAN",
		Key:       strPtr("方案"),
		Keys:      []string{"关键文件"},
	})
	if err == nil {
		t.Fatal("expected mixed scratchpad delete payload to fail")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("status code = %s, want InvalidArgument", got)
	}
}

// TestScratchpadUpsertReturnsCountersAndSingleItemExpansion verifies single-item shorthand still reaches the use case as one deterministic batch while the RPC response exposes stable write counters.
// TestScratchpadUpsertReturnsCountersAndSingleItemExpansion 用于验证单项简写仍会以一个确定性批次进入用例层，同时 RPC 响应会暴露稳定写入计数字段。
func TestScratchpadUpsertReturnsCountersAndSingleItemExpansion(t *testing.T) {
	stub := &stubScratchpadExecutor{
		upsertResult: logicdomain.ScratchpadMutationResult{
			Status:        logicdomain.ScratchpadStatusSuccess,
			Message:       "Upserted 1 scratchpad record(s).",
			AffectedCount: 1,
			InsertedCount: 1,
			UpdatedCount:  0,
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: stub,
	}, testBufSize)

	resp, err := fixture.client.ScratchpadUpsert(context.Background(), &vmmv1.ScratchpadUpsertRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		PlanName:  "USER_AUTH_PLAN",
		Key:       strPtr("方案"),
		Value:     strPtr("补齐登录流程"),
	})
	if err != nil {
		t.Fatalf("scratchpad upsert: %v", err)
	}
	if len(stub.upsertCmd.Items) != 1 || stub.upsertCmd.Items[0].Key != "方案" {
		t.Fatalf("unexpected upsert command items: %+v", stub.upsertCmd.Items)
	}
	if resp.GetAffectedCount() != 1 || resp.GetInsertedCount() != 1 || resp.GetUpdatedCount() != 0 {
		t.Fatalf("unexpected upsert counters: %+v", resp)
	}
}

// TestScratchpadGetReturnsMetadata verifies get responses now expose canonical plan metadata so compact recovery callers can restore the active task lock without guessing it from items[].
// TestScratchpadGetReturnsMetadata 用于验证 get 响应现在会暴露 canonical 计划 metadata，让 compact 恢复调用方无需再从 items[] 反推当前任务锁。
func TestScratchpadGetReturnsMetadata(t *testing.T) {
	updatedAt := time.Unix(1712300000, 0).UTC()
	stub := &stubScratchpadExecutor{
		getResult: logicdomain.ScratchpadQueryResult{
			Status:    logicdomain.ScratchpadStatusSuccess,
			Message:   "Retrieved 2 scratchpad record(s).",
			PlanName:  "USER_AUTH_PLAN",
			UpdatedAt: updatedAt,
			ItemCount: 2,
			Items: []logicdomain.ScratchpadItem{
				{Key: "方案", Value: "先收敛鉴权链路"},
				{Key: "关键文件", Value: "auth/service.go"},
			},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: stub,
	}, testBufSize)

	resp, err := fixture.client.ScratchpadGet(context.Background(), &vmmv1.ScratchpadGetRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
	})
	if err != nil {
		t.Fatalf("scratchpad get: %v", err)
	}
	if resp.GetPlanName() != "USER_AUTH_PLAN" {
		t.Fatalf("plan name = %q, want USER_AUTH_PLAN", resp.GetPlanName())
	}
	if resp.GetItemCount() != 2 {
		t.Fatalf("item count = %d, want 2", resp.GetItemCount())
	}
	if resp.GetUpdatedTimestamp() != updatedAt.UnixMilli() {
		t.Fatalf("updated timestamp = %d, want %d", resp.GetUpdatedTimestamp(), updatedAt.UnixMilli())
	}
	if len(resp.GetItems()) != 2 {
		t.Fatalf("items len = %d, want 2", len(resp.GetItems()))
	}
	if len(stub.getQuery.Keys) != 0 {
		t.Fatalf("expected empty key filter for full get, got %+v", stub.getQuery.Keys)
	}
}

// TestScratchpadGetSupportsMultiKeyFilters verifies the transport layer forwards repeated keys to the use case so callers can restore multiple anchors in one round-trip.
// TestScratchpadGetSupportsMultiKeyFilters 用于验证传输层会把重复 key 列表下传给用例层，让调用方一次往返恢复多个锚点。
func TestScratchpadGetSupportsMultiKeyFilters(t *testing.T) {
	stub := &stubScratchpadExecutor{
		getResult: logicdomain.ScratchpadQueryResult{
			Status:    logicdomain.ScratchpadStatusSuccess,
			Message:   "Retrieved 2 scratchpad record(s).",
			PlanName:  "USER_AUTH_PLAN",
			ItemCount: 2,
			Items: []logicdomain.ScratchpadItem{
				{Key: "关键文件", Value: "auth/service.go"},
				{Key: "方案", Value: "先收敛鉴权链路"},
			},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: stub,
	}, testBufSize)

	resp, err := fixture.client.ScratchpadGet(context.Background(), &vmmv1.ScratchpadGetRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		Keys:      []string{"方案", "关键文件"},
	})
	if err != nil {
		t.Fatalf("scratchpad get with keys: %v", err)
	}
	if len(stub.getQuery.Keys) != 2 || stub.getQuery.Keys[0] != "方案" || stub.getQuery.Keys[1] != "关键文件" {
		t.Fatalf("unexpected get query keys: %+v", stub.getQuery.Keys)
	}
	if len(resp.GetItems()) != 2 {
		t.Fatalf("items len = %d, want 2", len(resp.GetItems()))
	}
}

// TestScratchpadGetRejectsInvalidKey verifies the transport validator rejects oversized or blank keys before the isolated DWM read path starts.
// TestScratchpadGetRejectsInvalidKey 用于验证传输层会在隔离 DWM 读取路径开始前拒绝空白或超长 key。
func TestScratchpadGetRejectsInvalidKey(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: &stubScratchpadExecutor{},
	}, testBufSize)

	_, err := fixture.client.ScratchpadGet(context.Background(), &vmmv1.ScratchpadGetRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		Keys:      []string{" "},
	})
	if err == nil {
		t.Fatal("expected invalid scratchpad get key to fail")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("status code = %s, want InvalidArgument", got)
	}
}

// TestScratchpadListKeysReturnsPlanNameAndKeys verifies list-keys responses expose the canonical plan name plus the stable ordered key slice without forcing callers to load values.
// TestScratchpadListKeysReturnsPlanNameAndKeys 用于验证 list-keys 响应会暴露 canonical 计划名和稳定有序的 key 列表，而无需调用方再额外加载 value。
func TestScratchpadListKeysReturnsPlanNameAndKeys(t *testing.T) {
	updatedAt := time.Unix(1712300000, 0).UTC()
	stub := &stubScratchpadExecutor{
		listKeysResult: logicdomain.ScratchpadKeyListResult{
			Status:    logicdomain.ScratchpadStatusSuccess,
			Message:   "Listed 2 scratchpad key(s).",
			PlanName:  "USER_AUTH_PLAN",
			UpdatedAt: updatedAt,
			KeyCount:  2,
			Keys:      []string{"关键文件", "方案"},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: stub,
	}, testBufSize)

	resp, err := fixture.client.ScratchpadListKeys(context.Background(), &vmmv1.ScratchpadListKeysRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
	})
	if err != nil {
		t.Fatalf("scratchpad list keys: %v", err)
	}
	if stub.listKeysQuery.Scope.SessionKey != "sess-1" || stub.listKeysQuery.Scope.UserID != 7 || stub.listKeysQuery.Scope.ProjectID != 9 {
		t.Fatalf("unexpected list-keys query scope: %+v", stub.listKeysQuery.Scope)
	}
	if resp.GetPlanName() != "USER_AUTH_PLAN" {
		t.Fatalf("plan name = %q, want USER_AUTH_PLAN", resp.GetPlanName())
	}
	if resp.GetKeyCount() != 2 {
		t.Fatalf("key count = %d, want 2", resp.GetKeyCount())
	}
	if resp.GetUpdatedTimestamp() != updatedAt.UnixMilli() {
		t.Fatalf("updated timestamp = %d, want %d", resp.GetUpdatedTimestamp(), updatedAt.UnixMilli())
	}
	if len(resp.GetKeys()) != 2 || resp.GetKeys()[0] != "关键文件" || resp.GetKeys()[1] != "方案" {
		t.Fatalf("unexpected key list: %+v", resp.GetKeys())
	}
}

// TestScratchpadListKeysRejectsMissingSession verifies the transport validator rejects incomplete list-keys scope requests before the isolated DWM read path starts.
// TestScratchpadListKeysRejectsMissingSession 用于验证传输层会在隔离 DWM list-keys 读取开始前拒绝不完整的 scope 请求。
func TestScratchpadListKeysRejectsMissingSession(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: &stubScratchpadExecutor{},
	}, testBufSize)

	_, err := fixture.client.ScratchpadListKeys(context.Background(), &vmmv1.ScratchpadListKeysRequest{
		SessionId: " ",
		UserId:    7,
		ProjectId: 9,
	})
	if err == nil {
		t.Fatal("expected invalid scratchpad list-keys request to fail")
	}
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("status code = %s, want InvalidArgument", got)
	}
}

// TestScratchpadDeleteReturnsAffectedCountAndSingleKeyExpansion verifies single-key delete shorthand still reaches the use case as one deterministic batch while the RPC response exposes the delete count.
// TestScratchpadDeleteReturnsAffectedCountAndSingleKeyExpansion 用于验证单键删除简写仍会以一个确定性批次进入用例层，同时 RPC 响应会暴露删除计数。
func TestScratchpadDeleteReturnsAffectedCountAndSingleKeyExpansion(t *testing.T) {
	stub := &stubScratchpadExecutor{
		deleteResult: logicdomain.ScratchpadMutationResult{
			Status:        logicdomain.ScratchpadStatusSuccess,
			Message:       "Deleted 1 scratchpad record(s).",
			AffectedCount: 1,
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:        xid.NewGenerator(),
		Scratchpad: stub,
	}, testBufSize)

	resp, err := fixture.client.ScratchpadDelete(context.Background(), &vmmv1.ScratchpadDeleteRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		PlanName:  "USER_AUTH_PLAN",
		Key:       strPtr("方案"),
	})
	if err != nil {
		t.Fatalf("scratchpad delete: %v", err)
	}
	if len(stub.deleteCmd.Keys) != 1 || stub.deleteCmd.Keys[0] != "方案" {
		t.Fatalf("unexpected delete command keys: %+v", stub.deleteCmd.Keys)
	}
	if resp.GetAffectedCount() != 1 {
		t.Fatalf("affected count = %d, want 1", resp.GetAffectedCount())
	}
}

// strPtr keeps optional scratchpad transport fields readable inside focused RPC tests.
// strPtr 用于让聚焦型 RPC 测试里的可选 scratchpad 传输字段保持可读。
func strPtr(value string) *string {
	return &value
}
