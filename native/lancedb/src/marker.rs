//! Native storage identity and durable schema-intent recovery for runtime startup.
//! 原生存储层的身份标记及持久化 Schema 意图，服务于运行时启动恢复。

use super::arrow_codec::schema_from_columns;
use super::error::NativeError;
use super::table::validate_table_schema;
use super::{ABI_VERSION, ENGINE_VERSION, STATUS_NOT_FOUND, SchemaRequest};
use lancedb::Connection;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};

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

/// Prepares and validates the native marker before connecting to a database directory.
/// 在连接数据库目录前准备并校验原生标记。
pub(super) fn prepare_native_directory(path: &Path) -> Result<bool, NativeError> {
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
pub(super) fn register_pending_schema_intent(
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
pub(super) fn recover_pending_schema_intents(
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
pub(super) fn register_schema_identity(
    path: &Path,
    request: &SchemaRequest,
) -> Result<(), NativeError> {
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ColumnSpec;
    use std::path::PathBuf;

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
