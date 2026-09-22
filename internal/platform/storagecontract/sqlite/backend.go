// backend.go defines the neutral SQLite value and result contracts shared by native and FFI adapters.
// backend.go 用于定义原生与 FFI SQLite 适配器共享的中立参数和值结果契约。
package sqlite

// TokenizerMode identifies the lexical mode used by an SQLite FTS backend.
// TokenizerMode 表示 SQLite FTS 后端使用的词法模式。
type TokenizerMode int32

const (
	// TokenizerNone selects SQLite unicode61 tokenization without GSE pre-tokenization.
	// TokenizerNone 表示使用 SQLite unicode61 分词，并关闭 GSE 预分词。
	TokenizerNone TokenizerMode = 0

	// TokenizerJieba selects the legacy VLDB tokenizer ABI value.
	// TokenizerJieba 表示旧 VLDB 分词器 ABI 值。
	TokenizerJieba TokenizerMode = 1

	// TokenizerGSE selects native SQLite pre-tokenization with the embedded GSE dictionary.
	// TokenizerGSE 表示原生 SQLite 使用内嵌 GSE 词典预分词。
	TokenizerGSE TokenizerMode = 2
)

// SQLValueKind identifies the scalar representation accepted by SQLite execution adapters.
// SQLValueKind 表示 SQLite 执行适配器接受的标量参数类型。
type SQLValueKind int32

const (
	// SQLValueNull represents a SQL NULL parameter.
	// SQLValueNull 表示 SQL NULL 参数。
	SQLValueNull SQLValueKind = 0
	// SQLValueInt64 represents a signed 64-bit integer parameter.
	// SQLValueInt64 表示有符号 64 位整数参数。
	SQLValueInt64 SQLValueKind = 1
	// SQLValueFloat64 represents a double-precision floating-point parameter.
	// SQLValueFloat64 表示双精度浮点参数。
	SQLValueFloat64 SQLValueKind = 2
	// SQLValueString represents a UTF-8 text parameter.
	// SQLValueString 表示 UTF-8 文本参数。
	SQLValueString SQLValueKind = 3
	// SQLValueBytes represents an opaque byte-array parameter.
	// SQLValueBytes 表示不透明字节数组参数。
	SQLValueBytes SQLValueKind = 4
	// SQLValueBool represents a boolean parameter encoded as an SQLite integer.
	// SQLValueBool 表示编码为 SQLite 整数的布尔参数。
	SQLValueBool SQLValueKind = 5
)

// SQLValue carries one typed SQL parameter without coupling callers to a driver package.
// SQLValue 携带一个强类型 SQL 参数，同时避免调用方依赖具体驱动包。
type SQLValue struct {
	Kind    SQLValueKind
	Int64   int64
	Float64 float64
	String  string
	Bytes   []byte
	Bool    bool
}

// ExecuteResult reports the outcome of one script or batch execution.
// ExecuteResult 描述一次脚本或批量执行的结果。
type ExecuteResult struct {
	Success            bool
	Message            string
	RowsChanged        int64
	LastInsertRowID    int64
	StatementsExecuted int64
}

// QueryJSONResult contains a JSON array of rows and its exact row count.
// QueryJSONResult 包含 JSON 行数组及其精确行数。
type QueryJSONResult struct {
	JSONData string
	RowCount uint64
}

// EnsureFtsIndexResult reports FTS initialization and the effective tokenizer mode.
// EnsureFtsIndexResult 描述 FTS 初始化结果及实际采用的分词模式。
type EnsureFtsIndexResult struct {
	Success       bool
	TokenizerMode TokenizerMode
}

// RebuildFtsIndexResult reports a completed FTS rebuild and its document count.
// RebuildFtsIndexResult 描述一次完成的 FTS 重建及文档数量。
type RebuildFtsIndexResult struct {
	Success       bool
	TokenizerMode TokenizerMode
	ReindexedRows uint64
}

// FtsMutationResult reports one incremental FTS document mutation.
// FtsMutationResult 描述一次增量 FTS 文档变更。
type FtsMutationResult struct {
	Success      bool
	AffectedRows uint64
}

// SearchHit is one normalized BM25 result returned by an SQLite FTS backend.
// SearchHit 表示 SQLite FTS 后端返回的一条标准化 BM25 结果。
type SearchHit struct {
	ID             string
	FilePath       string
	Title          string
	TitleHighlight string
	ContentSnippet string
	Score          float64
	Rank           uint64
	RawScore       float64
}

// SearchResult contains ordered FTS hits together with source metadata.
// SearchResult 包含有序 FTS 命中及其来源元数据。
type SearchResult struct {
	Total     uint64
	Source    string
	QueryMode string
	Hits      []SearchHit
}
