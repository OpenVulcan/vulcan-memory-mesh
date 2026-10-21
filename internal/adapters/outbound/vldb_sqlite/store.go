// SQLite adapter construction and handle lifecycle for runtime storage.
// SQLite 适配层运行时存储的构造与句柄生命周期。
package vldb_sqlite

import (
	"context"
	"errors"
	"fmt"
	"github.com/openvulcan/vmm/internal/platform/ffi/sqliteffi"
	sqlitecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store is the SQLite local-FFI adapter used for hierarchy metadata, session/turn persistence, cache storage, and built-in FTS.
// Store 用于作为 SQLite 本地 FFI 适配器，承接层级元数据、session/turn 持久化、缓存存储以及内建 FTS。
type Store struct {
	lib           *sqliteffi.Library
	runtime       *sqliteffi.Runtime
	database      sqliteDatabaseHandle
	timeout       time.Duration
	writeMu       sync.Mutex
	shutdownMu    sync.Mutex
	tokenizerMode sqlitecontract.TokenizerMode
	ftsIndexName  string
	// skipDebugSeed prevents migration targets from receiving local debug hierarchy rows.
	// skipDebugSeed 防止迁移目标被写入本地调试层级种子行。
	skipDebugSeed bool
}

// sqliteDatabaseHandle narrows the SQLite FFI surface that the adapter depends on so unit tests can replace the local database handle with one focused in-memory fake.
// sqliteDatabaseHandle 用于收窄适配器依赖的 SQLite FFI 能力面，这样单测就可以把本地数据库句柄替换成一个聚焦的内存 fake。
type sqliteDatabaseHandle interface {
	ExecuteScript(ctx context.Context, sql string, params []sqlitecontract.SQLValue, paramsJSON string) (sqlitecontract.ExecuteResult, error)
	ExecuteBatch(ctx context.Context, sql string, items [][]sqlitecontract.SQLValue) (sqlitecontract.ExecuteResult, error)
	QueryJSON(ctx context.Context, sql string, params []sqlitecontract.SQLValue, paramsJSON string) (sqlitecontract.QueryJSONResult, error)
	EnsureFtsIndex(ctx context.Context, indexName string, mode sqlitecontract.TokenizerMode) (sqlitecontract.EnsureFtsIndexResult, error)
	RebuildFtsIndex(ctx context.Context, indexName string, mode sqlitecontract.TokenizerMode) (sqlitecontract.RebuildFtsIndexResult, error)
	UpsertFtsDocument(ctx context.Context, indexName string, mode sqlitecontract.TokenizerMode, id string, filePath string, title string, content string) (sqlitecontract.FtsMutationResult, error)
	DeleteFtsDocument(ctx context.Context, indexName string, id string) (sqlitecontract.FtsMutationResult, error)
	SearchFts(ctx context.Context, indexName string, mode sqlitecontract.TokenizerMode, query string, limit uint32, offset uint32) (sqlitecontract.SearchResult, error)
	Close() error
}

// hasSQLiteStore reports whether the adapter currently holds one local SQLite database handle.
// hasSQLiteStore 用于判断适配器当前是否持有一个本地 SQLite 数据库句柄。
func (s *Store) hasSQLiteStore() bool {
	return s != nil && s.database != nil
}

// StoreOptions collects optional SQLite adapter features that change local FTS behavior without affecting unrelated callers.
// StoreOptions 用于收集会改变本地 FTS 行为、但不会影响其他调用方的 SQLite 适配器可选特性。
type StoreOptions struct {
	TokenizerMode string
	// SkipDebugSeed leaves a newly bootstrapped native target empty for offline migration import.
	// SkipDebugSeed 让新启动的原生迁移目标保持空库，以便离线迁移导入完整业务数据。
	SkipDebugSeed bool
}

// NewStore opens the packaged SQLite dynamic library and ensures the local schema and built-in FTS index are initialized before serving traffic.
// NewStore 用于打开打包后的 SQLite 动态库，并在对外提供服务前确保本地表结构和内建 FTS 索引已经初始化。
func NewStore(libraryPath string, databasePath string, timeout time.Duration, options ...StoreOptions) (*Store, error) {
	if strings.TrimSpace(libraryPath) == "" {
		return nil, fmt.Errorf("sqlite library path is required")
	}
	if strings.TrimSpace(databasePath) == "" {
		return nil, fmt.Errorf("sqlite database path is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	storeOptions := StoreOptions{TokenizerMode: "jieba"}
	if len(options) > 0 {
		storeOptions = options[0]
	}

	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite database dir: %w", err)
	}
	lib, err := sqliteffi.Open(strings.TrimSpace(libraryPath))
	if err != nil {
		return nil, err
	}
	runtimeHandle, err := lib.CreateRuntime()
	if err != nil {
		_ = lib.Close()
		return nil, fmt.Errorf("create sqlite runtime: %w", err)
	}
	databaseHandle, err := runtimeHandle.OpenDatabase(strings.TrimSpace(databasePath))
	if err != nil {
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	tokenizerMode, err := parseSQLiteTokenizerMode(storeOptions.TokenizerMode)
	if err != nil {
		_ = databaseHandle.Close()
		_ = runtimeHandle.Close()
		_ = lib.Close()
		return nil, err
	}

	store := &Store{
		lib:           lib,
		runtime:       runtimeHandle,
		database:      databaseHandle,
		timeout:       timeout,
		tokenizerMode: tokenizerMode,
		ftsIndexName:  memoryFTSIndexName,
	}
	if err := store.init(context.Background()); err != nil {
		_ = store.Shutdown(context.Background())
		return nil, err
	}
	return store, nil
}

// Shutdown closes the opened database, runtime, and dynamic-library handles used by the SQLite local-FFI adapter.
// Shutdown 用于关闭 SQLite 本地 FFI 适配器所使用的数据库、运行时和动态库句柄。
func (s *Store) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	var closeErr error
	var ctxErr error
	if ctx == nil {
		ctx = context.Background()
	} else {
		select {
		case <-ctx.Done():
			ctxErr = ctx.Err()
		default:
		}
	}
	if s.database != nil {
		closeErr = s.database.Close()
	}
	if s.runtime != nil {
		if err := s.runtime.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		s.runtime = nil
	}
	if s.lib != nil {
		if err := s.lib.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
		s.lib = nil
	}
	if ctxErr != nil && closeErr != nil {
		return errors.Join(ctxErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return ctxErr
}

// init ensures the SQLite schema is bootstrapped or incrementally migrated before the store starts serving traffic.
// init 用于在存储开始提供服务前，确保 SQLite schema 已完成初始化或增量迁移。
func (s *Store) init(ctx context.Context) error {
	if err := s.ensureSQLiteSchema(ctx); err != nil {
		return err
	}
	return s.ensureBuiltinMemoryFTS(ctx)
}

// parseSQLiteTokenizerMode maps the config string into the SQLite FFI tokenizer enum.
// parseSQLiteTokenizerMode 用于把配置字符串映射为 SQLite FFI 分词枚举。
func parseSQLiteTokenizerMode(mode string) (sqlitecontract.TokenizerMode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "jieba":
		return sqlitecontract.TokenizerJieba, nil
	case "none":
		return sqlitecontract.TokenizerNone, nil
	default:
		return sqlitecontract.TokenizerNone, fmt.Errorf("unsupported sqlite tokenizer mode: %s", mode)
	}
}

// checkSQLiteContext returns the current context error when the caller has already cancelled the operation.
// checkSQLiteContext 用于在调用方已取消操作时返回当前 context 错误。
func checkSQLiteContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
