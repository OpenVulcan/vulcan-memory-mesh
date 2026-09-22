// database.go opens one CGo-free SQLite database and owns its serialized native operations.
// database.go 用于打开一个 CGO-free SQLite 数据库，并负责串行化原生操作。
package native_sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/openvulcan/vmm/internal/platform/textutil"
)

const (
	// nativeMarkerTable identifies databases initialized by the native storage backend.
	// nativeMarkerTable 用于识别已经由原生存储后端初始化的数据库。
	nativeMarkerTable = "vmm_native_storage_marker"

	// nativeMarkerVersion is incremented only when the native file format changes incompatibly.
	// nativeMarkerVersion 仅在原生文件格式发生不兼容变更时递增。
	nativeMarkerVersion = 1

	// defaultNativeTimeout bounds operations when the caller does not provide a positive timeout.
	// defaultNativeTimeout 用于在调用方未提供正超时时间时限制原生操作。
	defaultNativeTimeout = 5 * time.Second
)

// Database is the neutral native SQLite handle consumed by the reusable relational store.
// Database 是可复用关系存储使用的中立原生 SQLite 句柄。
type Database struct {
	db           *sql.DB
	databasePath string
	timeout      time.Duration
	tokenizer    *textutil.LexicalTokenizer

	operationGate chan struct{}
	stateMu       sync.RWMutex
	closed        bool
}

// Open creates or reopens a native database after rejecting non-native existing SQLite files.
// Open 在拒绝已有非原生 SQLite 文件后创建或重新打开原生数据库。
func Open(databasePath string, timeout time.Duration) (*Database, error) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, errors.New("native sqlite database path is required")
	}
	if timeout <= 0 {
		timeout = defaultNativeTimeout
	}
	if !isSQLiteMemoryPath(databasePath) {
		// Probe existing files through a percent-encoded read-only URI before opening the writable handle.
		// 对已有文件先使用百分号编码的只读 URI 探针，随后才打开可写句柄。
		if err := inspectExistingDatabaseReadOnly(databasePath, timeout); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
			return nil, fmt.Errorf("create native sqlite database dir: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open native sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	closeDB := func(cause error) (*Database, error) {
		_ = db.Close()
		return nil, cause
	}
	if err := db.PingContext(ctx); err != nil {
		return closeDB(fmt.Errorf("ping native sqlite database: %w", err))
	}

	// Inspect the file before applying PRAGMAs or DDL so an old VLDB database cannot be silently adopted.
	// 在修改 PRAGMA 或执行 DDL 前检查文件，避免静默接管旧 VLDB 数据库。
	conn, err := db.Conn(ctx)
	if err != nil {
		return closeDB(fmt.Errorf("acquire native sqlite connection: %w", err))
	}
	nativeMarkerPresent, existingTables, inspectErr := inspectDatabaseMarker(ctx, conn)
	_ = conn.Close()
	if inspectErr != nil {
		return closeDB(inspectErr)
	}
	if !nativeMarkerPresent && len(existingTables) > 0 {
		return closeDB(fmt.Errorf("native sqlite refuses existing non-native database %q; use offline migration into a new path", databasePath))
	}

	if conn, err = db.Conn(ctx); err != nil {
		return closeDB(fmt.Errorf("acquire native sqlite initialization connection: %w", err))
	}
	if err := initializeConnection(ctx, conn, timeout); err != nil {
		_ = conn.Close()
		return closeDB(err)
	}
	if err := ensureNativeMarker(ctx, conn); err != nil {
		_ = conn.Close()
		return closeDB(err)
	}
	_ = conn.Close()

	tokenizer, err := textutil.NewLexicalTokenizer(textutil.LexicalTokenizerConfig{EnablePreTokenize: true})
	if err != nil {
		return closeDB(fmt.Errorf("initialize native lexical tokenizer: %w", err))
	}
	return &Database{
		db:            db,
		databasePath:  databasePath,
		timeout:       timeout,
		tokenizer:     tokenizer,
		operationGate: make(chan struct{}, 1),
	}, nil
}

// Close waits for the serialized operation to finish and closes the native database idempotently.
// Close 等待串行操作完成后关闭原生数据库，并保证重复调用安全。
func (d *Database) Close() error {
	if d == nil {
		return nil
	}
	d.stateMu.RLock()
	closed := d.closed
	d.stateMu.RUnlock()
	if closed {
		return nil
	}
	d.operationGate <- struct{}{}
	defer func() { <-d.operationGate }()
	d.stateMu.Lock()
	if d.closed {
		d.stateMu.Unlock()
		return nil
	}
	d.closed = true
	db := d.db
	d.db = nil
	d.stateMu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

// CheckHealth verifies the marker, SQLite quick-check result, and native FTS metadata probe.
// CheckHealth 校验原生标记、SQLite quick-check 结果以及原生 FTS 元数据探针。
func (d *Database) CheckHealth(ctx context.Context) error {
	return d.withOperation(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var markerVersion int
		if err := conn.QueryRowContext(ctx, "SELECT format_version FROM "+quoteIdentifier(nativeMarkerTable)+" WHERE id = 1").Scan(&markerVersion); err != nil {
			return fmt.Errorf("read native sqlite marker: %w", err)
		}
		if markerVersion != nativeMarkerVersion {
			return fmt.Errorf("unsupported native sqlite marker version %d", markerVersion)
		}
		var quickCheck string
		if err := conn.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil {
			return fmt.Errorf("run native sqlite quick_check: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(quickCheck), "ok") {
			return fmt.Errorf("native sqlite quick_check returned %q", quickCheck)
		}
		if err := d.checkFTSHealthLocked(ctx, conn); err != nil {
			return err
		}
		return nil
	})
}

// withOperation runs one database operation on the single controlled connection and honors cancellation while waiting.
// withOperation 在单个受控连接上运行一次数据库操作，并在等待期间遵守取消信号。
func (d *Database) withOperation(ctx context.Context, operation func(context.Context, *sql.Conn) error) error {
	return d.withOperationTimeout(ctx, d.timeout, operation)
}

// withMaintenanceOperation runs one serialized operation with the longer FTS maintenance budget.
// withMaintenanceOperation 使用更长的 FTS 维护预算运行一次串行操作。
func (d *Database) withMaintenanceOperation(ctx context.Context, operation func(context.Context, *sql.Conn) error) error {
	return d.withOperationTimeout(ctx, d.maintenanceTimeout(), operation)
}

// withOperationTimeout serializes one connection operation and applies the selected timeout budget.
// withOperationTimeout 串行化单连接操作，并应用调用方选择的超时预算。
func (d *Database) withOperationTimeout(ctx context.Context, timeout time.Duration, operation func(context.Context, *sql.Conn) error) error {
	if d == nil {
		return errors.New("native sqlite database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := boundContext(ctx, timeout)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case d.operationGate <- struct{}{}:
	}
	defer func() { <-d.operationGate }()

	d.stateMu.RLock()
	closed := d.closed
	db := d.db
	d.stateMu.RUnlock()
	if closed || db == nil {
		return errors.New("native sqlite database is closed")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire native sqlite operation connection: %w", err)
	}
	defer conn.Close()
	return operation(ctx, conn)
}

// boundContext applies one timeout without overriding a shorter caller deadline.
// boundContext 应用一次超时，同时不覆盖调用方更短的截止时间。
func boundContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

// maintenanceTimeout returns the durable budget for full FTS initialization and recovery.
// maintenanceTimeout 返回完整 FTS 初始化与恢复使用的持久化维护预算。
func (d *Database) maintenanceTimeout() time.Duration {
	if d != nil && d.timeout > 5*time.Minute {
		return d.timeout
	}
	return 5 * time.Minute
}

// inspectDatabaseMarker reads existing user tables without making any schema or pragma changes.
// inspectDatabaseMarker 只读检查已有用户表，不修改 schema 或 pragma。
func inspectDatabaseMarker(ctx context.Context, conn *sql.Conn) (bool, []string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT GLOB 'sqlite_*' ORDER BY name")
	if err != nil {
		return false, nil, fmt.Errorf("inspect native sqlite schema: %w", err)
	}
	defer rows.Close()
	nativeMarkerPresent := false
	objects := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, nil, fmt.Errorf("scan native sqlite schema object: %w", err)
		}
		if name == nativeMarkerTable {
			nativeMarkerPresent = true
			continue
		}
		objects = append(objects, name)
	}
	if err := rows.Err(); err != nil {
		return false, nil, fmt.Errorf("iterate native sqlite schema: %w", err)
	}
	if nativeMarkerPresent {
		var markerVersion int
		var backend string
		if err := conn.QueryRowContext(ctx, "SELECT format_version, backend FROM "+quoteIdentifier(nativeMarkerTable)+" WHERE id = 1").Scan(&markerVersion, &backend); err != nil {
			return false, nil, fmt.Errorf("inspect native sqlite marker row: %w", err)
		}
		if markerVersion != nativeMarkerVersion || backend != "modernc.org/sqlite" {
			return false, nil, fmt.Errorf("unsupported native sqlite marker version %d backend %q", markerVersion, backend)
		}
	}
	return nativeMarkerPresent, objects, nil
}

// inspectExistingDatabaseReadOnly rejects an existing non-native file before any writable SQLite setup.
// inspectExistingDatabaseReadOnly 在任何可写 SQLite 初始化前拒绝已有的非原生文件。
func inspectExistingDatabaseReadOnly(databasePath string, timeout time.Duration) error {
	info, err := os.Stat(databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect native sqlite database path: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil
	}
	dsn, err := sqliteReadOnlyDSN(databasePath)
	if err != nil {
		return fmt.Errorf("build native sqlite read-only probe URI: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	probe, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open native sqlite read-only probe: %w", err)
	}
	probe.SetMaxOpenConns(1)
	probe.SetMaxIdleConns(1)
	probe.SetConnMaxLifetime(0)
	closeProbe := func(cause error) error {
		if closeErr := probe.Close(); closeErr != nil {
			return errors.Join(cause, fmt.Errorf("close native sqlite read-only probe: %w", closeErr))
		}
		return cause
	}
	if err := probe.PingContext(ctx); err != nil {
		return closeProbe(fmt.Errorf("ping native sqlite read-only probe: %w", err))
	}
	conn, err := probe.Conn(ctx)
	if err != nil {
		return closeProbe(fmt.Errorf("acquire native sqlite read-only probe connection: %w", err))
	}
	nativeMarkerPresent, existingObjects, inspectErr := inspectDatabaseMarker(ctx, conn)
	_ = conn.Close()
	if inspectErr != nil {
		return closeProbe(inspectErr)
	}
	if !nativeMarkerPresent && len(existingObjects) > 0 {
		return closeProbe(fmt.Errorf("native sqlite refuses existing non-native database %q; use offline migration into a new path", databasePath))
	}
	return closeProbe(nil)
}

// sqliteReadOnlyDSN creates a file URI whose path bytes cannot be mistaken for query syntax.
// sqliteReadOnlyDSN 创建文件 URI，避免路径字节被误解析为查询语法。
func sqliteReadOnlyDSN(databasePath string) (string, error) {
	if strings.TrimSpace(databasePath) == "" {
		return "", errors.New("native sqlite read-only probe path is required")
	}
	if isSQLiteMemoryPath(databasePath) {
		return "", errors.New("native sqlite read-only probe does not support memory databases")
	}
	abs, err := filepath.Abs(filepath.Clean(databasePath))
	if err != nil {
		return "", fmt.Errorf("resolve native sqlite read-only probe path: %w", err)
	}
	slashPath := filepath.ToSlash(abs)
	// Windows drive paths need a URI slash before the drive letter.
	// Windows 驱动器路径需要在盘符前补一个 URI 斜杠。
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	probeURI := url.URL{Scheme: "file", Path: slashPath, RawQuery: "mode=ro"}
	return probeURI.String(), nil
}

// initializeConnection applies durable SQLite hardening to the single native connection.
// initializeConnection 为原生单连接应用持久化 SQLite 加固选项。
func initializeConnection(ctx context.Context, conn *sql.Conn, timeout time.Duration) error {
	busyMillis := timeout.Milliseconds()
	if busyMillis < 1 {
		busyMillis = 1
	}
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyMillis),
	}
	for _, pragma := range pragmas {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("initialize native sqlite %s: %w", pragma, err)
		}
	}
	return nil
}

// ensureNativeMarker creates the native ownership marker after the read-only compatibility gate passes.
// ensureNativeMarker 在通过只读兼容门禁后创建原生所有权标记。
func ensureNativeMarker(ctx context.Context, conn *sql.Conn) error {
	statement := `
CREATE TABLE IF NOT EXISTS vmm_native_storage_marker (
  id INTEGER PRIMARY KEY CHECK(id = 1),
  format_version INTEGER NOT NULL,
  backend TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO vmm_native_storage_marker (id, format_version, backend, created_at, updated_at)
VALUES (1, 1, 'modernc.org/sqlite', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  format_version = excluded.format_version,
  backend = excluded.backend,
  updated_at = excluded.updated_at;
`
	if _, err := conn.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("ensure native sqlite marker: %w", err)
	}
	return nil
}

// isSQLiteMemoryPath identifies SQLite URI forms that do not have a filesystem parent directory.
// isSQLiteMemoryPath 判断不具有文件系统父目录的 SQLite URI 形式。
func isSQLiteMemoryPath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return lower == ":memory:" || strings.HasPrefix(lower, "file::memory:") || strings.HasPrefix(lower, "file:memdb")
}

// quoteIdentifier quotes a validated or internally generated SQLite identifier.
// quoteIdentifier 为已校验或内部生成的 SQLite 标识符加引号。
func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// Ensure the neutral contract remains part of this package's public adapter surface at compile time.
// 确保中立契约在编译期仍然属于本包公开适配器表面。
