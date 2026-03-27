// server_test.go verifies the inbound gRPC adapter against the latest workspace plus PreCheck/PostAction contract.
// server_test.go 用于围绕最新的空间管理与 PreCheck/PostAction 契约验证入站 gRPC 适配层。
package grpcapi

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	appports "github.com/openvulcan/vmm/internal/app/ports"
	"github.com/openvulcan/vmm/internal/app/usecase"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

const testBufSize = 1 << 20

// grpcFixture groups the bufconn server, client, and test log buffer used by one gRPC adapter test.
// grpcFixture 用于聚合单个 gRPC 适配层测试使用的 bufconn 服务、客户端和测试日志缓冲区。
type grpcFixture struct {
	client vmmv1.VMMServiceClient
	server *grpc.Server
	conn   *grpc.ClientConn
	logs   *bytes.Buffer
}

// close releases all bufconn resources after one test finishes.
// close 用于在测试结束后释放全部 bufconn 资源。
func (f *grpcFixture) close() {
	if f == nil {
		return
	}
	if f.conn != nil {
		_ = f.conn.Close()
	}
	if f.server != nil {
		f.server.Stop()
	}
}

// newTestFixture starts one in-memory gRPC server with the requested dependencies and receive limit.
// newTestFixture 用于按输入依赖和接收限制启动一个内存 gRPC 服务。
func newTestFixture(t *testing.T, deps Dependencies, maxReceiveBytes int) *grpcFixture {
	t.Helper()
	if maxReceiveBytes <= 0 {
		maxReceiveBytes = testBufSize
	}
	logBuf := &bytes.Buffer{}
	if deps.Logger == nil {
		deps.Logger = logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	}
	if deps.Validator == nil {
		deps.Validator = NewRequestValidator()
	}
	if deps.WorkspaceTimeout <= 0 {
		deps.WorkspaceTimeout = time.Second
	}
	if deps.PreCheckTimeout <= 0 {
		deps.PreCheckTimeout = time.Second
	}
	if deps.PostActionTimeout <= 0 {
		deps.PostActionTimeout = time.Second
	}

	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxReceiveBytes),
		grpc.ChainUnaryInterceptor(BuildUnaryInterceptors(deps)...),
	)
	vmmv1.RegisterVMMServiceServer(server, NewServer(deps))
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.DialContext(
		context.Background(),
		"bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		server.Stop()
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
	})
	return &grpcFixture{
		client: vmmv1.NewVMMServiceClient(conn),
		server: server,
		conn:   conn,
		logs:   logBuf,
	}
}

// TestHealthzReturnsTraceHeader verifies the trace interceptor reuses an incoming trace id on unary responses.
// TestHealthzReturnsTraceHeader 用于验证 trace 拦截器会在一元响应里复用传入的 trace id。
func TestHealthzReturnsTraceHeader(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{IDs: xid.NewGenerator()}, testBufSize)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(traceIDHeader, "trace-z"))

	resp, err := fixture.client.Healthz(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	if resp.GetTraceId() != "trace-z" {
		t.Fatalf("trace id = %q", resp.GetTraceId())
	}
}

// TestListProjectsReturnsDisplayPath verifies admin project listing exposes the requested [PROJECT_ID]Team/Space/Project display string.
// TestListProjectsReturnsDisplayPath 用于验证项目列表会输出要求的 [PROJECT_ID]Team/Space/Project 展示字符串。
func TestListProjectsReturnsDisplayPath(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Workspace: &stubWorkspaceExecutor{
			projects: []logicdomain.ProjectRecord{{
				ID:        9,
				TeamID:    3,
				SpaceID:   5,
				TeamName:  "TeamA",
				SpaceName: "SpaceA",
				Name:      "ProjectA",
			}},
		},
	}, testBufSize)

	resp, err := fixture.client.ListProjects(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(resp.GetProjects()) != 1 {
		t.Fatalf("projects len = %d", len(resp.GetProjects()))
	}
	item := resp.GetProjects()[0]
	if item.GetDisplayPath() != "[9]TeamA/SpaceA/ProjectA" {
		t.Fatalf("display path = %q", item.GetDisplayPath())
	}
}

// TestPreCheckRejectsMissingUserContent verifies the latest transport contract rejects empty user content after scope resolution succeeds.
// TestPreCheckRejectsMissingUserContent 用于验证在范围解析成功后，最新传输契约仍会拒绝空 user_content。
func TestPreCheckRejectsMissingUserContent(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:           xid.NewGenerator(),
		PreCheck:      preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) { return usecase.PreCheckResult{}, nil }),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
	})
	if err == nil {
		t.Fatal("expected invalid argument error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("unexpected error type: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("status code = %s", st.Code())
	}
	if len(st.Details()) == 0 {
		t.Fatal("expected error details")
	}
	info, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok || info.Reason != "GRPC_VALIDATION_FAILED" {
		t.Fatalf("unexpected detail: %#v", st.Details()[0])
	}
}

// TestPreCheckUsesResolvedSession verifies the scope interceptor injects one resolved session into the current pre-check use case.
// TestPreCheckUsesResolvedSession 用于验证范围拦截器会把解析后的 session 注入当前 pre-check 用例。
func TestPreCheckUsesResolvedSession(t *testing.T) {
	calls := make(chan usecase.PreCheckCommand, 1)
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(_ context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			calls <- cmd
			return usecase.PreCheckResult{ShouldInject: false}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "当前项目怎么样",
	})
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	select {
	case cmd := <-calls:
		if cmd.Session.SessionID != 41 || cmd.Session.UserID != 7 || cmd.Session.ProjectID != 9 {
			t.Fatalf("unexpected resolved session: %+v", cmd.Session)
		}
		if cmd.UserContent != "当前项目怎么样" {
			t.Fatalf("unexpected user content: %q", cmd.UserContent)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-check use case was not invoked")
	}
}

// TestPostActionReturnsAcceptedImmediately verifies the transport acknowledges first, then continues background persistence with cleaned content.
// TestPostActionReturnsAcceptedImmediately 用于验证传输层会先确认请求，再携带清洗后的内容继续后台持久化。
func TestPostActionReturnsAcceptedImmediately(t *testing.T) {
	started := make(chan usecase.PostActionCommand, 1)
	release := make(chan struct{})
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(_ context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			started <- cmd
			<-release
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)
	defer close(release)

	resp, err := fixture.client.PostAction(context.Background(), &vmmv1.PostActionRequest{
		SessionId:        "sess-1",
		UserId:           7,
		ProjectId:        9,
		UserContent:      "<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)",
		AssistantContent: "最终回答",
		Timeline: []*vmmv1.PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答 data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatalf("post-action: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}
	select {
	case cmd := <-started:
		if cmd.Session.SessionID != 41 || cmd.Session.UserID != 7 || cmd.Session.ProjectID != 9 {
			t.Fatalf("unexpected resolved session: %+v", cmd.Session)
		}
		if cmd.UserContent != "第一问 [图片: 猫]" {
			t.Fatalf("unexpected cleaned user content: %q", cmd.UserContent)
		}
		if len(cmd.Timeline) != 2 {
			t.Fatalf("timeline len = %d", len(cmd.Timeline))
		}
		if cmd.Timeline[0].Content != "中间回答 [图片已过滤]" {
			t.Fatalf("unexpected cleaned timeline content: %q", cmd.Timeline[0].Content)
		}
		if cmd.AssistantContent != "最终回答" {
			t.Fatalf("unexpected cleaned assistant content: %q", cmd.AssistantContent)
		}
	case <-time.After(time.Second):
		t.Fatal("background post-action was not started")
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, `msg="post-action received raw"`) {
		t.Fatalf("expected raw receipt log, got %s", logs)
	}
	if !strings.Contains(logs, `msg="post-action received cleaned"`) {
		t.Fatalf("expected cleaned receipt log, got %s", logs)
	}
	if !strings.Contains(logs, `<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)`) {
		t.Fatalf("expected raw payload log, got %s", logs)
	}
	if !strings.Contains(logs, `第一问 [图片: 猫]`) {
		t.Fatalf("expected cleaned payload log, got %s", logs)
	}
}

// TestServerRejectsOversizedPayload verifies the gRPC receive limit still rejects oversized payloads before business logic runs.
// TestServerRejectsOversizedPayload 用于验证 gRPC 接收限制仍会在业务逻辑执行前拒绝超大载荷。
func TestServerRejectsOversizedPayload(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:           xid.NewGenerator(),
		PreCheck:      preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) { return usecase.PreCheckResult{}, nil }),
		ScopeResolver: stubScopeResolver{},
	}, 128)

	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: strings.Repeat("x", 2048),
	})
	if err == nil {
		t.Fatal("expected oversized request error")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// preCheckFunc adapts a plain function to the current PreCheckExecutor interface.
// preCheckFunc 用于把普通函数适配到当前 PreCheckExecutor 接口。
type preCheckFunc func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error)

// Execute delegates pre-check execution to the wrapped function.
// Execute 用于把 pre-check 执行委托给包装函数。
func (f preCheckFunc) Execute(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
	return f(ctx, cmd)
}

// postActionFunc adapts a plain function to the current PostActionExecutor interface.
// postActionFunc 用于把普通函数适配到当前 PostActionExecutor 接口。
type postActionFunc func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error)

// Execute delegates post-action execution to the wrapped function.
// Execute 用于把 post-action 执行委托给包装函数。
func (f postActionFunc) Execute(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
	return f(ctx, cmd)
}

// stubScopeResolver returns one deterministic resolved session so transport tests can focus on adapter behavior.
// stubScopeResolver 用于返回一个固定的已解析 session，让传输层测试聚焦适配层行为。
type stubScopeResolver struct{}

// ResolveRequestScope returns one deterministic session row for the current test contract.
// ResolveRequestScope 用于为当前测试契约返回一个固定的 session 记录。
func (stubScopeResolver) ResolveRequestScope(_ context.Context, sessionKey string, userID, projectID uint64) (logicdomain.SessionRef, error) {
	return logicdomain.SessionRef{
		SessionID:   41,
		SessionKey:  sessionKey,
		UserID:      userID,
		TeamID:      3,
		SpaceID:     5,
		ProjectID:   projectID,
		UserName:    "alice",
		TeamName:    "TeamA",
		SpaceName:   "SpaceA",
		ProjectName: "ProjectA",
	}, nil
}

// stubWorkspaceExecutor supplies just enough admin behavior for gRPC transport tests.
// stubWorkspaceExecutor 用于为 gRPC 传输测试提供最小但足够的管理行为。
type stubWorkspaceExecutor struct {
	projects []logicdomain.ProjectRecord
}

// ListProjects returns the canned project list for deterministic transport assertions.
// ListProjects 用于返回预置项目列表，保证传输层断言稳定。
func (s *stubWorkspaceExecutor) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProject keeps the test double interface-complete while focused tests only cover project listing.
// ResolveProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) ResolveProject(context.Context, string) (logicdomain.ProjectRecord, error) {
	return logicdomain.ProjectRecord{}, nil
}

// EnsureProject keeps the test double interface-complete while focused tests only cover project listing.
// EnsureProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) EnsureProject(context.Context, string, bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProject keeps the test double interface-complete while focused tests only cover project listing.
// DeleteProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) DeleteProject(context.Context, string, bool) (logicdomain.ProjectDeleteResult, error) {
	return logicdomain.ProjectDeleteResult{}, nil
}

// MigrateProject keeps the test double interface-complete while focused tests only cover project listing.
// MigrateProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) MigrateProject(context.Context, string, string, bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, nil
}

// ResolveUser keeps the test double interface-complete while focused tests only cover project listing.
// ResolveUser 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) ResolveUser(context.Context, string, bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, nil
}

// ListUsers keeps the test double interface-complete while focused tests only cover project listing.
// ListUsers 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) ListUsers(context.Context) ([]logicdomain.UserRecord, error) {
	return nil, nil
}

// DeleteUser keeps the test double interface-complete while focused tests only cover project listing.
// DeleteUser 用于补齐测试替身接口，而当前聚焦测试只覆盖项目列表。
func (s *stubWorkspaceExecutor) DeleteUser(context.Context, string, string) (logicdomain.UserDeleteResult, error) {
	return logicdomain.UserDeleteResult{}, nil
}

var _ appports.RequestScopeResolver = stubScopeResolver{}
