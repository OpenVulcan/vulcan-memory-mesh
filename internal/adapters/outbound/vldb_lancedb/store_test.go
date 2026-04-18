// store_test.go verifies the LanceDB local-FFI adapter keeps request shaping, filter generation, and table maintenance stable after the gRPC fallback path was removed.
// store_test.go 用于验证在移除 gRPC fallback 路径后，LanceDB 本地 FFI 适配器仍能保持请求成形、过滤表达式生成与表维护逻辑稳定。
package vldb_lancedb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	"github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"
)

// TestUpsertEncodesJSONRowsAndKeys verifies the adapter sends a dimension-qualified JSON-row upsert keyed by id.
// TestUpsertEncodesJSONRowsAndKeys 用于验证适配器会向带维度后缀的表发送一条以 id 为键的 JSON 行 upsert。
func TestUpsertEncodesJSONRowsAndKeys(t *testing.T) {
	engine := &fakeLanceDBEngine{}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}

	var capturedTable string
	var capturedFormat lancedbffi.InputFormat
	var capturedData []byte
	var capturedKeys []string
	engine.vectorUpsertFunc = func(tableName string, format lancedbffi.InputFormat, data []byte, keyColumns []string) (lancedbffi.UpsertResult, error) {
		capturedTable = tableName
		capturedFormat = format
		capturedData = append([]byte(nil), data...)
		capturedKeys = append([]string(nil), keyColumns...)
		return lancedbffi.UpsertResult{}, nil
	}

	err := store.Upsert(context.Background(), logicdomain.MemoryRecord{
		ID:           "mem-1",
		Text:         "gateway-backed memory",
		Vector:       []float32{0.1, 0.2, 0.3},
		SourceTurnID: 41,
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
	if capturedTable != "vmm_memory_vectors_3" || capturedFormat != lancedbffi.InputFormatJSONRows {
		t.Fatalf("unexpected upsert target: table=%q format=%d", capturedTable, capturedFormat)
	}
	if len(capturedKeys) != 1 || capturedKeys[0] != "id" {
		t.Fatalf("unexpected key columns = %#v", capturedKeys)
	}
	var rows []map[string]any
	if err := json.Unmarshal(capturedData, &rows); err != nil {
		t.Fatalf("decode upsert rows: %v", err)
	}
	if len(rows) != 1 || rows[0]["id"] != "mem-1" || rows[0]["content"] != "gateway-backed memory" {
		t.Fatalf("unexpected row payload = %#v", rows)
	}
}

// TestSearchMapsRowsAndFilter verifies JSON rows map back into numeric hierarchy filters while preserving shared-user semantics.
// TestSearchMapsRowsAndFilter 用于验证 JSON rows 会映射回数字层级过滤条件，并保留共享用户语义。
func TestSearchMapsRowsAndFilter(t *testing.T) {
	engine := &fakeLanceDBEngine{}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}

	var capturedFilter string
	engine.vectorSearchFunc = func(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
		capturedFilter = filter
		return lancedbffi.SearchResult{
			Format: lancedbffi.OutputFormatJSONRows,
			Rows:   1,
			Data: []byte(`[
{"id":"mem-1","content":"hello from gateway","team_id":3,"space_id":5,"project_id":9,"session_id":11,"user_id":0,"metadata_json":"{\"source\":\"seed\"}","_distance":0.25}
]`),
		}, nil
	}

	hits, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	})
	if err != nil {
		t.Fatalf("search memory vectors: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "mem-1" || hits[0].Filter.UserID != 0 {
		t.Fatalf("unexpected hits = %#v", hits)
	}
	if !strings.Contains(capturedFilter, "(user_id = 0 OR user_id = 7)") {
		t.Fatalf("unexpected filter expression: %s", capturedFilter)
	}
}

// TestSearchRejectsMalformedNumericFields verifies malformed numeric strings from JSON rows fail fast instead of being silently coerced.
// TestSearchRejectsMalformedNumericFields 用于验证 JSON rows 中的畸形数字字符串会快速报错，而不是被静默吞掉。
func TestSearchRejectsMalformedNumericFields(t *testing.T) {
	engine := &fakeLanceDBEngine{}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}

	engine.vectorSearchFunc = func(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
		return lancedbffi.SearchResult{
			Format: lancedbffi.OutputFormatJSONRows,
			Data: []byte(`[
{"id":"mem-1","content":"hello from gateway","team_id":"not-a-number","space_id":5,"project_id":9,"session_id":11,"user_id":7,"metadata_json":"{}","_distance":"bad-distance"}
]`),
		}, nil
	}

	_, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:    7,
		TeamID:    3,
		SpaceID:   5,
		ProjectID: 9,
	})
	if err == nil || !strings.Contains(err.Error(), "decode lancedb search rows") || !strings.Contains(err.Error(), "team_id") {
		t.Fatalf("expected wrapped numeric decode error, got %v", err)
	}
}

// TestSearchBuildsCompactBoundaryFilter verifies vector recall can exclude current-session post-compact turns in the first-stage predicate.
// TestSearchBuildsCompactBoundaryFilter 用于验证向量召回可以直接在第一阶段谓词里排除当前 session 的 post-compact turn。
func TestSearchBuildsCompactBoundaryFilter(t *testing.T) {
	engine := &fakeLanceDBEngine{}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}

	var capturedFilter string
	engine.vectorSearchFunc = func(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
		capturedFilter = filter
		return lancedbffi.SearchResult{Format: lancedbffi.OutputFormatJSONRows, Data: []byte(`[]`)}, nil
	}

	if _, err := store.Search(context.Background(), []float32{0.2, 0.3, 0.4}, 3, logicdomain.SearchFilter{
		UserID:            7,
		TeamID:            3,
		SpaceID:           5,
		ProjectID:         9,
		BoundarySessionID: 11,
		BoundaryMaxTurnID: 42,
	}); err != nil {
		t.Fatalf("search memory vectors: %v", err)
	}
	if !strings.Contains(capturedFilter, "session_id != 11") || !strings.Contains(capturedFilter, "source_turn_id <= 42") {
		t.Fatalf("unexpected compact boundary filter expression: %s", capturedFilter)
	}
}

// TestInitIgnoresAlreadyExistsResponses verifies repeated boots treat existing tables as a successful idempotent state.
// TestInitIgnoresAlreadyExistsResponses 用于验证重复启动会把“表已存在”视为成功的幂等状态。
func TestInitIgnoresAlreadyExistsResponses(t *testing.T) {
	engine := &fakeLanceDBEngine{
		createTableFunc: func(request lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error) {
			return lancedbffi.CreateTableResult{}, errTableExists()
		},
	}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate already-exists error: %v", err)
	}

	engine = &fakeLanceDBEngine{
		createTableFunc: func(request lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error) {
			return lancedbffi.CreateTableResult{Success: false, Message: "Table 'vmm_memory_vectors_3' already exists"}, nil
		},
	}
	store = &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}
	if err := store.init(context.Background()); err != nil {
		t.Fatalf("init should tolerate already-exists body response: %v", err)
	}
}

// TestDebugDropConfiguredTableTreatsMissingTableAsSuccess verifies repeated debug-clean attempts stay idempotent when the table is already gone.
// TestDebugDropConfiguredTableTreatsMissingTableAsSuccess 用于验证目标表已不存在时，重复 debug-clean 仍保持幂等成功。
func TestDebugDropConfiguredTableTreatsMissingTableAsSuccess(t *testing.T) {
	engine := &fakeLanceDBEngine{
		dropTableFunc: func(request lancedbffi.DropTableRequest) (lancedbffi.DropTableResult, error) {
			return lancedbffi.DropTableResult{Success: false, Message: "table does not exist"}, nil
		},
	}
	if err := debugDropTableWithEngine(context.Background(), engine, "vmm_memory_vectors_3", time.Second); err != nil {
		t.Fatalf("debug drop configured table should tolerate missing table: %v", err)
	}
}

// TestDeleteByIDsBuildsPreciseDeleteCondition verifies rollback deletes target only the supplied vector ids.
// TestDeleteByIDsBuildsPreciseDeleteCondition 用于验证回滚删除只会精确命中提供的向量 id。
func TestDeleteByIDsBuildsPreciseDeleteCondition(t *testing.T) {
	engine := &fakeLanceDBEngine{}
	store := &Store{engine: engine, tableName: "vmm_memory_vectors_3", vectorColumn: "vector", dimension: 3}

	var capturedCondition string
	engine.deleteFunc = func(request lancedbffi.DeleteRequest) (lancedbffi.DeleteResult, error) {
		capturedCondition = request.Condition
		return lancedbffi.DeleteResult{Success: true, DeletedRows: 0, Message: "ok"}, nil
	}

	deletedRows, err := store.DeleteByIDs(context.Background(), []string{"vec-1", "vec-2", "vec-1"})
	if err != nil {
		t.Fatalf("delete vector ids: %v", err)
	}
	if deletedRows != 0 {
		t.Fatalf("expected fake delete rows to stay 0, got %d", deletedRows)
	}
	if !strings.Contains(capturedCondition, "id = 'vec-1'") || !strings.Contains(capturedCondition, "id = 'vec-2'") {
		t.Fatalf("unexpected delete-by-ids condition: %s", capturedCondition)
	}
	if strings.Count(capturedCondition, "vec-1") != 1 {
		t.Fatalf("expected duplicate ids to be de-duplicated, got %s", capturedCondition)
	}
}

// errTableExists returns one create-table error string shaped like the upstream engine.
// errTableExists 用于返回一条与上游 engine 形态一致的“表已存在”错误。
func errTableExists() error {
	return &tableExistsError{message: "Table 'vmm_memory_vectors_3' already exists"}
}

// tableExistsError is one tiny error wrapper used by init idempotence tests.
// tableExistsError 用于作为 init 幂等测试里的一个轻量错误包装器。
type tableExistsError struct {
	message string
}

// Error returns the wrapped message.
// Error 用于返回包装后的错误消息。
func (e *tableExistsError) Error() string {
	return e.message
}
