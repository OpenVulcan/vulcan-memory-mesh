// sqliteffi.go provides the minimal SQLite FFI wrapper used by VMM split-mode relational storage and built-in FTS integration.
// sqliteffi.go 用于提供 VMM split 模式关系存储与内建 FTS 集成所需的最小 SQLite FFI 包装层。
package sqliteffi

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// StatusCode represents the FFI invocation status code.
// StatusCode 表示 FFI 调用状态码。
type StatusCode int32

const (
	// StatusSuccess indicates a successful FFI invocation.
	// StatusSuccess 表示 FFI 调用成功。
	StatusSuccess StatusCode = 0
	// StatusFailure indicates a failed FFI invocation.
	// StatusFailure 表示 FFI 调用失败。
	StatusFailure StatusCode = 1
)

// TokenizerMode represents the SQLite FFI tokenizer mode.
// TokenizerMode 表示 SQLite FFI 分词模式。
type TokenizerMode int32

const (
	// TokenizerNone disables the Chinese tokenizer.
	// TokenizerNone 表示关闭中文分词器。
	TokenizerNone TokenizerMode = 0
	// TokenizerJieba enables the bundled Jieba tokenizer.
	// TokenizerJieba 表示启用内建 Jieba 分词器。
	TokenizerJieba TokenizerMode = 1
)

// SQLValueKind represents the SQL parameter value kind accepted by the FFI ABI.
// SQLValueKind 表示 FFI ABI 接受的 SQL 参数类型。
type SQLValueKind int32

const (
	// SQLValueNull represents a NULL SQL parameter.
	// SQLValueNull 表示 NULL SQL 参数。
	SQLValueNull SQLValueKind = 0
	// SQLValueInt64 represents an int64 SQL parameter.
	// SQLValueInt64 表示 int64 SQL 参数。
	SQLValueInt64 SQLValueKind = 1
	// SQLValueFloat64 represents a float64 SQL parameter.
	// SQLValueFloat64 表示 float64 SQL 参数。
	SQLValueFloat64 SQLValueKind = 2
	// SQLValueString represents a string SQL parameter.
	// SQLValueString 表示字符串 SQL 参数。
	SQLValueString SQLValueKind = 3
	// SQLValueBytes represents a byte-array SQL parameter.
	// SQLValueBytes 表示字节数组 SQL 参数。
	SQLValueBytes SQLValueKind = 4
	// SQLValueBool represents a boolean SQL parameter.
	// SQLValueBool 表示布尔 SQL 参数。
	SQLValueBool SQLValueKind = 5
)

// SQLValue describes one SQL parameter passed into the FFI layer.
// SQLValue 描述一条传入 FFI 层的 SQL 参数。
type SQLValue struct {
	Kind    SQLValueKind
	Int64   int64
	Float64 float64
	String  string
	Bytes   []byte
	Bool    bool
}

// ExecuteResult describes the unified execute and execute-batch result.
// ExecuteResult 描述 execute 与 execute-batch 的统一结果。
type ExecuteResult struct {
	Success            bool
	Message            string
	RowsChanged        int64
	LastInsertRowID    int64
	StatementsExecuted int64
}

// QueryJSONResult describes one query-json result.
// QueryJSONResult 描述一次 query-json 结果。
type QueryJSONResult struct {
	JSONData string
	RowCount uint64
}

// EnsureFtsIndexResult describes the ensure-index result.
// EnsureFtsIndexResult 描述 ensure-index 结果。
type EnsureFtsIndexResult struct {
	Success       bool
	TokenizerMode TokenizerMode
}

// RebuildFtsIndexResult describes the rebuild-index result.
// RebuildFtsIndexResult 描述 rebuild-index 结果。
type RebuildFtsIndexResult struct {
	Success       bool
	TokenizerMode TokenizerMode
	ReindexedRows uint64
}

// FtsMutationResult describes one FTS document mutation result.
// FtsMutationResult 描述一次 FTS 文档变更结果。
type FtsMutationResult struct {
	Success      bool
	AffectedRows uint64
}

// SearchHit describes one FTS search hit.
// SearchHit 描述一条 FTS 检索命中。
type SearchHit struct {
	ID             string
	FilePath       string
	Title          string
	TitleHighlight string
	ContentSnippet string
	Score          float64
	Rank           uint64
	RawScore       float64
}

// SearchResult describes the FTS search response.
// SearchResult 描述 FTS 检索响应。
type SearchResult struct {
	Total     uint64
	Source    string
	QueryMode string
	Hits      []SearchHit
}

// Library represents one loaded SQLite dynamic library.
// Library 表示一个已加载的 SQLite 动态库。
type Library struct {
	handle uintptr

	runtimeCreateDefault          func() unsafe.Pointer
	runtimeDestroy                func(unsafe.Pointer)
	runtimeOpenDatabase           func(unsafe.Pointer, *byte) unsafe.Pointer
	runtimeCloseDatabase          func(unsafe.Pointer, *byte) uint8
	databaseDestroy               func(unsafe.Pointer)
	databaseDBPath                func(unsafe.Pointer) *byte
	databaseExecuteScript         func(unsafe.Pointer, *byte, *ffiValue, uint64, *byte) unsafe.Pointer
	databaseExecuteBatch          func(unsafe.Pointer, *byte, *ffiValueSlice, uint64) unsafe.Pointer
	databaseQueryJSON             func(unsafe.Pointer, *byte, *ffiValue, uint64, *byte) unsafe.Pointer
	executeResultDestroy          func(unsafe.Pointer)
	executeResultSuccess          func(unsafe.Pointer) uint8
	executeResultMessage          func(unsafe.Pointer) *byte
	executeResultRowsChanged      func(unsafe.Pointer) int64
	executeResultLastInsertRowID  func(unsafe.Pointer) int64
	executeResultStatementsExec   func(unsafe.Pointer) int64
	queryJSONResultDestroy        func(unsafe.Pointer)
	queryJSONResultJSONData       func(unsafe.Pointer) *byte
	queryJSONResultRowCount       func(unsafe.Pointer) uint64
	databaseEnsureFtsIndex        func(unsafe.Pointer, *byte, TokenizerMode, *ensureFtsIndexResultPod) int32
	databaseRebuildFtsIndex       func(unsafe.Pointer, *byte, TokenizerMode, *rebuildFtsIndexResultPod) int32
	databaseUpsertFtsDocument     func(unsafe.Pointer, *byte, TokenizerMode, *byte, *byte, *byte, *byte, *ftsMutationResultPod) int32
	databaseDeleteFtsDocument     func(unsafe.Pointer, *byte, *byte, *ftsMutationResultPod) int32
	databaseSearchFts             func(unsafe.Pointer, *byte, TokenizerMode, *byte, uint32, uint32) unsafe.Pointer
	searchResultDestroy           func(unsafe.Pointer)
	searchResultTotal             func(unsafe.Pointer) uint64
	searchResultLen               func(unsafe.Pointer) uint64
	searchResultSource            func(unsafe.Pointer) *byte
	searchResultQueryMode         func(unsafe.Pointer) *byte
	searchResultGetID             func(unsafe.Pointer, uint64) *byte
	searchResultGetFilePath       func(unsafe.Pointer, uint64) *byte
	searchResultGetTitle          func(unsafe.Pointer, uint64) *byte
	searchResultGetTitleHighlight func(unsafe.Pointer, uint64) *byte
	searchResultGetContentSnippet func(unsafe.Pointer, uint64) *byte
	searchResultGetScore          func(unsafe.Pointer, uint64) float64
	searchResultGetRank           func(unsafe.Pointer, uint64) uint64
	searchResultGetRawScore       func(unsafe.Pointer, uint64) float64
	stringFree                    func(*byte)
	lastErrorMessage              func() *byte
	clearLastError                func()
}

// Runtime represents the SQLite runtime handle.
// Runtime 表示 SQLite 运行时句柄。
type Runtime struct {
	lib    *Library
	handle unsafe.Pointer
}

// Database represents one opened SQLite database handle.
// Database 表示一个已打开的 SQLite 数据库句柄。
type Database struct {
	lib    *Library
	handle unsafe.Pointer
}

type ensureFtsIndexResultPod struct {
	Success       uint8
	_             [3]byte
	TokenizerMode uint32
}

type rebuildFtsIndexResultPod struct {
	Success       uint8
	_             [3]byte
	TokenizerMode uint32
	ReindexedRows uint64
}

type ftsMutationResultPod struct {
	Success      uint8
	_            [7]byte
	AffectedRows uint64
}

type byteView struct {
	Data *byte
	Len  uint64
}

type ffiValue struct {
	Kind         SQLValueKind
	Int64Value   int64
	Float64Value float64
	StringValue  *byte
	BytesValue   byteView
	BoolValue    uint8
}

type ffiValueSlice struct {
	Values *ffiValue
	Len    uint64
}

type ffiValueArray struct {
	values        []ffiValue
	stringKeepers [][]byte
	bytesKeepers  [][]byte
}

type ffiValueMatrix struct {
	slices []ffiValueSlice
	rows   []ffiValueArray
}

// Open loads the target SQLite dynamic library and binds the required FFI symbols.
// Open 用于加载目标 SQLite 动态库并绑定所需 FFI 符号。
func Open(path string) (*Library, error) {
	handle, err := openLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("加载 SQLite 动态库失败 / failed to load SQLite dynamic library: %w", err)
	}

	lib := &Library{handle: handle}
	bind := func(target any, name string) {
		purego.RegisterLibFunc(target, handle, name)
	}

	bind(&lib.runtimeCreateDefault, "vldb_sqlite_runtime_create_default")
	bind(&lib.runtimeDestroy, "vldb_sqlite_runtime_destroy")
	bind(&lib.runtimeOpenDatabase, "vldb_sqlite_runtime_open_database")
	bind(&lib.runtimeCloseDatabase, "vldb_sqlite_runtime_close_database")
	bind(&lib.databaseDestroy, "vldb_sqlite_database_destroy")
	bind(&lib.databaseDBPath, "vldb_sqlite_database_db_path")
	bind(&lib.databaseExecuteScript, "vldb_sqlite_database_execute_script")
	bind(&lib.databaseExecuteBatch, "vldb_sqlite_database_execute_batch")
	bind(&lib.databaseQueryJSON, "vldb_sqlite_database_query_json")
	bind(&lib.executeResultDestroy, "vldb_sqlite_execute_result_destroy")
	bind(&lib.executeResultSuccess, "vldb_sqlite_execute_result_success")
	bind(&lib.executeResultMessage, "vldb_sqlite_execute_result_message")
	bind(&lib.executeResultRowsChanged, "vldb_sqlite_execute_result_rows_changed")
	bind(&lib.executeResultLastInsertRowID, "vldb_sqlite_execute_result_last_insert_rowid")
	bind(&lib.executeResultStatementsExec, "vldb_sqlite_execute_result_statements_executed")
	bind(&lib.queryJSONResultDestroy, "vldb_sqlite_query_json_result_destroy")
	bind(&lib.queryJSONResultJSONData, "vldb_sqlite_query_json_result_json_data")
	bind(&lib.queryJSONResultRowCount, "vldb_sqlite_query_json_result_row_count")
	bind(&lib.databaseEnsureFtsIndex, "vldb_sqlite_database_ensure_fts_index")
	bind(&lib.databaseRebuildFtsIndex, "vldb_sqlite_database_rebuild_fts_index")
	bind(&lib.databaseUpsertFtsDocument, "vldb_sqlite_database_upsert_fts_document")
	bind(&lib.databaseDeleteFtsDocument, "vldb_sqlite_database_delete_fts_document")
	bind(&lib.databaseSearchFts, "vldb_sqlite_database_search_fts")
	bind(&lib.searchResultDestroy, "vldb_sqlite_search_result_destroy")
	bind(&lib.searchResultTotal, "vldb_sqlite_search_result_total")
	bind(&lib.searchResultLen, "vldb_sqlite_search_result_len")
	bind(&lib.searchResultSource, "vldb_sqlite_search_result_source")
	bind(&lib.searchResultQueryMode, "vldb_sqlite_search_result_query_mode")
	bind(&lib.searchResultGetID, "vldb_sqlite_search_result_get_id")
	bind(&lib.searchResultGetFilePath, "vldb_sqlite_search_result_get_file_path")
	bind(&lib.searchResultGetTitle, "vldb_sqlite_search_result_get_title")
	bind(&lib.searchResultGetTitleHighlight, "vldb_sqlite_search_result_get_title_highlight")
	bind(&lib.searchResultGetContentSnippet, "vldb_sqlite_search_result_get_content_snippet")
	bind(&lib.searchResultGetScore, "vldb_sqlite_search_result_get_score")
	bind(&lib.searchResultGetRank, "vldb_sqlite_search_result_get_rank")
	bind(&lib.searchResultGetRawScore, "vldb_sqlite_search_result_get_raw_score")
	bind(&lib.stringFree, "vldb_sqlite_string_free")
	bind(&lib.lastErrorMessage, "vldb_sqlite_last_error_message")
	bind(&lib.clearLastError, "vldb_sqlite_clear_last_error")

	return lib, nil
}

// Close releases the loaded SQLite dynamic library handle.
// Close 用于释放已加载的 SQLite 动态库句柄。
func (lib *Library) Close() error {
	if lib == nil || lib.handle == 0 {
		return nil
	}
	err := closeLibrary(lib.handle)
	lib.handle = 0
	return err
}

// CreateRuntime creates the default SQLite runtime.
// CreateRuntime 用于创建默认 SQLite 运行时。
func (lib *Library) CreateRuntime() (*Runtime, error) {
	if lib == nil {
		return nil, errors.New("library is nil / library 不能为空")
	}
	handle := lib.runtimeCreateDefault()
	if handle == nil {
		return nil, lib.lastError()
	}
	rt := &Runtime{lib: lib, handle: handle}
	runtime.SetFinalizer(rt, func(value *Runtime) {
		_ = value.Close()
	})
	return rt, nil
}

// Close destroys the runtime handle.
// Close 用于销毁运行时句柄。
func (rt *Runtime) Close() error {
	if rt == nil || rt.handle == nil {
		return nil
	}
	rt.lib.runtimeDestroy(rt.handle)
	rt.handle = nil
	runtime.SetFinalizer(rt, nil)
	return nil
}

// OpenDatabase opens one SQLite database by path.
// OpenDatabase 用于按路径打开一个 SQLite 数据库。
func (rt *Runtime) OpenDatabase(path string) (*Database, error) {
	ptr, keep := makeCString(path)
	defer keep()
	handle := rt.lib.runtimeOpenDatabase(rt.handle, ptr)
	if handle == nil {
		return nil, rt.lib.lastError()
	}
	db := &Database{lib: rt.lib, handle: handle}
	runtime.SetFinalizer(db, func(value *Database) {
		_ = value.Close()
	})
	return db, nil
}

// CloseDatabase closes one opened database by path inside the runtime registry.
// CloseDatabase 用于在运行时注册表中按路径关闭一个已打开数据库。
func (rt *Runtime) CloseDatabase(path string) bool {
	ptr, keep := makeCString(path)
	defer keep()
	return rt.lib.runtimeCloseDatabase(rt.handle, ptr) != 0
}

// Close destroys the database handle.
// Close 用于销毁数据库句柄。
func (db *Database) Close() error {
	if db == nil || db.handle == nil {
		return nil
	}
	db.lib.databaseDestroy(db.handle)
	db.handle = nil
	runtime.SetFinalizer(db, nil)
	return nil
}

// DBPath returns the physical database path for the current handle.
// DBPath 用于返回当前句柄对应的物理数据库路径。
func (db *Database) DBPath() (string, error) {
	return db.lib.takeOwnedString(func() *byte {
		return db.lib.databaseDBPath(db.handle)
	})
}

// ExecuteScript executes one SQL script with typed parameters.
// ExecuteScript 用于执行一段带强类型参数的 SQL 脚本。
func (db *Database) ExecuteScript(sql string, params []SQLValue, paramsJSON string) (ExecuteResult, error) {
	sqlPtr, keepSQL := makeCString(sql)
	defer keepSQL()
	values, keepValues, err := buildFFIValues(params)
	if err != nil {
		return ExecuteResult{}, err
	}
	defer keepValues()
	paramsJSONPtr, keepParamsJSON := nullableCString(paramsJSON)
	defer keepParamsJSON()

	handle := db.lib.databaseExecuteScript(db.handle, sqlPtr, values.ptr(), uint64(len(values.values)), paramsJSONPtr)
	if handle == nil {
		return ExecuteResult{}, db.lib.lastError()
	}
	defer db.lib.executeResultDestroy(handle)
	return db.lib.readExecuteResult(handle)
}

// ExecuteBatch executes one repeated-shape SQL batch with typed parameters.
// ExecuteBatch 用于执行一组同构的带强类型参数 SQL 批处理。
func (db *Database) ExecuteBatch(sql string, items [][]SQLValue) (ExecuteResult, error) {
	sqlPtr, keepSQL := makeCString(sql)
	defer keepSQL()
	matrix, keepMatrix, err := buildFFIValueMatrix(items)
	if err != nil {
		return ExecuteResult{}, err
	}
	defer keepMatrix()

	handle := db.lib.databaseExecuteBatch(db.handle, sqlPtr, matrix.ptr(), uint64(len(matrix.slices)))
	if handle == nil {
		return ExecuteResult{}, db.lib.lastError()
	}
	defer db.lib.executeResultDestroy(handle)
	return db.lib.readExecuteResult(handle)
}

// QueryJSON executes one SQL query and returns the JSON rows result.
// QueryJSON 用于执行一条 SQL 查询并返回 JSON rows 结果。
func (db *Database) QueryJSON(sql string, params []SQLValue, paramsJSON string) (QueryJSONResult, error) {
	sqlPtr, keepSQL := makeCString(sql)
	defer keepSQL()
	values, keepValues, err := buildFFIValues(params)
	if err != nil {
		return QueryJSONResult{}, err
	}
	defer keepValues()
	paramsJSONPtr, keepParamsJSON := nullableCString(paramsJSON)
	defer keepParamsJSON()

	handle := db.lib.databaseQueryJSON(db.handle, sqlPtr, values.ptr(), uint64(len(values.values)), paramsJSONPtr)
	if handle == nil {
		return QueryJSONResult{}, db.lib.lastError()
	}
	defer db.lib.queryJSONResultDestroy(handle)

	jsonData, err := db.lib.takeOwnedString(func() *byte {
		return db.lib.queryJSONResultJSONData(handle)
	})
	if err != nil {
		return QueryJSONResult{}, err
	}
	return QueryJSONResult{
		JSONData: jsonData,
		RowCount: db.lib.queryJSONResultRowCount(handle),
	}, nil
}

// EnsureFtsIndex ensures one named FTS index exists with the provided tokenizer mode.
// EnsureFtsIndex 用于确保一个命名 FTS 索引存在，并应用给定分词模式。
func (db *Database) EnsureFtsIndex(indexName string, mode TokenizerMode) (EnsureFtsIndexResult, error) {
	ptr, keep := makeCString(indexName)
	defer keep()
	var pod ensureFtsIndexResultPod
	status := StatusCode(db.lib.databaseEnsureFtsIndex(db.handle, ptr, mode, &pod))
	if status != StatusSuccess {
		return EnsureFtsIndexResult{}, db.lib.lastError()
	}
	return EnsureFtsIndexResult{
		Success:       pod.Success != 0,
		TokenizerMode: TokenizerMode(pod.TokenizerMode),
	}, nil
}

// RebuildFtsIndex rebuilds one named FTS index with the provided tokenizer mode.
// RebuildFtsIndex 用于按给定分词模式重建一个命名 FTS 索引。
func (db *Database) RebuildFtsIndex(indexName string, mode TokenizerMode) (RebuildFtsIndexResult, error) {
	ptr, keep := makeCString(indexName)
	defer keep()
	var pod rebuildFtsIndexResultPod
	status := StatusCode(db.lib.databaseRebuildFtsIndex(db.handle, ptr, mode, &pod))
	if status != StatusSuccess {
		return RebuildFtsIndexResult{}, db.lib.lastError()
	}
	return RebuildFtsIndexResult{
		Success:       pod.Success != 0,
		TokenizerMode: TokenizerMode(pod.TokenizerMode),
		ReindexedRows: pod.ReindexedRows,
	}, nil
}

// UpsertFtsDocument writes one document into the named FTS index.
// UpsertFtsDocument 用于把一条文档写入指定 FTS 索引。
func (db *Database) UpsertFtsDocument(indexName string, mode TokenizerMode, id string, filePath string, title string, content string) (FtsMutationResult, error) {
	indexPtr, keepIndex := makeCString(indexName)
	defer keepIndex()
	idPtr, keepID := makeCString(id)
	defer keepID()
	filePtr, keepFile := makeCString(filePath)
	defer keepFile()
	titlePtr, keepTitle := makeCString(title)
	defer keepTitle()
	contentPtr, keepContent := makeCString(content)
	defer keepContent()

	var pod ftsMutationResultPod
	status := StatusCode(db.lib.databaseUpsertFtsDocument(db.handle, indexPtr, mode, idPtr, filePtr, titlePtr, contentPtr, &pod))
	if status != StatusSuccess {
		return FtsMutationResult{}, db.lib.lastError()
	}
	return FtsMutationResult{
		Success:      pod.Success != 0,
		AffectedRows: pod.AffectedRows,
	}, nil
}

// DeleteFtsDocument deletes one document from the named FTS index by id.
// DeleteFtsDocument 用于按 id 从指定 FTS 索引删除一条文档。
func (db *Database) DeleteFtsDocument(indexName string, id string) (FtsMutationResult, error) {
	indexPtr, keepIndex := makeCString(indexName)
	defer keepIndex()
	idPtr, keepID := makeCString(id)
	defer keepID()

	var pod ftsMutationResultPod
	status := StatusCode(db.lib.databaseDeleteFtsDocument(db.handle, indexPtr, idPtr, &pod))
	if status != StatusSuccess {
		return FtsMutationResult{}, db.lib.lastError()
	}
	return FtsMutationResult{
		Success:      pod.Success != 0,
		AffectedRows: pod.AffectedRows,
	}, nil
}

// SearchFts executes one normalized BM25 search against the named index.
// SearchFts 用于针对指定索引执行一次标准化 BM25 检索。
func (db *Database) SearchFts(indexName string, mode TokenizerMode, query string, limit uint32, offset uint32) (SearchResult, error) {
	indexPtr, keepIndex := makeCString(indexName)
	defer keepIndex()
	queryPtr, keepQuery := makeCString(query)
	defer keepQuery()

	handle := db.lib.databaseSearchFts(db.handle, indexPtr, mode, queryPtr, limit, offset)
	if handle == nil {
		return SearchResult{}, db.lib.lastError()
	}
	defer db.lib.searchResultDestroy(handle)

	source, err := db.lib.takeOwnedString(func() *byte {
		return db.lib.searchResultSource(handle)
	})
	if err != nil {
		return SearchResult{}, err
	}
	queryMode, err := db.lib.takeOwnedString(func() *byte {
		return db.lib.searchResultQueryMode(handle)
	})
	if err != nil {
		return SearchResult{}, err
	}
	total := db.lib.searchResultTotal(handle)
	length := db.lib.searchResultLen(handle)
	hits := make([]SearchHit, 0, length)
	for i := uint64(0); i < length; i++ {
		id, err := db.lib.takeOwnedString(func() *byte { return db.lib.searchResultGetID(handle, i) })
		if err != nil {
			return SearchResult{}, err
		}
		filePath, err := db.lib.takeOwnedString(func() *byte { return db.lib.searchResultGetFilePath(handle, i) })
		if err != nil {
			return SearchResult{}, err
		}
		title, err := db.lib.takeOwnedString(func() *byte { return db.lib.searchResultGetTitle(handle, i) })
		if err != nil {
			return SearchResult{}, err
		}
		titleHighlight, err := db.lib.takeOwnedString(func() *byte { return db.lib.searchResultGetTitleHighlight(handle, i) })
		if err != nil {
			return SearchResult{}, err
		}
		contentSnippet, err := db.lib.takeOwnedString(func() *byte { return db.lib.searchResultGetContentSnippet(handle, i) })
		if err != nil {
			return SearchResult{}, err
		}
		hits = append(hits, SearchHit{
			ID:             id,
			FilePath:       filePath,
			Title:          title,
			TitleHighlight: titleHighlight,
			ContentSnippet: contentSnippet,
			Score:          sanitizeFiniteFloat(db.lib.searchResultGetScore(handle, i)),
			Rank:           db.lib.searchResultGetRank(handle, i),
			RawScore:       sanitizeFiniteFloat(db.lib.searchResultGetRawScore(handle, i)),
		})
	}

	return SearchResult{
		Total:     total,
		Source:    source,
		QueryMode: queryMode,
		Hits:      hits,
	}, nil
}

func (array *ffiValueArray) ptr() *ffiValue {
	if array == nil || len(array.values) == 0 {
		return nil
	}
	return &array.values[0]
}

func (array *ffiValueArray) keepAlive() {
	if array == nil {
		return
	}
	runtime.KeepAlive(array.values)
	for _, value := range array.stringKeepers {
		runtime.KeepAlive(value)
	}
	for _, value := range array.bytesKeepers {
		runtime.KeepAlive(value)
	}
}

func (matrix *ffiValueMatrix) ptr() *ffiValueSlice {
	if matrix == nil || len(matrix.slices) == 0 {
		return nil
	}
	return &matrix.slices[0]
}

func (matrix *ffiValueMatrix) keepAlive() {
	if matrix == nil {
		return
	}
	runtime.KeepAlive(matrix.slices)
	for index := range matrix.rows {
		matrix.rows[index].keepAlive()
	}
}

func buildFFIValues(values []SQLValue) (ffiValueArray, func(), error) {
	if len(values) == 0 {
		return ffiValueArray{}, func() {}, nil
	}
	array := ffiValueArray{values: make([]ffiValue, len(values))}
	for index, value := range values {
		array.values[index].Kind = value.Kind
		switch value.Kind {
		case SQLValueNull:
		case SQLValueInt64:
			array.values[index].Int64Value = value.Int64
		case SQLValueFloat64:
			array.values[index].Float64Value = value.Float64
		case SQLValueString:
			buffer := append([]byte(value.String), 0)
			array.stringKeepers = append(array.stringKeepers, buffer)
			array.values[index].StringValue = &buffer[0]
		case SQLValueBytes:
			if len(value.Bytes) > 0 {
				buffer := append([]byte(nil), value.Bytes...)
				array.bytesKeepers = append(array.bytesKeepers, buffer)
				array.values[index].BytesValue = byteView{Data: &buffer[0], Len: uint64(len(buffer))}
			}
		case SQLValueBool:
			array.values[index].BoolValue = boolToUint8(value.Bool)
		default:
			return ffiValueArray{}, nil, fmt.Errorf("unsupported SQL value kind / 不支持的 SQL 参数类型: %d", value.Kind)
		}
	}
	return array, func() { array.keepAlive() }, nil
}

func buildFFIValueMatrix(items [][]SQLValue) (ffiValueMatrix, func(), error) {
	if len(items) == 0 {
		return ffiValueMatrix{}, func() {}, nil
	}
	matrix := ffiValueMatrix{
		slices: make([]ffiValueSlice, len(items)),
		rows:   make([]ffiValueArray, len(items)),
	}
	for index, item := range items {
		row, _, err := buildFFIValues(item)
		if err != nil {
			return ffiValueMatrix{}, nil, err
		}
		matrix.rows[index] = row
		matrix.slices[index] = ffiValueSlice{
			Values: row.ptr(),
			Len:    uint64(len(row.values)),
		}
	}
	return matrix, func() { matrix.keepAlive() }, nil
}

func nullableCString(value string) (*byte, func()) {
	if value == "" {
		return nil, func() {}
	}
	return makeCString(value)
}

func (lib *Library) readExecuteResult(handle unsafe.Pointer) (ExecuteResult, error) {
	message, err := lib.takeOwnedString(func() *byte {
		return lib.executeResultMessage(handle)
	})
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{
		Success:            lib.executeResultSuccess(handle) != 0,
		Message:            message,
		RowsChanged:        lib.executeResultRowsChanged(handle),
		LastInsertRowID:    lib.executeResultLastInsertRowID(handle),
		StatementsExecuted: lib.executeResultStatementsExec(handle),
	}, nil
}

func (lib *Library) takeOwnedString(getter func() *byte) (string, error) {
	ptr := getter()
	if ptr == nil {
		return "", lib.lastError()
	}
	defer lib.stringFree(ptr)
	return readCString(ptr), nil
}

func (lib *Library) lastError() error {
	if lib == nil {
		return errors.New("library is nil / library 不能为空")
	}
	ptr := lib.lastErrorMessage()
	if ptr == nil {
		return errors.New("ffi call failed without error message / FFI 调用失败但未返回错误消息")
	}
	return errors.New(readCString(ptr))
}

func makeCString(value string) (*byte, func()) {
	buffer := append([]byte(value), 0)
	return &buffer[0], func() {
		runtime.KeepAlive(buffer)
	}
}

func readCString(ptr *byte) string {
	if ptr == nil {
		return ""
	}
	base := uintptr(unsafe.Pointer(ptr))
	length := 0
	for {
		if *(*byte)(unsafe.Pointer(base + uintptr(length))) == 0 {
			break
		}
		length++
	}
	return string(unsafe.Slice(ptr, length))
}

func boolToUint8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func sanitizeFiniteFloat(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
