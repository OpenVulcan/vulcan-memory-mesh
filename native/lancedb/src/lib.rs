//! Stable C ABI for the VMM native LanceDB storage backend.
//! VMM 原生 LanceDB 存储后端的稳定 C ABI。

mod arrow_codec;
mod error;
mod ffi;
mod marker;
mod table;

use arrow_codec::{schema_from_columns, schema_to_json};
use error::NativeError;
use ffi::{
    abi_length, ffi_status, read_bytes, read_json, read_optional_utf8, read_utf8, run_with_timeout,
    runtime_ref, set_last_error, write_bytes, write_handle, write_u64,
};
use lancedb::table::OptimizeAction;
use lancedb::{Connection, connect};
use marker::{
    prepare_native_directory, recover_pending_schema_intents, register_pending_schema_intent,
    register_schema_identity,
};
use serde::{Deserialize, Serialize};
use std::cell::RefCell;
use std::ffi::CString;
use std::os::raw::c_char;
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use table::{delete_table, ensure_table, search_table, upsert_table};

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
