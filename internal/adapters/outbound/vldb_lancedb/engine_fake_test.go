// engine_fake_test.go provides lightweight in-memory LanceDB engine doubles used by adapter tests after the gRPC fallback path was removed.
// engine_fake_test.go 用于提供轻量级内存 LanceDB engine 替身，让适配器测试在移除 gRPC fallback 路径后仍能聚焦断言本地调用行为。
package vldb_lancedb

import "github.com/openvulcan/vmm/internal/platform/ffi/lancedbffi"

// fakeLanceDBEngine is one focused in-memory engine double for vector-store tests.
// fakeLanceDBEngine 用于作为向量存储测试的聚焦内存 engine 替身。
type fakeLanceDBEngine struct {
	createTableFunc  func(lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error)
	vectorUpsertFunc func(tableName string, format lancedbffi.InputFormat, data []byte, keyColumns []string) (lancedbffi.UpsertResult, error)
	vectorSearchFunc func(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error)
	deleteFunc       func(lancedbffi.DeleteRequest) (lancedbffi.DeleteResult, error)
	dropTableFunc    func(lancedbffi.DropTableRequest) (lancedbffi.DropTableResult, error)
}

// CreateTable records or delegates one create-table request.
// CreateTable 用于记录或转发一次建表请求。
func (f *fakeLanceDBEngine) CreateTable(request lancedbffi.CreateTableRequest) (lancedbffi.CreateTableResult, error) {
	if f == nil || f.createTableFunc == nil {
		return lancedbffi.CreateTableResult{Success: true, Message: "ok"}, nil
	}
	return f.createTableFunc(request)
}

// VectorUpsertRaw records or delegates one vector upsert request.
// VectorUpsertRaw 用于记录或转发一次向量写入请求。
func (f *fakeLanceDBEngine) VectorUpsertRaw(tableName string, format lancedbffi.InputFormat, data []byte, keyColumns []string) (lancedbffi.UpsertResult, error) {
	if f == nil || f.vectorUpsertFunc == nil {
		return lancedbffi.UpsertResult{}, nil
	}
	return f.vectorUpsertFunc(tableName, format, data, keyColumns)
}

// VectorSearchF32 records or delegates one vector search request.
// VectorSearchF32 用于记录或转发一次向量检索请求。
func (f *fakeLanceDBEngine) VectorSearchF32(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lancedbffi.OutputFormat) (lancedbffi.SearchResult, error) {
	if f == nil || f.vectorSearchFunc == nil {
		return lancedbffi.SearchResult{}, nil
	}
	return f.vectorSearchFunc(tableName, vector, limit, filter, vectorColumn, outputFormat)
}

// Delete records or delegates one delete request.
// Delete 用于记录或转发一次删除请求。
func (f *fakeLanceDBEngine) Delete(request lancedbffi.DeleteRequest) (lancedbffi.DeleteResult, error) {
	if f == nil || f.deleteFunc == nil {
		return lancedbffi.DeleteResult{Success: true, Message: "ok"}, nil
	}
	return f.deleteFunc(request)
}

// DropTable records or delegates one drop-table request.
// DropTable 用于记录或转发一次删表请求。
func (f *fakeLanceDBEngine) DropTable(request lancedbffi.DropTableRequest) (lancedbffi.DropTableResult, error) {
	if f == nil || f.dropTableFunc == nil {
		return lancedbffi.DropTableResult{Success: true, Message: "ok"}, nil
	}
	return f.dropTableFunc(request)
}

// Close keeps the fake aligned with the production engine interface.
// Close 用于让 fake 与生产 engine 接口保持一致。
func (f *fakeLanceDBEngine) Close() error {
	return nil
}
