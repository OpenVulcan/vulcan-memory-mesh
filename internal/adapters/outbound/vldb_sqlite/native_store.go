// native_store.go exposes the relational Store on top of the modernc native SQLite backend.
// native_store.go 在 modernc 原生 SQLite 后端之上暴露现有关系存储，并复用完整业务 schema。
package vldb_sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	native_sqlite "github.com/openvulcan/vmm/internal/adapters/outbound/native_sqlite"
	sqlitecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
)

// nativeSQLiteHealthChecker is the health surface implemented only by the native database handle.
// nativeSQLiteHealthChecker 是仅由原生数据库句柄实现的健康检查接口。
type nativeSQLiteHealthChecker interface {
	CheckHealth(context.Context) error
}

// nativeSQLiteFTSRebuilder is the maintenance surface for rebuilding the derived native FTS index.
// nativeSQLiteFTSRebuilder 是重建原生派生 FTS 索引的维护接口。
type nativeSQLiteFTSRebuilder interface {
	RebuildFtsIndex(context.Context, string, sqlitecontract.TokenizerMode) (sqlitecontract.RebuildFtsIndexResult, error)
}

// NewNativeStore opens a new-path native SQLite store and initializes the same relational schema used by the existing adapter.
// NewNativeStore 打开新路径原生 SQLite 存储，并初始化现有适配器使用的完整关系 schema。
func NewNativeStore(databasePath string, timeout time.Duration, options ...StoreOptions) (*Store, error) {
	if strings.TrimSpace(databasePath) == "" {
		return nil, fmt.Errorf("native sqlite database path is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	storeOptions := StoreOptions{TokenizerMode: "gse"}
	if len(options) > 0 {
		storeOptions = options[0]
	}
	tokenizerMode, err := parseNativeSQLiteTokenizerMode(storeOptions.TokenizerMode)
	if err != nil {
		return nil, err
	}
	database, err := native_sqlite.Open(databasePath, timeout)
	if err != nil {
		return nil, err
	}
	store := &Store{
		database:      database,
		timeout:       timeout,
		tokenizerMode: tokenizerMode,
		ftsIndexName:  memoryFTSIndexName,
		skipDebugSeed: storeOptions.SkipDebugSeed,
	}
	if err := store.init(context.Background()); err != nil {
		_ = store.Shutdown(context.Background())
		return nil, fmt.Errorf("initialize native sqlite store: %w", err)
	}
	return store, nil
}

// CheckHealth verifies the native marker, SQLite integrity, and ready FTS metadata before serving requests.
// CheckHealth 在承载请求前校验原生标记、SQLite 完整性及已就绪 FTS 元数据。
func (s *Store) CheckHealth(ctx context.Context) error {
	if s == nil || s.database == nil {
		return fmt.Errorf("native sqlite store is not initialized")
	}
	checker, ok := s.database.(nativeSQLiteHealthChecker)
	if !ok {
		return fmt.Errorf("sqlite store does not expose native health checks")
	}
	return checker.CheckHealth(ctx)
}

// RebuildFTSIndex reconstructs the native derived index from relational source rows and returns its durable result.
// RebuildFTSIndex 从关系源表重建原生派生索引，并返回持久化重建结果。
func (s *Store) RebuildFTSIndex(ctx context.Context) error {
	if s == nil || s.database == nil {
		return fmt.Errorf("native sqlite store is not initialized")
	}
	rebuilder, ok := s.database.(nativeSQLiteFTSRebuilder)
	if !ok {
		return fmt.Errorf("sqlite store does not expose native FTS maintenance")
	}
	result, err := rebuilder.RebuildFtsIndex(ctx, s.ftsIndexName, s.tokenizerMode)
	if err != nil {
		return fmt.Errorf("rebuild native sqlite FTS index: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("rebuild native sqlite FTS index reported unsuccessful result")
	}
	if result.TokenizerMode != s.tokenizerMode {
		return fmt.Errorf("rebuild native sqlite FTS index returned tokenizer mode %d, expected %d", result.TokenizerMode, s.tokenizerMode)
	}
	return nil
}

// parseNativeSQLiteTokenizerMode maps native configuration without reusing the legacy Jieba enum value.
// parseNativeSQLiteTokenizerMode 解析原生配置，避免复用旧 Jieba 枚举值掩盖不同索引语义。
func parseNativeSQLiteTokenizerMode(mode string) (sqlitecontract.TokenizerMode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "gse":
		return sqlitecontract.TokenizerGSE, nil
	case "none", "unicode61":
		return sqlitecontract.TokenizerNone, nil
	case "jieba":
		return sqlitecontract.TokenizerNone, fmt.Errorf("native sqlite does not support legacy jieba tokenizer; use gse or unicode61 / 原生 SQLite 不支持旧 jieba 分词器，请使用 gse 或 unicode61")
	default:
		return sqlitecontract.TokenizerNone, fmt.Errorf("unsupported native sqlite tokenizer mode: %s", mode)
	}
}
