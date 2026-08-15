// Package vldb_controller_test verifies real SQLite and LanceDB operations through one spawned controller process.
// vldb_controller_test 包用于验证通过同一个被启动的 controller 进程执行真实 SQLite 与 LanceDB 操作。
package vldb_controller_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_controller"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb"
	"github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestRuntimeOwnsSQLiteAndLanceDB verifies one controller session can initialize, mutate, query, and release both VMM stores.
// TestRuntimeOwnsSQLiteAndLanceDB 用于验证单个 controller 会话能够初始化、写入、查询并释放两种 VMM 存储。
func TestRuntimeOwnsSQLiteAndLanceDB(t *testing.T) {
	controllerBinary := locateControllerBinary(t)
	endpoint := reserveControllerEndpoint(t)
	databaseRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	owner, err := vldb_controller.New(ctx, newIntegrationRuntimeConfig(endpoint, controllerBinary, databaseRoot, true, false))
	if err != nil {
		t.Fatalf("start controller runtime: %v", err)
	}
	t.Cleanup(func() {
		_ = owner.Shutdown(context.Background())
	})
	if err := owner.CheckHealth(ctx); err != nil {
		t.Fatalf("check controller runtime health: %v", err)
	}

	sqliteStore, err := vldb_sqlite.NewControllerStore(owner, 10*time.Second, vldb_sqlite.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		t.Fatalf("open controller sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Shutdown(context.Background())
	})
	schemaVersion, err := sqliteStore.GetSchemaComponentVersion(ctx, "sqlite")
	if err != nil {
		t.Fatalf("query controller sqlite schema version: %v", err)
	}
	if schemaVersion != 22 {
		t.Fatalf("sqlite schema version = %d, want 22", schemaVersion)
	}
	projectResult, err := sqliteStore.EnsureProjectPath(ctx, "ControllerTeam/ControllerSpace/ControllerProject", true)
	if err != nil {
		t.Fatalf("create project through controller sqlite: %v", err)
	}
	if projectResult.Project.ID == 0 {
		t.Fatal("expected controller sqlite project id")
	}

	// failureSession owns one real pending turn used to verify the schema trigger and durable fifth-failure transition through the controller boundary.
	// failureSession 持有一条真实 pending turn，用于跨 controller 边界验证 schema 触发器与第 5 次失败的持久化迁移。
	failureSession, err := sqliteStore.ResolveRequestScope(ctx, "controller-failure-pass", 1, projectResult.Project.ID)
	if err != nil {
		t.Fatalf("resolve controller failure session: %v", err)
	}
	failureTurn, err := sqliteStore.AppendTurnRecord(ctx, failureSession, logicdomain.TurnRecord{
		UserContent:      "remember this after the model recovers",
		AssistantContent: "the primary response already completed",
	})
	if err != nil {
		t.Fatalf("append controller failure turn: %v", err)
	}
	for attempt := 1; attempt <= 5; attempt++ {
		failureResult, failureErr := sqliteStore.RecordTurnAnalysisFailure(
			ctx,
			failureSession,
			failureTurn.ID,
			logicdomain.TurnAnalysisFailureStagePostAction,
			"simulated slow provider timeout",
			5,
			false,
		)
		if failureErr != nil {
			t.Fatalf("record controller analysis failure %d: %v", attempt, failureErr)
		}
		if failureResult.AttemptCount != attempt || failureResult.Passed != (attempt == 5) {
			t.Fatalf("failure attempt %d result = %+v", attempt, failureResult)
		}
	}
	pendingTurns, err := sqliteStore.LoadPendingSessionTurns(ctx, failureSession)
	if err != nil {
		t.Fatalf("load pending turns after terminal Pass: %v", err)
	}
	if len(pendingTurns) != 0 {
		t.Fatalf("terminal Pass left pending turns: %+v", pendingTurns)
	}

	lanceStore, err := vldb_lancedb.NewControllerStore(owner, 10*time.Second, "controller_vectors", "vector", 4)
	if err != nil {
		t.Fatalf("open controller lancedb store: %v", err)
	}
	t.Cleanup(func() {
		_ = lanceStore.Shutdown(context.Background())
	})
	filter := logicdomain.SearchFilter{
		TeamID:    projectResult.Project.TeamID,
		SpaceID:   projectResult.Project.SpaceID,
		ProjectID: projectResult.Project.ID,
		UserID:    1,
		SessionID: 1,
	}
	record := logicdomain.MemoryRecord{
		ID:           "controller-vector-1",
		Text:         "controller vector smoke",
		Vector:       []float32{1, 0, 0, 0},
		Filter:       filter,
		SourceTurnID: 1,
		Status:       0,
		Metadata:     map[string]string{"test": "controller"},
		CreatedAt:    time.Now().UTC(),
	}
	if err := lanceStore.Upsert(ctx, record); err != nil {
		t.Fatalf("upsert controller lancedb vector: %v", err)
	}
	hits, err := lanceStore.Search(ctx, []float32{1, 0, 0, 0}, 3, filter)
	if err != nil {
		t.Fatalf("search controller lancedb vector: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != record.ID {
		t.Fatalf("controller lancedb hits = %#v, want id %q", hits, record.ID)
	}

	// Verify maintenance-style exclusive sessions fail while the live VMM owner remains attached.
	// 验证模拟维护工具的独占会话会在存活 VMM owner 仍附着时拒绝启动。
	exclusiveOwner, err := vldb_controller.New(ctx, newIntegrationRuntimeConfig(endpoint, controllerBinary, databaseRoot, false, true))
	if err == nil {
		_ = exclusiveOwner.Shutdown(context.Background())
		t.Fatal("expected exclusive controller runtime to reject an active owner")
	}
	if !strings.Contains(err.Error(), "active clients") {
		t.Fatalf("exclusive controller runtime error = %v", err)
	}

	// Attach a second independent client session to the same physical resources and verify both backends reuse controller-owned handles.
	// 把第二个独立客户端会话附着到相同物理资源，并验证两种后端都复用 controller 持有的句柄。
	secondOwner, err := vldb_controller.New(ctx, newIntegrationRuntimeConfig(endpoint, controllerBinary, databaseRoot, false, false))
	if err != nil {
		t.Fatalf("attach second controller runtime: %v", err)
	}
	t.Cleanup(func() {
		_ = secondOwner.Shutdown(context.Background())
	})
	secondSQLiteStore, err := vldb_sqlite.NewControllerStore(secondOwner, 10*time.Second, vldb_sqlite.StoreOptions{TokenizerMode: "jieba"})
	if err != nil {
		t.Fatalf("open second controller sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = secondSQLiteStore.Shutdown(context.Background())
	})
	resolvedProject, err := secondSQLiteStore.ResolveProjectRef(ctx, "ControllerTeam/ControllerSpace/ControllerProject")
	if err != nil {
		t.Fatalf("read shared project through second controller sqlite binding: %v", err)
	}
	if resolvedProject.ID != projectResult.Project.ID {
		t.Fatalf("shared controller sqlite project id = %d, want %d", resolvedProject.ID, projectResult.Project.ID)
	}
	secondLanceStore, err := vldb_lancedb.NewControllerStore(secondOwner, 10*time.Second, "controller_vectors", "vector", 4)
	if err != nil {
		t.Fatalf("open second controller lancedb store: %v", err)
	}
	t.Cleanup(func() {
		_ = secondLanceStore.Shutdown(context.Background())
	})
	secondHits, err := secondLanceStore.Search(ctx, []float32{1, 0, 0, 0}, 3, filter)
	if err != nil {
		t.Fatalf("search shared vector through second controller lancedb binding: %v", err)
	}
	if len(secondHits) != 1 || secondHits[0].ID != record.ID {
		t.Fatalf("second controller lancedb hits = %#v, want id %q", secondHits, record.ID)
	}
}

// newIntegrationRuntimeConfig creates the pinned, isolated controller configuration shared by real integration-test sessions.
// newIntegrationRuntimeConfig 创建真实集成测试会话共享的固定版本隔离 controller 配置。
func newIntegrationRuntimeConfig(endpoint string, executable string, databaseRoot string, autoSpawn bool, requireExclusiveSpace bool) vldb_controller.Config {
	return vldb_controller.Config{
		Endpoint:              endpoint,
		AutoSpawn:             autoSpawn,
		Executable:            executable,
		ProcessMode:           "managed",
		MinimumUptime:         time.Second,
		IdleTimeout:           2 * time.Second,
		LeaseTTL:              10 * time.Second,
		ConnectTimeout:        2 * time.Second,
		StartupTimeout:        15 * time.Second,
		StartupRetryInterval:  100 * time.Millisecond,
		LeaseRenewInterval:    time.Second,
		RequestTimeout:        10 * time.Second,
		RequireExclusiveSpace: requireExclusiveSpace,
		SpaceID:               "vmm-controller-integration",
		SpaceLabel:            "VMM controller integration",
		SpaceRoot:             databaseRoot,
		SQLiteDatabase:        filepath.Join(databaseRoot, "sqlite.db"),
		LanceDBDirectory:      filepath.Join(databaseRoot, "lancedb"),
	}
}

// locateControllerBinary resolves the pinned workspace dependency and skips only when host dependencies were not installed.
// locateControllerBinary 用于解析固定版本工作区依赖，仅在尚未安装宿主依赖时跳过。
func locateControllerBinary(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	binaryName := "vldb-controller"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", ".."))
	binaryPath := filepath.Join(repositoryRoot, "third_party", "deps", binaryName)
	if info, err := os.Stat(binaryPath); err != nil || info.IsDir() {
		t.Skipf("controller integration dependency is unavailable: %s", binaryPath)
	}
	return binaryPath
}

// reserveControllerEndpoint obtains a currently unused loopback port for one managed integration-test process.
// reserveControllerEndpoint 为单个 managed 集成测试进程获取当前未占用的 loopback 端口。
func reserveControllerEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve controller endpoint: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved controller endpoint: %v", err)
	}
	return "http://" + address
}
