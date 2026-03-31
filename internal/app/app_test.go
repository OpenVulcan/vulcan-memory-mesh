// app_test.go verifies runtime-only composition details such as gRPC reflection registration without depending on external gateways.
// app_test.go 用于在不依赖外部网关的前提下，验证 gRPC 反射注册等仅运行时装配细节。
package app

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb/proto/v1"
	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	"github.com/openvulcan/vmm/internal/config"
	"google.golang.org/grpc"
)

// TestNewLocalRegistersReflection verifies the local runtime exposes both the main VMM service and gRPC reflection.
// TestNewLocalRegistersReflection 用于验证本地运行时会同时暴露主 VMM 服务和 gRPC reflection。
func TestNewLocalRegistersReflection(t *testing.T) {
	// Build prompt layout from real repository assets so the composition root still uses the shipped prompt tree.
	// 基于仓库里的真实资源构建提示词布局，确保组合根仍然使用随仓库发布的提示词目录。
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(t.TempDir(), "go-build", "vmm-local.exe"),
		filepath.Join(root, "cmd", "vmm-local"),
		"",
		"local",
	)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := config.NewPromptManager(layout.SystemDir, layout.UserDir)
	if err != nil {
		t.Fatal(err)
	}

	// Start local fake gateways on loopback TCP so NewLocal can dial them through the normal gRPC clients.
	// 在本机回环地址启动假的网关，让 NewLocal 可以通过正常 gRPC 客户端拨号。
	sqliteAddr, stopSQLite := startFakeSQLiteGateway(t)
	defer stopSQLite()
	lanceAddr, stopLance := startFakeLanceDBGateway(t)
	defer stopLance()

	cfg := config.DefaultLocal()
	cfg.SQLite.Address = sqliteAddr
	cfg.LanceDB.Address = lanceAddr
	cfg.LLM.Endpoint = "https://example.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.Model = "test-llm"
	cfg.Embedding.Endpoint = "https://example.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.Embedding.Model = "test-embedding"
	cfg.Embedding.Dimension = 1024

	app, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	info := app.Server.GetServiceInfo()
	if _, ok := info["grpc.reflection.v1alpha.ServerReflection"]; !ok {
		t.Fatalf("expected gRPC reflection to be registered, got services: %#v", info)
	}
	if _, ok := info["vmm.v1.VMMService"]; !ok {
		t.Fatalf("expected VMM service to be registered, got services: %#v", info)
	}
}

// startFakeSQLiteGateway serves the minimal SQLite RPC surface needed by runtime composition tests.
// startFakeSQLiteGateway 用于提供运行时装配测试所需的最小 SQLite RPC 面。
func startFakeSQLiteGateway(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake sqlite: %v", err)
	}
	server := grpc.NewServer()
	sqlitev1.RegisterSqliteServiceServer(server, fakeSQLiteGateway{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), func() {
		server.Stop()
		_ = listener.Close()
	}
}

// startFakeLanceDBGateway serves the minimal LanceDB RPC surface needed by runtime composition tests.
// startFakeLanceDBGateway 用于提供运行时装配测试所需的最小 LanceDB RPC 面。
func startFakeLanceDBGateway(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake lancedb: %v", err)
	}
	server := grpc.NewServer()
	lancedbv1.RegisterLanceDbServiceServer(server, fakeLanceDBGateway{})
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String(), func() {
		server.Stop()
		_ = listener.Close()
	}
}

// fakeSQLiteGateway implements the tiny subset of SQLite calls exercised while bootstrapping the local runtime.
// fakeSQLiteGateway 用于实现本地运行时启动时会触发的最小 SQLite 调用集合。
type fakeSQLiteGateway struct {
	sqlitev1.UnimplementedSqliteServiceServer
}

// ExecuteScript always succeeds because this test only cares about successful schema bootstrap wiring.
// ExecuteScript 总是返回成功，因为这个测试只关心 schema 引导接线是否成功。
func (fakeSQLiteGateway) ExecuteScript(context.Context, *sqlitev1.ExecuteRequest) (*sqlitev1.ExecuteResponse, error) {
	return &sqlitev1.ExecuteResponse{Success: true, Message: "ok"}, nil
}

// QueryJson returns canned rows for version and noise-cache lookups used during startup.
// QueryJson 用于返回启动期 schema 版本和噪声缓存查询需要的预置结果。
func (fakeSQLiteGateway) QueryJson(_ context.Context, req *sqlitev1.QueryRequest) (*sqlitev1.QueryJsonResponse, error) {
	sql := strings.TrimSpace(req.GetSql())
	switch {
	case strings.Contains(sql, "FROM vmm_version"):
		return &sqlitev1.QueryJsonResponse{JsonData: `[{"schema_version":3}]`}, nil
	case strings.Contains(sql, "FROM vmm_noise_embeddings"):
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	default:
		return &sqlitev1.QueryJsonResponse{JsonData: `[]`}, nil
	}
}

// QueryStream stays unused in this focused runtime composition test.
// QueryStream 在这个聚焦的运行时装配测试里保持未使用状态。
func (fakeSQLiteGateway) QueryStream(*sqlitev1.QueryRequest, grpc.ServerStreamingServer[sqlitev1.QueryResponse]) error {
	return nil
}

// fakeLanceDBGateway implements the tiny subset of LanceDB calls exercised while bootstrapping the local runtime.
// fakeLanceDBGateway 用于实现本地运行时启动时会触发的最小 LanceDB 调用集合。
type fakeLanceDBGateway struct {
	lancedbv1.UnimplementedLanceDbServiceServer
}

// CreateTable always succeeds because the runtime composition test only needs idempotent table creation wiring.
// CreateTable 总是返回成功，因为运行时装配测试只需要验证幂等建表接线。
func (fakeLanceDBGateway) CreateTable(context.Context, *lancedbv1.CreateTableRequest) (*lancedbv1.CreateTableResponse, error) {
	return &lancedbv1.CreateTableResponse{Success: true, Message: "ok"}, nil
}

// VectorUpsert stays unused in this focused runtime composition test.
// VectorUpsert 在这个聚焦的运行时装配测试里保持未使用状态。
func (fakeLanceDBGateway) VectorUpsert(context.Context, *lancedbv1.UpsertRequest) (*lancedbv1.UpsertResponse, error) {
	return &lancedbv1.UpsertResponse{Success: true, Message: "ok"}, nil
}

// VectorSearch stays unused in this focused runtime composition test.
// VectorSearch 在这个聚焦的运行时装配测试里保持未使用状态。
func (fakeLanceDBGateway) VectorSearch(context.Context, *lancedbv1.SearchRequest) (*lancedbv1.SearchResponse, error) {
	return &lancedbv1.SearchResponse{Success: true, Message: "ok", Data: []byte("[]")}, nil
}

// Delete stays unused in this focused runtime composition test.
// Delete 在这个聚焦的运行时装配测试里保持未使用状态。
func (fakeLanceDBGateway) Delete(context.Context, *lancedbv1.DeleteRequest) (*lancedbv1.DeleteResponse, error) {
	return &lancedbv1.DeleteResponse{Success: true, DeletedRows: 0, Message: "ok"}, nil
}
