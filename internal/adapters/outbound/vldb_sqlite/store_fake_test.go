// store_fake_test.go provides lightweight in-memory SQLite FFI doubles used by adapter tests after the gRPC gateway path was removed.
// store_fake_test.go 用于提供轻量级内存 SQLite FFI 替身，让适配器测试在移除 gRPC 网关路径后仍能聚焦断言本地调用行为。
package vldb_sqlite

import (
	"context"

	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
)

// fakeExecuteRequest captures one local ExecuteScript call issued by the adapter.
// fakeExecuteRequest 用于记录适配器发出的单次本地 ExecuteScript 调用。
type fakeExecuteRequest struct {
	SQL        string
	Params     []sqliteffi.SQLValue
	ParamsJSON string
}

// GetSql returns the recorded SQL text.
// GetSql 用于返回记录下来的 SQL 文本。
func (r *fakeExecuteRequest) GetSql() string {
	if r == nil {
		return ""
	}
	return r.SQL
}

// GetParams returns the recorded typed SQL params.
// GetParams 用于返回记录下来的强类型 SQL 参数。
func (r *fakeExecuteRequest) GetParams() []sqliteffi.SQLValue {
	if r == nil {
		return nil
	}
	return r.Params
}

// GetParamsJson returns the recorded legacy params_json payload.
// GetParamsJson 用于返回记录下来的 legacy params_json 载荷。
func (r *fakeExecuteRequest) GetParamsJson() string {
	if r == nil {
		return ""
	}
	return r.ParamsJSON
}

// fakeExecuteBatchItem captures one local ExecuteBatch row payload.
// fakeExecuteBatchItem 用于记录单条本地 ExecuteBatch 行载荷。
type fakeExecuteBatchItem struct {
	Params []sqliteffi.SQLValue
}

// GetParams returns the recorded batch params.
// GetParams 用于返回记录下来的批量参数。
func (i *fakeExecuteBatchItem) GetParams() []sqliteffi.SQLValue {
	if i == nil {
		return nil
	}
	return i.Params
}

// fakeExecuteBatchRequest captures one local ExecuteBatch call.
// fakeExecuteBatchRequest 用于记录单次本地 ExecuteBatch 调用。
type fakeExecuteBatchRequest struct {
	SQL   string
	Items []fakeExecuteBatchItem
}

// GetSql returns the recorded SQL text.
// GetSql 用于返回记录下来的 SQL 文本。
func (r *fakeExecuteBatchRequest) GetSql() string {
	if r == nil {
		return ""
	}
	return r.SQL
}

// GetItems returns the recorded batch rows.
// GetItems 用于返回记录下来的批量行。
func (r *fakeExecuteBatchRequest) GetItems() []fakeExecuteBatchItem {
	if r == nil {
		return nil
	}
	return r.Items
}

// fakeQueryRequest captures one local QueryJSON call.
// fakeQueryRequest 用于记录单次本地 QueryJSON 调用。
type fakeQueryRequest struct {
	SQL        string
	Params     []sqliteffi.SQLValue
	ParamsJSON string
}

// GetSql returns the recorded SQL text.
// GetSql 用于返回记录下来的 SQL 文本。
func (r *fakeQueryRequest) GetSql() string {
	if r == nil {
		return ""
	}
	return r.SQL
}

// GetParams returns the recorded typed SQL params.
// GetParams 用于返回记录下来的强类型 SQL 参数。
func (r *fakeQueryRequest) GetParams() []sqliteffi.SQLValue {
	if r == nil {
		return nil
	}
	return r.Params
}

// GetParamsJson returns the recorded legacy params_json payload.
// GetParamsJson 用于返回记录下来的 legacy params_json 载荷。
func (r *fakeQueryRequest) GetParamsJson() string {
	if r == nil {
		return ""
	}
	return r.ParamsJSON
}

// fakeExecuteResponse mirrors the subset of execute metadata that adapter tests care about.
// fakeExecuteResponse 用于镜像适配器测试关心的执行响应元数据子集。
type fakeExecuteResponse struct {
	Success            bool
	Message            string
	RowsChanged        int64
	LastInsertRowid    int64
	StatementsExecuted int64
}

// fakeQueryJSONResponse mirrors the subset of query-json metadata that adapter tests care about.
// fakeQueryJSONResponse 用于镜像适配器测试关心的 query-json 响应子集。
type fakeQueryJSONResponse struct {
	JsonData string
	RowCount uint64
}

// fakeSQLiteDatabase is one lightweight in-memory database double that satisfies the adapter's local FFI dependency surface.
// fakeSQLiteDatabase 用于作为轻量级内存数据库替身，满足适配器依赖的本地 FFI 能力面。
type fakeSQLiteDatabase struct {
	executeScriptFunc     func(context.Context, *fakeExecuteRequest) (*fakeExecuteResponse, error)
	executeBatchFunc      func(context.Context, *fakeExecuteBatchRequest) (*fakeExecuteResponse, error)
	queryJSONFunc         func(context.Context, *fakeQueryRequest) (*fakeQueryJSONResponse, error)
	ensureFtsIndexFunc    func(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error)
	rebuildFtsIndexFunc   func(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error)
	upsertFtsDocumentFunc func(indexName string, mode sqliteffi.TokenizerMode, id string, filePath string, title string, content string) (sqliteffi.FtsMutationResult, error)
	deleteFtsDocumentFunc func(indexName string, id string) (sqliteffi.FtsMutationResult, error)
	searchFtsFunc         func(indexName string, mode sqliteffi.TokenizerMode, query string, limit uint32, offset uint32) (sqliteffi.SearchResult, error)
}

// ExecuteScript records or delegates one ExecuteScript request.
// ExecuteScript 用于记录或转发一次 ExecuteScript 请求。
func (f *fakeSQLiteDatabase) ExecuteScript(sql string, params []sqliteffi.SQLValue, paramsJSON string) (sqliteffi.ExecuteResult, error) {
	if f == nil || f.executeScriptFunc == nil {
		return sqliteffi.ExecuteResult{Success: true}, nil
	}
	response, err := f.executeScriptFunc(context.Background(), &fakeExecuteRequest{
		SQL:        sql,
		Params:     append([]sqliteffi.SQLValue(nil), params...),
		ParamsJSON: paramsJSON,
	})
	if err != nil {
		return sqliteffi.ExecuteResult{}, err
	}
	if response == nil {
		return sqliteffi.ExecuteResult{}, nil
	}
	return sqliteffi.ExecuteResult{
		Success:            response.Success,
		Message:            response.Message,
		RowsChanged:        response.RowsChanged,
		LastInsertRowID:    response.LastInsertRowid,
		StatementsExecuted: response.StatementsExecuted,
	}, nil
}

// ExecuteBatch records or delegates one ExecuteBatch request.
// ExecuteBatch 用于记录或转发一次 ExecuteBatch 请求。
func (f *fakeSQLiteDatabase) ExecuteBatch(sql string, items [][]sqliteffi.SQLValue) (sqliteffi.ExecuteResult, error) {
	if f == nil || f.executeBatchFunc == nil {
		return sqliteffi.ExecuteResult{Success: true}, nil
	}
	recordedItems := make([]fakeExecuteBatchItem, 0, len(items))
	for _, item := range items {
		recordedItems = append(recordedItems, fakeExecuteBatchItem{Params: append([]sqliteffi.SQLValue(nil), item...)})
	}
	response, err := f.executeBatchFunc(context.Background(), &fakeExecuteBatchRequest{
		SQL:   sql,
		Items: recordedItems,
	})
	if err != nil {
		return sqliteffi.ExecuteResult{}, err
	}
	if response == nil {
		return sqliteffi.ExecuteResult{}, nil
	}
	return sqliteffi.ExecuteResult{
		Success:            response.Success,
		Message:            response.Message,
		RowsChanged:        response.RowsChanged,
		LastInsertRowID:    response.LastInsertRowid,
		StatementsExecuted: response.StatementsExecuted,
	}, nil
}

// QueryJSON records or delegates one QueryJSON request.
// QueryJSON 用于记录或转发一次 QueryJSON 请求。
func (f *fakeSQLiteDatabase) QueryJSON(sql string, params []sqliteffi.SQLValue, paramsJSON string) (sqliteffi.QueryJSONResult, error) {
	if f == nil || f.queryJSONFunc == nil {
		return sqliteffi.QueryJSONResult{JSONData: "[]"}, nil
	}
	response, err := f.queryJSONFunc(context.Background(), &fakeQueryRequest{
		SQL:        sql,
		Params:     append([]sqliteffi.SQLValue(nil), params...),
		ParamsJSON: paramsJSON,
	})
	if err != nil {
		return sqliteffi.QueryJSONResult{}, err
	}
	if response == nil {
		return sqliteffi.QueryJSONResult{}, nil
	}
	return sqliteffi.QueryJSONResult{
		JSONData: response.JsonData,
		RowCount: response.RowCount,
	}, nil
}

// EnsureFtsIndex records or delegates one FTS ensure request.
// EnsureFtsIndex 用于记录或转发一次 FTS ensure 请求。
func (f *fakeSQLiteDatabase) EnsureFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.EnsureFtsIndexResult, error) {
	if f == nil || f.ensureFtsIndexFunc == nil {
		return sqliteffi.EnsureFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}
	return f.ensureFtsIndexFunc(indexName, mode)
}

// RebuildFtsIndex records or delegates one FTS rebuild request.
// RebuildFtsIndex 用于记录或转发一次 FTS rebuild 请求。
func (f *fakeSQLiteDatabase) RebuildFtsIndex(indexName string, mode sqliteffi.TokenizerMode) (sqliteffi.RebuildFtsIndexResult, error) {
	if f == nil || f.rebuildFtsIndexFunc == nil {
		return sqliteffi.RebuildFtsIndexResult{Success: true, TokenizerMode: mode}, nil
	}
	return f.rebuildFtsIndexFunc(indexName, mode)
}

// UpsertFtsDocument records or delegates one FTS upsert request.
// UpsertFtsDocument 用于记录或转发一次 FTS upsert 请求。
func (f *fakeSQLiteDatabase) UpsertFtsDocument(indexName string, mode sqliteffi.TokenizerMode, id string, filePath string, title string, content string) (sqliteffi.FtsMutationResult, error) {
	if f == nil || f.upsertFtsDocumentFunc == nil {
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}
	return f.upsertFtsDocumentFunc(indexName, mode, id, filePath, title, content)
}

// DeleteFtsDocument records or delegates one FTS delete request.
// DeleteFtsDocument 用于记录或转发一次 FTS delete 请求。
func (f *fakeSQLiteDatabase) DeleteFtsDocument(indexName string, id string) (sqliteffi.FtsMutationResult, error) {
	if f == nil || f.deleteFtsDocumentFunc == nil {
		return sqliteffi.FtsMutationResult{Success: true, AffectedRows: 1}, nil
	}
	return f.deleteFtsDocumentFunc(indexName, id)
}

// SearchFts records or delegates one FTS search request.
// SearchFts 用于记录或转发一次 FTS search 请求。
func (f *fakeSQLiteDatabase) SearchFts(indexName string, mode sqliteffi.TokenizerMode, query string, limit uint32, offset uint32) (sqliteffi.SearchResult, error) {
	if f == nil || f.searchFtsFunc == nil {
		return sqliteffi.SearchResult{}, nil
	}
	return f.searchFtsFunc(indexName, mode, query, limit, offset)
}

// Close releases the fake handle and keeps the interface contract complete.
// Close 用于释放 fake 句柄，并保持接口契约完整。
func (f *fakeSQLiteDatabase) Close() error {
	return nil
}
