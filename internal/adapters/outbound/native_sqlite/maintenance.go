// maintenance.go implements transactional cleanup of native FTS derived state.
// maintenance.go 用于在同一事务内清理原生 FTS 派生状态。
package native_sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ResetFTSAndExecute drops every native FTS object together with the caller's business cleanup script.
// ResetFTSAndExecute 在调用方业务清理脚本中一并删除全部原生 FTS 对象。
func (d *Database) ResetFTSAndExecute(ctx context.Context, businessScript string) error {
	if strings.TrimSpace(businessScript) == "" {
		return errors.New("native sqlite cleanup script is required")
	}
	return d.withMaintenanceOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		cleanupSQL, err := nativeFTSCleanupSQL(ctx, conn)
		if err != nil {
			return err
		}
		combined, err := injectNativeFTSCleanup(businessScript, cleanupSQL)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, combined); err != nil {
			if rollbackErr := d.rollbackExplicitTransaction(conn, combined); rollbackErr != nil {
				return fmt.Errorf("execute native sqlite cleanup: %w; rollback: %v", err, rollbackErr)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nativeSQLiteCommitError("execute native sqlite cleanup", err)
			}
			return fmt.Errorf("execute native sqlite cleanup: %w", err)
		}
		return nil
	})
}

// nativeFTSCleanupSQL renders safe drops for all native FTS generations and auxiliary triggers.
// nativeFTSCleanupSQL 为全部原生 FTS 代次表和辅助触发器生成安全删除语句。
func nativeFTSCleanupSQL(ctx context.Context, conn *sql.Conn) (string, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT type, name, COALESCE(sql, '')
FROM sqlite_master
WHERE (type = 'trigger' AND name GLOB 'trg_vmm_native_fts_*')
   OR (type = 'table' AND name GLOB 'vmm_native_fts_*')
ORDER BY type DESC, name ASC`)
	if err != nil {
		return "", fmt.Errorf("inspect native fts cleanup objects: %w", err)
	}
	defer rows.Close()
	triggers := make([]string, 0)
	virtualTables := make([]string, 0)
	for rows.Next() {
		var objectType, name, definition string
		if err := rows.Scan(&objectType, &name, &definition); err != nil {
			return "", fmt.Errorf("scan native fts cleanup object: %w", err)
		}
		if objectType == "trigger" {
			triggers = append(triggers, name)
			continue
		}
		if strings.Contains(strings.ToUpper(definition), "USING FTS5") {
			virtualTables = append(virtualTables, name)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate native fts cleanup objects: %w", err)
	}
	var builder strings.Builder
	for _, name := range triggers {
		builder.WriteString("DROP TRIGGER IF EXISTS ")
		builder.WriteString(quoteIdentifier(name))
		builder.WriteString(";\n")
	}
	for _, name := range virtualTables {
		builder.WriteString("DROP TABLE IF EXISTS ")
		builder.WriteString(quoteIdentifier(name))
		builder.WriteString(";\n")
	}
	// These names are fixed auxiliary tables; IF EXISTS keeps cleanup idempotent on partially initialized files.
	// 这些名称是固定辅助表；IF EXISTS 让部分初始化文件也能幂等清理。
	builder.WriteString("DROP TABLE IF EXISTS ")
	builder.WriteString(quoteIdentifier(nativeFTSMetadataTable))
	builder.WriteString(";\nDROP TABLE IF EXISTS ")
	builder.WriteString(quoteIdentifier(nativeFTSQueueTable))
	builder.WriteString(";\n")
	return builder.String(), nil
}

// injectNativeFTSCleanup inserts derived-state drops before the business COMMIT boundary.
// injectNativeFTSCleanup 将派生状态删除语句插入业务 COMMIT 边界之前。
func injectNativeFTSCleanup(businessScript, cleanupSQL string) (string, error) {
	chars := []rune(businessScript)
	commitStart, found := lastSQLKeywordPosition(chars, "COMMIT")
	if !found {
		if containsSQLKeyword(businessScript, "BEGIN") {
			return "", errors.New("native sqlite cleanup script with BEGIN must contain COMMIT")
		}
		return "BEGIN IMMEDIATE;\n" + businessScript + "\n" + cleanupSQL + "COMMIT;", nil
	}
	return string(chars[:commitStart]) + cleanupSQL + string(chars[commitStart:]), nil
}

// lastSQLKeywordPosition finds the last standalone keyword outside SQL literals and comments.
// lastSQLKeywordPosition 查找 SQL 字面量和注释之外的最后一个独立关键字。
func lastSQLKeywordPosition(chars []rune, keyword string) (int, bool) {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	last := -1
	for index := 0; index < len(chars); {
		switch chars[index] {
		case '\'', '"', '`':
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
				index += 2
				for index < len(chars) && chars[index] != '\n' {
					index++
				}
			} else {
				index++
			}
		case '/':
			if index+1 < len(chars) && chars[index+1] == '*' {
				index += 2
				for index+1 < len(chars) && !(chars[index] == '*' && chars[index+1] == '/') {
					index++
				}
				if index+1 < len(chars) {
					index += 2
				}
			} else {
				index++
			}
		default:
			if !isSQLIdentifierRune(chars[index]) {
				index++
				continue
			}
			start := index
			for index < len(chars) && isSQLIdentifierRune(chars[index]) {
				index++
			}
			if strings.EqualFold(string(chars[start:index]), keyword) {
				last = start
			}
		}
	}
	return last, last >= 0
}

// isSQLIdentifierRune matches the token characters used by SQLite keyword scans.
// isSQLIdentifierRune 匹配 SQLite 关键字扫描所使用的标记字符。
func isSQLIdentifierRune(value rune) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
