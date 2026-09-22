// sql.go preserves the established ExecuteScript, ExecuteBatch, and QueryJSON semantics on database/sql.
// sql.go 用于在 database/sql 上保持既有 ExecuteScript、ExecuteBatch 与 QueryJSON 语义。
package native_sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	storagecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
)

// ExecuteScript runs a multi-statement script without parameters or one parameterized statement.
// ExecuteScript 执行无参数多语句脚本，或执行带参数的单条语句。
func (d *Database) ExecuteScript(ctx context.Context, sqlText string, params []storagecontract.SQLValue, paramsJSON string) (storagecontract.ExecuteResult, error) {
	if strings.TrimSpace(sqlText) == "" {
		return storagecontract.ExecuteResult{}, errors.New("sql must not be empty")
	}
	values, err := resolveSQLParams(params, paramsJSON)
	if err != nil {
		return storagecontract.ExecuteResult{}, err
	}
	statementCount := countSQLStatements(sqlText)
	if len(values) > 0 && statementCount > 1 {
		return storagecontract.ExecuteResult{}, errors.New("flat params or params_json are only supported for a single SQL statement")
	}
	var result storagecontract.ExecuteResult
	err = d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var execResult sql.Result
		if len(values) == 0 {
			execResult, err = conn.ExecContext(ctx, sqlText)
		} else {
			execResult, err = conn.ExecContext(ctx, sqlText, values...)
		}
		if err != nil {
			if rollbackErr := d.rollbackExplicitTransaction(conn, sqlText); rollbackErr != nil {
				return fmt.Errorf("sqlite execute script: %w; rollback explicit transaction: %v", err, rollbackErr)
			}
			if containsSQLKeyword(sqlText, "COMMIT") && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nativeSQLiteCommitError("sqlite execute script", err)
			}
			return fmt.Errorf("sqlite execute script: %w", err)
		}
		if scriptMayLeaveTransactionOpen(sqlText) {
			if rollbackErr := d.rollbackExplicitTransaction(conn, sqlText); rollbackErr != nil {
				return fmt.Errorf("sqlite execute script left explicit transaction open: %w", rollbackErr)
			}
		}
		result = executionResult(execResult, statementCount, len(values) == 0)
		return nil
	})
	if err != nil {
		return storagecontract.ExecuteResult{}, err
	}
	return result, nil
}

// ExecuteBatch runs one parameterized SQL statement repeatedly in one explicit transaction.
// ExecuteBatch 在一个显式事务中重复执行一条带参数 SQL 语句。
func (d *Database) ExecuteBatch(ctx context.Context, sqlText string, items [][]storagecontract.SQLValue) (storagecontract.ExecuteResult, error) {
	if strings.TrimSpace(sqlText) == "" {
		return storagecontract.ExecuteResult{}, errors.New("sql must not be empty")
	}
	if len(items) == 0 {
		return storagecontract.ExecuteResult{}, errors.New("items must not be empty")
	}
	if countSQLStatements(sqlText) > 1 {
		return storagecontract.ExecuteResult{}, errors.New("execute_batch only supports a single SQL statement")
	}

	var result storagecontract.ExecuteResult
	err := d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite begin batch transaction: %w", err)
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()
		stmt, err := tx.PrepareContext(ctx, sqlText)
		if err != nil {
			return fmt.Errorf("sqlite prepare batch statement: %w", err)
		}
		defer stmt.Close()
		var rowsChanged int64
		var lastInsertRowID int64
		for index, item := range items {
			values, err := sqlValuesToDriverValues(item)
			if err != nil {
				return fmt.Errorf("prepare sqlite batch item %d: %w", index, err)
			}
			execResult, err := stmt.ExecContext(ctx, values...)
			if err != nil {
				return fmt.Errorf("sqlite execute batch item %d: %w", index, err)
			}
			changed, changedErr := execResult.RowsAffected()
			if changedErr != nil {
				return fmt.Errorf("read sqlite batch rows changed for item %d: %w", index, changedErr)
			}
			rowsChanged = saturatingAddInt64(rowsChanged, changed)
			if id, idErr := execResult.LastInsertId(); idErr == nil {
				lastInsertRowID = id
			}
		}
		if err := tx.Commit(); err != nil {
			return nativeSQLiteCommitError("sqlite commit batch transaction", err)
		}
		committed = true
		result = storagecontract.ExecuteResult{
			Success:            true,
			Message:            fmt.Sprintf("batch executed successfully (statements_executed=%d rows_changed=%d)", len(items), rowsChanged),
			RowsChanged:        rowsChanged,
			LastInsertRowID:    lastInsertRowID,
			StatementsExecuted: int64(len(items)),
		}
		return nil
	})
	if err != nil {
		return storagecontract.ExecuteResult{}, err
	}
	return result, nil
}

// QueryJSON executes one query and returns rows using the same scalar-to-JSON mapping as the FFI backend.
// QueryJSON 执行一条查询，并保持与 FFI 后端相同的标量到 JSON 映射。
func (d *Database) QueryJSON(ctx context.Context, sqlText string, params []storagecontract.SQLValue, paramsJSON string) (storagecontract.QueryJSONResult, error) {
	if strings.TrimSpace(sqlText) == "" {
		return storagecontract.QueryJSONResult{}, errors.New("sql must not be empty")
	}
	if countSQLStatements(sqlText) > 1 {
		return storagecontract.QueryJSONResult{}, errors.New("query_json only supports a single SQL statement")
	}
	values, err := resolveSQLParams(params, paramsJSON)
	if err != nil {
		return storagecontract.QueryJSONResult{}, err
	}
	var result storagecontract.QueryJSONResult
	err = d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, sqlText, values...)
		if err != nil {
			return fmt.Errorf("sqlite query: %w", err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			return fmt.Errorf("read sqlite query columns: %w", err)
		}
		jsonRows := make([]map[string]any, 0)
		for rows.Next() {
			values := make([]any, len(columns))
			destinations := make([]any, len(columns))
			for index := range values {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				return fmt.Errorf("scan sqlite query row: %w", err)
			}
			row := make(map[string]any, len(columns))
			for index, column := range columns {
				row[column] = sqliteValueToJSON(values[index])
			}
			jsonRows = append(jsonRows, row)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate sqlite query rows: %w", err)
		}
		body, err := json.Marshal(jsonRows)
		if err != nil {
			return fmt.Errorf("serialize sqlite query rows: %w", err)
		}
		result = storagecontract.QueryJSONResult{JSONData: string(body), RowCount: uint64(len(jsonRows))}
		return nil
	})
	if err != nil {
		return storagecontract.QueryJSONResult{}, err
	}
	return result, nil
}

// nativeSQLiteCommitError preserves commit-boundary uncertainty when cancellation races a native commit.
// nativeSQLiteCommitError 在取消与原生提交竞争时保留提交边界不确定语义。
func nativeSQLiteCommitError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: commit outcome unknown: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// resolveSQLParams chooses exactly one supported parameter representation and converts it to driver values.
// resolveSQLParams 选择唯一一种受支持的参数表示，并将其转换为驱动参数值。
func resolveSQLParams(params []storagecontract.SQLValue, paramsJSON string) ([]any, error) {
	if len(params) > 0 && strings.TrimSpace(paramsJSON) != "" {
		return nil, errors.New("provide either flat params or params_json, but not both")
	}
	if len(params) > 0 {
		return sqlValuesToDriverValues(params)
	}
	if strings.TrimSpace(paramsJSON) == "" {
		return nil, nil
	}
	var rawValues []json.RawMessage
	if err := json.Unmarshal([]byte(paramsJSON), &rawValues); err != nil {
		return nil, fmt.Errorf("params_json must be a JSON array of scalar values: %w", err)
	}
	values := make([]any, 0, len(rawValues))
	for index, raw := range rawValues {
		value, err := jsonRawValueToDriverValue(raw)
		if err != nil {
			return nil, fmt.Errorf("parse params_json item %d: %w", index, err)
		}
		values = append(values, value)
	}
	return values, nil
}

// sqlValuesToDriverValues maps neutral SQL values to database/sql driver values without lossy integer conversion.
// sqlValuesToDriverValues 将中立 SQL 值映射为 database/sql 驱动值，并避免整数有损转换。
func sqlValuesToDriverValues(values []storagecontract.SQLValue) ([]any, error) {
	out := make([]any, 0, len(values))
	for index, value := range values {
		switch value.Kind {
		case storagecontract.SQLValueNull:
			out = append(out, nil)
		case storagecontract.SQLValueInt64:
			out = append(out, value.Int64)
		case storagecontract.SQLValueFloat64:
			if math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0) {
				return nil, fmt.Errorf("parameter %d contains non-finite float", index)
			}
			out = append(out, value.Float64)
		case storagecontract.SQLValueString:
			out = append(out, value.String)
		case storagecontract.SQLValueBytes:
			out = append(out, append([]byte(nil), value.Bytes...))
		case storagecontract.SQLValueBool:
			if value.Bool {
				out = append(out, int64(1))
			} else {
				out = append(out, int64(0))
			}
		default:
			return nil, fmt.Errorf("parameter %d has unsupported SQL value kind %d", index, value.Kind)
		}
	}
	return out, nil
}

// jsonRawValueToDriverValue maps one JSON scalar to a SQLite driver value.
// jsonRawValueToDriverValue 将一个 JSON 标量映射为 SQLite 驱动值。
func jsonRawValueToDriverValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON scalar: %w", err)
	}
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case bool:
		if typed {
			return int64(1), nil
		}
		return int64(0), nil
	case string:
		return typed, nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		floating, err := typed.Float64()
		if err != nil || math.IsNaN(floating) || math.IsInf(floating, 0) {
			return nil, errors.New("JSON number is outside SQLite numeric range")
		}
		return floating, nil
	default:
		return nil, errors.New("params_json only supports scalar JSON values (null, bool, number, string)")
	}
}

// executionResult normalizes database/sql result metadata to the shared execute result contract.
// executionResult 将 database/sql 结果元数据归一到共享执行结果契约。
func executionResult(result sql.Result, statementCount int, script bool) storagecontract.ExecuteResult {
	rowsChanged, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		rowsChanged = 0
	}
	lastInsertRowID, idErr := result.LastInsertId()
	if idErr != nil {
		lastInsertRowID = 0
	}
	if script {
		return storagecontract.ExecuteResult{
			Success:            true,
			Message:            "script executed successfully",
			RowsChanged:        rowsChanged,
			LastInsertRowID:    lastInsertRowID,
			StatementsExecuted: int64(statementCount),
		}
	}
	return storagecontract.ExecuteResult{
		Success:            true,
		Message:            fmt.Sprintf("statement executed successfully (rows_changed=%d)", rowsChanged),
		RowsChanged:        rowsChanged,
		LastInsertRowID:    lastInsertRowID,
		StatementsExecuted: 1,
	}
}

// sqliteValueToJSON preserves SQLite integers and represents blobs as byte arrays like the Rust gateway.
// sqliteValueToJSON 保持 SQLite 整数精度，并像 Rust 网关一样把 blob 表示为字节数组。
func sqliteValueToJSON(value any) any {
	switch typed := value.(type) {
	case nil, string, bool, int64, float64:
		return typed
	case int:
		return int64(typed)
	case []byte:
		bytes := make([]int, len(typed))
		for index, value := range typed {
			bytes[index] = int(value)
		}
		return bytes
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// saturatingAddInt64 prevents an impossible driver overflow from wrapping batch row counts negative.
// saturatingAddInt64 防止驱动异常导致批量行数溢出并回绕为负数。
func saturatingAddInt64(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

// rollbackExplicitTransaction clears a transaction with an independent cleanup context and invalidates a connection if cleanup is uncertain.
// rollbackExplicitTransaction 使用独立清理上下文回滚事务，并在清理结果不确定时使连接失效。
func (d *Database) rollbackExplicitTransaction(conn *sql.Conn, sqlText string) error {
	if !containsSQLKeyword(sqlText, "BEGIN") {
		return nil
	}
	cleanupCtx := context.Background()
	cancel := func() {}
	if d != nil && d.timeout > 0 {
		cleanupCtx, cancel = context.WithTimeout(cleanupCtx, d.timeout)
	}
	defer cancel()
	if _, err := conn.ExecContext(cleanupCtx, "ROLLBACK"); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no transaction") {
			return nil
		}
		discardErr := conn.Raw(func(any) error {
			return driver.ErrBadConn
		})
		if discardErr != nil {
			return errors.Join(err, discardErr)
		}
		return err
	}
	return nil
}

// scriptMayLeaveTransactionOpen identifies explicit transaction scripts that need a post-call rollback guard.
// scriptMayLeaveTransactionOpen 识别需要在调用结束后执行回滚保护的显式事务脚本。
func scriptMayLeaveTransactionOpen(sqlText string) bool {
	// Any BEGIN token is sufficient to request an independent cleanup attempt.
	// 任何 BEGIN 标记都足以触发独立清理尝试。
	return containsSQLKeyword(sqlText, "BEGIN")
}

// containsSQLKeyword scans SQL tokens while ignoring quoted text and comments used as data.
// containsSQLKeyword 扫描 SQL 标记，并忽略作为数据出现的引号文本与注释。
func containsSQLKeyword(sqlText, keyword string) bool {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	if keyword == "" {
		return false
	}
	chars := []rune(sqlText)
	token := make([]rune, 0, len(keyword))
	flush := func() bool {
		matched := strings.EqualFold(string(token), keyword)
		token = token[:0]
		return matched
	}
	for index := 0; index < len(chars); {
		switch chars[index] {
		case '\'', '"', '`':
			if flush() {
				return true
			}
			quote := chars[index]
			index++
			for index < len(chars) {
				if chars[index] == quote {
					if index+1 < len(chars) && chars[index+1] == quote {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
		case '-':
			if index+1 < len(chars) && chars[index+1] == '-' {
				if flush() {
					return true
				}
				index += 2
				for index < len(chars) && chars[index] != '\n' {
					index++
				}
			} else {
				if flush() {
					return true
				}
				index++
			}
		case '/':
			if index+1 < len(chars) && chars[index+1] == '*' {
				if flush() {
					return true
				}
				index += 2
				for index+1 < len(chars) && !(chars[index] == '*' && chars[index+1] == '/') {
					index++
				}
				if index+1 < len(chars) {
					index += 2
				}
			} else {
				if flush() {
					return true
				}
				index++
			}
		default:
			if unicode.IsLetter(chars[index]) || unicode.IsDigit(chars[index]) || chars[index] == '_' {
				token = append(token, unicode.ToUpper(chars[index]))
				index++
				continue
			}
			if flush() {
				return true
			}
			index++
		}
	}
	return flush()
}

// countSQLStatements counts effective statements while ignoring quoted literals and SQL comments.
// countSQLStatements 统计有效 SQL 语句数量，同时忽略引号字面量和 SQL 注释。
func countSQLStatements(sqlText string) int {
	chars := []rune(sqlText)
	count := 0
	hasContent := false
	for index := 0; index < len(chars); {
		switch chars[index] {
		case '\'':
			hasContent = true
			index++
			for index < len(chars) {
				if chars[index] == '\'' {
					if index+1 < len(chars) && chars[index+1] == '\'' {
						index += 2
					} else {
						index++
						break
					}
				} else {
					index++
				}
			}
		case '"', '`':
			quote := chars[index]
			hasContent = true
			index++
			for index < len(chars) {
				if chars[index] == quote {
					if index+1 < len(chars) && chars[index+1] == quote {
						index += 2
					} else {
						index++
						break
					}
				} else {
					index++
				}
			}
		case '-', '/':
			if index+1 < len(chars) && ((chars[index] == '-' && chars[index+1] == '-') || (chars[index] == '/' && chars[index+1] == '*')) {
				if chars[index] == '-' {
					index += 2
					for index < len(chars) && chars[index] != '\n' {
						index++
					}
				} else {
					index += 2
					for index+1 < len(chars) && !(chars[index] == '*' && chars[index+1] == '/') {
						index++
					}
					if index+1 < len(chars) {
						index += 2
					}
				}
			} else {
				hasContent = true
				index++
			}
		case ';':
			if hasContent {
				count++
			}
			hasContent = false
			index++
		default:
			if !isSQLWhitespace(chars[index]) {
				hasContent = true
			}
			index++
		}
	}
	if hasContent {
		count++
	}
	return count
}

// isSQLWhitespace keeps statement counting aligned with SQLite's whitespace handling.
// isSQLWhitespace 让语句计数与 SQLite 的空白处理保持一致。
func isSQLWhitespace(value rune) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}
