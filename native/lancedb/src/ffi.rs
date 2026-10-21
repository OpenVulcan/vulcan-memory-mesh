//! C ABI buffer validation, panic containment, and bounded runtime execution.
//! C ABI 边界的缓冲区校验、panic 隔离与有界运行时执行。

use super::error::NativeError;
use super::{LAST_ERROR, STATUS_OK, VmmLancedbBytes, VmmLancedbRuntime};
use serde::Deserialize;
use std::panic::{AssertUnwindSafe, catch_unwind};

/// Executes one ABI operation while keeping Rust panics inside the library boundary.
/// 执行一次 ABI 操作并把 Rust panic 限制在库边界内。
pub(super) fn ffi_status<F>(operation: F) -> i32
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
pub(super) fn set_last_error(error: NativeError) -> i32 {
    let code = error.code;
    LAST_ERROR.with(|slot| *slot.borrow_mut() = error.message);
    code
}

/// Converts a fixed-width ABI length into the current platform size.
/// 将固定宽度 ABI 长度转换为当前平台的 usize。
pub(super) fn abi_length(value: u64, name: &str) -> Result<usize, NativeError> {
    usize::try_from(value)
        .map_err(|_| NativeError::invalid(format!("{name} length exceeds this platform")))
}

/// Reads a fixed-length UTF-8 string from the C ABI.
/// 从 C ABI 读取固定长度 UTF-8 字符串。
pub(super) fn read_utf8(
    pointer: *const u8,
    length: u64,
    name: &str,
) -> Result<String, NativeError> {
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
pub(super) fn read_optional_utf8(
    pointer: *const u8,
    length: u64,
    name: &str,
) -> Result<String, NativeError> {
    if length == 0 {
        return Ok(String::new());
    }
    read_utf8(pointer, length, name)
}

/// Reads a fixed-length byte slice from the C ABI.
/// 从 C ABI 读取固定长度字节切片。
pub(super) fn read_bytes(
    pointer: *const u8,
    length: u64,
    name: &str,
) -> Result<Vec<u8>, NativeError> {
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
pub(super) fn read_json<T: for<'de> Deserialize<'de>>(
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
pub(super) unsafe fn write_bytes(
    out: *mut VmmLancedbBytes,
    mut bytes: Vec<u8>,
) -> Result<(), NativeError> {
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
pub(super) unsafe fn write_handle(
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
pub(super) unsafe fn write_u64(out: *mut u64, value: u64) -> Result<(), NativeError> {
    if out.is_null() {
        return Err(NativeError::invalid("count output is null"));
    }
    unsafe { *out = value };
    Ok(())
}

/// Borrows a non-null runtime pointer for one synchronous ABI operation.
/// 为一次同步 ABI 操作借用非空运行时指针。
pub(super) unsafe fn runtime_ref<'a>(
    runtime: *mut VmmLancedbRuntime,
) -> Result<&'a VmmLancedbRuntime, NativeError> {
    unsafe { runtime.as_ref() }.ok_or_else(|| NativeError::invalid("runtime is null"))
}

/// Runs one future with an optional millisecond timeout.
/// 使用可选的毫秒超时运行一个 future。
pub(super) fn run_with_timeout<T, F>(
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
