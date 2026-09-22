// runtime_context_test.go verifies controller request budgets and retryable ownership shutdown in the outbound adapter.
// runtime_context_test.go 验证出站适配层的 Controller 请求预算与可重试所有权释放。
package vldb_controller

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	controllerclient "github.com/OpenVulcan/vldb-controller/client-go/controller"
	pb "github.com/OpenVulcan/vldb-controller/client-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRequestContextWithParent verifies RPC limits without waiting for timers to expire.
// TestRequestContextWithParent 验证 RPC 时限和取消传播，无需等待定时器到期。
func TestRequestContextWithParent(t *testing.T) {
	runtime := &Runtime{requestTimeout: 2 * time.Second}
	start := time.Now()
	ctx, cancel := runtime.RequestContextWithParent(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(start.Add(2*time.Second)) || deadline.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("configured timeout missing: deadline=%v present=%v", deadline, ok)
	}
	parentDeadline := time.Now().Add(time.Second)
	parent, cancelParent := context.WithDeadline(context.Background(), parentDeadline)
	defer cancelParent()
	child, cancelChild := runtime.RequestContextWithParent(parent)
	defer cancelChild()
	if got, ok := child.Deadline(); !ok || !got.Equal(parentDeadline) {
		t.Fatalf("shorter caller deadline lost: %v", got)
	}
	cancelParent()
	if child.Err() != context.Canceled {
		t.Fatalf("caller cancellation lost: %v", child.Err())
	}
	var absent *Runtime
	defaultCtx, release := absent.RequestContextWithParent(nil)
	defer release()
	if _, ok := defaultCtx.Deadline(); !ok {
		t.Fatal("default RPC deadline missing")
	}
}

// shutdownProbeServer injects one failure at each dependency boundary and records cleanup ordering.
// shutdownProbeServer 在每个依赖边界注入一次失败并记录清理顺序。
type shutdownProbeServer struct {
	pb.UnimplementedControllerServiceServer

	mu sync.Mutex

	failLanceDB     int
	failSQLite      int
	failDetach      int
	failUnregister  int
	registers       int
	lanceCalls      int
	sqliteCalls     int
	detachCalls     int
	unregisterCalls int
}

// GetStatus reports a ready service so the SDK can connect without spawning a process.
// GetStatus 返回就绪服务状态，使 SDK 无需启动进程即可连接。
func (*shutdownProbeServer) GetStatus(context.Context, *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	return &pb.GetStatusResponse{Status: &pb.ControllerStatusSnapshot{
		ProcessMode: pb.ControllerProcessMode_CONTROLLER_PROCESS_MODE_SERVICE,
	}}, nil
}

// RegisterClient issues a stable test session identifier.
// RegisterClient 返回稳定的测试会话标识。
func (s *shutdownProbeServer) RegisterClient(context.Context, *pb.RegisterClientRequest) (*pb.RegisterClientResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registers++
	return &pb.RegisterClientResponse{Client: &pb.ClientLeaseSnapshot{ClientSessionId: fmt.Sprintf("shutdown-session-%d", s.registers)}}, nil
}

// UnregisterClient fails a configured number of times so client shutdown retry behavior is observable.
// UnregisterClient 按配置次数失败，以便观察客户端关闭重试行为。
func (s *shutdownProbeServer) UnregisterClient(context.Context, *pb.UnregisterClientRequest) (*pb.UnregisterClientResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregisterCalls++
	if s.failUnregister > 0 {
		s.failUnregister--
		return nil, status.Error(codes.FailedPrecondition, "injected unregister failure")
	}
	return &pb.UnregisterClientResponse{Removed: true}, nil
}

// DisableLanceDb records and optionally rejects LanceDB cleanup.
// DisableLanceDb 记录并可按配置拒绝 LanceDB 清理。
func (s *shutdownProbeServer) DisableLanceDb(context.Context, *pb.DisableBackendRequest) (*pb.DisableBackendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lanceCalls++
	if s.failLanceDB > 0 {
		s.failLanceDB--
		return nil, status.Error(codes.FailedPrecondition, "injected lancedb failure")
	}
	return &pb.DisableBackendResponse{Disabled: true}, nil
}

// DisableSqlite records and optionally rejects SQLite cleanup.
// DisableSqlite 记录并可按配置拒绝 SQLite 清理。
func (s *shutdownProbeServer) DisableSqlite(context.Context, *pb.DisableBackendRequest) (*pb.DisableBackendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sqliteCalls++
	if s.failSQLite > 0 {
		s.failSQLite--
		return nil, status.Error(codes.FailedPrecondition, "injected sqlite failure")
	}
	return &pb.DisableBackendResponse{Disabled: true}, nil
}

// DetachSpace records and optionally rejects controller-space cleanup.
// DetachSpace 记录并可按配置拒绝 controller 空间清理。
func (s *shutdownProbeServer) DetachSpace(context.Context, *pb.DetachSpaceRequest) (*pb.DetachSpaceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detachCalls++
	if s.failDetach > 0 {
		s.failDetach--
		return nil, status.Error(codes.FailedPrecondition, "injected detach failure")
	}
	return &pb.DetachSpaceResponse{Detached: true}, nil
}

// TestRuntimeShutdownRetainsOwnershipUntilEachCleanupStepSucceeds verifies ordered, retryable runtime shutdown.
// TestRuntimeShutdownRetainsOwnershipUntilEachCleanupStepSucceeds 验证按顺序且可重试的运行时关闭。
func TestRuntimeShutdownRetainsOwnershipUntilEachCleanupStepSucceeds(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for controller shutdown test: %v", err)
	}
	server := grpc.NewServer()
	probe := &shutdownProbeServer{failLanceDB: 1}
	pb.RegisterControllerServiceServer(server, probe)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client := controllerclient.New(controllerclient.Config{
		Endpoint:           listener.Addr().String(),
		ConnectTimeout:     time.Second,
		LeaseRenewInterval: time.Hour,
	}, controllerclient.ClientRegistration{ClientName: "shutdown-test", HostKind: "test"})
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), time.Second)
	defer cancelConnect()
	if err := client.Connect(connectCtx); err != nil {
		t.Fatalf("connect test controller client: %v", err)
	}
	runtime := &Runtime{
		client:           client,
		spaceID:          "space",
		sqliteBindingID:  "sqlite",
		lanceDBBindingID: "lancedb",
		sqliteEnabled:    true,
		lanceDBEnabled:   true,
		spaceAttached:    true,
	}

	if err := runtime.Shutdown(context.Background()); err == nil {
		t.Fatal("expected injected LanceDB cleanup failure")
	}
	assertRuntimeCleanupState(t, runtime, true, true, true, false)
	assertShutdownCounts(t, probe, 1, 0, 0, 0)

	probe.mu.Lock()
	probe.failSQLite = 1
	probe.mu.Unlock()
	if err := runtime.Shutdown(context.Background()); err == nil {
		t.Fatal("expected injected SQLite cleanup failure")
	}
	assertRuntimeCleanupState(t, runtime, false, true, true, false)
	assertShutdownCounts(t, probe, 2, 1, 0, 0)

	probe.mu.Lock()
	probe.failDetach = 1
	probe.mu.Unlock()
	if err := runtime.Shutdown(context.Background()); err == nil {
		t.Fatal("expected injected detach failure")
	}
	assertRuntimeCleanupState(t, runtime, false, false, true, false)
	assertShutdownCounts(t, probe, 2, 2, 1, 0)

	probe.mu.Lock()
	probe.failUnregister = 1
	probe.mu.Unlock()
	if err := runtime.Shutdown(context.Background()); err == nil {
		t.Fatal("expected injected client unregister failure")
	}
	assertRuntimeCleanupState(t, runtime, false, false, false, false)
	assertShutdownCounts(t, probe, 2, 2, 2, 1)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.Shutdown(canceled); err != nil {
		t.Fatalf("cleanup must use an independent bounded context: %v", err)
	}
	assertRuntimeCleanupState(t, runtime, false, false, false, true)
	assertShutdownCounts(t, probe, 2, 2, 2, 2)
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	assertShutdownCounts(t, probe, 2, 2, 2, 2)
}

// assertRuntimeCleanupState checks the flags retained after a partial cleanup attempt.
// assertRuntimeCleanupState 检查部分清理尝试后保留的运行时状态标志。
func assertRuntimeCleanupState(t *testing.T, runtime *Runtime, wantLance, wantSQLite, wantAttached, wantClosed bool) {
	t.Helper()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.lanceDBEnabled != wantLance || runtime.sqliteEnabled != wantSQLite || runtime.spaceAttached != wantAttached || runtime.closed != wantClosed {
		t.Fatalf("runtime cleanup state = lance:%v sqlite:%v attached:%v closed:%v, want lance:%v sqlite:%v attached:%v closed:%v", runtime.lanceDBEnabled, runtime.sqliteEnabled, runtime.spaceAttached, runtime.closed, wantLance, wantSQLite, wantAttached, wantClosed)
	}
	if wantClosed && runtime.client != nil {
		t.Fatal("closed runtime retained controller client")
	}
	if !wantClosed && runtime.client == nil {
		t.Fatal("partially closed runtime lost controller client")
	}
}

// assertShutdownCounts verifies that later dependencies are untouched after an earlier failure.
// assertShutdownCounts 验证前置依赖失败后续依赖没有被调用。
func assertShutdownCounts(t *testing.T, probe *shutdownProbeServer, lance, sqlite, detach, unregister int) {
	t.Helper()
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.lanceCalls != lance || probe.sqliteCalls != sqlite || probe.detachCalls != detach || probe.unregisterCalls != unregister {
		t.Fatalf("cleanup calls = lance:%d sqlite:%d detach:%d unregister:%d, want lance:%d sqlite:%d detach:%d unregister:%d", probe.lanceCalls, probe.sqliteCalls, probe.detachCalls, probe.unregisterCalls, lance, sqlite, detach, unregister)
	}
}
