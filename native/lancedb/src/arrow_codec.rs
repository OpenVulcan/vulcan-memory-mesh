//! Strict JSON and Arrow conversion at the native storage read/write boundary.
//! 原生存储读写边界的严格 JSON 与 Arrow 转换。

use super::ColumnSpec;
use super::error::NativeError;
use arrow_array::builder::{
    BooleanBuilder, FixedSizeListBuilder, Float32Builder, Float64Builder, Int32Builder,
    Int64Builder, StringBuilder, UInt32Builder, UInt64Builder,
};
use arrow_array::{
    Array, ArrayRef, BooleanArray, FixedSizeListArray, Float32Array, Float64Array, Int32Array,
    Int64Array, RecordBatch, StringArray, UInt32Array, UInt64Array,
};
use arrow_schema::{DataType, Field, Schema, SchemaRef};
use serde_json::{Map, Number, Value};
use std::sync::Arc;

/// Converts the neutral column contract into an Arrow schema.
/// 把中立列契约转换成 Arrow Schema。
pub(super) fn schema_from_columns(columns: &[ColumnSpec]) -> Result<SchemaRef, NativeError> {
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

/// Encodes one Arrow schema into the stable JSON schema representation.
/// 把一个 Arrow Schema 编码为稳定的 JSON Schema 表示。
pub(super) fn schema_to_json(schema: &SchemaRef) -> Result<Value, NativeError> {
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
pub(super) fn batch_to_rows(
    schema: &SchemaRef,
    batch: &RecordBatch,
) -> Result<Vec<Value>, NativeError> {
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
pub(super) fn rows_to_batch(
    data: &[u8],
    schema: SchemaRef,
) -> Result<(RecordBatch, u64), NativeError> {
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
