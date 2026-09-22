// nativelance.go binds the fixed C ABI exported by the official Rust LanceDB cdylib.
// nativelance.go 绑定官方 Rust LanceDB cdylib 导出的固定 C ABI。
package nativelance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/openvulcan/vmm/internal/platform/storagecontract/lance"
)

const (
	// ABIStatusOK is the native success status.
	// ABIStatusOK 表示原生调用成功。
	ABIStatusOK int32 = 0
	// ABIStatusCancelled is the native cancellation status.
	// ABIStatusCancelled 表示原生调用被取消。
	ABIStatusCancelled int32 = 4
	// ABIStatusTimeout is the native read timeout status.
	// ABIStatusTimeout 表示原生读取超时。
	ABIStatusTimeout int32 = 5
	// ABIStatusOutcomeUncertain is the native mutation uncertainty status.
	// ABIStatusOutcomeUncertain 表示原生变更提交结果不确定。
	ABIStatusOutcomeUncertain int32 = 7
)

// ABIError preserves the native status code and diagnostic text across the Go boundary.
// ABIError 在 Go 边界保留原生状态码与诊断文本。
type ABIError struct {
	// Code is the stable Rust status code.
	// Code 是稳定的 Rust 状态码。
	Code int32
	// Message is the library-owned diagnostic copied into Go memory.
	// Message 是复制到 Go 内存的库诊断文本。
	Message string
}

// Error returns the native diagnostic text with its status code.
// Error 返回带状态码的原生诊断文本。
func (e ABIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("native LanceDB ABI status %d", e.Code)
	}
	return fmt.Sprintf("native LanceDB ABI status %d: %s", e.Code, e.Message)
}

// OutcomeUncertainError marks a mutation whose commit result cannot be confirmed after timeout.
// OutcomeUncertainError 标记超时后无法确认提交结果的变更操作。
type OutcomeUncertainError struct {
	// Message explains the native uncertainty boundary.
	// Message 说明原生不确定性边界。
	Message string
}

// Error returns the mutation uncertainty diagnostic.
// Error 返回变更结果不确定诊断。
func (e OutcomeUncertainError) Error() string {
	if e.Message == "" {
		return "native LanceDB mutation outcome is uncertain"
	}
	return "native LanceDB mutation outcome is uncertain: " + e.Message
}

// IsOutcomeUncertain reports whether an error crossed the native mutation uncertainty boundary.
// IsOutcomeUncertain 报告错误是否越过原生变更结果不确定边界。
func IsOutcomeUncertain(err error) bool {
	var uncertain OutcomeUncertainError
	return errors.As(err, &uncertain)
}

// Bytes is the ownership descriptor returned by the Rust library.
// Bytes 是 Rust 库返回的内存所有权描述符。
type bytesPod struct {
	Data *byte
	Len  uint64
	Cap  uint64
}

// nativeLibraryCache keeps one validated dynamic-library load per canonical path for process lifetime.
// nativeLibraryCache 按规范路径缓存每个动态库的一次有效加载，并保持到进程退出。
var nativeLibraryCache = struct {
	sync.Mutex
	handles map[string]uintptr
}{
	handles: make(map[string]uintptr),
}

// Library represents one dynamically loaded native LanceDB library.
// Library 表示一个动态加载的原生 LanceDB 库。
type Library struct {
	handle uintptr

	abiVersion       func() uint32
	engineVersion    func(*bytesPod) int32
	capabilities     func(*bytesPod) int32
	runtimeCreate    func(*byte, uint64, uint64, *unsafe.Pointer) int32
	runtimeDestroy   func(unsafe.Pointer) int32
	createTable      func(unsafe.Pointer, *byte, uint64, *byte, uint64, uint8, uint64, *bytesPod) int32
	upsert           func(unsafe.Pointer, *byte, uint64, uint32, *byte, uint64, *byte, uint64, uint64, *bytesPod) int32
	search           func(unsafe.Pointer, *byte, uint64, *float32, uint64, uint32, *byte, uint64, *byte, uint64, uint64, *bytesPod) int32
	delete           func(unsafe.Pointer, *byte, uint64, *byte, uint64, uint64, *bytesPod) int32
	countRows        func(unsafe.Pointer, *byte, uint64, *byte, uint64, uint64, *uint64) int32
	schema           func(unsafe.Pointer, *byte, uint64, uint64, *bytesPod) int32
	optimize         func(unsafe.Pointer, *byte, uint64, uint64) int32
	dropTable        func(unsafe.Pointer, *byte, uint64, uint64, *bytesPod) int32
	bytesFree        func(*byte, uint64, uint64)
	bytesFreeProc    uintptr
	lastErrorMessage func(*bytesPod) int32
	clearLastError   func()
}

// Runtime owns one Rust executor and one official LanceDB local connection.
// Runtime 持有一个 Rust 执行器与一个官方 LanceDB 本地连接。
type Runtime struct {
	mu     sync.RWMutex
	lib    *Library
	handle unsafe.Pointer
}

// Engine implements the neutral context-aware LanceDB contract on one runtime.
// Engine 在一个运行时上实现带 context 的中立 LanceDB 契约。
type Engine struct {
	mu      sync.RWMutex
	runtime *Runtime
}

// Open loads one native library and validates the exported ABI version.
// Open 加载一个原生库并校验导出的 ABI 版本。
func Open(path string) (*Library, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("native LanceDB library path is required")
	}
	cacheKey := canonicalLibraryPath(path)
	nativeLibraryCache.Lock()
	defer nativeLibraryCache.Unlock()
	handle, cached := nativeLibraryCache.handles[cacheKey]
	if !cached {
		var err error
		handle, err = openLibrary(path)
		if err != nil {
			return nil, fmt.Errorf("load native LanceDB library %q: %w", path, err)
		}
		nativeLibraryCache.handles[cacheKey] = handle
	}
	discardInvalidLoad := func() {
		if cached {
			return
		}
		delete(nativeLibraryCache.handles, cacheKey)
		_ = closeLibrary(handle)
	}
	lib := &Library{handle: handle}
	requiredSymbols := []string{
		"vmm_lancedb_abi_version",
		"vmm_lancedb_engine_version",
		"vmm_lancedb_capabilities",
		"vmm_lancedb_runtime_create",
		"vmm_lancedb_runtime_destroy",
		"vmm_lancedb_create_table",
		"vmm_lancedb_upsert",
		"vmm_lancedb_search",
		"vmm_lancedb_delete",
		"vmm_lancedb_count_rows",
		"vmm_lancedb_schema",
		"vmm_lancedb_optimize",
		"vmm_lancedb_drop_table",
		"vmm_lancedb_bytes_free",
		"vmm_lancedb_string_free",
		"vmm_lancedb_last_error_message",
		"vmm_lancedb_clear_last_error",
	}
	for _, symbol := range requiredSymbols {
		if _, err := lookupSymbol(handle, symbol); err != nil {
			discardInvalidLoad()
			return nil, fmt.Errorf("resolve native LanceDB symbol %q: %w", symbol, err)
		}
	}
	bind := func(target any, name string) {
		purego.RegisterLibFunc(target, handle, name)
	}
	bind(&lib.abiVersion, "vmm_lancedb_abi_version")
	bind(&lib.engineVersion, "vmm_lancedb_engine_version")
	bind(&lib.capabilities, "vmm_lancedb_capabilities")
	bind(&lib.runtimeCreate, "vmm_lancedb_runtime_create")
	bind(&lib.runtimeDestroy, "vmm_lancedb_runtime_destroy")
	bind(&lib.createTable, "vmm_lancedb_create_table")
	bind(&lib.upsert, "vmm_lancedb_upsert")
	bind(&lib.search, "vmm_lancedb_search")
	bind(&lib.delete, "vmm_lancedb_delete")
	bind(&lib.countRows, "vmm_lancedb_count_rows")
	bind(&lib.schema, "vmm_lancedb_schema")
	bind(&lib.optimize, "vmm_lancedb_optimize")
	bind(&lib.dropTable, "vmm_lancedb_drop_table")
	bind(&lib.lastErrorMessage, "vmm_lancedb_last_error_message")
	bind(&lib.clearLastError, "vmm_lancedb_clear_last_error")
	if runtime.GOOS == "windows" {
		proc, err := lookupSymbol(handle, "vmm_lancedb_bytes_free")
		if err != nil {
			discardInvalidLoad()
			return nil, fmt.Errorf("resolve native bytes_free: %w", err)
		}
		lib.bytesFreeProc = proc
	} else {
		bind(&lib.bytesFree, "vmm_lancedb_bytes_free")
	}
	if got := lib.abiVersion(); got != 1 {
		discardInvalidLoad()
		return nil, fmt.Errorf("unsupported native LanceDB ABI version %d", got)
	}
	// Validate semantic compatibility before creating a database or accepting a cached load.
	// 在创建数据库或接受缓存加载前校验语义兼容性。
	version, err := lib.EngineVersion()
	if err != nil {
		discardInvalidLoad()
		return nil, fmt.Errorf("read native LanceDB engine version: %w", err)
	}
	capabilities, err := lib.Capabilities()
	if err != nil {
		discardInvalidLoad()
		return nil, fmt.Errorf("read native LanceDB capabilities: %w", err)
	}
	if err := validateNativeCapabilities(version, capabilities); err != nil {
		discardInvalidLoad()
		return nil, err
	}
	return lib, nil
}

// canonicalLibraryPath normalizes one dynamic-library path for process-local load deduplication.
// canonicalLibraryPath 规范化动态库路径，用于进程内去重加载。
func canonicalLibraryPath(path string) string {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = filepath.Clean(absPath)
	} else {
		path = filepath.Clean(path)
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// Close releases logical ownership without unloading the process-resident DLL; Runtime.Close releases database resources.
// Close 结束逻辑所有权但不卸载进程驻留 DLL；数据库资源由 Runtime.Close 释放。
func (lib *Library) Close() error {
	return nil
}

// EngineVersion returns the exact official engine version compiled into the DLL.
// EngineVersion 返回 DLL 编译时绑定的官方引擎精确版本。
func (lib *Library) EngineVersion() (string, error) {
	data, err := lib.callBytes(lib.engineVersion)
	return string(data), err
}

// Capabilities returns the native capability document as JSON bytes.
// Capabilities 返回原生能力文档 JSON 字节。
func (lib *Library) Capabilities() ([]byte, error) {
	return lib.callBytes(lib.capabilities)
}

// CreateRuntime opens one native database directory and keeps its Rust executor resident.
// CreateRuntime 打开一个原生数据库目录并保持 Rust 执行器驻留。
func (lib *Library) CreateRuntime(databasePath string) (*Runtime, error) {
	return lib.createRuntime(databasePath, 0)
}

// CreateRuntimeWithTimeout opens one native database directory with a startup deadline in addition to operation deadlines.
// CreateRuntimeWithTimeout 在操作超时之外，为打开一个原生数据库目录应用启动截止时间。
func (lib *Library) CreateRuntimeWithTimeout(databasePath string, timeout time.Duration) (*Runtime, error) {
	return lib.createRuntime(databasePath, durationMilliseconds(timeout))
}

// createRuntime performs the fixed-width runtime_create call while preserving thread-local diagnostics.
// createRuntime 在线程局部诊断仍有效的范围内执行固定宽度 runtime_create 调用。
func (lib *Library) createRuntime(databasePath string, timeoutMS uint64) (*Runtime, error) {
	if lib == nil || lib.handle == 0 {
		return nil, fmt.Errorf("native LanceDB library is closed")
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, fmt.Errorf("native LanceDB database path is required")
	}
	pathBytes := []byte(databasePath)
	var pathPtr *byte
	if len(pathBytes) > 0 {
		pathPtr = &pathBytes[0]
	}
	var handle unsafe.Pointer
	runtime.LockOSThread()
	status := lib.runtimeCreate(pathPtr, uint64(len(pathBytes)), timeoutMS, &handle)
	runtime.KeepAlive(pathBytes)
	err := lib.statusError(status)
	runtime.UnlockOSThread()
	if err != nil {
		return nil, fmt.Errorf("create native LanceDB runtime: %w", err)
	}
	if handle == nil {
		return nil, fmt.Errorf("create native LanceDB runtime returned a nil handle")
	}
	return &Runtime{lib: lib, handle: handle}, nil
}

// OpenDefaultEngine returns a neutral engine bound to the runtime connection.
// OpenDefaultEngine 返回绑定到运行时连接的中立引擎。
func (rt *Runtime) OpenDefaultEngine() (*Engine, error) {
	if rt == nil {
		return nil, fmt.Errorf("native LanceDB runtime is closed")
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.handle == nil {
		return nil, fmt.Errorf("native LanceDB runtime is closed")
	}
	return &Engine{runtime: rt}, nil
}

// Close destroys the Rust runtime while preserving the library handle for later release.
// Close 销毁 Rust 运行时，同时保留动态库句柄供随后释放。
func (rt *Runtime) Close() error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.handle == nil {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status := rt.lib.runtimeDestroy(rt.handle)
	err := rt.lib.statusError(status)
	if err != nil {
		return err
	}
	rt.handle = nil
	return nil
}

// Close detaches an engine wrapper; the owning Runtime controls the Rust handle lifetime.
// Close 解除引擎包装；Rust 句柄生命周期由所属 Runtime 控制。
func (engine *Engine) Close() error {
	if engine == nil {
		return nil
	}
	engine.mu.Lock()
	engine.runtime = nil
	engine.mu.Unlock()
	return nil
}

// CreateTable ensures one table schema through the native marker and Arrow schema checks.
// CreateTable 通过原生标记与 Arrow Schema 检查确保一个表存在。
func (engine *Engine) CreateTable(ctx context.Context, request lance.CreateTableRequest) (lance.CreateTableResult, error) {
	var result lance.CreateTableResult
	payload, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("marshal native create-table request: %w", err)
	}
	data, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		name := []byte(request.TableName)
		var namePtr *byte
		if len(name) > 0 {
			namePtr = &name[0]
		}
		schema := payload
		var schemaPtr *byte
		if len(schema) > 0 {
			schemaPtr = &schema[0]
		}
		status := engine.runtime.lib.createTable(handle, namePtr, uint64(len(name)), schemaPtr, uint64(len(schema)), boolByte(request.OverwriteIfExists), timeout, out)
		runtime.KeepAlive(name)
		runtime.KeepAlive(schema)
		_ = timeout
		return status
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode native create-table result: %w", err)
	}
	return result, nil
}

// VectorUpsertRaw performs an atomic official merge-insert keyed by the supplied columns.
// VectorUpsertRaw 使用给定键列执行官方原子 merge-insert。
func (engine *Engine) VectorUpsertRaw(ctx context.Context, tableName string, format lance.InputFormat, data []byte, keyColumns []string) (lance.UpsertResult, error) {
	var result lance.UpsertResult
	if format != lance.InputFormatJSONRows {
		return result, fmt.Errorf("unsupported native input format %d", format)
	}
	keys, err := json.Marshal(keyColumns)
	if err != nil {
		return result, fmt.Errorf("marshal native key columns: %w", err)
	}
	output, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		name := []byte(tableName)
		var namePtr *byte
		if len(name) > 0 {
			namePtr = &name[0]
		}
		var dataPtr *byte
		if len(data) > 0 {
			dataPtr = &data[0]
		}
		var keyPtr *byte
		if len(keys) > 0 {
			keyPtr = &keys[0]
		}
		status := engine.runtime.lib.upsert(handle, namePtr, uint64(len(name)), uint32(format), dataPtr, uint64(len(data)), keyPtr, uint64(len(keys)), timeout, out)
		runtime.KeepAlive(name)
		runtime.KeepAlive(data)
		runtime.KeepAlive(keys)
		return status
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return result, fmt.Errorf("decode native upsert result: %w", err)
	}
	return result, nil
}

// VectorSearchF32 performs explicit L2 vector search with engine-side prefiltering.
// VectorSearchF32 执行显式 L2 向量检索与引擎侧预过滤。
func (engine *Engine) VectorSearchF32(ctx context.Context, tableName string, vector []float32, limit uint32, filter string, vectorColumn string, outputFormat lance.OutputFormat) (lance.SearchResult, error) {
	result := lance.SearchResult{Format: outputFormat}
	if outputFormat != lance.OutputFormatJSONRows {
		return result, fmt.Errorf("unsupported native output format %d", outputFormat)
	}
	if len(vector) == 0 {
		return result, fmt.Errorf("native search vector must not be empty")
	}
	output, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		name := []byte(tableName)
		filterBytes := []byte(filter)
		column := []byte(vectorColumn)
		var namePtr, filterPtr, columnPtr *byte
		if len(name) > 0 {
			namePtr = &name[0]
		}
		if len(filterBytes) > 0 {
			filterPtr = &filterBytes[0]
		}
		if len(column) > 0 {
			columnPtr = &column[0]
		}
		status := engine.runtime.lib.search(handle, namePtr, uint64(len(name)), &vector[0], uint64(len(vector)), limit, filterPtr, uint64(len(filterBytes)), columnPtr, uint64(len(column)), timeout, out)
		runtime.KeepAlive(name)
		runtime.KeepAlive(filterBytes)
		runtime.KeepAlive(column)
		runtime.KeepAlive(vector)
		return status
	})
	if err != nil {
		return result, err
	}
	result.Data = output
	var rows []json.RawMessage
	if err := json.Unmarshal(output, &rows); err != nil {
		return result, fmt.Errorf("decode native search rows: %w", err)
	}
	result.Rows = uint64(len(rows))
	return result, nil
}

// Delete removes rows matching one engine predicate.
// Delete 删除匹配引擎谓词的行。
func (engine *Engine) Delete(ctx context.Context, request lance.DeleteRequest) (lance.DeleteResult, error) {
	var result lance.DeleteResult
	output, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		tableName := []byte(request.TableName)
		condition := []byte(request.Condition)
		return engine.runtime.lib.delete(handle, bytesPointer(tableName), uint64(len(tableName)), bytesPointer(condition), uint64(len(condition)), timeout, out)
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return result, fmt.Errorf("decode native delete result: %w", err)
	}
	return result, nil
}

// DropTable explicitly removes one table through the native engine.
// DropTable 通过原生引擎显式删除一个表。
func (engine *Engine) DropTable(ctx context.Context, request lance.DropTableRequest) (lance.DropTableResult, error) {
	var result lance.DropTableResult
	name := []byte(request.TableName)
	output, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		return engine.runtime.lib.dropTable(handle, bytesPointer(name), uint64(len(name)), timeout, out)
	})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return result, fmt.Errorf("decode native drop-table result: %w", err)
	}
	return result, nil
}

// CheckHealth verifies that the runtime and native connection remain open.
// CheckHealth 校验运行时与原生连接仍保持打开。
func (engine *Engine) CheckHealth(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	_, _, unlock, err := engine.lockForCall()
	if err != nil {
		return err
	}
	unlock()
	return nil
}

// CountRows counts rows after applying one optional engine predicate.
// CountRows 在应用可选引擎谓词后统计行数。
func (engine *Engine) CountRows(ctx context.Context, tableName, filter string) (uint64, error) {
	var count uint64
	err := engine.callScalar(ctx, func(handle unsafe.Pointer, timeout uint64) int32 {
		name := []byte(tableName)
		predicate := []byte(filter)
		return engine.runtime.lib.countRows(handle, bytesPointer(name), uint64(len(name)), bytesPointer(predicate), uint64(len(predicate)), timeout, &count)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Schema returns the canonical Arrow schema JSON for one table.
// Schema 返回一个表的规范 Arrow Schema JSON。
func (engine *Engine) Schema(ctx context.Context, tableName string) ([]byte, error) {
	output, err := engine.callJSON(ctx, func(handle unsafe.Pointer, out *bytesPod, timeout uint64) int32 {
		name := []byte(tableName)
		return engine.runtime.lib.schema(handle, bytesPointer(name), uint64(len(name)), timeout, out)
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

// Optimize performs official LanceDB maintenance for one table.
// Optimize 执行一个表的官方 LanceDB 维护操作。
func (engine *Engine) Optimize(ctx context.Context, tableName string) error {
	err := engine.callScalar(ctx, func(handle unsafe.Pointer, timeout uint64) int32 {
		name := []byte(tableName)
		return engine.runtime.lib.optimize(handle, bytesPointer(name), uint64(len(name)), timeout)
	})
	return err
}

// callJSON invokes one byte-buffer-returning native operation with a context-derived deadline.
// callJSON 使用从 context 派生的截止时间调用一个返回字节缓冲区的原生操作。
func (engine *Engine) callJSON(ctx context.Context, call func(unsafe.Pointer, *bytesPod, uint64) int32) ([]byte, error) {
	timeout, err := contextTimeout(ctx)
	if err != nil {
		return nil, err
	}
	rt, handle, unlock, err := engine.lockForCall()
	if err != nil {
		return nil, err
	}
	defer unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var output bytesPod
	status := call(handle, &output, timeout)
	data := rt.lib.copyBytes(output)
	if err := rt.lib.statusError(status); err != nil {
		return data, err
	}
	return data, nil
}

// callScalar invokes one scalar native operation with a context-derived deadline.
// callScalar 使用从 context 派生的截止时间调用一个返回标量的原生操作。
func (engine *Engine) callScalar(ctx context.Context, call func(unsafe.Pointer, uint64) int32) error {
	timeout, err := contextTimeout(ctx)
	if err != nil {
		return err
	}
	rt, handle, unlock, err := engine.lockForCall()
	if err != nil {
		return err
	}
	defer unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status := call(handle, timeout)
	return rt.lib.statusError(status)
}

// lockForCall holds both engine and runtime read locks until one synchronous native call has copied its result and read TLS diagnostics.
// lockForCall 持有引擎与运行时读锁，直到同步原生调用复制结果并读取 TLS 诊断完成。
func (engine *Engine) lockForCall() (*Runtime, unsafe.Pointer, func(), error) {
	if engine == nil {
		return nil, nil, func() {}, fmt.Errorf("native LanceDB engine is closed")
	}
	engine.mu.RLock()
	rt := engine.runtime
	if rt == nil {
		engine.mu.RUnlock()
		return nil, nil, func() {}, fmt.Errorf("native LanceDB engine is closed")
	}
	rt.mu.RLock()
	if rt.handle == nil || rt.lib == nil || rt.lib.handle == 0 {
		rt.mu.RUnlock()
		engine.mu.RUnlock()
		return nil, nil, func() {}, fmt.Errorf("native LanceDB runtime is closed")
	}
	return rt, rt.handle, func() {
		rt.mu.RUnlock()
		engine.mu.RUnlock()
	}, nil
}

// callBytes executes one status-returning operation that writes an owned byte buffer.
// callBytes 执行一个写入库拥有字节缓冲区的状态返回操作。
func (lib *Library) callBytes(call func(*bytesPod) int32) ([]byte, error) {
	if lib == nil || lib.handle == 0 {
		return nil, fmt.Errorf("native LanceDB library is closed")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var output bytesPod
	status := call(&output)
	data := lib.copyBytes(output)
	if err := lib.statusError(status); err != nil {
		return nil, err
	}
	return data, nil
}

// copyBytes copies one Rust-owned allocation before releasing it in the same library.
// copyBytes 在同一动态库内释放前复制一份 Rust 所有的分配。
func (lib *Library) copyBytes(output bytesPod) []byte {
	if output.Data == nil || output.Len == 0 {
		lib.freeBytes(output)
		return nil
	}
	if output.Len > uint64(int(^uint(0)>>1)) {
		lib.freeBytes(output)
		return nil
	}
	data := append([]byte(nil), unsafe.Slice(output.Data, int(output.Len))...)
	lib.freeBytes(output)
	return data
}

// freeBytes releases one buffer through the exact library that allocated it.
// freeBytes 通过分配该缓冲区的同一动态库释放它。
func (lib *Library) freeBytes(output bytesPod) {
	if lib == nil || output.Data == nil {
		return
	}
	if runtime.GOOS == "windows" {
		lib.callWindowsBytesFree(output)
		return
	}
	lib.bytesFree(output.Data, output.Len, output.Cap)
}

// statusError translates one native status and its thread-local diagnostic.
// statusError 将原生状态及其线程局部诊断转换为 Go 错误。
func (lib *Library) statusError(status int32) error {
	if status == ABIStatusOK {
		return nil
	}
	message := lib.lastErrorText()
	switch status {
	case ABIStatusCancelled:
		return context.Canceled
	case ABIStatusTimeout:
		return context.DeadlineExceeded
	case ABIStatusOutcomeUncertain:
		return OutcomeUncertainError{Message: message}
	default:
		return ABIError{Code: status, Message: message}
	}
}

// lastErrorText copies and frees the native thread-local error message.
// lastErrorText 复制并释放原生线程局部错误文本。
func (lib *Library) lastErrorText() string {
	if lib == nil || lib.lastErrorMessage == nil {
		return ""
	}
	var output bytesPod
	status := lib.lastErrorMessage(&output)
	data := lib.copyBytes(output)
	if status != ABIStatusOK {
		return ""
	}
	return string(data)
}

// contextError checks cancellation before entering a synchronous native call.
// contextError 在进入同步原生调用前检查取消状态。
func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// contextTimeout converts a context deadline to the native millisecond timeout.
// contextTimeout 将 context 截止时间转换为原生毫秒超时。
func contextTimeout(ctx context.Context) (uint64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	if ctx == nil {
		return 0, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, context.DeadlineExceeded
	}
	milliseconds := uint64(remaining / time.Millisecond)
	if milliseconds == 0 {
		milliseconds = 1
	}
	return milliseconds, nil
}

// durationMilliseconds converts an optional Go duration into the fixed-width native timeout unit.
// durationMilliseconds 将可选 Go 时长转换为原生固定宽度超时单位。
func durationMilliseconds(timeout time.Duration) uint64 {
	if timeout <= 0 {
		return 0
	}
	milliseconds := uint64(timeout / time.Millisecond)
	if milliseconds == 0 {
		return 1
	}
	return milliseconds
}

// contextStatus maps a pre-call context error to the stable native status set.
// contextStatus 将调用前 context 错误映射为稳定原生状态集合。
func contextStatus(err error) int32 {
	if errors.Is(err, context.DeadlineExceeded) {
		return ABIStatusTimeout
	}
	return ABIStatusCancelled
}

// boolByte converts a Go boolean into the C ABI byte representation.
// boolByte 将 Go 布尔值转换为 C ABI 字节表示。
func boolByte(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

// bytesPointer returns a nullable pointer to a byte slice without adding a terminator.
// bytesPointer 返回字节切片的可空指针，不额外添加终止符。
func bytesPointer(data []byte) *byte {
	if len(data) == 0 {
		return nil
	}
	return &data[0]
}
