// SQLite full-text index synchronization after durable memory writes.
// SQLite 适配层长期记忆写入后的全文索引同步。
package vldb_sqlite

import (
	"context"
	"fmt"
	"github.com/openvulcan/vmm/internal/adapters/outbound/storageutil"
	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
	sqlitecontract "github.com/openvulcan/vmm/internal/platform/storagecontract/sqlite"
	"strconv"
	"strings"
)

// ensureBuiltinMemoryFTS ensures the built-in SQLite FTS index exists and rebuilds it when the configured tokenizer mode changes.
// ensureBuiltinMemoryFTS 用于确保内建 SQLite FTS 索引存在，并在配置的分词模式发生变化时触发重建。
func (s *Store) ensureBuiltinMemoryFTS(ctx context.Context) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if s == nil || s.database == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	result, err := s.database.EnsureFtsIndex(ctx, s.ftsIndexName, s.tokenizerMode)
	if err != nil {
		return fmt.Errorf("ensure sqlite builtin fts index: %w", err)
	}
	if err := sqliteFTSEnsureResultError("ensure sqlite builtin fts index", result); err != nil {
		return err
	}
	if result.TokenizerMode != s.tokenizerMode {
		rebuildResult, err := s.database.RebuildFtsIndex(ctx, s.ftsIndexName, s.tokenizerMode)
		if err != nil {
			return fmt.Errorf("rebuild sqlite builtin fts index: %w", err)
		}
		if err := sqliteFTSRebuildResultError("rebuild sqlite builtin fts index", rebuildResult); err != nil {
			return err
		}
	}
	return checkSQLiteContext(ctx)
}

// sqliteFTSEnsureResultError converts an unsuccessful ensure result into a boundary error before callers trust tokenizer metadata.
// sqliteFTSEnsureResultError 用于在调用方信任分词器元数据前，把 ensure 返回的失败结果位转换为边界错误。
func sqliteFTSEnsureResultError(stage string, result sqlitecontract.EnsureFtsIndexResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// sqliteFTSRebuildResultError converts an unsuccessful rebuild result into a boundary error so full-index recovery cannot report success on a failed FFI result.
// sqliteFTSRebuildResultError 用于把 rebuild 返回的失败结果位转换为边界错误，避免全量索引恢复在 FFI 失败时被误判为成功。
func sqliteFTSRebuildResultError(stage string, result sqlitecontract.RebuildFtsIndexResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// sqliteFTSMutationResultError converts an unsuccessful incremental mutation result into a boundary error before the relational/FTS write is considered synchronized.
// sqliteFTSMutationResultError 用于在关系表与 FTS 写入被视为同步前，把增量变更返回的失败结果位转换为边界错误。
func sqliteFTSMutationResultError(stage string, result sqlitecontract.FtsMutationResult) error {
	if result.Success {
		return nil
	}
	return fmt.Errorf("%s reported unsuccessful result", strings.TrimSpace(stage))
}

// syncMemoryFTSAfterWrite synchronizes inserted and deleted memory rows into the built-in SQLite FTS index and falls back to a full rebuild when incremental sync fails.
// syncMemoryFTSAfterWrite 用于把插入与删除的记忆行同步到内建 SQLite FTS 索引，并在增量同步失败时回退到全量重建。
func (s *Store) syncMemoryFTSAfterWrite(ctx context.Context, inserted []logicdomain.MemoryNodeRecord, deletedMemoryIDs []uint64) error {
	if err := checkSQLiteContext(ctx); err != nil {
		return err
	}
	if s == nil || s.database == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	if len(inserted) == 0 && len(deletedMemoryIDs) == 0 {
		return nil
	}
	for _, record := range inserted {
		result, err := s.database.UpsertFtsDocument(
			ctx,
			s.ftsIndexName,
			s.tokenizerMode,
			strconv.FormatUint(record.ID, 10),
			record.VectorID,
			strings.TrimSpace(record.Abstract),
			strings.TrimSpace(record.Details),
		)
		if err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, fmt.Errorf("upsert sqlite memory fts document %d: %w", record.ID, err))
		}
		if err := sqliteFTSMutationResultError(fmt.Sprintf("upsert sqlite memory fts document %d", record.ID), result); err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, err)
		}
	}
	for _, memoryID := range storageutil.NormalizeUint64List(deletedMemoryIDs) {
		result, err := s.database.DeleteFtsDocument(ctx, s.ftsIndexName, strconv.FormatUint(memoryID, 10))
		if err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, fmt.Errorf("delete sqlite memory fts document %d: %w", memoryID, err))
		}
		if err := sqliteFTSMutationResultError(fmt.Sprintf("delete sqlite memory fts document %d", memoryID), result); err != nil {
			return s.rebuildMemoryFTSWithFallback(ctx, err)
		}
	}
	return checkSQLiteContext(ctx)
}

// rebuildMemoryFTSWithFallback rebuilds the full built-in FTS index so relational/FTS consistency can self-heal after one incremental mutation fails.
// rebuildMemoryFTSWithFallback 用于重建整个内建 FTS 索引，让关系表与 FTS 在单次增量同步失败后能够自愈。
func (s *Store) rebuildMemoryFTSWithFallback(ctx context.Context, cause error) error {
	result, err := s.database.RebuildFtsIndex(ctx, s.ftsIndexName, s.tokenizerMode)
	if err != nil {
		return fmt.Errorf("%w; rebuild sqlite builtin fts index also failed: %v", cause, err)
	}
	if err := sqliteFTSRebuildResultError("rebuild sqlite builtin fts index", result); err != nil {
		return fmt.Errorf("%w; rebuild sqlite builtin fts index also failed: %v", cause, err)
	}
	return cause
}
