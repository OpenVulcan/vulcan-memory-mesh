// SQLite execution, parameter binding and retry boundaries used by repository methods.
// SQLite 适配层仓储方法共用的执行、参数绑定与重试边界。
package vldb_sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	sqlitecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
	"math"
	"strings"
	"time"
)

// exec sends one SQL statement or script to the SQLite gateway and prefers native typed params for the sqlite-first runtime path.
// exec 用于把单条 SQL 或脚本发送给 SQLite 网关，并在 sqlite-first 运行路径里优先使用原生强类型参数。
func (s *Store) exec(ctx context.Context, sql string, params ...any) error {
	_, err := s.execResult(ctx, sql, params...)
	return err
}

// execResult sends one SQL statement or script to SQLite and returns the execution metadata for callers that need affected-row checks.
// execResult 用于把单条 SQL 或脚本发送给 SQLite，并把执行元数据返回给需要校验影响行数的调用方。
func (s *Store) execResult(ctx context.Context, sql string, params ...any) (sqlitecontract.ExecuteResult, error) {
	if !s.hasSQLiteStore() {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return sqlitecontract.ExecuteResult{}, err
	}
	resp, err := s.executeScript(ctx, strings.TrimSpace(sql), prepared.Values)
	if err != nil {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite execute: %w", err)
	}
	if !resp.Success {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite execute: %s", strings.TrimSpace(resp.Message))
	}
	return resp, nil
}

// execExpectRowsChanged executes one write and requires SQLite to report the exact affected-row count for state-machine updates.
// execExpectRowsChanged 用于执行一次写入，并要求 SQLite 返回精确影响行数，服务于状态机类更新的命中校验。
func (s *Store) execExpectRowsChanged(ctx context.Context, operation string, expectedRows int64, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		return err
	}
	if resp.RowsChanged != expectedRows {
		return fmt.Errorf("%s affected %d rows, want %d", operation, resp.RowsChanged, expectedRows)
	}
	return nil
}

// sqlitePartialMutationError converts a post-mutation failure into the shared uncertain-outcome contract so upper layers do not add unsafe cleanup or failure writes.
// sqlitePartialMutationError 用于把已发生部分写入后的失败转换为统一的结果不确定契约，避免上层追加不安全的清理或失败写入。
func sqlitePartialMutationError(mutated bool, operation string, err error) error {
	return sqlitePartialMutationErrorWithFreshVectorReference(mutated, false, operation, err)
}

// sqliteAutocommitRowsChangedDriftError classifies a single SQLite autocommit row-count drift as ordinary when no row changed and outcome-uncertain once any row has already been mutated.
// sqliteAutocommitRowsChangedDriftError 用于分类单条 SQLite 自动提交语句的行数漂移：未改变任何行时返回普通错误，已经改变至少一行时返回结果不确定错误。
func sqliteAutocommitRowsChangedDriftError(operation string, messagePrefix string, rowsChanged, expectedRows int64) error {
	if rowsChanged == expectedRows {
		return nil
	}
	err := fmt.Errorf("%s affected %d rows, want %d", messagePrefix, rowsChanged, expectedRows)
	return sqlitePartialMutationError(rowsChanged > 0, operation, err)
}

// sqliteWriteCommitBoundaryError marks SQLite write failures that report an unknown commit boundary while leaving ordinary write failures unchanged.
// sqliteWriteCommitBoundaryError 用于标记报告提交边界未知的 SQLite 写入失败，同时保持普通写入失败不变。
func sqliteWriteCommitBoundaryError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if isSQLiteOutcomeUncertainError(err) {
		return logicdomain.OutcomeUncertainError{
			Operation: operation,
			Message:   err.Error(),
		}
	}
	return err
}

// sqliteWriteCommitOrPartialMutationError preserves explicit commit-boundary ambiguity before using confirmed prior writes to classify ordinary failures.
// sqliteWriteCommitOrPartialMutationError 用于优先保留明确的提交边界不确定语义，再用已确认的前序写入分类普通失败。
func sqliteWriteCommitOrPartialMutationError(mutated bool, operation string, err error) error {
	if err == nil {
		return nil
	}
	if logicdomain.IsOutcomeUncertain(err) {
		return err
	}
	if classifiedErr := sqliteWriteCommitBoundaryError(operation, err); logicdomain.IsOutcomeUncertain(classifiedErr) {
		return classifiedErr
	}
	return sqlitePartialMutationError(mutated, operation, err)
}

// sqlitePartialMutationErrorWithFreshVectorReference marks partial SQLite writes and records whether fresh vector rows may now be referenced by durable memory rows.
// sqlitePartialMutationErrorWithFreshVectorReference 用于标记 SQLite 部分写入，并记录新向量是否可能已经被长期 memory 行引用。
func sqlitePartialMutationErrorWithFreshVectorReference(mutated bool, freshVectorReference bool, operation string, err error) error {
	if err == nil {
		return nil
	}
	if !mutated || logicdomain.IsOutcomeUncertain(err) {
		return err
	}
	return logicdomain.OutcomeUncertainError{
		Operation:            operation,
		Message:              err.Error(),
		FreshVectorReference: freshVectorReference,
	}
}

// sqliteWriteStatement stores one ordered write statement plus its typed parameters.
// sqliteWriteStatement 用于保存顺序写入中的单条语句及其强类型参数。
type sqliteWriteStatement struct {
	// SQL stores one executable SQLite statement without transaction control.
	// SQL 用于保存一条不包含事务控制语句的 SQLite 可执行语句。
	SQL string
	// Params stores the typed values bound to SQL placeholders.
	// Params 用于保存绑定到 SQL 占位符的强类型参数值。
	Params []any
}

// execWriteStatements executes ordered single-statement typed writes without pretending to hold a cross-call SQLite transaction that the real FFI cannot preserve.
// execWriteStatements 用于按顺序执行单语句强类型写入，不伪装成真实 FFI 无法跨调用保留的 SQLite 事务。
func (s *Store) execWriteStatements(ctx context.Context, statements ...sqliteWriteStatement) error {
	if len(statements) == 0 {
		return nil
	}
	for index, statement := range statements {
		if err := s.exec(ctx, statement.SQL, statement.Params...); err != nil {
			return fmt.Errorf("execute sqlite write statement %d: %w", index+1, err)
		}
	}
	return nil
}

// execBatch sends one repeated-shape write workload through SQLite's native ExecuteBatch RPC so sqlite-first storage avoids one-RPC-per-row churn.
// execBatch 用于通过 SQLite 原生 ExecuteBatch RPC 发送同构写入，避免 sqlite-first 存储退化成“一行一次 RPC”。
func (s *Store) execBatch(ctx context.Context, sql string, items [][]any) error {
	_, err := s.execBatchResult(ctx, sql, items)
	return err
}

// execBatchResult sends one repeated-shape write workload and returns SQLite execution metadata for callers that must verify batch row counts.
// execBatchResult 用于发送同构批量写入，并返回 SQLite 执行元数据，供必须校验批量命中行数的调用方使用。
func (s *Store) execBatchResult(ctx context.Context, sql string, items [][]any) (sqlitecontract.ExecuteResult, error) {
	if !s.hasSQLiteStore() {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite store is not initialized")
	}
	if len(items) == 0 {
		return sqlitecontract.ExecuteResult{Success: true}, nil
	}
	batchItems := make([][]sqlitecontract.SQLValue, 0, len(items))
	for idx, item := range items {
		values, err := prepareSQLiteBatchParams(item)
		if err != nil {
			return sqlitecontract.ExecuteResult{}, fmt.Errorf("prepare sqlite batch item %d: %w", idx, err)
		}
		batchItems = append(batchItems, values)
	}
	resp, err := s.executeBatch(ctx, strings.TrimSpace(sql), batchItems)
	if err != nil {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite execute batch: %w", err)
	}
	if !resp.Success {
		return sqlitecontract.ExecuteResult{}, fmt.Errorf("sqlite execute batch: %s", strings.TrimSpace(resp.Message))
	}
	return resp, nil
}

// queryRows decodes one JSON query response into a typed slice so higher-level methods can stay small and explicit while still preferring native sqlite params.
// queryRows 用于把 JSON 查询结果解码为强类型切片，在保持上层逻辑简洁的同时优先使用 sqlite 原生参数。
func queryRows[T any](s *Store, ctx context.Context, sql string, params ...any) ([]T, error) {
	if !s.hasSQLiteStore() {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	resp, err := s.queryJSON(ctx, strings.TrimSpace(sql), prepared.Values)
	if err != nil {
		return nil, fmt.Errorf("sqlite query: %w", err)
	}
	rows := make([]T, 0)
	body := strings.TrimSpace(resp.JSONData)
	if body == "" {
		return rows, nil
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		return nil, fmt.Errorf("decode sqlite query rows: %w", err)
	}
	return rows, nil
}

// sqlitePreparedParams stores the normalized parameter payload that can be attached to ExecuteScript / QueryJson requests.
// sqlitePreparedParams 用于保存标准化后的参数载荷，便于附加到 ExecuteScript / QueryJson 请求。
type sqlitePreparedParams struct {
	Values []sqlitecontract.SQLValue
}

// prepareSQLiteParams converts Go scalar values into the typed SQLite FFI payload expected by the local sqlite-first runtime path.
// prepareSQLiteParams 用于把 Go 标量参数转换成 sqlite-first 本地 FFI 路径期望的强类型载荷。
func prepareSQLiteParams(params []any) (sqlitePreparedParams, error) {
	if len(params) == 0 {
		return sqlitePreparedParams{}, nil
	}
	values := make([]sqlitecontract.SQLValue, 0, len(params))
	for idx, param := range params {
		value, err := toSQLiteValue(param)
		if err != nil {
			return sqlitePreparedParams{}, fmt.Errorf("convert sqlite param %d: %w", idx, err)
		}
		values = append(values, value)
	}
	return sqlitePreparedParams{Values: values}, nil
}

// prepareSQLiteBatchParams converts one ExecuteBatch item into native SQLite FFI values.
// prepareSQLiteBatchParams 用于把单个 ExecuteBatch item 转换成原生 SQLite FFI 值。
func prepareSQLiteBatchParams(params []any) ([]sqlitecontract.SQLValue, error) {
	prepared, err := prepareSQLiteParams(params)
	if err != nil {
		return nil, err
	}
	return prepared.Values, nil
}

// toSQLiteValue maps one Go scalar to the SQLite FFI typed value so the adapter can stay sqlite-native by default.
// toSQLiteValue 用于把单个 Go 标量映射为 SQLite FFI 的强类型值，让适配器默认保持 sqlite 原生风格。
func toSQLiteValue(param any) (sqlitecontract.SQLValue, error) {
	switch value := param.(type) {
	case nil:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueNull}, nil
	case bool:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueBool, Bool: value}, nil
	case string:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueString, String: value}, nil
	case []byte:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueBytes, Bytes: append([]byte(nil), value...)}, nil
	case json.RawMessage:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueString, String: string(value)}, nil
	case int:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case int8:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case int16:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case int32:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case int64:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: value}, nil
	case uint:
		if uint64(value) > math.MaxInt64 {
			return sqlitecontract.SQLValue{}, fmt.Errorf("uint %d overflows sqlite int64 binding", value)
		}
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case uint8:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case uint16:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case uint32:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case uint64:
		if value > math.MaxInt64 {
			return sqlitecontract.SQLValue{}, fmt.Errorf("uint64 %d overflows sqlite int64 binding", value)
		}
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueInt64, Int64: int64(value)}, nil
	case float32:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueFloat64, Float64: float64(value)}, nil
	case float64:
		return sqlitecontract.SQLValue{Kind: sqlitecontract.SQLValueFloat64, Float64: value}, nil
	default:
		return sqlitecontract.SQLValue{}, fmt.Errorf("unsupported sqlite param type %T", param)
	}
}

// executeScript performs one local ExecuteScript FFI call with bounded retry logic for retryable busy/locked/schema errors.
// executeScript 用于执行一次本地 ExecuteScript FFI 调用，并对可重试的 busy/locked/schema 错误做有界重试。
func (s *Store) executeScript(ctx context.Context, sql string, params []sqlitecontract.SQLValue) (sqlitecontract.ExecuteResult, error) {
	var response sqlitecontract.ExecuteResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.ExecuteScript(ctx, sql, params, "")
		return err
	})
	return response, err
}

// executeBatch performs one local ExecuteBatch FFI call with bounded retry logic so SQLITE_BUSY / SQLITE_LOCKED do not immediately bubble up to the caller.
// executeBatch 用于执行一次本地 ExecuteBatch FFI 调用，并对 SQLITE_BUSY / SQLITE_LOCKED 之类错误做有界重试，避免立刻上抛给调用方。
func (s *Store) executeBatch(ctx context.Context, sql string, items [][]sqlitecontract.SQLValue) (sqlitecontract.ExecuteResult, error) {
	var response sqlitecontract.ExecuteResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.ExecuteBatch(ctx, sql, items)
		return err
	})
	return response, err
}

// queryJSON performs one local QueryJSON FFI call with the same retry contract used by writes so retryable SQLITE_SCHEMA / SQLITE_BUSY failures can self-heal.
// queryJSON 用于执行一次本地 QueryJSON FFI 调用，并复用与写请求一致的重试契约，让 SQLITE_SCHEMA / SQLITE_BUSY 这类可重试错误有机会自行恢复。
func (s *Store) queryJSON(ctx context.Context, sql string, params []sqlitecontract.SQLValue) (sqlitecontract.QueryJSONResult, error) {
	var response sqlitecontract.QueryJSONResult
	err := s.withSQLiteRetry(ctx, func() error {
		var err error
		response, err = s.database.QueryJSON(ctx, sql, params, "")
		return err
	})
	return response, err
}

// withSQLiteRetry wraps one local SQLite FFI call so retryable busy/locked/schema errors are retried with a tiny exponential backoff.
// withSQLiteRetry 用于包裹单个本地 SQLite FFI 调用，并对可重试的 busy/locked/schema 错误做一个很小的指数退避重试。
func (s *Store) withSQLiteRetry(ctx context.Context, call func() error) error {
	var lastErr error
	for attempt := 0; attempt < sqliteRetryMaxAttempts; attempt++ {
		if err := checkSQLiteContext(ctx); err != nil {
			return err
		}
		err := call()
		if err == nil {
			// A confirmed commit must remain successful even if cancellation arrives immediately afterwards.
			// 已确认提交成功后，即使紧接着发生取消，也必须保留成功结果，避免调用方重复写入。
			return nil
		}
		lastErr = err
		if !isSQLiteRetryableError(err) || attempt == sqliteRetryMaxAttempts-1 {
			return lastErr
		}
		if err := waitSQLiteRetry(ctx, attempt); err != nil {
			return lastErr
		}
	}
	return lastErr
}

// isSQLiteRetryableError detects retryable SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA errors returned by the local FFI layer.
// isSQLiteRetryableError 用于识别本地 FFI 层返回的 SQLITE_BUSY / SQLITE_LOCKED / SQLITE_SCHEMA 可重试错误。
func isSQLiteRetryableError(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lowered, "sqlite_busy") ||
		strings.Contains(lowered, "sqlite_locked") ||
		strings.Contains(lowered, "sqlite_schema") ||
		strings.Contains(lowered, "database is locked")
}

// waitSQLiteRetry sleeps for a tiny exponential backoff while still honoring the parent context deadline.
// waitSQLiteRetry 用于执行一个很小的指数退避等待，同时继续遵守父 context 的截止时间。
func waitSQLiteRetry(ctx context.Context, attempt int) error {
	delay := sqliteRetryBaseDelay * time.Duration(1<<attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isSQLiteOutcomeUncertainError detects gateway failures whose text indicates the commit result may already have happened even though the RPC returned an error.
// isSQLiteOutcomeUncertainError 用于识别这类网关失败：虽然 RPC 返回报错，但错误文本表明提交结果本身可能已经发生。
func isSQLiteOutcomeUncertainError(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(strings.TrimSpace(err.Error()))
	fragments := []string{
		"resource deadlock would occur",
		"failed to commit",
		"transactioncontext error",
		"commit outcome unknown",
		"outcome uncertain",
	}
	for _, fragment := range fragments {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}
	return false
}

// idRow mirrors one MAX(id) style query used to allocate deterministic numeric ids.
// idRow 用于映射 MAX(id) 这类查询结果，以便分配确定性的数字 ID。
type idRow struct {
	NextID uint64 `json:"next_id"`
}

// nextNumericID allocates the next numeric identifier from one table under the adapter write lock.
// nextNumericID 用于在适配器写锁保护下，从某张表里分配下一个数字 ID。
func (s *Store) nextNumericID(ctx context.Context, table string) (uint64, error) {
	rows, err := queryRows[idRow](s, ctx, fmt.Sprintf(`SELECT COALESCE(MAX(id), 0) + 1 AS next_id FROM %s`, table))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || rows[0].NextID == 0 {
		return 1, nil
	}
	return rows[0].NextID, nil
}

// execInsertOneRow executes one durable create statement and classifies row-count drift by confirmed mutation state.
// execInsertOneRow 用于执行一条长期创建语句，并按已确认写入状态分类行数漂移。
func (s *Store) execInsertOneRow(ctx context.Context, operation string, sql string, params ...any) error {
	resp, err := s.execResult(ctx, sql, params...)
	if err != nil {
		if isSQLiteOutcomeUncertainError(err) {
			return logicdomain.OutcomeUncertainError{
				Operation: operation,
				Message:   err.Error(),
			}
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	return sqliteAutocommitRowsChangedDriftError(operation, operation, resp.RowsChanged, 1)
}

// sqliteInsertCommitUnknownError reports whether one insert failure came from an ambiguous SQLite commit boundary instead of affected-row drift.
// sqliteInsertCommitUnknownError 用于判断一次插入失败是否来自 SQLite 提交边界不明确，而不是影响行数漂移。
func sqliteInsertCommitUnknownError(err error) bool {
	return err != nil && logicdomain.IsOutcomeUncertain(err) && isSQLiteOutcomeUncertainError(err)
}

// sqliteUint64Params converts normalized numeric ids into typed SQLite bind parameters for dynamic IN predicates.
// sqliteUint64Params 用于把规范化后的数字 id 转换为 SQLite 强类型绑定参数，供动态 IN 条件使用。
func sqliteUint64Params(values []uint64) []any {
	if len(values) == 0 {
		return nil
	}
	params := make([]any, 0, len(values))
	for _, value := range values {
		params = append(params, value)
	}
	return params
}

// sqlitePlaceholders renders the exact placeholder list for one dynamic SQLite IN predicate whose values are supplied through typed params.
// sqlitePlaceholders 用于为动态 SQLite IN 条件渲染精确数量的占位符，实际值通过强类型参数传入。
func sqlitePlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for idx := range parts {
		parts[idx] = "?"
	}
	return strings.Join(parts, ",")
}
