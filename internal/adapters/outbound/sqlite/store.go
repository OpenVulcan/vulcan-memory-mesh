// store.go implements the SQLite-backed outbound archive adapter.
// store.go 用于实现基于 SQLite 的出站归档适配器。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

const (
	driverName = "sqlite"
	schemaSQL  = `
CREATE TABLE IF NOT EXISTS vmm_memories (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vmm_memories_session_id ON vmm_memories(session_id);
`
)

// Store is the SQLite adapter used by the /chat archive flow to persist scrubbed messages.
// Store 用于作为 /chat 归档流程的 SQLite 适配器，持久化已脱敏消息。
type Store struct {
	db *sql.DB
}

// NewStore opens the SQLite database and ensures the archive schema exists before serving traffic.
// NewStore 用于打开 SQLite 数据库，并在提供服务前确保归档表结构已存在。
func NewStore(path string) (*Store, error) {
	dsn, err := normalizeDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// SaveMemory persists one scrubbed chat message without ever storing the raw sensitive source text.
// SaveMemory 用于持久化一条已脱敏聊天消息，绝不写入原始敏感文本。
func (s *Store) SaveMemory(ctx context.Context, record logicdomain.ArchivedMemory) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vmm_memories (id, session_id, content, created_at) VALUES (?, ?, ?, ?)`,
		record.ID,
		record.SessionID,
		record.Content,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert archived memory: %w", err)
	}
	return nil
}

// Shutdown closes the SQLite handle when the application shuts down.
// Shutdown 用于在应用关闭时关闭 SQLite 连接句柄。
func (s *Store) Shutdown(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// init creates tables and indexes lazily when the store boots.
// init 用于在存储启动时惰性创建表和索引。
func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	return nil
}

// normalizeDSN converts a filesystem path into a sqlite DSN and ensures the parent directory exists.
// normalizeDSN 用于把文件系统路径转换成 sqlite DSN，并确保父目录存在。
func normalizeDSN(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("sqlite path is required")
	}
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return path, nil
	}
	cleaned := filepath.Clean(path)
	dir := filepath.Dir(cleaned)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create sqlite data dir: %w", err)
		}
	}
	return cleaned, nil
}
