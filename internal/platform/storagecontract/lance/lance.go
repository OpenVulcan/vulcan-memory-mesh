// Package lance defines the backend-neutral LanceDB contract shared by legacy and native adapters.
// Package lance 定义旧适配器与原生适配器共用的中立 LanceDB 契约。
package lance

import "context"

// InputFormat identifies the wire representation accepted by a vector write.
// InputFormat 标识向量写入所接受的线表示形式。
type InputFormat int32

const (
	// InputFormatJSONRows represents a JSON array of row objects.
	// InputFormatJSONRows 表示 JSON 行对象数组。
	InputFormatJSONRows InputFormat = 1
)

// OutputFormat identifies the wire representation returned by a vector query.
// OutputFormat 标识向量查询返回的线表示形式。
type OutputFormat int32

const (
	// OutputFormatJSONRows represents a JSON array of result row objects.
	// OutputFormatJSONRows 表示 JSON 结果行对象数组。
	OutputFormatJSONRows OutputFormat = 2
)

// CreateTableColumn describes one table field without depending on an engine SDK.
// CreateTableColumn 描述一个不依赖具体引擎 SDK 的表字段。
type CreateTableColumn struct {
	// Name is the physical field name.
	// Name 表示物理字段名称。
	Name string `json:"name"`
	// ColumnType is the contract type token such as string, int64, or vector_float32.
	// ColumnType 表示 string、int64、vector_float32 等契约类型标记。
	ColumnType string `json:"column_type"`
	// VectorDim is required for vector_float32 fields.
	// VectorDim 表示 vector_float32 字段所需的维度。
	VectorDim uint32 `json:"vector_dim,omitempty"`
	// Nullable records whether the engine accepts null values for this field.
	// Nullable 表示引擎是否接受此字段的空值。
	Nullable bool `json:"nullable"`
}

// CreateTableRequest describes one idempotent table creation or schema check.
// CreateTableRequest 描述一次幂等建表或 Schema 检查请求。
type CreateTableRequest struct {
	// TableName is the logical table name.
	// TableName 表示逻辑表名。
	TableName string `json:"table_name"`
	// Columns contains the expected physical schema.
	// Columns 表示期望的物理 Schema。
	Columns []CreateTableColumn `json:"columns"`
	// OverwriteIfExists enables an explicit destructive replacement.
	// OverwriteIfExists 表示是否显式允许破坏性替换已存在的表。
	OverwriteIfExists bool `json:"overwrite_if_exists"`
}

// CreateTableResult reports the idempotent table operation outcome.
// CreateTableResult 报告幂等建表操作结果。
type CreateTableResult struct {
	// Success reports whether the requested state is present.
	// Success 表示请求的目标状态是否已经存在。
	Success bool `json:"success"`
	// Message gives a short engine-neutral diagnostic.
	// Message 提供简短的中立诊断信息。
	Message string `json:"message"`
}

// UpsertResult reports the native engine's merge-insert accounting.
// UpsertResult 报告原生引擎 merge-insert 的计数结果。
type UpsertResult struct {
	// Version is the committed table version when the engine exposes one.
	// Version 表示引擎提供时的已提交表版本。
	Version uint64 `json:"version"`
	// InputRows is the number of source rows.
	// InputRows 表示输入源行数。
	InputRows uint64 `json:"input_rows"`
	// InsertedRows is the number of new rows.
	// InsertedRows 表示新增行数。
	InsertedRows uint64 `json:"inserted_rows"`
	// UpdatedRows is the number of rows replaced by merge-insert.
	// UpdatedRows 表示 merge-insert 替换的行数。
	UpdatedRows uint64 `json:"updated_rows"`
	// DeletedRows is the number of rows removed by the operation.
	// DeletedRows 表示本次操作删除的行数。
	DeletedRows uint64 `json:"deleted_rows"`
}

// SearchResult contains a typed output format marker and owned result bytes.
// SearchResult 包含类型化输出格式标记和由调用方拥有的结果字节。
type SearchResult struct {
	// Format identifies the result representation.
	// Format 表示结果表示形式。
	Format OutputFormat
	// Rows is the number of result rows.
	// Rows 表示结果行数。
	Rows uint64
	// Data contains the encoded result rows.
	// Data 表示编码后的结果行数据。
	Data []byte
}

// DeleteRequest describes one predicate deletion.
// DeleteRequest 描述一次谓词删除。
type DeleteRequest struct {
	// TableName is the target table.
	// TableName 表示目标表。
	TableName string `json:"table_name"`
	// Condition is a validated engine predicate.
	// Condition 表示已验证的引擎谓词。
	Condition string `json:"condition"`
}

// DeleteResult reports a deletion result and its affected row count.
// DeleteResult 报告删除结果及受影响行数。
type DeleteResult struct {
	// Success reports whether deletion completed.
	// Success 表示删除是否完成。
	Success bool `json:"success"`
	// Message gives a short engine-neutral diagnostic.
	// Message 提供简短的中立诊断信息。
	Message string `json:"message"`
	// Version is the committed version when available.
	// Version 表示可用时的已提交版本。
	Version uint64 `json:"version"`
	// DeletedRows is the affected row count.
	// DeletedRows 表示受影响行数。
	DeletedRows uint64 `json:"deleted_rows"`
}

// DropTableRequest describes one explicit table drop.
// DropTableRequest 描述一次显式删表。
type DropTableRequest struct {
	// TableName is the table to drop.
	// TableName 表示待删除的表。
	TableName string `json:"table_name"`
}

// DropTableResult reports one table drop operation.
// DropTableResult 报告一次删表操作。
type DropTableResult struct {
	// Success reports whether the table is absent after the operation.
	// Success 表示操作后目标表是否已经不存在。
	Success bool `json:"success"`
	// Message gives a short engine-neutral diagnostic.
	// Message 提供简短的中立诊断信息。
	Message string `json:"message"`
}

// Engine is the smallest business-facing vector backend contract.
// Engine 是业务侧使用的最小向量后端契约。
type Engine interface {
	// CreateTable ensures the requested schema exists.
	// CreateTable 确保请求的 Schema 存在。
	CreateTable(context.Context, CreateTableRequest) (CreateTableResult, error)
	// VectorUpsertRaw performs an atomic key-based merge insert.
	// VectorUpsertRaw 执行基于键的原子 merge insert。
	VectorUpsertRaw(context.Context, string, InputFormat, []byte, []string) (UpsertResult, error)
	// VectorSearchF32 performs a prefiltered float32 vector search.
	// VectorSearchF32 执行带预过滤的 float32 向量检索。
	VectorSearchF32(context.Context, string, []float32, uint32, string, string, OutputFormat) (SearchResult, error)
	// Delete removes rows matching the validated predicate.
	// Delete 删除符合已验证谓词的行。
	Delete(context.Context, DeleteRequest) (DeleteResult, error)
	// DropTable explicitly removes one table.
	// DropTable 显式删除一个表。
	DropTable(context.Context, DropTableRequest) (DropTableResult, error)
	// Close releases backend-owned handles.
	// Close 释放后端持有的句柄。
	Close() error
}

// HealthEngine exposes optional native health and maintenance capabilities.
// HealthEngine 暴露可选的原生健康与维护能力。
type HealthEngine interface {
	// CheckHealth verifies the connection and required table state.
	// CheckHealth 校验连接及必要表状态。
	CheckHealth(context.Context) error
	// CountRows counts rows after applying an engine predicate.
	// CountRows 在应用引擎谓词后统计行数。
	CountRows(context.Context, string, string) (uint64, error)
	// Schema returns the engine's canonical schema JSON.
	// Schema 返回引擎的规范 Schema JSON。
	Schema(context.Context, string) ([]byte, error)
	// Optimize runs the backend's supported maintenance operation.
	// Optimize 执行后端支持的维护操作。
	Optimize(context.Context, string) error
}
