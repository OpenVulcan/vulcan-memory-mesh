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
	"google.golang.org/protobuf/proto"
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
		deps.Logger = logx.New(logBuf, logx.Config{
			Level:         "info",
			Format:        "text",
			DebugPayloads: deps.DebugRPCPayloads,
		})
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

// TestHealthzIgnoresNilExtraInterceptor verifies transport startup filters accidental nil extra interceptors so one configuration hole does not degrade every request into Internal.
// TestHealthzIgnoresNilExtraInterceptor 用于验证传输层启动时会过滤误传入的 nil 额外拦截器，避免一个配置空洞把全部请求都降级成 Internal。
func TestHealthzIgnoresNilExtraInterceptor(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs:               xid.NewGenerator(),
		ExtraInterceptors: []grpc.UnaryServerInterceptor{nil},
	}, testBufSize)

	resp, err := fixture.client.Healthz(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("healthz with nil extra interceptor: %v", err)
	}
	if resp.GetStatus() != "ok" {
		t.Fatalf("status = %q", resp.GetStatus())
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

// TestGetProfileNodesReturnsActiveNodes verifies the profile query RPC returns only the active atomic nodes exposed by the profile use case.
// TestGetProfileNodesReturnsActiveNodes 用于验证画像查询 RPC 会返回画像用例暴露的 active 原子节点。
func TestGetProfileNodesReturnsActiveNodes(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Profiles: &stubProfileExecutor{
			queryResult: usecase.ProfileQueryResult{
				Target: logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
				Nodes: []logicdomain.ProfileNodeRecord{
					{
						ID:            101,
						ProfileType:   logicdomain.ProfileTypeProject,
						BindID:        9,
						Content:       "项目必须优先支持多种 AI 编程工具。",
						Priority:      logicdomain.ProfilePriorityP0,
						ProfileLevel:  logicdomain.ProfileLevelPersistent,
						RefreshWeight: 2,
						ProfileDate:   "2026-03-30",
						SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
						SourceID:      77,
					},
				},
			},
		},
	}, testBufSize)

	resp, err := fixture.client.GetProfileNodes(context.Background(), &vmmv1.GetProfileNodesRequest{
		Target:    vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT,
		ProjectId: 9,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("get profile nodes: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("nodes len = %d", len(resp.GetNodes()))
	}
	if resp.GetNodes()[0].GetProfileNodeId() != 101 || resp.GetNodes()[0].GetPriority() != "P0" || resp.GetNodes()[0].GetLevel() != "L3" {
		t.Fatalf("unexpected profile node entry: %+v", resp.GetNodes()[0])
	}
}

// TestGetProfileNodesNormalizesLegacyProfileDate verifies profile query responses expose the anchored local profile date instead of the raw legacy UTC-truncated stored value.
// TestGetProfileNodesNormalizesLegacyProfileDate 用于验证画像查询响应会返回基于锚点修正后的本地 profile 日期，而不是原始存储的 legacy UTC 截断值。
func TestGetProfileNodesNormalizesLegacyProfileDate(t *testing.T) {
	testutil.UseFixedLocalTime(t, "Asia/Shanghai")
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Profiles: &stubProfileExecutor{
			queryResult: usecase.ProfileQueryResult{
				Target: logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
				Nodes: []logicdomain.ProfileNodeRecord{
					{
						ID:                  102,
						ProfileType:         logicdomain.ProfileTypeProject,
						BindID:              9,
						Content:             "项目必须优先支持多种 AI 编程工具。",
						Priority:            logicdomain.ProfilePriorityP0,
						ProfileLevel:        logicdomain.ProfileLevelPersistent,
						RefreshWeight:       2,
						ProfileDate:         "2026-04-01",
						ProfileDateAnchorAt: time.Date(2026, 4, 1, 16, 30, 0, 0, time.UTC),
						CreatedAt:           time.Date(2026, 4, 2, 3, 0, 0, 0, time.UTC),
						SourceKind:          logicdomain.ProfileSourceKindTurnExtract,
						SourceID:            88,
					},
				},
			},
		},
	}, testBufSize)

	resp, err := fixture.client.GetProfileNodes(context.Background(), &vmmv1.GetProfileNodesRequest{
		Target:    vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT,
		ProjectId: 9,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("get profile nodes: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("nodes len = %d", len(resp.GetNodes()))
	}
	if resp.GetNodes()[0].GetProfileDate() != "2026-04-02" {
		t.Fatalf("expected normalized anchored profile date 2026-04-02, got %+v", resp.GetNodes()[0])
	}
}

// TestGetProfileNodesKeepsUnlimitedLimitWhenOmitted verifies transport normalization does not inject a hidden cap when callers omit limit.
// TestGetProfileNodesKeepsUnlimitedLimitWhenOmitted 用于验证当调用方省略 limit 时，传输层不会偷偷注入隐藏上限。
func TestGetProfileNodesKeepsUnlimitedLimitWhenOmitted(t *testing.T) {
	profiles := &stubProfileExecutor{
		queryResult: usecase.ProfileQueryResult{
			Target: logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeProject, BindID: 9},
			Nodes: []logicdomain.ProfileNodeRecord{{
				ID:            103,
				ProfileType:   logicdomain.ProfileTypeProject,
				BindID:        9,
				Content:       "项目统一使用 Go。",
				Priority:      logicdomain.ProfilePriorityP0,
				ProfileLevel:  logicdomain.ProfileLevelPersistent,
				RefreshWeight: 1,
				ProfileDate:   "2026-04-02",
				SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
				SourceID:      66,
			}},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:      xid.NewGenerator(),
		Profiles: profiles,
	}, testBufSize)

	resp, err := fixture.client.GetProfileNodes(context.Background(), &vmmv1.GetProfileNodesRequest{
		Target:    vmmv1.ProfileTarget_PROFILE_TARGET_PROJECT,
		ProjectId: 9,
	})
	if err != nil {
		t.Fatalf("get profile nodes: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("nodes len = %d", len(resp.GetNodes()))
	}
	if profiles.queryCmd.Limit != 0 {
		t.Fatalf("expected omitted limit to stay unlimited (0), got %+v", profiles.queryCmd)
	}
}

// TestGetProfileNodesPassesAllTargetSentinel verifies the transport exposes an explicit ALL target without reusing the legacy zero-value target.
// TestGetProfileNodesPassesAllTargetSentinel 用于验证传输层暴露显式 ALL 目标，并且不会复用旧有零值 target。
func TestGetProfileNodesPassesAllTargetSentinel(t *testing.T) {
	profiles := &stubProfileExecutor{
		queryResult: usecase.ProfileQueryResult{
			Nodes: []logicdomain.ProfileNodeRecord{
				{
					ID:            104,
					ProfileType:   logicdomain.ProfileTypeTeam,
					BindID:        3,
					Content:       "团队统一使用中文提交信息。",
					Priority:      logicdomain.ProfilePriorityP0,
					ProfileLevel:  logicdomain.ProfileLevelPersistent,
					RefreshWeight: 1,
					ProfileDate:   "2026-05-31",
					SourceKind:    logicdomain.ProfileSourceKindTurnExtract,
					SourceID:      44,
				},
				{
					ID:            105,
					ProfileType:   logicdomain.ProfileTypeUser,
					BindID:        7,
					Content:       "用户偏好先给结论。",
					Priority:      logicdomain.ProfilePriorityP1,
					ProfileLevel:  logicdomain.ProfileLevelStable,
					RefreshWeight: 2,
					ProfileDate:   "2026-05-31",
					SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
					SourceID:      45,
				},
			},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:      xid.NewGenerator(),
		Profiles: profiles,
	}, testBufSize)

	resp, err := fixture.client.GetProfileNodes(context.Background(), &vmmv1.GetProfileNodesRequest{
		Target:    vmmv1.ProfileTarget_PROFILE_TARGET_ALL,
		UserId:    7,
		ProjectId: 9,
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("get all profile nodes: %v", err)
	}
	if profiles.queryCmd.ProfileType != usecase.ProfileQueryTypeAll || profiles.queryCmd.UserID != 7 || profiles.queryCmd.ProjectID != 9 || profiles.queryCmd.Limit != 20 {
		t.Fatalf("unexpected all target query command: %+v", profiles.queryCmd)
	}
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("nodes len = %d", len(resp.GetNodes()))
	}
	if resp.GetNodes()[0].GetTarget() != vmmv1.ProfileTarget_PROFILE_TARGET_TEAM || resp.GetNodes()[1].GetTarget() != vmmv1.ProfileTarget_PROFILE_TARGET_USER {
		t.Fatalf("unexpected returned node targets: %+v", resp.GetNodes())
	}
}

// TestApplyProfileInstructionReturnsAcceptedAndRetired verifies the manual profile instruction RPC returns the synchronous reviewed writeback result.
// TestApplyProfileInstructionReturnsAcceptedAndRetired 用于验证手工画像指令 RPC 会返回同步评审后的写回结果。
func TestApplyProfileInstructionReturnsAcceptedAndRetired(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Profiles: &stubProfileExecutor{
			applyResult: usecase.ProfileInstructionResult{
				Target:        logicdomain.ProfileTargetRef{ProfileType: logicdomain.ProfileTypeTeam, BindID: 3},
				InstructionID: 77,
				AcceptedNodes: []logicdomain.ProfileNodeRecord{
					{
						ID:            201,
						ProfileType:   logicdomain.ProfileTypeTeam,
						BindID:        3,
						Content:       "团队服务端统一使用 Go 语言实现。",
						Priority:      logicdomain.ProfilePriorityP0,
						ProfileLevel:  logicdomain.ProfileLevelPersistent,
						RefreshWeight: 3,
						ProfileDate:   "2026-03-30",
						SourceKind:    logicdomain.ProfileSourceKindManualInstruction,
						SourceID:      77,
					},
				},
				RetiredNodes: []logicdomain.ProfileRetireDecision{
					{NodeID: 41, Reason: "新的团队级规范覆盖旧语言约定。"},
				},
				ReviewReason: "团队显式指令应作为最高权威规则。",
			},
		},
	}, testBufSize)

	resp, err := fixture.client.ApplyProfileInstruction(context.Background(), &vmmv1.ApplyProfileInstructionRequest{
		Target:      vmmv1.ProfileTarget_PROFILE_TARGET_TEAM,
		ProjectId:   9,
		Instruction: "以后团队服务端统一使用 Go。",
	})
	if err != nil {
		t.Fatalf("apply profile instruction: %v", err)
	}
	if resp.GetInstructionId() != 77 || len(resp.GetAcceptedNodes()) != 1 || len(resp.GetRetiredNodes()) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.GetAcceptedNodes()[0].GetTarget() != vmmv1.ProfileTarget_PROFILE_TARGET_TEAM || resp.GetAcceptedNodes()[0].GetSourceKind() != vmmv1.ProfileNodeSourceKind_PROFILE_NODE_SOURCE_KIND_MANUAL_INSTRUCTION {
		t.Fatalf("unexpected accepted node: %+v", resp.GetAcceptedNodes()[0])
	}
	if resp.GetRetiredNodes()[0].GetProfileNodeId() != 41 || resp.GetRetiredNodes()[0].GetReason() == "" {
		t.Fatalf("unexpected retired node: %+v", resp.GetRetiredNodes()[0])
	}
}

// TestSearchMemoryEventsReturnsHits verifies the simplified memory-search RPC keeps only AI-facing fields such as memory_id, optional source_turn_id, abstract, preview, and category label.
// TestSearchMemoryEventsReturnsHits 用于验证简化后的记忆检索 RPC 只返回 AI 需要的字段，例如 memory_id、可选 source_turn_id、摘要、预览和英文分类标签。
func TestSearchMemoryEventsReturnsHits(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Memory: &stubMemoryExecutor{
			searchResult: usecase.MemoryQueryResult{
				Results: []usecase.MemoryQueryGroupResult{
					{
						QueryIndex: 0,
						Query:      "喜欢的水果",
						Hits: []usecase.MemoryQueryHit{
							{
								MemoryRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 201},
								SourceRef:      logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeTurn, ID: 41},
								SourceKind:     logicdomain.MemorySourceKindTurnExtract,
								ScopeLevel:     logicdomain.MemoryScopeLevelProject,
								SessionID:      12,
								Abstract:       "用户喜欢吃香蕉。",
								DetailsPreview: "来自近期饮食偏好提炼。",
								Category:       3,
								Score:          0.91,
								CreatedAt:      time.UnixMilli(1775000002000),
							},
						},
					},
				},
			},
		},
	}, testBufSize)

	resp, err := fixture.client.SearchMemoryEvents(context.Background(), &vmmv1.SearchMemoryEventsRequest{
		UserId:    7,
		ProjectId: 9,
		Queries:   []string{"喜欢的水果"},
		TopK:      5,
	})
	if err != nil {
		t.Fatalf("search memory events: %v", err)
	}
	if len(resp.GetResults()) != 1 || resp.GetResults()[0].GetQuery() != "喜欢的水果" || len(resp.GetResults()[0].GetHits()) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	hit := resp.GetResults()[0].GetHits()[0]
	if hit.GetMemoryId() != 201 {
		t.Fatalf("unexpected memory id: %+v", hit)
	}
	if hit.GetSourceTurnId() != 41 {
		t.Fatalf("unexpected source turn id: %+v", hit)
	}
	if hit.GetAbstract() != "用户喜欢吃香蕉。" || hit.GetDetailsPreview() != "来自近期饮食偏好提炼。" {
		t.Fatalf("unexpected abstract/preview: %+v", hit)
	}
	if hit.GetCategory() != "business_logic" {
		t.Fatalf("unexpected category label: %+v", hit)
	}
	if hit.GetCreatedDatetime() != logicdomain.FormatDisplayDateTime(time.UnixMilli(1775000002000)) {
		t.Fatalf("unexpected created datetime: %+v", hit)
	}
}

// TestGetTurnDetailsReturnsRows verifies the turn-detail RPC returns structured AI-facing fields so callers do not need to parse dehydrated storage JSON themselves.
// TestGetTurnDetailsReturnsRows 用于验证 turn 详情 RPC 会直接返回面向 AI 的结构化字段，避免调用方再自行解析脱水存储 JSON。
func TestGetTurnDetailsReturnsRows(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Memory: &stubMemoryExecutor{
			turnResult: usecase.TurnDetailResult{
				Turns: []usecase.TurnDetailRecord{
					{
						Turn: logicdomain.SessionTurnRecord{
							ID:                41,
							SessionID:         12,
							ProjectID:         9,
							DehydratedContent: `{"user":"我喜欢香蕉"}`,
							DehydratedBudget:  32,
							ExtractedStatus:   1,
							Details:           "近期饮食偏好提炼。",
							DetailsBudget:     9,
							CreatedAt:         time.UnixMilli(1775000000000),
							UpdatedAt:         time.UnixMilli(1775000001000),
						},
						UserContent:      "我喜欢香蕉",
						Timeline:         []logicdomain.TurnDetailTimelineItem{{Type: "assistant", Content: "中间确认"}},
						AssistantContent: "收到",
						PreviousTurnIDs:  []uint64{38, 39, 40},
						NextTurnIDs:      []uint64{42, 43, 44},
					},
				},
			},
		},
	}, testBufSize)

	resp, err := fixture.client.GetTurnDetails(context.Background(), &vmmv1.GetTurnDetailsRequest{
		TurnIds: []uint64{41},
	})
	if err != nil {
		t.Fatalf("get turn details: %v", err)
	}
	if len(resp.GetTurns()) != 1 || resp.GetTurns()[0].GetTurnId() != 41 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.GetTurns()[0].GetUserQuestion() != "我喜欢香蕉" || resp.GetTurns()[0].GetAssistantAnswer() != "收到" {
		t.Fatalf("expected parsed conversation content, got %+v", resp.GetTurns()[0])
	}
	if resp.GetTurns()[0].GetDetail() != "近期饮食偏好提炼。" {
		t.Fatalf("expected parsed turn detail, got %+v", resp.GetTurns()[0])
	}
	if len(resp.GetTurns()[0].GetPreviousTurnIds()) != 3 || len(resp.GetTurns()[0].GetNextTurnIds()) != 3 || len(resp.GetTurns()[0].GetTimeline()) != 1 {
		t.Fatalf("expected neighboring turn ids and timeline, got %+v", resp.GetTurns()[0])
	}
}

// TestWriteMemoriesPersistsResolvedSession verifies the direct-write RPC accepts compact numeric concepts, computes lifecycle server-side, and returns only the created memory ids.
// TestWriteMemoriesPersistsResolvedSession 用于验证主动写记忆 RPC 接收紧凑数字概念值、由服务端计算生命周期，并且只返回新建 memory id。
func TestWriteMemoriesPersistsResolvedSession(t *testing.T) {
	memory := &stubMemoryExecutor{
		writeResult: usecase.WriteMemoriesResult{
			Items: []usecase.WriteMemoryResultItem{
				{
					Ref:        logicdomain.MemoryRef{Type: logicdomain.MemoryRefTypeMemory, ID: 301},
					SourceKind: logicdomain.MemorySourceKindGRPCAIWrite,
					ScopeLevel: logicdomain.MemoryScopeLevelProject,
					Deduped:    false,
				},
			},
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:           xid.NewGenerator(),
		Memory:        memory,
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

	resp, err := fixture.client.WriteMemories(context.Background(), &vmmv1.WriteMemoriesRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		Items: []*vmmv1.WriteMemoryItem{
			{
				ScopeLevel:  2,
				Abstract:    "用户喜欢吃香蕉。",
				Details:     "由工作态 AI 工具主动写入。",
				Category:    3,
				Priority:    2,
				MemoryLevel: 3,
			},
		},
	})
	if err != nil {
		t.Fatalf("write memories: %v", err)
	}
	if len(resp.GetItems()) != 1 || resp.GetItems()[0].GetMemoryId() != 301 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if memory.writeCmd.Session.SessionID != 41 || memory.writeCmd.Session.ProjectID != 9 || memory.writeCmd.Session.UserID != 7 {
		t.Fatalf("unexpected write session: %+v", memory.writeCmd.Session)
	}
	if len(memory.writeCmd.Items) != 1 || memory.writeCmd.Items[0].ScopeLevel != logicdomain.MemoryScopeLevelProject {
		t.Fatalf("unexpected write command: %+v", memory.writeCmd)
	}
}

// TestWriteMemoriesRejectsNilItem verifies the transport returns one validation error instead of panicking when direct tests or manual integrations pass a nil write item inside the repeated payload.
// TestWriteMemoriesRejectsNilItem 用于验证当直接测试或手工集成在 repeated 载荷中传入 nil 写入项时，传输层会返回校验错误，而不是直接 panic。
func TestWriteMemoriesRejectsNilItem(t *testing.T) {
	server := NewServer(Dependencies{
		Memory: &stubMemoryExecutor{},
	})

	_, err := server.WriteMemories(context.Background(), &vmmv1.WriteMemoriesRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
		Items:     []*vmmv1.WriteMemoryItem{nil},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestGetProfileBundleReturnsCombinedPrompt verifies the bundle RPC returns the authoritative combined prompt text in full mode.
// TestGetProfileBundleReturnsCombinedPrompt 用于验证 bundle RPC 在 full 模式下返回权威的组合提示词文本。
func TestGetProfileBundleReturnsCombinedPrompt(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Profiles: &stubProfileExecutor{
			bundleResult: usecase.ProfileBundleResult{
				Mode:               usecase.ProfileBundleModeFull,
				IncludeExplanation: true,
				CombinedText:       "以下内容是结合用户历史习惯、偏好、设定总结的画像。\n\n以下是等级与偏好权重说明：\n...\n\n以下是你当前所处的项目环境约束（优先级：Project > Space > Team）：\n[TEAM]\n团队画像\n\n[PROJECT]\n项目画像\n\n以下是你当前正在服务的目标用户偏好（请在不违反环境约束的前提下，尽量迎合用户）：\n[USER]\n用户画像",
			},
		},
	}, testBufSize)

	resp, err := fixture.client.GetProfileBundle(context.Background(), &vmmv1.GetProfileBundleRequest{
		UserId:             7,
		ProjectId:          9,
		Mode:               vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL,
		IncludeExplanation: proto.Bool(true),
	})
	if err != nil {
		t.Fatalf("get profile bundle: %v", err)
	}
	if resp.GetMode() != vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL || !resp.GetIncludeExplanation() {
		t.Fatalf("unexpected mode flags: %+v", resp)
	}
	if !strings.Contains(resp.GetCombinedText(), "[TEAM]") || !strings.Contains(resp.GetCombinedText(), "[PROJECT]") || !strings.Contains(resp.GetCombinedText(), "[USER]") {
		t.Fatalf("unexpected combined text: %q", resp.GetCombinedText())
	}
	if resp.GetEnvironmentPriorityText() != "" || resp.GetExplanationText() != "" {
		t.Fatalf("expected helper texts to stay empty in full mode, got %+v", resp)
	}
	if resp.GetTeamProfile() != "" || resp.GetProjectProfile() != "" || resp.GetUserProfile() != "" || resp.GetSpaceProfile() != "" {
		t.Fatalf("expected split scope fields to stay empty in full mode, got %+v", resp)
	}
}

// TestGetProfileBundleDefaultsExplanationForFullMode verifies transport normalization enables the inline P/L/W explanation by default for full bundles.
// TestGetProfileBundleDefaultsExplanationForFullMode 用于验证传输层规范化会为 full bundle 默认开启内联的 P/L/W 说明。
func TestGetProfileBundleDefaultsExplanationForFullMode(t *testing.T) {
	profiles := &stubProfileExecutor{
		bundleResult: usecase.ProfileBundleResult{
			Mode:               usecase.ProfileBundleModeFull,
			IncludeExplanation: true,
			CombinedText:       "组合结果",
		},
	}
	fixture := newTestFixture(t, Dependencies{
		IDs:      xid.NewGenerator(),
		Profiles: profiles,
	}, testBufSize)

	resp, err := fixture.client.GetProfileBundle(context.Background(), &vmmv1.GetProfileBundleRequest{
		UserId:    7,
		ProjectId: 9,
		Mode:      vmmv1.ProfileBundleMode_PROFILE_BUNDLE_MODE_FULL,
	})
	if err != nil {
		t.Fatalf("get profile bundle with default explanation: %v", err)
	}
	if !profiles.bundleCmd.IncludeExplanation {
		t.Fatalf("expected full mode to default include_explanation=true, got %+v", profiles.bundleCmd)
	}
	if !resp.GetIncludeExplanation() {
		t.Fatalf("expected response include_explanation=true, got %+v", resp)
	}
}

// TestPreCheckRejectsMissingUserContent verifies the latest transport contract rejects empty user content after scope resolution succeeds.
// TestPreCheckRejectsMissingUserContent 用于验证在范围解析成功后，最新传输契约仍会拒绝空 user_content。
func TestPreCheckRejectsMissingUserContent(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{}, nil
		}),
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
		if cmd.RecallMode != usecase.PreCheckRecallModeLegacy {
			t.Fatalf("expected default recall mode legacy, got %d", cmd.RecallMode)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-check use case was not invoked")
	}
}

// TestPreCheckPassesRecallMode verifies the transport forwards one explicit recall mode into the use case without rewriting the caller's selected strategy.
// TestPreCheckPassesRecallMode 用于验证传输层会把显式 recall mode 原样透传到用例，而不会重写调用方选择的策略。
func TestPreCheckPassesRecallMode(t *testing.T) {
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
		RecallMode:  vmmv1.PreCheckRecallMode_PRE_CHECK_RECALL_MODE_SESSION_COMPACT,
	})
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	select {
	case cmd := <-calls:
		if cmd.RecallMode != usecase.PreCheckRecallModeSessionCompact {
			t.Fatalf("expected session compact recall mode, got %d", cmd.RecallMode)
		}
	case <-time.After(time.Second):
		t.Fatal("pre-check use case was not invoked")
	}
}

// TestChatCompactUsesResolvedSession verifies the scope interceptor injects one resolved session into the compact acknowledgement use case.
// TestChatCompactUsesResolvedSession 用于验证范围拦截器会把解析后的 session 注入 compact 确认用例。
func TestChatCompactUsesResolvedSession(t *testing.T) {
	calls := make(chan usecase.ChatCompactCommand, 1)
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		ChatCompact: chatCompactFunc(func(_ context.Context, cmd usecase.ChatCompactCommand) (usecase.ChatCompactResult, error) {
			calls <- cmd
			return usecase.ChatCompactResult{Accepted: true, Updated: true, CompactedTurnID: 88}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

	resp, err := fixture.client.ChatCompact(context.Background(), &vmmv1.ChatCompactRequest{
		SessionId: "sess-1",
		UserId:    7,
		ProjectId: 9,
	})
	if err != nil {
		t.Fatalf("chat compact: %v", err)
	}
	if !resp.GetAccepted() || !resp.GetUpdated() || resp.GetCompactedTurnId() != 88 {
		t.Fatalf("unexpected chat compact response: %+v", resp)
	}
	select {
	case cmd := <-calls:
		if cmd.Session.SessionID != 41 || cmd.Session.UserID != 7 || cmd.Session.ProjectID != 9 {
			t.Fatalf("unexpected resolved session: %+v", cmd.Session)
		}
	case <-time.After(time.Second):
		t.Fatal("chat compact use case was not invoked")
	}
}

// TestPostActionReturnsAcceptedSynchronously verifies the transport now waits for the synchronous post-action flow while still forwarding cleaned content.
// TestPostActionReturnsAcceptedSynchronously 用于验证传输层现在会等待同步 post-action 流程完成，同时继续转发清洗后的内容。
func TestPostActionReturnsAcceptedSynchronously(t *testing.T) {
	var received usecase.PostActionCommand
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(_ context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			received = cmd
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

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
	if received.Session.SessionID != 41 || received.Session.UserID != 7 || received.Session.ProjectID != 9 {
		t.Fatalf("unexpected resolved session: %+v", received.Session)
	}
	if received.UserContent != "第一问 [Image: 猫]" {
		t.Fatalf("unexpected cleaned user content: %q", received.UserContent)
	}
	if len(received.Timeline) != 2 {
		t.Fatalf("timeline len = %d", len(received.Timeline))
	}
	if received.Timeline[0].Content != "中间回答 [Image filtered]" {
		t.Fatalf("unexpected cleaned timeline content: %q", received.Timeline[0].Content)
	}
	if received.AssistantContent != "最终回答" {
		t.Fatalf("unexpected cleaned assistant content: %q", received.AssistantContent)
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, `MSG："post-action received raw"`) {
		t.Fatalf("expected raw receipt log, got %s", logs)
	}
	if !strings.Contains(logs, `MSG："post-action received cleaned"`) {
		t.Fatalf("expected cleaned receipt log, got %s", logs)
	}
	if strings.Contains(logs, `<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)`) {
		t.Fatalf("expected raw payload to stay out of logs, got %s", logs)
	}
	if strings.Contains(logs, `第一问 [Image: 猫]`) {
		t.Fatalf("expected cleaned payload text to stay out of logs, got %s", logs)
	}
	if !strings.Contains(logs, `user_content_present`) || !strings.Contains(logs, `assistant_content_present`) || !strings.Contains(logs, `timeline_nonempty_items`) {
		t.Fatalf("expected redacted payload metadata in logs, got %s", logs)
	}
	if strings.Contains(logs, `user_content_sha256`) || strings.Contains(logs, `assistant_content_sha256`) || strings.Contains(logs, `timeline_sha256`) {
		t.Fatalf("expected receipt logs to avoid stable payload digests, got %s", logs)
	}
}

// TestPreCheckKeepsPayloadLogsRedactedByDefault verifies the shared RPC payload debug switch stays secure-by-default for pre-check traffic and only records safe metadata when callers do not explicitly opt in.
// TestPreCheckKeepsPayloadLogsRedactedByDefault 用于验证共享 RPC 载荷调试开关在 pre-check 链路上默认仍保持安全输出；调用方未显式开启时，只会记录安全元信息。
func TestPreCheckKeepsPayloadLogsRedactedByDefault(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{
				ShouldInject: true,
				ContextText:  "项目画像：SQLite schema 13 已迁移",
				ContextItems: []logicdomain.ContextItem{
					{Kind: "memory", Title: "混合召回记忆", Text: "请优先检查 FTS 表是否已重建", Source: "memory", Score: 0.92},
				},
				Degraded: false,
			}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)

	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "继续昨天关于 SQLite schema 13 的排查",
	})
	if err != nil {
		t.Fatalf("pre-check: %v", err)
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, `MSG："pre-check received"`) || !strings.Contains(logs, `MSG："pre-check returned"`) {
		t.Fatalf("expected pre-check request and response logs, got %s", logs)
	}
	if strings.Contains(logs, `继续昨天关于 SQLite schema 13 的排查`) || strings.Contains(logs, `项目画像：SQLite schema 13 已迁移`) {
		t.Fatalf("expected pre-check payloads to stay out of default logs, got %s", logs)
	}
	if !strings.Contains(logs, `user_content_present`) || !strings.Contains(logs, `context_nonempty_items`) {
		t.Fatalf("expected redacted pre-check payload metadata, got %s", logs)
	}
	if strings.Contains(logs, `context_items_json`) {
		t.Fatalf("expected default logs to avoid serialized context items, got %s", logs)
	}
}

// TestPreCheckLogsFullPayloadsWhenDebugSwitchEnabled verifies the shared RPC payload debug switch can opt pre-check request and assembled context output back into full logs for local troubleshooting.
// TestPreCheckLogsFullPayloadsWhenDebugSwitchEnabled 用于验证共享 RPC 载荷调试开关开启后，pre-check 的请求与组装上下文输出会重新写入完整日志，方便本地排障。
func TestPreCheckLogsFullPayloadsWhenDebugSwitchEnabled(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{
				ShouldInject: true,
				ContextText:  "项目画像：SQLite schema 13 已迁移",
				ContextItems: []logicdomain.ContextItem{
					{Kind: "memory", Title: "混合召回记忆", Text: "请优先检查 FTS 表是否已重建", Source: "memory", Score: 0.92, TurnID: 41, CreatedTimestamp: 1775000003000, CreatedDateTime: logicdomain.FormatDisplayDateTime(time.UnixMilli(1775000003000))},
				},
				Degraded: true,
			}, nil
		}),
		ScopeResolver:    stubScopeResolver{},
		DebugRPCPayloads: true,
	}, testBufSize)

	resp, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "继续昨天关于 SQLite schema 13 的排查",
	})
	if err != nil {
		t.Fatalf("pre-check with debug payload logs: %v", err)
	}
	if len(resp.GetContextItems()) != 1 {
		t.Fatalf("expected one grpc context item, got %+v", resp)
	}
	if resp.GetContextItems()[0].GetTurnId() != 41 || !resp.GetContextItems()[0].GetHasDialogue() {
		t.Fatalf("expected grpc context item to expose turn linkage, got %+v", resp.GetContextItems()[0])
	}
	if resp.GetContextItems()[0].GetCreatedDatetime() != logicdomain.FormatDisplayDateTime(time.UnixMilli(1775000003000)) {
		t.Fatalf("expected grpc context item to expose created datetime, got %+v", resp.GetContextItems()[0])
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, "JSON(request_payload)：") || !strings.Contains(logs, "继续昨天关于 SQLite schema 13 的排查") {
		t.Fatalf("expected full pre-check request payload in debug logs, got %s", logs)
	}
	if !strings.Contains(logs, "JSON(response_payload)：") || !strings.Contains(logs, `请优先检查 FTS 表是否已重建`) {
		t.Fatalf("expected full pre-check response payload fields in debug logs, got %s", logs)
	}
	if strings.Contains(logs, "项目画像：SQLite schema 13 已迁移") || strings.Contains(logs, `混合召回记忆`) || strings.Contains(logs, `"title":`) || strings.Contains(logs, `"source":`) || strings.Contains(logs, `"kind":`) {
		t.Fatalf("expected debug response payload to omit deprecated pre-check fields, got %s", logs)
	}
	if !strings.Contains(logs, `"has_dialogue": true`) || !strings.Contains(logs, `"turn_id": 41`) || !strings.Contains(logs, `"created_datetime":`) {
		t.Fatalf("expected debug response payload to expose transport turn linkage fields, got %s", logs)
	}
	if !strings.Contains(logs, `user_content_present`) || strings.Contains(logs, `context_text_present`) {
		t.Fatalf("expected debug logs to keep safe summary fields alongside payloads, got %s", logs)
	}
}

// TestPreCheckLogsProtectedPayloadsWhenProtectionEnabled verifies pre-check request and response logs keep plaintext out of default operator output while still persisting encrypted audit envelopes when payload protection is enabled.
// TestPreCheckLogsProtectedPayloadsWhenProtectionEnabled 用于验证启用受保护载荷后，pre-check 请求与返回日志会把明文排除在默认运维输出之外，同时仍保留加密审计信封。
func TestPreCheckLogsProtectedPayloadsWhenProtectionEnabled(t *testing.T) {
	logBuf := &bytes.Buffer{}
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		Logger: logx.New(logBuf, logx.Config{
			Level:                "info",
			Format:               "text",
			ProtectPayloads:      true,
			PayloadEncryptionKey: "0123456789abcdef0123456789abcdef",
		}),
		PreCheck: preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{
				ShouldInject: true,
				ContextText:  "项目记忆：SQLite schema 13 已迁移",
				ContextItems: []logicdomain.ContextItem{
					{Kind: "memory", Title: "混合召回记忆", Text: "请优先检查 FTS 表是否已重建", Source: "memory", Score: 0.92},
				},
				Degraded: false,
			}, nil
		}),
		ScopeResolver: stubScopeResolver{},
	}, testBufSize)
	fixture.logs = logBuf

	_, err := fixture.client.PreCheck(context.Background(), &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "继续昨天关于 SQLite schema 13 的排查",
	})
	if err != nil {
		t.Fatalf("pre-check with protected payload logs: %v", err)
	}
	logs := logBuf.String()
	if strings.Contains(logs, `继续昨天关于 SQLite schema 13 的排查`) || strings.Contains(logs, `项目记忆：SQLite schema 13 已迁移`) {
		t.Fatalf("expected protected pre-check logs to hide plaintext payloads, got %s", logs)
	}
	if !strings.Contains(logs, "JSON(request_payload_protected)：") || !strings.Contains(logs, "JSON(response_payload_protected)：") {
		t.Fatalf("expected protected payload envelopes in pre-check logs, got %s", logs)
	}
	if !strings.Contains(logs, `"algorithm": "AES-256-GCM"`) || !strings.Contains(logs, `"ciphertext":`) {
		t.Fatalf("expected AES-256-GCM protected payload metadata, got %s", logs)
	}
}

// TestPostActionLogsFullPayloadsWhenDebugSwitchEnabled verifies one explicit debug-only switch can opt receipt logs back into raw payload output for local troubleshooting.
// TestPostActionLogsFullPayloadsWhenDebugSwitchEnabled 用于验证显式调试开关开启后，收据日志会重新输出原始载荷，方便本地排障。
func TestPostActionLogsFullPayloadsWhenDebugSwitchEnabled(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PostAction: postActionFunc(func(_ context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			return usecase.PostActionResult{Accepted: true}, nil
		}),
		ScopeResolver:    stubScopeResolver{},
		DebugRPCPayloads: true,
	}, testBufSize)

	_, err := fixture.client.PostAction(context.Background(), &vmmv1.PostActionRequest{
		SessionId:        "sess-1",
		UserId:           7,
		ProjectId:        9,
		UserContent:      "<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)",
		AssistantContent: "最终回答",
		Timeline: []*vmmv1.PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答 data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"},
		},
	})
	if err != nil {
		t.Fatalf("post-action with debug payload logs: %v", err)
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, "TEXT(user_content)：") || !strings.Contains(logs, `assistant_content："最终回答"`) || !strings.Contains(logs, "JSON(timeline_json)：") {
		t.Fatalf("expected full payload fields in debug logs, got %s", logs)
	}
	if !strings.Contains(logs, `<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)`) {
		t.Fatalf("expected raw user payload in debug logs, got %s", logs)
	}
	if strings.Contains(logs, `user_content_present`) || strings.Contains(logs, `timeline_marshaled`) {
		t.Fatalf("expected debug logs to bypass redacted receipt fields, got %s", logs)
	}
}

// TestPostActionDirectCallUsesDefaultSanitizer verifies one partially constructed server still applies the default storage sanitizer when direct tests or manual integrations bypass NewServer and call PostAction directly.
// TestPostActionDirectCallUsesDefaultSanitizer 用于验证当直接测试或手工集成绕过 NewServer 并直接调用 PostAction 时，部分装配的服务实例仍会应用默认的存储型清洗器。
func TestPostActionDirectCallUsesDefaultSanitizer(t *testing.T) {
	var received usecase.PostActionCommand
	server := &Server{
		postAction: postActionFunc(func(_ context.Context, cmd usecase.PostActionCommand) (usecase.PostActionResult, error) {
			received = cmd
			return usecase.PostActionResult{Accepted: true}, nil
		}),
	}
	ctx := withResolvedSessionRef(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-1",
		UserID:     7,
		ProjectID:  9,
	})

	resp, err := server.PostAction(ctx, &vmmv1.PostActionRequest{
		SessionId:        "sess-1",
		UserId:           7,
		ProjectId:        9,
		UserContent:      "<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)",
		AssistantContent: "最终回答",
		Timeline: []*vmmv1.PostActionTimelineItem{
			{Type: "assistant", Content: "中间回答 data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAUA"},
		},
	})
	if err != nil {
		t.Fatalf("post-action direct call: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatal("expected accepted=true")
	}
	if received.UserContent != "第一问 [Image: 猫]" {
		t.Fatalf("unexpected cleaned user content: %q", received.UserContent)
	}
	if len(received.Timeline) != 1 || received.Timeline[0].Content != "中间回答 [Image filtered]" {
		t.Fatalf("unexpected cleaned timeline: %+v", received.Timeline)
	}
	if received.RawUserContent != "<think>hidden</think> 第一问 ![猫](https://cdn.example.com/cat.jpg)" {
		t.Fatalf("unexpected raw user content: %q", received.RawUserContent)
	}
}

// TestPostActionRejectsNilTimelineItem verifies the transport returns one validation error instead of panicking when direct tests or manual integrations pass a nil timeline item.
// TestPostActionRejectsNilTimelineItem 用于验证当直接测试或手工集成传入 nil timeline 项时，传输层会返回校验错误，而不是直接 panic。
func TestPostActionRejectsNilTimelineItem(t *testing.T) {
	server := NewServer(Dependencies{
		PostAction: postActionFunc(func(context.Context, usecase.PostActionCommand) (usecase.PostActionResult, error) {
			return usecase.PostActionResult{Accepted: true}, nil
		}),
	})

	_, err := server.PostAction(context.Background(), &vmmv1.PostActionRequest{
		SessionId:        "sess-1",
		UserId:           7,
		ProjectId:        9,
		UserContent:      "第一问",
		AssistantContent: "收到",
		Timeline:         []*vmmv1.PostActionTimelineItem{nil},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestServerRejectsOversizedPayload verifies the gRPC receive limit still rejects oversized payloads before business logic runs.
// TestServerRejectsOversizedPayload 用于验证 gRPC 接收限制仍会在业务逻辑执行前拒绝超大载荷。
func TestServerRejectsOversizedPayload(t *testing.T) {
	fixture := newTestFixture(t, Dependencies{
		IDs: xid.NewGenerator(),
		PreCheck: preCheckFunc(func(context.Context, usecase.PreCheckCommand) (usecase.PreCheckResult, error) {
			return usecase.PreCheckResult{}, nil
		}),
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

// TestRecoveryInterceptorRedactsRawPanic verifies panic recovery logs keep stable diagnostics without writing the raw panic payload into runtime logs.
// TestRecoveryInterceptorRedactsRawPanic 用于验证 panic 恢复日志会保留稳定诊断字段，但不会把原始 panic 载荷写入运行时日志。
func TestRecoveryInterceptorRedactsRawPanic(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := RecoveryInterceptor(logger)
	panicText := "user bank 1234 secret"

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/PreCheck"}, func(context.Context, any) (any, error) {
		panic(panicText)
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `MSG："panic recovered"`) {
		t.Fatalf("expected panic recovery log, got %s", logs)
	}
	if strings.Contains(logs, panicText) {
		t.Fatalf("expected panic text to stay out of logs, got %s", logs)
	}
	if !strings.Contains(logs, "panic_type") || !strings.Contains(logs, "panic_len") || !strings.Contains(logs, "panic_sha256") {
		t.Fatalf("expected redacted panic diagnostics, got %s", logs)
	}
}

// TestRequestLoggerInterceptorAllowsNilInfo verifies the exported request logger stays usable in direct tests or manual invocations even when UnaryServerInfo is absent.
// TestRequestLoggerInterceptorAllowsNilInfo 用于验证导出的请求日志拦截器在直接测试或手工调用缺少 UnaryServerInfo 时仍可用，而不会直接崩溃。
func TestRequestLoggerInterceptorAllowsNilInfo(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "debug", Format: "text"})
	interceptor := RequestLoggerInterceptor(logger)

	_, err := interceptor(context.Background(), nil, nil, func(context.Context, any) (any, error) {
		return &emptypb.Empty{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected request logger error: %v", err)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `MSG："grpc request"`) {
		t.Fatalf("expected grpc request log, got %s", logs)
	}
	if !strings.Contains(logs, `method："unknown"`) {
		t.Fatalf("expected fallback method label in logs, got %s", logs)
	}
}

// TestRecoveryInterceptorAllowsNilInfo verifies the exported recovery interceptor still converts panics into internal errors when UnaryServerInfo is absent.
// TestRecoveryInterceptorAllowsNilInfo 用于验证导出的恢复拦截器在缺少 UnaryServerInfo 时仍会把 panic 转换成内部错误，而不会直接崩溃。
func TestRecoveryInterceptorAllowsNilInfo(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := RecoveryInterceptor(logger)
	panicText := "nil info panic payload"

	_, err := interceptor(context.Background(), nil, nil, func(context.Context, any) (any, error) {
		panic(panicText)
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
	logs := logBuf.String()
	if strings.Contains(logs, panicText) {
		t.Fatalf("expected panic text to stay out of logs, got %s", logs)
	}
	if !strings.Contains(logs, `method："unknown"`) {
		t.Fatalf("expected fallback method label in panic logs, got %s", logs)
	}
}

// TestTraceIDInterceptorAllowsNilContext verifies the exported trace interceptor remains safe when direct tests or manual probes invoke it with a nil context.
// TestTraceIDInterceptorAllowsNilContext 用于验证导出的 trace 拦截器在直接测试或手工探测以 nil context 调用时仍然安全，不会直接崩溃。
func TestTraceIDInterceptorAllowsNilContext(t *testing.T) {
	interceptor := TraceIDInterceptor(stubIDGenerator{id: "trc-test"})

	_, err := interceptor(nil, nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}, func(ctx context.Context, _ any) (any, error) {
		if got := trace.IDFromContext(ctx); got != "trc-test" {
			t.Fatalf("trace id = %q", got)
		}
		return &emptypb.Empty{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected trace interceptor error: %v", err)
	}
}

// TestScopeResolutionInterceptorAllowsNilContext verifies scope resolution still succeeds when direct interceptor calls omit the request context.
// TestScopeResolutionInterceptorAllowsNilContext 用于验证直接调用范围拦截器且缺少请求 context 时，范围解析仍能成功完成。
func TestScopeResolutionInterceptorAllowsNilContext(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := ScopeResolutionInterceptor(stubScopeResolver{}, logger)
	req := &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "当前项目怎么样",
	}

	_, err := interceptor(nil, req, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/PreCheck"}, func(ctx context.Context, _ any) (any, error) {
		session, ok := resolvedSessionRefFromContext(ctx)
		if !ok {
			t.Fatal("expected resolved session in context")
		}
		if session.SessionID != 41 || session.UserID != 7 || session.ProjectID != 9 {
			t.Fatalf("unexpected resolved session: %+v", session)
		}
		return &emptypb.Empty{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected scope interceptor error: %v", err)
	}
}

// TestScopeResolutionInterceptorInfersBusinessRPCWithoutInfo verifies the exported scope interceptor still resolves the session for business-chain requests when direct tests or manual integrations omit UnaryServerInfo.
// TestScopeResolutionInterceptorInfersBusinessRPCWithoutInfo 用于验证当直接测试或手工集成缺少 UnaryServerInfo 时，导出的范围拦截器仍会为业务链请求解析 session。
func TestScopeResolutionInterceptorInfersBusinessRPCWithoutInfo(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := ScopeResolutionInterceptor(stubScopeResolver{}, logger)
	req := &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "当前项目怎么样",
	}

	_, err := interceptor(context.Background(), req, nil, func(ctx context.Context, _ any) (any, error) {
		session, ok := resolvedSessionRefFromContext(ctx)
		if !ok {
			t.Fatal("expected resolved session in context")
		}
		if session.SessionID != 41 || session.UserID != 7 || session.ProjectID != 9 {
			t.Fatalf("unexpected resolved session: %+v", session)
		}
		return &emptypb.Empty{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected scope interceptor error: %v", err)
	}
}

// TestRequestLoggerInterceptorAllowsNilContext verifies the exported request logger remains safe when direct tests invoke it without a request context.
// TestRequestLoggerInterceptorAllowsNilContext 用于验证导出的请求日志拦截器在直接测试缺少请求 context 时仍然安全，不会因为读取 peer 信息而崩溃。
func TestRequestLoggerInterceptorAllowsNilContext(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "debug", Format: "text"})
	interceptor := RequestLoggerInterceptor(logger)

	_, err := interceptor(nil, nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}, func(context.Context, any) (any, error) {
		return &emptypb.Empty{}, nil
	})
	if err != nil {
		t.Fatalf("unexpected request logger error: %v", err)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `MSG："grpc request"`) {
		t.Fatalf("expected grpc request log, got %s", logs)
	}
}

// TestTraceIDInterceptorAllowsNilHandler verifies the exported trace interceptor returns a stable internal error instead of panicking when direct tests forget to pass a unary handler.
// TestTraceIDInterceptorAllowsNilHandler 用于验证导出的 trace 拦截器在直接测试忘记传入 unary handler 时，会返回稳定的内部错误，而不是直接 panic。
func TestTraceIDInterceptorAllowsNilHandler(t *testing.T) {
	interceptor := TraceIDInterceptor(stubIDGenerator{id: "trc-test"})

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}, nil)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestScopeResolutionInterceptorAllowsNilHandler verifies the scope interceptor degrades into a stable internal error when direct invocations omit the unary handler after scope resolution succeeds.
// TestScopeResolutionInterceptorAllowsNilHandler 用于验证范围拦截器在完成 scope 解析后若直接调用缺少 unary handler，会退化为稳定的内部错误。
func TestScopeResolutionInterceptorAllowsNilHandler(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := ScopeResolutionInterceptor(stubScopeResolver{}, logger)
	req := &vmmv1.PreCheckRequest{
		SessionId:   "sess-1",
		UserId:      7,
		ProjectId:   9,
		UserContent: "当前项目怎么样",
	}

	_, err := interceptor(context.Background(), req, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/PreCheck"}, nil)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestRequestLoggerInterceptorAllowsNilHandler verifies the exported request logger emits one degraded request log instead of panicking when direct tests omit the unary handler.
// TestRequestLoggerInterceptorAllowsNilHandler 用于验证导出的请求日志拦截器在直接测试缺少 unary handler 时，会输出一条降级请求日志，而不是直接 panic。
func TestRequestLoggerInterceptorAllowsNilHandler(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := RequestLoggerInterceptor(logger)

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}, nil)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `MSG："grpc request"`) {
		t.Fatalf("expected grpc request log, got %s", logs)
	}
	if !strings.Contains(logs, `code："Internal"`) {
		t.Fatalf("expected degraded internal status in logs, got %s", logs)
	}
}

// TestRecoveryInterceptorAllowsNilHandler verifies the exported recovery interceptor returns a stable internal error when direct tests omit the unary handler and therefore no panic recovery path can run.
// TestRecoveryInterceptorAllowsNilHandler 用于验证导出的恢复拦截器在直接测试缺少 unary handler 时，会返回稳定的内部错误，因为这时不存在可恢复的下游 panic 路径。
func TestRecoveryInterceptorAllowsNilHandler(t *testing.T) {
	logBuf := &bytes.Buffer{}
	logger := logx.New(logBuf, logx.Config{Level: "info", Format: "text"})
	interceptor := RecoveryInterceptor(logger)

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/vmm.v1.VMMService/Healthz"}, nil)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestListProjectsAllowsNilContext verifies the exported server method remains safe when direct tests or manual integrations call it with a nil context.
// TestListProjectsAllowsNilContext 用于验证导出的服务端方法在直接测试或手工集成以 nil context 调用时仍然安全，而不会在超时包装阶段直接 panic。
func TestListProjectsAllowsNilContext(t *testing.T) {
	server := &Server{
		workspace: &stubWorkspaceExecutor{
			projects: []logicdomain.ProjectRecord{{
				ID:        9,
				TeamID:    3,
				SpaceID:   5,
				TeamName:  "TeamA",
				SpaceName: "SpaceA",
				Name:      "ProjectA",
			}},
		},
	}

	resp, err := server.ListProjects(nil, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected list projects error: %v", err)
	}
	if len(resp.GetProjects()) != 1 || resp.GetProjects()[0].GetProjectId() != 9 {
		t.Fatalf("unexpected list projects response: %+v", resp)
	}
}

// TestResolveProjectAllowsNilValidator verifies one partially assembled server can still fall back to the default validator instead of panicking on a nil validate field.
// TestResolveProjectAllowsNilValidator 用于验证部分装配的服务端在 validate 字段为 nil 时会回退到默认校验器，而不是直接 panic。
func TestResolveProjectAllowsNilValidator(t *testing.T) {
	server := &Server{
		workspace: &stubWorkspaceExecutor{
			resolvedProject: logicdomain.ProjectRecord{
				ID:        9,
				TeamID:    3,
				SpaceID:   5,
				TeamName:  "TeamA",
				SpaceName: "SpaceA",
				Name:      "ProjectA",
			},
		},
	}

	resp, err := server.ResolveProject(context.Background(), &vmmv1.ResolveProjectRequest{ProjectRef: "9"})
	if err != nil {
		t.Fatalf("unexpected resolve project error: %v", err)
	}
	if resp.GetProject().GetProjectId() != 9 {
		t.Fatalf("unexpected resolved project: %+v", resp.GetProject())
	}
}

// TestHealthzRejectsNilReceiver verifies the exported health check no longer reports success when direct tests accidentally invoke it on a nil Server receiver.
// TestHealthzRejectsNilReceiver 用于验证导出的健康检查在直接测试误把它调用到 nil Server 接收者上时，不会再错误地返回成功。
func TestHealthzRejectsNilReceiver(t *testing.T) {
	var server *Server

	_, err := server.Healthz(context.Background(), &emptypb.Empty{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %s", status.Code(err))
	}
}

// TestListProjectsRejectsNilReceiver verifies admin RPCs return one stable internal error instead of panicking when direct tests invoke them on a nil Server receiver.
// TestListProjectsRejectsNilReceiver 用于验证管理类 RPC 在直接测试把它们调用到 nil Server 接收者上时，会返回稳定的内部错误，而不是直接 panic。
func TestListProjectsRejectsNilReceiver(t *testing.T) {
	var server *Server

	_, err := server.ListProjects(context.Background(), &emptypb.Empty{})
	if status.Code(err) != codes.Internal {
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

// chatCompactFunc adapts a plain function to the current ChatCompactExecutor interface.
// chatCompactFunc 用于把普通函数适配到当前 ChatCompactExecutor 接口。
type chatCompactFunc func(ctx context.Context, cmd usecase.ChatCompactCommand) (usecase.ChatCompactResult, error)

// Execute delegates chat-compact execution to the wrapped function.
// Execute 用于把 chat-compact 执行委托给包装函数。
func (f chatCompactFunc) Execute(ctx context.Context, cmd usecase.ChatCompactCommand) (usecase.ChatCompactResult, error) {
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

// stubIDGenerator returns one deterministic id so interceptor tests can assert the injected trace id without depending on xid randomness.
// stubIDGenerator 用于返回固定 id，让拦截器测试可以稳定断言注入的 trace id，而不依赖 xid 随机结果。
type stubIDGenerator struct {
	id string
}

// NewID returns the canned identifier expected by the current interceptor test.
// NewID 用于返回当前拦截器测试预期的固定标识。
func (s stubIDGenerator) NewID(string) string {
	return s.id
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
	projects            []logicdomain.ProjectRecord
	resolvedProject     logicdomain.ProjectRecord
	deleteProjectResult logicdomain.ProjectDeleteResult
	deleteProjectErr    error
	deleteUserResult    logicdomain.UserDeleteResult
	deleteUserErr       error
}

// ListProjects returns the canned project list for deterministic transport assertions.
// ListProjects 用于返回预置项目列表，保证传输层断言稳定。
func (s *stubWorkspaceExecutor) ListProjects(context.Context) ([]logicdomain.ProjectRecord, error) {
	return append([]logicdomain.ProjectRecord(nil), s.projects...), nil
}

// ResolveProject returns the canned project record for deterministic transport assertions.
// ResolveProject 用于返回预设项目记录，保证传输层断言稳定。
func (s *stubWorkspaceExecutor) ResolveProject(_ context.Context, _ string) (logicdomain.ProjectRecord, error) {
	return s.resolvedProject, nil
}

// EnsureProject keeps the test double interface-complete while focused tests only cover project creation.
// EnsureProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目创建。
func (s *stubWorkspaceExecutor) EnsureProject(_ context.Context, _ string, _ bool) (logicdomain.ProjectMutationResult, error) {
	return logicdomain.ProjectMutationResult{}, nil
}

// DeleteProject keeps the test double interface-complete while focused tests only cover project deletion.
// DeleteProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目删除。
func (s *stubWorkspaceExecutor) DeleteProject(_ context.Context, _ string, _ bool) (logicdomain.ProjectDeleteResult, error) {
	return s.deleteProjectResult, s.deleteProjectErr
}

// MigrateProject keeps the test double interface-complete while focused tests only cover project migration.
// MigrateProject 用于补齐测试替身接口，而当前聚焦测试只覆盖项目迁移。
func (s *stubWorkspaceExecutor) MigrateProject(_ context.Context, _ string, _ string, _ bool) (logicdomain.ProjectMigrationResult, error) {
	return logicdomain.ProjectMigrationResult{}, nil
}

// ResolveUser keeps the test double interface-complete while focused tests only cover user resolution.
// ResolveUser 用于补齐测试替身接口，而当前聚焦测试只覆盖用户解析。
func (s *stubWorkspaceExecutor) ResolveUser(_ context.Context, _ string, _ bool) (logicdomain.UserResolveResult, error) {
	return logicdomain.UserResolveResult{}, nil
}

// ListUsers keeps the test double interface-complete while focused tests only cover user listing.
// ListUsers 用于补齐测试替身接口，而当前聚焦测试只覆盖用户列表。
func (s *stubWorkspaceExecutor) ListUsers(_ context.Context) ([]logicdomain.UserRecord, error) {
	return nil, nil
}

// DeleteUser keeps the test double interface-complete while focused tests only cover user deletion.
// DeleteUser 用于补齐测试替身接口，而当前聚焦测试只覆盖用户删除。
func (s *stubWorkspaceExecutor) DeleteUser(_ context.Context, _ string, _ string) (logicdomain.UserDeleteResult, error) {
	return s.deleteUserResult, s.deleteUserErr
}

// TestDeleteProjectMapsProtectedResourceError verifies the admin delete RPC converts the protected-default-project failure into one stable FailedPrecondition gRPC status.
// TestDeleteProjectMapsProtectedResourceError 用于验证管理删除 RPC 会把“默认项目受保护”失败转换成稳定的 FailedPrecondition gRPC 状态。
func TestDeleteProjectMapsProtectedResourceError(t *testing.T) {
	server := &Server{
		workspace: &stubWorkspaceExecutor{
			deleteProjectErr: logicdomain.ProtectedResourceError{
				Resource: "project",
				Message:  "default project default/default/default cannot be deleted",
			},
		},
	}

	_, err := server.DeleteProject(context.Background(), &vmmv1.DeleteProjectRequest{
		ProjectPath:   logicdomain.DefaultProjectPath(),
		ConfirmDelete: true,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status code = %s", status.Code(err))
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected grpc status, got %v", err)
	}
	if st.Message() != "project: default project default/default/default cannot be deleted" {
		t.Fatalf("unexpected status message: %s", st.Message())
	}
	if len(st.Details()) != 1 {
		t.Fatalf("expected one status detail, got %d", len(st.Details()))
	}
	info, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok {
		t.Fatalf("expected ErrorInfo detail, got %T", st.Details()[0])
	}
	if info.Reason != "RESOURCE_PROTECTED" || info.Metadata["category"] != "protection" {
		t.Fatalf("unexpected error info: %+v", info)
	}
}

// TestDeleteUserMapsProtectedResourceError verifies the admin delete RPC converts the protected-default-user failure into one stable FailedPrecondition gRPC status.
// TestDeleteUserMapsProtectedResourceError 用于验证管理删除 RPC 会把“默认用户受保护”失败转换成稳定的 FailedPrecondition gRPC 状态。
func TestDeleteUserMapsProtectedResourceError(t *testing.T) {
	server := &Server{
		workspace: &stubWorkspaceExecutor{
			deleteUserErr: logicdomain.ProtectedResourceError{
				Resource: "user",
				Message:  "default user default cannot be deleted",
			},
		},
	}

	_, err := server.DeleteUser(context.Background(), &vmmv1.DeleteUserRequest{
		UserRef:          logicdomain.DefaultWorkspaceResourceName,
		ConfirmationCode: "confirm-me",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status code = %s", status.Code(err))
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected grpc status, got %v", err)
	}
	if st.Message() != "user: default user default cannot be deleted" {
		t.Fatalf("unexpected status message: %s", st.Message())
	}
	if len(st.Details()) != 1 {
		t.Fatalf("expected one status detail, got %d", len(st.Details()))
	}
	info, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok {
		t.Fatalf("expected ErrorInfo detail, got %T", st.Details()[0])
	}
	if info.Reason != "RESOURCE_PROTECTED" || info.Metadata["category"] != "protection" {
		t.Fatalf("unexpected error info: %+v", info)
	}
}

// stubProfileExecutor supplies just enough profile behavior for gRPC transport tests.
// stubProfileExecutor 用于为 gRPC 传输测试提供最小但足够的画像行为。
type stubProfileExecutor struct {
	queryResult  usecase.ProfileQueryResult
	queryErr     error
	queryCmd     usecase.ProfileQueryCommand
	bundleResult usecase.ProfileBundleResult
	bundleErr    error
	bundleCmd    usecase.ProfileBundleCommand
	applyResult  usecase.ProfileInstructionResult
	applyErr     error
}

// GetNodes returns the canned profile query result for deterministic transport assertions.
// GetNodes 用于返回预设画像查询结果，保证传输层断言稳定。
func (s *stubProfileExecutor) GetNodes(_ context.Context, cmd usecase.ProfileQueryCommand) (usecase.ProfileQueryResult, error) {
	s.queryCmd = cmd
	return s.queryResult, s.queryErr
}

// GetBundle returns the canned profile bundle result for deterministic transport assertions.
// GetBundle 用于返回预设画像 bundle 结果，保证传输层断言稳定。
func (s *stubProfileExecutor) GetBundle(_ context.Context, cmd usecase.ProfileBundleCommand) (usecase.ProfileBundleResult, error) {
	s.bundleCmd = cmd
	return s.bundleResult, s.bundleErr
}

// ApplyInstruction returns the canned manual instruction result for deterministic transport assertions.
// ApplyInstruction 用于返回预设手工画像指令结果，保证传输层断言稳定。
func (s *stubProfileExecutor) ApplyInstruction(_ context.Context, _ usecase.ProfileInstructionCommand) (usecase.ProfileInstructionResult, error) {
	return s.applyResult, s.applyErr
}

// stubMemoryExecutor supplies just enough memory-query behavior for gRPC transport tests.
// stubMemoryExecutor 用于为 gRPC 传输测试提供最小但足够的记忆查询行为。
type stubMemoryExecutor struct {
	searchResult usecase.MemoryQueryResult
	searchErr    error
	turnResult   usecase.TurnDetailResult
	turnErr      error
	detailResult usecase.MemoryDetailResult
	detailErr    error
	writeResult  usecase.WriteMemoriesResult
	writeErr     error
	writeCmd     usecase.WriteMemoriesCommand
}

// Search returns the canned grouped memory-query result for deterministic transport assertions.
// Search 用于返回预设的分组记忆查询结果，保证传输层断言稳定。
func (s *stubMemoryExecutor) Search(_ context.Context, _ usecase.MemoryQueryCommand) (usecase.MemoryQueryResult, error) {
	return s.searchResult, s.searchErr
}

// GetTurns returns the canned turn-detail result for deterministic transport assertions.
// GetTurns 用于返回预设的 turn 详情结果，保证传输层断言稳定。
func (s *stubMemoryExecutor) GetTurns(_ context.Context, _ usecase.TurnDetailCommand) (usecase.TurnDetailResult, error) {
	return s.turnResult, s.turnErr
}

// GetDetails returns the canned mixed-detail result for deterministic transport assertions.
// GetDetails 用于返回预设的混合详情结果，保证传输层断言稳定。
func (s *stubMemoryExecutor) GetDetails(_ context.Context, _ usecase.MemoryDetailCommand) (usecase.MemoryDetailResult, error) {
	return s.detailResult, s.detailErr
}

// Write captures the direct-write command and returns the canned write result for deterministic transport assertions.
// Write 用于捕获主动写入命令，并返回预设写入结果，保证传输层断言稳定。
func (s *stubMemoryExecutor) Write(_ context.Context, cmd usecase.WriteMemoriesCommand) (usecase.WriteMemoriesResult, error) {
	s.writeCmd = cmd
	return s.writeResult, s.writeErr
}

var _ appports.RequestScopeResolver = stubScopeResolver{}
