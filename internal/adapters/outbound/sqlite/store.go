// store.go implements the SQLite-backed outbound archive adapter.
// store.go 用于实现基于 SQLite 的出站归档适配器。
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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

CREATE TABLE IF NOT EXISTS vmm_noise_embeddings (
  scope TEXT NOT NULL,
  language TEXT NOT NULL,
  category_name TEXT NOT NULL,
  phrase TEXT NOT NULL,
  model TEXT NOT NULL,
  dimension INTEGER NOT NULL,
  rules_hash TEXT NOT NULL,
  vector_json TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (scope, language, category_name, phrase, model, dimension, rules_hash)
);
CREATE INDEX IF NOT EXISTS idx_vmm_noise_embeddings_lookup
  ON vmm_noise_embeddings(scope, language, model, dimension, rules_hash);
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

// LoadNoiseEmbeddingCache reads one fully qualified semantic prototype bundle from SQLite for startup-time reuse.
// LoadNoiseEmbeddingCache 用于从 SQLite 读取一组完整限定的语义原型缓存，以供启动阶段复用。
func (s *Store) LoadNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery) ([]logicdomain.NoiseEmbeddingCacheEntry, error) {
	// Query the exact cache fingerprint so model, dimension, and rules changes always miss cleanly.
	// 按完整缓存指纹查询，确保模型、维度或规则变化时一定会干净失效。
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
FROM vmm_noise_embeddings
WHERE scope = ? AND language = ? AND model = ? AND dimension = ? AND rules_hash = ?
ORDER BY category_name, phrase
`, query.Scope, query.Language, query.Model, query.Dimension, query.RulesHash)
	if err != nil {
		return nil, fmt.Errorf("load noise embedding cache: %w", err)
	}
	defer rows.Close()

	// Decode each cached vector row into the domain cache entry shape expected by the processor.
	// 将每条缓存向量记录解码成处理器期望的领域缓存结构。
	entries := make([]logicdomain.NoiseEmbeddingCacheEntry, 0)
	for rows.Next() {
		var entry logicdomain.NoiseEmbeddingCacheEntry
		var vectorJSON string
		var updatedAt string
		if err := rows.Scan(&entry.Scope, &entry.Language, &entry.CategoryName, &entry.Phrase, &entry.Model, &entry.Dimension, &entry.RulesHash, &vectorJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan noise embedding cache: %w", err)
		}
		if err := json.Unmarshal([]byte(vectorJSON), &entry.Vector); err != nil {
			return nil, fmt.Errorf("decode cached vector: %w", err)
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			entry.UpdatedAt = timestamp
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate noise embedding cache: %w", err)
	}
	return entries, nil
}

// ReplaceNoiseEmbeddingCache refreshes one language bundle in place so stale vectors are pruned when config changes.
// ReplaceNoiseEmbeddingCache 用于原地刷新某个语言包的缓存，从而在配置变化时清理陈旧向量。
func (s *Store) ReplaceNoiseEmbeddingCache(ctx context.Context, query logicdomain.NoiseEmbeddingCacheQuery, entries []logicdomain.NoiseEmbeddingCacheEntry) error {
	// Replace the selected scope/language cache atomically so startup always sees a self-consistent bundle.
	// 以原子方式替换选中作用域和语言的缓存，保证启动时看到的始终是一组自洽数据。
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin noise embedding cache transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `DELETE FROM vmm_noise_embeddings WHERE scope = ? AND language = ?`, query.Scope, query.Language); err != nil {
		return fmt.Errorf("clear stale noise embedding cache: %w", err)
	}
	for _, entry := range entries {
		vectorJSON, marshalErr := json.Marshal(entry.Vector)
		if marshalErr != nil {
			return fmt.Errorf("encode noise embedding cache vector: %w", marshalErr)
		}
		updatedAt := entry.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO vmm_noise_embeddings (
  scope, language, category_name, phrase, model, dimension, rules_hash, vector_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, entry.Scope, entry.Language, entry.CategoryName, entry.Phrase, entry.Model, entry.Dimension, entry.RulesHash, string(vectorJSON), updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert noise embedding cache: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit noise embedding cache transaction: %w", err)
	}
	committed = true
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
