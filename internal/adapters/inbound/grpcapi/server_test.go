// server_test.go verifies the inbound gRPC adapter behavior through an in-memory bufconn server.
// server_test.go 用于通过内存 bufconn 服务验证入站 gRPC 适配层行为。
package grpcapi

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	vmmv1 "github.com/openvulcan/vmm/internal/adapters/inbound/grpcapi/proto/v1"
	"github.com/openvulcan/vmm/internal/adapters/outbound/memory_mock"
	"github.com/openvulcan/vmm/internal/app/usecase"
	"github.com/openvulcan/vmm/internal/logic/processor"
	"github.com/openvulcan/vmm/internal/platform/logx"
	"github.com/openvulcan/vmm/internal/platform/trace"
	"github.com/openvulcan/vmm/internal/platform/xid"
	"github.com/openvulcan/vmm/internal/testutil"
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

// grpcFixture groups the bufconn server, client, and test logger used by one gRPC adapter test.
// grpcFixture 用于聚合单个 gRPC 适配层测试所需的 bufconn 服务、客户端和测试日志器。
type grpcFixture struct {
	client vmmv1.VMMServiceClient
	server *grpc.Server
	conn   *grpc.ClientConn
	logs   *bytes.Buffer
}

// close releases all bufconn resources after one test finishes.
// close 用于在单个测试结束后释放所有 bufconn 资源。
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

// newTestFixture starts one in-memory gRPC server with the requested dependencies.
// newTestFixture 用于按输入依赖启动一个内存中的 gRPC 服务。
func newTestFixture(t *testing.T, deps Dependencies, maxReceiveBytes int) *grpcFixture {
	t.Helper()
	if maxReceiveBytes <= 0 {
		maxReceiveBytes = testBufSize
	}
	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxReceiveBytes),
		grpc.ChainUnaryInterceptor(BuildUnaryInterceptors(deps)...),
	)
	vmmv1.RegisterVMMServiceServer(server, NewServer(deps))
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("bufconn server stopped: %v", err)
		}
	}()
	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(dialer))
	if err != nil {
		server.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
	})
	return &grpcFixture{client: vmmv1.NewVMMServiceClient(conn), server: server, conn: conn}
}

// TestPreCheckRejectsMissingUser verifies the simplified gRPC pre-check contract still rejects empty current user text.
// TestPreCheckRejectsMissingUser 用于验证简化后的 gRPC pre-check 契约仍会拒绝空的当前用户文本。
func TestPreCheckRejectsMissingUser(t *testing.T) {
	fixture := testutil.MustRealRuntimeFixture(t)
	logger := logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"})
	deps := Dependencies{
		IDs:             xid.NewGenerator(),
		PreCheck:        usecase.NewPreCheckUseCase(processor.NewIntentExtractor(fixture.LLM, fixture.Prompts, fixture.Config.LLM.Model, 5), processor.NewContextAssembler(fixture.Prompts, fixture.Config.LLM.Model), fixture.Embedding, memory_mock.NewVectorStore(), memory_mock.NewPersonaProvider(), logger, fixture.Config.PreCheck.IntentTimeout.Duration, 5, 5, float64Ptr(0.4), fixture.Config.Embedding.Model, fixture.Config.Embedding.Dimension),
		Logger:          logger,
		Validator:       NewRequestValidator("compat"),
		PreCheckTimeout: time.Second,
	}
	client := newTestFixture(t, deps, testBufSize).client
	_, err := client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId: "s1",
		UserId:    "u1",
		TeamId:    "t1",
		ProjectId: "p1",
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
	details := st.Details()
	if len(details) == 0 {
		t.Fatal("expected error details")
	}
	info, ok := details[0].(*errdetails.ErrorInfo)
	if !ok || info.Reason != "GRPC_VALIDATION_FAILED" {
		t.Fatalf("unexpected error detail: %#v", details[0])
	}
}

// TestPostActionReturnsAcceptedImmediately verifies the new gRPC post-action method acknowledges before background persistence completes.
// TestPostActionReturnsAcceptedImmediately 用于验证新的 gRPC post-action 方法会在后台持久化完成前立即确认接收。
func TestPostActionReturnsAcceptedImmediately(t *testing.T) {
	started := make(chan usecase.PostActionCommand, 1)
	release := make(chan struct{})
	var logBuf bytes.Buffer
	deps := Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			started <- cmd
			<-release
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		Logger:            logx.New(&logBuf, logx.Config{Level: "info", Format: "text"}),
		Validator:         NewRequestValidator("compat"),
		PostActionTimeout: 3 * time.Second,
	}
	fixture := newTestFixture(t, deps, testBufSize)
	defer close(release)

	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-trace-id", "trace-fixed"))
	resp, err := fixture.client.PostAction(ctx, &vmmv1.PostActionRequest{
		SessionId:        "s1",
		UserContent:      "<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)",
		AssistantContent: "最终回答 " + strings.Repeat("longword ", 1700),
		Timeline: []*vmmv1.PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答 data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAccepted() || resp.GetTraceId() != "trace-fixed" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	select {
	case cmd := <-started:
		if !cmd.SkipNoiseGate {
			t.Fatal("expected timeline-driven request to skip noise gate")
		}
		if len(cmd.RawMessagesSnapshot) != 4 {
			t.Fatalf("snapshot len = %d", len(cmd.RawMessagesSnapshot))
		}
		if got := cmd.RawMessagesSnapshot[0].Content; got != "第一问 [图片: 猫]" {
			t.Fatalf("sanitized user_content = %#v", got)
		}
		if got := cmd.RawMessagesSnapshot[1].Content; got != "中间回答 [图片已过滤]" {
			t.Fatalf("sanitized timeline content = %#v", got)
		}
		assistantText, ok := cmd.RawMessagesSnapshot[3].Content.(string)
		if !ok {
			t.Fatalf("assistant content type = %T", cmd.RawMessagesSnapshot[3].Content)
		}
		if !strings.Contains(assistantText, "[已按 Token 预算截断]") {
			t.Fatalf("assistant content did not include token marker: %q", assistantText)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background processing did not start")
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `msg="post-action received raw"`) {
		t.Fatalf("expected raw receipt log, got %s", logs)
	}
	if !strings.Contains(logs, `msg="post-action received cleaned"`) {
		t.Fatalf("expected cleaned receipt log, got %s", logs)
	}
	if !strings.Contains(logs, `<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)`) {
		t.Fatalf("expected raw payload in logs, got %s", logs)
	}
	if !strings.Contains(logs, `第一问 [图片: 猫]`) {
		t.Fatalf("expected cleaned payload in logs, got %s", logs)
	}
}

// TestChatReturnsScrubbedMessage verifies the chat RPC still returns the scrubbed archive payload.
// TestChatReturnsScrubbedMessage 用于验证 chat RPC 仍会返回脱敏后的归档结果。
func TestChatReturnsScrubbedMessage(t *testing.T) {
	deps := Dependencies{
		IDs: xid.NewGenerator(),
		Chat: chatFunc(func(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error) {
			return usecase.ChatResult{
				SessionID: cmd.SessionID,
				Message:   "你好，我的电话是 [MOBILE_MASKED]",
				Language:  cmd.Language,
				TraceID:   trace.IDFromContext(ctx),
			}, nil
		}),
		Logger:      logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator:   NewRequestValidator("compat"),
		ChatTimeout: time.Second,
	}
	fixture := newTestFixture(t, deps, testBufSize)
	resp, err := fixture.client.Chat(context.Background(), &vmmv1.ChatRequest{
		SessionId:      "s1",
		Message:        "你好，我的电话是 13800138000",
		AcceptLanguage: "zh-CN,zh;q=0.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetLanguage() != "zh-CN" || resp.GetMessage() != "你好，我的电话是 [MOBILE_MASKED]" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestHealthzReturnsTraceHeader verifies that the trace interceptor still exposes one stable trace id to unary responses.
// TestHealthzReturnsTraceHeader 用于验证 trace 拦截器仍会为一元响应暴露稳定 trace id。
func TestHealthzReturnsTraceHeader(t *testing.T) {
	deps := Dependencies{
		IDs:       xid.NewGenerator(),
		Logger:    logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator: NewRequestValidator("compat"),
	}
	fixture := newTestFixture(t, deps, testBufSize)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-trace-id", "trace-z"))
	resp, err := fixture.client.Healthz(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetTraceId() != "trace-z" {
		t.Fatalf("trace id = %q", resp.GetTraceId())
	}
}

// TestServerRejectsOversizedPayload verifies the server-level receive limit blocks messages that exceed the configured size.
// TestServerRejectsOversizedPayload 用于验证服务级接收限制会阻止超过配置大小的消息。
func TestServerRejectsOversizedPayload(t *testing.T) {
	deps := Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{}, nil
		}),
		Logger:    logx.New(&bytes.Buffer{}, logx.Config{Level: "info", Format: "text"}),
		Validator: NewRequestValidator("compat"),
	}
	fixture := newTestFixture(t, deps, 128)
	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "s1",
		UserId:      "u1",
		TeamId:      "t1",
		ProjectId:   "p1",
		UserContent: strings.Repeat("x", 2048),
	})
	if err == nil {
		t.Fatal("expected oversized request error")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// float64Ptr returns one float pointer for test-only wiring.
// float64Ptr 用于为测试接线返回一个浮点数指针。
func float64Ptr(v float64) *float64 { return &v }

// chatFunc adapts one plain function into the ChatExecutor interface.
// chatFunc 用于把普通函数适配成 ChatExecutor 接口。
type chatFunc func(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error)

// Execute delegates chat execution to the wrapped function.
// Execute 用于把 chat 执行转发给包装函数。
func (f chatFunc) Execute(ctx context.Context, cmd usecase.ChatCommand) (usecase.ChatResult, error) {
	return f(ctx, cmd)
}

// preCheckFunc adapts one plain function into the PreCheckExecutor interface.
// preCheckFunc 用于把普通函数适配成 PreCheckExecutor 接口。
type preCheckFunc func(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error)

// Execute delegates pre-check execution to the wrapped function.
// Execute 用于把 pre-check 执行转发给包装函数。
func (f preCheckFunc) Execute(ctx context.Context, cmd usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
	return f(ctx, cmd)
}

// postActionFunc adapts one plain function into the PostActionExecutor interface.
// postActionFunc 用于把普通函数适配成 PostActionExecutor 接口。
type postActionFunc func(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error)

// Execute delegates post-action execution to the wrapped function.
// Execute 用于把 post-action 执行转发给包装函数。
func (f postActionFunc) Execute(ctx context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
	return f(ctx, cmd)
}
