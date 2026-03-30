// store_test.go exercises the DuckDB-gateway adapter against the current hierarchy/session/turn schema.
// store_test.go 用于围绕当前层级、session 和 turn 表结构验证 DuckDB 网关适配器。
package vldb_duckdb

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	duckdbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_duckdb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// TestInitBootstrapsCurrentSchemaOnFreshInstall verifies fresh installs reset disposable debug tables, create the current baseline schema, and record its version.
// TestInitBootstrapsCurrentSchemaOnFreshInstall 用于验证首次安装会重置可丢弃的调试表、创建当前基线结构，并写入对应版本号。
func TestInitBootstrapsCurrentSchemaOnFreshInstall(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"WHERE singleton_id = 1": `[]`,
		},
	}
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}

	execs := server.execRequests()
	if len(execs) != 5 {
		t.Fatalf("expected 5 execute calls, got %d", len(execs))
	}
	if !strings.Contains(execs[0].Sql, "CREATE TABLE IF NOT EXISTS vmm_version") {
		t.Fatalf("missing version bootstrap sql: %s", execs[0].Sql)
	}
	if !strings.Contains(execs[1].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing managed schema reset sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_users") {
		t.Fatalf("missing current schema user table sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_turn_records") {
		t.Fatalf("missing current schema turn table sql: %s", execs[2].Sql)
	}
	if !strings.Contains(execs[2].Sql, "turn_count INTEGER NOT NULL DEFAULT 0") {
		t.Fatalf("missing current schema session turn counter sql: %s", execs[2].Sql)
	}
	if strings.Contains(execs[2].Sql, "vmm_memories") {
		t.Fatalf("unexpected legacy compatibility table in current schema sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[3].Sql, "DELETE FROM vmm_version") {
		t.Fatalf("missing version cleanup sql: %s", execs[3].Sql)
	}
	if !strings.Contains(execs[4].Sql, "INSERT INTO vmm_version") {
		t.Fatalf("missing version insert sql: %s", execs[4].Sql)
	}
	var params []any
	if err := json.Unmarshal([]byte(execs[4].ParamsJson), &params); err != nil {
		t.Fatalf("decode version insert params: %v", err)
	}
	if len(params) != 3 || params[0] != float64(versionSingletonID) || params[1] != float64(currentSchemaVersion) {
		t.Fatalf("unexpected version params: %#v", params)
	}
}

// TestInitResetsManagedSchemaOnVersionMismatch verifies old debug data is discarded and the current baseline schema is recreated.
// TestInitResetsManagedSchemaOnVersionMismatch 用于验证遇到旧版本调试数据时会直接丢弃并重建当前基线 schema。
func TestInitResetsManagedSchemaOnVersionMismatch(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"WHERE singleton_id = 1": `[{"schema_version":1}]`,
		},
	}
	store := newDuckDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store with version mismatch: %v", err)
	}
	execs := server.execRequests()
	if len(execs) != 5 {
		t.Fatalf("expected version bootstrap plus reset path, got %d calls", len(execs))
	}
	if !strings.Contains(execs[1].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing reset sql after version mismatch: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "CREATE TABLE IF NOT EXISTS vmm_turn_records") {
		t.Fatalf("missing recreated turn schema after version mismatch: %s", execs[2].Sql)
	}
}

// TestResolveRequestScopeCreatesSession verifies business interceptors can resolve numeric user/project ids and auto-create one session row.
// TestResolveRequestScopeCreatesSession 用于验证业务拦截器可以解析数字 user/project id，并自动创建 session 行。
func TestResolveRequestScopeCreatesSession(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":3}]`,
			"FROM vmm_users":      `[{"id":7,"name":"alice","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_sessions":   `[]`,
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
	if session.TurnCount != 0 || session.SummarizeBudget != 0 || !session.CreatedAt.Equal(session.UpdatedAt) {
		t.Fatalf("unexpected new session analysis counters: %+v", session)
	}

	execs := server.execRequests()
	last := execs[len(execs)-1]
	if !strings.Contains(last.Sql, "INSERT INTO vmm_sessions") {
		t.Fatalf("expected session insert sql, got %s", last.Sql)
	}
}

// TestResolveRequestScopeReusesExistingSessionAfterUserSwitch verifies debug-stage session reuse tolerates manual user-id switches under the same project/session key.
// TestResolveRequestScopeReusesExistingSessionAfterUserSwitch 用于验证调试阶段在同一 project/session_key 下即使手动切换 user_id，也仍然会复用已有 session。
func TestResolveRequestScopeReusesExistingSessionAfterUserSwitch(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version":    `[{"schema_version":3}]`,
			"FROM vmm_users":      `[{"id":8,"name":"bob","delete_confirm_code":"","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_projects p": `[{"id":9,"team_id":3,"space_id":5,"name":"proj-a","team_name":"team-a","space_name":"space-a","created_at":"2026-03-27T00:00:00Z","updated_at":"2026-03-27T00:00:00Z"}]`,
			"FROM vmm_sessions":   `[{"id":41,"session_key":"sess-key-1","user_id":7,"team_id":3,"space_id":5,"project_id":9,"turn_count":2,"last_summarized_id":0,"summarize_content":"","summarize_budget":0,"created_timestamp":1710000000000,"updated_timestamp":1710000001000}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	session, err := store.ResolveRequestScope(context.Background(), "sess-key-1", 8, 9)
	if err != nil {
		t.Fatalf("resolve request scope with switched user id: %v", err)
	}
	if session.SessionID != 41 || session.SessionKey != "sess-key-1" {
		t.Fatalf("unexpected reused session ref: %+v", session)
	}
	if session.UserID != 8 || session.ProjectID != 9 || session.TeamID != 3 || session.SpaceID != 5 {
		t.Fatalf("unexpected resolved scope after user switch: %+v", session)
	}
	if session.TurnCount != 2 || session.SummarizeBudget != 0 || session.UpdatedAt.IsZero() {
		t.Fatalf("unexpected reused session counters: %+v", session)
	}

	execs := server.execRequests()
	for _, req := range execs {
		if strings.Contains(req.Sql, "INSERT INTO vmm_sessions") {
			t.Fatalf("did not expect a new session row when reusing existing session: %s", req.Sql)
		}
	}
}

// TestAppendTurnRecordPersistsDehydratedPayload verifies post-action persistence writes one dehydrated turn row and then increments the session turn counter.
// TestAppendTurnRecordPersistsDehydratedPayload 用于验证 post-action 持久化会写入一条脱水 turn 行，并随后递增 session turn 计数。
func TestAppendTurnRecordPersistsDehydratedPayload(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_version": `[{"schema_version":3}]`,
			"SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM vmm_turn_records": `[{"next_id":100}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	err := store.AppendTurnRecord(context.Background(), logicdomain.SessionRef{
		SessionID:  41,
		SessionKey: "sess-key-1",
		UserID:     7,
		TeamID:     3,
		SpaceID:    5,
		ProjectID:  9,
	}, logicdomain.TurnRecord{
		UserContent:      "第一问",
		AssistantContent: "最终回答",
		Timeline: []logicdomain.TurnTimelineItem{
			{Type: "assistant", Content: "中间回答"},
			{Type: "user", Content: "补充问题"},
		},
	})
	if err != nil {
		t.Fatalf("append turn record: %v", err)
	}

	execs := server.execRequests()
	if len(execs) < 3 {
		t.Fatalf("expected version bootstrap + 2 persistence writes, got %d", len(execs))
	}
	insertSQL := execs[len(execs)-2].Sql
	if !strings.Contains(insertSQL, "INSERT INTO vmm_turn_records") {
		t.Fatalf("expected turn insert sql, got %s", insertSQL)
	}
	if !strings.Contains(insertSQL, "\"assistant\":\"最终回答\"") {
		t.Fatalf("expected final assistant in dehydrated payload, got %s", insertSQL)
	}
	if !strings.Contains(insertSQL, "\"content\":\"中间回答\"") {
		t.Fatalf("expected cleaned assistant timeline content to stay in dehydrated payload, got %s", insertSQL)
	}
	updateSQL := execs[len(execs)-1].Sql
	if !strings.Contains(updateSQL, "SET turn_count = turn_count + 1") {
		t.Fatalf("expected session turn counter update sql, got %s", updateSQL)
	}
	if !strings.Contains(updateSQL, "summarize_budget = summarize_budget + ") {
		t.Fatalf("expected session summarize budget update sql, got %s", updateSQL)
	}
}

// TestDebugCleanManagedSchemaExecutesDropScript verifies the debug-clean helper wipes the managed DuckDB schema through one execute call.
// TestDebugCleanManagedSchemaExecutesDropScript 用于验证调试清理辅助逻辑会通过一次执行调用清空受管 DuckDB schema。
func TestDebugCleanManagedSchemaExecutesDropScript(t *testing.T) {
	server := &fakeDuckDBServer{}
	store := newDuckDBTestStoreWithoutInit(t, server)

	if err := debugCleanWithClient(context.Background(), store.client, time.Second); err != nil {
		t.Fatalf("debug clean managed schema: %v", err)
	}

	execs := server.execRequests()
	if len(execs) != 1 {
		t.Fatalf("expected 1 debug-clean execute call, got %d", len(execs))
	}
	if !strings.Contains(execs[0].Sql, "DROP TABLE IF EXISTS vmm_turn_records") {
		t.Fatalf("missing managed table cleanup in debug-clean sql: %s", execs[0].Sql)
	}
	if !strings.Contains(execs[0].Sql, "DROP TABLE IF EXISTS vmm_version") {
		t.Fatalf("missing version-table cleanup in debug-clean sql: %s", execs[0].Sql)
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
	mu         sync.Mutex
	execs      []*duckdbv1.ExecuteRequest
	querys     []*duckdbv1.QueryRequest
	queryJSON  map[string]string
	execErrors map[string]string
}

// ExecuteScript records every execute request so tests can assert SQL shape and parameters.
// ExecuteScript 用于记录每一次执行请求，方便测试断言 SQL 形态和参数。
func (s *fakeDuckDBServer) ExecuteScript(_ context.Context, req *duckdbv1.ExecuteRequest) (*duckdbv1.ExecuteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, req)
	for fragment, message := range s.execErrors {
		if strings.Contains(req.Sql, fragment) {
			return &duckdbv1.ExecuteResponse{Success: false, Message: message}, nil
		}
	}
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
