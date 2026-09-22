/*
 * Stable fixed-width C ABI for the VMM native LanceDB cdylib.
 * VMM 原生 LanceDB cdylib 使用的稳定固定宽度 C ABI。
 *
 * This header describes ownership, lifetime, and timeout semantics for every exported symbol.
 * 本头文件说明所有导出符号的所有权、生命周期和超时语义。
 */
#ifndef VMM_LANCEDB_H
#define VMM_LANCEDB_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* The ABI version is checked before any runtime handle is created.
 * ABI 版本必须在创建运行时句柄前完成校验。
 */
#define VMM_LANCEDB_ABI_VERSION UINT32_C(1)

/* Stable status values returned by operations in this header.
 * 本头文件中所有操作返回的稳定状态值。
 */
#define VMM_LANCEDB_STATUS_OK INT32_C(0)
#define VMM_LANCEDB_STATUS_INVALID_ARGUMENT INT32_C(1)
#define VMM_LANCEDB_STATUS_NOT_FOUND INT32_C(2)
#define VMM_LANCEDB_STATUS_SCHEMA_MISMATCH INT32_C(3)
#define VMM_LANCEDB_STATUS_CANCELLED INT32_C(4)
#define VMM_LANCEDB_STATUS_TIMEOUT INT32_C(5)
#define VMM_LANCEDB_STATUS_IO INT32_C(6)
#define VMM_LANCEDB_STATUS_OUTCOME_UNCERTAIN INT32_C(7)
#define VMM_LANCEDB_STATUS_INTERNAL INT32_C(8)

/* Opaque runtime handle owned by the native library until runtime_destroy succeeds.
 * 原生库持有的不透明运行时句柄，直到 runtime_destroy 成功前都必须保持有效。
 */
typedef struct VmmLancedbRuntime VmmLancedbRuntime;

/* A byte buffer allocated by this library and released with bytes_free.
 * 本库分配并必须通过 bytes_free 释放的字节缓冲区。
 *
 * The caller must copy data before calling bytes_free; the pointer, length, and capacity are an
 * inseparable ownership tuple and must be passed back unchanged exactly once.
 * 调用方必须在 bytes_free 前复制数据；指针、长度和容量是不可拆分的所有权元组，必须原样且仅一次传回。
 */
typedef struct VmmLancedbBytes {
    uint8_t* data;
    uint64_t len;
    uint64_t cap;
} VmmLancedbBytes;

/* Returns the ABI version used by this library.
 * 返回本库使用的 ABI 版本。
 */
uint32_t vmm_lancedb_abi_version(void);

/* Returns the exact official LanceDB engine version as a library-owned UTF-8 buffer.
 * 以本库拥有的 UTF-8 缓冲区返回精确的官方 LanceDB 引擎版本。
 */
int32_t vmm_lancedb_engine_version(VmmLancedbBytes* out);

/* Returns the JSON capability document, including L2 and timeout/cancellation semantics.
 * 返回能力 JSON 文档，其中包含 L2 及超时/取消语义。
 */
int32_t vmm_lancedb_capabilities(VmmLancedbBytes* out);

/* Opens one local native database directory and returns an owned runtime handle.
 * 打开一个本地原生数据库目录并返回由本库拥有的运行时句柄。
 *
 * The path is a UTF-8 byte span and is not NUL-terminated; path_len is the exact byte length.
 * 路径是 UTF-8 字节区间而非 NUL 结尾字符串；path_len 是精确的字节长度。
 * timeout_ms bounds startup connection work; zero means no startup timeout.
 * timeout_ms 限制启动连接工作；零表示不设置启动超时。
 */
int32_t vmm_lancedb_runtime_create(
    const uint8_t* path,
    uint64_t path_len,
    uint64_t timeout_ms,
    VmmLancedbRuntime** out);

/* Destroys a runtime after all in-flight calls have completed.
 * 必须在所有进行中的调用完成后销毁运行时。
 */
int32_t vmm_lancedb_runtime_destroy(VmmLancedbRuntime* runtime);

/* Ensures the requested table schema without destructive replacement unless overwrite is set.
 * 确保目标表 Schema；除非 overwrite 置位，否则不执行破坏性替换。
 *
 * schema_json is a UTF-8 JSON byte span matching the VMM create-table contract.
 * schema_json 是符合 VMM 建表契约的 UTF-8 JSON 字节区间。
 */
int32_t vmm_lancedb_create_table(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    const uint8_t* schema_json,
    uint64_t schema_json_len,
    uint8_t overwrite_if_exists,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Performs atomic official LanceDB merge_insert using key_columns_json.
 * 使用 key_columns_json 执行官方 LanceDB 原子 merge_insert。
 *
 * input_format 1 is JSON rows; a timeout may return OUTCOME_UNCERTAIN after commit visibility is unknown.
 * input_format 1 表示 JSON 行；提交可见性未知时超时可能返回 OUTCOME_UNCERTAIN。
 */
int32_t vmm_lancedb_upsert(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    uint32_t input_format,
    const uint8_t* data,
    uint64_t data_len,
    const uint8_t* key_columns_json,
    uint64_t key_columns_len,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Executes explicit L2 nearest-neighbor search with optional engine-side prefiltering.
 * 执行明确的 L2 近邻检索，并支持引擎侧可选预过滤。
 *
 * output_format 2 is JSON rows; vector_len is a count of float32 elements.
 * output_format 2 表示 JSON 行；vector_len 是 float32 元素数量。
 */
int32_t vmm_lancedb_search(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    const float* vector,
    uint64_t vector_len,
    uint32_t limit,
    const uint8_t* filter,
    uint64_t filter_len,
    const uint8_t* vector_column,
    uint64_t vector_column_len,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Deletes rows matching one LanceDB predicate.
 * 删除匹配一个 LanceDB 谓词的行。
 */
int32_t vmm_lancedb_delete(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    const uint8_t* condition,
    uint64_t condition_len,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Counts rows after applying the optional LanceDB predicate.
 * 应用可选 LanceDB 谓词后统计行数。
 */
int32_t vmm_lancedb_count_rows(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    const uint8_t* filter,
    uint64_t filter_len,
    uint64_t timeout_ms,
    uint64_t* out_count);

/* Returns the canonical Arrow schema JSON for one table.
 * 返回一个表的规范 Arrow Schema JSON。
 */
int32_t vmm_lancedb_schema(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Runs official LanceDB maintenance for one table.
 * 对一个表执行官方 LanceDB 维护操作。
 */
int32_t vmm_lancedb_optimize(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    uint64_t timeout_ms);

/* Drops one table through the explicit destructive maintenance operation.
 * 通过明确的破坏性维护操作删除一个表。
 */
int32_t vmm_lancedb_drop_table(
    VmmLancedbRuntime* runtime,
    const uint8_t* table_name,
    uint64_t table_name_len,
    uint64_t timeout_ms,
    VmmLancedbBytes* out);

/* Releases one buffer returned by this library using the exact pointer, length, and capacity.
 * 使用本库返回的原始指针、长度和容量释放一个缓冲区。
 *
 * This function does not accept a VmmLancedbBytes struct by value, so the Windows ABI remains
 * scalar and fixed-width; the tuple must be released on the same loaded library.
 * 本函数不按值接收 VmmLancedbBytes 结构，因此 Windows ABI 保持标量且固定宽度；元组必须由同一个已加载库释放。
 */
void vmm_lancedb_bytes_free(uint8_t* data, uint64_t len, uint64_t cap);

/* Releases one library-owned NUL-terminated C string returned by a string-valued ABI extension.
 * 释放字符串型 ABI 扩展返回的一条由本库拥有的 NUL 结尾 C 字符串。
 */
void vmm_lancedb_string_free(char* value);

/* Returns the latest calling-thread error as a library-owned UTF-8 buffer.
 * 返回当前调用线程最近一次错误的本库拥有 UTF-8 缓冲区。
 */
int32_t vmm_lancedb_last_error_message(VmmLancedbBytes* out);

/* Clears the latest calling-thread error without returning a buffer.
 * 清除当前调用线程最近一次错误且不返回缓冲区。
 */
void vmm_lancedb_clear_last_error(void);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* VMM_LANCEDB_H */
