//! Stable internal errors shared by the native ABI and storage modules.
//! 原生 ABI 与存储模块共用的稳定内部错误定义。

use super::{
    STATUS_INTERNAL, STATUS_INVALID_ARGUMENT, STATUS_IO, STATUS_NOT_FOUND,
    STATUS_OUTCOME_UNCERTAIN, STATUS_SCHEMA_MISMATCH, STATUS_TIMEOUT,
};

/// Internal error carrying a stable ABI classification and human-readable text.
/// 携带稳定 ABI 分类和可读文本的内部错误。
#[derive(Debug)]
pub(super) struct NativeError {
    /// ABI status classification.
    /// ABI 状态分类。
    pub(super) code: i32,
    /// Diagnostic text.
    /// 诊断文本。
    pub(super) message: String,
}

impl NativeError {
    /// Creates a table-not-found error with a stable status code.
    /// 创建表不存在错误并返回稳定的状态码。
    pub(super) fn not_found(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_NOT_FOUND,
            message: message.into(),
        }
    }

    /// Creates an invalid-argument error.
    /// 创建参数无效错误。
    pub(super) fn invalid(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_INVALID_ARGUMENT,
            message: message.into(),
        }
    }

    /// Creates an internal error.
    /// 创建内部错误。
    pub(super) fn internal(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_INTERNAL,
            message: message.into(),
        }
    }

    /// Creates an uncertain-mutation error.
    /// 创建写操作结果不确定错误。
    pub(super) fn outcome_uncertain(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_OUTCOME_UNCERTAIN,
            message: message.into(),
        }
    }

    /// Creates a timeout error for a read or a bounded operation.
    /// 创建读取或有界操作的超时错误。
    pub(super) fn timeout(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_TIMEOUT,
            message: message.into(),
        }
    }

    /// Creates an I/O error.
    /// 创建 I/O 错误。
    pub(super) fn io(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_IO,
            message: message.into(),
        }
    }

    /// Creates a schema mismatch error.
    /// 创建 Schema 不匹配错误。
    pub(super) fn schema(message: impl Into<String>) -> Self {
        Self {
            code: STATUS_SCHEMA_MISMATCH,
            message: message.into(),
        }
    }

    /// Reports whether this error is a timeout.
    /// 报告此错误是否属于超时。
    pub(super) fn is_timeout(&self) -> bool {
        self.code == STATUS_TIMEOUT
    }
}

impl NativeError {
    /// Converts an official LanceDB error into the stable ABI classification.
    /// 把官方 LanceDB 错误转换成稳定 ABI 分类。
    pub(super) fn from_lancedb(error: lancedb::Error) -> Self {
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
