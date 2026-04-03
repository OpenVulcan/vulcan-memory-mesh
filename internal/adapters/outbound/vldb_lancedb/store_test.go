// store_test.go exercises the LanceDB-gateway adapter against the flattened numeric metadata contract.
// store_test.go 用于围绕扁平化数字元数据契约验证 LanceDB 网关适配器。
package vldb_lancedb

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	lancedbv1 "github.com/openvulcan/vmm/internal/adapters/outbound/vldb_lancedb/proto/v1"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// TestUpsertEncodesJSONRowsAndKeys verifies the adapter sends one dimension-qualified JSON-row upsert keyed by id.
// TestUpsertEncodesJSONRowsAndKeys 用于验证适配器会向带维度后缀的表发送一条以 id 为键的 JSON 行 upsert。
func TestUpsertEncodesJSONRowsAndKeys(t *testing.T) {
	server := &fakeLanceDBServer{}
	store := newLanceDBTestStore(t, server)

	if len(server.createRequests) != 1 || server.createRequests[0].TableName != "vmm_memory_vectors_3" {
		t.Fatalf("create table request = %#v", server.createRequests)
	}

	err := store.Upsert(context.Background(), logicdomain.MemoryRecord{
		ID:     "mem-1",
		Text:   "gateway-backed memory",
		Vector: []float32{0.1, 0.2, 0.3},
		Filter: logicdomain.SearchFilter{
			UserID:    7,
			TeamID:    3,
			SpaceID:   5,
			ProjectID: 9,
			SessionID: 11,
		},
		Metadata:  map[string]string{"source": "seed"},
		CreatedAt: time.Date(2026, 3, 27, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("upsert memory record: %v", err)
	}

	requests := server.upsertRequests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 upsert request, got %d", len(requests))
	}
	req := requests[0]
	if req.TableName != "vmm_memory_vectors_3" {
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
	row := rows[0]
	if row["id"] != "mem-1" || row["content"] != "gateway-backed memory" {
		t.Fatalf("unexpected row payload = %#v", row)
	}
	if row["team_id"] != float64(3) || row["space_id"] != float64(5) || row["project_id"] != float64(9) || row["session_id"] != float64(11) || row["user_id"] != float64(7) {
		t.Fatalf("unexpected flattened ids = %#v", row)
	}
}

// TestSearchMapsRowsAndFilter verifies search rows map back into numeric hierarchy filters and preserve shared-user semantics.
// TestSearchMapsRowsAndFilter 用于验证检索结果会映射回数字层级过滤条件，并保留共享用户语义。
func TestSearchMapsRowsAndFilter(t *testing.T) {
	server := &fakeLanceDBServer{
		searchData: []byte(`[
			{
				"id":"mem-1",
				"content":"hello from gateway",
				"team_id":3,
				"space_id":5,
				"project_id":9,
				"session_id":11,
				"user_id":0,
				"metadata_json":"{\"source\":\"seed\"}",
				"_distance":0.25
			}
		]`),
	}
	store := newLanceDBTestStore(t, server)

	hits, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	})
	if err != nil {
		t.Fatalf("search memory vectors: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != "mem-1" || hits[0].Text != "hello from gateway" {
		t.Fatalf("unexpected hit = %#v", hits[0])
	}
	if hits[0].Filter.TeamID != 3 || hits[0].Filter.SpaceID != 5 || hits[0].Filter.ProjectID != 9 || hits[0].Filter.SessionID != 11 || hits[0].Filter.UserID != 0 {
		t.Fatalf("unexpected hit filter = %#v", hits[0].Filter)
	}
	requests := server.searchRequests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 search request, got %d", len(requests))
	}
	if !strings.Contains(requests[0].Filter, "(user_id = 0 OR user_id = 7)") {
		t.Fatalf("unexpected filter expression: %s", requests[0].Filter)
	}
}

// TestSearchRejectsMalformedNumericFields verifies malformed numeric strings from the LanceDB gateway fail fast instead of being silently coerced to zero-value ids or distances.
// TestSearchRejectsMalformedNumericFields 用于验证当 LanceDB 网关返回畸形数字字符串时，适配器会快速报错，而不是悄悄把它们吞成零值 id 或距离。
func TestSearchRejectsMalformedNumericFields(t *testing.T) {
	server := &fakeLanceDBServer{
		searchData: []byte(`[
			{
				"id":"mem-1",
				"content":"hello from gateway",
				"team_id":"not-a-number",
				"space_id":5,
				"project_id":9,
				"session_id":11,
				"user_id":7,
				"metadata_json":"{}",
				"_distance":"bad-distance"
			}
		]`),
	}
	store := newLanceDBTestStore(t, server)

	_, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	})
	if err == nil {
		t.Fatal("expected malformed numeric search row to fail")
	}
	if !strings.Contains(err.Error(), "decode lancedb search rows") || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("expected wrapped numeric decode error, got %v", err)
	}
}

// TestInitIgnoresAlreadyExistsResponses verifies repeated boots treat existing tables as a successful idempotent state.
// TestInitIgnoresAlreadyExistsResponses 用于验证重复启动会把“表已存在”视为成功的幂等状态。
func TestInitIgnoresAlreadyExistsResponses(t *testing.T) {
	transportServer := &fakeLanceDBServer{
		createErr: status.Error(codes.Internal, "Table 'vmm_memory_vectors_3' already exists"),
	}
	store := newLanceDBTestStoreWithoutInit(t, transportServer)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate transport already-exists error: %v", err)
	}

	bodyServer := &fakeLanceDBServer{
		createResponse: &lancedbv1.CreateTableResponse{Success: false, Message: "Table 'vmm_memory_vectors_3' already exists"},
	}
	store = newLanceDBTestStoreWithoutInit(t, bodyServer)
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate body already-exists response: %v", err)
	}
}

// TestDebugDropConfiguredTableDropsResolvedTable verifies the debug-clean helper drops the resolved runtime LanceDB table.
// TestDebugDropConfiguredTableDropsResolvedTable 用于验证调试清理辅助逻辑会删除解析后的运行时 LanceDB 表。
func TestDebugDropConfiguredTableDropsResolvedTable(t *testing.T) {
	server := &fakeLanceDBServer{}
	store := newLanceDBTestStoreWithoutInit(t, server)

	if err := debugDropTableWithClient(context.Background(), store.client, "vmm_memory_vectors_3", time.Second); err != nil {
		t.Fatalf("debug drop configured table: %v", err)
	}

	requests := server.dropRequests()
	if len(requests) != 1 {
		t.Fatalf("expected 1 drop-table request, got %d", len(requests))
	}
	if requests[0].GetTableName() != "vmm_memory_vectors_3" {
		t.Fatalf("unexpected drop-table target: %s", requests[0].GetTableName())
	}
}

// TestDebugDropConfiguredTableTreatsMissingTableAsSuccess verifies repeated debug cleans stay idempotent when the target table is already gone.
// TestDebugDropConfiguredTableTreatsMissingTableAsSuccess 用于验证目标表已经不存在时，重复执行调试清理仍保持幂等成功。
func TestDebugDropConfiguredTableTreatsMissingTableAsSuccess(t *testing.T) {
	server := &fakeLanceDBServer{
		dropResponse: &lancedbv1.DropTableResponse{Success: false, Message: "table does not exist"},
	}
	store := newLanceDBTestStoreWithoutInit(t, server)

	if err := debugDropTableWithClient(context.Background(), store.client, "vmm_memory_vectors_3", time.Second); err != nil {
		t.Fatalf("debug drop configured table should tolerate missing table: %v", err)
	}
}

// TestDeleteByIDsBuildsPreciseDeleteCondition verifies rollback deletes target only the supplied vector ids.
// TestDeleteByIDsBuildsPreciseDeleteCondition 用于验证回滚删除只会精确命中提供的向量 id。
func TestDeleteByIDsBuildsPreciseDeleteCondition(t *testing.T) {
	server := &fakeLanceDBServer{}
	store := newLanceDBTestStore(t, server)

	deletedRows, err := store.DeleteByIDs(context.Background(), []string{"vec-1", "vec-2", "vec-1"})
	if err != nil {
		t.Fatalf("delete vector ids: %v", err)
	}
	if deletedRows != 0 {
		t.Fatalf("expected fake delete rows to stay 0, got %d", deletedRows)
	}
	if len(server.deleteCalls) != 1 {
		t.Fatalf("expected 1 delete request, got %d", len(server.deleteCalls))
	}
	condition := server.deleteCalls[0].GetCondition()
	if !strings.Contains(condition, "id = 'vec-1'") || !strings.Contains(condition, "id = 'vec-2'") {
		t.Fatalf("unexpected delete-by-ids condition: %s", condition)
	}
	if strings.Count(condition, "vec-1") != 1 {
		t.Fatalf("expected duplicate ids to be de-duplicated, got %s", condition)
	}
}

// newLanceDBTestStore creates one initialized adapter backed by a bufconn gRPC server.
// newLanceDBTestStore 用于创建一个已经初始化完成、并由 bufconn gRPC 服务支撑的适配器实例。
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

	return &Store{
		conn:         conn,
		client:       lancedbv1.NewLanceDbServiceClient(conn),
		timeout:      time.Second,
		tableName:    resolveVectorTableName("vmm_memory_vectors", 3),
		vectorColumn: "vector",
		dimension:    3,
	}
}

// fakeLanceDBServer records gRPC requests so adapter tests can assert the emitted gateway contract.
// fakeLanceDBServer 用于记录 gRPC 请求，方便适配器测试断言实际发出的网关契约。
type fakeLanceDBServer struct {
	lancedbv1.UnimplementedLanceDbServiceServer
	mu             sync.Mutex
	createRequests []*lancedbv1.CreateTableRequest
	createResponse *lancedbv1.CreateTableResponse
	createErr      error
	upsertCalls    []*lancedbv1.UpsertRequest
	searchCalls    []*lancedbv1.SearchRequest
	searchData     []byte
	searchMessage  string
	searchSuccess  bool
	deleteCalls    []*lancedbv1.DeleteRequest
	dropCalls      []*lancedbv1.DropTableRequest
	dropResponse   *lancedbv1.DropTableResponse
	dropErr        error
}

// CreateTable records the bootstrap request and reports success so the adapter can initialize normally.
// CreateTable 用于记录启动建表请求，并返回成功结果让适配器正常初始化。
func (s *fakeLanceDBServer) CreateTable(_ context.Context, req *lancedbv1.CreateTableRequest) (*lancedbv1.CreateTableResponse, error) {
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
func (s *fakeLanceDBServer) VectorUpsert(_ context.Context, req *lancedbv1.UpsertRequest) (*lancedbv1.UpsertResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertCalls = append(s.upsertCalls, req)
	return &lancedbv1.UpsertResponse{Success: true, Message: "ok"}, nil
}

// VectorSearch records search requests and returns one canned JSON payload for deterministic adapter assertions.
// VectorSearch 用于记录检索请求，并返回预置 JSON 载荷供适配器做确定性断言。
func (s *fakeLanceDBServer) VectorSearch(_ context.Context, req *lancedbv1.SearchRequest) (*lancedbv1.SearchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchCalls = append(s.searchCalls, req)
	success := true
	if s.searchSuccess {
		success = s.searchSuccess
	}
	return &lancedbv1.SearchResponse{
		Success: success,
		Message: s.searchMessage,
		Data:    s.searchData,
	}, nil
}

// Delete returns a successful deletion response because these focused tests do not inspect delete semantics.
// Delete 用于返回成功删除响应，因为这些聚焦测试不会断言删除语义。
func (s *fakeLanceDBServer) Delete(_ context.Context, req *lancedbv1.DeleteRequest) (*lancedbv1.DeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, req)
	return &lancedbv1.DeleteResponse{Success: true, DeletedRows: 0, Message: "ok"}, nil
}

// DropTable records drop requests so tests can assert debug-clean behavior against the resolved runtime table name.
// DropTable 用于记录删表请求，方便测试断言调试清理是否命中了正确的运行时表名。
func (s *fakeLanceDBServer) DropTable(_ context.Context, req *lancedbv1.DropTableRequest) (*lancedbv1.DropTableResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropCalls = append(s.dropCalls, req)
	if s.dropErr != nil {
		return nil, s.dropErr
	}
	if s.dropResponse != nil {
		return s.dropResponse, nil
	}
	return &lancedbv1.DropTableResponse{Success: true, Message: "ok"}, nil
}

// upsertRequests returns a stable snapshot of recorded upsert requests.
// upsertRequests 用于返回已记录 upsert 请求的稳定快照。
func (s *fakeLanceDBServer) upsertRequests() []*lancedbv1.UpsertRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*lancedbv1.UpsertRequest, len(s.upsertCalls))
	copy(out, s.upsertCalls)
	return out
}

// searchRequests returns a stable snapshot of recorded search requests.
// searchRequests 用于返回已记录检索请求的稳定快照。
func (s *fakeLanceDBServer) searchRequests() []*lancedbv1.SearchRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*lancedbv1.SearchRequest, len(s.searchCalls))
	copy(out, s.searchCalls)
	return out
}

// dropRequests returns a stable snapshot of recorded drop-table requests.
// dropRequests 用于返回已记录删表请求的稳定快照。
func (s *fakeLanceDBServer) dropRequests() []*lancedbv1.DropTableRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*lancedbv1.DropTableRequest, len(s.dropCalls))
	copy(out, s.dropCalls)
	return out
}
