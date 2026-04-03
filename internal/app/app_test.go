// app_test.go verifies runtime-only composition details such as gRPC reflection registration without depending on external gateways.
// app_test.go 用于在不依赖外部网关的前提下，验证 gRPC 反射注册等仅运行时装配细节。
package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb/proto/v1"
	sqlitev1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_sqlite/proto/v1"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/config"
	"github.com/openvulcan/vmm/internal/platform/logx"
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

// TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs verifies runtime file logs stay next to the resolved system config root so packaged binaries use `output/logs` while go-run development uses the repository `logs` directory.
// TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs 用于验证运行时文件日志会落在系统配置根的同级目录；这样打包二进制走 `output/logs`，而 go run 调试走仓库根 `logs`。
func TestResolveRuntimeLogDirUsesSiblingOfSystemConfigs(t *testing.T) {
	logDir, err := resolveRuntimeLogDir(config.PromptLayout{SystemDir: filepath.Join("D:", "repo", "output", "configs")})
	if err != nil {
		t.Fatalf("resolve runtime log dir for packaged layout: %v", err)
	}
	if want := filepath.Join("D:", "repo", "output", "logs"); logDir != want {
		t.Fatalf("packaged log dir = %q, want %q", logDir, want)
	}

	logDir, err = resolveRuntimeLogDir(config.PromptLayout{SystemDir: filepath.Join("D:", "repo", "configs")})
	if err != nil {
		t.Fatalf("resolve runtime log dir for go-run layout: %v", err)
	}
	if want := filepath.Join("D:", "repo", "logs"); logDir != want {
		t.Fatalf("go-run log dir = %q, want %q", logDir, want)
	}
}

// TestNewLocalCreatesRuntimeLogFile verifies application composition eagerly creates the day/hour log file under the resolved runtime log root so startup fails early on invalid paths instead of silently dropping logs later.
// TestNewLocalCreatesRuntimeLogFile 用于验证应用装配会在解析出的运行时日志根目录下立即创建按天/小时的日志文件，让路径异常在启动阶段就暴露出来，而不是之后静默丢日志。
func TestNewLocalCreatesRuntimeLogFile(t *testing.T) {
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	layout, err := config.ResolvePromptLayout(
		filepath.Join(root, "output", "bin", "vmm-local.exe"),
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

	application, err := NewLocal(cfg, prompts, layout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = application.Shutdown(context.Background())
	})

	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		t.Fatal(err)
	}
	dayDir := filepath.Join(logDir, time.Now().Local().Format("20060102"))
	entries, err := os.ReadDir(dayDir)
	if err != nil {
		t.Fatalf("read runtime log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one hourly log file in %s", dayDir)
	}
}

// TestNewLocalClosesRuntimeLogFileOnInitFailure verifies startup closes the eagerly opened runtime log file again when later dependency wiring fails, so callers do not inherit leaked file handles on Windows or other strict filesystems.
// TestNewLocalClosesRuntimeLogFileOnInitFailure 用于验证当后续依赖装配失败时，启动流程会把提前打开的运行时日志文件重新关闭，避免调用方在 Windows 等严格文件系统上继承泄露的文件句柄。
func TestNewLocalClosesRuntimeLogFileOnInitFailure(t *testing.T) {
	root := t.TempDir()
	layout := config.PromptLayout{
		SystemDir: filepath.Join(root, "configs"),
	}
	cfg := config.DefaultLocal()
	cfg.LLM.Provider = "unsupported"

	_, err := NewLocal(cfg, nil, layout)
	if err == nil {
		t.Fatal("expected startup error")
	}

	logDir, err := resolveRuntimeLogDir(layout)
	if err != nil {
		t.Fatalf("resolve runtime log dir: %v", err)
	}
	if err := os.RemoveAll(logDir); err != nil {
		t.Fatalf("remove runtime log dir after init failure: %v", err)
	}
	if _, statErr := os.Stat(logDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected runtime log dir to be removable after init failure, stat err=%v", statErr)
	}
}

// TestBuildRerankerFallsBackToLLMAPIKey verifies DashScope rerank can reuse the existing LLM api key when rerank.api_key is omitted.
// TestBuildRerankerFallsBackToLLMAPIKey 用于验证在省略 rerank.api_key 时，DashScope rerank 可以回退复用现有 llm.api_key。
func TestBuildRerankerFallsBackToLLMAPIKey(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.LLM.APIKey = "shared-llm-key"
	cfg.Rerank.Enabled = true
	cfg.Rerank.Provider = "dashscope"
	cfg.Rerank.APIKey = ""
	cfg.Rerank.Endpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"
	cfg.Rerank.Model = "qwen3-vl-rerank"

	reranker, err := buildReranker(cfg)
	if err != nil {
		t.Fatalf("build reranker: %v", err)
	}
	if reranker == nil {
		t.Fatal("expected reranker to be constructed")
	}
}

// TestBuildAdaptersAllowTrimmedProviderAliases verifies runtime adapter construction stays aligned with config validation when provider aliases contain surrounding whitespace.
// TestBuildAdaptersAllowTrimmedProviderAliases 用于验证当 provider 别名带有首尾空白时，运行时适配器构建仍与配置校验口径保持一致。
func TestBuildAdaptersAllowTrimmedProviderAliases(t *testing.T) {
	cfg := config.DefaultLocal()
	cfg.LLM.Provider = " openai "
	cfg.LLM.Endpoint = "https://example.com/v1"
	cfg.LLM.APIKey = "test-key"
	cfg.LLM.Model = "test-llm"
	cfg.Embedding.Provider = " openai "
	cfg.Embedding.Endpoint = "https://example.com/v1"
	cfg.Embedding.APIKey = "test-key"
	cfg.Embedding.Model = "test-embedding"
	cfg.Embedding.Dimension = 1024
	cfg.Rerank.Enabled = true
	cfg.Rerank.Provider = " dashscope "
	cfg.Rerank.Endpoint = "https://dashscope.aliyuncs.com/api/v1/services/rerank/text-rerank/text-rerank"
	cfg.Rerank.APIKey = "test-key"
	cfg.Rerank.Model = "qwen3-vl-rerank"
	cfg.Vector.Provider = " lancedb "
	cfg.Relational.Provider = " sqlite "
	cfg.SQLite.Address = "127.0.0.1:19501"
	cfg.LanceDB.Address = "127.0.0.1:19301"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validation should accept trimmed provider aliases: %v", err)
	}
	if _, err := buildLLM(cfg); err != nil {
		t.Fatalf("build llm with trimmed provider alias: %v", err)
	}
	if _, err := buildEmbedding(cfg); err != nil {
		t.Fatalf("build embedding with trimmed provider alias: %v", err)
	}
	if _, err := buildReranker(cfg); err != nil {
		t.Fatalf("build reranker with trimmed provider alias: %v", err)
	}
	if _, err := buildVector(cfg); err != nil {
		t.Fatalf("build vector with trimmed provider alias: %v", err)
	}
	if _, err := buildRelational(cfg); err != nil {
		t.Fatalf("build relational with trimmed provider alias: %v", err)
	}
}

// TestApplicationRunRejectsNilReceiver verifies exported startup fails with one deterministic error instead of panicking when callers invoke it on a nil application pointer.
// TestApplicationRunRejectsNilReceiver 用于验证调用方在空应用指针上触发启动时，会收到确定性错误而不是直接 panic。
func TestApplicationRunRejectsNilReceiver(t *testing.T) {
	var app *Application

	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("expected run error")
	}
	if !strings.Contains(err.Error(), "application is nil") {
		t.Fatalf("unexpected run error: %v", err)
	}
}

// TestApplicationRunRejectsNilServer verifies exported startup rejects incomplete runtime wiring before a nil grpc.Server can panic inside Serve.
// TestApplicationRunRejectsNilServer 用于验证导出启动入口会在 nil grpc.Server 进入 Serve 之前拒绝不完整装配，避免内部 panic。
func TestApplicationRunRejectsNilServer(t *testing.T) {
	app := &Application{
		Config: config.DefaultLocal(),
	}

	err := app.Run(nil)
	if err == nil {
		t.Fatal("expected run error")
	}
	if !strings.Contains(err.Error(), "grpc server is not initialized") {
		t.Fatalf("unexpected run error: %v", err)
	}
}

// TestApplicationShutdownContinuesAfterDependencyError verifies graceful shutdown keeps draining later dependencies even when an earlier shutdown hook fails.
// TestApplicationShutdownContinuesAfterDependencyError 用于验证优雅停机会在前一个依赖关闭失败后继续释放后续依赖。
func TestApplicationShutdownContinuesAfterDependencyError(t *testing.T) {
	cfg := config.DefaultLocal()
	first := &stubShutdowner{err: errors.New("first failed")}
	second := &stubShutdowner{}
	app := &Application{
		Config:    cfg,
		Logger:    nil,
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{second, first},
	}

	err := app.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected shutdown error")
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("expected both shutdowners to be called once, got first=%d second=%d", first.calls, second.calls)
	}
	if !strings.Contains(err.Error(), "shutdown dependency[1]") {
		t.Fatalf("expected aggregated shutdown error to mention failing dependency, got %v", err)
	}
}

// TestApplicationShutdownAllowsNilServer verifies exported shutdown can still drain dependencies when tests or partial construction leave the gRPC server unset.
// TestApplicationShutdownAllowsNilServer 用于验证当测试替身或部分装配过程未设置 gRPC 服务时，导出的 shutdown 仍能继续释放依赖而不崩溃。
func TestApplicationShutdownAllowsNilServer(t *testing.T) {
	shutdowner := &stubShutdowner{}
	app := &Application{
		Shutdowns: []appports.Shutdowner{shutdowner},
	}

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with nil server: %v", err)
	}
	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationShutdownAllowsNilReceiver verifies nil application pointers do not crash shared cleanup paths.
// TestApplicationShutdownAllowsNilReceiver 用于验证空应用指针不会把共享清理路径直接打崩。
func TestApplicationShutdownAllowsNilReceiver(t *testing.T) {
	var app *Application

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown nil application: %v", err)
	}
}

// TestApplicationShutdownSkipsNilShutdowners verifies exported shutdown still drains valid dependencies when a partially assembled runtime leaves nil shutdown slots in the slice.
// TestApplicationShutdownSkipsNilShutdowners 用于验证当部分装配运行时在 Shutdowns 切片中留下 nil 槽位时，导出的 shutdown 仍会跳过空槽并继续释放有效依赖。
func TestApplicationShutdownSkipsNilShutdowners(t *testing.T) {
	shutdowner := &stubShutdowner{}
	app := &Application{
		Shutdowns: []appports.Shutdowner{shutdowner, nil},
	}

	if err := app.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with nil shutdowner: %v", err)
	}
	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationRunDrainsShutdownsAfterExternalServerStop verifies exported startup still drains downstream shutdown hooks when another goroutine stops the gRPC server directly.
// TestApplicationRunDrainsShutdownsAfterExternalServerStop 用于验证当其他 goroutine 直接停止 gRPC 服务时，导出启动入口仍会继续释放下游 shutdown 钩子。
func TestApplicationRunDrainsShutdownsAfterExternalServerStop(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	shutdowner := &stubShutdowner{}
	app := &Application{
		Config:    config.DefaultLocal(),
		Logger:    logx.Default(),
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{shutdowner},
	}
	app.Config.GRPC.ListenAddr = addr

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app.Run(context.Background())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	app.Server.Stop()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run after external stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after external stop")
	}

	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
	}
}

// TestApplicationRunAllowsNilLogger verifies exported startup still remains usable when direct tests or partial runtime assembly omit the logger field.
// TestApplicationRunAllowsNilLogger 用于验证当直接测试或部分运行时装配遗漏 logger 字段时，导出的启动入口仍然可用，不会因为记录启动日志而直接 panic。
func TestApplicationRunAllowsNilLogger(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen addr: %v", err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	shutdowner := &stubShutdowner{}
	app := &Application{
		Config:    config.DefaultLocal(),
		Logger:    nil,
		Server:    grpc.NewServer(),
		Shutdowns: []appports.Shutdowner{shutdowner},
	}
	app.Config.GRPC.ListenAddr = addr

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- app.Run(context.Background())
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening: %v", dialErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	app.Server.Stop()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("run with nil logger: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return with nil logger")
	}

	if shutdowner.calls != 1 {
		t.Fatalf("expected shutdowner to be called once, got %d", shutdowner.calls)
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

// stubShutdowner records shutdown attempts for application lifecycle tests.
// stubShutdowner 用于为应用生命周期测试记录 shutdown 调用次数。
type stubShutdowner struct {
	calls int
	err   error
}

// Shutdown increments the call counter and returns the configured error.
// Shutdown 用于增加调用计数，并返回预设错误。
func (s *stubShutdowner) Shutdown(context.Context) error {
	s.calls++
	return s.err
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
