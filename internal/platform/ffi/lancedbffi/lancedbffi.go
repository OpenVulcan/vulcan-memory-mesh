// lancedbffi.go provides the minimal LanceDB FFI wrapper used by VMM split-mode vector storage.
// lancedbffi.go 用于提供 VMM split 模式向量存储所需的最小 LanceDB FFI 包装层。
package lancedbffi

import (
	"encoding/json"
	"errors"
	"fmt"
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

// InputFormat represents the upsert payload format.
// InputFormat 表示写入载荷格式。
type InputFormat int32

const (
	// InputFormatJSONRows means the payload is a JSON rows array.
	// InputFormatJSONRows 表示载荷为 JSON Rows 数组。
	InputFormatJSONRows InputFormat = 1
)

// OutputFormat represents the search output payload format.
// OutputFormat 表示检索输出载荷格式。
type OutputFormat int32

const (
	// OutputFormatJSONRows means the search result should be returned as JSON rows.
	// OutputFormatJSONRows 表示检索结果以 JSON Rows 形式返回。
	OutputFormatJSONRows OutputFormat = 2
)

// RuntimeOptions represents the runtime creation options.
// RuntimeOptions 表示运行时创建选项。
type RuntimeOptions struct {
	// DefaultDBPath is the default database path resolved by the runtime.
	// DefaultDBPath 表示运行时默认数据库路径。
	DefaultDBPath string
	// DBRoot is the named database root directory.
	// DBRoot 表示命名数据库根目录。
	DBRoot string
	// ReadConsistencyIntervalMS is the optional read-consistency refresh interval.
	// ReadConsistencyIntervalMS 表示可选的读一致性刷新间隔。
	ReadConsistencyIntervalMS uint64
	// HasReadConsistencyInterval reports whether the read-consistency interval is enabled.
	// HasReadConsistencyInterval 表示是否启用读一致性刷新间隔。
	HasReadConsistencyInterval bool
	// MaxUpsertPayload is the maximum accepted upsert payload size.
	// MaxUpsertPayload 表示单次写入的最大载荷大小。
	MaxUpsertPayload uintptr
	// MaxSearchLimit is the maximum accepted search limit.
	// MaxSearchLimit 表示单次检索允许的最大 limit。
	MaxSearchLimit uintptr
	// MaxConcurrentRequests is the maximum number of concurrent requests.
	// MaxConcurrentRequests 表示最大并发请求数。
	MaxConcurrentRequests uintptr
}

// CreateTableColumn describes one create-table column.
// CreateTableColumn 描述一条建表列定义。
type CreateTableColumn struct {
	Name       string `json:"name"`
	ColumnType string `json:"column_type"`
	VectorDim  uint32 `json:"vector_dim,omitempty"`
	Nullable   bool   `json:"nullable"`
}

// CreateTableRequest describes one create-table request.
// CreateTableRequest 描述一条建表请求。
type CreateTableRequest struct {
	TableName         string              `json:"table_name"`
	Columns           []CreateTableColumn `json:"columns"`
	OverwriteIfExists bool                `json:"overwrite_if_exists"`
}

// CreateTableResult describes the JSON compatibility create-table response.
// CreateTableResult 描述 JSON 兼容建表响应。
type CreateTableResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// UpsertResult describes one raw vector upsert result.
// UpsertResult 描述一次原始向量写入结果。
type UpsertResult struct {
	Version      uint64
	InputRows    uint64
	InsertedRows uint64
	UpdatedRows  uint64
	DeletedRows  uint64
}

// SearchResult describes one vector search result payload.
// SearchResult 描述一次向量检索返回载荷。
type SearchResult struct {
	Format OutputFormat
	Rows   uint64
	Data   []byte
}

// DeleteResult describes the JSON compatibility delete response.
// DeleteResult 描述 JSON 兼容删除响应。
type DeleteResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	Version     uint64 `json:"version"`
	DeletedRows uint64 `json:"deleted_rows"`
}

// DropTableResult describes the JSON compatibility drop-table response.
// DropTableResult 描述 JSON 兼容删表响应。
type DropTableResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// DeleteRequest describes one delete request executed through the JSON compatibility endpoint.
// DeleteRequest 描述一条经由 JSON 兼容接口执行的删除请求。
type DeleteRequest struct {
	TableName string `json:"table_name"`
	Condition string `json:"condition"`
}

// DropTableRequest describes one drop-table request executed through the JSON compatibility endpoint.
// DropTableRequest 描述一条经由 JSON 兼容接口执行的删表请求。
type DropTableRequest struct {
	TableName string `json:"table_name"`
}

// Library represents one loaded LanceDB dynamic library.
// Library 表示一个已加载的 LanceDB 动态库。
type Library struct {
	handle uintptr

	runtimeCreate              func(runtimeOptionsPod) unsafe.Pointer
	runtimeCreateProc          uintptr
	runtimeDestroy             func(unsafe.Pointer)
	runtimeOpenDefaultEngine   func(unsafe.Pointer) unsafe.Pointer
	runtimeDatabasePathForName func(unsafe.Pointer, *byte) *byte
	engineCreateTableJSON      func(unsafe.Pointer, *byte) *byte
	engineVectorUpsertRaw      func(unsafe.Pointer, *byte, InputFormat, *byte, uintptr, **byte, uintptr, *upsertResultPod) int32
	engineVectorSearchF32      func(unsafe.Pointer, *byte, *float32, uintptr, uint32, *byte, *byte, OutputFormat, *byteBufferPod, *searchResultMetaPod) int32
	engineDeleteJSON           func(unsafe.Pointer, *byte) *byte
	engineDropTableJSON        func(unsafe.Pointer, *byte) *byte
	engineDestroy              func(unsafe.Pointer)
	bytesFree                  func(byteBufferPod)
	bytesFreeProc              uintptr
	stringFree                 func(*byte)
	lastErrorMessage           func() *byte
	clearLastError             func()
}

// Runtime represents one LanceDB runtime handle.
// Runtime 表示一个 LanceDB 运行时句柄。
type Runtime struct {
	lib    *Library
	handle unsafe.Pointer
}

// Engine represents one LanceDB engine handle.
// Engine 表示一个 LanceDB 引擎句柄。
type Engine struct {
	lib    *Library
	handle unsafe.Pointer
}

type runtimeOptionsPod struct {
	DefaultDBPath              *byte
	DBRoot                     *byte
	ReadConsistencyIntervalMS  uint64
	HasReadConsistencyInterval uint8
	_                          [7]byte
	MaxUpsertPayload           uintptr
	MaxSearchLimit             uintptr
	MaxConcurrentRequests      uintptr
}

type upsertResultPod struct {
	Version      uint64
	InputRows    uint64
	InsertedRows uint64
	UpdatedRows  uint64
	DeletedRows  uint64
}

type searchResultMetaPod struct {
	Format     uint32
	_          [4]byte
	Rows       uint64
	ByteLength uintptr
}

type byteBufferPod struct {
	Data *byte
	Len  uintptr
	Cap  uintptr
}

const (
	// maxCStringReadBytes bounds how far the Go side will scan one FFI-owned C string before treating it as malformed and aborting the read.
	// maxCStringReadBytes 用于限制 Go 侧扫描一条 FFI 持有 C 字符串的最大长度，超出后会判定其为畸形返回并中止读取。
	maxCStringReadBytes = 64 * 1024
)

// Open loads the target dynamic library and binds the required FFI symbols.
// Open 用于加载目标动态库并绑定所需的 FFI 符号。
func Open(path string) (*Library, error) {
	handle, err := openLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("加载 LanceDB 动态库失败 / failed to load LanceDB dynamic library: %w", err)
	}

	lib := &Library{handle: handle}
	bind := func(target any, name string) {
		purego.RegisterLibFunc(target, handle, name)
	}

	if runtime.GOOS == "windows" {
		proc, err := lookupSymbol(handle, "vldb_lancedb_runtime_create")
		if err != nil {
			_ = closeLibrary(handle)
			return nil, fmt.Errorf("解析 LanceDB runtime_create 符号失败 / failed to resolve LanceDB runtime_create symbol: %w", err)
		}
		lib.runtimeCreateProc = proc
	} else {
		bind(&lib.runtimeCreate, "vldb_lancedb_runtime_create")
	}
	bind(&lib.runtimeDestroy, "vldb_lancedb_runtime_destroy")
	bind(&lib.runtimeOpenDefaultEngine, "vldb_lancedb_runtime_open_default_engine")
	bind(&lib.runtimeDatabasePathForName, "vldb_lancedb_runtime_database_path_for_name")
	bind(&lib.engineCreateTableJSON, "vldb_lancedb_engine_create_table_json")
	bind(&lib.engineVectorUpsertRaw, "vldb_lancedb_engine_vector_upsert_raw")
	bind(&lib.engineVectorSearchF32, "vldb_lancedb_engine_vector_search_f32")
	bind(&lib.engineDeleteJSON, "vldb_lancedb_engine_delete_json")
	bind(&lib.engineDropTableJSON, "vldb_lancedb_engine_drop_table_json")
	bind(&lib.engineDestroy, "vldb_lancedb_engine_destroy")
	if runtime.GOOS == "windows" {
		proc, err := lookupSymbol(handle, "vldb_lancedb_bytes_free")
		if err != nil {
			_ = closeLibrary(handle)
			return nil, fmt.Errorf("解析 LanceDB bytes_free 符号失败 / failed to resolve LanceDB bytes_free symbol: %w", err)
		}
		lib.bytesFreeProc = proc
	} else {
		bind(&lib.bytesFree, "vldb_lancedb_bytes_free")
	}
	bind(&lib.stringFree, "vldb_lancedb_string_free")
	bind(&lib.lastErrorMessage, "vldb_lancedb_last_error_message")
	bind(&lib.clearLastError, "vldb_lancedb_clear_last_error")

	return lib, nil
}

// Close releases the loaded dynamic library handle.
// Close 用于释放已加载的动态库句柄。
func (lib *Library) Close() error {
	if lib == nil || lib.handle == 0 {
		return nil
	}
	err := closeLibrary(lib.handle)
	lib.handle = 0
	return err
}

// DefaultRuntimeOptions returns the library-provided runtime option defaults.
// DefaultRuntimeOptions 用于返回库提供的运行时默认选项。
func (lib *Library) DefaultRuntimeOptions() RuntimeOptions {
	return RuntimeOptions{}
}

// CreateRuntime creates one LanceDB runtime from the provided options.
// CreateRuntime 用于基于给定选项创建一个 LanceDB 运行时。
func (lib *Library) CreateRuntime(options RuntimeOptions) (*Runtime, error) {
	defaultPathPtr, keepDefault := makeOptionalCString(options.DefaultDBPath)
	defer keepDefault()
	dbRootPtr, keepRoot := makeOptionalCString(options.DBRoot)
	defer keepRoot()

	handle := lib.callRuntimeCreate(runtimeOptionsPod{
		DefaultDBPath:              defaultPathPtr,
		DBRoot:                     dbRootPtr,
		ReadConsistencyIntervalMS:  options.ReadConsistencyIntervalMS,
		HasReadConsistencyInterval: boolToUint8(options.HasReadConsistencyInterval),
		MaxUpsertPayload:           options.MaxUpsertPayload,
		MaxSearchLimit:             options.MaxSearchLimit,
		MaxConcurrentRequests:      options.MaxConcurrentRequests,
	})
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

// OpenDefaultEngine opens the runtime default engine.
// OpenDefaultEngine 用于打开运行时默认引擎。
func (rt *Runtime) OpenDefaultEngine() (*Engine, error) {
	handle := rt.lib.runtimeOpenDefaultEngine(rt.handle)
	if handle == nil {
		return nil, rt.lib.lastError()
	}
	engine := &Engine{lib: rt.lib, handle: handle}
	runtime.SetFinalizer(engine, func(value *Engine) {
		_ = value.Close()
	})
	return engine, nil
}

// DatabasePathForName resolves the physical database path for the provided logical name.
// DatabasePathForName 用于解析指定逻辑名称对应的物理数据库路径。
func (rt *Runtime) DatabasePathForName(name string) (string, error) {
	namePtr, keepName := makeOptionalCString(name)
	defer keepName()
	return rt.lib.takeOwnedString(func() *byte {
		return rt.lib.runtimeDatabasePathForName(rt.handle, namePtr)
	})
}

// Close destroys the engine handle.
// Close 用于销毁引擎句柄。
func (engine *Engine) Close() error {
	if engine == nil || engine.handle == nil {
		return nil
	}
	engine.lib.engineDestroy(engine.handle)
	engine.handle = nil
	runtime.SetFinalizer(engine, nil)
	return nil
}

// CreateTable executes one create-table request through the JSON compatibility interface.
// CreateTable 用于经由 JSON 兼容接口执行一次建表请求。
func (engine *Engine) CreateTable(request CreateTableRequest) (CreateTableResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return CreateTableResult{}, fmt.Errorf("序列化 LanceDB 建表请求失败 / failed to marshal LanceDB create-table request: %w", err)
	}
	ptr, keep := makeCStringBytes(payload)
	defer keep()

	raw, err := engine.lib.takeOwnedString(func() *byte {
		return engine.lib.engineCreateTableJSON(engine.handle, ptr)
	})
	if err != nil {
		return CreateTableResult{}, err
	}
	var result CreateTableResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return CreateTableResult{}, fmt.Errorf("解析 LanceDB 建表响应失败 / failed to unmarshal LanceDB create-table response: %w", err)
	}
	return result, nil
}

// VectorUpsertRaw writes JSON rows into the target table with the provided key columns.
// VectorUpsertRaw 用于使用给定主键列把 JSON rows 写入目标表。
func (engine *Engine) VectorUpsertRaw(tableName string, format InputFormat, data []byte, keyColumns []string) (UpsertResult, error) {
	tablePtr, keepTable := makeCString(tableName)
	defer keepTable()
	keyPtrs, keepKeys := makeCStringArray(keyColumns)
	defer keepKeys()

	var dataPtr *byte
	var dataLen uintptr
	if len(data) > 0 {
		dataPtr = &data[0]
		dataLen = uintptr(len(data))
	}

	var pod upsertResultPod
	status := StatusCode(engine.lib.engineVectorUpsertRaw(
		engine.handle,
		tablePtr,
		format,
		dataPtr,
		dataLen,
		keyPtrs,
		uintptr(len(keyColumns)),
		&pod,
	))
	if status != StatusSuccess {
		return UpsertResult{}, engine.lib.lastError()
	}
	return UpsertResult{
		Version:      pod.Version,
		InputRows:    pod.InputRows,
		InsertedRows: pod.InsertedRows,
		UpdatedRows:  pod.UpdatedRows,
		DeletedRows:  pod.DeletedRows,
	}, nil
}

// VectorSearchF32 executes one float32 vector search and returns the raw output payload.
// VectorSearchF32 用于执行一次 float32 向量检索并返回原始输出载荷。
func (engine *Engine) VectorSearchF32(tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat OutputFormat) (SearchResult, error) {
	if len(vector) == 0 {
		return SearchResult{}, errors.New("vector must not be empty / 向量不能为空")
	}
	tablePtr, keepTable := makeCString(tableName)
	defer keepTable()
	filterPtr, keepFilter := makeOptionalCString(filter)
	defer keepFilter()
	columnPtr, keepColumn := makeOptionalCString(vectorColumn)
	defer keepColumn()

	var buffer byteBufferPod
	var meta searchResultMetaPod
	status := StatusCode(engine.lib.engineVectorSearchF32(
		engine.handle,
		tablePtr,
		&vector[0],
		uintptr(len(vector)),
		limit,
		filterPtr,
		columnPtr,
		outputFormat,
		&buffer,
		&meta,
	))
	if status != StatusSuccess {
		return SearchResult{}, engine.lib.lastError()
	}
	defer engine.lib.freeBytes(buffer)

	payload := []byte{}
	if buffer.Data != nil && buffer.Len > 0 {
		payload = append(payload, unsafe.Slice(buffer.Data, int(buffer.Len))...)
	}
	return SearchResult{
		Format: OutputFormat(meta.Format),
		Rows:   meta.Rows,
		Data:   payload,
	}, nil
}

// Delete executes one delete request through the JSON compatibility endpoint.
// Delete 用于通过 JSON 兼容接口执行一次删除请求。
func (engine *Engine) Delete(request DeleteRequest) (DeleteResult, error) {
	raw, err := engine.callJSON(func(payload *byte) *byte {
		return engine.lib.engineDeleteJSON(engine.handle, payload)
	}, request)
	if err != nil {
		return DeleteResult{}, err
	}
	var result DeleteResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return DeleteResult{}, fmt.Errorf("解析 LanceDB 删除响应失败 / failed to unmarshal LanceDB delete response: %w", err)
	}
	return result, nil
}

// DropTable executes one drop-table request through the JSON compatibility endpoint.
// DropTable 用于通过 JSON 兼容接口执行一次删表请求。
func (engine *Engine) DropTable(request DropTableRequest) (DropTableResult, error) {
	raw, err := engine.callJSON(func(payload *byte) *byte {
		return engine.lib.engineDropTableJSON(engine.handle, payload)
	}, request)
	if err != nil {
		return DropTableResult{}, err
	}
	var result DropTableResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return DropTableResult{}, fmt.Errorf("解析 LanceDB 删表响应失败 / failed to unmarshal LanceDB drop-table response: %w", err)
	}
	return result, nil
}

// callJSON marshals one JSON compatibility request and returns the owned response string.
// callJSON 用于序列化一条 JSON 兼容请求，并返回拥有所有权的响应字符串。
func (engine *Engine) callJSON(getter func(*byte) *byte, request any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("序列化 LanceDB JSON 请求失败 / failed to marshal LanceDB JSON request: %w", err)
	}
	ptr, keep := makeCStringBytes(payload)
	defer keep()
	return engine.lib.takeOwnedString(func() *byte {
		return getter(ptr)
	})
}

// takeOwnedString calls one FFI getter that returns an owned C string and frees it automatically.
// takeOwnedString 用于调用返回拥有所有权 C 字符串的 FFI getter，并自动释放返回值。
func (lib *Library) takeOwnedString(getter func() *byte) (string, error) {
	ptr := getter()
	if ptr == nil {
		return "", lib.lastError()
	}
	defer lib.stringFree(ptr)
	return readCString(ptr)
}

// lastError reads the latest FFI error message from the dynamic library.
// lastError 用于从动态库读取最近一次 FFI 错误消息。
func (lib *Library) lastError() error {
	if lib == nil {
		return errors.New("library is nil / library 不能为空")
	}
	ptr := lib.lastErrorMessage()
	if ptr == nil {
		return errors.New("ffi call failed without error message / FFI 调用失败但未返回错误消息")
	}
	message, err := readCString(ptr)
	if err != nil {
		return fmt.Errorf("ffi returned malformed error message / FFI 返回了畸形错误消息: %w", err)
	}
	return errors.New(message)
}

// makeCString creates a temporary C-style string buffer and returns a keep-alive closure.
// makeCString 用于创建临时 C 风格字符串缓冲区，并返回一个保活闭包。
func makeCString(value string) (*byte, func()) {
	buffer := append([]byte(value), 0)
	return &buffer[0], func() {
		runtime.KeepAlive(buffer)
	}
}

// makeCStringBytes creates a temporary C-style string buffer from UTF-8 bytes.
// makeCStringBytes 用于从 UTF-8 字节创建临时 C 风格字符串缓冲区。
func makeCStringBytes(value []byte) (*byte, func()) {
	buffer := append(append([]byte(nil), value...), 0)
	return &buffer[0], func() {
		runtime.KeepAlive(buffer)
	}
}

// makeOptionalCString creates one optional C-style string buffer.
// makeOptionalCString 用于创建一个可选的 C 风格字符串缓冲区。
func makeOptionalCString(value string) (*byte, func()) {
	if value == "" {
		return nil, func() {}
	}
	return makeCString(value)
}

// makeCStringArray creates one C string pointer array used by key-column FFI calls.
// makeCStringArray 用于创建供主键列 FFI 调用使用的 C 字符串指针数组。
func makeCStringArray(values []string) (**byte, func()) {
	if len(values) == 0 {
		return nil, func() {}
	}
	buffers := make([][]byte, 0, len(values))
	pointers := make([]*byte, 0, len(values))
	for _, value := range values {
		buffer := append([]byte(value), 0)
		buffers = append(buffers, buffer)
		pointers = append(pointers, &buffer[0])
	}
	return &pointers[0], func() {
		runtime.KeepAlive(buffers)
		runtime.KeepAlive(pointers)
	}
}

// readCString reads one NUL-terminated string from the provided pointer but refuses to scan beyond one fixed audit-friendly limit when the FFI side returns malformed data.
// readCString 用于从给定指针读取一条 NUL 结尾字符串；若 FFI 侧返回畸形数据，则会在固定且可审计的上界内停止扫描。
func readCString(ptr *byte) (string, error) {
	if ptr == nil {
		return "", nil
	}
	bytes := unsafe.Slice(ptr, maxCStringReadBytes)
	for idx, value := range bytes {
		if value == 0 {
			return string(bytes[:idx]), nil
		}
	}
	return "", fmt.Errorf("ffi string exceeds %d bytes or is not NUL-terminated", maxCStringReadBytes)
}

// boolToUint8 converts one boolean into the 0/1 form expected by the FFI ABI.
// boolToUint8 用于把布尔值转换为 FFI ABI 期望的 0/1 形式。
func boolToUint8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}
