// store_test.go exercises the DuckDB-gateway adapter without depending on a real external gateway.
// store_test.go 用于在不依赖真实外部网关的情况下验证 DuckDB 网关适配器。
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

// TestReplaceNoiseEmbeddingCacheRewritesScopedBundle verifies the adapter replaces one scoped cache bundle through delete-plus-insert writes.
// TestReplaceNoiseEmbeddingCacheRewritesScopedBundle 用于验证适配器会通过“先删后插”重写指定作用域的缓存包。
func TestReplaceNoiseEmbeddingCacheRewritesScopedBundle(t *testing.T) {
	server := &fakeDuckDBServer{}
	store := newDuckDBTestStore(t, server)

	err := store.ReplaceNoiseEmbeddingCache(context.Background(), logicdomain.NoiseEmbeddingCacheQuery{
		Scope:    "noise_gate",
		Language: "zh-CN",
	}, []logicdomain.NoiseEmbeddingCacheEntry{
		{
			Scope:        "noise_gate",
			Language:     "zh-CN",
			CategoryName: "meta_question",
			Phrase:       "你还记得吗",
			Model:        "text-embedding-v4",
			Dimension:    1024,
			RulesHash:    "hash-a",
			Vector:       []float32{0.1, 0.2},
			UpdatedAt:    time.Unix(100, 0).UTC(),
		},
	})
	if err != nil {
		t.Fatalf("replace cache: %v", err)
	}

	// The adapter should first clear the selected bundle and then insert the refreshed rows.
	// 适配器应先清空目标缓存包，再插入刷新后的新记录。
	execs := server.execRequests()
	if len(execs) < 3 {
		t.Fatalf("expected init + delete + insert calls, got %d", len(execs))
	}
	if !strings.Contains(execs[1].Sql, "DELETE FROM vmm_noise_embeddings") {
		t.Fatalf("unexpected delete sql: %s", execs[1].Sql)
	}
	if !strings.Contains(execs[2].Sql, "INSERT INTO vmm_noise_embeddings") {
		t.Fatalf("unexpected insert sql: %s", execs[2].Sql)
	}
	var params []any
	if err := json.Unmarshal([]byte(execs[2].ParamsJson), &params); err != nil {
		t.Fatalf("decode insert params: %v", err)
	}
	if got := params[2]; got != "meta_question" {
		t.Fatalf("category_name param = %#v", got)
	}
}

// TestUpsertChatLogsAppendsTurnIndexes verifies the adapter appends persisted turn indexes after querying the current session maximum.
// TestUpsertChatLogsAppendsTurnIndexes 用于验证适配器会先查询当前最大 turn_index，再向后追加新轮次。
func TestUpsertChatLogsAppendsTurnIndexes(t *testing.T) {
	server := &fakeDuckDBServer{
		queryJSON: map[string]string{
			"FROM vmm_chat_logs": `[{"max_turn_index":2}]`,
		},
	}
	store := newDuckDBTestStore(t, server)

	err := store.UpsertChatLogs(context.Background(), logicdomain.SessionRef{
		SessionID: "sess-1",
		UserID:    "u1",
		TeamID:    "t1",
		ProjectID: "p1",
		SpaceID:   "s1",
	}, []logicdomain.NormalizedTurn{
		{TurnIndex: 1, UserMessage: "第一问", AssistantReply: "第一答"},
		{TurnIndex: 2, UserMessage: "第二问", AssistantReply: "第二答"},
	})
	if err != nil {
		t.Fatalf("upsert chat logs: %v", err)
	}

	// The first appended rows should continue from the current max turn index rather than overwriting turn 1.
	// 新增的轮次应从当前最大 turn_index 继续递增，而不是回写覆盖 turn 1。
	execs := server.execRequests()
	if len(execs) < 3 {
		t.Fatalf("expected init + 2 insert calls, got %d", len(execs))
	}
	var firstParams []any
	if err := json.Unmarshal([]byte(execs[1].ParamsJson), &firstParams); err != nil {
		t.Fatalf("decode first insert params: %v", err)
	}
	if got := firstParams[1]; got != float64(3) {
		t.Fatalf("first persisted turn index = %#v", got)
	}
	var secondParams []any
	if err := json.Unmarshal([]byte(execs[2].ParamsJson), &secondParams); err != nil {
		t.Fatalf("decode second insert params: %v", err)
	}
	if got := secondParams[1]; got != float64(4) {
		t.Fatalf("second persisted turn index = %#v", got)
	}
}

// newDuckDBTestStore creates one adapter instance backed by a bufconn gRPC server.
// newDuckDBTestStore 用于创建一个由 bufconn gRPC 服务支撑的适配器测试实例。
func newDuckDBTestStore(t *testing.T, server *fakeDuckDBServer) *Store {
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

	store := &Store{
		conn:    conn,
		client:  duckdbv1.NewDuckDbServiceClient(conn),
		timeout: time.Second,
	}
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
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
func (s *fakeDuckDBServer) ExecuteScript(ctx context.Context, req *duckdbv1.ExecuteRequest) (*duckdbv1.ExecuteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execs = append(s.execs, req)
	return &duckdbv1.ExecuteResponse{Success: true, Message: "ok"}, nil
}

// QueryJson records every query request and returns the canned JSON payload that matches the SQL fragment.
// QueryJson 用于记录每一次查询请求，并按 SQL 片段返回预置 JSON 结果。
func (s *fakeDuckDBServer) QueryJson(ctx context.Context, req *duckdbv1.QueryRequest) (*duckdbv1.QueryJsonResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.querys = append(s.querys, req)
	for fragment, payload := range s.queryJSON {
		if strings.Contains(req.Sql, fragment) {
			return &duckdbv1.QueryJsonResponse{JsonData: payload}, nil
		}
	}
	return &duckdbv1.QueryJsonResponse{JsonData: "[]"}, nil
}

// QueryStream is not used by the adapter and therefore stays unimplemented in these focused tests.
// QueryStream 不会被当前适配器使用，因此在这些聚焦测试里保持未实现。
func (s *fakeDuckDBServer) QueryStream(req *duckdbv1.QueryRequest, stream grpc.ServerStreamingServer[duckdbv1.QueryResponse]) error {
	return nil
}

// execRequests returns a snapshot of recorded execute requests for stable assertions.
// execRequests 用于返回已记录执行请求的快照，便于做稳定断言。
func (s *fakeDuckDBServer) execRequests() []*duckdbv1.ExecuteRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*duckdbv1.ExecuteRequest, len(s.execs))
	copy(out, s.execs)
	return out
}
