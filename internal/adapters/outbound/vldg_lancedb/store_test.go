// store_test.go exercises the LanceDB-gateway adapter without depending on a real external gateway.
// store_test.go 用于在不依赖真实外部网关的情况下验证 LanceDB 网关适配器。
package vldg_lancedb

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldg_lancedb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestUpsertEncodesJSONRowsAndKeys verifies the adapter emits one JSON-row upsert keyed by id.
// TestUpsertEncodesJSONRowsAndKeys 用于验证适配器会按 id 键发送一条 JSON 行 upsert 请求。
func TestUpsertEncodesJSONRowsAndKeys(t *testing.T) {
	server := &fakeLanceDBServer{}
	store := newLanceDBTestStore(t, server)

	err := store.Upsert(context.Background(), logicdomain.MemoryRecord{
		ID:     "mem-1",
		Text:   "gateway-backed memory",
		Vector: []float32{0.1, 0.2, 0.3},
		Filter: logicdomain.SearchFilter{
			UserID:    "u1",
			ProjectID: "p1",
			SpaceID:   "s1",
		},
		Metadata:  map[string]string{"source": "seed"},
		CreatedAt: time.Date(2026, 3, 26, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("upsert memory record: %v", err)
	}

	// Inspect the emitted upsert payload so the adapter contract stays stable without a live gateway.
	// 检查实际发出的 upsert 载荷，保证在没有真实网关时适配器契约依然稳定。
	requests := server.upsertRequests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 upsert request, got %d", len(requests))
	}
	req := requests[0]
	if req.TableName != "vmm_memory_vectors" {
		t.Fatalf("table name = %q", req.TableName)
	}
	if len(req.KeyColumns) != 1 || req.KeyColumns[0] != "id" {
		t.Fatalf("key columns = %#v", req.KeyColumns)
	}
	var rows []map[string]any
	if err := json.Unmarshal(req.Data, &rows); err != nil {
		t.Fatalf("decode upsert rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := rows[0]["id"]; got != "mem-1" {
		t.Fatalf("row id = %#v", got)
	}
	if got := rows[0]["text"]; got != "gateway-backed memory" {
		t.Fatalf("row text = %#v", got)
	}
	if got := rows[0]["user_id"]; got != "u1" {
		t.Fatalf("row user_id = %#v", got)
	}
	if got := rows[0]["project_id"]; got != "p1" {
		t.Fatalf("row project_id = %#v", got)
	}
	if got := rows[0]["space_id"]; got != "s1" {
		t.Fatalf("row space_id = %#v", got)
	}
}

// TestSearchMapsRowsAndFilter verifies the adapter maps JSON rows and keeps the in-memory filter semantics.
// TestSearchMapsRowsAndFilter 用于验证适配器会正确映射 JSON 行，并保持与内存实现一致的过滤语义。
func TestSearchMapsRowsAndFilter(t *testing.T) {
	server := &fakeLanceDBServer{
		searchData: []byte(`[
			{
				"id":"mem-1",
				"text":"hello from gateway",
				"user_id":"",
				"project_id":"p1",
				"space_id":"s1",
				"metadata_json":"{\"source\":\"seed\"}",
				"_distance":0.25
			}
		]`),
	}
	store := newLanceDBTestStore(t, server)

	hits, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:    "u1",
		ProjectID: "p1",
		SpaceID:   "s1",
	})
	if err != nil {
		t.Fatalf("search memory vectors: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != "mem-1" {
		t.Fatalf("hit id = %q", hits[0].ID)
	}
	if hits[0].Score <= 0 || hits[0].Score >= 1 {
		t.Fatalf("hit score = %v", hits[0].Score)
	}
	if hits[0].Metadata["source"] != "seed" {
		t.Fatalf("hit metadata = %#v", hits[0].Metadata)
	}

	// Assert the gateway filter keeps globally shared user rows by allowing empty or zero user ids.
	// 断言网关过滤条件允许 user_id 为空或为 0 的共享记忆，保持与内存实现一致。
	requests := server.searchRequests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 search request, got %d", len(requests))
	}
	if !strings.Contains(requests[0].Filter, "user_id = '' OR user_id = '0' OR user_id = 'u1'") {
		t.Fatalf("unexpected filter expression: %s", requests[0].Filter)
	}
}

// TestInitIgnoresAlreadyExistsTransportError verifies startup stays idempotent when the gateway surfaces table reuse as a gRPC error.
// TestInitIgnoresAlreadyExistsTransportError 用于验证当网关通过 gRPC 错误报告“表已存在”时，启动仍保持幂等。
func TestInitIgnoresAlreadyExistsTransportError(t *testing.T) {
	server := &fakeLanceDBServer{
		createErr: status.Error(codes.Internal, "Table 'vmm_memory_vectors' already exists"),
	}
	store := newLanceDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate already-exists transport error: %v", err)
	}
}

// TestInitIgnoresAlreadyExistsResponse verifies startup also tolerates non-success responses that only report an existing table.
// TestInitIgnoresAlreadyExistsResponse 用于验证当网关通过非成功响应报告“表已存在”时，启动同样会容忍该情况。
func TestInitIgnoresAlreadyExistsResponse(t *testing.T) {
	server := &fakeLanceDBServer{
		createResponse: &lancedbv1.CreateTableResponse{Success: false, Message: "Table 'vmm_memory_vectors' already exists"},
	}
	store := newLanceDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate already-exists response: %v", err)
	}
}

// newLanceDBTestStore creates one adapter instance backed by a bufconn gRPC server.
// newLanceDBTestStore 用于创建一个由 bufconn gRPC 服务支撑的适配器测试实例。
func newLanceDBTestStore(t *testing.T, server *fakeLanceDBServer) *Store {
	t.Helper()
	store := newLanceDBTestStoreWithoutInit(t, server)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store
}

// newLanceDBTestStoreWithoutInit creates one adapter test instance but leaves table bootstrap to the caller.
// newLanceDBTestStoreWithoutInit 用于创建一个适配器测试实例，但把建表初始化交给调用方自行控制。
func newLanceDBTestStoreWithoutInit(t *testing.T, server *fakeLanceDBServer) *Store {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	lancedbv1.RegisterLanceDbServiceServer(grpcServer, server)
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
		conn:         conn,
		client:       lancedbv1.NewLanceDbServiceClient(conn),
		timeout:      time.Second,
		tableName:    "vmm_memory_vectors",
		vectorColumn: "vector",
		dimension:    3,
	}
	return store
}

// fakeLanceDBServer records gRPC requests so adapter tests can assert the emitted gateway contract.
// fakeLanceDBServer 用于记录 gRPC 请求，方便适配器测试断言实际发出的网关契约。
type fakeLanceDBServer struct {
	lancedbv1.UnimplementedLanceDbServiceServer
	mu              sync.Mutex
	createRequests  []*lancedbv1.CreateTableRequest
	createResponse  *lancedbv1.CreateTableResponse
	createErr       error
	upsertCalls     []*lancedbv1.UpsertRequest
	searchCalls     []*lancedbv1.SearchRequest
	searchData      []byte
	searchMessage   string
	searchSucceeded bool
}

// CreateTable records the bootstrap request and reports success so the adapter can initialize normally.
// CreateTable 用于记录启动建表请求，并返回成功结果让适配器正常初始化。
func (s *fakeLanceDBServer) CreateTable(ctx context.Context, req *lancedbv1.CreateTableRequest) (*lancedbv1.CreateTableResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createRequests = append(s.createRequests, req)
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createResponse != nil {
		return s.createResponse, nil
	}
	return &lancedbv1.CreateTableResponse{Success: true, Message: "ok"}, nil
}

// VectorUpsert records upsert payloads so tests can assert row encoding and key configuration.
// VectorUpsert 用于记录 upsert 载荷，方便测试断言行编码和主键配置。
func (s *fakeLanceDBServer) VectorUpsert(ctx context.Context, req *lancedbv1.UpsertRequest) (*lancedbv1.UpsertResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertCalls = append(s.upsertCalls, req)
	return &lancedbv1.UpsertResponse{Success: true, Message: "ok"}, nil
}

// VectorSearch records search requests and returns one canned JSON payload for deterministic adapter assertions.
// VectorSearch 用于记录检索请求，并返回预置 JSON 载荷供适配器做确定性断言。
func (s *fakeLanceDBServer) VectorSearch(ctx context.Context, req *lancedbv1.SearchRequest) (*lancedbv1.SearchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchCalls = append(s.searchCalls, req)
	success := s.searchSucceeded
	if !success {
		success = true
	}
	return &lancedbv1.SearchResponse{
		Success: success,
		Message: s.searchMessage,
		Data:    s.searchData,
	}, nil
}

// upsertRequests returns a snapshot of recorded upsert requests for stable assertions.
// upsertRequests 用于返回已记录的 upsert 请求快照，便于做稳定断言。
func (s *fakeLanceDBServer) upsertRequests() []*lancedbv1.UpsertRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*lancedbv1.UpsertRequest, len(s.upsertCalls))
	copy(out, s.upsertCalls)
	return out
}

// searchRequests returns a snapshot of recorded search requests for stable assertions.
// searchRequests 用于返回已记录的检索请求快照，便于做稳定断言。
func (s *fakeLanceDBServer) searchRequests() []*lancedbv1.SearchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*lancedbv1.SearchRequest, len(s.searchCalls))
	copy(out, s.searchCalls)
	return out
}
