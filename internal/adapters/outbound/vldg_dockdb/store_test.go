// store_test.go exercises the DockDB-gateway adapter against the current hierarchy/session/message schema.
// store_test.go 用于围绕当前层级、session 和消息表结构验证 DockDB 网关适配器。
package vldg_dockdb

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldg_dockdb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// TestInitAppliesSchemaMigrationsUpToCurrentVersion verifies fresh installs bootstrap the version table and apply both v1 and v2 migrations.
// TestInitAppliesSchemaMigrationsUpToCurrentVersion 用于验证首次安装会先引导版本表，再依次执行 v1 和 v2 迁移。
func TestInitAppliesSchemaMigrationsUpToCurrentVersion(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[]`,
		},
	}
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}

	execs := server.execRequests()
	if len(execs) != 9 {
		t.Fatalf("expected 9 execute calls, got %d", len(execs))
	}
	if !strings.Contains(execs[0].Sql, "CREATE TABLE IF NOT EXISTS vmm_version") {
		t.Fatalf("missing version bootstrap sql: %s", execs[0].Sql)
	}
	if !strings.Contains(execs[1].Sql, "ALTER TABLE vmm_version ADD COLUMN updated_at") {
		t.Fatalf("missing version-shape sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "UPDATE vmm_version SET updated_at") {
		t.Fatalf("missing version backfill sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[3].Sql, "CREATE TABLE IF NOT EXISTS vmm_memories") {
		t.Fatalf("missing schema v1 sql: %s", execs[3].Sql)
	}
	if !strings.Contains(execs[6].Sql, "CREATE TABLE IF NOT EXISTS vmm_users") {
		t.Fatalf("missing schema v2 sql: %s", execs[6].Sql)
	}
	var params []any
	if err := json.Unmarshal([]byte(execs[8].ParamsJson), &params); err != nil {
		t.Fatalf("decode version insert params: %v", err)
	}
	if len(params) != 2 || params[0] != float64(currentSchemaVersion) {
		t.Fatalf("unexpected version params: %#v", params)
	}
}

// TestResolveRequestScopeCreatesSession verifies business interceptors can resolve numeric user/project ids and auto-create one session row.
// TestResolveRequestScopeCreatesSession 用于验证业务拦截器可以解析数字 user/project id，并自动创建 session 行。
func TestResolveRequestScopeCreatesSession(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":2}]`,
			"FROM vmm_users":   `[{"id":7,"name":"alice","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_sessions": `[]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_sessions": `[{"next_id":41}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	session, err := store.ResolveRequestScope(context.Background(), "sess-key-1", 7, 9)
	if err != nil {
		t.Fatalf("resolve request scope: %v", err)
	}
	if session.SessionID != 41 || session.SessionKey != "sess-key-1" {
		t.Fatalf("unexpected session ref: %+v", session)
	}
	if session.TeamID != 3 || session.SpaceID != 5 || session.ProjectID != 9 || session.UserID != 7 {
		t.Fatalf("unexpected scope ids: %+v", session)
	}
	if session.TeamName != "team-a" || session.SpaceName != "space-a" || session.ProjectName != "proj-a" || session.UserName != "alice" {
		t.Fatalf("unexpected scope names: %+v", session)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1]
	if !strings.Contains(last.Sql, "INSERT INTO vmm_sessions") {
		t.Fatalf("expected session insert sql, got %s", last.Sql)
	}
}

// TestAppendChatMessagesAppendsSequentialIndexes verifies message-level persistence continues from the current last_message_index.
// TestAppendChatMessagesAppendsSequentialIndexes 用于验证消息级持久化会从当前 last_message_index 继续顺序追加。
func TestAppendChatMessagesAppendsSequentialIndexes(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":2}]`,
			"SELECT id, message_count, last_message_index": `[{"id":41,"message_count":2,"last_message_index":2}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_chat_messages": `[{"next_id":100}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	err := store.AppendChatMessages(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-key-1",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, []logicdomain.ChatMessage{
		{Role: "user", Content: "第一问", SourceKind: "entry_user"},
		{Role: "assistant", Content: "最终回答", SourceKind: "final_assistant"},
	})
	if err != nil {
		t.Fatalf("append chat messages: %v", err)
	}

	execs := server.execRequests()
	if len(execs) < 5 {
		t.Fatalf("expected init + 3 persistence writes, got %d", len(execs))
	}
	var firstParams []any
	if err := json.Unmarshal([]byte(execs[len(execs)-3].ParamsJson), &firstParams); err != nil {
		t.Fatalf("decode first insert params: %v", err)
	}
	if got := firstParams[2]; got != float64(3) {
		t.Fatalf("first persisted message index = %#v", got)
	}
	var secondParams []any
	if err := json.Unmarshal([]byte(execs[len(execs)-2].ParamsJson), &secondParams); err != nil {
		t.Fatalf("decode second insert params: %v", err)
	}
	if got := secondParams[2]; got != float64(4) {
		t.Fatalf("second persisted message index = %#v", got)
	}
}

// newDuckDBTestStore creates one initialized adapter backed by a bufconn gRPC server.
// newDuckDBTestStore 用于创建一个已经初始化完成、并由 bufconn gRPC 服务支撑的适配器实例。
func newDuckDBTestStore(t *testing.T, server *fakeDuckDBServer) *Store {
	t.Helper()
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
}

// newDuckDBTestStoreWithoutInit creates one adapter test instance and lets the caller control schema bootstrap timing.
// newDuckDBTestStoreWithoutInit 用于创建一个适配器测试实例，并把 schema 初始化时机交给调用方控制。
func newDuckDBTestStoreWithoutInit(t *testing.T, server *fakeDuckDBServer) *Store {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	duckdbv1.RegisterDuckDbServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return &Store{
		conn:    conn,
		client:  duckdbv1.NewDuckDbServiceClient(conn),
		timeout: time.Second,
	}
}

// fakeDuckDBServer captures SQL requests and returns canned JSON query payloads for adapter tests.
// fakeDuckDBServer 用于捕获 SQL 请求并返回预置 JSON 查询结果，供适配器测试使用。
type fakeDuckDBServer struct {
	duckdbv1.UnimplementedDuckDbServiceServer
	mu        sync.Mutex
	execs     []*duckdbv1.ExecuteRequest
	querys    []*duckdbv1.QueryRequest
	queryJSON map[string]string
}

// ExecuteScript records every execute request so tests can assert SQL shape and parameters.
// ExecuteScript 用于记录每一次执行请求，方便测试断言 SQL 形态和参数。
func (s *fakeDuckDBServer) ExecuteScript(_ context.Context, req *duckdbv1.ExecuteRequest) (*duckdbv1.ExecuteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, req)
	return &duckdbv1.ExecuteResponse{Success: true, Message: "ok"}, nil
}

// QueryJson records every query request and returns the canned JSON payload that matches the SQL fragment.
// QueryJson 用于记录每一次查询请求，并按 SQL 片段返回预置 JSON 结果。
func (s *fakeDuckDBServer) QueryJson(_ context.Context, req *duckdbv1.QueryRequest) (*duckdbv1.QueryJsonResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.querys = append(s.querys, req)
	bestFragment := ""
	bestPayload := ""
	for fragment, payload := range s.queryJSON {
		if strings.Contains(req.Sql, fragment) && len(fragment) > len(bestFragment) {
			bestFragment = fragment
			bestPayload = payload
		}
	}
	if bestFragment != "" {
		return &duckdbv1.QueryJsonResponse{JsonData: bestPayload}, nil
	}
	return &duckdbv1.QueryJsonResponse{JsonData: "[]"}, nil
}

// QueryStream stays unused in these focused tests.
// QueryStream 用于在这些聚焦测试里保持未使用状态。
func (s *fakeDuckDBServer) QueryStream(req *duckdbv1.QueryRequest, stream grpc.ServerStreamingServer[duckdbv1.QueryResponse]) error {
	return nil
}

// execRequests returns a stable snapshot of all execute calls observed during the test.
// execRequests 用于返回测试期间观察到的全部执行请求快照。
func (s *fakeDuckDBServer) execRequests() []*duckdbv1.ExecuteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*duckdbv1.ExecuteRequest, len(s.execs))
	copy(out, s.execs)
	return out
}
