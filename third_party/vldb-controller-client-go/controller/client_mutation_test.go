// Package controller verifies that transport recovery never replays a mutation with an unknown server-side outcome.
// controller 包用于验证传输恢复不会重放服务端结果未知的写操作。
package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/OpenVulcan/vldb-controller/client-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// uncertainMutationServer simulates a mutation that commits before its response transport fails.
// uncertainMutationServer 模拟写操作已经提交、但响应传输失败的服务端。
type uncertainMutationServer struct {
	pb.UnimplementedControllerServiceServer

	// mu protects request counters observed by the test goroutine.
	// mu 保护测试协程读取的请求计数器。
	mu sync.Mutex
	// registrations counts session registrations across recovery.
	// registrations 统计恢复前后的会话注册次数。
	registrations int
	// mutations counts actual mutation executions.
	// mutations 统计写操作的实际执行次数。
	mutations int
}

// GetStatus reports a ready controller so the SDK can complete its endpoint probe.
// GetStatus 返回就绪状态，使 SDK 能够完成端点探测。
func (s *uncertainMutationServer) GetStatus(context.Context, *pb.GetStatusRequest) (*pb.GetStatusResponse, error) {
	return &pb.GetStatusResponse{
		Status: &pb.ControllerStatusSnapshot{
			ProcessMode: pb.ControllerProcessMode_CONTROLLER_PROCESS_MODE_SERVICE,
		},
	}, nil
}

// RegisterClient issues a fresh session identifier for the initial call and the recovery connection.
// RegisterClient 为初始调用与恢复连接分别签发新的会话标识。
func (s *uncertainMutationServer) RegisterClient(context.Context, *pb.RegisterClientRequest) (*pb.RegisterClientResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations++
	return &pb.RegisterClientResponse{
		Client: &pb.ClientLeaseSnapshot{ClientSessionId: fmt.Sprintf("session-%d", s.registrations)},
	}, nil
}

// ExecuteSqliteScript records one committed mutation and then simulates a lost response.
// ExecuteSqliteScript 记录一次已提交写操作，随后模拟响应丢失。
func (s *uncertainMutationServer) ExecuteSqliteScript(context.Context, *pb.ExecuteSqliteScriptRequest) (*pb.ExecuteSqliteScriptResponse, error) {
	s.mu.Lock()
	s.mutations++
	s.mu.Unlock()
	return nil, status.Error(codes.Unavailable, "response lost after commit")
}

// UnregisterClient accepts test cleanup without affecting the mutation assertions.
// UnregisterClient 接受测试清理请求且不影响写操作断言。
func (s *uncertainMutationServer) UnregisterClient(context.Context, *pb.UnregisterClientRequest) (*pb.UnregisterClientResponse, error) {
	return &pb.UnregisterClientResponse{Removed: true}, nil
}

// snapshot returns concurrency-safe registration and mutation counters.
// snapshot 返回并发安全的会话注册数与写操作执行数。
func (s *uncertainMutationServer) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registrations, s.mutations
}

// TestMutationSessionDoesNotReplayAfterRecoverableFailure proves that session recovery and mutation retry are separate operations.
// TestMutationSessionDoesNotReplayAfterRecoverableFailure 证明会话恢复与写操作重试是两个相互独立的动作。
func TestMutationSessionDoesNotReplayAfterRecoverableFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for controller test server: %v", err)
	}
	server := grpc.NewServer()
	fake := &uncertainMutationServer{}
	pb.RegisterControllerServiceServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	client := New(Config{
		Endpoint:           listener.Addr().String(),
		ConnectTimeout:     time.Second,
		LeaseRenewInterval: time.Hour,
	}, ClientRegistration{ClientName: "mutation-test", HostKind: "test"})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = client.Shutdown(ctx)
	})

	_, err = client.ExecuteSqliteScript(context.Background(), &SqliteExecuteScriptRequest{
		SpaceID:   "root",
		BindingID: "sqlite-test",
		SQL:       "INSERT INTO events(value) VALUES ('committed')",
	})
	if !errors.Is(err, ErrMutationOutcomeUncertain) {
		t.Fatalf("expected uncertain mutation outcome, got %v", err)
	}
	var uncertain *MutationOutcomeUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected typed uncertain outcome error, got %T", err)
	}
	if uncertain.RecoveryError != nil {
		t.Fatalf("expected successful session recovery, got %v", uncertain.RecoveryError)
	}
	registrations, mutations := fake.snapshot()
	if registrations != 2 {
		t.Fatalf("expected one initial and one recovery registration, got %d", registrations)
	}
	if mutations != 1 {
		t.Fatalf("mutation must execute exactly once, got %d executions", mutations)
	}
}
