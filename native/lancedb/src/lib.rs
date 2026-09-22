//! Stable C ABI for the VMM native LanceDB storage backend.
//! VMM 原生 LanceDB 存储后端的稳定 C ABI。

use std::cell::RefCell;
use std::ffi::CString;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::os::raw::c_char;
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

use arrow_array::builder::{
    BooleanBuilder, FixedSizeListBuilder, Float32Builder, Float64Builder, Int32Builder,
    Int64Builder, StringBuilder, UInt32Builder, UInt64Builder,
};
use arrow_array::{
    Array, ArrayRef, BooleanArray, FixedSizeListArray, Float32Array, Float64Array, Int32Array,
    Int64Array, RecordBatch, StringArray, UInt32Array, UInt64Array,
};
use arrow_schema::{DataType, Field, Schema, SchemaRef};
use futures_util::TryStreamExt;
use lancedb::query::{ExecutableQuery, QueryBase};
use lancedb::table::OptimizeAction;
use lancedb::{Connection, DistanceType, Table, connect};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Number, Value};
use sha2::{Digest, Sha256};

/// ABI version exposed by the native library.
/// 原生库对外暴露的 ABI 版本。
pub const ABI_VERSION: u32 = 1;

/// Exact official LanceDB engine version compiled into this library.
/// 编译进本库的官方 LanceDB 引擎精确版本。
pub const ENGINE_VERSION: &str = "0.39.0";

/// Successful operation status.
/// 操作成功状态码。
pub const STATUS_OK: i32 = 0;
/// Invalid argument status.
/// 参数无效状态码。
pub const STATUS_INVALID_ARGUMENT: i32 = 1;
/// Table or database object not found status.
/// 表或数据库对象不存在状态码。
pub const STATUS_NOT_FOUND: i32 = 2;
/// Schema mismatch status.
/// Schema 不匹配状态码。
pub const STATUS_SCHEMA_MISMATCH: i32 = 3;
/// Cancellation status.
/// 取消状态码。
pub const STATUS_CANCELLED: i32 = 4;
/// Read timeout status.
/// 读取超时状态码。
pub const STATUS_TIMEOUT: i32 = 5;
/// I/O status.
/// I/O 状态码。
pub const STATUS_IO: i32 = 6;
/// Mutation outcome is not known after a timeout or engine failure.
/// 写操作超时或引擎失败后结果不确定状态码。
pub const STATUS_OUTCOME_UNCERTAIN: i32 = 7;
/// Internal engine status.
/// 内部引擎状态码。
pub const STATUS_INTERNAL: i32 = 8;

/// Owned byte buffer returned by this library.
/// 由本库返回并由本库释放的字节缓冲区。
#[repr(C)]
pub struct VmmLancedbBytes {
    /// Buffer pointer.
    /// 缓冲区指针。
    pub data: *mut u8,
    /// Number of initialized bytes.
    /// 已初始化字节数。
    pub len: u64,
    /// Capacity required to release the allocation.
    /// 释放分配所需的容量。
    pub cap: u64,
}

/// Runtime handle containing one official LanceDB connection and executor.
/// 包含一个官方 LanceDB 连接与执行器的运行时句柄。
pub struct VmmLancedbRuntime {
    /// Tokio executor used to drive the official async engine.
    /// 用于驱动官方异步引擎的 Tokio 执行器。
    executor: tokio::runtime::Runtime,
    /// Local LanceDB connection owned by this runtime.
    /// 由该运行时拥有的本地 LanceDB 连接。
    database: Connection,
    /// Database directory used for the native marker.
    /// 用于原生标记文件的数据库目录。
    database_path: PathBuf,
    /// Whether this directory already has a native table identity and may not auto-create a missing table.
    /// 该目录是否已经登记原生表身份；已登记目录缺表时禁止自动建表。
    initialized: AtomicBool,
}

/// Native schema column description accepted by the JSON ABI.
/// JSON ABI 接受的原生 Schema 列描述。
#[derive(Clone, Debug, Deserialize, Serialize)]
struct ColumnSpec {
    /// Physical column name.
    /// 物理列名。
    name: String,
    /// Contract type token.
    /// 契约类型标记。
    column_type: String,
    /// Vector dimension for vector_float32.
    /// vector_float32 的向量维度。
    #[serde(default)]
    vector_dim: u32,
    /// Whether null values are allowed.
    /// 是否允许空值。
    nullable: bool,
}

/// Native schema request carried by the create-table ABI.
/// 建表 ABI 携带的原生 Schema 请求。
#[derive(Clone, Debug, Deserialize, Serialize)]
struct SchemaRequest {
    /// Logical table name.
    /// 逻辑表名。
    table_name: String,
    /// Expected columns.
    /// 期望列集合。
    columns: Vec<ColumnSpec>,
    /// Whether an explicit destructive replacement is allowed.
    /// 是否允许显式破坏性替换。
    #[serde(default)]
    overwrite_if_exists: bool,
}

/// Marker persisted beside native LanceDB data to prevent opening an unrelated VLDB directory.
/// 保存于原生 LanceDB 数据旁的标记，用于防止误打开无关 VLDB 目录。
#[derive(Clone, Debug, Deserialize, Serialize)]
struct NativeMarker {
    /// Marker schema version.
    /// 标记 Schema 版本。
    schema_version: u32,
    /// Native ABI version.
    /// 原生 ABI 版本。
    abi_version: u32,
    /// Exact official engine version.
    /// 官方引擎精确版本。
    engine_version: String,
    /// Known table schema identities.
    /// 已登记表 Schema 身份集合。
    schema_identities: Vec<String>,
    /// Durable schema intents written before a table mutation so crash recovery can finish safely.
    /// 在表变更前持久化的 Schema 意图，用于安全完成崩溃恢复。
    #[serde(default)]
    pending_schema_identities: Vec<PendingSchemaIntent>,
}

/// Describes one table schema that may need recovery after a native process interruption.
/// 描述一次原生进程中断后可能需要恢复的表 Schema。
#[derive(Clone, Debug, Deserialize, Serialize)]
struct PendingSchemaIntent {
    /// Stable schema identity derived from the complete request.
    /// 从完整请求派生的稳定 Schema 身份。
    identity: String,
    /// Original schema request needed to validate or recreate the table.
    /// 用于校验或重建表的原始 Schema 请求。
    request: SchemaRequest,
    /// Whether recovery may create the table when it is missing.
    /// 表缺失时恢复是否允许创建该表。
    allow_create: bool,
}

/// Provides unique names for same-directory marker temporary files.
/// 为同目录标记临时文件提供唯一名称。
static MARKER_TEMP_SEQUENCE: AtomicU64 = AtomicU64::new(0);

// Thread-local error text consumed by the Go binding after a non-zero status.
// Go 绑定在非零状态后读取的线程局部错误文本。
thread_local! {
    static LAST_ERROR: RefCell<String> = const { RefCell::new(String::new()) };
}

/// Returns the ABI version for startup compatibility checks.
/// 返回供启动兼容性检查使用的 ABI 版本。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_abi_version() -> u32 {
    ABI_VERSION
}

/// Returns the exact official engine version as an owned UTF-8 buffer.
/// 以本库拥有的 UTF-8 缓冲区返回官方引擎精确版本。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_engine_version(out: *mut VmmLancedbBytes) -> i32 {
    ffi_status(|| unsafe { write_bytes(out, ENGINE_VERSION.as_bytes().to_vec()) })
}

/// Returns the fixed capability document used by runtime diagnostics.
/// 返回运行时诊断使用的固定能力文档。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_capabilities(out: *mut VmmLancedbBytes) -> i32 {
    ffi_status(|| {
        let capabilities = serde_json::json!({
            "schema_version": 1,
            "abi_version": ABI_VERSION,
            "engine_version": ENGINE_VERSION,
            "distance_type": "l2",
            "prefilter": true,
            "atomic_merge_insert": true,
            "operations": ["create_or_check", "upsert", "search", "delete", "count", "schema", "optimize", "drop"],
            "remote_storage": false,
            "context_deadline": "per_call_timeout",
            "context_cancellation": "before_call_only",
            "mutation_timeout_result": "outcome_uncertain",
            "library_lifetime": "process_resident"
        });
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&capabilities)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Creates a native runtime for one local database directory.
/// 为一个本地数据库目录创建原生运行时。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_runtime_create(
    path: *const u8,
    path_len: u64,
    timeout_ms: u64,
    out: *mut *mut VmmLancedbRuntime,
) -> i32 {
    ffi_status(|| {
        let path = read_utf8(path, path_len, "database_path")?;
        let database_path = PathBuf::from(path);
        prepare_native_directory(&database_path)?;
        let executor = tokio::runtime::Builder::new_multi_thread()
            .enable_all()
            .build()
            .map_err(|error| NativeError::internal(format!("create tokio runtime: {error}")))?;
        let uri = database_path.to_string_lossy().into_owned();
        let database = run_with_timeout(
            &executor,
            async {
                connect(&uri)
                    .execute()
                    .await
                    .map_err(NativeError::from_lancedb)
            },
            timeout_ms,
        )?;
        let initialized = recover_pending_schema_intents(&database_path, &database, &executor)?;
        let runtime = Box::new(VmmLancedbRuntime {
            executor,
            database,
            database_path,
            initialized: AtomicBool::new(initialized),
        });
        unsafe { write_handle(out, Box::into_raw(runtime)) }
    })
}

/// Destroys a runtime after all operations have completed.
/// 在所有操作完成后销毁运行时。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_runtime_destroy(runtime: *mut VmmLancedbRuntime) -> i32 {
    ffi_status(|| {
        if runtime.is_null() {
            return Ok(());
        }
        unsafe {
            drop(Box::from_raw(runtime));
        }
        Ok(())
    })
}

/// Ensures that one table exists and matches the requested schema.
/// 确保一个表存在且匹配请求的 Schema。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_create_table(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    schema_json: *const u8,
    schema_json_len: u64,
    overwrite_if_exists: u8,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let mut request: SchemaRequest = read_json(schema_json, schema_json_len, "schema")?;
        if request.table_name.is_empty() {
            request.table_name = table_name.clone();
        }
        if request.table_name != table_name {
            return Err(NativeError::invalid(
                "table name mismatch in schema request",
            ));
        }
        let expected = schema_from_columns(&request.columns)?;
        let overwrite = request.overwrite_if_exists || overwrite_if_exists != 0;
        let initialized = runtime.initialized.load(Ordering::Acquire);
        register_pending_schema_intent(
            &runtime.database_path,
            &request,
            !initialized || overwrite,
        )?;
        let task = ensure_table(
            &runtime.database,
            &request.table_name,
            expected,
            overwrite,
            initialized,
        );
        let result = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => {
                return Err(NativeError::outcome_uncertain(error.message));
            }
            Err(error) => return Err(error),
        };
        register_schema_identity(&runtime.database_path, &request)?;
        runtime.initialized.store(true, Ordering::Release);
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&result)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Performs an atomic official merge-insert keyed by the supplied columns.
/// 使用给定键列执行官方引擎原子 merge-insert。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_upsert(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    input_format: u32,
    data: *const u8,
    data_len: u64,
    key_columns_json: *const u8,
    key_columns_len: u64,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        if input_format != 1 {
            return Err(NativeError::invalid("only JSON rows input is supported"));
        }
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let data = read_bytes(data, data_len, "data")?;
        let keys: Vec<String> = read_json(key_columns_json, key_columns_len, "key_columns")?;
        if keys.is_empty() {
            return Err(NativeError::invalid("key_columns must not be empty"));
        }
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            upsert_table(table, data, keys).await
        };
        let result = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => {
                return Err(NativeError::outcome_uncertain(error.message));
            }
            Err(error) => return Err(error),
        };
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&result)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Executes a prefiltered L2 vector search and returns JSON rows.
/// 执行带预过滤的 L2 向量检索并返回 JSON 行。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_search(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    vector: *const f32,
    vector_len: u64,
    limit: u32,
    filter: *const u8,
    filter_len: u64,
    vector_column: *const u8,
    vector_column_len: u64,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        if vector.is_null() || vector_len == 0 {
            return Err(NativeError::invalid("vector must not be empty"));
        }
        let vector_length = abi_length(vector_len, "vector_len")?;
        let vector = unsafe { std::slice::from_raw_parts(vector, vector_length).to_vec() };
        if vector.iter().any(|value| !value.is_finite()) {
            return Err(NativeError::invalid("vector contains a non-finite value"));
        }
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let filter = read_optional_utf8(filter, filter_len, "filter")?;
        let vector_column = read_optional_utf8(vector_column, vector_column_len, "vector_column")?;
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            search_table(table, vector, limit, filter, vector_column).await
        };
        let result = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => return Err(error),
            Err(error) => return Err(error),
        };
        unsafe { write_bytes(out, result) }
    })
}

/// Deletes rows matching a validated predicate.
/// 删除符合已验证谓词的行。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_delete(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    condition: *const u8,
    condition_len: u64,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let condition = read_utf8(condition, condition_len, "condition")?;
        if condition.trim().is_empty() {
            return Err(NativeError::invalid("delete condition must not be empty"));
        }
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            delete_table(table, condition).await
        };
        let result = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => {
                return Err(NativeError::outcome_uncertain(error.message));
            }
            Err(error) => return Err(error),
        };
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&result)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Counts rows using the same engine predicate grammar as search and delete.
/// 使用与检索和删除相同的引擎谓词语法统计行数。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_count_rows(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    filter: *const u8,
    filter_len: u64,
    timeout_ms: u64,
    out_count: *mut u64,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let filter = read_optional_utf8(filter, filter_len, "filter")?;
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            table
                .count_rows((!filter.trim().is_empty()).then_some(filter))
                .await
                .map_err(NativeError::from_lancedb)
        };
        let count = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) => return Err(error),
        };
        unsafe { write_u64(out_count, count as u64) }
    })
}

/// Returns the canonical Arrow schema as a JSON document.
/// 返回规范 Arrow Schema JSON 文档。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_schema(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            table.schema().await.map_err(NativeError::from_lancedb)
        };
        let schema = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) => return Err(error),
        };
        let payload = schema_to_json(&schema)?;
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&payload)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Runs official LanceDB compaction and index maintenance for one table.
/// 对一个表执行官方 LanceDB 压缩与索引维护。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_optimize(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    timeout_ms: u64,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let database = runtime.database.clone();
        let task = async move {
            let table = database
                .open_table(&table_name)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            table
                .optimize(OptimizeAction::All)
                .await
                .map(|_| ())
                .map_err(NativeError::from_lancedb)
        };
        match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => {
                return Err(NativeError::outcome_uncertain(error.message));
            }
            Err(error) => return Err(error),
        }
        Ok(())
    })
}

/// Drops one table and reports the explicit destructive boundary.
/// 删除一个表并报告明确的破坏性边界。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_drop_table(
    runtime: *mut VmmLancedbRuntime,
    table_name: *const u8,
    table_name_len: u64,
    timeout_ms: u64,
    out: *mut VmmLancedbBytes,
) -> i32 {
    ffi_status(|| {
        let runtime = unsafe { runtime_ref(runtime)? };
        let table_name = read_utf8(table_name, table_name_len, "table_name")?;
        let database = runtime.database.clone();
        let task = async move {
            database
                .drop_table(&table_name, &[])
                .await
                .map_err(NativeError::from_lancedb)
                .map(|_| serde_json::json!({"success": true, "message": "table dropped"}))
        };
        let result = match run_with_timeout(&runtime.executor, task, timeout_ms) {
            Ok(result) => result,
            Err(error) if error.is_timeout() => {
                return Err(NativeError::outcome_uncertain(error.message));
            }
            Err(error) => return Err(error),
        };
        unsafe {
            write_bytes(
                out,
                serde_json::to_vec(&result)
                    .map_err(|error| NativeError::internal(error.to_string()))?,
            )
        }
    })
}

/// Releases one library-owned byte allocation using fixed-width scalar arguments.
/// 使用固定宽度标量参数释放一块由本库拥有的字节分配。
#[unsafe(no_mangle)]
pub unsafe extern "C" fn vmm_lancedb_bytes_free(data: *mut u8, len: u64, cap: u64) {
    if !data.is_null() {
        let Ok(length) = usize::try_from(len) else {
            return;
        };
        let Ok(capacity) = usize::try_from(cap) else {
            return;
        };
        // SAFETY: The caller passes the exact pointer, length, and capacity returned by this library.
        // 安全性：调用方传入本库原样返回的指针、长度和容量。
        unsafe { drop(Vec::from_raw_parts(data, length, capacity)) };
    }
}

/// Releases one library-owned C string allocation.
/// 释放一块由本库拥有的 C 字符串分配。
#[unsafe(no_mangle)]
pub unsafe extern "C" fn vmm_lancedb_string_free(value: *mut c_char) {
    if !value.is_null() {
        unsafe { drop(CString::from_raw(value)) };
    }
}

/// Returns the latest thread-local error as an owned byte buffer.
/// 以本库拥有的字节缓冲区返回最近一次线程局部错误。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_last_error_message(out: *mut VmmLancedbBytes) -> i32 {
    // Do not call `ffi_status` here: clearing LAST_ERROR before reading it would erase the diagnostic
    // that the caller is explicitly requesting. Any getter failure is stored as a new diagnostic.
    // 这里不能调用 `ffi_status`：读取前清空 LAST_ERROR 会抹掉调用方明确请求的诊断信息。
    // getter 自身失败时才记录新的诊断。
    match catch_unwind(AssertUnwindSafe(|| {
        let message = LAST_ERROR.with(|error| error.borrow().clone());
        unsafe { write_bytes(out, message.into_bytes()) }
    })) {
        Ok(Ok(())) => STATUS_OK,
        Ok(Err(error)) => set_last_error(error),
        Err(_) => set_last_error(NativeError::internal(
            "native LanceDB error getter panicked",
        )),
    }
}

/// Clears the latest thread-local error.
/// 清除最近一次线程局部错误。
#[unsafe(no_mangle)]
pub extern "C" fn vmm_lancedb_clear_last_error() {
    LAST_ERROR.with(|error| error.borrow_mut().clear());
}

/// Internal error carrying a stable ABI classification and human-readable text.
/// 携带稳定 ABI 分类和可读文本的内部错误。
#[derive(Debug)]
struct NativeError {
    /// ABI status classification.
    /// ABI 状态分类。
    code: i32,
    /// Diagnostic text.
    /// 诊断文本。
    message: String,
}

impl NativeError {
    /// Creates a table-not-found error with a stable status code.
    /// 创建表不存在错误并返回稳定的状态码。
    fn not_found(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_NOT_FOUND,
            message: message.into(),
        }
    }

    /// Creates an invalid-argument error.
    /// 创建参数无效错误。
    fn invalid(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_INVALID_ARGUMENT,
            message: message.into(),
        }
    }

    /// Creates an internal error.
    /// 创建内部错误。
    fn internal(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_INTERNAL,
            message: message.into(),
        }
    }

    /// Creates an uncertain-mutation error.
    /// 创建写操作结果不确定错误。
    fn outcome_uncertain(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_OUTCOME_UNCERTAIN,
            message: message.into(),
        }
    }

    /// Creates a timeout error for a read or a bounded operation.
    /// 创建读取或有界操作的超时错误。
    fn timeout(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_TIMEOUT,
            message: message.into(),
        }
    }

    /// Creates an I/O error.
    /// 创建 I/O 错误。
    fn io(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_IO,
            message: message.into(),
        }
    }

    /// Creates a schema mismatch error.
    /// 创建 Schema 不匹配错误。
    fn schema(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_SCHEMA_MISMATCH,
            message: message.into(),
        }
    }

    /// Reports whether this error is a timeout.
    /// 报告此错误是否属于超时。
    fn is_timeout(&self) -> bool {
        self.code == STATUS_TIMEOUT
    }
}

impl NativeError {
    /// Converts an official LanceDB error into the stable ABI classification.
    /// 把官方 LanceDB 错误转换成稳定 ABI 分类。
    fn from_lancedb(error: lancedb::Error) -> Self {
        let message = error.to_string();
        let lower = message.to_ascii_lowercase();
        let code = if lower.contains("not found") || lower.contains("does not exist") {
            STATUS_NOT_FOUND
        } else if lower.contains("schema") || lower.contains("dimension") {
            STATUS_SCHEMA_MISMATCH
        } else if lower.contains("invalid") || lower.contains("predicate") {
            STATUS_INVALID_ARGUMENT
        } else if lower.contains("io") || lower.contains("filesystem") {
            STATUS_IO
        } else {
            STATUS_INTERNAL
        };
        Self { code, message }
    }
}

/// Executes one ABI operation while keeping Rust panics inside the library boundary.
/// 执行一次 ABI 操作并把 Rust panic 限制在库边界内。
fn ffi_status<F>(operation: F) -> i32
where
    F: FnOnce() -> Result<(), NativeError>,
{
    LAST_ERROR.with(|error| error.borrow_mut().clear());
    match catch_unwind(AssertUnwindSafe(operation)) {
        Ok(Ok(())) => STATUS_OK,
        Ok(Err(error)) => set_last_error(error),
        Err(_) => set_last_error(NativeError::internal("native LanceDB operation panicked")),
    }
}

/// Stores one error and returns its stable status code.
/// 保存一个错误并返回其稳定状态码。
fn set_last_error(error: NativeError) -> i32 {
    let code = error.code;
    LAST_ERROR.with(|slot| *slot.borrow_mut() = error.message);
    code
}

/// Converts a fixed-width ABI length into the current platform size.
/// 将固定宽度 ABI 长度转换为当前平台的 usize。
fn abi_length(value: u64, name: &str) -> Result<usize, NativeError> {
    usize::try_from(value)
        .map_err(|_| NativeError::invalid(format!("{name} length exceeds this platform")))
}

/// Reads a fixed-length UTF-8 string from the C ABI.
/// 从 C ABI 读取固定长度 UTF-8 字符串。
fn read_utf8(pointer: *const u8, length: u64, name: &str) -> Result<String, NativeError> {
    let length = abi_length(length, name)?;
    if pointer.is_null() && length != 0 {
        return Err(NativeError::invalid(format!("{name} pointer is null")));
    }
    let bytes = if length == 0 {
        &[]
    } else {
        unsafe { std::slice::from_raw_parts(pointer, length) }
    };
    String::from_utf8(bytes.to_vec())
        .map_err(|error| NativeError::invalid(format!("{name} is not UTF-8: {error}")))
}

/// Reads an optional fixed-length UTF-8 string from the C ABI.
/// 从 C ABI 读取可选的固定长度 UTF-8 字符串。
fn read_optional_utf8(pointer: *const u8, length: u64, name: &str) -> Result<String, NativeError> {
    if length == 0 {
        return Ok(String::new());
    }
    read_utf8(pointer, length, name)
}

/// Reads a fixed-length byte slice from the C ABI.
/// 从 C ABI 读取固定长度字节切片。
fn read_bytes(pointer: *const u8, length: u64, name: &str) -> Result<Vec<u8>, NativeError> {
    let length = abi_length(length, name)?;
    if pointer.is_null() && length != 0 {
        return Err(NativeError::invalid(format!("{name} pointer is null")));
    }
    Ok(if length == 0 {
        Vec::new()
    } else {
        unsafe { std::slice::from_raw_parts(pointer, length).to_vec() }
    })
}

/// Decodes one JSON payload from an ABI byte range.
/// 从 ABI 字节范围解码一份 JSON 载荷。
fn read_json<T: for<'de> Deserialize<'de>>(
    pointer: *const u8,
    length: u64,
    name: &str,
) -> Result<T, NativeError> {
    let bytes = read_bytes(pointer, length, name)?;
    serde_json::from_slice(&bytes)
        .map_err(|error| NativeError::invalid(format!("decode {name} JSON: {error}")))
}

/// Writes an owned vector into an ABI output buffer.
/// 把一个拥有所有权的向量写入 ABI 输出缓冲区。
unsafe fn write_bytes(out: *mut VmmLancedbBytes, mut bytes: Vec<u8>) -> Result<(), NativeError> {
    if out.is_null() {
        return Err(NativeError::invalid("output buffer is null"));
    }
    let value = VmmLancedbBytes {
        data: bytes.as_mut_ptr(),
        len: bytes.len() as u64,
        cap: bytes.capacity() as u64,
    };
    std::mem::forget(bytes);
    unsafe { *out = value };
    Ok(())
}

/// Writes an opaque runtime handle into an ABI output pointer.
/// 把不透明运行时句柄写入 ABI 输出指针。
unsafe fn write_handle(
    out: *mut *mut VmmLancedbRuntime,
    handle: *mut VmmLancedbRuntime,
) -> Result<(), NativeError> {
    if out.is_null() {
        if !handle.is_null() {
            unsafe { drop(Box::from_raw(handle)) };
        }
        return Err(NativeError::invalid("runtime output is null"));
    }
    unsafe { *out = handle };
    Ok(())
}

/// Writes one count into an ABI output pointer.
/// 把一个计数写入 ABI 输出指针。
unsafe fn write_u64(out: *mut u64, value: u64) -> Result<(), NativeError> {
    if out.is_null() {
        return Err(NativeError::invalid("count output is null"));
    }
    unsafe { *out = value };
    Ok(())
}

/// Borrows a non-null runtime pointer for one synchronous ABI operation.
/// 为一次同步 ABI 操作借用非空运行时指针。
unsafe fn runtime_ref<'a>(
    runtime: *mut VmmLancedbRuntime,
) -> Result<&'a VmmLancedbRuntime, NativeError> {
    unsafe { runtime.as_ref() }.ok_or_else(|| NativeError::invalid("runtime is null"))
}

/// Runs one future with an optional millisecond timeout.
/// 使用可选的毫秒超时运行一个 future。
fn run_with_timeout<T, F>(
    executor: &tokio::runtime::Runtime,
    future: F,
    timeout_ms: u64,
) -> Result<T, NativeError>
where
    F: std::future::Future<Output = Result<T, NativeError>>,
{
    if timeout_ms == 0 {
        return executor.block_on(future);
    }
    match executor.block_on(async move {
        tokio::time::timeout(std::time::Duration::from_millis(timeout_ms), future).await
    }) {
        Ok(result) => result,
        Err(_) => Err(NativeError::timeout("native LanceDB operation timed out")),
    }
}

/// Prepares and validates the native marker before connecting to a database directory.
/// 在连接数据库目录前准备并校验原生标记。
fn prepare_native_directory(path: &Path) -> Result<bool, NativeError> {
    if path.as_os_str().is_empty() {
        return Err(NativeError::invalid("database path must not be empty"));
    }
    fs::create_dir_all(path)
        .map_err(|error| NativeError::io(format!("create database directory: {error}")))?;
    let marker_path = path.join(".vmm-native.json");
    if marker_path.exists() {
        let bytes = fs::read(&marker_path)
            .map_err(|error| NativeError::io(format!("read native marker: {error}")))?;
        let marker: NativeMarker = serde_json::from_slice(&bytes)
            .map_err(|error| NativeError::io(format!("decode native marker: {error}")))?;
        if marker.abi_version != ABI_VERSION || marker.engine_version != ENGINE_VERSION {
            return Err(NativeError::schema(format!(
                "native marker is for ABI {} engine {}, expected ABI {} engine {}",
                marker.abi_version, marker.engine_version, ABI_VERSION, ENGINE_VERSION
            )));
        }
        if marker.schema_identities.is_empty() && marker.pending_schema_identities.is_empty() {
            let has_untracked_data = fs::read_dir(path)
                .map_err(|error| {
                    NativeError::io(format!("list native database directory: {error}"))
                })?
                .any(|entry| {
                    entry
                        .as_ref()
                        .map(|item| {
                            let name = item.file_name();
                            name != ".vmm-writer.lock"
                                && name != ".vmm-pair.json"
                                && name != ".vmm-native.json"
                        })
                        .unwrap_or(true)
                });
            if has_untracked_data {
                return Err(NativeError::schema(
                    "native marker has no schema identity but database data is present",
                ));
            }
        }
        return Ok(!marker.schema_identities.is_empty());
    }
    let mut entries = fs::read_dir(path)
        .map_err(|error| NativeError::io(format!("list native database directory: {error}")))?;
    if entries.any(|entry| {
        entry
            .as_ref()
            .map(|item| {
                let name = item.file_name();
                name != ".vmm-writer.lock" && name != ".vmm-pair.json"
            })
            .unwrap_or(true)
    }) {
        return Err(NativeError::schema(
            "database directory is non-empty and has no compatible .vmm-native.json marker; migrate it before opening native LanceDB",
        ));
    }
    let marker = NativeMarker {
        schema_version: 1,
        abi_version: ABI_VERSION,
        engine_version: ENGINE_VERSION.to_string(),
        schema_identities: Vec::new(),
        pending_schema_identities: Vec::new(),
    };
    write_marker_atomic(&marker_path, &marker)?;
    Ok(false)
}

/// Reads and validates one native marker without changing it on disk.
/// 读取并校验一个原生标记，但不修改磁盘内容。
fn read_native_marker(path: &Path) -> Result<NativeMarker, NativeError> {
    let bytes =
        fs::read(path).map_err(|error| NativeError::io(format!("read native marker: {error}")))?;
    serde_json::from_slice(&bytes)
        .map_err(|error| NativeError::io(format!("decode native marker: {error}")))
}

/// Writes a marker through a same-directory durable temporary file and atomic replacement.
/// 通过同目录持久化临时文件和原子替换写入标记。
fn write_marker_atomic(path: &Path, marker: &NativeMarker) -> Result<(), NativeError> {
    let bytes = serde_json::to_vec_pretty(marker)
        .map_err(|error| NativeError::internal(error.to_string()))?;
    let parent = path
        .parent()
        .ok_or_else(|| NativeError::io("native marker has no parent directory"))?;
    let mut temporary = None;
    for _ in 0..32 {
        let sequence = MARKER_TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let temporary_path = parent.join(format!(
            ".vmm-native.json.tmp-{}-{sequence}",
            std::process::id()
        ));
        match OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary_path)
        {
            Ok(file) => {
                temporary = Some((temporary_path, file));
                break;
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => {
                return Err(NativeError::io(format!(
                    "create native marker temporary file: {error}"
                )));
            }
        }
    }
    let Some((temporary_path, mut file)) = temporary else {
        return Err(NativeError::io(
            "could not allocate a unique native marker temporary file",
        ));
    };
    let write_result = (|| {
        file.write_all(&bytes)?;
        file.sync_all()?;
        drop(file);
        replace_marker_file(&temporary_path, path)?;
        sync_marker_directory(parent)?;
        Ok::<(), std::io::Error>(())
    })();
    if let Err(error) = write_result {
        let _ = fs::remove_file(&temporary_path);
        return Err(NativeError::io(format!(
            "write native marker atomically: {error}"
        )));
    }
    Ok(())
}

/// Replaces the destination marker atomically on every supported operating system.
/// 在所有支持的操作系统上原子替换目标标记。
#[cfg(not(windows))]
fn replace_marker_file(source: &Path, destination: &Path) -> std::io::Result<()> {
    fs::rename(source, destination)
}

/// Replaces the destination marker through MoveFileExW without a remove-then-rename window.
/// 通过 MoveFileExW 替换目标标记，避免先删除再重命名的窗口。
#[cfg(windows)]
fn replace_marker_file(source: &Path, destination: &Path) -> std::io::Result<()> {
    use std::os::windows::ffi::OsStrExt;

    #[link(name = "kernel32")]
    unsafe extern "system" {
        fn MoveFileExW(existing: *const u16, replacement: *const u16, flags: u32) -> i32;
    }
    const MOVEFILE_REPLACE_EXISTING: u32 = 0x00000001;
    const MOVEFILE_WRITE_THROUGH: u32 = 0x00000008;
    let source_wide = source
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect::<Vec<_>>();
    let destination_wide = destination
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect::<Vec<_>>();
    let replaced = unsafe {
        MoveFileExW(
            source_wide.as_ptr(),
            destination_wide.as_ptr(),
            MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH,
        )
    };
    if replaced == 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

/// Flushes the containing directory after an atomic marker replacement on Unix filesystems.
/// 在 Unix 文件系统上原子替换后刷新包含目录。
#[cfg(unix)]
fn sync_marker_directory(path: &Path) -> std::io::Result<()> {
    OpenOptions::new().read(true).open(path)?.sync_all()
}

/// Windows directory metadata is durably handled by MoveFileExW with WRITE_THROUGH.
/// Windows 目录元数据由带 WRITE_THROUGH 的 MoveFileExW 负责持久化。
#[cfg(not(unix))]
fn sync_marker_directory(_path: &Path) -> std::io::Result<()> {
    Ok(())
}

/// Persists one schema intent before any table create or replacement can mutate LanceDB.
/// 在任何建表或替换会修改 LanceDB 前持久化一个 Schema 意图。
fn register_pending_schema_intent(
    path: &Path,
    request: &SchemaRequest,
    allow_create: bool,
) -> Result<(), NativeError> {
    let marker_path = path.join(".vmm-native.json");
    let mut marker = read_native_marker(&marker_path)?;
    let identity = schema_identity(request)?;
    if marker
        .schema_identities
        .iter()
        .any(|value| value == &identity)
        && !allow_create
    {
        return Ok(());
    }
    if marker
        .pending_schema_identities
        .iter()
        .any(|pending| pending.identity == identity && pending.allow_create == allow_create)
    {
        return Ok(());
    }
    marker
        .pending_schema_identities
        .retain(|pending| pending.identity != identity);
    marker.pending_schema_identities.push(PendingSchemaIntent {
        identity,
        request: request.clone(),
        allow_create,
    });
    write_marker_atomic(&marker_path, &marker)
}

/// Recovers durable schema intents after opening the official LanceDB connection.
/// 打开官方 LanceDB 连接后恢复持久化的 Schema 意图。
fn recover_pending_schema_intents(
    path: &Path,
    database: &Connection,
    executor: &tokio::runtime::Runtime,
) -> Result<bool, NativeError> {
    let marker_path = path.join(".vmm-native.json");
    let mut marker = read_native_marker(&marker_path)?;
    if marker.pending_schema_identities.is_empty() {
        return Ok(!marker.schema_identities.is_empty());
    }
    let pending = std::mem::take(&mut marker.pending_schema_identities);
    let mut completed_any = false;
    for intent in pending {
        let expected = schema_from_columns(&intent.request.columns)?;
        let opened = executor
            .block_on(database.open_table(&intent.request.table_name).execute())
            .map_err(NativeError::from_lancedb);
        match opened {
            Ok(table) => executor
                .block_on(validate_table_schema(&table, expected))
                .map_err(|error| {
                    NativeError::schema(format!(
                        "pending native table {} cannot be recovered: {}",
                        intent.request.table_name, error.message
                    ))
                })?,
            Err(error) if error.code == STATUS_NOT_FOUND && intent.allow_create => {
                executor
                    .block_on(
                        database
                            .create_empty_table(&intent.request.table_name, expected)
                            .execute(),
                    )
                    .map_err(NativeError::from_lancedb)?;
            }
            Err(error) if error.code == STATUS_NOT_FOUND => {
                // Keep an intent that is not authorized to create a missing table so normal startup
                // remains fail-closed while explicit maintenance can upgrade allow_create later.
                // 保留无权创建缺失表的意图，使正常启动继续 fail-closed，而显式维护可随后升级 allow_create。
                marker.pending_schema_identities.push(intent);
                continue;
            }
            Err(error) => return Err(error),
        }
        completed_any = true;
        if !marker
            .schema_identities
            .iter()
            .any(|value| value == &intent.identity)
        {
            marker.schema_identities.push(intent.identity);
        }
    }
    if completed_any {
        write_marker_atomic(&marker_path, &marker).map_err(|error| {
            NativeError::outcome_uncertain(format!(
                "native table recovery completed but marker update is uncertain: {}",
                error.message
            ))
        })?;
    }
    Ok(!marker.schema_identities.is_empty())
}

/// Registers one table schema identity in the native marker.
/// 把一个表 Schema 身份登记到原生标记中。
fn register_schema_identity(path: &Path, request: &SchemaRequest) -> Result<(), NativeError> {
    let marker_path = path.join(".vmm-native.json");
    let mut marker = read_native_marker(&marker_path)?;
    let identity = schema_identity(request)?;
    let identity_added = !marker
        .schema_identities
        .iter()
        .any(|value| value == &identity);
    if identity_added {
        marker.schema_identities.push(identity.clone());
    }
    let pending_removed = marker
        .pending_schema_identities
        .iter()
        .any(|pending| pending.identity == identity);
    if pending_removed {
        marker
            .pending_schema_identities
            .retain(|pending| pending.identity != identity);
    }
    if identity_added || pending_removed {
        write_marker_atomic(&marker_path, &marker).map_err(|error| {
            NativeError::outcome_uncertain(format!(
                "table {} is ready but native marker update is uncertain: {}",
                request.table_name, error.message
            ))
        })?;
    }
    Ok(())
}

/// Builds a stable schema identity used for migration and diagnostics.
/// 构造用于迁移和诊断的稳定 Schema 身份。
fn schema_identity(request: &SchemaRequest) -> Result<String, NativeError> {
    let canonical =
        serde_json::to_vec(request).map_err(|error| NativeError::internal(error.to_string()))?;
    let digest = Sha256::digest(canonical);
    Ok(format!("{}:{}", request.table_name, hex_digest(&digest)))
}

/// Encodes a digest without adding a second hexadecimal dependency.
/// 不增加第二个十六进制依赖来编码摘要。
fn hex_digest(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

/// Converts the neutral column contract into an Arrow schema.
/// 把中立列契约转换成 Arrow Schema。
fn schema_from_columns(columns: &[ColumnSpec]) -> Result<SchemaRef, NativeError> {
    if columns.is_empty() {
        return Err(NativeError::invalid(
            "table schema must contain at least one column",
        ));
    }
    let mut fields = Vec::with_capacity(columns.len());
    for column in columns {
        if column.name.trim().is_empty() {
            return Err(NativeError::invalid("schema column name must not be empty"));
        }
        let data_type = match column.column_type.trim().to_ascii_lowercase().as_str() {
            "string" => DataType::Utf8,
            "int64" => DataType::Int64,
            "uint64" => DataType::UInt64,
            "int32" => DataType::Int32,
            "uint32" => DataType::UInt32,
            "float32" => DataType::Float32,
            "float64" => DataType::Float64,
            "bool" => DataType::Boolean,
            "vector_float32" => {
                if column.vector_dim == 0 {
                    return Err(NativeError::invalid(format!(
                        "vector column {} dimension must be > 0",
                        column.name
                    )));
                }
                DataType::FixedSizeList(
                    Arc::new(Field::new("item", DataType::Float32, true)),
                    column.vector_dim as i32,
                )
            }
            unsupported => {
                return Err(NativeError::invalid(format!(
                    "unsupported schema type {unsupported}"
                )));
            }
        };
        fields.push(Field::new(&column.name, data_type, column.nullable));
    }
    Ok(Arc::new(Schema::new(fields)))
}

/// Creates a table when the directory is new, otherwise validates its schema without replacement.
/// 新目录建表，已有表只校验 Schema，不执行替换。
async fn ensure_table(
    database: &Connection,
    name: &str,
    expected: SchemaRef,
    overwrite: bool,
    initialized: bool,
) -> Result<Value, NativeError> {
    match database
        .open_table(name)
        .execute()
        .await
        .map_err(NativeError::from_lancedb)
    {
        Ok(table) => {
            if overwrite {
                database
                    .drop_table(name, &[])
                    .await
                    .map_err(|error| NativeError::outcome_uncertain(error.to_string()))?;
                database
                    .create_empty_table(name, expected)
                    .execute()
                    .await
                    .map_err(NativeError::from_lancedb)?;
                return Ok(serde_json::json!({"success": true, "message": "table recreated"}));
            }
            validate_table_schema(&table, expected).await?;
            Ok(serde_json::json!({"success": true, "message": "table already exists"}))
        }
        Err(error) if error.code == STATUS_NOT_FOUND => {
            if initialized && !overwrite {
                return Err(NativeError::not_found(format!(
                    "native table {name} is missing; run explicit maintenance before creating it"
                )));
            }
            database
                .create_empty_table(name, expected)
                .execute()
                .await
                .map_err(NativeError::from_lancedb)?;
            Ok(serde_json::json!({"success": true, "message": "table created"}))
        }
        Err(error) => Err(error),
    }
}

/// Checks an existing table schema field by field.
/// 逐列校验现有表 Schema。
async fn validate_table_schema(table: &Table, expected: SchemaRef) -> Result<(), NativeError> {
    let actual = table.schema().await.map_err(NativeError::from_lancedb)?;
    if actual.as_ref() != expected.as_ref() {
        return Err(NativeError::schema(format!(
            "table schema mismatch: expected {expected:?}, actual {actual:?}"
        )));
    }
    Ok(())
}

/// Performs one official atomic merge-insert and maps its accounting fields.
/// 执行一次官方原子 merge-insert 并映射其计数字段。
async fn upsert_table(
    table: Table,
    data: Vec<u8>,
    keys: Vec<String>,
) -> Result<Value, NativeError> {
    let schema = table.schema().await.map_err(NativeError::from_lancedb)?;
    let (batch, input_rows) = rows_to_batch(&data, schema.clone())?;
    let key_refs = keys.iter().map(String::as_str).collect::<Vec<_>>();
    let mut merge = table.merge_insert(&key_refs);
    merge
        .when_matched_update_all(None)
        .when_not_matched_insert_all();
    let reader = arrow_array::RecordBatchIterator::new(vec![Ok(batch)], schema);
    let result = merge
        .execute(Box::new(reader))
        .await
        .map_err(NativeError::from_lancedb)?;
    Ok(serde_json::json!({
        "version": result.version,
        "input_rows": input_rows,
        "inserted_rows": result.num_inserted_rows,
        "updated_rows": result.num_updated_rows,
        "deleted_rows": result.num_deleted_rows
    }))
}

/// Executes one filtered L2 vector search and serializes all result batches.
/// 执行一次带过滤的 L2 向量检索并序列化全部结果批次。
async fn search_table(
    table: Table,
    vector: Vec<f32>,
    limit: u32,
    filter: String,
    vector_column: String,
) -> Result<Vec<u8>, NativeError> {
    let mut query = table
        .query()
        .nearest_to(vector)
        .map_err(NativeError::from_lancedb)?
        .distance_type(DistanceType::L2);
    if !vector_column.trim().is_empty() {
        query = query.column(vector_column.trim());
    }
    if !filter.trim().is_empty() {
        query = query.only_if(filter.trim());
    }
    query = query.limit(if limit == 0 { 10 } else { limit as usize });
    let schema = query
        .output_schema()
        .await
        .map_err(NativeError::from_lancedb)?;
    let mut stream = query.execute().await.map_err(NativeError::from_lancedb)?;
    let mut rows = Vec::new();
    while let Some(batch) = stream.try_next().await.map_err(NativeError::from_lancedb)? {
        rows.extend(batch_to_rows(&schema, &batch)?);
    }
    serde_json::to_vec(&rows)
        .map_err(|error| NativeError::internal(format!("encode search rows: {error}")))
}

/// Executes one predicate delete and maps the official delete result.
/// 执行一次谓词删除并映射官方删除结果。
async fn delete_table(table: Table, condition: String) -> Result<Value, NativeError> {
    let result = table
        .delete(&condition)
        .await
        .map_err(NativeError::from_lancedb)?;
    Ok(serde_json::json!({
        "success": true,
        "message": "delete completed",
        "version": result.version,
        "deleted_rows": result.num_deleted_rows
    }))
}

/// Encodes one Arrow schema into the stable JSON schema representation.
/// 把一个 Arrow Schema 编码为稳定的 JSON Schema 表示。
fn schema_to_json(schema: &SchemaRef) -> Result<Value, NativeError> {
    let columns = schema
        .fields()
        .iter()
        .map(|field| {
            let (column_type, vector_dim) = match field.data_type() {
                DataType::Utf8 => ("string".to_string(), 0),
                DataType::Int64 => ("int64".to_string(), 0),
                DataType::UInt64 => ("uint64".to_string(), 0),
                DataType::Int32 => ("int32".to_string(), 0),
                DataType::UInt32 => ("uint32".to_string(), 0),
                DataType::Float32 => ("float32".to_string(), 0),
                DataType::Float64 => ("float64".to_string(), 0),
                DataType::Boolean => ("bool".to_string(), 0),
                DataType::FixedSizeList(item, dimension)
                    if item.data_type() == &DataType::Float32 =>
                {
                    ("vector_float32".to_string(), *dimension as u32)
                }
                unsupported => {
                    return Err(NativeError::schema(format!(
                        "unsupported schema type {unsupported:?}"
                    )));
                }
            };
            Ok(serde_json::json!({
                "name": field.name(),
                "column_type": column_type,
                "vector_dim": vector_dim,
                "nullable": field.is_nullable()
            }))
        })
        .collect::<Result<Vec<_>, NativeError>>()?;
    Ok(serde_json::json!({"columns": columns}))
}

/// Converts one Arrow record batch into JSON objects without passing Arrow across the ABI.
/// 把一个 Arrow RecordBatch 转为 JSON 对象，不让 Arrow 跨过 ABI。
fn batch_to_rows(schema: &SchemaRef, batch: &RecordBatch) -> Result<Vec<Value>, NativeError> {
    let mut rows = Vec::with_capacity(batch.num_rows());
    for row_index in 0..batch.num_rows() {
        let mut object = Map::new();
        for (column_index, field) in schema.fields().iter().enumerate() {
            let array = batch.column(column_index);
            object.insert(
                field.name().clone(),
                array_value(array, field.data_type(), row_index)?,
            );
        }
        rows.push(Value::Object(object));
    }
    Ok(rows)
}

/// Extracts one JSON value from a supported Arrow array cell.
/// 从受支持的 Arrow 数组单元提取一个 JSON 值。
fn array_value(array: &ArrayRef, data_type: &DataType, index: usize) -> Result<Value, NativeError> {
    if array.is_null(index) {
        return Ok(Value::Null);
    }
    match data_type {
        DataType::Utf8 => Ok(Value::String(
            array
                .as_any()
                .downcast_ref::<StringArray>()
                .ok_or_else(|| NativeError::internal("UTF-8 array downcast failed"))?
                .value(index)
                .to_string(),
        )),
        DataType::Int64 => Ok(Value::Number(Number::from(
            array
                .as_any()
                .downcast_ref::<Int64Array>()
                .ok_or_else(|| NativeError::internal("int64 array downcast failed"))?
                .value(index),
        ))),
        DataType::UInt64 => Ok(Value::Number(Number::from(
            array
                .as_any()
                .downcast_ref::<UInt64Array>()
                .ok_or_else(|| NativeError::internal("uint64 array downcast failed"))?
                .value(index),
        ))),
        DataType::Int32 => Ok(Value::Number(Number::from(
            array
                .as_any()
                .downcast_ref::<Int32Array>()
                .ok_or_else(|| NativeError::internal("int32 array downcast failed"))?
                .value(index),
        ))),
        DataType::UInt32 => Ok(Value::Number(Number::from(
            array
                .as_any()
                .downcast_ref::<UInt32Array>()
                .ok_or_else(|| NativeError::internal("uint32 array downcast failed"))?
                .value(index),
        ))),
        DataType::Float32 => float_json(
            array
                .as_any()
                .downcast_ref::<Float32Array>()
                .ok_or_else(|| NativeError::internal("float32 array downcast failed"))?
                .value(index) as f64,
        ),
        DataType::Float64 => float_json(
            array
                .as_any()
                .downcast_ref::<Float64Array>()
                .ok_or_else(|| NativeError::internal("float64 array downcast failed"))?
                .value(index),
        ),
        DataType::Boolean => Ok(Value::Bool(
            array
                .as_any()
                .downcast_ref::<BooleanArray>()
                .ok_or_else(|| NativeError::internal("boolean array downcast failed"))?
                .value(index),
        )),
        DataType::FixedSizeList(item, _) if item.data_type() == &DataType::Float32 => {
            let list = array
                .as_any()
                .downcast_ref::<FixedSizeListArray>()
                .ok_or_else(|| NativeError::internal("vector array downcast failed"))?;
            let values = list.value(index);
            let values = values
                .as_any()
                .downcast_ref::<Float32Array>()
                .ok_or_else(|| NativeError::internal("vector values downcast failed"))?;
            let mut output = Vec::with_capacity(values.len());
            for value_index in 0..values.len() {
                output.push(float_json(values.value(value_index) as f64)?);
            }
            Ok(Value::Array(output))
        }
        unsupported => Err(NativeError::internal(format!(
            "unsupported result type {unsupported:?}"
        ))),
    }
}

/// Converts one finite floating-point value into a JSON number.
/// 把一个有限浮点值转换为 JSON 数字。
fn float_json(value: f64) -> Result<Value, NativeError> {
    Number::from_f64(value)
        .map(Value::Number)
        .ok_or_else(|| NativeError::internal("non-finite floating-point value in result"))
}

/// Appends one nullable string cell while enforcing the requested field contract.
/// 追加一个可空字符串单元并校验请求字段契约。
fn append_string(
    builder: &mut StringBuilder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::String(value)) => {
            builder.append_value(value);
            Ok(())
        }
        None | Some(Value::Null) => Err(NativeError::invalid(format!(
            "required string field {name} is missing"
        ))),
        Some(_) => Err(NativeError::invalid(format!(
            "field {name} must be a string"
        ))),
    }
}

/// Appends one nullable signed 64-bit integer cell.
/// 追加一个可空有符号 64 位整数单元。
fn append_i64(
    builder: &mut Int64Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None | Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_i64()
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be an int64"))),
        None => Err(NativeError::invalid(format!(
            "required int64 field {name} is missing"
        ))),
    }
}

/// Appends one nullable unsigned 64-bit integer cell.
/// 追加一个可空无符号 64 位整数单元。
fn append_u64(
    builder: &mut UInt64Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_u64()
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be a uint64"))),
        None => Err(NativeError::invalid(format!(
            "required uint64 field {name} is missing"
        ))),
    }
}

/// Appends one nullable signed 32-bit integer cell.
/// 追加一个可空有符号 32 位整数单元。
fn append_i32(
    builder: &mut Int32Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_i64()
            .and_then(|value| i32::try_from(value).ok())
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be an int32"))),
        None => Err(NativeError::invalid(format!(
            "required int32 field {name} is missing"
        ))),
    }
}

/// Appends one nullable unsigned 32-bit integer cell.
/// 追加一个可空无符号 32 位整数单元。
fn append_u32(
    builder: &mut UInt32Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_u64()
            .and_then(|value| u32::try_from(value).ok())
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be a uint32"))),
        None => Err(NativeError::invalid(format!(
            "required uint32 field {name} is missing"
        ))),
    }
}

/// Appends one nullable finite 32-bit floating-point cell.
/// 追加一个可空有限 32 位浮点单元。
fn append_f32(
    builder: &mut Float32Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_f64()
            .and_then(|value| {
                let value = value as f32;
                value.is_finite().then_some(value)
            })
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be a finite float32"))),
        None => Err(NativeError::invalid(format!(
            "required float32 field {name} is missing"
        ))),
    }
}

/// Appends one nullable finite 64-bit floating-point cell.
/// 追加一个可空有限 64 位浮点单元。
fn append_f64(
    builder: &mut Float64Builder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(value) => value
            .as_f64()
            .filter(|value| value.is_finite())
            .map(|value| builder.append_value(value))
            .ok_or_else(|| NativeError::invalid(format!("field {name} must be a finite float64"))),
        None => Err(NativeError::invalid(format!(
            "required float64 field {name} is missing"
        ))),
    }
}

/// Appends one nullable boolean cell.
/// 追加一个可空布尔单元。
fn append_bool(
    builder: &mut BooleanBuilder,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
) -> Result<(), NativeError> {
    match value {
        None | Some(Value::Null) if nullable => {
            builder.append_null();
            Ok(())
        }
        Some(Value::Bool(value)) => {
            builder.append_value(*value);
            Ok(())
        }
        None | Some(Value::Null) => Err(NativeError::invalid(format!(
            "required bool field {name} is missing"
        ))),
        Some(_) => Err(NativeError::invalid(format!("field {name} must be a bool"))),
    }
}

/// Appends one nullable fixed-size float vector and rejects wrong dimensions.
/// 追加一个可空固定长度浮点向量并拒绝错误维度。
fn append_vector(
    builder: &mut FixedSizeListBuilder<Float32Builder>,
    value: Option<&Value>,
    nullable: bool,
    name: &str,
    dimension: usize,
) -> Result<(), NativeError> {
    match value {
        None | Some(Value::Null) if nullable => {
            for _ in 0..dimension {
                builder.values().append_null();
            }
            builder.append(false);
            Ok(())
        }
        Some(Value::Array(values)) if values.len() == dimension => {
            for value in values {
                let value = value
                    .as_f64()
                    .and_then(|value| {
                        let value = value as f32;
                        value.is_finite().then_some(value)
                    })
                    .ok_or_else(|| {
                        NativeError::invalid(format!(
                            "vector field {name} contains a non-finite or non-number value"
                        ))
                    })?;
                builder.values().append_value(value);
            }
            builder.append(true);
            Ok(())
        }
        Some(Value::Array(values)) => Err(NativeError::invalid(format!(
            "vector field {name} dimension {} does not match expected {dimension}",
            values.len()
        ))),
        None | Some(Value::Null) => Err(NativeError::invalid(format!(
            "required vector field {name} is missing"
        ))),
        Some(_) => Err(NativeError::invalid(format!(
            "field {name} must be a vector array"
        ))),
    }
}

/// Converts JSON rows into an Arrow record batch matching the table schema.
/// 把 JSON 行转换为匹配表 Schema 的 Arrow RecordBatch。
fn rows_to_batch(data: &[u8], schema: SchemaRef) -> Result<(RecordBatch, u64), NativeError> {
    let rows: Vec<Map<String, Value>> = serde_json::from_slice(data)
        .map_err(|error| NativeError::invalid(format!("decode upsert rows: {error}")))?;
    let row_count = rows.len() as u64;
    let arrays = schema
        .fields()
        .iter()
        .map(|field| build_array(field, &rows))
        .collect::<Result<Vec<_>, _>>()?;
    let batch = RecordBatch::try_new(schema, arrays)
        .map_err(|error| NativeError::invalid(format!("build upsert batch: {error}")))?;
    Ok((batch, row_count))
}

/// Builds one Arrow array from the fixed contract type and JSON rows.
/// 根据固定契约类型和 JSON 行构造一个 Arrow 数组。
fn build_array(field: &Field, rows: &[Map<String, Value>]) -> Result<ArrayRef, NativeError> {
    let name = field.name();
    match field.data_type() {
        DataType::Utf8 => {
            let mut builder = StringBuilder::with_capacity(rows.len(), rows.len() * 16);
            for row in rows {
                append_string(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::Int64 => {
            let mut builder = Int64Builder::with_capacity(rows.len());
            for row in rows {
                append_i64(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::UInt64 => {
            let mut builder = UInt64Builder::with_capacity(rows.len());
            for row in rows {
                append_u64(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::Int32 => {
            let mut builder = Int32Builder::with_capacity(rows.len());
            for row in rows {
                append_i32(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::UInt32 => {
            let mut builder = UInt32Builder::with_capacity(rows.len());
            for row in rows {
                append_u32(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::Float32 => {
            let mut builder = Float32Builder::with_capacity(rows.len());
            for row in rows {
                append_f32(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::Float64 => {
            let mut builder = Float64Builder::with_capacity(rows.len());
            for row in rows {
                append_f64(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::Boolean => {
            let mut builder = BooleanBuilder::with_capacity(rows.len());
            for row in rows {
                append_bool(&mut builder, row.get(name), field.is_nullable(), name)?;
            }
            Ok(Arc::new(builder.finish()))
        }
        DataType::FixedSizeList(item, dimension) if item.data_type() == &DataType::Float32 => {
            let mut builder = FixedSizeListBuilder::new(
                Float32Builder::with_capacity(rows.len() * *dimension as usize),
                *dimension,
            );
            for row in rows {
                append_vector(
                    &mut builder,
                    row.get(name),
                    field.is_nullable(),
                    name,
                    *dimension as usize,
                )?;
            }
            Ok(Arc::new(builder.finish()))
        }
        unsupported => Err(NativeError::invalid(format!(
            "unsupported input field {} type {unsupported:?}",
            name
        ))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Builds a unique temporary directory for marker-only unit tests.
    /// 为仅测试标记的单元测试创建唯一临时目录。
    fn marker_test_directory() -> PathBuf {
        let sequence = MARKER_TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        std::env::temp_dir().join(format!(
            "vmm-native-marker-test-{}-{sequence}",
            std::process::id()
        ))
    }

    /// Creates one schema request that exercises the vector identity path.
    /// 创建一个覆盖向量身份路径的 Schema 请求。
    fn marker_test_request() -> SchemaRequest {
        SchemaRequest {
            table_name: "vectors_2".to_string(),
            columns: vec![
                ColumnSpec {
                    name: "id".to_string(),
                    column_type: "string".to_string(),
                    vector_dim: 0,
                    nullable: false,
                },
                ColumnSpec {
                    name: "vector".to_string(),
                    column_type: "vector_float32".to_string(),
                    vector_dim: 2,
                    nullable: false,
                },
            ],
            overwrite_if_exists: false,
        }
    }

    /// Verifies pending intent survives an atomic marker replacement and is consumed only after registration.
    /// 验证 pending 意图可经原子标记替换持久化，并且仅在登记后被消费。
    #[test]
    fn marker_pending_schema_intent_is_recoverable() {
        let directory = marker_test_directory();
        fs::create_dir_all(&directory).expect("create marker test directory");
        let result = (|| {
            assert!(!prepare_native_directory(&directory)?);
            let request = marker_test_request();
            register_pending_schema_intent(&directory, &request, true)?;
            let marker_path = directory.join(".vmm-native.json");
            let pending = read_native_marker(&marker_path)?;
            assert_eq!(pending.schema_identities.len(), 0);
            assert_eq!(pending.pending_schema_identities.len(), 1);
            assert!(pending.pending_schema_identities[0].allow_create);
            register_schema_identity(&directory, &request)?;
            let completed = read_native_marker(&marker_path)?;
            assert_eq!(completed.schema_identities.len(), 1);
            assert!(completed.pending_schema_identities.is_empty());
            Ok::<(), NativeError>(())
        })();
        let _ = fs::remove_dir_all(&directory);
        result.expect("marker intent should remain recoverable");
    }

    /// Verifies marker writes always leave a complete JSON document at the destination.
    /// 验证标记写入后目标位置始终是完整 JSON 文档。
    #[test]
    fn marker_atomic_write_produces_valid_document() {
        let directory = marker_test_directory();
        fs::create_dir_all(&directory).expect("create marker test directory");
        let result = (|| {
            let marker_path = directory.join(".vmm-native.json");
            let marker = NativeMarker {
                schema_version: 1,
                abi_version: ABI_VERSION,
                engine_version: ENGINE_VERSION.to_string(),
                schema_identities: vec!["vectors_2:test".to_string()],
                pending_schema_identities: Vec::new(),
            };
            write_marker_atomic(&marker_path, &marker)?;
            let decoded = read_native_marker(&marker_path)?;
            assert_eq!(decoded.schema_identities, marker.schema_identities);
            assert!(decoded.pending_schema_identities.is_empty());
            Ok::<(), NativeError>(())
        })();
        let _ = fs::remove_dir_all(&directory);
        result.expect("atomic marker should decode");
    }
}
