//! Official LanceDB table operations invoked by the stable native ABI.
//! 稳定原生 ABI 调用的官方 LanceDB 表操作，属于存储层。

use super::STATUS_NOT_FOUND;
use super::arrow_codec::{batch_to_rows, rows_to_batch};
use super::error::NativeError;
use arrow_schema::SchemaRef;
use futures_util::TryStreamExt;
use lancedb::query::{ExecutableQuery, QueryBase};
use lancedb::{Connection, DistanceType, Table};
use serde_json::Value;

/// Creates a table when the directory is new, otherwise validates its schema without replacement.
/// 新目录建表，已有表只校验 Schema，不执行替换。
pub(super) async fn ensure_table(
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
pub(super) async fn validate_table_schema(
    table: &Table,
    expected: SchemaRef,
) -> Result<(), NativeError> {
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
pub(super) async fn upsert_table(
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
pub(super) async fn search_table(
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
pub(super) async fn delete_table(table: Table, condition: String) -> Result<Value, NativeError> {
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
