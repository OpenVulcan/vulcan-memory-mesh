// memory_unified_stub.go keeps the DuckDB adapter interface-complete while the unified memory-table migration is landing on the SQLite-first mainline.
// memory_unified_stub.go 用于在统一记忆表迁移优先落地到 SQLite 主线期间，让 DuckDB 适配器先保持接口完整。
package vldb_duckdb

import (
	"context"
	"fmt"
	"time"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// LoadRecentDirectMemoryWrites returns no exclusion-window writes until the DuckDB adapter finishes the unified memory-table migration.
// LoadRecentDirectMemoryWrites 用于在 DuckDB 适配器完成统一记忆表迁移前，先返回空的排斥窗口写入列表。
func (s *Store) LoadRecentDirectMemoryWrites(context.Context, logicdomain.SessionRef, time.Time, time.Time) ([]logicdomain.TurnAnalysisDirectWrite, error) {
	return []logicdomain.TurnAnalysisDirectWrite{}, nil
}

// AdvanceSessionExtractWindow is kept as a no-op until the DuckDB adapter gains the same exclusion-window columns as SQLite.
// AdvanceSessionExtractWindow 用于在 DuckDB 适配器补齐和 SQLite 同样的排斥窗口字段前，先保持无操作实现。
func (s *Store) AdvanceSessionExtractWindow(context.Context, uint64, time.Time, time.Time) error {
	return nil
}

// ApplyMemoryAdoption reports that unified memory lifecycle write-back is not yet implemented on the DuckDB adapter.
// ApplyMemoryAdoption 用于明确提示：DuckDB 适配器上的统一记忆生命周期回写尚未完成实现。
func (s *Store) ApplyMemoryAdoption(context.Context, logicdomain.SessionRef, []uint64, time.Time) error {
	return fmt.Errorf("duckdb memory adoption is not implemented yet")
}

// LoadMemoryNodesByIDs reports that unified memory-detail lookup is not yet implemented on the DuckDB adapter.
// LoadMemoryNodesByIDs 用于明确提示：DuckDB 适配器上的统一记忆详情查询尚未完成实现。
func (s *Store) LoadMemoryNodesByIDs(context.Context, []uint64) ([]logicdomain.MemoryNodeRecord, error) {
	return nil, fmt.Errorf("duckdb unified memory lookup is not implemented yet")
}

// LoadMemoryNodesByVectorIDs reports that vector-hit enrichment is not yet implemented on the DuckDB adapter.
// LoadMemoryNodesByVectorIDs 用于明确提示：DuckDB 适配器上的向量命中补全尚未完成实现。
func (s *Store) LoadMemoryNodesByVectorIDs(context.Context, []string) ([]logicdomain.MemoryNodeRecord, error) {
	return nil, fmt.Errorf("duckdb unified memory lookup is not implemented yet")
}

// SearchLexicalMemory keeps the SQLite-first hybrid retrieval interface complete while DuckDB still lacks the mirrored FTS table.
// SearchLexicalMemory 用于在 DuckDB 尚未补齐镜像 FTS 表期间，先保持 SQLite-first 混合检索接口完整。
func (s *Store) SearchLexicalMemory(context.Context, string, int, logicdomain.SearchFilter) ([]logicdomain.MemoryLexicalHit, error) {
	return []logicdomain.MemoryLexicalHit{}, nil
}

// FindRecentActiveMemoryByDedupe reports that direct-write soft idempotency is not yet implemented on the DuckDB adapter.
// FindRecentActiveMemoryByDedupe 用于明确提示：DuckDB 适配器上的主动写记忆软幂等尚未完成实现。
func (s *Store) FindRecentActiveMemoryByDedupe(context.Context, logicdomain.SessionRef, int, int, string, time.Time) (logicdomain.MemoryNodeRecord, bool, error) {
	return logicdomain.MemoryNodeRecord{}, false, fmt.Errorf("duckdb direct memory dedupe is not implemented yet")
}

// CreateDirectMemoryNode reports that direct AI-written memory persistence is not yet implemented on the DuckDB adapter.
// CreateDirectMemoryNode 用于明确提示：DuckDB 适配器上的 AI 主动写记忆持久化尚未完成实现。
func (s *Store) CreateDirectMemoryNode(context.Context, logicdomain.SessionRef, logicdomain.MemoryNodeRecord) (logicdomain.MemoryNodeRecord, error) {
	return logicdomain.MemoryNodeRecord{}, fmt.Errorf("duckdb direct memory persistence is not implemented yet")
}
